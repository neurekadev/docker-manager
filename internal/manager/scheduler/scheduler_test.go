package scheduler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/cron"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// A due schedule enqueues its job as the manager service identity with
// origin scheduled, the policy ID and the run's idempotency key; the
// creator's (missing) grants play no role: the engine has no authorizer, so
// no user could enqueue or dispatch anything.
func TestDueRunEnqueuesAsServiceIdentity(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pol-1", Cron: "0 3 * * *", Enabled: true}, CreatedBy: "alice"})
	h.tick()
	sc := h.schedule(KindPrune, "pol-1")
	if sc.NextRunAt == nil || !sc.NextRunAt.Equal(at("2026-03-01T03:00:00Z")) || !sc.Cursor.Equal(at("2026-03-01T00:00:00Z")) {
		t.Fatalf("schedule %+v", sc)
	}
	h.tickAt("2026-03-01T02:59:59Z")
	if n := len(h.allJobs()); n != 0 {
		t.Fatalf("%d jobs before the due time", n)
	}
	h.tickAt("2026-03-01T03:00:00Z")
	js := h.allJobs()
	if len(js) != 1 {
		t.Fatalf("jobs %v", js)
	}
	j := js[0]
	if j.Kind != jobspec.PruneRun || j.Origin != domain.OriginScheduled || j.InitiatorUserID != "" || j.InitiatorTokenID != "" ||
		j.PolicyID != "pol-1" || j.IdempotencyKey != "prune:pol-1:2026-03-01T03:00:00Z#0" || j.EnvironmentID != "env-1" {
		t.Fatalf("job %+v", j)
	}
	sc = h.schedule(KindPrune, "pol-1")
	if !sc.NextRunAt.Equal(at("2026-03-02T03:00:00Z")) || !sc.Cursor.Equal(at("2026-03-01T03:00:00Z")) {
		t.Fatalf("after the run: %+v", sc)
	}
	runs := h.history(KindPrune, "pol-1")
	if len(runs) != 1 || runs[0].Outcome != domain.RunEnqueued || len(runs[0].Jobs) != 1 || runs[0].Jobs[0].JobID != j.ID ||
		runs[0].Result() != "active" || runs[0].IdempotencyKey != "prune:pol-1:2026-03-01T03:00:00Z" {
		t.Fatalf("history %+v", runs)
	}
	// The job is dispatched once the agent is online, as the service identity.
	h.disp.Connect("env-1")
	h.dispatch()
	if got, _ := h.eng.Get(h.ctx, j.ID); got.State != domain.JobDispatched {
		t.Fatalf("job not dispatched: %s %s", got.State, got.ErrorClass)
	}
	// The engine audited the job as the service identity.
	recs, err := h.audit.Records(h.ctx, domain.AuditFilter{JobID: j.ID, Ascending: true})
	if err != nil || len(recs) == 0 || recs[0].Action != "job.queued" || recs[0].Actor.Kind != domain.AuditActorService {
		t.Fatalf("audit %+v %v", recs, err)
	}
}

// The policy creator being disabled or deleted does not stop an enabled
// schedule: scheduled work never runs as (or checks) the creator.
func TestScheduleContinuesAfterCreatorIsGone(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.disp.Connect("env-1")
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pol-1", Cron: "0 * * * *", Enabled: true}, CreatedBy: "alice"})
	h.tick()
	// alice is removed: the policy (an instance resource) stays; nothing in
	// the scheduler refers to her.
	h.sources[KindPrune].edit("pol-1", func(p *testPolicy) { p.CreatedBy = "" })
	for _, ts := range []string{"2026-03-01T01:00:00Z", "2026-03-01T02:00:00Z"} {
		h.tickAt(ts)
		h.dispatch()
		h.finishJobs(domain.JobSucceeded)
	}
	js := h.allJobs()
	if len(js) != 2 {
		t.Fatalf("jobs %v", jobTimes(js))
	}
	for _, j := range js {
		if j.State != domain.JobSucceeded || j.Origin != domain.OriginScheduled || j.InitiatorUserID != "" {
			t.Fatalf("job %+v", j)
		}
	}
	runs := h.history(KindPrune, "pol-1")
	if len(runs) != 2 || runs[0].Result() != "succeeded" || runs[1].Result() != "succeeded" ||
		runs[0].Jobs[0].FinishedAt == nil {
		t.Fatalf("history %+v", runs)
	}
}

// finishJobs completes every dispatched job with the given outcome through
// the agent protocol path (ack + result).
func (h *harness) finishJobs(state domain.JobState) {
	h.t.Helper()
	for _, f := range h.disp.Drain("env-1") {
		completeFrame(h, f, state)
	}
}

