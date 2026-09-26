package scheduler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// testPolicy is a policy of the test source. CreatedBy is the account that
// created it: the scheduler never uses it.
type testPolicy struct {
	PolicySchedule
	CreatedBy  string
	TargetGone bool
}

// testSource is the PolicySource used by the tests: an in-memory policy
// table whose runs enqueue one job per run (JobsPerRun more when set).
type testSource struct {
	mu         sync.Mutex
	jobKind    domain.JobKind
	policies   map[string]*testPolicy
	order      []string
	JobsPerRun int
	// FailSchedules makes Schedules fail (source outage).
	FailSchedules bool
	validates     []string
	jobsCalls     []Due
}

func newTestSource(jobKind domain.JobKind) *testSource {
	return &testSource{jobKind: jobKind, policies: map[string]*testPolicy{}}
}

func (s *testSource) put(p testPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.policies[p.PolicyID]; !ok {
		s.order = append(s.order, p.PolicyID)
	}
	cp := p
	s.policies[p.PolicyID] = &cp
}

func (s *testSource) edit(id string, fn func(p *testPolicy)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.policies[id])
}

func (s *testSource) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.policies, id)
	s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
}

func (s *testSource) Schedules(context.Context) ([]PolicySchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailSchedules {
		return nil, context.DeadlineExceeded
	}
	out := make([]PolicySchedule, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.policies[id].PolicySchedule)
	}
	return out, nil
}

func (s *testSource) Validate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validates = append(s.validates, id)
	p, ok := s.policies[id]
	switch {
	case !ok:
		return Reject(RejectPolicyNotFound, "the policy was deleted")
	case !p.Enabled:
		return Reject(RejectPolicyDisabled, "the policy is disabled")
	case p.TargetGone:
		return Reject(RejectTargetNotFound, "the policy's environment no longer exists")
	}
	return nil
}

func (s *testSource) Jobs(_ context.Context, due Due) ([]jobs.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobsCalls = append(s.jobsCalls, due)
	p, ok := s.policies[due.PolicyID]
	if !ok {
		return nil, Reject(RejectPolicyNotFound, "the policy was deleted")
	}
	n := max(s.JobsPerRun, 1)
	out := make([]jobs.Request, 0, n)
	for i := range n {
		req := jobs.Request{Kind: s.jobKind, EnvironmentID: p.EnvironmentID,
			Input: map[string]any{"policy": p.PolicyID, "part": i}}
		if s.jobKind == jobspec.BackupRun {
			req.Targets = []domain.JobTarget{{Type: domain.TargetRepository, ID: "repo-1"}}
		}
		out = append(out, req)
	}
	return out, nil
}

type harness struct {
	t       *testing.T
	ctx     context.Context
	db      *bun.DB
	clk     *clock.Fake
	disp    *jobstest.Dispatcher
	audit   *audit.Log
	eng     *jobs.Engine
	s       *Service
	sources map[string]*testSource
	opts    Options
}

func openDB(t *testing.T) *bun.DB {
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
	return db
}

// newHarness starts a manager's scheduler at start. The job engine has no
// Authorizer (DenyAll): no user holds any grant, so only the service
// identity can run anything.
func newHarness(t *testing.T, start time.Time) *harness {
	t.Helper()
	h := &harness{t: t, ctx: testutil.Context(t), db: openDB(t), clk: clock.NewFake(start), disp: jobstest.New(),
		sources: map[string]*testSource{
			KindPrune:  newTestSource(jobspec.PruneRun),
			KindBackup: newTestSource(jobspec.BackupRun),
		}}
	h.start()
	return h
}

