package stacks_test

import (
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

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	agentstacks "code.neureka.dev/docker-manager/docker-manager/internal/agent/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/agents"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// The harness runs the manager's stack service against the real agent-side
// stack service (internal/agent/stacks) in process: requests go straight to
// the agent's handlers over a temporary stacks root, and stack jobs run
// through the real job engine and the agent's executors with a scripted
// Compose SDK and Engine (no Docker).

const env = "env-1"

type allowAll struct{}

func (allowAll) Can(context.Context, authz.Principal, string, authz.Resource) authz.Decision {
	return authz.Allow("test")
}

// fakeComposer loads projects for real and scripts the SDK operations.
type fakeComposer struct {
	mu    sync.Mutex
	calls []string
	upErr error
	eng   *fakeEngine
	// Builds: the options of every Build call; buildLog lines are emitted
	// as BuildKit output of each build service; blockBuild makes the build
	// wait until it is cancelled (started is signaled first).
	builds     []compose.BuildOptions
	buildLog   []string
	blockBuild bool
	started    chan struct{}
}

func (f *fakeComposer) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

func (f *fakeComposer) Up(_ context.Context, p *compose.Project, _ compose.UpOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, "up:"+p.Name)
	err := f.upErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	// The project's services now run, with their images.
	var cs []engine.Container
	for _, s := range p.Services {
		cs = append(cs, engine.Container{ID: p.Name + "-" + s.Name, Names: []string{"/" + p.Name + "-" + s.Name + "-1"}, Image: s.Image,
			ImageID: "sha256:" + s.Name, State: "running", Labels: map[string]string{lifecycle.ComposeProjectLabel: p.Name,
				lifecycle.ComposeServiceLabel: s.Name, "com.docker.compose.project.working_dir": filepath.ToSlash(p.Dir),
				"com.docker.compose.project.config_files": strings.Join(slashAll(p.ConfigFiles), ",")}})
	}
	f.eng.setProject(p.Name, cs)
	return nil
}

func slashAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, filepath.ToSlash(s))
	}
	return out
}

func (f *fakeComposer) Pull(context.Context, *compose.Project, compose.RunOptions) error { return nil }
func (f *fakeComposer) Build(ctx context.Context, p *compose.Project, o compose.BuildOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, "build:"+p.Name)
	f.builds = append(f.builds, o)
	logs, block, started := f.buildLog, f.blockBuild, f.started
	f.mu.Unlock()
	for _, s := range p.Services {
		if !s.Build || (len(o.Services) > 0 && !slices.Contains(o.Services, s.Name)) {
			continue
		}
		if o.BuildEvents != nil {
			for _, l := range logs {
				o.BuildEvents(s.Image, engine.BuildEvent{Step: "[2/2] RUN make", Status: "log", Log: []byte(l + "\n")})
			}
		}
		if block {
			if started != nil {
				started <- struct{}{}
			}
			<-ctx.Done()
			return engine.Errorf("image.build", engine.CodeCanceled, "context canceled")
		}
		if o.Built != nil {
			o.Built(compose.BuiltImage{Service: s.Name, Image: s.Image, ImageID: "sha256:built-" + s.Name})
		}
	}
	return nil
}

func (f *fakeComposer) Create(_ context.Context, p *compose.Project, o compose.CreateOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create:"+p.Name+":"+strings.Join(o.Services, ","))
	return nil
}

func (f *fakeComposer) Builds() []compose.BuildOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]compose.BuildOptions(nil), f.builds...)
}

func (f *fakeComposer) Down(_ context.Context, name string, _ *compose.Project, _ compose.DownOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, "down:"+name)
	f.mu.Unlock()
	f.eng.setProject(name, nil)
	return nil
}

func (f *fakeComposer) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeEngine holds containers per project.
type fakeEngine struct {
	engine.Engine
	mu       sync.Mutex
	projects map[string][]engine.Container
}

func (f *fakeEngine) setProject(name string, cs []engine.Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[name] = cs
}

func (f *fakeEngine) all() []engine.Container {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []engine.Container
	for _, cs := range f.projects {
		out = append(out, cs...)
	}
	return out
}