// Missed runs after downtime: a kind that catches up enqueues exactly one
// catch-up run and records the missed instants; a kind that skips records
// them and enqueues nothing.
func TestDowntimeCatchUpOnceAndSkip(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindBackup, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bk", Cron: "0 2 * * *", Enabled: true}})
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
	h.tick()
	// The manager is down from 2026-03-01T00:00 until 2026-03-04T10:00:
	// backup instants 03-01..03-04 02:00 and prune instants 03-01..03-04
	// 03:00 were missed.
	h.clk.Set(at("2026-03-04T10:00:00Z"))
	h.start() // restart: new engine and scheduler over the same database
	h.tick()
	js := h.allJobs()
	if len(js) != 1 || js[0].Kind != jobspec.BackupRun || js[0].IdempotencyKey != "backup:bk:2026-03-04T02:00:00Z#0" {
		t.Fatalf("jobs after downtime: %+v", js)
	}
	bk := h.history(KindBackup, "bk")
	mustEqual(t, "backup history", instants(bk), []string{"2026-03-03T02:00:00Z missed", "2026-03-04T02:00:00Z enqueued"})
	if bk[0].MissedCount != 3 || !bk[0].MissedFrom.Equal(at("2026-03-01T02:00:00Z")) || !strings.Contains(bk[0].Reason, "one catch-up run replaces them") {
		t.Fatalf("missed row %+v", bk[0])
	}
	if !bk[1].CatchUp || bk[1].MissedCount != 4 || !strings.Contains(bk[1].Reason, "catch-up run") {
		t.Fatalf("catch-up row %+v", bk[1])
	}
	pr := h.history(KindPrune, "pr")
	mustEqual(t, "prune history", instants(pr), []string{"2026-03-04T03:00:00Z missed"})
	if pr[0].MissedCount != 4 || !strings.Contains(pr[0].Reason, "do not catch up") || pr[0].ErrorClass != "missed" {
		t.Fatalf("prune missed row %+v", pr[0])
	}
	if sc := h.schedule(KindPrune, "pr"); !sc.NextRunAt.Equal(at("2026-03-05T03:00:00Z")) {
		t.Fatalf("prune next run %v", sc.NextRunAt)
	}
	if sc := h.schedule(KindBackup, "bk"); !sc.NextRunAt.Equal(at("2026-03-05T02:00:00Z")) {
		t.Fatalf("backup next run %v", sc.NextRunAt)
	}
	// Ticking again enqueues nothing more (no storm).
	h.tickAt("2026-03-04T10:05:00Z")
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs after another pass", n)
	}
	if acts := h.auditActions(); !slices.Contains(acts, "schedule.run_missed") {
		t.Fatalf("audit %v", acts)
	}
}

// A frequent schedule after a long downtime: counting stops at a bound,
// the latest instant is still found, and one row (plus at most one
// catch-up run) is written.
func TestLongDowntimeOfFrequentSchedule(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "* * * * *", Enabled: true}})
	h.policy(KindBackup, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bk", Cron: "*/2 * * * *", Enabled: true}})
	h.tick()
	h.clk.Set(at("2026-03-31T12:00:30Z"))
	h.start()
	h.tick()
	pr := h.history(KindPrune, "pr")
	mustEqual(t, "prune", instants(pr), []string{"2026-03-31T11:59:00Z missed", "2026-03-31T12:00:00Z enqueued"})
	if !strings.HasPrefix(pr[0].Reason, "more than 9999 scheduled runs between 2026-03-01T00:01:00Z and 2026-03-31T11:59:00Z") {
		t.Fatalf("%+v", pr[0])
	}
	bk := h.history(KindBackup, "bk")
	mustEqual(t, "backup", instants(bk), []string{"2026-03-31T11:58:00Z missed", "2026-03-31T12:00:00Z enqueued"})
	if bk[1].CatchUp {
		t.Fatalf("the 12:00 run is on time: %+v", bk[1])
	}
	if sc := h.schedule(KindPrune, "pr"); !sc.NextRunAt.Equal(at("2026-03-31T12:01:00Z")) {
		t.Fatalf("%+v", sc)
	}
	if n := len(h.allJobs()); n != 2 {
		t.Fatalf("%d jobs", n)
	}
	// A weekly schedule (centuries "down") resolves its latest instants
	// through the window search too.
	spec, now := cron.MustParse("0 5 * * 0"), at("2230-06-01T00:00:00Z")
	d := dueInstants(spec, time.UTC, at("2026-03-01T00:00:00Z"), now)
	next, _ := spec.Next(d.latest, time.UTC)
	afterPrev, _ := spec.Next(d.prev, time.UTC)
	if !d.more || d.count != maxMissedScan || d.latest.After(now) || !next.At.After(now) || !afterPrev.At.Equal(d.latest) {
		t.Fatalf("%+v", d)
	}
}

// Restarting right at a due time: the current run is on time; earlier
// instants missed during the downtime are recorded once.
func TestRestartOnTimeRecordsEarlierMissed(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
	h.tick()
	h.clk.Set(at("2026-03-03T03:01:00Z")) // within the grace period of 03-03 03:00
	h.start()
	h.tick()
	pr := h.history(KindPrune, "pr")
	mustEqual(t, "history", instants(pr), []string{"2026-03-02T03:00:00Z missed", "2026-03-03T03:00:00Z enqueued"})
	if pr[1].CatchUp || pr[0].MissedCount != 2 || !strings.Contains(pr[0].Reason, "started on time") {
		t.Fatalf("history %+v", pr)
	}
}

