package updates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	agentstacks "github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/regclient"
	"github.com/neurekadev/dockyard/internal/manager/regclient/regtest"
	"github.com/neurekadev/dockyard/internal/manager/registries"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/updates"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

const env = "env-1"

// The harness runs the real manager pieces (job engine, audit, registry
// connections against the fake OCI registry, scheduler, update service)
// and plays the agent in process: the real agent stack executor and
// compose.* handlers over real definition files in a temporary stacks root
// and the in-memory Engine. Only the Compose SDK's create is scripted.

type ownerGuard struct{}

func (ownerGuard) RequireOwner(context.Context, bool) (string, error) { return "owner", nil }

// fakeStacks is the manager's stack records.
type fakeStacks struct {
	mu     sync.Mutex
	stacks map[string]domain.Stack
}

func (f *fakeStacks) Get(_ context.Context, id string) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.stacks[id]
	if !ok {
		return st, domain.ErrStackNotFound
	}
	return st, nil
}

func (f *fakeStacks) List(_ context.Context, flt domain.StackFilter) ([]domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Stack
	for _, st := range f.stacks {
		if flt.EnvironmentID == "" || st.EnvironmentID == flt.EnvironmentID {
			out = append(out, st)
		}
	}
	slices.SortFunc(out, func(a, b domain.Stack) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *fakeStacks) RecordUpdatedImages(_ context.Context, _ bun.IDB, id string, images []domain.StackImage, after []domain.StackServiceState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.stacks[id]
	for _, img := range images {
		for i := range st.Images {
			if st.Images[i].Service == img.Service {
				st.Images[i] = img
			}
		}
	}
	if after != nil {
		st.EngineServices = after
	}
	f.stacks[id] = st
	return nil
}

func (f *fakeStacks) put(st domain.Stack) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stacks[st.ID] = st
}

// agentComposer loads projects for real; Create recreates the service
// containers whose image changed on the in-memory Engine (Compose's
// divergence rule), without starting them.
type agentComposer struct {
	eng   *enginefake.Engine
	mu    sync.Mutex
	calls []string
}

func (c *agentComposer) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}
func (c *agentComposer) Up(context.Context, *compose.Project, compose.UpOptions) error { return nil }
func (c *agentComposer) Pull(context.Context, *compose.Project, compose.RunOptions) error {
	return nil
}
func (c *agentComposer) Build(context.Context, *compose.Project, compose.BuildOptions) error {
	return nil
}
func (c *agentComposer) Down(context.Context, string, *compose.Project, compose.DownOptions) error {
	return nil
}

func (c *agentComposer) Create(ctx context.Context, p *compose.Project, o compose.CreateOptions) error {
	c.mu.Lock()
	c.calls = append(c.calls, "create:"+strings.Join(o.Services, ","))
	c.mu.Unlock()
	for _, name := range o.Services {
		var svc compose.ServiceInfo
		for _, s := range p.Services {
			if s.Name == name {
				svc = s
			}
		}
		img, err := c.eng.InspectImage(ctx, svc.Image)
		if err != nil {
			return err
		}
		list, err := lifecycle.ProjectContainers(ctx, c.eng, p.Name)
		if err != nil {
			return err
		}
		for _, ct := range list {
			if ct.Labels[lifecycle.ComposeServiceLabel] != name || ct.ImageID == img.ID {
				continue
			}
			if err := c.eng.RemoveContainer(ctx, ct.ID, engine.RemoveOptions{Force: true}); err != nil {
				return err
			}
			if _, _, err := c.eng.CreateContainer(ctx, engine.ContainerSpec{Name: strings.TrimPrefix(ct.Names[0], "/"), Image: svc.Image,
				Labels: ct.Labels}); err != nil {
				return err
			}
		}
	}
	return nil
}

type agentDeps struct {
	c   *agentComposer
	eng *enginefake.Engine
	st  *storage.Result
}

