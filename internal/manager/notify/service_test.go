package notify_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/notify"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// guard is a switchable owner guard that records whether each call asked
// for a recent step-up.
type guard struct {
	mu     sync.Mutex
	err    error // any call
	stepUp error // calls that need a recent step-up
	recent []bool
}

func (g *guard) RequireOwner(_ context.Context, recent bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recent = append(g.recent, recent)
	if g.err != nil {
		return "", g.err
	}
	if recent && g.stepUp != nil {
		return "", g.stepUp
	}
	return "owner", nil
}

func (g *guard) set(err, stepUp error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err, g.stepUp = err, stepUp
}

// hook is a webhook receiver on the loopback interface.
type hook struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	bodies []string
	paths  []string
	block  chan struct{}
}

func newHook(t *testing.T) *hook {
	h := &hook{status: http.StatusOK, block: make(chan struct{})}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(b))
		h.paths = append(h.paths, r.URL.Path)
		status := h.status
		h.mu.Unlock()
		switch status {
		case 0: // hang until the client gives up
			select {
			case <-h.block:
			case <-r.Context().Done():
			}
			return
		case http.StatusFound:
			http.Redirect(w, r, "https://hooks.example.com/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(func() {
		close(h.block)
		h.srv.Close()
	})
	return h
}

func (h *hook) answer(status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
}

func (h *hook) received() (bodies, paths []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...), append([]string(nil), h.paths...)
}

// url is a generic webhook address on the hook whose path holds token.
func (h *hook) url(token string) string {
	return "generic+" + h.srv.URL + "/hook/" + token
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	dbPath  string
	db      *bun.DB
	clk     *clock.Fake
	guard   *guard
	svc     *notify.Service
	hook    *hook
	secrets *canary.Set
	logs    *testutil.LogBuffer
}

func newFixture(t *testing.T, timeout time.Duration) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-manager.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, dbPath: path, db: db, clk: testutil.FakeClock(), guard: &guard{}, secrets: canary.New(), hook: newHook(t)}
	logger, buf := testutil.CaptureLogger()
	f.logs = buf
	f.secrets.CheckLogsAtCleanup(t, buf)
	now := f.clk.Now().UTC()
	for _, id := range []string{"env-1", "env-2"} {
		env := domain.Environment{ID: id, Name: id, EngineID: "E-" + id, InstallID: "i-" + id, Status: domain.EnvironmentActive,
			Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &env); err != nil {
			t.Fatal(err)
		}
	}
	f.svc, err = notify.New(notify.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: f.clk, Logger: logger, Guard: f.guard,
		Timeout: timeout, PublicURL: "https://docker.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.assertNoLeaks)
	return f
}

// assertNoLeaks scans the whole database file (including the WAL) for
// plaintext addresses.
func (f *fixture) assertNoLeaks() {
	t := f.t
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Errorf("checkpoint: %v", err)
	}
	for _, p := range []string{f.dbPath, f.dbPath + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
		f.secrets.AssertClean(t, "database file "+filepath.Base(p), b)
	}
}

// address returns a generic webhook address with a fresh canary token.
func (f *fixture) address(name string) string {
	return f.hook.url(f.secrets.New(canary.NotificationURL, name))
}

func (f *fixture) create(in domain.NotificationChannelInput) domain.NotificationChannel {
	f.t.Helper()
	c, err := f.svc.Create(f.ctx, in)
	if err != nil {
		f.t.Fatalf("create %s: %v", in.Name, err)
	}
	f.secrets.AssertClean(f.t, "created channel", c)
	return c
}

func (f *fixture) channel(name string) domain.NotificationChannel {
	return f.create(domain.NotificationChannelInput{Name: name, Address: f.address(name + " webhook"), Enabled: true, SendResolved: true})
}

