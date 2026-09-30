package managermove

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	envmigrations "github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const (
	publicURL = "https://docker.example.com"
	ownerID   = "owner-1"
)

// fakeGuard answers owner checks.
type fakeGuard struct{ err error }

func (g *fakeGuard) RequireOwner(context.Context, bool) (string, error) {
	if g.err != nil {
		return "", g.err
	}
	return ownerID, nil
}

// memAudit collects non-request audit events.
type memAudit struct {
	mu  sync.Mutex
	evs []domain.AuditEvent
}

func (m *memAudit) Record(_ context.Context, ev domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, ev)
	return nil
}

func (m *memAudit) actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.evs {
		out = append(out, e.Action)
	}
	return out
}

// fakeEnrollments is the agents service's enrollment side: the fixture
// enrolls an agent by hand (enrollNewServer).
type fakeEnrollments struct {
	mu      sync.Mutex
	f       *fixture
	byID    map[string]domain.Enrollment
	agents  map[string]domain.Agent
	specs   []domain.EnrollmentSpec
	revoked []string
}

func (e *fakeEnrollments) CreateEnrollment(_ context.Context, r domain.EnrollmentSpec) (domain.CreatedEnrollment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.f.clk.Now().UTC()
	en := domain.Enrollment{ID: ids.New(), Intent: r.Intent, EnvironmentName: r.EnvironmentName, CreatedBy: r.CreatedBy, CreatedAt: now,
		ExpiresAt: now.Add(r.TTL)}
	e.byID[en.ID] = en
	e.specs = append(e.specs, r)
	return domain.CreatedEnrollment{Enrollment: en, Token: "dye_" + en.ID + "_token"}, nil
}

func (e *fakeEnrollments) GetEnrollment(_ context.Context, id string) (domain.Enrollment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	en, ok := e.byID[id]
	if !ok {
		return en, domain.ErrEnrollmentNotFound
	}
	return en, nil
}

func (e *fakeEnrollments) RevokeEnrollment(_ context.Context, id string) (domain.Enrollment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revoked = append(e.revoked, id)
	en := e.byID[id]
	if en.UsedAt == nil { // like the agents service: only a pending token is revoked
		now := e.f.clk.Now().UTC()
		en.RevokedAt = &now
		e.byID[id] = en
	}
	return en, nil
}

func (e *fakeEnrollments) GetAgent(_ context.Context, id string) (domain.Agent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[id]
	if !ok {
		return a, domain.ErrAgentNotFound
	}
	return a, nil
}

func (e *fakeEnrollments) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	return store.GetEnvironment(ctx, e.f.db, id)
}

// fakeHub is the agent hub: which environments are online, which serve
// manager.redirect, and the redirects it received.
type fakeHub struct {
	mu        sync.Mutex
	online    map[string]bool
	noRequest map[string]bool
	failWith  map[string]error
	redirects map[string]protocol.ManagerRedirectInput
}

func newFakeHub() *fakeHub {
	return &fakeHub{online: map[string]bool{}, noRequest: map[string]bool{}, failWith: map[string]error{},
		redirects: map[string]protocol.ManagerRedirectInput{}}
}

func (h *fakeHub) Online(env string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online[env]
}

func (h *fakeHub) EnvironmentServes(env, name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online[env] && name == protocol.ReqManagerRedirect && !h.noRequest[env]
}

func (h *fakeHub) RequestEnvironment(_ context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if name != protocol.ReqManagerRedirect {
		return nil, errors.New("unexpected request " + name)
	}
	if err := h.failWith[env]; err != nil {
		return nil, err
	}
	h.redirects[env] = input.(protocol.ManagerRedirectInput)
	return json.RawMessage(`{}`), nil
}

func (h *fakeHub) setOnline(env string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.online[env] = on
}