func (d agentDeps) Composer() agentstacks.Composer { return d.c }
func (d agentDeps) Engine() engine.Engine          { return d.eng }
func (d agentDeps) Storage() *storage.Result       { return d.st }

// agentLink routes the manager's agent requests to the agent's handlers.
type agentLink struct {
	mu      sync.Mutex
	offline bool
	agent   *agentstacks.Service
}

func (a *agentLink) RequestEnvironment(ctx context.Context, _, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a.mu.Lock()
	off := a.offline
	a.mu.Unlock()
	if off {
		return nil, jobs.ErrAgentOffline
	}
	h, ok := a.agent.Requests()[name]
	if !ok {
		return nil, errors.New("no handler " + name)
	}
	b, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := h(ctx, b)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type environments struct{ link *agentLink }

func (e environments) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	e.link.mu.Lock()
	defer e.link.mu.Unlock()
	return domain.Environment{ID: id, Status: domain.EnvironmentActive, Online: !e.link.offline}, nil
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	db     *bun.DB
	clk    *clock.Fake
	disp   *jobstest.Dispatcher
	audit  *audit.Log
	eng    *jobs.Engine
	regs   *registries.Service
	reg    *regtest.Registry
	sched  *scheduler.Service
	svc    *updates.Service
	stacks *fakestacks
	link   *agentLink
	agent  *agentstacks.Service
	engine *enginefake.Engine
	comp   *agentComposer
	res    *fakeResources
	root   string
}

type fakestacks = fakeStacks

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, db: db, clk: clock.NewFake(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)), disp: jobstest.New(),
		stacks: &fakeStacks{stacks: map[string]domain.Stack{}}, root: filepath.Join(dir, "stacks")}
	log := testutil.Logger(t)
	if h.audit, err = audit.New(audit.Options{DB: db, Clock: h.clk, Logger: log}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h.reg = regtest.New(t, regtest.AuthBearer, "robot", "registry-password-1")
	client := regclient.New(regclient.Options{HTTP: h.reg.Client(), Clock: h.clk, Logger: log, Jitter: func() float64 { return 0 }})
	if h.regs, err = registries.New(registries.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: h.clk, Logger: log,
		Guard: ownerGuard{}, Audit: h.audit, Client: client}); err != nil {
		t.Fatal(err)
	}
	if h.eng, err = jobs.New(jobs.Options{DB: db, Clock: h.clk, Logger: log, Dispatcher: h.disp, Audit: h.audit,
		CommandSecrets: func(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
			regs, err := h.regs.CommandSecrets(ctx, j)
			if err != nil || len(regs) == 0 {
				return nil, err
			}
			return &protocol.CommandSecrets{Registries: regs}, nil
		}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.eng.Close)
	// The agent.
	if err := os.MkdirAll(h.root, 0o750); err != nil {
		t.Fatal(err)
	}
	h.engine = enginefake.New("engine-1")
	h.comp = &agentComposer{eng: h.engine}
	res := &storage.Result{StacksDir: filepath.ToSlash(h.root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(h.root), OK: true}}}
	h.agent = agentstacks.New(agentstacks.Options{Deps: agentDeps{c: h.comp, eng: h.engine, st: res}, Clock: h.clk, Logger: log,
		WaitTimeout: time.Second})
	h.link = &agentLink{agent: h.agent}
	if h.svc, err = updates.New(updates.Options{DB: db, Clock: h.clk, Logger: log, Jobs: h.eng, Stacks: h.stacks, Registries: h.regs,
		Agents: h.link, Environments: environments{h.link}, Audit: h.audit}); err != nil {
		t.Fatal(err)
	}
	if h.sched, err = scheduler.New(scheduler.Options{DB: db, Clock: h.clk, Logger: log, Jobs: h.eng, Audit: h.audit}); err != nil {
		t.Fatal(err)
	}
	h.res = &fakeResources{eng: h.engine, specs: map[string]protocol.ContainerSpec{}}
	h.svc.SetResources(h.res)
	if err := h.svc.Register(h.sched); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.disp.Connect(env)
	return h
}