func (f *fakeEngine) ListContainers(_ context.Context, flt engine.ContainerFilter) ([]engine.Container, error) {
	var out []engine.Container
	for _, c := range f.all() {
		ok := true
		for _, l := range flt.Labels {
			k, v, hasV := strings.Cut(l, "=")
			got, has := c.Labels[k]
			if !has || (hasV && got != v) {
				ok = false
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, error) {
	for _, c := range f.all() {
		if c.ID == id {
			return engine.ContainerDetails{ID: c.ID, Name: strings.TrimPrefix(c.Names[0], "/"), Image: c.Image, ImageID: c.ImageID,
				State: engine.ContainerState{Status: c.State, Running: c.State == "running"}, RestartPolicy: "unless-stopped"}, nil
		}
	}
	return engine.ContainerDetails{}, engine.Errorf("container.inspect", engine.CodeNotFound, "no such container")
}

func (f *fakeEngine) InspectImage(_ context.Context, ref string) (engine.ImageDetails, error) {
	return engine.ImageDetails{ID: ref, OS: "linux", Architecture: "amd64"}, nil
}

func (f *fakeEngine) StartContainer(context.Context, string) error { return nil }
func (f *fakeEngine) StopContainer(_ context.Context, id string, _ *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cs := range f.projects {
		for i := range cs {
			if cs[i].ID == id {
				cs[i].State = "exited"
			}
		}
	}
	return nil
}

type agentDeps struct {
	c   *fakeComposer
	eng *fakeEngine
	st  *storage.Result
}

func (d agentDeps) Composer() agentstacks.Composer { return d.c }
func (d agentDeps) Engine() engine.Engine          { return d.eng }
func (d agentDeps) Storage() *storage.Result       { return d.st }

// fakeAgents routes requests to the agent's handlers (encoding them like
// the session does) while online.
type fakeAgents struct {
	mu       sync.Mutex
	online   bool
	handlers map[string]session.RequestHandler
	requests []string
	// features are the capabilities features the agent announces.
	features map[string]bool
}

func (f *fakeAgents) EnvironmentHasFeature(environmentID, feature string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online && environmentID == env && f.features[feature]
}

func (f *fakeAgents) RequestEnvironment(ctx context.Context, environmentID, name string, input any, _ time.Duration) (json.RawMessage, error) {
	f.mu.Lock()
	online := f.online && environmentID == env
	f.requests = append(f.requests, name)
	f.mu.Unlock()
	if !online {
		return nil, jobs.ErrAgentOffline
	}
	h := f.handlers[name]
	if h == nil {
		return nil, &agents.RequestError{Code: protocol.CodeUnsupportedRequest, Message: name}
	}
	in, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := h(ctx, in)
	if err != nil {
		var he *session.HandlerError
		if errors.As(err, &he) {
			return nil, &agents.RequestError{Code: he.Code, Message: he.Message}
		}
		return nil, err
	}
	return json.Marshal(out)
}

func (f *fakeAgents) setOnline(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = on
}

func (f *fakeAgents) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

type fakeEnvironments struct{ agents *fakeAgents }

func (f fakeEnvironments) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if id != env {
		return domain.Environment{}, domain.ErrEnvironmentNotFound
	}
	f.agents.mu.Lock()
	defer f.agents.mu.Unlock()
	return domain.Environment{ID: env, Name: "nas", Status: domain.EnvironmentActive, Online: f.agents.online}, nil
}

// fakeRegistries selects connections by registry host prefix.
type fakeRegistries struct {
	byHost map[string]string // reference prefix -> connection ID
	err    error
	// build: the host-wide connections offered to builds; usable: the
	// connections that exist and are active.
	build  []string
	usable map[string]bool
}

func (f *fakeRegistries) BuildCredentials(context.Context, string) ([]string, []string, error) {
	return append([]string(nil), f.build...), nil, nil
}

func (f *fakeRegistries) Usable(_ context.Context, ids []string) error {
	for _, id := range ids {
		if !f.usable[id] {
			return domain.ErrRegistryConnectionNotFound
		}
	}
	return nil
}

func (f *fakeRegistries) Select(_ context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error) {
	if f.err != nil {
		return domain.RegistrySelection{}, f.err
	}
	for prefix, id := range f.byHost {
		if strings.HasPrefix(req.Reference, prefix) {
			return domain.RegistrySelection{Reference: req.Reference, Selected: &domain.RegistryConnection{ID: id}}, nil
		}
	}
	return domain.RegistrySelection{Reference: req.Reference}, nil
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	db     *bun.DB
	clk    *clock.Fake
	root   string
	disp   *jobstest.Dispatcher
	eng    *jobs.Engine
	svc    *stacks.Service
	agents *fakeAgents
	agent  *agentstacks.Service
	regs   *fakeRegistries
	comp   *fakeComposer
	engine *fakeEngine
	bus    *events.Bus
}

var alice = authz.Principal{Kind: authz.KindUser, UserID: "alice"}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ctx: testutil.Context(t), db: storetest.Migrated(t), clk: testutil.FakeClock(), root: t.TempDir(), disp: jobstest.New()}
	now := h.clk.Now()
	if err := store.InsertEnvironment(h.ctx, h.db, &domain.Environment{ID: env, Name: "nas", Status: domain.EnvironmentActive,
		Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	h.engine = &fakeEngine{projects: map[string][]engine.Container{}}
	h.comp = &fakeComposer{eng: h.engine}
	res := &storage.Result{StacksDir: filepath.ToSlash(h.root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(h.root), OK: true}}}
	h.agent = agentstacks.New(agentstacks.Options{Deps: agentDeps{c: h.comp, eng: h.engine, st: res}, Clock: h.clk, Logger: testutil.Logger(t)})
	h.agents = &fakeAgents{online: true, handlers: h.agent.Requests()}
	h.disp.Connect(env)
	var err error
	h.regs = &fakeRegistries{byHost: map[string]string{}}
	cmdSecrets := func(_ context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
		refs, err := jobspec.CredentialRefsOf(j.Input)
		if err != nil || len(refs.RegistryConnections) == 0 {
			return nil, err
		}
		s := &protocol.CommandSecrets{}
		for _, id := range refs.RegistryConnections {
			s.Registries = append(s.Registries, protocol.RegistryCredential{ConnectionID: id, Host: "registry.example:5000", Username: "bot", Secret: "pw-" + id})
		}
		return s, nil
	}
	if h.eng, err = jobs.New(jobs.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(t), Dispatcher: h.disp, Authorizer: allowAll{},
		CommandSecrets: cmdSecrets}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.eng.Close)
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h.bus = events.New(h.clk)
	if h.svc, err = stacks.New(stacks.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(t), Keyring: secrets.NewKeyring(key),
		Agents: h.agents, Environments: fakeEnvironments{h.agents}, Jobs: h.eng, Bus: h.bus, Registries: h.regs}); err != nil {
		t.Fatal(err)
	}
	return h
}

