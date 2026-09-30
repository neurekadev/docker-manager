package managermove

import (
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	envmigrations "github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// readyToRun creates a move whose new server enrolled and checked in.
func readyToRun(t *testing.T, f *fixture) (domain.ManagerMove, string) {
	t.Helper()
	m, code := f.create()
	f.enrollNewServer(m)
	var nr *domain.MoveNotReadyError
	if _, err := f.handoff(code); !errors.As(err, &nr) {
		t.Fatalf("check-in: %v", err)
	}
	return m, code
}

// run starts Move everything and dispatches it.
func run(t *testing.T, f *fixture) domain.Job {
	t.Helper()
	j, err := f.svc.StartRun(f.owner())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	return j
}

func (m *fakeMigrations) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.started)
}

func (m *fakeMigrations) last() startedMigration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started[len(m.started)-1]
}

// TestMoveEverything: manager.move runs the environment migration from
// the environment next to the manager to the new server's with the
// owner's principal (once per job), the move is moving meanwhile (one run
// at a time; the handoff answers not_ready with the progress) and ready
// when the migration completed.
func TestMoveEverything(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, code := readyToRun(t, f)
	j := run(t, f)
	if j.Kind != moveKind || f.move(m.ID).State != domain.MoveMoving || f.move(m.ID).MoveJobID != j.ID {
		t.Fatalf("started %+v, move %+v", j, f.move(m.ID))
	}
	if _, err := f.svc.StartRun(f.owner()); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("second run: %v", err)
	}
	waitUntil(t, f, false, func() bool { return f.migr.count() == 1 })
	started := f.migr.last()
	if started.source != "env-old" || started.target != "env-new" || started.key != "manager-move-"+j.ID {
		t.Fatalf("migration %+v", started)
	}
	if f.migr.principal != (authz.Principal{Kind: authz.KindUser, UserID: ownerID}) {
		t.Fatalf("principal %+v", f.migr.principal)
	}
	waitUntil(t, f, false, func() bool { return f.move(m.ID).MigrationID == started.id })
	var nr *domain.MoveNotReadyError
	if _, err := f.handoff(code); !errors.As(err, &nr) || nr.State != domain.MoveMoving || nr.StacksMoved != 1 || nr.StacksTotal != 2 ||
		nr.CurrentStack != "web" {
		t.Fatalf("handoff while the apps move: %v", err)
	}
	if v, err := f.svc.Current(f.ctx); err != nil || v.Progress == nil || v.Progress.JobID != j.ID || v.Progress.MigrationID != started.id ||
		v.Progress.StacksMoved != 1 || v.Progress.CurrentStack != "web" {
		t.Fatalf("progress %+v %v", v.Progress, err)
	}
	f.finishJob(started.id, domain.JobSucceeded)
	if done := waitJob(t, f, j.ID); done.State != domain.JobSucceeded {
		t.Fatalf("manager.move %s %s: %s", done.State, done.ErrorClass, done.Recovery)
	}
	if got := f.move(m.ID); got.State != domain.MoveReady || got.ReadyAt == nil {
		t.Fatalf("after the migration %+v", got)
	}
	if f.migr.count() != 1 {
		t.Fatal("the migration started twice")
	}
}

// TestMoveEverythingFails: a migration that does not complete fails
// manager.move with its guidance and puts the move back to open; Move
// everything again starts a new migration (what is left).
func TestMoveEverythingFails(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, _ := readyToRun(t, f)
	j := run(t, f)
	waitUntil(t, f, false, func() bool { return f.migr.count() == 1 })
	first := f.migr.last()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE jobs SET state = 'failed', error_class = 'stack_not_moved', recovery = 'Fix the web stack.' WHERE id = ?",
		first.id); err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, f, j.ID)
	if done.State != domain.JobFailed || done.ErrorClass != ClassAppsNotMoved || done.Recovery == "" {
		t.Fatalf("manager.move %s %s: %s", done.State, done.ErrorClass, done.Recovery)
	}
	if got := f.move(m.ID); got.State != domain.MoveOpen || got.MoveJobID != j.ID {
		t.Fatalf("after the failure %+v", got)
	}
	if v, _ := f.svc.Current(f.ctx); v.Progress == nil || v.Progress.ErrorClass != ClassAppsNotMoved {
		t.Fatalf("progress after the failure %+v", v.Progress)
	}
	// The waiting manager checks in every 10 s; its last check-in is fresh.
	j2 := run(t, f)
	waitUntil(t, f, false, func() bool { return f.migr.count() == 2 })
	if second := f.migr.last(); second.key != "manager-move-"+j2.ID || second.id == first.id {
		t.Fatalf("second migration %+v", second)
	}
}

// TestMoveEverythingWithoutApps: nothing to move (no movable stack, or no
// environment next to the manager) makes the move ready at once; other
// blockers fail with the plan's messages.
func TestMoveEverythingWithoutApps(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, _ := readyToRun(t, f)
	f.migr.blocked = &envmigrations.EnvironmentBlockedError{Plan: envmigrations.EnvironmentPlan{
		Blockers: []envmigrations.Finding{{Code: envmigrations.FindingNoStacks, Message: "the environment has no stack to migrate"}}}}
	if done := waitJob(t, f, run(t, f).ID); done.State != domain.JobSucceeded || f.move(m.ID).State != domain.MoveReady {
		t.Fatalf("no stacks: %s %s, move %s", done.State, done.ErrorClass, f.move(m.ID).State)
	}

	g := newFixture(t, testutil.FakeClock(), nil)
	g.colocate()
	m, _ = readyToRun(t, g)
	g.migr.blocked = &envmigrations.EnvironmentBlockedError{Plan: envmigrations.EnvironmentPlan{
		Blockers: []envmigrations.Finding{{Code: "insufficient_space", Message: "the destination is full"}}}}
	done := waitJob(t, g, run(t, g).ID)
	if done.State != domain.JobFailed || done.ErrorClass != ClassAppsBlocked || g.move(m.ID).State != domain.MoveOpen {
		t.Fatalf("blocked: %s %s, move %s", done.State, done.ErrorClass, g.move(m.ID).State)
	}

	h := newFixture(t, testutil.FakeClock(), nil)
	m, _ = readyToRun(t, h)
	if done := waitJob(t, h, run(t, h).ID); done.State != domain.JobSucceeded || h.move(m.ID).State != domain.MoveReady || h.migr.count() != 0 {
		t.Fatalf("no environment next to the manager: %s, move %s", done.State, h.move(m.ID).State)
	}
}

// TestCancelWhileMoving: cancelling a moving move cancels manager.move
// (which cancels the migration); nothing becomes ready.
func TestCancelWhileMoving(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, _ := readyToRun(t, f)
	j := run(t, f)
	waitUntil(t, f, false, func() bool { return f.move(m.ID).MigrationID != "" })
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := f.move(m.ID); got.State != domain.MoveCancelled {
		t.Fatalf("after the cancel %+v", got)
	}
	f.finishJob(f.migr.last().id, domain.JobCancelled)
	if done := waitJob(t, f, j.ID); done.State == domain.JobSucceeded {
		t.Fatal("a cancelled move's manager.move succeeded")
	}
	if got := f.move(m.ID); got.State != domain.MoveCancelled {
		t.Fatalf("after the cancel %+v", got)
	}
}