// ref is a reference on the fake registry.
func (h *harness) ref(repoTag string) string { return h.reg.Host() + "/" + repoTag }

// publish serves a new single-platform manifest for repo:tag in the
// registry and makes the Engine's pull of the tag return it; it returns
// the digest.
func (h *harness) publish(repo, tag, salt string) string {
	d := h.reg.Put(repo, tag, regclient.MediaOCIManifest, []byte(regtest.ManifestBody+salt))
	h.engine.Publish(h.ref(repo+":"+tag), d)
	return d
}

// stackService describes a service of the fixture stack.
type stackService struct {
	name, image string
	deps        []lifecycle.Dependency
	running     bool
}

// deployStack writes the project files, pulls and runs the services on
// the Engine and records the stack as DockYard deployed it (applied
// revision = the files' hash, applied images with their digests).
func (h *harness) deployStack(id, name, yaml string, extra map[string]string, services []stackService) domain.Stack {
	h.t.Helper()
	dir := filepath.Join(h.root, name)
	files := map[string]string{"compose.yaml": yaml}
	for k, v := range extra {
		files[k] = v
	}
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	st := domain.Stack{ID: id, EnvironmentID: env, Name: name, Root: domain.StackRootStacks, Dir: name, Status: domain.StackDeployed}
	for _, s := range services {
		if _, err := h.engine.PullImage(h.ctx, s.image, engine.PullOptions{}); err != nil {
			h.t.Fatal(err)
		}
		cid := h.engine.AddContainer(engine.ContainerSpec{Name: name + "-" + s.name + "-1", Image: s.image, Labels: map[string]string{
			lifecycle.ComposeProjectLabel: name, lifecycle.ComposeServiceLabel: s.name, lifecycle.DependsOnLabel: lifecycle.FormatDependsOn(s.deps)}}, false)
		if s.running {
			if err := h.engine.StartContainer(h.ctx, cid); err != nil {
				h.t.Fatal(err)
			}
		}
		c, _ := h.engine.Container(cid)
		digests := h.engine.ImageDigests(c.Details.ImageID)
		d := ""
		if len(digests) > 0 {
			d = digests[0][strings.Index(digests[0], "@")+1:]
		}
		def := domain.StackServiceDef{Name: s.name, Image: s.image}
		for _, dep := range s.deps {
			def.DependsOn = append(def.DependsOn, domain.StackDependency{Service: dep.Service, Condition: dep.Condition, Required: dep.Required, Restart: dep.Restart})
		}
		st.Services = append(st.Services, def)
		st.Images = append(st.Images, domain.StackImage{Service: s.name, Image: s.image, ImageID: c.Details.ImageID, Digest: d, Platform: "linux/amd64"})
		running := 0
		if s.running {
			running = 1
		}
		st.EngineServices = append(st.EngineServices, domain.StackServiceState{Service: s.name, Containers: 1, Running: running})
	}
	out, err := h.link.RequestEnvironment(h.ctx, env, protocol.ReqComposeRead, protocol.ComposeReadInput{Stack: protocol.ProjectRef{
		Root: protocol.RootStacks, Dir: name, ProjectName: name}}, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	var read protocol.ComposeReadOutput
	if err := json.Unmarshal(out, &read); err != nil {
		h.t.Fatal(err)
	}
	st.Applied = &domain.RevisionRef{ID: "rev-" + id, Seq: 1, Hash: read.Snapshot.Hash}
	st.Observed = st.Applied
	h.stacks.put(st)
	return st
}

// snapshot returns the bytes of every file under the stack's directory.
func (h *harness) snapshot(name string) map[string][]byte {
	h.t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(filepath.Join(h.root, name), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = b
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) assertSame(before, after map[string][]byte) {
	h.t.Helper()
	if len(before) != len(after) {
		h.t.Errorf("files before %d after %d", len(before), len(after))
	}
	for p, b := range before {
		if !bytes.Equal(after[p], b) {
			h.t.Errorf("%s changed", filepath.Base(p))
		}
	}
}

// policy creates a policy with the given schedules' enabled flags.
func (h *harness) policy(np updates.NewPolicy) domain.UpdatePolicy {
	h.t.Helper()
	if np.EnvironmentID == "" {
		np.EnvironmentID = env
	}
	if np.Name == "" {
		np.Name = "policy " + np.TargetID
	}
	p, err := h.svc.Create(h.ctx, np)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

// check runs a manual update.check through the job engine (manager
// executor) and returns the finished job.
func (h *harness) check(p domain.UpdatePolicy) domain.Job {
	h.t.Helper()
	j, err := h.svc.StartCheck(h.ctx, authz.Service(), p.ID, "")
	if err != nil {
		h.t.Fatal(err)
	}
	h.dispatch()
	h.eng.Wait()
	got, err := h.eng.Get(h.ctx, j.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

func (h *harness) dispatch() {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

// playAgent executes every command sent to the environment with the real
// agent executor and reports the results to the engine.
func (h *harness) playAgent() {
	h.t.Helper()
	execs := map[domain.JobKind]jobexec.Executor{}
	for _, x := range h.agent.Executors() {
		execs[x.Kind] = x
	}
	for _, f := range h.disp.Drain(env) {
		if f.Type != protocol.TypeCommand {
			continue
		}
		cmd, err := protocol.DecodePayload[protocol.CommandPayload](f)
		if err != nil {
			h.t.Fatal(err)
		}
		st := &jobexec.State{JobID: f.JobID, Attempt: f.Attempt, Kind: domain.JobKind(cmd.Kind), Input: cmd.Input, Secrets: cmd.Secrets}
		res, err := jobexec.Run(h.ctx, execs[domain.JobKind(cmd.Kind)], st, jobexec.Options{Journal: memJournal{}})
		if err != nil {
			h.t.Fatal(err)
		}
		ack, err := protocol.NewFrame(protocol.TypeAck, "ack-"+f.ID, f.ID, f.Ref(), protocol.AckPayload{Accepted: true})
		if err != nil {
			h.t.Fatal(err)
		}
		result, err := protocol.NewFrame(protocol.TypeResult, "res-"+f.ID, f.ID, f.Ref(), res)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, fr := range []*protocol.Frame{ack, result} {
			if _, err := h.eng.HandleAgentFrame(h.ctx, env, fr); err != nil {
				h.t.Fatal(err)
			}
		}
	}
}

type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

// run enqueues a manual run, lets the agent execute it and returns the
// finished job.
func (h *harness) run(p domain.UpdatePolicy) domain.Job {
	h.t.Helper()
	j, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	return h.finish(j)
}

func (h *harness) finish(j domain.Job) domain.Job {
	h.t.Helper()
	h.dispatch()
	h.playAgent()
	got, err := h.eng.Get(h.ctx, j.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	if !got.State.Terminal() {
		h.t.Fatalf("job %s is %s", got.ID, got.State)
	}
	return got
}

func (h *harness) candidates(p domain.UpdatePolicy) map[string]domain.UpdateCandidate {
	h.t.Helper()
	cs, err := h.svc.Candidates(h.ctx, p.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]domain.UpdateCandidate{}
	for _, c := range cs {
		out[c.Service] = c
	}
	return out
}

// updateRuns counts the update.run jobs.
func (h *harness) updateRuns() int {
	h.t.Helper()
	js, err := h.eng.List(h.ctx, domain.JobFilter{Kinds: []domain.JobKind{jobspec.UpdateRun}, Limit: 100})
	if err != nil {
		h.t.Fatal(err)
	}
	return len(js)
}

func pulls(e *enginefake.Engine) int {
	n := 0
	for _, c := range e.Calls() {
		if c == "image.pull" {
			n++
		}
	}
	return n
}
