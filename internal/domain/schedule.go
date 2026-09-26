package domain

import (
	"errors"
	"time"
)

// Scheduled work (#13): the manager's shared cron scheduler decides when a
// policy's job is enqueued; the job engine (#26) runs it.

// ScheduleCatchUp says what happens to runs missed while the manager was
// not running (or the clock jumped forward).
type ScheduleCatchUp string

// Catch-up policies.
const (
	// CatchUpOnce: at most one catch-up run is enqueued for all missed
	// runs of a schedule (backups, verification, update checks).
	CatchUpOnce ScheduleCatchUp = "once"
	// CatchUpSkip: missed runs are recorded and not run (prune, update
	// runs: destructive or disruptive work never starts at an unexpected
	// time).
	CatchUpSkip ScheduleCatchUp = "skip"
)

// ScheduleOutcome is the outcome of one scheduled run.
type ScheduleOutcome string

// Run outcomes.
const (
	// RunPending: reserved, the job is being enqueued (transient; resolved
	// after a crash on the next start).
	RunPending ScheduleOutcome = "pending"
	// RunEnqueued: the run's jobs exist; their states are the result.
	RunEnqueued ScheduleOutcome = "enqueued"
	// RunMissed: the manager was not running at the scheduled time and the
	// kind does not catch up (or a later catch-up run superseded it).
	RunMissed ScheduleOutcome = "missed"
	// RunSkipped: the previous run of the policy was still active.
	RunSkipped ScheduleOutcome = "skipped"
	// RunRejected: the policy's revalidation refused the run (disabled,
	// target gone, ...).
	RunRejected ScheduleOutcome = "rejected"
	// RunFailed: the job could not be enqueued.
	RunFailed ScheduleOutcome = "failed"
)

// ScheduleOutcomes returns every outcome.
func ScheduleOutcomes() []ScheduleOutcome {
	return []ScheduleOutcome{RunPending, RunEnqueued, RunMissed, RunSkipped, RunRejected, RunFailed}
}

// Schedule is the scheduler's durable state of one policy's schedule.
type Schedule struct {
	ID string
	// Kind is the schedule kind (backup, prune, ...).
	Kind     string
	PolicyID string
	// Name is the policy's display name.
	Name string
	// EnvironmentID is set for environment-scoped policies.
	EnvironmentID string
	Cron          string
	TimeZone      string
	Enabled       bool
	// InvalidReason is set when Cron or TimeZone cannot be evaluated; the
	// schedule then never runs.
	InvalidReason string
	// Cursor is the last scheduled instant that was processed, or the time
	// the schedule was created, enabled or edited.
	Cursor time.Time
	// NextRunAt is the next run (nil when disabled or invalid).
	NextRunAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScheduleRun is one scheduled instant of a schedule and what happened.
type ScheduleRun struct {
	ID           string
	ScheduleID   string
	ScheduledFor time.Time
	// IdempotencyKey is <kind>:<policy ID>:<instant>; its jobs use it (plus
	// "#<n>") as their job-engine idempotency key.
	IdempotencyKey string
	Outcome        ScheduleOutcome
	// CatchUp marks a run enqueued late for runs missed while the manager
	// was not running.
	CatchUp bool
	// MissedCount / MissedFrom summarize missed instants recorded by this
	// row (outcome missed) or covered by this catch-up run.
	MissedCount int
	MissedFrom  *time.Time
	// Reason explains missed, skipped, rejected and failed runs.
	Reason string
	// ErrorClass is a stable code for rejected and failed runs.
	ErrorClass string
	Jobs       []ScheduleRunJob
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ScheduleRunJob is a job enqueued for a run, with its latest known state.
type ScheduleRunJob struct {
	JobID      string
	Position   int
	Kind       JobKind
	State      JobState
	ErrorClass string
	// BlockedReason is the job's blocked reason while it waits.
	BlockedReason string
	FinishedAt    *time.Time
}

// Result summarizes a run: the outcome for runs without jobs; for
// enqueued runs "active" while a job is not terminal, otherwise the job
// state (the worst one when a run has several jobs).
func (r ScheduleRun) Result() string {
	if r.Outcome != RunEnqueued {
		return string(r.Outcome)
	}
	worst := JobSucceeded
	rank := map[JobState]int{JobSucceeded: 0, JobCancelled: 1, JobPartial: 2, JobInterrupted: 3, JobFailed: 4}
	for _, j := range r.Jobs {
		if !j.State.Terminal() {
			return "active"
		}
		if rank[j.State] > rank[worst] {
			worst = j.State
		}
	}
	return string(worst)
}

// ScheduleDefault is the editable default cron expression of a kind.
type ScheduleDefault struct {
	Kind  string
	Label string
	Cron  string
	// Suggested is Docker Manager's shipped suggestion.
	Suggested string
	CatchUp   ScheduleCatchUp
}

// ScheduleDefaults are the instance's schedule settings: one default
// expression per kind and the default time zone. They only prefill new
// policies; existing policies keep their saved expression and zone.
type ScheduleDefaults struct {
	TimeZone  string
	Kinds     []ScheduleDefault
	Revision  int64
	UpdatedAt time.Time
}

// Default returns the default of kind.
func (d ScheduleDefaults) Default(kind string) (ScheduleDefault, bool) {
	for _, k := range d.Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return ScheduleDefault{}, false
}

// ScheduleDefaultsPatch changes schedule defaults (nil/absent = unchanged).
type ScheduleDefaultsPatch struct {
	TimeZone *string
	// Crons maps kinds to new default expressions.
	Crons map[string]string
}

// ScheduleFilter selects schedules.
type ScheduleFilter struct {
	Kind          string
	EnvironmentID string
	Enabled       *bool
	// AfterID continues after this schedule ID (pagination).
	AfterID string
	Limit   int
}

// Schedule errors.
var (
	// ErrScheduleInvalid wraps validation errors (cron expression, time
	// zone, unknown kind); the wrapped *cron.ParseError names the field.
	ErrScheduleInvalid = errors.New("invalid schedule")
	// ErrScheduleKindUnknown: no schedule kind with this key.
	ErrScheduleKindUnknown = errors.New("unknown schedule kind")
)
