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

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/requestinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const publicURL = "https://docker.example.com"

// fakeGuard answers owner checks.
type fakeGuard struct{ err error }

func (g *fakeGuard) RequireOwner(context.Context, bool) (string, error) {
	if g.err != nil {
		return "", g.err
	}
	return "owner-1", nil
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

// fixture is one manager's move service over a migrated SQLite database in
// its own data directory, with a fake clock and a job engine.
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
	svc      *Service
	restarts atomic.Int32
	closed   bool
}

func newFixture(t *testing.T, clk *clock.Fake, client *http.Client) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, ctx: testutil.Context(t), clk: clk, dataDir: dir, dbPath: filepath.Join(dir, "docker-manager.db"),
		keyFile: filepath.Join(dir, "secret.key"), client: client}
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
	if f.eng, err = jobs.New(jobs.Options{DB: db, Clock: f.clk, Logger: testutil.Logger(t), MoveLock: f.lock}); err != nil {
		t.Fatal(err)
	}
	if f.guard == nil {
		f.guard, f.audit = &fakeGuard{}, &memAudit{}
	}
	pub, _ := url.Parse(publicURL)
	f.svc, err = New(Options{DB: db, Keyring: f.keyring, Clock: f.clk, Logger: testutil.Logger(t), Lock: f.lock, Jobs: f.eng, Guard: f.guard,
		Audit: f.audit, Instance: inst, DataDir: f.dataDir, PublicURL: pub, Build: buildinfo.Info{Version: "1.5.0"}, HTTPClient: f.client,
		RequestRestart: func() { f.restarts.Add(1) },
		Migrations: func(ctx context.Context) ([]string, error) {
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

// secure is a request context that reached the manager over HTTPS on its
// public URL (the secure origin of the move routes).
func (f *fixture) secure() context.Context {
	return requestinfo.With(f.ctx, requestinfo.Info{Scheme: "https", Host: "docker.example.com", ClientIP: netip.MustParseAddr("203.0.113.7")})
}

// runningJob stores a running manager job (a job the handoff waits for).
func (f *fixture) runningJob() string {
	f.t.Helper()
	now := f.clk.Now().UTC()
	j := domain.Job{ID: ids.New(), Kind: jobspec.ManagerReceive, Executor: domain.ExecutorManager, Origin: domain.OriginScheduled,
		Targets: []domain.JobTarget{{Type: domain.TargetManager, ID: "instance"}}, Input: []byte("{}"), InputHash: "h", Attempt: 1,
		State: domain.JobRunning, Progress: domain.JobProgress{Percent: -1}, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertJob(f.ctx, f.db, &j); err != nil {
		f.t.Fatal(err)
	}
	return j.ID
}

func (f *fixture) finishJob(id string) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE jobs SET state = 'succeeded' WHERE id = ?", id); err != nil {
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
	code := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
	fail := func(w http.ResponseWriter, err error) {
		var jr *domain.JobsRunningError
		status, c := http.StatusInternalServerError, "internal"
		switch {
		case errors.As(err, &jr):
			status, c = http.StatusConflict, "jobs_running"
			w.Header().Set("Retry-After", "1")
			w.Header().Set(JobsRunningHeader, strconv.Itoa(jr.Count))
		case errors.Is(err, domain.ErrMoveCodeInvalid):
			status, c = http.StatusUnauthorized, "move_code_invalid"
		case errors.Is(err, domain.ErrManagerMoveState):
			status, c = http.StatusConflict, "manager_move_state"
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": c, "message": err.Error()})
	}
	mux.HandleFunc("POST "+HandoffPath, func(w http.ResponseWriter, r *http.Request) {
		pkg, err := old.svc.Handoff(old.secure(), code(r))
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", PackageContentType)
		w.Header().Set(packageSizeHeader, strconv.FormatInt(pkg.Size(), 10))
		_ = pkg.Write(w)
	})
	mux.HandleFunc("POST "+ConfirmPath, func(w http.ResponseWriter, r *http.Request) {
		if confirmFailures != nil && confirmFailures.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if _, err := old.svc.Confirm(old.secure(), code(r)); err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

// waitJob waits until a job reached a terminal state.
func waitJob(t *testing.T, f *fixture, id string) domain.Job {
	t.Helper()
	ch, cancel := f.eng.Subscribe(id)
	defer cancel()
	for {
		j, err := f.eng.Get(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State.Terminal() {
			return j
		}
		select {
		case <-ch:
		case <-f.ctx.Done():
			t.Fatalf("job %s did not finish (state %s)", id, j.State)
		case <-time.After(50 * time.Millisecond):
			// Manager jobs report through the engine; poll as a safety net.
		}
	}
}

// waitUntil polls cond (the receive runs on its own goroutine).
func waitUntil(t *testing.T, f *fixture, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-f.ctx.Done():
			t.Fatal("condition not reached")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// advanceUntilDone moves the fake clock a second at a time (the receive's
// retry waits) until the job ends.
func advanceUntilDone(t *testing.T, f *fixture, id string) domain.Job {
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