// fakeMigrations stands in for the environment migration service: its
// migration is a real job row the test finishes (finishMigration).
type fakeMigrations struct {
	mu        sync.Mutex
	f         *fixture
	started   []startedMigration
	blocked   *envmigrations.EnvironmentBlockedError
	retained  int
	principal authz.Principal
}

type startedMigration struct {
	source, target, key string
	id                  string
}

func (m *fakeMigrations) StartEnvironment(ctx context.Context, p authz.Principal, source string, r envmigrations.EnvironmentRequest) (domain.Job,
	domain.EnvironmentMigration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked != nil {
		return domain.Job{}, domain.EnvironmentMigration{}, m.blocked
	}
	for _, s := range m.started {
		if s.key == r.IdempotencyKey {
			j, err := store.GetJob(ctx, m.f.db, s.id)
			return j, domain.EnvironmentMigration{ID: s.id}, err
		}
	}
	m.principal = p
	now := m.f.clk.Now().UTC()
	j := domain.Job{ID: ids.New(), Kind: jobspec.EnvironmentMigrate, Executor: domain.ExecutorManager, Origin: domain.OriginManual,
		InitiatorUserID: p.UserID, EnvironmentID: source, Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "stack-1"}},
		Input: []byte("{}"), InputHash: "h", Attempt: 1, State: domain.JobRunning, Progress: domain.JobProgress{Percent: -1}, CreatedAt: now,
		UpdatedAt: now}
	if err := store.InsertJob(ctx, m.f.db, &j); err != nil {
		return domain.Job{}, domain.EnvironmentMigration{}, err
	}
	rec := domain.EnvironmentMigration{ID: j.ID, SourceEnvironmentID: source, TargetEnvironmentID: r.TargetEnvironmentID, State: domain.MigrationRunning,
		Groups: [][]string{{"stack-1", "stack-2"}}, CreatedAt: now, UpdatedAt: now,
		Stacks: []domain.EnvironmentMigrationStack{{StackID: "stack-1", Name: "proxy", State: domain.EnvironmentStackMoved},
			{StackID: "stack-2", Name: "web", State: domain.EnvironmentStackMoving}}}
	if err := store.InsertEnvironmentMigration(ctx, m.f.db, &rec); err != nil {
		return domain.Job{}, domain.EnvironmentMigration{}, err
	}
	m.started = append(m.started, startedMigration{source: source, target: r.TargetEnvironmentID, key: r.IdempotencyKey, id: j.ID})
	return j, rec, nil
}

func (m *fakeMigrations) GetEnvironmentMigration(ctx context.Context, id string) (domain.EnvironmentMigration, error) {
	return store.GetEnvironmentMigration(ctx, m.f.db, id)
}

func (m *fakeMigrations) RetainedSources(context.Context, string) ([]envmigrations.RetainedSource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]envmigrations.RetainedSource, m.retained)
	return out, nil
}

// fixture is one manager's move service over a migrated SQLite database in
// its own data directory, with a fake clock, a job engine and fakes of
// the agents and migration services.
type fixture struct {
	t        *testing.T
	ctx      context.Context
	clk      *clock.Fake
	dataDir  string
	dbPath   string
	keyFile  string
	client   *http.Client
	db       *bun.DB
	keyring  *secrets.Keyring
	inst     domain.Instance
	lock     *movelock.Lock
	eng      *jobs.Engine
	guard    *fakeGuard
	audit    *memAudit
	enroll   *fakeEnrollments
	hub      *fakeHub
	migr     *fakeMigrations
	bus      *events.Bus
	waiting  *WaitingConfig
	varsSet  bool
	svc      *Service
	restarts atomic.Int32
	closed   bool
}

func newFixture(t *testing.T, clk *clock.Fake, client *http.Client) *fixture {
	t.Helper()
	return newFixtureWith(t, clk, client, nil)
}

