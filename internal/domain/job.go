package domain

import (
	"errors"
	"slices"
	"time"
)

// JobKind names a kind of job in the catalog (internal/jobspec), e.g.
// "stack.deploy". Kind names are stable identifiers stored in the database
// and sent to agents; never rename one.
type JobKind string

// JobState is the lifecycle state of a job. See CanTransition.
type JobState string

// Job states. Waiting: queued, blocked. Active (holding locks): dispatched,
// running, cancelling. Terminal: succeeded, failed, partial, cancelled,
// interrupted.
const (
	JobQueued      JobState = "queued"
	JobBlocked     JobState = "blocked"
	JobDispatched  JobState = "dispatched"
	JobRunning     JobState = "running"
	JobCancelling  JobState = "cancelling"
	JobSucceeded   JobState = "succeeded"
	JobFailed      JobState = "failed"
	JobPartial     JobState = "partial"
	JobCancelled   JobState = "cancelled"
	JobInterrupted JobState = "interrupted"
)

// JobStates returns every job state.
func JobStates() []JobState {
	return []JobState{JobQueued, JobBlocked, JobDispatched, JobRunning, JobCancelling,
		JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted}
}

// Terminal reports whether s is final. A terminal job holds no locks and
// never changes state again.
func (s JobState) Terminal() bool {
	switch s {
	case JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted:
		return true
	}
	return false
}

// Waiting reports whether s is queued or blocked (no locks held yet).
func (s JobState) Waiting() bool { return s == JobQueued || s == JobBlocked }

// Active reports whether s holds locks and has been handed to an executor.
func (s JobState) Active() bool {
	return s == JobDispatched || s == JobRunning || s == JobCancelling
}

// Valid reports whether s is a known state.
func (s JobState) Valid() bool { return slices.Contains(JobStates(), s) }

// jobTransitions is THE table of legal state changes. Every state change of
// a job goes through CanTransition; keep docs/architecture/job-engine.md in
// sync.
var jobTransitions = map[JobState][]JobState{
	// queued -> failed: offline deadline, lost authorization, unknown kind.
	JobQueued:  {JobBlocked, JobDispatched, JobCancelled, JobFailed},
	JobBlocked: {JobDispatched, JobCancelled, JobFailed},
	// dispatched -> terminal: result before/without ack, offline deadline
	// for an unacknowledged command, reconciliation after reconnect.
	JobDispatched: {JobRunning, JobCancelling, JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted},
	// running -> dispatched: an interrupted attempt whose in-flight step is
	// idempotent is re-dispatched as a new attempt (resume).
	JobRunning:    {JobDispatched, JobCancelling, JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted},
	JobCancelling: {JobSucceeded, JobFailed, JobPartial, JobCancelled, JobInterrupted},
}

// CanTransition reports whether a job may move from one state to another.
func CanTransition(from, to JobState) bool {
	return slices.Contains(jobTransitions[from], to)
}

// JobOrigin says why a job exists. It is audit metadata, never an
// access-control owner (#17).
type JobOrigin string

// Job origins.
const (
	OriginManual    JobOrigin = "manual"
	OriginScheduled JobOrigin = "scheduled"
	OriginAPIToken  JobOrigin = "api_token"
)

// Valid reports whether o is known.
func (o JobOrigin) Valid() bool {
	return o == OriginManual || o == OriginScheduled || o == OriginAPIToken
}

// JobExecutor says where a job's steps run.
type JobExecutor string

// Executors.
const (
	ExecutorAgent   JobExecutor = "agent"
	ExecutorManager JobExecutor = "manager"
)

// TargetType is the type of resource a job targets.
type TargetType string

// Target types.
const (
	TargetStack      TargetType = "stack"
	TargetContainer  TargetType = "container"
	TargetVolume     TargetType = "volume"
	TargetImage      TargetType = "image"
	TargetNetwork    TargetType = "network"
	TargetRepository TargetType = "repository"
	// TargetPath is a file-system path the job reads (or modifies in place).
	TargetPath TargetType = "path"
	// TargetDestinationPath is a file-system path the job writes.
	TargetDestinationPath TargetType = "destination_path"
)

// TargetTypes returns every target type.
func TargetTypes() []TargetType {
	return []TargetType{TargetStack, TargetContainer, TargetVolume, TargetImage, TargetNetwork,
		TargetRepository, TargetPath, TargetDestinationPath}
}

// JobTarget is one resource a job acts on.
type JobTarget struct {
	Type TargetType
	// ID is the resource's stable identifier within its environment (stack
	// ID, container name or ID, volume name, image reference, network name,
	// absolute path, repository ID).
	ID string
	// EnvironmentID overrides the job's environment for this target
	// (migrations touch two environments). Empty means the job's environment.
	EnvironmentID string
}

// LockScope is a resource dimension of the lock matrix.
type LockScope string

// Lock scopes.
const (
	LockHost       LockScope = "host"
	LockStack      LockScope = "stack"
	LockContainer  LockScope = "container"
	LockVolume     LockScope = "volume"
	LockImage      LockScope = "image"
	LockNetwork    LockScope = "network"
	LockFilePath   LockScope = "file_path"
	LockRepository LockScope = "repository"
)

// LockScopes returns every scope.
func LockScopes() []LockScope {
	return []LockScope{LockHost, LockStack, LockContainer, LockVolume, LockImage, LockNetwork, LockFilePath, LockRepository}
}

// LockMode is shared or exclusive.
type LockMode string

// Lock modes.
const (
	LockShared    LockMode = "shared"
	LockExclusive LockMode = "exclusive"
)

// LockAll is the lock name matching every resource of a scope in an
// environment (e.g. prune takes a shared lock on all volumes).
const LockAll = "*"

