package notify_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	// during runs while a request is answered (before the status).
	during func()
}

func newHook(t *testing.T) *hook {
	h := &hook{status: http.StatusOK, block: make(chan struct{})}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(b))
		h.paths = append(h.paths, r.URL.Path)
		status, during := h.status, h.during
		h.mu.Unlock()
		if during != nil {
			during()
		}
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

func (h *hook) onRequest(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.during = f
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
	return f.create(domain.NotificationChannelInput{Name: name, Address: f.address(name + " webhook"), Enabled: true,
		AllEnvironments: true})
}

func TestCreateSealsTheAddressAndReturnsMetadataOnly(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops webhook")
	if c.Service != "generic" || c.Target != "127.0.0.1" || !c.Enabled || c.AddressVersion != 1 ||
		!strings.HasPrefix(c.AddressFingerprint, "fp_") || c.Revision != 1 || c.LastResult != "" || !c.AllEnvironments || len(c.EnvironmentIDs) != 0 {
		t.Fatalf("%+v", c)
	}
	// No subscriptions given: every outcome of every kind.
	if !c.Subscriptions.Equal(domain.AllNotificationSubscriptions()) {
		t.Fatalf("subscriptions %v", c.Subscriptions)
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
	if err != nil || len(list) != 2 || !list[0].InApp() || list[1].ID != c.ID {
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
	c := f.create(domain.NotificationChannelInput{Name: "Reveal", Address: addr, Enabled: true, AllEnvironments: true})
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
		"unknown kind": {domain.NotificationChannelInput{Name: "x", Address: ok,
			Subscriptions: domain.NotificationSubscriptions{"weather": {domain.OutcomeWarning}}}, "events"},
		"foreign outcome": {domain.NotificationChannelInput{Name: "x", Address: ok,
			Subscriptions: domain.NotificationSubscriptions{domain.NotifyPrune: {domain.OutcomeWarning}}}, "events"},
		"nothing": {domain.NotificationChannelInput{Name: "x", Address: ok,
			Subscriptions: domain.NotificationSubscriptions{domain.NotifyRAID: {}}}, "events"},
		"unknown env": {domain.NotificationChannelInput{Name: "x", Address: ok, EnvironmentIDs: []string{"env-9"}}, "environmentIds"},
		// Every environment is a choice, never an empty list.
		"no environments": {domain.NotificationChannelInput{Name: "x", Address: ok}, "environmentIds"},
		"all and a list":  {domain.NotificationChannelInput{Name: "x", Address: ok, AllEnvironments: true, EnvironmentIDs: []string{"env-1"}}, "environmentIds"},
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
	if _, err := f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: " taken ", Address: ok, AllEnvironments: true}); !errors.Is(err, domain.ErrNotificationChannelNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
}

func TestCreateStoresTheSubscription(t *testing.T) {
	f := newFixture(t, 0)
	c := f.create(domain.NotificationChannelInput{Name: "Disks", Address: f.address("disks webhook"), Enabled: false,
		Subscriptions: domain.NotificationSubscriptions{
			domain.NotifyRAID:       {domain.OutcomeResolved, domain.OutcomeCritical, domain.OutcomeCritical},
			domain.NotifyDiskHealth: {domain.OutcomeCritical},
			domain.NotifyBackup:     {},
		},
		EnvironmentIDs: []string{"env-2", "env-1", "env-2"}})
	got, err := f.svc.Get(f.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Outcomes in their kind's order, without duplicates or empty kinds.
	want := domain.NotificationSubscriptions{
		domain.NotifyDiskHealth: {domain.OutcomeCritical},
		domain.NotifyRAID:       {domain.OutcomeCritical, domain.OutcomeResolved},
	}
	if got.Enabled || len(got.Subscriptions) != 2 || !slices.Equal(got.Subscriptions[domain.NotifyRAID], want[domain.NotifyRAID]) ||
		!got.Subscriptions.Equal(want) || got.AllEnvironments || strings.Join(got.EnvironmentIDs, ",") != "env-1,env-2" {
		t.Fatalf("%+v", got)
	}
	// The subscription is replaced as a whole.
	only := domain.NotificationSubscriptions{domain.NotifyBackup: {domain.OutcomeFailure}}
	upd, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Subscriptions: &only})
	if err != nil || !upd.Subscriptions.Equal(only) {
		t.Fatalf("%+v %v", upd, err)
	}
	c = upd
	// An emptied list is refused: it never means every environment.
	var fe *domain.FieldError
	none, no, yes := []string{}, false, true
	for name, p := range map[string]domain.NotificationChannelPatch{
		"empty list":          {EnvironmentIDs: &none},
		"restricted, no list": {AllEnvironments: &no, EnvironmentIDs: &none},
	} {
		if _, err := f.svc.Update(f.ctx, c.ID, c.Revision, p); !errors.As(err, &fe) || fe.Field != "environmentIds" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Every environment is chosen explicitly (and clears the list).
	next, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{AllEnvironments: &yes})
	if err != nil || !next.AllEnvironments || len(next.EnvironmentIDs) != 0 {
		t.Fatalf("%+v %v", next, err)
	}
	if got, _ := f.svc.Get(f.ctx, c.ID); !got.AllEnvironments || len(got.EnvironmentIDs) != 0 {
		t.Fatalf("stored %+v", got)
	}
	// Listing environments restricts it again.
	one := []string{"env-2"}
	next, err = f.svc.Update(f.ctx, c.ID, next.Revision, domain.NotificationChannelPatch{EnvironmentIDs: &one})
	if err != nil || next.AllEnvironments || strings.Join(next.EnvironmentIDs, ",") != "env-2" {
		t.Fatalf("%+v %v", next, err)
	}
}