// Restarts never enqueue a run twice: a new runner over the same database
// keeps the persisted next run, and re-processing a recorded instant is a
// no-op.
func TestRestartDoesNotDuplicate(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "*/30 * * * *", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-01T00:30:00Z")
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	for range 3 {
		h.start()
		h.tick()
	}
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs after restarts", n)
	}
	sc := h.schedule(KindPrune, "pr")
	if !sc.NextRunAt.Equal(at("2026-03-01T01:00:00Z")) || !sc.Cursor.Equal(at("2026-03-01T00:30:00Z")) {
		t.Fatalf("persisted state %+v", sc)
	}
	// Processing the same instant again (a stale cursor) cannot enqueue a
	// second run: the run row is unique per schedule and instant.
	sc.Cursor = at("2026-03-01T00:00:00Z")
	sc.NextRunAt = new(at("2026-03-01T00:30:00Z"))
	if err := store.UpdateSchedule(h.ctx, h.db, &sc); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.tick()
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs after re-processing the instant", n)
	}
	h.cancelAll()
	h.tickAt("2026-03-01T01:00:00Z")
	if n := len(h.allJobs()); n != 2 {
		t.Fatalf("%d jobs at the next instant", n)
	}
}

// A crash after the run was recorded but before its job was enqueued: the
// next start enqueues it exactly once (on time) or records it as missed
// (too late for a kind that does not catch up).
func TestCrashBeforeEnqueue(t *testing.T) {
	for _, tc := range []struct {
		name, restart string
		wantJobs      int
		wantOutcome   domain.ScheduleOutcome
	}{
		{"restart within grace", "2026-03-01T03:02:00Z", 1, domain.RunEnqueued},
		{"restart too late for prune", "2026-03-01T04:00:00Z", 0, domain.RunMissed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, at("2026-03-01T00:00:00Z"))
			h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
			h.tick()
			h.clk.Set(at("2026-03-01T03:00:00Z"))
			// The first half of a pass (record the due instant), then "crash".
			k, _ := h.s.Kind(KindPrune)
			if err := h.s.advance(h.ctx, k, h.schedule(KindPrune, "pr"), h.s.now()); err != nil {
				t.Fatal(err)
			}
			if runs := h.history(KindPrune, "pr"); len(runs) != 1 || runs[0].Outcome != domain.RunPending {
				t.Fatalf("pending %+v", runs)
			}
			h.clk.Set(at(tc.restart))
			h.start()
			h.tick()
			h.tick()
			if n := len(h.allJobs()); n != tc.wantJobs {
				t.Fatalf("%d jobs", n)
			}
			if runs := h.history(KindPrune, "pr"); len(runs) != 1 || runs[0].Outcome != tc.wantOutcome {
				t.Fatalf("history %+v", runs)
			}
		})
	}
}

