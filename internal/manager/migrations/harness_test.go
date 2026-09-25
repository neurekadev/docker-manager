package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/migration/migrationtest"
	"github.com/neurekadev/dockyard/internal/agent/session"
	agentstacks "github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/store/storetest"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
	"github.com/neurekadev/dockyard/internal/streammux/muxtest"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// The harness runs the manager's migration service against two simulated
// agents: each is the real agent migration service (and the stack
// service's compose.services) over an in-memory Engine and host
// filesystem, reached through an in-memory stream multiplexer like a
// session. Agents can be killed (their session ends mid-stream) and
// revived.

type fakeAgent struct {
	env      *migrationtest.Env
	requests map[string]session.RequestHandler
	streams  map[string]muxtest.Handler
	pipe     *muxtest.Pipe
	online   bool
	plain    bool
}

type fakeAgents struct {
	t    *testing.T
	mu   sync.Mutex
	envs map[string]*fakeAgent
	// calls records request names per environment.
	calls map[string][]string
	// failNext makes the next request of a name on an environment fail.
	failNext map[string]error
	// onStream lets a test replace a stream handler (fault injection).
	onOpen func(env, kind string)
}

type stackDeps struct{ e *migrationtest.Env }

func (d stackDeps) Composer() agentstacks.Composer { return nil }
func (d stackDeps) Engine() engine.Engine {
	return migrationtest.HostEngine{Engine: d.e.Engine, Host: d.e.Host}
}
func (d stackDeps) Storage() *storage.Result { s := *d.e.Storage; return &s }

func newFakeAgents(t *testing.T) *fakeAgents {
	return &fakeAgents{t: t, envs: map[string]*fakeAgent{}, calls: map[string][]string{}, failNext: map[string]error{}}
}

func (f *fakeAgents) add(id string, e *migrationtest.Env) *fakeAgent {
	reqs := e.Service.Requests()
	maps.Copy(reqs, agentstacks.New(agentstacks.Options{Deps: stackDeps{e}, Logger: testutil.Logger(f.t)}).Requests())
	streams := map[string]muxtest.Handler{}
	for k, v := range e.Service.Streams() {
		streams[k] = muxtest.Handler(v)
	}
	a := &fakeAgent{env: e, requests: reqs, streams: streams, online: true}
	a.pipe = muxtest.New(f.t, streams)
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
	f.calls[env] = append(f.calls[env], name)
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
	if f.onOpen != nil {
		f.onOpen(env, kind)
	}
	return a.pipe.Open(ctx, kind, input, o)
}

func (f *fakeAgents) Online(env string) bool {
	_, err := f.agent(env)
	return err == nil
}

// kill ends an agent's session (open streams fail) and takes it offline.
func (f *fakeAgents) kill(env string) {
	f.mu.Lock()
	a := f.envs[env]
	a.online = false
	f.mu.Unlock()
	a.pipe.Close()
}

// revive reconnects an agent with a new session.
func (f *fakeAgents) revive(env string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.envs[env]
	a.pipe = muxtest.New(f.t, a.streams)
	a.online = true
}

func (f *fakeAgents) called(env, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls[env] {
		if c == name {
			n++
		}
	}
	return n
}

// fakeEnvironments serves environments and their capabilities.
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

func (f fakeEnvironments) EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error) {
	env, err := f.GetEnvironment(ctx, id)
	if err != nil {
		return domain.EnvironmentSystem{}, err
	}
	f.a.mu.Lock()
	a := f.a.envs[id]
	f.a.mu.Unlock()
	c := protocol.CapabilitiesPayload{Requests: slices.Sorted(maps.Keys(a.requests)), Streams: slices.Sorted(maps.Keys(a.streams)),
		Transport: protocol.TransportInfo{ManagerURL: "https://dockyard.example", PlainHTTP: a.plain}}
	b, _ := json.Marshal(c)
	return domain.EnvironmentSystem{Environment: env, Agent: &domain.Agent{ID: "agent-" + id, EnvironmentID: id, Capabilities: string(b)}}, nil
}

// fakeJobs holds the migration job and the destination deploy jobs.
type fakeJobs struct {
	mu       sync.Mutex
	jobs     map[string]domain.Job
	subs     map[string][]chan struct{}
	enqueued []jobs.Request
	// deployOutcome is the state the destination deploy ends in.
	deployOutcome domain.JobState
	onDeploy      func(st domain.Stack)
	cancelled     []string
	seq           int
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
	f.enqueued = append(f.enqueued, req)
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

func (f *fakeJobs) Subscribe(id string) (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{}, 1)
	f.subs[id] = append(f.subs[id], ch)
	return ch, func() {}
}

func (f *fakeJobs) OnFinish(domain.JobKind, jobs.FinishHook)       {}
func (f *fakeJobs) RegisterManagerExecutor(jobexec.Executor) error { return nil }
func (f *fakeJobs) Cancel(_ context.Context, id string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	j := f.jobs[id]
	j.State = domain.JobCancelled
	f.jobs[id] = j
	return j, nil
}

// fakeStacks keeps stack records in memory.
type fakeStacks struct {
	mu        sync.Mutex
	stacks    map[string]domain.Stack
	jobs      *fakeJobs
	deploys   []authz.Principal
	placeErr  error
	published []string
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

func (f *fakeStacks) Place(_ context.Context, _ bun.IDB, id string, p domain.StackPlacement) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.placeErr != nil {
		return domain.Stack{}, f.placeErr
	}
	st := f.stacks[id]
	p.Apply(&st)
	st.Revision++
	f.stacks[id] = st
	return st, nil
}

