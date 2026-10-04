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
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// inAppChannel is the In App channel as a new installation has it.
func inAppChannel() domain.NotificationChannel {
	return domain.NotificationChannel{ID: domain.InAppChannelID, Name: domain.InAppChannelName, Service: domain.InAppService,
		Enabled: true, Subscriptions: domain.AllNotificationSubscriptions(), AllEnvironments: true}
}

func (f *fakeAlerts) EnvironmentName(_ context.Context, id string) string {
	if id == "" {
		return ""
	}
	return "name of " + id
}

// fakeNotificationEvents is an in-memory notification and threshold
// service (the API's authorization and shapes are under test).
type fakeNotificationEvents struct {
	mu       sync.Mutex
	list     []domain.Notification
	settings domain.AlertSettings
	updates  int
	inApp    domain.NotificationChannel
	filter   domain.NotificationFilter
}

func newFakeNotificationEvents() *fakeNotificationEvents {
	mk := func(id string, kind domain.NotificationEventKind, outcome domain.NotificationOutcome, env string, jobKind domain.JobKind,
		targets ...domain.JobTarget) domain.Notification {
		return domain.Notification{ID: id, Kind: kind, Outcome: outcome, EnvironmentID: env, JobID: "job-" + id, JobKind: jobKind,
			Targets: targets, Origin: domain.OriginScheduled, Title: "notification " + id, Facts: map[string]string{}, CreatedAt: alertsAt}
	}
	prune := mk("n-1", domain.NotifyPrune, domain.OutcomeSuccess, "env-1", "prune.run",
		domain.JobTarget{Type: "maintenance_policy", ID: "mp-1"})
	prune.Facts = map[string]string{"reclaimedBytes": "4294967296", "removed": "3", "imagesRemoved": "3", "imagesBytes": "4294967296"}
	return &fakeNotificationEvents{
		list: []domain.Notification{
			prune,
			mk("n-2", domain.NotifyBackup, domain.OutcomeFailure, "env-2", "backup.run", domain.JobTarget{Type: domain.TargetStack, ID: "s2"}),
			mk("n-3", domain.NotifyUpdates, domain.OutcomeSuccess, "env-1", "update.run", domain.JobTarget{Type: domain.TargetStack, ID: "s1"}),
		},
		settings: domain.AlertSettings{Thresholds: domain.DefaultAlertThresholds(), Revision: 1, UpdatedAt: alertsAt},
		inApp:    inAppChannel(),
	}
}

func (f *fakeNotificationEvents) Notifications(_ context.Context, flt domain.NotificationFilter, before string, limit int) ([]domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filter = flt
	var out []domain.Notification
	for _, n := range f.list {
		if (flt.Kind != "" && n.Kind != flt.Kind) || (flt.Outcome != "" && n.Outcome != flt.Outcome) ||
			(flt.EnvironmentID != "" && n.EnvironmentID != flt.EnvironmentID) || (before != "" && n.ID >= before) {
			continue
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b domain.Notification) int { return strings.Compare(b.ID, a.ID) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeNotificationEvents) Settings(context.Context) (domain.AlertSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, nil
}

func (f *fakeNotificationEvents) UpdateSettings(_ context.Context, revision int64, next domain.AlertSettings) (domain.AlertSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision != f.settings.Revision {
		return domain.AlertSettings{}, domain.ErrRevisionConflict
	}
	if t := next.Thresholds; t.MemoryWarning > 0 && t.MemoryCritical > 0 && t.MemoryWarning >= t.MemoryCritical {
		return domain.AlertSettings{}, &domain.FieldError{Field: "memoryWarning", Message: "must be below the critical level"}
	}
	f.updates++
	next.Revision, next.UpdatedAt = f.settings.Revision+1, alertsAt
	f.settings = next
	return next, nil
}

func (f *fakeNotificationEvents) InAppChannel(context.Context) (domain.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inApp, nil
}

func (f *fakeNotificationEvents) EnvironmentName(_ context.Context, id string) string {
	if id == "" {
		return ""
	}
	return "name of " + id
}

func newNotificationEventsFixture(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeNotificationEvents, *audit.Log) {
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
	clk := clock.NewFake(alertsAt)
	log, err := audit.New(audit.Options{DB: db, Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	svc := newFakeNotificationEvents()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Clock: clk, NotificationEvents: svc, Audit: log, Events: events.New(clk),
		Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), svc, log
}

func TestNotificationsAreShownThroughTheirJob(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("jo", "jobs").Group("jobs", "allow job.read @env:env-1").
		Member("rita", "restricted")
	h, _, _ := newNotificationEventsFixture(t, pol)
	list := func(user, query string) []Notification {
		r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/notifications" + query})
		if r.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", user, r.Status, r.Body)
		}
		var page struct {
			Items []Notification `json:"items"`
		}
		if err := json.Unmarshal(r.Body, &page); err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	ids := func(ns []Notification) string {
		var out []string
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		user, query, want string
	}{
		{"olga", "", "n-3,n-2,n-1"},
		{"olga", "?kind=prune", "n-1"},
		{"olga", "?outcome=failure", "n-2"},
		{"olga", "?environmentId=env-1", "n-3,n-1"},
		{"jo", "", "n-3,n-1"},
		{"rita", "", ""},
	} {
		if got := ids(list(c.user, c.query)); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.user, c.query, got, c.want)
		}
	}
	// The prune's numbers come as labeled fields, with its environment.
	n := list("olga", "?kind=prune")[0]
	if n.Link != "/jobs/job-n-1" || n.Detail != "Removed 3 objects and reclaimed 4 GiB." || len(n.Fields) < 3 ||
		n.Fields[0] != (NotificationField{Name: "Environment", Value: "name of env-1", Inline: true}) {
		t.Fatalf("%+v", n)
	}
	if !slices.Contains(n.Fields, NotificationField{Name: "Images", Value: "3 images · 4 GiB", Inline: true}) {
		t.Fatalf("%+v", n.Fields)
	}
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/notifications?kind=disk_health"})
	if r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("an alert kind: %d", r.Status)
	}
}