// A crash after the jobs were enqueued but before the run was linked to
// them: the next start finds the jobs by their idempotency keys and links
// them instead of enqueueing again (also for multi-job runs, and when the
// policy's input changed meanwhile).
func TestCrashAfterEnqueue(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.sources[KindBackup].JobsPerRun = 2
	h.policy(KindBackup, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bk", Cron: "0 2 * * *", Enabled: true}})
	h.tick()
	h.clk.Set(at("2026-03-01T02:00:00Z"))
	k, _ := h.s.Kind(KindBackup)
	if err := h.s.advance(h.ctx, k, h.schedule(KindBackup, "bk"), h.s.now()); err != nil {
		t.Fatal(err)
	}
	run := h.history(KindBackup, "bk")[0]
	// The first of the two jobs was enqueued, then the manager crashed.
	if _, _, err := h.eng.Enqueue(h.ctx, jobs.Request{Kind: jobspec.BackupRun, Principal: authz.Service(), PolicyID: "bk",
		EnvironmentID: "env-1", Targets: []domain.JobTarget{{Type: domain.TargetRepository, ID: "repo-1"}},
		Input: map[string]any{"policy": "bk", "part": 0, "changed": "before the crash"}, IdempotencyKey: run.IdempotencyKey + "#0"}); err != nil {
		t.Fatal(err)
	}
	h.clk.Set(at("2026-03-01T02:01:00Z"))
	h.start()
	h.tick()
	js := h.allJobs()
	if len(js) != 2 || js[0].IdempotencyKey != run.IdempotencyKey+"#0" || js[1].IdempotencyKey != run.IdempotencyKey+"#1" {
		t.Fatalf("jobs %+v", js)
	}
	runs := h.history(KindBackup, "bk")
	if len(runs) != 1 || runs[0].Outcome != domain.RunEnqueued || len(runs[0].Jobs) != 2 || runs[0].Jobs[0].JobID != js[0].ID {
		t.Fatalf("history %+v", runs)
	}
	// The overlap check did not mistake the run's own job for an active
	// previous run, and the already started run was not revalidated.
	if v := h.sources[KindBackup].validates; len(v) != 0 {
		t.Fatalf("revalidated %v", v)
	}
	h.start()
	h.tick()
	if n := len(h.allJobs()); n != 2 {
		t.Fatalf("%d jobs", n)
	}
}

// An offline agent: the scheduled job waits blocked (agent_offline) and
// fails after the kind's offline deadline; the schedule history shows both.
func TestOfflineAgentVisibleInHistory(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-01T03:00:00Z")
	h.dispatch()
	runs := h.history(KindPrune, "pr")
	if len(runs) != 1 || runs[0].Jobs[0].State != domain.JobBlocked || runs[0].Jobs[0].BlockedReason != domain.BlockedAgentOffline ||
		runs[0].Result() != "active" {
		t.Fatalf("while offline %+v", runs)
	}
	spec, _ := jobspec.Lookup(jobspec.PruneRun)
	h.clk.Set(at("2026-03-01T03:00:00Z").Add(spec.OfflineDeadline))
	h.dispatch()
	runs = h.history(KindPrune, "pr")
	if j := runs[0].Jobs[0]; j.State != domain.JobFailed || j.ErrorClass != domain.ErrorAgentOffline || j.FinishedAt == nil || runs[0].Result() != "failed" {
		t.Fatalf("after the deadline %+v", runs[0])
	}
	// The terminal state was recorded on the run itself (it survives job
	// retention).
	var state, class string
	if err := h.db.NewRaw(`SELECT state, error_class FROM schedule_run_jobs`).Scan(h.ctx, &state, &class); err != nil ||
		state != string(domain.JobFailed) || class != domain.ErrorAgentOffline {
		t.Fatalf("recorded %s %s %v", state, class, err)
	}
	// The next day's run is not blocked by the failed one.
	h.tickAt("2026-03-02T03:00:00Z")
	if runs := h.history(KindPrune, "pr"); len(runs) != 2 || runs[1].Outcome != domain.RunEnqueued {
		t.Fatalf("next day %+v", runs)
	}
}

// Overlap prevention: while a policy's job is not terminal, a due run is
// skipped and recorded; afterwards runs resume. Manual runs of the policy
// count too.
func TestOverlapIsSkipped(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "*/10 * * * *", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-01T00:10:00Z")
	first := h.allJobs()[0]
	h.tickAt("2026-03-01T00:20:00Z")
	runs := h.history(KindPrune, "pr")
	mustEqual(t, "history", instants(runs), []string{"2026-03-01T00:10:00Z enqueued", "2026-03-01T00:20:00Z skipped"})
	if !strings.Contains(runs[1].Reason, first.ID) || runs[1].ErrorClass != "previous_run_active" {
		t.Fatalf("skipped row %+v", runs[1])
	}
	h.cancelAll()
	h.tickAt("2026-03-01T00:30:00Z")
	if n := len(h.allJobs()); n != 2 {
		t.Fatalf("%d jobs after the previous run ended", n)
	}
	h.cancelAll()
	// A manual run of the policy (same policy ID) blocks the schedule too.
	if _, _, err := h.eng.Enqueue(h.ctx, jobs.Request{Kind: jobspec.PruneRun, Principal: authz.Service(), PolicyID: "pr",
		EnvironmentID: "env-1", Input: map[string]any{"manual": true}}); err != nil {
		t.Fatal(err)
	}
	h.tickAt("2026-03-01T00:40:00Z")
	if runs := h.history(KindPrune, "pr"); runs[len(runs)-1].Outcome != domain.RunSkipped {
		t.Fatalf("history %v", instants(runs))
	}
	if acts := h.auditActions(); !slices.Contains(acts, "schedule.run_skipped") {
		t.Fatalf("audit %v", acts)
	}
}

// Disable, edit and preview never perform a run; re-enabling does not
// catch up the disabled period.
func TestDisableEditPreviewWithoutRunning(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	src := h.sources[KindPrune]
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
	h.tick()

	// Preview: nothing is stored or run.
	p, err := h.s.Preview(h.ctx, PreviewRequest{Cron: "*/5 * * * *", TimeZone: "Europe/Berlin", Count: 3})
	if err != nil || len(p.Runs) != 3 {
		t.Fatalf("preview %+v %v", p, err)
	}

	// Disable before the due time: no run, no next run.
	src.edit("pr", func(p *testPolicy) { p.Enabled = false })
	h.tickAt("2026-03-01T01:00:00Z")
	if sc := h.schedule(KindPrune, "pr"); sc.Enabled || sc.NextRunAt != nil {
		t.Fatalf("disabled %+v", sc)
	}
	h.stepTo("2026-03-01T04:00:00Z", 30*time.Minute)

	// Edit while disabled, then re-enable after two instants passed: no
	// catch-up, the next run follows the new expression.
	src.edit("pr", func(p *testPolicy) { p.Cron = "30 6 * * *"; p.TimeZone = "Europe/Berlin" })
	h.tickAt("2026-03-02T09:00:00Z")
	src.edit("pr", func(p *testPolicy) { p.Enabled = true })
	h.tickAt("2026-03-03T09:00:00Z")
	sc := h.schedule(KindPrune, "pr")
	if !sc.Enabled || sc.Cron != "30 6 * * *" || !sc.NextRunAt.Equal(at("2026-03-04T05:30:00Z")) {
		t.Fatalf("re-enabled %+v", sc)
	}
	// Editing an enabled schedule re-evaluates from the edit.
	src.edit("pr", func(p *testPolicy) { p.Cron = "0 12 * * *" })
	h.tickAt("2026-03-03T10:00:00Z")
	if sc := h.schedule(KindPrune, "pr"); !sc.NextRunAt.Equal(at("2026-03-03T11:00:00Z")) || !sc.Cursor.Equal(at("2026-03-03T10:00:00Z")) {
		t.Fatalf("edited %+v", sc)
	}
	if n := len(h.allJobs()); n != 0 {
		t.Fatalf("%d jobs", n)
	}
	if runs := h.history(KindPrune, "pr"); len(runs) != 0 {
		t.Fatalf("history %v", instants(runs))
	}
	// Renaming does not reset the cursor.
	src.edit("pr", func(p *testPolicy) { p.Name = "Weekly cleanup" })
	h.tickAt("2026-03-03T10:30:00Z")
	if sc := h.schedule(KindPrune, "pr"); sc.Name != "Weekly cleanup" || !sc.Cursor.Equal(at("2026-03-03T10:00:00Z")) {
		t.Fatalf("renamed %+v", sc)
	}
	h.tickAt("2026-03-03T11:00:00Z")
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs at the edited time", n)
	}
}