func TestArchivedEnvironmentsStayInAFilterButAreNeverAdded(t *testing.T) {
	f := newFixture(t, 0)
	c := f.create(domain.NotificationChannelInput{Name: "Prod", Address: f.address("prod webhook"), Enabled: true,
		EnvironmentIDs: []string{"env-2"}})
	env, err := store.GetEnvironment(f.ctx, f.db, "env-2")
	if err != nil {
		t.Fatal(err)
	}
	env.Status = domain.EnvironmentArchived
	if err := store.UpdateEnvironment(f.ctx, f.db, &env, 0); err != nil {
		t.Fatal(err)
	}
	// Saving the dialog again (same list, new name) keeps the archived one:
	// the channel does not widen to every environment.
	name, same := "Prod team", []string{"env-2"}
	next, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Name: &name, EnvironmentIDs: &same})
	if err != nil || next.AllEnvironments || strings.Join(next.EnvironmentIDs, ",") != "env-2" {
		t.Fatalf("%+v %v", next, err)
	}
	if next.Wants(domain.NotifyJobFailed, domain.OutcomeFailure, "env-1") {
		t.Fatal("a channel for an archived environment receives other environments' events")
	}
	// An archived environment is never added to another filter.
	other := f.create(domain.NotificationChannelInput{Name: "Lab", Address: f.address("lab webhook"), Enabled: true,
		EnvironmentIDs: []string{"env-1"}})
	add := []string{"env-1", "env-2"}
	var fe *domain.FieldError
	if _, err := f.svc.Update(f.ctx, other.ID, other.Revision, domain.NotificationChannelPatch{EnvironmentIDs: &add}); !errors.As(err, &fe) ||
		fe.Field != "environmentIds" {
		t.Fatalf("adding an archived environment: %v", err)
	}
	if _, err := f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: "New", Address: f.address("new webhook"),
		EnvironmentIDs: []string{"env-2"}}); !errors.As(err, &fe) {
		t.Fatalf("creating with an archived environment: %v", err)
	}
}

