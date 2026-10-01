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

// fakeAlerts is an in-memory alert service (the API's authorization is
// under test).
type fakeAlerts struct {
	mu     sync.Mutex
	alerts map[string]domain.Alert
	by     []string
}

var alertsAt = time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)

func newFakeAlerts() *fakeAlerts {
	mk := func(id string, kind domain.NotificationEventKind, env, typ, res string) domain.Alert {
		return domain.Alert{ID: id, DedupeKey: id, Kind: kind, Severity: domain.AlertWarning, State: domain.AlertFiring, EnvironmentID: env,
			ResourceType: typ, ResourceID: res, Title: "alert " + id, Facts: map[string]string{}, StartedAt: alertsAt, UpdatedAt: alertsAt,
			LastSeenAt: alertsAt, Revision: 1}
	}
	job := mk("a-4", domain.NotifyJobFailed, "env-1", domain.AlertResourceJob, "job-1")
	job.JobKind, job.Targets = "stack.deploy", []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}}
	upd := mk("a-5", domain.NotifyUpdates, "env-1", domain.AlertResourceUpdatePolicy, "pol-1")
	upd.Targets = []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}}
	resolved := mk("a-6", domain.NotifyDiskHealth, "env-1", domain.AlertResourceDisk, "/dev/sdb")
	resolved.State, resolved.ResolvedAt, resolved.Resolution = domain.AlertResolved, &alertsAt, domain.AlertResolvedFixed
	list := []domain.Alert{
		mk("a-1", domain.NotifyDiskHealth, "env-1", domain.AlertResourceDisk, "/dev/sda"),
		mk("a-2", domain.NotifyRAID, "env-1", domain.AlertResourceRAID, "md0"),
		mk("a-3", domain.NotifyDiskHealth, "env-2", domain.AlertResourceDisk, "/dev/sda"),
		job, upd, resolved,
	}
	f := &fakeAlerts{alerts: map[string]domain.Alert{}}
	for _, a := range list {
		f.alerts[a.ID] = a
	}
	return f
}

