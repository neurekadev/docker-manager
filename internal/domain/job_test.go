package domain

import "testing"

// TestJobTransitionTable pins the complete state machine: every allowed
// transition is listed here and everything else must be rejected.
func TestJobTransitionTable(t *testing.T) {
	allowed := map[[2]JobState]bool{}
	allow := func(from JobState, to ...JobState) {
		for _, s := range to {
			allowed[[2]JobState{from, s}] = true
		}
	}
	terminal := []JobState{JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted}
	allow(JobQueued, JobBlocked, JobDispatched, JobCancelled, JobFailed)
	allow(JobBlocked, JobDispatched, JobCancelled, JobFailed)
	allow(JobDispatched, append([]JobState{JobRunning, JobCancelling}, terminal...)...)
	allow(JobRunning, append([]JobState{JobDispatched, JobCancelling}, terminal...)...)
	allow(JobCancelling, terminal...)

	for _, from := range JobStates() {
		for _, to := range JobStates() {
			want := allowed[[2]JobState{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestJobStateClassification(t *testing.T) {
	for _, s := range JobStates() {
		if !s.Valid() {
			t.Errorf("%s not valid", s)
		}
		n := 0
		for _, b := range []bool{s.Terminal(), s.Waiting(), s.Active()} {
			if b {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s must be exactly one of terminal/waiting/active", s)
		}
		// Terminal states are final.
		if s.Terminal() {
			for _, to := range JobStates() {
				if CanTransition(s, to) {
					t.Errorf("terminal %s can move to %s", s, to)
				}
			}
		}
	}
	if JobState("bogus").Valid() || CanTransition("bogus", JobQueued) {
		t.Fatal("unknown state accepted")
	}
}

// TestEveryStateReachesATerminalState ensures no job can be stranded: failed
// is reachable from every waiting or active state and cancelled from every
// state except cancelling (which ends in whatever outcome the executor
// reaches), so a job never silently disappears.
func TestEveryStateReachesATerminalState(t *testing.T) {
	for _, s := range JobStates() {
		if s.Terminal() {
			continue
		}
		if !CanTransition(s, JobCancelled) && s != JobCancelling {
			t.Errorf("%s cannot be cancelled", s)
		}
		if !CanTransition(s, JobFailed) {
			t.Errorf("%s cannot fail", s)
		}
	}
	for _, o := range []JobOrigin{OriginManual, OriginScheduled, OriginAPIToken} {
		if !o.Valid() {
			t.Errorf("%s invalid", o)
		}
	}
	if JobOrigin("cron").Valid() {
		t.Error("unknown origin valid")
	}
}