// A test reads the channel and its address together: when the address is
// replaced while the message is on its way, the message went to the
// address that was read and its result is not recorded for the new one.
func TestResultOfAReplacedAddressIsNotRecorded(t *testing.T) {
	f := newFixture(t, 0)
	c := f.channel("Ops")
	replacement := f.address("replacement webhook")
	var replaced domain.NotificationChannel
	f.hook.onRequest(func() {
		f.hook.onRequest(nil)
		var err error
		if replaced, err = f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Address: &replacement}); err != nil {
			t.Errorf("replace: %v", err)
		}
	})
	res, err := f.svc.Test(f.ctx, c.ID)
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if replaced.AddressVersion != 2 {
		t.Fatalf("replaced %+v", replaced)
	}
	_, paths := f.hook.received()
	if len(paths) != 1 || strings.Contains(replacement, paths[0]) {
		t.Fatalf("sent to %q", paths)
	}
	got, err := f.svc.Get(f.ctx, c.ID)
	if err != nil || got.AddressVersion != 2 || got.LastResult != "" || got.LastAttemptAt != nil || got.LastSuccessAt != nil {
		t.Fatalf("the new address got the old address's result: %+v %v", got, err)
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

// The In App channel has no address: its subscription, environments and
// Enabled change; it keeps its name and can't be deleted, tested, revealed
// or sent through.
func TestInAppChannelIsBuiltIn(t *testing.T) {
	f := newFixture(t, 0)
	c, err := f.svc.Get(f.ctx, domain.InAppChannelID)
	if err != nil || c.Name != domain.InAppChannelName || c.Service != domain.InAppService || !c.Enabled ||
		!c.Subscriptions.Equal(domain.AllNotificationSubscriptions()) || !c.AllEnvironments {
		t.Fatalf("%+v %v", c, err)
	}
	subs := domain.NotificationSubscriptions{domain.NotifyBackup: {domain.OutcomeFailure}}
	off, name, envs := false, " In App ", []string{"env-1"}
	next, err := f.svc.Update(f.ctx, c.ID, c.Revision, domain.NotificationChannelPatch{Name: &name, Enabled: &off,
		Subscriptions: &subs, EnvironmentIDs: &envs})
	if err != nil || next.Enabled || !next.Subscriptions.Equal(subs) || next.AllEnvironments || next.EnvironmentIDs[0] != "env-1" ||
		next.Name != domain.InAppChannelName || next.Revision != c.Revision+1 {
		t.Fatalf("%+v %v", next, err)
	}
	var fe *domain.FieldError
	other, addr := "Bell", f.address("in app")
	if _, err := f.svc.Update(f.ctx, c.ID, next.Revision, domain.NotificationChannelPatch{Name: &other}); !errors.As(err, &fe) ||
		fe.Field != "name" {
		t.Fatalf("rename: %v", err)
	}
	if _, err := f.svc.Update(f.ctx, c.ID, next.Revision, domain.NotificationChannelPatch{Address: &addr}); !errors.As(err, &fe) ||
		fe.Field != "address" {
		t.Fatalf("address: %v", err)
	}
	if err := f.svc.Delete(f.ctx, c.ID, next.Revision); !errors.Is(err, domain.ErrNotificationChannelBuiltIn) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.svc.Test(f.ctx, c.ID); !errors.Is(err, domain.ErrNotificationChannelBuiltIn) {
		t.Fatalf("test: %v", err)
	}
	if _, err := f.svc.Reveal(f.ctx, c.ID); !errors.Is(err, domain.ErrNotificationChannelBuiltIn) {
		t.Fatalf("reveal: %v", err)
	}
	if _, err := f.svc.Send(f.ctx, c.ID, domain.NotificationMessage{Title: "x"}); !errors.Is(err, domain.ErrNotificationChannelBuiltIn) {
		t.Fatalf("send: %v", err)
	}
	// Its name stays taken.
	if _, err := f.svc.Create(f.ctx, domain.NotificationChannelInput{Name: "in app", Address: f.address("other"), Enabled: true,
		AllEnvironments: true}); !errors.Is(err, domain.ErrNotificationChannelNameTaken) {
		t.Fatalf("name: %v", err)
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
	c := f.create(domain.NotificationChannelInput{Name: "Gone", Address: addr, Enabled: true, AllEnvironments: true})
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
	failed := domain.OutcomeFailure
	c := domain.NotificationChannel{Enabled: true, AllEnvironments: true,
		Subscriptions: domain.NotificationSubscriptions{domain.NotifyJobFailed: {failed}, domain.NotifyBackup: {domain.OutcomeSuccess}}}
	if !c.Wants(domain.NotifyJobFailed, failed, "env-1") || c.Wants(domain.NotifyRAID, domain.OutcomeCritical, "env-1") {
		t.Fatal("kinds")
	}
	// Outcomes are chosen per kind.
	if c.Wants(domain.NotifyJobFailed, domain.OutcomeResolved, "env-1") || c.Wants(domain.NotifyBackup, failed, "env-1") ||
		!c.Wants(domain.NotifyBackup, domain.OutcomeSuccess, "env-1") {
		t.Fatal("outcomes")
	}
	c.AllEnvironments, c.EnvironmentIDs = false, []string{"env-2"}
	if c.Wants(domain.NotifyJobFailed, failed, "env-1") || !c.Wants(domain.NotifyJobFailed, failed, "env-2") ||
		!c.Wants(domain.NotifyJobFailed, failed, "") {
		t.Fatal("environments")
	}
	// A filter whose environments are all gone sends no environment's events.
	c.EnvironmentIDs = nil
	if c.Wants(domain.NotifyJobFailed, failed, "env-2") {
		t.Fatal("an emptied filter widened to every environment")
	}
	c.Enabled = false
	if c.Wants(domain.NotifyJobFailed, failed, "env-2") {
		t.Fatal("disabled")
	}
}

// The bell shows what a channel would have been sent: a resolution only
// with what it fired with, a failed job as its area.
func TestShowsAlertAndNotification(t *testing.T) {
	c := domain.NotificationChannel{ID: domain.InAppChannelID, Enabled: true, AllEnvironments: true,
		Subscriptions: domain.NotificationSubscriptions{domain.NotifyDiskHealth: {domain.OutcomeCritical, domain.OutcomeResolved},
			domain.NotifyBackup: {domain.OutcomeFailure}, domain.NotifyPrune: {domain.OutcomeSuccess}}}
	disk := domain.Alert{Kind: domain.NotifyDiskHealth, Severity: domain.AlertCritical, State: domain.AlertFiring, EnvironmentID: "env-1"}
	if !c.ShowsAlert(disk) {
		t.Fatal("firing")
	}
	warn := disk
	warn.Severity = domain.AlertWarning
	if c.ShowsAlert(warn) {
		t.Fatal("an outcome it does not send")
	}
	fixed := disk
	fixed.State, fixed.Resolution = domain.AlertResolved, domain.AlertResolvedFixed
	if !c.ShowsAlert(fixed) {
		t.Fatal("resolved")
	}
	removed := fixed
	removed.Resolution = domain.AlertResolvedRemoved
	if c.ShowsAlert(removed) {
		t.Fatal("removed: nothing to tell")
	}
	warn.State, warn.Resolution = domain.AlertResolved, domain.AlertResolvedFixed
	if c.ShowsAlert(warn) {
		t.Fatal("the resolution of what it never showed")
	}
	// A failed backup verification is a backup failure, its resolution too.
	job := domain.Alert{Kind: domain.NotifyJobFailed, Severity: domain.AlertCritical, State: domain.AlertFiring, JobKind: "backup.verify"}
	if !c.ShowsAlert(job) {
		t.Fatal("failed job of an area")
	}
	job.State, job.Resolution = domain.AlertResolved, domain.AlertResolvedFixed
	if !c.ShowsAlert(job) {
		t.Fatal("resolved job of an area")
	}
	job.JobKind = "stack.deploy"
	if c.ShowsAlert(job) {
		t.Fatal("other jobs")
	}
	if !c.ShowsNotification(domain.Notification{Kind: domain.NotifyPrune, Outcome: domain.OutcomeSuccess, EnvironmentID: "env-1"}) ||
		c.ShowsNotification(domain.Notification{Kind: domain.NotifyPrune, Outcome: domain.OutcomeFailure}) {
		t.Fatal("notifications")
	}
	c.Enabled = false
	if c.ShowsAlert(disk) || c.ShowsNotification(domain.Notification{Kind: domain.NotifyPrune, Outcome: domain.OutcomeSuccess}) {
		t.Fatal("off")
	}
}

func TestEveryKindHasOutcomes(t *testing.T) {
	for _, k := range domain.NotificationEventKinds() {
		if len(k.Outcomes()) == 0 {
			t.Errorf("%s has no outcomes", k)
		}
	}
	for _, k := range append(domain.AlertKinds(), domain.NotificationKinds()...) {
		if !k.Valid() {
			t.Errorf("%s is not an event kind", k)
		}
	}
	if domain.NotificationEventKind("weather").Valid() || len(domain.NotificationEventKind("weather").Outcomes()) != 0 {
		t.Error("unknown kind")
	}
}