func (f *fakeAlerts) List(_ context.Context, flt domain.AlertFilter, before string, limit int) ([]domain.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Alert
	for _, a := range f.alerts {
		switch flt.State {
		case domain.AlertListActive:
			if !a.Active() {
				continue
			}
		case domain.AlertListResolved:
			if a.State != domain.AlertResolved {
				continue
			}
		}
		if (flt.Kind != "" && a.Kind != flt.Kind) || (flt.EnvironmentID != "" && a.EnvironmentID != flt.EnvironmentID) ||
			(before != "" && a.ID >= before) {
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b domain.Alert) int { return strings.Compare(b.ID, a.ID) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeAlerts) Get(_ context.Context, id string) (domain.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.alerts[id]
	if !ok {
		return a, domain.ErrAlertNotFound
	}
	return a, nil
}

func (f *fakeAlerts) Dismiss(ctx context.Context, id, userID string) (domain.Alert, error) {
	out, err := f.DismissMany(ctx, []string{id}, userID, true)
	if err != nil {
		return domain.Alert{}, err
	}
	return out[0], nil
}

func (f *fakeAlerts) DismissMany(_ context.Context, ids []string, userID string, strict bool) ([]domain.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Alert
	for _, id := range ids {
		a, ok := f.alerts[id]
		if !ok {
			if strict {
				return nil, domain.ErrAlertNotFound
			}
			continue
		}
		if a.State != domain.AlertFiring {
			if strict {
				return nil, domain.ErrAlertNotFiring
			}
			continue
		}
		if a.DismissedAt == nil {
			a.DismissedAt, a.DismissedBy, a.DismissedByName, a.Revision = &alertsAt, userID, "Name of "+userID, a.Revision+1
			f.alerts[id] = a
			f.by = append(f.by, userID)
		}
		out = append(out, a)
	}
	return out, nil
}

type alertsFixture struct {
	h   http.Handler
	svc *fakeAlerts
	log *audit.Log
}

func newAlertsFixture(t *testing.T, pol *authztest.Policy) alertsFixture {
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
	svc := newFakeAlerts()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Clock: clk, Alerts: svc, Audit: log, Events: events.New(clk), Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	return alertsFixture{h: authztest.Authenticate(withTestContext(t, mux, "")), svc: svc, log: log}
}

func alertIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var page struct {
		Items []Alert `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range page.Items {
		out = append(out, a.ID)
	}
	slices.Sort(out)
	return out
}

func TestAlertsAreShownThroughTheirSource(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("sam", "sys").Group("sys", "allow environment.system.read @env:env-1").
		Member("jo", "jobs").Group("jobs", "allow job.read @all").
		Member("uma", "updates").Group("updates", "allow update_policy.read @all").
		Member("rita", "restricted")
	f := newAlertsFixture(t, pol)
	list := func(user, query string) []string {
		r := authztest.Do(t, f.h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/alerts" + query})
		if r.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", user, r.Status, r.Body)
		}
		return alertIDs(t, r.Body)
	}
	for _, c := range []struct {
		user, query string
		want        []string
	}{
		{"olga", "", []string{"a-1", "a-2", "a-3", "a-4", "a-5", "a-6"}},
		{"olga", "?state=active", []string{"a-1", "a-2", "a-3", "a-4", "a-5"}},
		{"olga", "?kind=raid", []string{"a-2"}},
		{"olga", "?environmentId=env-2", []string{"a-3"}},
		{"sam", "", []string{"a-1", "a-2", "a-6"}},
		{"jo", "", []string{"a-4"}},
		{"uma", "", []string{"a-5"}},
		{"rita", "", nil},
	} {
		if got := list(c.user, c.query); !slices.Equal(got, c.want) {
			t.Errorf("%s %s: %v, want %v", c.user, c.query, got, c.want)
		}
	}
	// A hidden alert is not found.
	if r := authztest.Do(t, f.h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/alerts/a-3"}); r.Status != http.StatusNotFound {
		t.Fatalf("%d", r.Status)
	}
	f.svc.mu.Lock()
	esc := f.svc.alerts["a-1"]
	esc.Escalation = 2
	f.svc.alerts["a-1"] = esc
	f.svc.mu.Unlock()
	r := authztest.Do(t, f.h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/alerts/a-1"})
	var a Alert
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &a) != nil || a.Link != "/environments/env-1?tab=system" || len(a.Actions) != 0 ||
		a.Escalation != 2 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, f.h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/alerts"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.Status)
	}
}

func TestDismissNeedsAlertDismissOnTheSource(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("sam", "sys").Group("sys", "allow environment.system.read @env:env-1").
		Member("dora", "ops").Group("ops", "allow environment.system.read @all", "allow alert.dismiss @env:env-1")
	f := newAlertsFixture(t, pol)
	dismiss := func(user, id string) authztest.Response {
		return authztest.Do(t, f.h, user, authztest.Call{Method: http.MethodPost, Path: "/api/v1/alerts/" + id + "/dismissals"})
	}
	if r := dismiss("sam", "a-1"); r.Status != http.StatusForbidden {
		t.Fatalf("without alert.dismiss: %d %s", r.Status, r.Body)
	}
	if r := dismiss("sam", "a-3"); r.Status != http.StatusNotFound {
		t.Fatalf("hidden: %d", r.Status)
	}
	if r := dismiss("dora", "a-3"); r.Status != http.StatusForbidden {
		t.Fatalf("outside the granted environment: %d", r.Status)
	}
	// The GET shows dora the action.
	var shown Alert
	if r := authztest.Do(t, f.h, "dora", authztest.Call{Method: http.MethodGet, Path: "/api/v1/alerts/a-1"}); json.Unmarshal(r.Body, &shown) != nil ||
		!slices.Contains(shown.Actions, "alert.dismiss") {
		t.Fatalf("%s", r.Body)
	}
	r := dismiss("dora", "a-1")
	var a Alert
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &a) != nil || !a.Dismissed || a.DismissedBy == nil || a.DismissedBy.ID != "dora" ||
		len(a.Actions) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := dismiss("dora", "a-6"); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodeAlertNotFiring) {
		t.Fatalf("resolved: %d %s", r.Status, r.Body)
	}
	recs, err := f.log.Records(testutil.Context(t), domain.AuditFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range recs {
		if rec.OperationID == "create-alert-dismissal" && rec.Action == "alert.dismiss" && rec.Outcome == domain.AuditSuccess &&
			len(rec.Targets) == 1 && rec.Targets[0].ID == "a-1" && strings.Contains(string(rec.Details), `"kind":"disk_health"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit record of the dismissal: %+v", recs)
	}
}

func TestDismissAllTakesWhatTheCallerMayDismiss(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("dora", "ops").Group("ops", "allow environment.system.read @all", "allow alert.dismiss @env:env-1")
	f := newAlertsFixture(t, pol)
	r := authztest.Do(t, f.h, "dora", authztest.Call{Method: http.MethodPost, Path: "/api/v1/alerts/dismissals", Body: map[string]any{}})
	var out AlertDismissals
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &out) != nil {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	slices.Sort(out.AlertIDs)
	if out.Dismissed != 2 || !slices.Equal(out.AlertIDs, []string{"a-1", "a-2"}) {
		t.Fatalf("%+v", out)
	}
	// Listed IDs: only those the caller may dismiss.
	r = authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/alerts/dismissals",
		Body: map[string]any{"alertIds": []string{"a-3", "a-6", "nope"}}})
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &out) != nil || out.Dismissed != 1 || out.AlertIDs[0] != "a-3" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}