func TestCreateSealsTheAddressAndReturnsMetadataOnly(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops webhook")
	if c.Service != "generic" || c.Target != "127.0.0.1" || !c.Enabled || !c.SendResolved || c.AddressVersion != 1 ||
		!strings.HasPrefix(c.AddressFingerprint, "fp_") || c.Revision != 1 || c.LastResult != "" || len(c.EnvironmentIDs) != 0 {
		t.Fatalf("%+v", c)
	}
	// No event kinds given: every kind.
	if len(c.EventKinds) != len(domain.NotificationEventKinds()) {
		t.Fatalf("kinds %v", c.EventKinds)
	}
	var sealed string
	if err := f.db.NewSelect().Table("notification_channels").Column("secret_sealed").Where("id = ?", c.ID).Scan(f.ctx, &sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "dy1.") {
		t.Fatalf("address not sealed: %q", sealed[:8])
	}
	got, err := f.svc.Get(f.ctx, c.ID)
	if err != nil || got.AddressFingerprint != c.AddressFingerprint || got.Name != "Ops webhook" {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := f.svc.List(f.ctx, "", 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	f.secrets.AssertClean(t, "get", got)
	f.secrets.AssertClean(t, "list", list)
	// Creating needs the owner with a recent step-up.
	if f.guard.recent[0] != true {
		t.Fatalf("recent flags %v", f.guard.recent)
	}
}

func TestRevealReturnsTheAddressToTheOwnerWithStepUp(t *testing.T) {
	f := newFixture(t, 0)
	addr := f.address("revealed webhook")
	c := f.create(domain.NotificationChannelInput{Name: "Reveal", Address: addr, Enabled: true})
	got, err := f.svc.Reveal(f.ctx, c.ID)
	if err != nil || got != addr {
		t.Fatalf("reveal: %v (matches %v)", err, got == addr)
	}
	f.guard.set(nil, domain.ErrStepUpRequired)
	if _, err := f.svc.Reveal(f.ctx, c.ID); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("without step-up: %v", err)
	}
	f.guard.set(domain.ErrForbidden, nil)
	if _, err := f.svc.Reveal(f.ctx, c.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("not the owner: %v", err)
	}
	f.guard.set(nil, nil)
	if _, err := f.svc.Reveal(f.ctx, "nope"); !errors.Is(err, domain.ErrNotificationChannelNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestAdministrationIsOwnerOnly(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops")
	name := "Renamed"
	addr := f.address("other webhook")
	f.guard.set(domain.ErrForbidden, nil)
	checks := map[string]error{}
	_, checks["create"] = f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: "x", Address: addr})
	_, checks["update"] = f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Name: &name})
	_, checks["reveal"] = f.svc.Reveal(f.ctx, c.ID)
	_, checks["test"] = f.svc.Test(f.ctx, c.ID)
	checks["delete"] = f.svc.Delete(f.ctx, c.ID, c.Revision)
	for op, err := range checks {
		if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s = %v, want forbidden", op, err)
		}
	}
	// Step-up: creating, changing the address and revealing it; not
	// renaming, testing or deleting.
	f.guard.set(nil, domain.ErrStepUpRequired)
	if _, err := f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: "x", Address: addr}); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Errorf("create: %v", err)
	}
	if _, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Address: &addr}); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Errorf("address change: %v", err)
	}
	c2, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Name: &name})
	if err != nil || c2.Name != "Renamed" || c2.Revision != 2 {
		t.Fatalf("rename: %+v %v", c2, err)
	}
	if _, err := f.svc.Test(f.ctx, c.ID); err != nil {
		t.Errorf("test: %v", err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c2.Revision); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t, 0)
	f.channel("Taken")
	ok := f.address("valid webhook")
	cases := map[string]struct {
		in    domain.NotificationChannelInput
		field string
	}{
		"empty name":        {domain.NotificationChannelInput{Name: "  ", Address: ok}, "name"},
		"long name":         {domain.NotificationChannelInput{Name: strings.Repeat("x", 101), Address: ok}, "name"},
		"control character": {domain.NotificationChannelInput{Name: "a\nb", Address: ok}, "name"},
		"no address":        {domain.NotificationChannelInput{Name: "x"}, "address"},
		"not a URL":         {domain.NotificationChannelInput{Name: "x", Address: "hooks example com"}, "address"},
		"unknown service":   {domain.NotificationChannelInput{Name: "x", Address: "carrierpigeon://coop/42"}, "address"},
		"broken address":    {domain.NotificationChannelInput{Name: "x", Address: "slack://not-a-token@webhook"}, "address"},
		"unknown kind":      {domain.NotificationChannelInput{Name: "x", Address: ok, EventKinds: []domain.NotificationEventKind{"weather"}}, "eventKinds"},
		"no kinds":          {domain.NotificationChannelInput{Name: "x", Address: ok, EventKinds: []domain.NotificationEventKind{}}, "eventKinds"},
		"unknown env":       {domain.NotificationChannelInput{Name: "x", Address: ok, EnvironmentIDs: []string{"env-9"}}, "environmentIds"},
	}
	for name, c := range cases {
		_, err := f.svc.Create(f.ctx, c.in)
		var fe *domain.FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v, want a field error on %s", name, err, c.field)
			continue
		}
		f.secrets.AssertClean(t, name+" error", err)
		if strings.Contains(err.Error(), "://") {
			t.Errorf("%s: the error repeats the address: %v", name, err)
		}
	}
	if _, err := f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: " taken ", Address: ok}); !errors.Is(err, domain.ErrNotificationChannelNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
}