// A policy that became invalid for its run is rejected when due, and a
// queued scheduled job whose policy was disabled meanwhile fails at
// dispatch with policy_rejected.
func TestRevalidationAtDueAndDispatch(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	src := h.sources[KindPrune]
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 * * * *", Enabled: true}})
	h.tick()
	src.edit("pr", func(p *testPolicy) { p.TargetGone = true })
	h.tickAt("2026-03-01T01:00:00Z")
	runs := h.history(KindPrune, "pr")
	if len(runs) != 1 || runs[0].Outcome != domain.RunRejected || runs[0].ErrorClass != RejectTargetNotFound ||
		!strings.Contains(runs[0].Reason, "no longer exists") || len(h.allJobs()) != 0 {
		t.Fatalf("rejected at due time %+v", runs)
	}
	src.edit("pr", func(p *testPolicy) { p.TargetGone = false })
	h.tickAt("2026-03-01T02:00:00Z")
	j := h.allJobs()[0]
	// Disabled after the job was queued, before it was dispatched: the
	// engine asks the scheduler, which asks the policy source.
	src.edit("pr", func(p *testPolicy) { p.Enabled = false })
	h.disp.Connect("env-1")
	h.dispatch()
	got, _ := h.eng.Get(h.ctx, j.ID)
	if got.State != domain.JobFailed || got.ErrorClass != domain.ErrorPolicyRejected || !strings.Contains(got.ErrorMessage, "disabled") {
		t.Fatalf("job %+v", got)
	}
	if h.disp.Pending("env-1") != 0 {
		t.Fatal("a command was sent")
	}
	if runs := h.history(KindPrune, "pr"); runs[1].Result() != "failed" || runs[1].Jobs[0].ErrorClass != domain.ErrorPolicyRejected {
		t.Fatalf("history %+v", runs[1])
	}
}

// A deleted policy's schedule and history are removed; an invalid saved
// expression never runs and says why.
func TestDeletedAndInvalidPolicies(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	src := h.sources[KindPrune]
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "gone", Cron: "0 * * * *", Enabled: true}})
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bad", Cron: "0 0 30 2 *", TimeZone: "Mars/Olympus", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-01T01:00:00Z")
	bad := h.schedule(KindPrune, "bad")
	if bad.NextRunAt != nil || !strings.Contains(bad.InvalidReason, "never matches") || !strings.Contains(bad.InvalidReason, "Mars/Olympus") {
		t.Fatalf("invalid %+v", bad)
	}
	src.remove("gone")
	h.tickAt("2026-03-01T02:00:00Z")
	if _, _, ok, _ := h.s.Status(h.ctx, KindPrune, "gone", 1); ok {
		t.Fatal("deleted policy still scheduled")
	}
	var n int
	if err := h.db.NewRaw(`SELECT count(*) FROM schedule_runs`).Scan(h.ctx, &n); err != nil || n != 0 {
		t.Fatalf("%d runs left (%v)", n, err)
	}
	if js := h.allJobs(); len(js) != 1 {
		t.Fatalf("jobs %v", js) // the 01:00 run of "gone"
	}
	// A source outage keeps existing schedules (never deletes them).
	src.FailSchedules = true
	if err := h.s.Tick(h.ctx); err == nil {
		t.Fatal("tick hid the source outage")
	}
	if _, _, ok, _ := h.s.Status(h.ctx, KindPrune, "bad", 1); !ok {
		t.Fatal("an outage removed a schedule")
	}
}

