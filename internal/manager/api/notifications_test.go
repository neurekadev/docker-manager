package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/notify"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// fakeNotifications is an in-memory notification service: it checks
// nothing itself (the API's owner check is under test) and keeps each
// channel's address beside it.
type fakeNotifications struct {
	mu        sync.Mutex
	channels  map[string]domain.NotificationChannel
	addresses map[string]string
	limited   bool
	result    notify.Result
	lastPatch domain.NotificationChannelPatch
}

func newFakeNotifications(address string) *fakeNotifications {
	at := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	c := domain.NotificationChannel{ID: "c-1", Name: "Ops", Service: "generic", Target: "hooks.example.com", Enabled: true,
		EventKinds: domain.NotificationEventKinds(), SendResolved: true, AllEnvironments: true, AddressFingerprint: "fp_0000000000000001",
		AddressVersion:   1,
		AddressUpdatedAt: at, Revision: 1, CreatedAt: at, UpdatedAt: at}
	return &fakeNotifications{channels: map[string]domain.NotificationChannel{"c-1": c}, addresses: map[string]string{"c-1": address},
		result: notify.Result{OK: true, At: at}}
}

func (f *fakeNotifications) List(context.Context, string, int) ([]domain.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.NotificationChannel
	for _, c := range f.channels {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b domain.NotificationChannel) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *fakeNotifications) Get(_ context.Context, id string) (domain.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok {
		return c, domain.ErrNotificationChannelNotFound
	}
	return c, nil
}

func (f *fakeNotifications) Create(ctx context.Context, in domain.NotificationChannelInput) (domain.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.channels {
		if strings.EqualFold(c.Name, in.Name) {
			return domain.NotificationChannel{}, domain.ErrNotificationChannelNameTaken
		}
	}
	c := domain.NotificationChannel{ID: "c-new", Name: in.Name, Service: "ntfy", Enabled: in.Enabled, EventKinds: in.EventKinds,
		SendResolved: in.SendResolved, AllEnvironments: in.AllEnvironments, EnvironmentIDs: in.EnvironmentIDs, AddressFingerprint: "fp_2",
		AddressVersion: 1, Revision: 1}
	if c.EventKinds == nil {
		c.EventKinds = domain.NotificationEventKinds()
	}
	f.channels[c.ID], f.addresses[c.ID] = c, in.Address
	audit.AddTarget(ctx, domain.AuditTarget{Type: "notification_channel", ID: c.ID})
	audit.SetDetail(ctx, "service", c.Service)
	return c, nil
}

func (f *fakeNotifications) Update(_ context.Context, id string, revision int64, p domain.NotificationChannelPatch) (domain.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPatch = p
	c, ok := f.channels[id]
	if !ok {
		return c, domain.ErrNotificationChannelNotFound
	}
	if c.Revision != revision {
		return c, domain.ErrRevisionMismatch
	}
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.EventKinds != nil {
		c.EventKinds = *p.EventKinds
	}
	if p.EnvironmentIDs != nil {
		c.EnvironmentIDs = *p.EnvironmentIDs
	}
	if p.AllEnvironments != nil {
		c.AllEnvironments = *p.AllEnvironments
	}
	if p.Address != nil {
		f.addresses[id] = *p.Address
		c.AddressVersion++
	}
	c.Revision++
	f.channels[id] = c
	return c, nil
}

func (f *fakeNotifications) Delete(_ context.Context, id string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.channels, id)
	return nil
}

func (f *fakeNotifications) Reveal(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.addresses[id]
	if !ok {
		return "", domain.ErrNotificationChannelNotFound
	}
	return a, nil
}

func (f *fakeNotifications) Test(_ context.Context, id string) (notify.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.channels[id]; !ok {
		return notify.Result{}, domain.ErrNotificationChannelNotFound
	}
	if f.limited {
		return notify.Result{}, &notify.TestRateLimitedError{RetryAfter: 3200 * time.Millisecond}
	}
	return f.result, nil
}

type notificationsFixture struct {
	h       http.Handler
	svc     *fakeNotifications
	log     *audit.Log
	bus     *events.Bus
	secrets *canary.Set
	address string
}

