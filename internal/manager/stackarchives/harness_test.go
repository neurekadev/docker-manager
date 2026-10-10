package stackarchives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/migration/migrationtest"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/streammux/muxtest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The harness runs the archive service against simulated agents: each is
// the real agent migration service over an in-memory Engine and host
// filesystem, reached through an in-memory stream multiplexer like a
// session (as in the migrations package's tests).

type fakeAgent struct {
	env      *migrationtest.Env
	requests map[string]session.RequestHandler
	pipe     *muxtest.Pipe
	online   bool
	features []string
}

type fakeAgents struct {
	t        *testing.T
	mu       sync.Mutex
	envs     map[string]*fakeAgent
	failNext map[string]error
}

func newFakeAgents(t *testing.T) *fakeAgents {
	return &fakeAgents{t: t, envs: map[string]*fakeAgent{}, failNext: map[string]error{}}
}

func (f *fakeAgents) add(id string, e *migrationtest.Env) *fakeAgent {
	streams := map[string]muxtest.Handler{}
	for k, v := range e.Service.Streams() {
		streams[k] = muxtest.Handler(v)
	}
	a := &fakeAgent{env: e, requests: e.Service.Requests(), pipe: muxtest.New(f.t, streams), online: true,
		features: []string{protocol.FeatureMigrationComposeVolume}}
	f.mu.Lock()
	f.envs[id] = a
	f.mu.Unlock()
	return a
}

func (f *fakeAgents) agent(env string) (*fakeAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.envs[env]
	if a == nil || !a.online {
		return nil, jobs.ErrAgentOffline
	}
	return a, nil
}

