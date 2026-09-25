package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// apiPolicySource is a fixed policy table for the schedule routes.
type apiPolicySource struct {
	kind     jobspec.Spec
	policies []scheduler.PolicySchedule
}

func (s apiPolicySource) Schedules(context.Context) ([]scheduler.PolicySchedule, error) {
	return s.policies, nil
}

func (s apiPolicySource) Validate(context.Context, string) error { return nil }

func (s apiPolicySource) Jobs(_ context.Context, due scheduler.Due) ([]jobs.Request, error) {
	for _, p := range s.policies {
		if p.PolicyID == due.PolicyID {
			return []jobs.Request{{Kind: s.kind.Kind, EnvironmentID: p.EnvironmentID}}, nil
		}
	}
	return nil, scheduler.Reject(scheduler.RejectPolicyNotFound, "gone")
}

type scheduleFixture struct {
	h     http.Handler
	sched *scheduler.Service
	clk   *clock.Fake
}

func newScheduleFixture(t *testing.T, pol *authztest.Policy) scheduleFixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	eng, err := jobs.New(jobs.Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Dispatcher: jobstest.New()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	sched, err := scheduler.New(scheduler.Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Jobs: eng})
	if err != nil {
		t.Fatal(err)
	}
	prune, _ := jobspec.Lookup(jobspec.PruneRun)
	backup, _ := jobspec.Lookup(jobspec.ManagerVerify)
	if err := sched.Register(scheduler.KindPrune, apiPolicySource{kind: prune, policies: []scheduler.PolicySchedule{
		{PolicyID: "pol-a", Name: "Env 1 cleanup", EnvironmentID: "env-1", Cron: "30 2 * * *", TimeZone: "America/New_York", Enabled: true},
		{PolicyID: "pol-b", Name: "Env 2 cleanup", EnvironmentID: "env-2", Cron: "0 3 * * 0", TimeZone: "UTC", Enabled: false},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Register(scheduler.KindBackupVerification, apiPolicySource{kind: backup, policies: []scheduler.PolicySchedule{
		{PolicyID: "repo-1", Name: "Offsite", Cron: "0 5 * * 0", TimeZone: "Europe/Berlin", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Clock: clk, Schedules: sched, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	return scheduleFixture{h: authztest.Authenticate(withTestContext(t, mux, "")), sched: sched, clk: clk}
}

func scheduleRoutes(t *testing.T) []authztest.Call {
	calls := authztest.Routes(t, nil, "/api/v1/schedule-defaults", "/api/v1/schedules")
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": "*"}
		if calls[i].OperationID == "update-schedule-defaults" {
			calls[i].Body = map[string]any{"timeZone": "Europe/Berlin"}
		}
	}
	if len(calls) != 3 { // the preview is for every signed-in caller
		t.Fatalf("schedule routes: %+v", calls)
	}
	return calls
}

func listScheduleIDs(t *testing.T, h http.Handler, user string) []string {
	t.Helper()
	r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/schedules"})
	var page Page[Schedule]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil {
		t.Fatalf("list as %s: %d %s", user, r.Status, r.Body)
	}
	var out []string
	for _, s := range page.Items {
		out = append(out, s.PolicyID)
	}
	slices.Sort(out)
	return out
}

// The cross-policy view lists an entry only with schedule.read AND the
// policy's own read capability on it; settings routes need the settings
// capabilities; previews need only authentication.
func TestScheduleRoutesAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("mia", "ops").Group("ops", "allow schedule.read @env:env-1", "allow maintenance_policy.read @all").
		Member("sam", "sched").Group("sched", "allow schedule.read @all").
		Member("bea", "backup").Group("backup", "allow schedule.read @all", "allow backup_repository.read @backup_repository:repo-1").
		Member("rita", "restricted").
		Member("sue", "settings").Group("settings", "allow settings.read @all", "allow settings.manage @all")
	f := newScheduleFixture(t, pol)

	if got := listScheduleIDs(t, f.h, "olga"); !slices.Equal(got, []string{"pol-a", "pol-b", "repo-1"}) {
		t.Fatalf("owner sees %v", got)
	}
	if got := listScheduleIDs(t, f.h, "mia"); !slices.Equal(got, []string{"pol-a"}) {
		t.Fatalf("mia sees %v", got)
	}
	if got := listScheduleIDs(t, f.h, "sam"); len(got) != 0 {
		t.Fatalf("schedule.read alone shows %v", got)
	}
	if got := listScheduleIDs(t, f.h, "bea"); !slices.Equal(got, []string{"repo-1"}) {
		t.Fatalf("bea sees %v", got)
	}

	calls := scheduleRoutes(t)
	allowed, denied := authztest.Split(calls, "settings.read", "settings.manage")
	authztest.AssertOnly(t, f.h, "sue", allowed, denied)
	authztest.AssertOnly(t, f.h, "rita", nil, calls)
	if r := authztest.Do(t, f.h, "rita", authztest.Call{Method: http.MethodPost, Path: "/api/v1/schedules/previews",
		Body: map[string]any{"cron": "0 2 * * *"}}); r.Status != http.StatusOK {
		t.Fatalf("restricted preview: %d", r.Status)
	}
	if r := authztest.Do(t, f.h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/schedules/previews",
		Body: map[string]any{"cron": "0 2 * * *"}}); r.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous preview: %d", r.Status)
	}
}

func TestListSchedulesShowsNextRunAndHistory(t *testing.T) {
	f := newScheduleFixture(t, authztest.New().Owner("olga"))
	// 2026-03-08 02:30 does not exist in New York: the run is at 03:00 EDT.
	f.clk.Set(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC))
	if err := f.sched.Tick(testutil.Context(t)); err != nil {
		t.Fatal(err)
	}
	r := authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/schedules?kind=prune&enabled=true"})
	var page Page[Schedule]
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil || len(page.Items) != 1 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	s := page.Items[0]
	if s.PolicyID != "pol-a" || s.KindLabel != "Docker prune" || s.CatchUp != "skip" || s.NextRun == nil ||
		s.NextRun.Local != "2026-03-09T02:30" || s.NextRun.DST != "none" || len(s.RecentRuns) != 1 {
		t.Fatalf("%+v", s)
	}
	run := s.RecentRuns[0]
	if run.Outcome != "enqueued" || run.Result != "active" || len(run.Jobs) != 1 || run.Jobs[0].Kind != "prune.run" ||
		!run.ScheduledFor.Equal(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", run)
	}
	// Pagination with filters: one per page, the cursor is bound to them.
	r = authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/schedules?limit=1"})
	if err := json.Unmarshal(r.Body, &page); err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("%s", r.Body)
	}
	r = authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/schedules?kind=prune&cursor=" + page.NextCursor})
	if r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("cursor reused with other filters: %d", r.Status)
	}
}