func TestCreateStoresTheSubscription(t *testing.T) {
	f := newFixture(t, 0)
	c := f.create(domain.NotificationChannelInput{Name: "Disks", Address: f.address("disks webhook"), Enabled: false,
		EventKinds:     []domain.NotificationEventKind{domain.NotifyRAID, domain.NotifyDiskHealth, domain.NotifyRAID},
		EnvironmentIDs: []string{"env-2", "env-1", "env-2"}})
	got, err := f.svc.Get(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.SendResolved || len(got.EventKinds) != 2 || got.EventKinds[0] != domain.NotifyDiskHealth ||
		got.EventKinds[1] != domain.NotifyRAID || strings.Join(got.EnvironmentIDs, ",") != "env-1,env-2" {
		t.Fatalf("%+v", got)
	}
	// Clearing the environment filter means every environment again.
	none := []string{}
	next, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{EnvironmentIDs: &none})
	if err != nil || len(next.EnvironmentIDs) != 0 {
		t.Fatalf("%+v %v", next, err)
	}
	if got, _ := f.svc.Get(f.ctx, c.ID); len(got.EnvironmentIDs) != 0 {
		t.Fatalf("stored filter %v", got.EnvironmentIDs)
	}
}

func TestUpdateAddressReSealsAndBumpsTheVersion(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops")
	if res, err := f.svc.Test(f.ctx, c.ID); err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	c, _ = f.svc.Get(f.ctx, c.ID)
	if c.LastResult != domain.NotificationResultOK {
		t.Fatalf("last result %q", c.LastResult)
	}
	addr := f.address("new webhook")
	next, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Address: &addr})
	if err != nil {
		t.Fatal(err)
	}
	if next.AddressVersion != 2 || next.AddressFingerprint == c.AddressFingerprint || next.Revision != c.Revision+1 ||
		next.LastResult != "" || next.LastAttemptAt != nil {
		t.Fatalf("%+v", next)
	}
	if got, err := f.svc.Reveal(f.ctx, c.ID); err != nil || got != addr {
		t.Fatalf("reveal after change: %v", err)
	}
	// Stale revision.
	name := "x"
	if _, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Name: &name}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale: %v", err)
	}
	bad := "carrierpigeon://coop"
	if _, err := f.svc.Update(f.ctx, c.ID, next.Revision, domain.NotificationChannelPatch{Address: &bad}); err == nil {
		t.Fatal("an unknown service was accepted")
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops")
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision+1); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(f.ctx, c.ID); !errors.Is(err, domain.ErrNotificationChannelNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, c.Revision); !errors.Is(err, domain.ErrNotificationChannelNotFound) {
		t.Fatalf("twice: %v", err)
	}
}