func (f *fakeAgents) RequestEnvironment(ctx context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a, err := f.agent(env)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	fail := f.failNext[env+"/"+name]
	delete(f.failNext, env+"/"+name)
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	h := a.requests[name]
	if h == nil {
		return nil, fmt.Errorf("unsupported request %s", name)
	}
	raw, _ := json.Marshal(input)
	out, err := h(ctx, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (f *fakeAgents) OpenStream(ctx context.Context, env, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	a, err := f.agent(env)
	if err != nil {
		return nil, err
	}
	return a.pipe.Open(ctx, kind, input, o)
}

func (f *fakeAgents) Online(env string) bool {
	_, err := f.agent(env)
	return err == nil
}

func (f *fakeAgents) EnvironmentServes(env, name string) bool {
	a, err := f.agent(env)
	return err == nil && a.requests[name] != nil
}

func (f *fakeAgents) EnvironmentHasFeature(env, feature string) bool {
	a, err := f.agent(env)
	return err == nil && slices.Contains(a.features, feature)
}

type fakeEnvironments struct{ a *fakeAgents }

func (f fakeEnvironments) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	f.a.mu.Lock()
	defer f.a.mu.Unlock()
	a := f.a.envs[id]
	if a == nil {
		return domain.Environment{}, domain.ErrEnvironmentNotFound
	}
	return domain.Environment{ID: id, Name: id, Online: a.online, Status: domain.EnvironmentActive}, nil
}

// fakeJobs keeps jobs in memory.
type fakeJobs struct {
	mu            sync.Mutex
	jobs          map[string]domain.Job
	subs          map[string][]chan struct{}
	seq           int
	deployOutcome domain.JobState
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{jobs: map[string]domain.Job{}, subs: map[string][]chan struct{}{}, deployOutcome: domain.JobSucceeded}
}

func (f *fakeJobs) newID() string {
	f.seq++
	return fmt.Sprintf("0199aaaa-0000-7000-8000-%012d", f.seq)
}

func (f *fakeJobs) Enqueue(_ context.Context, req jobs.Request) (domain.Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, _ := json.Marshal(req.Input)
	j := domain.Job{ID: f.newID(), Kind: req.Kind, EnvironmentID: req.EnvironmentID, Targets: req.Targets, Input: in,
		State: domain.JobQueued, Origin: domain.OriginManual, InitiatorUserID: req.Principal.UserID}
	f.jobs[j.ID] = j
	return j, true, nil
}

func (f *fakeJobs) put(j domain.Job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[j.ID] = j
	for _, ch := range f.subs[j.ID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeJobs) Get(_ context.Context, id string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return j, domain.ErrJobNotFound
	}
	return j, nil
}

func (f *fakeJobs) List(_ context.Context, flt domain.JobFilter) ([]domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Job
	for _, j := range f.jobs {
		if len(flt.Kinds) > 0 && !slices.Contains(flt.Kinds, j.Kind) {
			continue
		}
		if flt.Target != nil && !slices.ContainsFunc(j.Targets, func(t domain.JobTarget) bool { return t.Type == flt.Target.Type && t.ID == flt.Target.ID }) {
			continue
		}
		out = append(out, j)
	}
	slices.SortFunc(out, func(a, b domain.Job) int {
		switch {
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	return out, nil
}

func (f *fakeJobs) Subscribe(id string) (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{}, 1)
	f.subs[id] = append(f.subs[id], ch)
	return ch, func() {}
}

func (f *fakeJobs) OnFinish(domain.JobKind, jobs.FinishHook)       {}
func (f *fakeJobs) RegisterManagerExecutor(jobexec.Executor) error { return nil }

// fakeStacks keeps stack records in memory.
type fakeStacks struct {
	mu         sync.Mutex
	stacks     map[string]domain.Stack
	jobs       *fakeJobs
	protected  map[string]bool
	recordErr  error
	deployErr  error
	recorded   []string
	forgotten  []string
	deploys    int
	nextID     int
	discovered map[string]bool // running Compose projects by environment/name
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

func (f *fakeStacks) FindByName(_ context.Context, env, name string) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, st := range f.stacks {
		if st.EnvironmentID == env && st.Name == name {
			return st, nil
		}
	}
	return domain.Stack{}, domain.ErrStackNotFound
}

func (f *fakeStacks) Protection(_ context.Context, st domain.Stack) (*protocol.Protection, error) {
	if f.protected[st.ID] {
		return &protocol.Protection{Reason: "docker_manager"}, nil
	}
	return nil, nil
}

func (f *fakeStacks) Deploy(_ context.Context, _ authz.Principal, st domain.Stack, r domain.StackJobRequest, _ domain.StackDeployOptions) (domain.Job, error) {
	f.mu.Lock()
	f.deploys++
	err := f.deployErr
	f.mu.Unlock()
	if err != nil {
		return domain.Job{}, err
	}
	f.jobs.mu.Lock()
	id, outcome := f.jobs.newID(), f.jobs.deployOutcome
	f.jobs.mu.Unlock()
	j := domain.Job{ID: id, Kind: jobspec.StackDeploy, EnvironmentID: st.EnvironmentID, State: outcome, IdempotencyKey: r.IdempotencyKey}
	if outcome != domain.JobSucceeded {
		j.ErrorClass = "dependency_failed"
	}
	f.jobs.put(j)
	return j, nil
}

func (f *fakeStacks) CheckArchiveName(ctx context.Context, env, name, own string) error {
	if st, err := f.FindByName(ctx, env, name); err == nil && st.ID != own {
		return domain.ErrStackNameTaken
	}
	if f.discovered[env+"/"+name] {
		return &domain.StackError{Code: domain.StackErrProjectExists, Message: "running"}
	}
	return nil
}

func (f *fakeStacks) ReserveArchiveStack(ctx context.Context, r domain.StackFromArchive) (domain.Stack, error) {
	if err := f.CheckArchiveName(ctx, r.EnvironmentID, r.Name, ""); err != nil {
		return domain.Stack{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	st := domain.Stack{ID: fmt.Sprintf("st-new-%d", f.nextID), EnvironmentID: r.EnvironmentID, Name: r.Name, DisplayName: r.DisplayName,
		Meta: r.Meta, Root: domain.StackRootStacks, Dir: r.Name, Status: domain.StackUndeployed, ConfigFiles: r.ConfigFiles, EnvFiles: r.EnvFiles}
	f.stacks[st.ID] = st
	return st, nil
}

func (f *fakeStacks) AttachArchiveJob(_ context.Context, id string, j domain.Job) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.stacks[id]
	if !ok {
		return st, domain.ErrStackNotFound
	}
	st.LastJobID = j.ID
	f.stacks[id] = st
	return st, nil
}

func (f *fakeStacks) DropArchiveStack(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.stacks, id)
	return nil
}

func (f *fakeStacks) RecordArchiveStack(_ context.Context, id string, _ authz.Principal) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, id)
	return f.stacks[id], f.recordErr
}

func (f *fakeStacks) ForgetArchiveStack(_ context.Context, _ bun.IDB, id string, _ domain.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.stacks, id)
	f.forgotten = append(f.forgotten, id)
	return nil
}

// allow is an Authorizer granting everything except deny.
type allow struct{ deny map[string]bool }

func (a allow) Can(_ context.Context, _ authz.Principal, capability string, _ authz.Resource) authz.Decision {
	if a.deny[capability] {
		return authz.Deny("denied")
	}
	return authz.Allow("test")
}

type memJournal struct{}

func (memJournal) Save(context.Context, *jobexec.State) error { return nil }

// world is one archive test setup: the "shop" stack runs on srcEnv; dstEnv
// is empty.
type world struct {
	t      *testing.T
	ctx    context.Context
	clk    *clock.Fake
	agents *fakeAgents
	jobs   *fakeJobs
	stacks *fakeStacks
	src    *migrationtest.Env
	dst    *migrationtest.Env
	svc    *Service
	auth   allow
	user   authz.Principal
	dir    string
}

const (
	srcEnv = "env-src"
	dstEnv = "env-dst"
)