func files(compose, env string) []domain.StackFile {
	out := []domain.StackFile{{Path: "compose.yaml", Content: []byte(compose)}}
	if env != "" {
		out = append(out, domain.StackFile{Path: ".env", Content: []byte(env)})
	}
	return out
}

func (h *harness) create(name, composeYAML, envFile string) domain.Stack {
	h.t.Helper()
	st, v, err := h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: name,
		Files: files(composeYAML, envFile)}})
	if err != nil {
		h.t.Fatalf("create %s: %v (validation %+v)", name, err, v)
	}
	return st
}

func (h *harness) get(id string) domain.Stack {
	h.t.Helper()
	st, err := h.svc.Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) path(parts ...string) string {
	return filepath.Join(append([]string{h.root}, parts...)...)
}

func (h *harness) read(parts ...string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.path(parts...))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *harness) write(content string, parts ...string) {
	h.t.Helper()
	p := h.path(parts...)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// deploy enqueues a deploy of st.
func (h *harness) deploy(st domain.Stack) domain.Job {
	h.t.Helper()
	j, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	return j
}

// run dispatches queued jobs and runs every command with the agent's
// executors, returning the finished jobs' IDs in order.
func (h *harness) run() []string {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	var done []string
	for _, f := range h.disp.Drain(env) {
		if f.Type != protocol.TypeCommand {
			continue
		}
		cmd, err := protocol.DecodePayload[protocol.CommandPayload](f)
		if err != nil {
			h.t.Fatal(err)
		}
		h.frame(protocol.TypeAck, f, protocol.AckPayload{Accepted: true})
		var exec jobexec.Executor
		for _, x := range h.agent.Executors() {
			if string(x.Kind) == cmd.Kind {
				exec = x
			}
		}
		st := &jobexec.State{JobID: f.JobID, Attempt: f.Attempt, FencingToken: f.FencingToken, Kind: exec.Kind, Input: cmd.Input,
			Secrets: cmd.Secrets}
		res, err := jobexec.Run(h.ctx, exec, st, jobexec.Options{Journal: nopJournal{}})
		if err != nil {
			h.t.Fatal(err)
		}
		h.frame(protocol.TypeResult, f, res)
		done = append(done, f.JobID)
	}
	return done
}

func (h *harness) frame(typ protocol.Type, cmd *protocol.Frame, payload any) {
	h.t.Helper()
	f, err := protocol.NewFrame(typ, string(typ)+"-"+cmd.ID, cmd.ID, cmd.Ref(), payload)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.eng.HandleAgentFrame(h.ctx, env, f); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) job(id string) domain.Job {
	h.t.Helper()
	j, err := h.eng.Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return j
}

type nopJournal struct{}

func (nopJournal) Save(context.Context, *jobexec.State) error { return nil }

func stackErrCode(err error) string {
	var se *domain.StackError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}