// JobLock is one entry of a job's lock set.
type JobLock struct {
	Scope LockScope
	// EnvironmentID is empty for instance-wide scopes (repository).
	EnvironmentID string
	// Name identifies the resource within scope and environment: empty for
	// host, LockAll for every resource, a cleaned absolute path for
	// file_path (prefix semantics), otherwise the target ID.
	Name string
	Mode LockMode
}

// JobProgress is the latest progress report of a job.
type JobProgress struct {
	// Percent is 0..100, or -1 when unknown.
	Percent int
	Step    string
	Message string
}

// Item result statuses.
const (
	ItemSucceeded = "succeeded"
	ItemFailed    = "failed"
	ItemSkipped   = "skipped"
)

// JobItem is the result for one item of a multi-item job (a pruned image, a
// backed-up volume, ...).
type JobItem struct {
	Name    string
	Status  string
	Message string
}

// Blocked reasons.
const (
	BlockedLock         = "lock"
	BlockedAgentOffline = "agent_offline"
	BlockedConcurrency  = "concurrency_limit"
)

// Stable job error classes.
const (
	// ErrorAgentOffline: the environment's agent was offline past the kind's
	// deadline; no Docker mutation was attempted.
	ErrorAgentOffline = "agent_offline"
	// ErrorAuthorizationRevoked: the initiator lost the required grant
	// before the queued job was dispatched.
	ErrorAuthorizationRevoked = "authorization_revoked"
	// ErrorStepFailed: a step returned an error.
	ErrorStepFailed = "step_failed"
	// ErrorUnknownOutcome: a non-idempotent step was in flight when the
	// executor died; it is never retried automatically.
	ErrorUnknownOutcome = "unknown_outcome"
	// ErrorJournalLost: the agent has no record of a job it acknowledged.
	ErrorJournalLost = "journal_lost"
	// ErrorResumeLimit: the job was interrupted too many times.
	ErrorResumeLimit = "resume_limit"
	// ErrorRejected: the agent refused the command (unsupported kind, bad input).
	ErrorRejected = "rejected"
	// ErrorCompensationFailed: a compensating step (e.g. restarting stopped
	// containers) failed; see recovery guidance.
	ErrorCompensationFailed = "compensation_failed"
	// ErrorExecutorRestarted: the executor (agent or manager) stopped while
	// the job was running and the job could not resume.
	ErrorExecutorRestarted = "executor_restarted"
	// ErrorCancelled: the job was cancelled.
	ErrorCancelled = "cancelled"
	// ErrorInternal: an engine bug or database failure.
	ErrorInternal = "internal"
)

// Job is a durable, manager-owned unit of long or mutating work (#26).
type Job struct {
	ID       string
	Kind     JobKind
	Executor JobExecutor
	Origin   JobOrigin
	// InitiatorUserID and InitiatorTokenID are audit metadata only.
	InitiatorUserID  string
	InitiatorTokenID string
	PolicyID         string
	// EnvironmentID is empty for manager-only kinds.
	EnvironmentID string
	Targets       []JobTarget
	// Input is the kind-specific JSON input (an object).
	Input          []byte
	InputHash      string
	IdempotencyKey string
	Attempt        int
	State          JobState
	Progress       JobProgress
	Items          []JobItem
	ErrorClass     string
	ErrorMessage   string
	// Recovery is operator guidance for non-successful terminal states.
	Recovery      string
	BlockedBy     string
	BlockedReason string
	// Locks is the planned lock set (computed from the kind and targets at
	// enqueue); it is held while the job is active.
	Locks           []JobLock
	FencingToken    uint64
	CancelRequested bool
	// Execution journal (step bookkeeping for resume/recovery).
	CurrentStep    string
	StepInFlight   bool
	CompletedSteps []string
	Compensations  []JobCompensation
	Resumes        int
	// LastEventSeq is the sequence number of the newest event.
	LastEventSeq int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DispatchedAt *time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// JobCompensation is a compensating action registered by a step (e.g.
// "restart the containers stopped for a backup"). Unreleased compensations
// always run when a job does not succeed, including after a crash.
type JobCompensation struct {
	Name     string
	Args     []byte
	Released bool
	Done     bool
	Error    string
}

// Job event types.
const (
	JobEventState    = "state"
	JobEventProgress = "progress"
	JobEventItem     = "item"
	JobEventLog      = "log"
	JobEventWarning  = "warning"
)

// JobEvent is one entry of a job's bounded progress/event log.
type JobEvent struct {
	JobID   string
	Seq     int64
	At      time.Time
	Type    string
	State   JobState
	Message string
	// Percent is set for progress events (-1 unknown).
	Percent int
	Step    string
	Item    *JobItem
}

// JobFilter selects jobs for listing. Results are newest first (by ID,
// which is a UUIDv7).
type JobFilter struct {
	States        []JobState
	Kinds         []JobKind
	EnvironmentID string
	// Target matches jobs with this target (EnvironmentID optional).
	Target *JobTarget
	// BeforeID returns jobs with ID < BeforeID (pagination cursor).
	BeforeID string
	Limit    int
}

// Job engine errors shared by the engine and the API.
var (
	ErrJobNotFound            = errors.New("job not found")
	ErrJobFinished            = errors.New("job already finished")
	ErrJobIdempotencyConflict = errors.New("idempotency key already used with a different request")
	ErrJobForbidden           = errors.New("not permitted to run this job")
	ErrJobUnknownKind         = errors.New("unknown job kind")
	ErrJobInvalid             = errors.New("invalid job request")
	ErrJobKindUnavailable     = errors.New("job kind has no executor on this manager")
)