// inApp lists only the notifications the In App channel shows, still
// filtered by job.read.
func TestInAppNotificationsFollowTheInAppChannel(t *testing.T) {
	pol := authztest.New().Owner("olga").Member("jo", "jobs").Group("jobs", "allow job.read @env:env-1")
	h, svc, _ := newNotificationEventsFixture(t, pol)
	ids := func(user, query string) string {
		r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/notifications" + query})
		var page struct {
			Items []Notification `json:"items"`
		}
		if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil {
			t.Fatalf("%s: %d %s", user, r.Status, r.Body)
		}
		var out []string
		for _, n := range page.Items {
			out = append(out, n.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids("olga", "?inApp=true"); got != "n-3,n-2,n-1" {
		t.Fatalf("everything: %s", got)
	}
	if got := ids("jo", "?inApp=true"); got != "n-3,n-1" {
		t.Fatalf("jo: %s", got)
	}
	svc.mu.Lock()
	svc.inApp.Subscriptions = domain.NotificationSubscriptions{domain.NotifyPrune: {domain.OutcomeSuccess},
		domain.NotifyBackup: {domain.OutcomeFailure}}
	svc.inApp.AllEnvironments, svc.inApp.EnvironmentIDs = false, []string{"env-1"}
	svc.mu.Unlock()
	if got := ids("olga", "?inApp=true"); got != "n-1" {
		t.Fatalf("prune successes of env-1: %s", got)
	}
	if got := ids("olga", ""); got != "n-3,n-2,n-1" {
		t.Fatalf("without inApp: %s", got)
	}
	since := alertsAt.Add(-time.Hour)
	ids("olga", "?inApp=true&since="+since.Format(time.RFC3339))
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if !svc.filter.Since.Equal(since) {
		t.Fatalf("since: %v", svc.filter.Since)
	}
}

func TestAlertThresholdsAreTheOwners(t *testing.T) {
	pol := authztest.New().Owner("olga").Member("adam", "admins").Group("admins", "allow settings.read @all", "allow settings.manage @all")
	h, svc, log := newNotificationEventsFixture(t, pol)
	path := "/api/v1/alert-settings"
	if r := authztest.Do(t, h, "adam", authztest.Call{Method: http.MethodGet, Path: path}); r.Status != http.StatusForbidden {
		t.Fatalf("admin: %d", r.Status)
	}
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: path})
	var got AlertSettings
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil || r.Header.Get("ETag") != `"1"` ||
		got.Thresholds.TemperatureWarning != 80 || got.Thresholds.DiskSpaceCritical != 95 || got.Overrides == nil {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	body := map[string]any{
		"thresholds": map[string]any{"temperatureWarning": 75, "temperatureCritical": 85, "diskSpaceWarning": 80,
			"diskSpaceCritical": 90, "memoryWarning": 0, "memoryCritical": 98},
		"overrides": []map[string]any{{"environmentId": "env-1", "diskSpaceWarning": 0}},
	}
	if r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: path, Body: body}); r.Status != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.Status)
	}
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: path, Headers: map[string]string{"If-Match": `"1"`}, Body: body})
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &got) != nil || r.Header.Get("ETag") != `"2"` ||
		got.Thresholds.TemperatureWarning != 75 || got.Thresholds.MemoryWarning != 0 || len(got.Overrides) != 1 ||
		got.Overrides[0].DiskSpaceWarning == nil || *got.Overrides[0].DiskSpaceWarning != 0 || got.Overrides[0].MemoryCritical != nil {
		t.Fatalf("put: %d %s", r.Status, r.Body)
	}
	// An override's absent level keeps the default; 0 turns it off.
	if eff := svc.settings.For("env-1"); eff.DiskSpaceWarning != 0 || eff.DiskSpaceCritical != 90 {
		t.Fatalf("%+v", eff)
	}
	if r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: path, Headers: map[string]string{"If-Match": `"1"`},
		Body: body}); r.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", r.Status)
	}
	body["thresholds"].(map[string]any)["memoryWarning"] = 99
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: path, Headers: map[string]string{"If-Match": `"2"`}, Body: body})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "body.memoryWarning") {
		t.Fatalf("invalid: %d %s", r.Status, r.Body)
	}
	body["thresholds"].(map[string]any)["temperatureWarning"] = 151
	if r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPut, Path: path, Headers: map[string]string{"If-Match": `"2"`},
		Body: body}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("out of range: %d", r.Status)
	}
	if svc.updates != 1 {
		t.Fatalf("updates %d", svc.updates)
	}
	recs, err := log.Records(testutil.Context(t), domain.AuditFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range recs {
		if rec.OperationID == "update-alert-settings" && rec.Outcome == domain.AuditSuccess {
			found = true
		}
	}
	if !found {
		t.Fatalf("not audited: %+v", recs)
	}
}
