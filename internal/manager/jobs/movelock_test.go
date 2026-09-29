package jobs_test

import (
	"errors"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
)

// TestMoveLockStopsNewJobs (manager move): while the manager moves to a
// new server the engine refuses every new job (manual and scheduled) with
// ErrManagerMoved and dispatches nothing already queued; the queued job
// stays queued (it travels in the copy) and starts once the lock opens
// (a cancelled move).
func TestMoveLockStopsNewJobs(t *testing.T) {
	lock := movelock.New()
	h := newHarness(t, func(o *jobs.Options) { o.MoveLock = lock })
	h.disp.Connect("e1")
	queued := h.enqueue(jobs.Request{Kind: jobspec.ContainerRestart, EnvironmentID: "e1", Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})

	lock.Set(movelock.ReadOnly)
	for _, p := range []authz.Principal{user("alice"), authz.Service()} {
		_, _, err := h.eng.Enqueue(h.ctx, jobs.Request{Kind: jobspec.ContainerStop, Principal: p, EnvironmentID: "e1",
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "db"}}})
		if !errors.Is(err, jobs.ErrManagerMoved) {
			t.Fatalf("enqueue as %s while moving: %v, want ErrManagerMoved", p.Kind, err)
		}
	}
	h.dispatch()
	h.wantState(queued.ID, domain.JobQueued)
	if n := h.disp.Pending("e1"); n != 0 {
		t.Fatalf("%d commands sent while moving", n)
	}

	lock.Set(movelock.AgentsRefused)
	h.dispatch()
	h.wantState(queued.ID, domain.JobQueued)

	lock.Set(movelock.Open)
	h.dispatch()
	h.wantState(queued.ID, domain.JobDispatched)
}
