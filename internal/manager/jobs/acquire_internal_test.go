package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type onlineAll struct{ sent []*protocol.Frame }

func (*onlineAll) Online(string) bool { return true }
func (o *onlineAll) Send(_ context.Context, _ string, f *protocol.Frame) error {
	o.sent = append(o.sent, f)
	return nil
}

// TestLockAcquisitionIsAllOrNothing fails the acquisition transaction in the
// middle of inserting a job's lock set: no lock, no fencing token and no
// state change may persist (no partial acquisition).
func TestLockAcquisitionIsAllOrNothing(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	disp := &onlineAll{}
	e, err := New(Options{DB: db, Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Dispatcher: disp})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	j, _, err := e.Enqueue(ctx, Request{Kind: jobspec.RestoreRun, Principal: authz.Service(), EnvironmentID: "e1",
		Targets: []domain.JobTarget{{Type: domain.TargetVolume, ID: "a"}, {Type: domain.TargetVolume, ID: "b"}, {Type: domain.TargetRepository, ID: "r"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Locks) < 4 {
		t.Fatalf("locks %v", j.Locks)
	}
	injected := errors.New("disk I/O error")
	var order []int
	e.testHookLockInsert = func(i int) error {
		order = append(order, i)
		if i == 2 {
			return injected
		}
		return nil
	}
	if err := e.DispatchPending(ctx); !errors.Is(err, injected) {
		t.Fatalf("dispatch err = %v", err)
	}
	if held, _ := store.HeldLocks(ctx, db); len(held) != 0 {
		t.Fatalf("partial acquisition persisted: %+v", held)
	}
	got, _ := store.GetJob(ctx, db, j.ID)
	if !got.State.Waiting() || got.FencingToken != 0 || len(disp.sent) != 0 {
		t.Fatalf("job changed: %+v sent %d", got, len(disp.sent))
	}

	e.testHookLockInsert = nil
	if err := e.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	held, _ := store.HeldLocks(ctx, db)
	if len(held) != len(j.Locks) {
		t.Fatalf("held %d, want %d", len(held), len(j.Locks))
	}
	got, _ = store.GetJob(ctx, db, j.ID)
	// The rolled-back attempt did not consume a fencing token.
	if got.State != domain.JobDispatched || got.FencingToken != 1 || len(disp.sent) != 1 {
		t.Fatalf("job %+v", got)
	}
}

func TestTransitionRejectsIllegalMoves(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	e, _ := New(Options{DB: db, Clock: testutil.FakeClock()})
	defer e.Close()
	j, _, err := e.Enqueue(ctx, Request{Kind: jobspec.StackStart, Principal: authz.Service(), EnvironmentID: "e1",
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.JobState{domain.JobRunning, domain.JobSucceeded, domain.JobInterrupted, domain.JobCancelling} {
		cp := j
		if err := e.transition(ctx, db, &cp, to, "x"); !errors.Is(err, errIllegalTransition) {
			t.Errorf("queued -> %s: %v", to, err)
		}
	}
	if got, _ := store.GetJob(ctx, db, j.ID); got.State != domain.JobQueued {
		t.Fatalf("state changed to %s", got.State)
	}
}