func newWorld(t *testing.T, tune ...func(o *Options)) *world {
	t.Helper()
	w := &world{t: t, ctx: testutil.Context(t), clk: testutil.FakeClock(), agents: newFakeAgents(t), jobs: newFakeJobs(),
		auth: allow{deny: map[string]bool{}}, user: authz.Principal{Kind: authz.KindUser, UserID: "u-alice"}, dir: t.TempDir()}
	base := filepath.ToSlash(t.TempDir())
	w.src = migrationtest.NewEnv(t, "src", base+"/src", 10<<30)
	w.dst = migrationtest.NewEnv(t, "dst", base+"/dst", 10<<30)
	ref := migrationtest.SeedShop(w.src)
	w.agents.add(srcEnv, w.src)
	w.agents.add(dstEnv, w.dst)
	w.stacks = &fakeStacks{jobs: w.jobs, protected: map[string]bool{}, discovered: map[string]bool{},
		stacks: map[string]domain.Stack{"st-shop": {ID: "st-shop", EnvironmentID: srcEnv, Name: ref.ProjectName, DisplayName: "Shop",
			Root: ref.Root, Dir: ref.Dir, Status: domain.StackDeployed}}}
	o := Options{Clock: w.clk, Logger: testutil.Logger(t), Agents: w.agents, Environments: fakeEnvironments{w.agents}, Jobs: w.jobs,
		Stacks: w.stacks, Authorizer: w.auth, Dir: w.dir, MaxSize: 1 << 30, FreeBytes: func(string) int64 { return 100 << 30 },
		ReconnectWait: 30 * time.Second}
	for _, fn := range tune {
		fn(&o)
	}
	svc, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	return w
}

func (w *world) stack(id string) domain.Stack {
	st, _ := w.stacks.Get(w.ctx, id)
	return st
}

// run executes a job's attempt to its end and records the outcome on the
// job (as the engine does).
func (w *world) run(j domain.Job) protocol.ResultPayload {
	w.t.Helper()
	x := w.svc.exportExecutor()
	if j.Kind == jobspec.StackImportArchive {
		x = w.svc.importExecutor()
	}
	st := &jobexec.State{JobID: j.ID, Attempt: 1, Kind: j.Kind, Input: j.Input}
	res, err := jobexec.Run(w.ctx, x, st, jobexec.Options{Journal: memJournal{}})
	if err != nil {
		w.t.Fatal(err)
	}
	cur, _ := w.jobs.Get(w.ctx, j.ID)
	cur.State, cur.ErrorClass, cur.ResultOutput = domain.JobState(res.Outcome), res.ErrorClass, st.Output
	w.jobs.put(cur)
	var finish func(context.Context, bun.IDB, domain.Job) error = w.svc.onExportFinished
	if j.Kind == jobspec.StackImportArchive {
		finish = w.svc.onImportFinished
	}
	if err := finish(w.ctx, nil, cur); err != nil {
		w.t.Fatal(err)
	}
	return res
}

func (w *world) running(e *migrationtest.Env, names ...string) bool {
	for _, n := range names {
		c, ok := e.Engine.Container(n)
		if !ok || !c.Details.State.Running {
			return false
		}
	}
	return true
}

// export runs a whole export of the shop stack and returns its file.
func (w *world) export(r ExportRequest) ExportFile {
	w.t.Helper()
	j, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), r)
	if err != nil {
		w.t.Fatal(err)
	}
	if res := w.run(j); res.Outcome != string(domain.JobSucceeded) {
		w.t.Fatalf("export ended %s (%s): %s", res.Outcome, res.ErrorClass, res.Message)
	}
	f, err := w.svc.Export(w.ctx, "st-shop", j.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return f
}

// upload uploads an export's archive as the user.
func (w *world) upload(f ExportFile) Upload {
	w.t.Helper()
	file, err := w.svc.OpenExport(f)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	u, err := w.svc.StoreUpload(w.ctx, w.user, f.Size, file)
	if err != nil {
		w.t.Fatal(err)
	}
	return u
}

// tree is a host directory's entries without times that are not kept.
func tree(e *migrationtest.Env, dir string) map[string]migrationtest.Snapshot {
	out := e.Host.Tree(dir)
	for k, v := range out {
		v.ATime = time.Time{}
		if v.Type == "symlink" {
			// os.Root has no lutimes, and a symlink's mode is not kept.
			v.MTime, v.Mode = time.Time{}, 0
		}
		out[k] = v
	}
	return out
}

func volumeDir(t *testing.T, e *migrationtest.Env, name string) string {
	t.Helper()
	v, err := e.Engine.InspectVolume(context.Background(), name)
	if err != nil {
		t.Fatalf("volume %s: %v", name, err)
	}
	return v.Mountpoint
}

var errBoom = errors.New("boom")

// keys returns a map's keys sorted.
func keys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// engineSpecWithPort is a container publishing a host port.
func engineSpecWithPort(name string, port uint16) engine.ContainerSpec {
	return engine.ContainerSpec{Name: name, Image: "busybox", Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: port, Protocol: "tcp"}}}
}

// container adds a running container to e.
func container(e *migrationtest.Env, name string) {
	e.Engine.AddContainer(engine.ContainerSpec{Name: name, Image: "busybox"}, true)
}