func (f *fakeStacks) Published(st domain.Stack, attrs map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, attrs["change"])
}

// Deploy starts a destination deploy job that ends with the configured
// outcome (after running onDeploy, e.g. starting containers).
func (f *fakeStacks) Deploy(_ context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, _ domain.StackDeployOptions) (domain.Job, error) {
	f.mu.Lock()
	f.deploys = append(f.deploys, p)
	f.mu.Unlock()
	f.jobs.mu.Lock()
	id := f.jobs.newID()
	outcome, hook := f.jobs.deployOutcome, f.jobs.onDeploy
	f.jobs.mu.Unlock()
	j := domain.Job{ID: id, Kind: jobspec.StackDeploy, EnvironmentID: st.EnvironmentID, State: domain.JobRunning, IdempotencyKey: r.IdempotencyKey}
	f.jobs.put(j)
	// The goroutine finishes its own copy: the returned value must not be
	// written concurrently with the caller reading it.
	done := j
	go func() {
		if hook != nil {
			hook(st)
		}
		done.State = outcome
		if outcome != domain.JobSucceeded {
			done.ErrorClass = "dependency_failed"
		}
		f.jobs.put(done)
	}()
	return j, nil
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

// world is one migration test setup.
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
}

const (
	srcEnv = "env-src"
	dstEnv = "env-dst"
)

func newWorld(t *testing.T, tune ...func(o *Options)) *world {
	t.Helper()
	w := &world{t: t, ctx: testutil.Context(t), clk: testutil.FakeClock(), agents: newFakeAgents(t), jobs: newFakeJobs(),
		auth: allow{deny: map[string]bool{}}, user: authz.Principal{Kind: authz.KindUser, UserID: "u-alice"}}
	base := filepath.ToSlash(t.TempDir())
	w.src = migrationtest.NewEnv(t, "src", base+"/src", 10<<30)
	w.dst = migrationtest.NewEnv(t, "dst", base+"/dst", 10<<30)
	ref := migrationtest.SeedShop(w.src)
	w.agents.add(srcEnv, w.src)
	w.agents.add(dstEnv, w.dst)
	w.stacks = &fakeStacks{jobs: w.jobs, stacks: map[string]domain.Stack{"st-shop": {ID: "st-shop", EnvironmentID: srcEnv, Name: ref.ProjectName,
		Root: ref.Root, Dir: ref.Dir, Status: domain.StackDeployed, EngineState: domain.EngineStateRunning}}}
	o := Options{DB: storetest.Migrated(t), Clock: w.clk, Logger: testutil.Logger(t), Agents: w.agents,
		Environments: fakeEnvironments{w.agents}, Jobs: w.jobs, Stacks: w.stacks, Authorizer: w.auth,
		ReconnectWait: 30 * time.Second}
	for _, fn := range tune {
		fn(&o)
	}
	svc, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	// The destination deploy starts the stack's containers there.
	w.jobs.onDeploy = func(st domain.Stack) {
		for _, name := range []string{"shop-db-1", "shop-web-1"} {
			w.dst.Engine.AddContainer(engine.ContainerSpec{Name: name, Image: "postgres:17", Labels: map[string]string{
				protocol.ComposeProjectLabel: "shop", protocol.ComposeWorkingDirLabel: w.dst.ProjectDir(st.Dir)}}, true)
		}
	}
	return w
}

func (w *world) stack() domain.Stack {
	st, _ := w.stacks.Get(w.ctx, "st-shop")
	return st
}

// start previews and enqueues a stack migration and returns the job.
func (w *world) start(sel Selection) domain.Job {
	w.t.Helper()
	j, m, err := w.svc.StartStack(w.ctx, w.user, w.stack(), StackRequest{TargetEnvironmentID: dstEnv, Selection: sel})
	if err != nil {
		w.t.Fatal(err)
	}
	if m.ID != j.ID || m.State != domain.MigrationRunning {
		w.t.Fatalf("record %+v", m)
	}
	return j
}

// run executes the job's attempt to its end (the manager stays up).
func (w *world) run(j domain.Job, wrap func(x *jobexec.Executor)) (*jobexec.State, jobexec.Options, error) {
	w.t.Helper()
	x := w.svc.stackExecutor()
	if j.Kind == jobspec.VolumeMigrate {
		x = w.svc.volumeExecutor()
	}
	if wrap != nil {
		wrap(&x)
	}
	st := &jobexec.State{JobID: j.ID, Attempt: 1, Kind: j.Kind, Input: j.Input}
	o := jobexec.Options{Journal: memJournal{}}
	res, err := jobexec.Run(w.ctx, x, st, o)
	if err == nil {
		st.Outcome = &res
	}
	return st, o, err
}

func (w *world) record(id string) domain.Migration {
	w.t.Helper()
	m, err := w.svc.Get(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return m
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

func (w *world) stopped(e *migrationtest.Env, names ...string) bool {
	for _, n := range names {
		c, ok := e.Engine.Container(n)
		if !ok || c.Details.State.Running {
			return false
		}
	}
	return true
}