func newNotificationsFixture(t *testing.T, pol *authztest.Policy) notificationsFixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC))
	log, err := audit.New(audit.Options{DB: db, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	set := canary.New()
	address := "generic+https://hooks.example.com/incoming/" + set.New(canary.NotificationURL, "stored address")
	svc := newFakeNotifications(address)
	bus := events.New(clk)
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Clock: clk, Notifications: svc, Audit: log, Events: bus, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	return notificationsFixture{h: authztest.Authenticate(withTestContext(t, mux, "")), svc: svc, log: log, bus: bus, secrets: set, address: address}
}

// notificationCalls is one valid call per notification route (valid
// bodies, so a refusal is the owner check, not request validation).
func notificationCalls(t *testing.T) []authztest.Call {
	t.Helper()
	ifMatch := map[string]string{"If-Match": `"1"`}
	valid := map[string]authztest.Call{
		"list-notification-channels":       {},
		"create-notification-channel":      {Body: map[string]any{"name": "Pager", "address": "ntfy://ntfy.sh/pager-topic-1234"}},
		"get-notification-channel":         {},
		"update-notification-channel":      {Body: map[string]any{"name": "Ops team"}, Headers: ifMatch},
		"delete-notification-channel":      {Headers: ifMatch},
		"get-notification-channel-address": {},
		"create-notification-channel-test": {},
	}
	calls := authztest.Routes(t, map[string]string{"channelId": "c-1"}, "/api/v1/notification-channels")
	if len(calls) != len(valid) {
		t.Fatalf("notification routes: %+v", calls)
	}
	for i, c := range calls {
		v, ok := valid[c.OperationID]
		if !ok {
			t.Fatalf("unexpected route %s", c)
		}
		if c.Capability != "owner" {
			t.Errorf("%s: capability %s, want owner", c, c.Capability)
		}
		calls[i].Body, calls[i].Headers = v.Body, v.Headers
	}
	return calls
}

// Every notification route is the owner's: members with broad grants and
// the owner's own API tokens are refused.
func TestNotificationRoutesAreOwnerOnly(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("adam", "admins").Group("admins", "allow settings.manage @all", "allow settings.read @all", "allow registry.read @all").
		Member("rita", "restricted").
		Token("tok-1", "olga", "allow settings.read @all")
	calls := notificationCalls(t)
	f := newNotificationsFixture(t, pol)
	authztest.AssertOnly(t, f.h, "adam", nil, calls)
	authztest.AssertOnly(t, f.h, "rita", nil, calls)
	// The owner last: its calls change (and delete) the channel.
	var mutations, reads []authztest.Call
	for _, c := range calls {
		if c.Method == http.MethodGet {
			reads = append(reads, c)
		} else {
			mutations = append(mutations, c)
		}
	}
	authztest.AssertOnly(t, f.h, "olga", reads, nil)
	for _, c := range reads {
		c.Headers = map[string]string{authztest.TokenHeader: "tok-1"}
		if r := authztest.Do(t, f.h, "olga", c); r.Status != http.StatusForbidden || !strings.Contains(string(r.Body), CodeAPITokenNotAllowed) {
			t.Errorf("API token %s: %d %s", c, r.Status, r.Body)
		}
	}
	authztest.AssertOnly(t, f.h, "olga", mutations, nil)
	if r := authztest.Do(t, f.h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/notification-channels"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

func TestNotificationChannelsNeverShowTheAddressButTheReveal(t *testing.T) {
	f := newNotificationsFixture(t, authztest.New().Owner("olga"))
	changes := f.bus.Subscribe(16, func(e events.Event) bool { return e.Type == events.ResourceChanged })
	defer changes.Close()
	do := func(c authztest.Call) authztest.Response {
		return authztest.Do(t, f.h, "olga", c)
	}
	r := do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/notification-channels"})
	var page Page[NotificationChannel]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil || len(page.Items) != 1 {
		t.Fatalf("list: %d %s", r.Status, r.Body)
	}
	if c := page.Items[0]; c.Service != "generic" || c.Target != "hooks.example.com" || len(c.EventKinds) != 5 ||
		c.EnvironmentIDs == nil || c.Address.Fingerprint != "fp_0000000000000001" || c.Address.Version != 1 {
		t.Fatalf("%+v", c)
	}
	f.secrets.AssertClean(t, "list", r.Body)
	r = do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/notification-channels/c-1"})
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"1"` {
		t.Fatalf("get: %d %v", r.Status, r.Header)
	}
	f.secrets.AssertClean(t, "get", r.Body)

	// The reveal returns it (and only it) and is audited without it.
	r = do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/notification-channels/c-1/address"})
	var revealed NotificationChannelAddress
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &revealed) != nil || revealed.Address != f.address {
		t.Fatalf("reveal: %d", r.Status)
	}

	// Creating with a new secret address: the response and the audit
	// record never carry it.
	created := "ntfy://ntfy.sh/" + f.secrets.New(canary.NotificationURL, "created address")
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/notification-channels",
		Body: map[string]any{"name": "Pager", "address": created, "eventKinds": []string{"job_failed"}, "environmentIds": []string{}}})
	var c NotificationChannel
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &c) != nil || c.ID != "c-new" || !c.Enabled || !c.SendResolved ||
		!c.AllEnvironments || len(c.EventKinds) != 1 || r.Header.Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	f.secrets.AssertClean(t, "create response", r.Body)
	if f.svc.addresses["c-new"] != created {
		t.Fatal("the address did not reach the service")
	}

	recs, err := f.log.Records(testutil.Context(t), domain.AuditFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	f.secrets.AssertClean(t, "audit records", recs)
	var reveal, create *domain.AuditRecord
	for i := range recs {
		switch recs[i].OperationID {
		case "get-notification-channel-address":
			reveal = &recs[i]
		case "create-notification-channel":
			create = &recs[i]
		case "list-notification-channels", "get-notification-channel":
			t.Errorf("plain reads are audited: %s", recs[i].OperationID)
		}
	}
	if reveal == nil || reveal.Action != "notification_channel.reveal" || reveal.Category != domain.AuditCredentials ||
		len(reveal.Targets) != 1 || reveal.Targets[0] != (domain.AuditTarget{Type: "notification_channel", ID: "c-1"}) {
		t.Fatalf("reveal audit: %+v", reveal)
	}
	if create == nil || create.Action != "notification_channel.create" || !strings.Contains(string(create.Details), `"service":"ntfy"`) {
		t.Fatalf("create audit: %+v", create)
	}
	// Mutations reach live streams as settings changes (#23).
	select {
	case e := <-changes.C():
		if e.ResourceType != "notification_channel" || e.ResourceID != "c-new" || e.Attributes["op"] != "create" {
			t.Fatalf("event %+v", e)
		}
	default:
		t.Fatal("no change event")
	}
}

func TestNotificationEditsAndErrors(t *testing.T) {
	f := newNotificationsFixture(t, authztest.New().Owner("olga"))
	do := func(c authztest.Call) authztest.Response {
		return authztest.Do(t, f.h, "olga", c)
	}
	path := "/api/v1/notification-channels/c-1"
	if r := do(authztest.Call{Method: http.MethodPatch, Path: path, Body: map[string]any{"name": "x"}}); r.Status != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.Status)
	}
	r := do(authztest.Call{Method: http.MethodPatch, Path: path, Headers: map[string]string{"If-Match": `"1"`},
		Body: map[string]any{"enabled": false, "eventKinds": []string{"raid"}, "allEnvironments": false, "environmentIds": []string{"env-1"}}})
	var c NotificationChannel
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &c) != nil || c.Enabled || c.Revision != 2 || len(c.EventKinds) != 1 ||
		c.AllEnvironments || len(c.EnvironmentIDs) != 1 || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	// The environment choice is passed on explicitly, never inferred.
	if p := f.svc.lastPatch; p.AllEnvironments == nil || *p.AllEnvironments || p.EnvironmentIDs == nil || len(*p.EnvironmentIDs) != 1 {
		t.Fatalf("patch passed on %+v", p)
	}
	// Creating with environments restricts the channel to them.
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/notification-channels",
		Body: map[string]any{"name": "Lab", "address": "ntfy://ntfy.sh/lab-topic-12345", "environmentIds": []string{"env-1"}}})
	var created NotificationChannel
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &created) != nil || created.AllEnvironments ||
		len(created.EnvironmentIDs) != 1 {
		t.Fatalf("create with environments: %d %s", r.Status, r.Body)
	}
	if r := do(authztest.Call{Method: http.MethodPatch, Path: path, Headers: map[string]string{"If-Match": `"1"`},
		Body: map[string]any{"name": "y"}}); r.Status != http.StatusPreconditionFailed || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("stale: %d %v", r.Status, r.Header)
	}
	if r := do(authztest.Call{Method: http.MethodPatch, Path: path, Headers: map[string]string{"If-Match": `"2"`},
		Body: map[string]any{"eventKinds": []string{"weather"}}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown kind: %d", r.Status)
	}
	if r := do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/notification-channels",
		Body: map[string]any{"name": "ops", "address": "ntfy://ntfy.sh/x"}}); r.Status != http.StatusConflict ||
		!strings.Contains(string(r.Body), CodeNotificationChannelNameTaken) {
		t.Fatalf("name taken: %d %s", r.Status, r.Body)
	}
	// Tests: a failed delivery is a 200 with the class in words; too many
	// tests are a 429 with Retry-After.
	f.svc.result = notify.Result{ErrorClass: domain.NotifyErrAuth, Message: notify.Message(domain.NotifyErrAuth), At: time.Date(2026, 9, 30, 9, 1, 0, 0, time.UTC)}
	r = do(authztest.Call{Method: http.MethodPost, Path: path + "/tests"})
	var tr NotificationChannelTest
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &tr) != nil || tr.OK || tr.ErrorClass != "auth" || tr.Message == "" {
		t.Fatalf("test: %d %s", r.Status, r.Body)
	}
	f.svc.limited = true
	r = do(authztest.Call{Method: http.MethodPost, Path: path + "/tests"})
	if r.Status != http.StatusTooManyRequests || r.Header.Get("Retry-After") != "4" ||
		!strings.Contains(string(r.Body), CodeNotificationTestRateLimited) {
		t.Fatalf("rate limited: %d %v %s", r.Status, r.Header, r.Body)
	}
	for _, p := range []string{"/api/v1/notification-channels/nope", "/api/v1/notification-channels/nope/address"} {
		if r := do(authztest.Call{Method: http.MethodGet, Path: p}); r.Status != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
	if r := do(authztest.Call{Method: http.MethodDelete, Path: path, Headers: map[string]string{"If-Match": `"2"`}}); r.Status != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Status)
	}
}