func TestTestSendsAndRecordsTheResult(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops")
	res, err := f.svc.Test(f.ctx, c.ID)
	if err != nil || !res.OK || res.ErrorClass != "" || res.Message != "" || !res.At.Equal(f.clk.Now().UTC()) {
		t.Fatalf("%+v %v", res, err)
	}
	bodies, paths := f.hook.received()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"Ops" works`) || !strings.Contains(bodies[0], "https://docker.example.com") ||
		!strings.HasPrefix(paths[0], "/hook/canary-notify-") {
		t.Fatalf("received %q at %q", bodies, paths)
	}
	got, _ := f.svc.Get(f.ctx, c.ID)
	if got.LastResult != domain.NotificationResultOK || got.LastAttemptAt == nil || got.LastSuccessAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestSendFailuresAreClassesWithoutTheAddress(t *testing.T) {
	cases := map[int]string{
		http.StatusNotFound:            domain.NotifyErrHTTP4xx,
		http.StatusBadRequest:          domain.NotifyErrHTTP4xx,
		http.StatusUnauthorized:        domain.NotifyErrAuth,
		http.StatusForbidden:           domain.NotifyErrAuth,
		http.StatusInternalServerError: domain.NotifyErrHTTP5xx,
		http.StatusBadGateway:          domain.NotifyErrHTTP5xx,
		http.StatusFound:               domain.NotifyErrRedirect,
	}
	f := newFixture(t, 0)
	c := f.channel("Ops")
	for status, want := range cases {
		f.hook.answer(status)
		res, err := f.svc.Send(f.ctx, c.ID, domain.NotificationMessage{Title: "Disk", Body: "sda is failing"})
		if err != nil || res.OK || res.ErrorClass != want || res.Message == "" {
			t.Errorf("%d: %+v %v, want %s", status, res, err, want)
		}
		f.secrets.AssertClean(t, "result", res)
		got, _ := f.svc.Get(f.ctx, c.ID)
		if got.LastResult != want || got.LastSuccessAt != nil {
			t.Errorf("%d: stored %q", status, got.LastResult)
		}
	}
	if !strings.Contains(f.logs.String(), `"error_class":"http_4xx"`) {
		t.Errorf("failures are not logged by class:\n%s", f.logs.String())
	}
}

func TestSendTimesOut(t *testing.T) {
	f := newFixture(t, 200*time.Millisecond)
	c := f.channel("Slow")
	f.hook.answer(0)
	res, err := f.svc.Send(f.ctx, c.ID, domain.NotificationMessage{Body: "hello"})
	if err != nil || res.OK || res.ErrorClass != domain.NotifyErrTimeout {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSendCannotConnect(t *testing.T) {
	f := newFixture(t, 0)
	closed := httptest.NewServer(http.NotFoundHandler())
	token := f.secrets.New(canary.NotificationURL, "closed webhook")
	addr := "generic+" + closed.URL + "/hook/" + token
	closed.Close()
	c := f.create(domain.NotificationChannelInput{Name: "Gone", Address: addr, Enabled: true})
	res, err := f.svc.Send(f.ctx, c.ID, domain.NotificationMessage{Body: "hello"})
	if err != nil || res.OK || res.ErrorClass != domain.NotifyErrConnect {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestTestsAreRateLimitedPerChannel(t *testing.T) {
	f := newFixture(t, 0)
	a, b := f.channel("A"), f.channel("B")
	if _, err := f.svc.Test(f.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	var rl *notify.TestRateLimitedError
	if _, err := f.svc.Test(f.ctx, a.ID); !errors.As(err, &rl) || rl.RetryAfter != notify.TestInterval {
		t.Fatalf("second test: %v", err)
	}
	// Another channel is not limited.
	if _, err := f.svc.Test(f.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(2 * time.Second)
	if _, err := f.svc.Test(f.ctx, a.ID); !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second {
		t.Fatalf("after 2 s: %v", err)
	}
	f.clk.Advance(3 * time.Second)
	if _, err := f.svc.Test(f.ctx, a.ID); err != nil {
		t.Fatalf("after 5 s: %v", err)
	}
	// Sends for alerts are not rate limited.
	for range 2 {
		if _, err := f.svc.Send(f.ctx, a.ID, domain.NotificationMessage{Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWants(t *testing.T) {
	c := domain.NotificationChannel{Enabled: true, EventKinds: []domain.NotificationEventKind{domain.NotifyJobFailed}}
	if !c.Wants(domain.NotifyJobFailed, "env-1") || c.Wants(domain.NotifyRAID, "env-1") {
		t.Fatal("kinds")
	}
	c.EnvironmentIDs = []string{"env-2"}
	if c.Wants(domain.NotifyJobFailed, "env-1") || !c.Wants(domain.NotifyJobFailed, "env-2") || !c.Wants(domain.NotifyJobFailed, "") {
		t.Fatal("environments")
	}
	c.Enabled = false
	if c.Wants(domain.NotifyJobFailed, "env-2") {
		t.Fatal("disabled")
	}
}