// start creates the engine and the scheduler (again, for restarts) over
// the same database and registers the sources.
func (h *harness) start() {
	h.t.Helper()
	var err error
	if h.audit, err = audit.New(audit.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(h.t)}); err != nil {
		h.t.Fatal(err)
	}
	if h.eng, err = jobs.New(jobs.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(h.t), Dispatcher: h.disp, Audit: h.audit}); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(h.eng.Close)
	o := h.opts
	o.DB, o.Clock, o.Logger, o.Jobs, o.Audit = h.db, h.clk, testutil.Logger(h.t), h.eng, h.audit
	if h.s, err = New(o); err != nil {
		h.t.Fatal(err)
	}
	for kind, src := range h.sources {
		if err := h.s.Register(kind, src); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := h.eng.Recover(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) policy(kind string, p testPolicy) {
	h.t.Helper()
	if p.TimeZone == "" {
		p.TimeZone = "UTC"
	}
	if p.EnvironmentID == "" {
		p.EnvironmentID = "env-1"
	}
	if p.Name == "" {
		p.Name = p.PolicyID
	}
	h.sources[kind].put(p)
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.s.Tick(h.ctx); err != nil {
		h.t.Fatalf("tick at %s: %v", h.clk.Now().UTC().Format(time.RFC3339), err)
	}
}

func (h *harness) tickAt(ts string) {
	h.t.Helper()
	h.clk.Set(at(ts))
	h.tick()
}

// stepTo ticks every step up to and including end.
func (h *harness) stepTo(end string, step time.Duration) {
	h.t.Helper()
	e := at(end)
	for h.clk.Now().Before(e) {
		next := h.clk.Now().Add(step)
		if next.After(e) {
			next = e
		}
		h.clk.Set(next)
		h.tick()
	}
}

func (h *harness) dispatch() {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatalf("dispatch: %v", err)
	}
}

func (h *harness) allJobs() []domain.Job {
	h.t.Helper()
	js, err := h.eng.List(h.ctx, domain.JobFilter{Limit: 1000})
	if err != nil {
		h.t.Fatal(err)
	}
	slices.Reverse(js) // oldest first
	return js
}

func (h *harness) cancelAll() {
	h.t.Helper()
	for _, j := range h.allJobs() {
		if !j.State.Terminal() {
			if _, err := h.eng.Cancel(h.ctx, j.ID); err != nil {
				h.t.Fatal(err)
			}
		}
	}
}

func (h *harness) schedule(kind, policy string) domain.Schedule {
	h.t.Helper()
	sc, _, ok, err := h.s.Status(h.ctx, kind, policy, 1)
	if err != nil || !ok {
		h.t.Fatalf("schedule %s/%s: ok=%v err=%v", kind, policy, ok, err)
	}
	return sc
}

// history returns a policy's runs oldest first.
func (h *harness) history(kind, policy string) []domain.ScheduleRun {
	h.t.Helper()
	_, runs, ok, err := h.s.Status(h.ctx, kind, policy, 100)
	if err != nil || !ok {
		h.t.Fatalf("history %s/%s: ok=%v err=%v", kind, policy, ok, err)
	}
	slices.Reverse(runs)
	return runs
}

func instants(runs []domain.ScheduleRun) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ScheduledFor.Format(time.RFC3339)+" "+string(r.Outcome))
	}
	return out
}

func jobTimes(js []domain.Job) []string {
	out := make([]string, 0, len(js))
	for _, j := range js {
		out = append(out, j.CreatedAt.UTC().Format(time.RFC3339))
	}
	return out
}

func mustEqual[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		b1, _ := json.MarshalIndent(got, "", " ")
		b2, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("%s:\n got  %s\n want %s", what, b1, b2)
	}
}

func (h *harness) auditActions() []string {
	h.t.Helper()
	recs, err := h.audit.Records(h.ctx, domain.AuditFilter{Ascending: true, Limit: 1000})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Action)
	}
	return out
}

func newFakeClock(start time.Time) *clock.Fake { return clock.NewFake(start) }

// completeFrame plays the agent for a command frame: acknowledge it and
// report the outcome.
func completeFrame(h *harness, cmd *protocol.Frame, state domain.JobState) {
	h.t.Helper()
	if cmd.Type != protocol.TypeCommand {
		return
	}
	ack, err := protocol.NewFrame(protocol.TypeAck, "ack-"+cmd.ID, cmd.ID, cmd.Ref(), protocol.AckPayload{Accepted: true})
	if err != nil {
		h.t.Fatal(err)
	}
	res, err := protocol.NewFrame(protocol.TypeResult, "res-"+cmd.ID, cmd.ID, cmd.Ref(), protocol.ResultPayload{Outcome: string(state)})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, f := range []*protocol.Frame{ack, res} {
		if _, err := h.eng.HandleAgentFrame(h.ctx, "env-1", f); err != nil {
			h.t.Fatal(err)
		}
	}
}