// DST at the scheduler level: one job per day across spring-forward and
// fall-back in America/New_York and Europe/Berlin, at the documented
// instants, while the runner is evaluated every minute.
func TestDSTTransitionsEnqueueOnce(t *testing.T) {
	for _, tc := range []struct {
		name, zone, cron, from, to string
		want                       []string
	}{
		{"new york spring forward", "America/New_York", "30 2 * * *", "2026-03-07T00:00:00Z", "2026-03-09T12:00:00Z",
			[]string{"2026-03-07T07:30:00Z", "2026-03-08T07:00:00Z", "2026-03-09T06:30:00Z"}},
		{"new york fall back", "America/New_York", "30 1 * * *", "2026-10-31T00:00:00Z", "2026-11-02T12:00:00Z",
			[]string{"2026-10-31T05:30:00Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"}},
		{"berlin spring forward", "Europe/Berlin", "30 2 * * *", "2026-03-28T00:00:00Z", "2026-03-30T12:00:00Z",
			[]string{"2026-03-28T01:30:00Z", "2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z"}},
		{"berlin fall back", "Europe/Berlin", "30 2 * * *", "2026-10-24T00:00:00Z", "2026-10-26T12:00:00Z",
			[]string{"2026-10-24T00:30:00Z", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, at(tc.from))
			h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: tc.cron, TimeZone: tc.zone, Enabled: true}})
			h.tick()
			// Evaluate every 5 minutes; jobs are cancelled after each run
			// so overlap prevention does not interfere.
			for h.clk.Now().Before(at(tc.to)) {
				h.clk.Advance(5 * time.Minute)
				h.tick()
				h.cancelAll()
			}
			var got []string
			for _, r := range h.history(KindPrune, "pr") {
				if r.Outcome != domain.RunEnqueued {
					t.Fatalf("run %+v", r)
				}
				got = append(got, r.ScheduledFor.Format(time.RFC3339))
			}
			mustEqual(t, "runs", got, tc.want)
			if n := len(h.allJobs()); n != len(tc.want) {
				t.Fatalf("%d jobs", n)
			}
		})
	}
}