// newFixtureWith builds a fixture; waiting starts it in waiting mode.
func newFixtureWith(t *testing.T, clk *clock.Fake, client *http.Client, waiting *WaitingConfig) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, ctx: testutil.Context(t), clk: clk, dataDir: dir, dbPath: filepath.Join(dir, "docker-manager.db"),
		keyFile: filepath.Join(dir, "secret.key"), client: client, waiting: waiting, varsSet: waiting != nil}
	f.enroll = &fakeEnrollments{f: f, byID: map[string]domain.Enrollment{}, agents: map[string]domain.Agent{}}
	f.hub = newFakeHub()
	f.migr = &fakeMigrations{f: f}
	f.bus = events.New(clk)
	f.open()
	return f
}

// open starts (or restarts) the manager's pieces over the data directory.
func (f *fixture) open() {
	t := f.t
	t.Helper()
	db, err := store.Open(f.ctx, f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.db, f.closed = db, false
	t.Cleanup(f.close)
	if _, err := store.Migrate(f.ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(f.dataDir, "snap"),
		Clock: f.clk, Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	inst, found, err := store.GetInstance(f.ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		if inst, err = store.CreateInstance(f.ctx, db, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	f.inst = inst
	key, err := secrets.LoadKeyFile(f.keyFile)
	if errors.Is(err, secrets.ErrKeyFileMissing) {
		key, err = secrets.CreateKeyFile(f.keyFile, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	f.keyring = secrets.NewKeyring(key)
	f.lock = movelock.New()
	lvl, err := LockLevel(f.ctx, db, f.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.lock.Set(lvl)
	if f.waiting != nil {
		f.lock.Set(movelock.Waiting)
	}
	if f.eng, err = jobs.New(jobs.Options{DB: db, Clock: f.clk, Logger: testutil.Logger(t), MoveLock: f.lock,
		Authorizer: authztest.New().Owner(ownerID)}); err != nil {
		t.Fatal(err)
	}
	if f.guard == nil {
		f.guard, f.audit = &fakeGuard{}, &memAudit{}
	}
	pub, _ := url.Parse(publicURL)
	f.svc, err = New(Options{DB: db, Keyring: f.keyring, Clock: f.clk, Logger: testutil.Logger(t), Lock: f.lock, Jobs: f.eng, Guard: f.guard,
		Audit: f.audit, Instance: inst, DataDir: f.dataDir, PublicURL: pub, Build: buildinfo.Info{Version: "1.5.0"}, HTTPClient: f.client,
		RequestRestart: func() { f.restarts.Add(1) }, Enrollments: f.enroll, Hub: f.hub, Migrations: f.migr,
		Render: func(in RenderInput) Files {
			return Files{ComposeYAML: "name: docker-manager\n", Env: "DOCKER_MANAGER_MOVE_FROM=" + in.OldManagerURL + "\nDOCKER_MANAGER_MOVE_CODE=" +
				in.MoveCode + "\nDOCKER_AGENT_ENROLLMENT_TOKEN=" + in.EnrollmentToken + "\n"}
		},
		Waiting: f.waiting, MoveVariablesSet: f.varsSet, Bus: f.bus,
		SchemaMigrations: func(ctx context.Context) ([]string, error) {
			applied, _, err := store.Status(ctx, db, migrations.Migrations)
			return applied, err
		},
		CheckSchema: func(ctx context.Context, db bun.IDB) error {
			_, _, err := store.Status(ctx, db, migrations.Migrations)
			return err
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) close() {
	if f.closed {
		return
	}
	f.closed = true
	f.eng.Close()
	_ = f.db.Close()
}

// owner is a request context of the signed-in owner (the principal of
// Move everything).
func (f *fixture) owner() context.Context {
	ctx, err := authz.WithPrincipal(f.ctx, authz.Principal{Kind: authz.KindUser, UserID: ownerID})
	if err != nil {
		f.t.Fatal(err)
	}
	return ctx
}

// from is a request context from the new server's address.
func (f *fixture) from() context.Context {
	return requestinfo.With(f.ctx, requestinfo.Info{Scheme: "http", Host: "192.168.1.10:8080", ClientIP: netip.MustParseAddr("192.168.1.20")})
}

// signed signs a move request of the given path with code now.
func (f *fixture) signed(code, path string) MoveAuth {
	f.t.Helper()
	h, err := SignRequest(code, http.MethodPost, path, f.clk.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	return MoveAuth{Header: h, Method: http.MethodPost, Path: path}
}

func (f *fixture) handoff(code string) (*Package, error) {
	return f.svc.Handoff(f.from(), f.signed(code, HandoffPath))
}

func (f *fixture) confirm(code string) (domain.ManagerMove, error) {
	return f.svc.Confirm(f.from(), f.signed(code, ConfirmPath))
}

// addEnvironment stores an active environment.
func (f *fixture) addEnvironment(id, name, serviceAddress string) domain.Environment {
	f.t.Helper()
	now := f.clk.Now().UTC()
	e := domain.Environment{ID: id, Name: name, ServiceAddress: serviceAddress, EngineID: "ENGINE-" + id, InstallID: "install-" + id,
		Status: domain.EnvironmentActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertEnvironment(f.ctx, f.db, &e); err != nil {
		f.t.Fatal(err)
	}
	return e
}

// addStack stores a stack of env.
func (f *fixture) addStack(env, name string) {
	f.t.Helper()
	now := f.clk.Now().UTC()
	st := domain.Stack{ID: ids.New(), EnvironmentID: env, Name: name, Root: protocol.RootStacks, Dir: name, Origin: "imported",
		Status: domain.StackDeployed, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if err := store.InsertStack(f.ctx, f.db, &st); err != nil {
		f.t.Fatal(err)
	}
}

// colocate records an environment next to this manager (with a stack),
// online.
func (f *fixture) colocate() string {
	f.t.Helper()
	f.addEnvironment("env-old", "old-server", "192.168.1.10")
	f.addStack("env-old", "web")
	f.svc.ObserveColocation("env-old", true)
	f.hub.setOnline("env-old", true)
	return "env-old"
}

// create creates a move to 192.168.1.20 and returns it with the code read
// from the rendered .env.
func (f *fixture) create() (domain.ManagerMove, string) {
	f.t.Helper()
	c, err := f.svc.CreateMove(f.ctx, CreateRequest{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.20"})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Move, envValue(c.Files.Env, "DOCKER_MANAGER_MOVE_CODE")
}

// envValue reads KEY=value from a rendered .env.
func envValue(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// enrollNewServer enrolls the new server's agent with the move's token
// (environment env-new, online).
func (f *fixture) enrollNewServer(m domain.ManagerMove) string {
	f.t.Helper()
	f.addEnvironment("env-new", "192.168.1.20", "")
	f.enroll.mu.Lock()
	en := f.enroll.byID[m.EnrollmentID]
	now := f.clk.Now().UTC()
	en.UsedAt, en.AgentID = &now, "agent-new"
	f.enroll.byID[m.EnrollmentID] = en
	f.enroll.agents["agent-new"] = domain.Agent{ID: "agent-new", EnvironmentID: "env-new"}
	f.enroll.mu.Unlock()
	f.hub.setOnline("env-new", true)
	return "env-new"
}

// setState writes a move's state directly (tests of later states).
func (f *fixture) setState(id string, state domain.ManagerMoveState) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE manager_moves SET state = ? WHERE id = ?", string(state), id); err != nil {
		f.t.Fatal(err)
	}
}

// runningJob stores a running manager job (a job the handoff waits for).
func (f *fixture) runningJob() string {
	f.t.Helper()
	now := f.clk.Now().UTC()
	j := domain.Job{ID: ids.New(), Kind: jobspec.ManagerBackup, Executor: domain.ExecutorManager, Origin: domain.OriginScheduled,
		Targets: []domain.JobTarget{{Type: domain.TargetRepository, ID: "repo"}}, Input: []byte("{}"), InputHash: "h", Attempt: 1,
		State: domain.JobRunning, Progress: domain.JobProgress{Percent: -1}, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertJob(f.ctx, f.db, &j); err != nil {
		f.t.Fatal(err)
	}
	return j.ID
}

func (f *fixture) finishJob(id string, state domain.JobState) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE jobs SET state = ? WHERE id = ?", string(state), id); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) move(id string) domain.ManagerMove {
	f.t.Helper()
	m, err := store.GetManagerMove(f.ctx, f.db, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// oldManagerHandler serves the old manager's handoff and confirmation
// like the API does (the API's own mapping is tested in package api).
func oldManagerHandler(old *fixture, confirmFailures *atomic.Int32) http.Handler {
	mux := http.NewServeMux()
	auth := func(r *http.Request) MoveAuth {
		return MoveAuth{Header: r.Header.Get("Authorization"), Method: r.Method, Path: r.URL.Path}
	}
	fail := func(w http.ResponseWriter, err error) {
		var jr *domain.JobsRunningError
		var nr *domain.MoveNotReadyError
		status, c := http.StatusInternalServerError, "internal"
		switch {
		case errors.As(err, &jr):
			status, c = http.StatusConflict, "jobs_running"
			w.Header().Set("Retry-After", "1")
			w.Header().Set(JobsRunningHeader, strconv.Itoa(jr.Count))
		case errors.As(err, &nr):
			status, c = http.StatusConflict, "manager_move_not_ready"
			w.Header().Set("Retry-After", "10")
			w.Header().Set(MoveStateHeader, string(nr.State))
			w.Header().Set(MoveStacksHeader, strconv.Itoa(nr.StacksMoved)+"/"+strconv.Itoa(nr.StacksTotal))
			w.Header().Set(MoveCurrentStackHeader, nr.CurrentStack)
		case errors.Is(err, domain.ErrMoveCodeInvalid):
			status, c = http.StatusUnauthorized, "move_code_invalid"
		case errors.Is(err, domain.ErrMoveClockSkew):
			status, c = http.StatusUnauthorized, "move_clock_skew"
		case errors.Is(err, domain.ErrManagerMoveState):
			status, c = http.StatusConflict, "manager_move_state"
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": c, "message": err.Error()})
	}
	mux.HandleFunc("GET "+CheckInPath, func(w http.ResponseWriter, r *http.Request) {
		c, err := old.svc.CheckIn(old.from(), auth(r))
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": string(c.State), "stacksMoved": c.StacksMoved, "stacksTotal": c.StacksTotal,
			"currentStack": c.CurrentStack, "jobsRunning": c.JobsRunning})
	})
	mux.HandleFunc("POST "+HandoffPath, func(w http.ResponseWriter, r *http.Request) {
		pkg, err := old.svc.Handoff(old.from(), auth(r))
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", PackageContentType)
		w.Header().Set(PackageSizeHeader, strconv.FormatInt(pkg.Size(), 10))
		_ = pkg.Write(w)
	})
	mux.HandleFunc("POST "+ConfirmPath, func(w http.ResponseWriter, r *http.Request) {
		if confirmFailures != nil && confirmFailures.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if _, err := old.svc.Confirm(old.from(), auth(r)); err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

// waitJob waits until a job reached a terminal state, moving the fake
// clock a second at a time (the steps' poll timers).
func waitJob(t *testing.T, f *fixture, id string) domain.Job {
	t.Helper()
	for {
		j, err := f.eng.Get(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State.Terminal() {
			return j
		}
		f.clk.Advance(time.Second)
		select {
		case <-f.ctx.Done():
			t.Fatalf("job %s did not finish (state %s)", id, j.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitUntil polls cond (work runs on other goroutines), moving the fake
// clock a second at a time when advance is set.
func waitUntil(t *testing.T, f *fixture, advance bool, cond func() bool) {
	t.Helper()
	for !cond() {
		if advance {
			f.clk.Advance(time.Second)
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("condition not reached")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
