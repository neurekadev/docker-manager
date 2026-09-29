package scheduler

import (
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
)

// TestMoveLockFiresNothing (manager move): while the manager moves to a
// new server the scheduler fires no schedule; once the lock opens again (a
// cancelled move) the due run is enqueued as usual.
func TestMoveLockFiresNothing(t *testing.T) {
	h := newHarness(t, at("2026-03-01T00:00:00Z"))
	lock := movelock.New()
	h.opts.MoveLock = lock
	h.start()
	h.policy(KindPrune, testPolicy{PolicySchedule: PolicySchedule{PolicyID: "pol-1", Cron: "0 3 * * *", Enabled: true}})
	h.tick()

	lock.Set(movelock.ReadOnly)
	h.tickAt("2026-03-01T03:00:00Z")
	if n := len(h.allJobs()); n != 0 {
		t.Fatalf("%d jobs fired while the manager moves", n)
	}
	if runs := h.history(KindPrune, "pol-1"); len(runs) != 0 {
		t.Fatalf("history while moving: %+v", runs)
	}

	lock.Set(movelock.Open)
	h.tick()
	if n := len(h.allJobs()); n != 1 {
		t.Fatalf("%d jobs after the lock opened, want the due run", n)
	}
}