// The runner loop sleeps exactly until the next run (bounded by MaxSleep)
// and wakes on Notify.
func TestRunnerWakesAtNextRun(t *testing.T) {
	h := newHarness(t, at("2026-03-01T02:30:00Z"))
	h.opts.MaxSleep = 2 * time.Hour
	h.start()
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * *", Enabled: true}})
	select { // Register's Notify: the first pass covers it
	case <-h.s.wake:
	default:
	}
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() { done <- h.s.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if err := h.clk.BlockUntilWaiters(h.ctx, 1); err != nil {
		t.Fatal(err)
	}
	// One second before the run nothing happens; the runner's single timer
	// is still armed for 03:00.
	h.clk.Advance(30*time.Minute - time.Second)
	if h.clk.Waiters() != 1 || len(h.allJobs()) != 0 {
		t.Fatalf("early: waiters %d jobs %d", h.clk.Waiters(), len(h.allJobs()))
	}
	h.clk.Advance(time.Second)
	if err := h.clk.BlockUntilWaiters(h.ctx, 1); err != nil { // the pass finished and re-armed
		t.Fatal(err)
	}
	js := h.allJobs()
	if len(js) != 1 || !js[0].CreatedAt.Equal(at("2026-03-01T03:00:00Z")) {
		t.Fatalf("jobs %v", jobTimes(js))
	}
	// Notify wakes the runner at once (a new policy due now is picked up
	// without waiting for the timer).
	h.policy(KindBackup, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bk", Cron: "*/1 * * * *", Enabled: true}})
	h.s.Notify()
	for {
		if err := h.clk.BlockUntilWaiters(h.ctx, 1); err != nil {
			t.Fatal(err)
		}
		if _, _, ok, _ := h.s.Status(h.ctx, KindBackup, "bk", 1); ok {
			break
		}
		h.s.Notify()
	}
	sc := h.schedule(KindBackup, "bk")
	if !sc.NextRunAt.Equal(at("2026-03-01T03:01:00Z")) {
		t.Fatalf("new schedule %+v", sc)
	}
}

// Clock jumps: a forward jump behaves like downtime (missed/catch-up), a
// backward jump never repeats a processed instant.
func TestClockJumps(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	h.policy(KindBackup, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "bk", Cron: "0 * * * *", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-01T01:00:00Z")
	h.cancelAll()
	// Forward by 5 hours: one catch-up run for 06:00 is on time (grace),
	// 02:00-05:00 are missed.
	h.tickAt("2026-03-01T06:00:30Z")
	mustEqual(t, "history", instants(h.history(KindBackup, "bk")),
		[]string{"2026-03-01T01:00:00Z enqueued", "2026-03-01T05:00:00Z missed", "2026-03-01T06:00:00Z enqueued"})
	h.cancelAll()
	// Backward: a manager whose clock is 3 hours behind (restart with the
	// wrong time) sees a cursor in the future.
	back := newClockAt(h, at("2026-03-01T03:30:00Z"))
	back.tick()
	sc := back.schedule(KindBackup, "bk")
	if !sc.NextRunAt.Equal(at("2026-03-01T04:00:00Z")) {
		t.Fatalf("after the backward jump %+v", sc)
	}
	for _, ts := range []string{"2026-03-01T04:00:00Z", "2026-03-01T05:00:00Z", "2026-03-01T06:00:00Z"} {
		back.tickAt(ts)
		back.cancelAll()
	}
	// 04:00 is new (it was never processed); 05:00 and 06:00 were already
	// recorded and are not enqueued again.
	var keys []string
	for _, j := range back.allJobs() {
		keys = append(keys, j.IdempotencyKey)
	}
	mustEqual(t, "jobs", keys, []string{"backup:bk:2026-03-01T01:00:00Z#0", "backup:bk:2026-03-01T06:00:00Z#0", "backup:bk:2026-03-01T04:00:00Z#0"})
	back.tickAt("2026-03-01T07:00:00Z")
	if n := len(back.allJobs()); n != 4 {
		t.Fatalf("%d jobs at 07:00", n)
	}
}

// newClockAt restarts h's manager on a new fake clock at start (fake
// clocks cannot move backwards).
func newClockAt(h *harness, start time.Time) *harness {
	h2 := *h
	h2.clk = newFakeClock(start)
	h2.start()
	return &h2
}

func TestValidateSpecAndKinds(t *testing.T) {
	if err := ValidateSpec("0 2 * * *", "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	err := ValidateSpec("61 2 * * *", "Nowhere/City")
	var ie *InvalidError
	if !errors.As(err, &ie) || len(ie.Problems) != 2 || !errors.Is(err, domain.ErrScheduleInvalid) {
		t.Fatalf("%v", err)
	}
	if ie.Problems[0].Field != cron.FieldMinute || ie.Problems[1].Field != cron.FieldTimeZone {
		t.Fatalf("%+v", ie.Problems)
	}
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	if err := h.s.Register("nope", newTestSource(jobspec.PruneRun)); !errors.Is(err, domain.ErrScheduleKindUnknown) {
		t.Fatal(err)
	}
	if err := h.s.Register(KindPrune, newTestSource(jobspec.PruneRun)); err == nil {
		t.Fatal("second source for a kind")
	}
	for _, bad := range []Kind{
		{Key: "Bad Key", Label: "x", Suggested: "0 0 * * *", CatchUp: domain.CatchUpOnce, PolicyType: "p", ReadCapability: "p.read", JobKinds: []domain.JobKind{jobspec.PruneRun}},
		{Key: "x", Label: "x", Suggested: "0 0 30 2 *", CatchUp: domain.CatchUpOnce, PolicyType: "p", ReadCapability: "p.read", JobKinds: []domain.JobKind{jobspec.PruneRun}},
		{Key: "x", Label: "x", Suggested: "0 0 * * *", CatchUp: "sometimes", PolicyType: "p", ReadCapability: "p.read", JobKinds: []domain.JobKind{jobspec.PruneRun}},
		{Key: "x", Label: "x", Suggested: "0 0 * * *", CatchUp: domain.CatchUpOnce, PolicyType: "p", ReadCapability: "p.read", JobKinds: []domain.JobKind{"nope.run"}},
		{Key: KindPrune, Label: "x", Suggested: "0 0 * * *", CatchUp: domain.CatchUpOnce, PolicyType: "p", ReadCapability: "p.read", JobKinds: []domain.JobKind{jobspec.PruneRun}},
	} {
		if err := h.s.RegisterKind(bad); err == nil {
			t.Errorf("RegisterKind(%+v) accepted", bad)
		}
	}
	var keys []string
	for _, k := range h.s.Kinds() {
		keys = append(keys, k.Key+"="+k.Suggested+"/"+string(k.CatchUp))
	}
	mustEqual(t, "kinds", keys, []string{"backup=0 2 * * */once", "update_check=0 3 * * */once", "update_run=0 4 * * */skip",
		"prune=0 3 * * 0/skip", "backup_verification=0 5 * * 0/once"})
}

// Editable defaults: suggestions until edited, validated, revisioned; they
// only prefill new policies (existing schedules keep their expression).
func TestScheduleDefaults(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	d, err := h.s.Defaults(h.ctx)
	if err != nil || d.TimeZone != "UTC" || d.Revision != 1 || len(d.Kinds) != 5 {
		t.Fatalf("%+v %v", d, err)
	}
	if b, _ := d.Default(KindBackup); b.Cron != "0 2 * * *" || b.Suggested != "0 2 * * *" {
		t.Fatalf("%+v", b)
	}
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "0 3 * * 0", Enabled: true}})
	h.tick()

	tz := "Europe/Berlin"
	_, _, err = h.s.UpdateDefaults(h.ctx, 1, domain.ScheduleDefaultsPatch{TimeZone: new("Nowhere/X"), Crons: map[string]string{"prune": "99 * * * *", "zzz": "0 0 * * *"}})
	var de *DefaultsError
	if !errors.As(err, &de) || len(de.Problems) != 2 || !slices.Equal(de.UnknownKinds, []string{"zzz"}) || !errors.Is(err, domain.ErrScheduleInvalid) {
		t.Fatalf("%v", err)
	}
	before, after, err := h.s.UpdateDefaults(h.ctx, 1, domain.ScheduleDefaultsPatch{TimeZone: &tz, Crons: map[string]string{"prune": " 0   1 * * 6 "}})
	if err != nil || before.Revision != 1 || after.Revision != 2 || after.TimeZone != tz {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	if p, _ := after.Default(KindPrune); p.Cron != "0 1 * * 6" || p.Suggested != "0 3 * * 0" {
		t.Fatalf("%+v", p)
	}
	if _, _, err := h.s.UpdateDefaults(h.ctx, 1, domain.ScheduleDefaultsPatch{TimeZone: &tz}); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	c, z, err := h.s.Default(h.ctx, KindPrune)
	if err != nil || c != "0 1 * * 6" || z != tz {
		t.Fatalf("%s %s %v", c, z, err)
	}
	if _, _, err := h.s.Default(h.ctx, "nope"); !errors.Is(err, domain.ErrScheduleKindUnknown) {
		t.Fatal(err)
	}
	h.tick()
	if sc := h.schedule(KindPrune, "pr"); sc.Cron != "0 3 * * 0" || sc.TimeZone != "UTC" {
		t.Fatalf("an existing policy changed: %+v", sc)
	}
	// A preview without a zone uses the instance default.
	p, err := h.s.Preview(h.ctx, PreviewRequest{Cron: "0 12 * * *", Count: 1, From: at("2026-03-01T00:00:00Z")})
	if err != nil || p.TimeZone != tz || !p.Runs[0].At.Equal(at("2026-03-01T11:00:00Z")) {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestPreview(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	p, err := h.s.Preview(h.ctx, PreviewRequest{Cron: "30 2 * * *", TimeZone: "America/New_York", Kind: KindPrune,
		From: at("2026-03-07T00:00:00Z"), Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Runs) != 3 || p.Runs[1].DST != cron.DSTGap || !p.Runs[1].At.Equal(at("2026-03-08T07:00:00Z")) ||
		p.Runs[1].Nominal != "2026-03-08T02:30" || !strings.Contains(p.Runs[1].DSTNotes, "does not exist") {
		t.Fatalf("%+v", p.Runs)
	}
	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"daylight-saving gap", "skipped and recorded", "never run a scheduled time twice", "agent_offline", "service identity"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q: %s", want, joined)
		}
	}
	p, err = h.s.Preview(h.ctx, PreviewRequest{Cron: "30 1 * * *", TimeZone: "Europe/Berlin", From: at("2026-10-24T12:00:00Z"), Count: 100})
	if err != nil || len(p.Runs) != MaxPreviewCount {
		t.Fatalf("%d %v", len(p.Runs), err)
	}
	p, err = h.s.Preview(h.ctx, PreviewRequest{Cron: "30 2 * * *", TimeZone: "Europe/Berlin", From: at("2026-10-24T12:00:00Z"), Count: 1})
	if err != nil || p.Runs[0].DST != cron.DSTRepeated || !strings.Contains(strings.Join(p.Notes, " "), "repeated") {
		t.Fatalf("%+v %v", p, err)
	}
	var ie *InvalidError
	if _, err := h.s.Preview(h.ctx, PreviewRequest{Cron: "* * *", TimeZone: "UTC"}); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	if _, err := h.s.Preview(h.ctx, PreviewRequest{Cron: "* * * * *", Kind: "nope"}); !errors.Is(err, domain.ErrScheduleKindUnknown) {
		t.Fatal(err)
	}
	var n int
	if err := h.db.NewRaw(`SELECT count(*) FROM schedules`).Scan(h.ctx, &n); err != nil || n != 0 {
		t.Fatalf("preview stored %d schedules", n)
	}
}

func TestNextRunAnnotation(t *testing.T) {
	h := newHarness(t, at("2026-03-07T12:00:00Z"))
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pr", Cron: "30 2 * * *", TimeZone: "America/New_York", Enabled: true}})
	h.tick()
	h.tickAt("2026-03-08T00:00:00Z")
	r, ok := h.s.NextRun(h.schedule(KindPrune, "pr"))
	if !ok || r.DST != cron.DSTGap || r.Nominal != "2026-03-08T02:30" || !r.At.Equal(at("2026-03-08T07:00:00Z")) {
		t.Fatalf("%+v", r)
	}
}