func TestScheduleDefaultsRoutes(t *testing.T) {
	f := newScheduleFixture(t, authztest.New().Owner("olga"))
	r := authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/schedule-defaults"})
	var d ScheduleDefaults
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"1"` || json.Unmarshal(r.Body, &d) != nil || d.TimeZone != "UTC" || len(d.Kinds) != 5 {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Body)
	}
	patch := func(ifMatch string, body any) authztest.Response {
		h := map[string]string{}
		if ifMatch != "" {
			h["If-Match"] = ifMatch
		}
		return authztest.Do(t, f.h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/schedule-defaults", Body: body, Headers: h})
	}
	if r := patch("", map[string]any{"timeZone": "Europe/Berlin"}); r.Status != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", r.Status)
	}
	r = patch(`"1"`, map[string]any{"timeZone": "Mars/Base", "crons": map[string]string{"prune": "0 0 30 2 *", "nope": "* * * * *"}})
	if r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid: %d %s", r.Status, r.Body)
	}
	for _, field := range []string{`"body.timeZone"`, `"body.crons.prune"`, `"body.crons.nope"`, "never matches"} {
		if !strings.Contains(string(r.Body), field) {
			t.Errorf("422 lacks %s: %s", field, r.Body)
		}
	}
	r = patch(`"1"`, map[string]any{"timeZone": "Europe/Berlin", "crons": map[string]string{"backup": "15 1 * * *"}})
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"2"` || json.Unmarshal(r.Body, &d) != nil || d.TimeZone != "Europe/Berlin" {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	if d.Kinds[0].Kind != "backup" || d.Kinds[0].Cron != "15 1 * * *" || d.Kinds[0].Suggested != "0 2 * * *" {
		t.Fatalf("%+v", d.Kinds[0])
	}
	if r := patch(`"1"`, map[string]any{"timeZone": "UTC"}); r.Status != http.StatusPreconditionFailed || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("stale: %d %v", r.Status, r.Header)
	}
}

func TestSchedulePreviewRoute(t *testing.T) {
	f := newScheduleFixture(t, authztest.New().Member("rita", "restricted"))
	r := authztest.Do(t, f.h, "rita", authztest.Call{Method: http.MethodPost, Path: "/api/v1/schedules/previews", Body: map[string]any{
		"cron": "30  2 * * *", "timeZone": "America/New_York", "kind": "backup", "from": "2026-03-07T00:00:00Z", "count": 3}})
	var p SchedulePreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &p) != nil {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if p.Cron != "30 2 * * *" || p.CatchUp != "once" || len(p.Runs) != 3 || len(p.Notes) < 4 {
		t.Fatalf("%+v", p)
	}
	gap := p.Runs[1]
	if gap.DST != "gap" || gap.Local != "2026-03-08T02:30" || !gap.UTC.Equal(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)) ||
		!strings.Contains(string(r.Body), `"at":"2026-03-08T03:00:00-04:00"`) || gap.DSTNote == "" {
		t.Fatalf("%+v %s", gap, r.Body)
	}
	r = authztest.Do(t, f.h, "rita", authztest.Call{Method: http.MethodPost, Path: "/api/v1/schedules/previews", Body: map[string]any{
		"cron": "61 2 * * *", "timeZone": "Nowhere/City"}})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), `"body.cron"`) ||
		!strings.Contains(string(r.Body), `"body.timeZone"`) || !strings.Contains(string(r.Body), "minute: 61 is outside 0-59") {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r = authztest.Do(t, f.h, "rita", authztest.Call{Method: http.MethodPost, Path: "/api/v1/schedules/previews", Body: map[string]any{
		"cron": "0 2 * * *", "kind": "nope"}})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), `"body.kind"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestValidateSchedule(t *testing.T) {
	if err := ValidateSchedule("0 2 * * *", "UTC", "body.schedule"); err != nil {
		t.Fatal(err)
	}
	err := ValidateSchedule("0 2 * *", "", "body.schedule")
	var e *Error
	if !errors.As(err, &e) || e.status != http.StatusUnprocessableEntity || len(e.Details) != 2 ||
		e.Details[0].Field != "body.schedule.cron" || e.Details[1].Field != "body.schedule.timeZone" {
		t.Fatalf("%+v", err)
	}
}
