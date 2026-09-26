package domain

import (
	"errors"
	"time"
)

// Digest-driven automatic updates (#20, #9). An update policy opts one
// Docker Manager-managed stack (all of its services, or the listed ones minus
// exclusions) or one Docker Manager-managed standalone container into following
// the digest behind its existing explicit tag. Checks compare the
// registry's host-platform manifest digest with the digest applied on the
// host; runs pull the unchanged tagged reference and recreate what
// changed. Nothing ever rewrites a user's Compose, override or env file,
// and there is no automatic rollback.

// UpdateTargetType is what a policy updates.
type UpdateTargetType string

// Update targets.
const (
	UpdateTargetStack     UpdateTargetType = "stack"
	UpdateTargetContainer UpdateTargetType = "container"
)

// UpdateSchedule is a policy's check or run schedule (#13): its own
// expression, IANA zone and enabled flag (disabled until the user enables
// it: no automatic check or update happens before).
type UpdateSchedule struct {
	Cron     string
	TimeZone string
	Enabled  bool
}

// UpdateWindow restricts scheduled runs to days of the week and a time of
// day (in the run schedule's zone). End before Start spans midnight.
type UpdateWindow struct {
	// Days are 0 (Sunday) to 6; empty means every day.
	Days []int
	// Start and End are "HH:MM"; End is exclusive.
	Start string
	End   string
}

// UpdatePolicy opts one target into digest-driven updates.
type UpdatePolicy struct {
	ID string
	// ParentID identifies the environment policy that manages this target.
	// Empty marks a policy created before environment policies existed.
	ParentID      string
	Inactive      bool
	EnvironmentID string
	Name          string
	TargetType    UpdateTargetType
	// TargetID is the stack ID or the container name.
	TargetID string
	// Services opt in services of a stack (empty: every service);
	// ExcludeServices are never updated.
	Services        []string
	ExcludeServices []string
	Check           UpdateSchedule
	Run             UpdateSchedule
	Window          *UpdateWindow
	// WaitTimeoutSeconds bounds dependency waits and the health
	// confirmation of a run (0: the agent's default).
	WaitTimeoutSeconds int
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// EnvironmentUpdatePolicy controls automatic updates for one environment,
// or every environment when EnvironmentID is empty. Targets are discovered
// when checks and runs are scheduled, so new stacks and containers are covered.
type EnvironmentUpdatePolicy struct {
	ID            string
	EnvironmentID string
	Name          string
	ExcludeStacks []string
	// ExcludeContainers contains container names for a single environment;
	// global policies use environmentID/containerName pairs.
	ExcludeContainers  []string
	Check              UpdateSchedule
	Run                UpdateSchedule
	Window             *UpdateWindow
	WaitTimeoutSeconds int
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// UpdatePolicyPatch changes a policy (nil fields are kept).
type UpdatePolicyPatch struct {
	Name               *string
	Services           *[]string
	ExcludeServices    *[]string
	Check              *UpdateSchedule
	Run                *UpdateSchedule
	Window             *UpdateWindow
	ClearWindow        bool
	WaitTimeoutSeconds *int
}

// UpdateCandidateStatus is the state of one service's (or the
// container's) image.
type UpdateCandidateStatus string

// Candidate statuses.
const (
	// CandidateIneligible: the definition cannot follow a digest (Reason).
	CandidateIneligible UpdateCandidateStatus = "ineligible"
	// CandidateUnchecked: eligible, not checked yet.
	CandidateUnchecked UpdateCandidateStatus = "unchecked"
	// CandidateUpToDate: the registry's host-platform image is the one
	// applied (an index change alone never counts).
	CandidateUpToDate UpdateCandidateStatus = "up_to_date"
	// CandidateAvailable: the tag names a new host-platform digest.
	CandidateAvailable UpdateCandidateStatus = "update_available"
	// CandidateQuarantined: the new digest failed an update and is not
	// tried again automatically.
	CandidateQuarantined UpdateCandidateStatus = "quarantined"
	// CandidateCheckFailed: the registry check failed (ErrorClass:
	// unauthorized, forbidden, rate_limited, ...); nothing is pulled.
	CandidateCheckFailed UpdateCandidateStatus = "check_failed"
	// CandidateRunFailed: the last run could not pull (401/403/429, ...)
	// or its outcome is unknown; a successful check is needed before the
	// next run (no repeated pulls).
	CandidateRunFailed UpdateCandidateStatus = "run_failed"
)

// Ineligibility reasons (UpdateCandidate.Reason), each with a message.
const (
	UpdateReasonBuildOnly      = "build_only"
	UpdateReasonDigestPinned   = "digest_pinned"
	UpdateReasonUntagged       = "untagged"
	UpdateReasonPullPolicy     = "pull_policy_conflict"
	UpdateReasonInvalidRef     = "invalid_reference"
	UpdateReasonNotDeployed    = "not_deployed"
	UpdateReasonNoBaseline     = "no_applied_digest"
	UpdateReasonExcluded       = "excluded"
	UpdateReasonProtected      = "protected"
	UpdateReasonNoRecreateSpec = "no_recreate_spec"
	UpdateReasonStackManaged   = "stack_managed"
)

// UpdateCandidate is the digest model of one service (Service = Compose
// service name) or of the policy's container (Service = container name).
type UpdateCandidate struct {
	ID       string
	PolicyID string
	Service  string
	// Reference is the resolved tagged reference (its literal text in the
	// user's source is never changed); Registry, Repository and Tag are its
	// normalized parts; Platform the host platform checked.
	Reference  string
	Registry   string
	Repository string
	Tag        string
	Platform   string
	// RegistryConnectionID is the connection used for the last check
	// ("" = anonymous).
	RegistryConnectionID string
	Eligible             bool
	Reason               string
	ReasonMessage        string
	// NonVersionTag warns that the tag ("latest", "main") can change
	// meaning; such tags are eligible (#25).
	NonVersionTag bool
	Status        UpdateCandidateStatus
	// AppliedDigest is what runs on the host (repository digest of the
	// applied image), AppliedImageID its image; PreviousDigest what ran
	// before the last update; CandidateDigest the registry's host-platform
	// manifest digest and CandidateIndexDigest the tag's index digest.
	AppliedDigest        string
	AppliedImageID       string
	PreviousDigest       string
	CandidateDigest      string
	CandidateIndexDigest string
	ErrorClass           string
	ErrorMessage         string
	RetryAfterSeconds    int
	CheckedAt            *time.Time
	CheckJobID           string
	// SourceHashBefore/After are the definition hashes read before and
	// after the last check (stacks; empty when the agent was offline).
	SourceHashBefore string
	SourceHashAfter  string
	UpdatedAt        time.Time
}

// UpdateQuarantine is a candidate digest that failed an update.
type UpdateQuarantine struct {
	PolicyID   string
	Service    string
	Digest     string
	JobID      string
	ErrorClass string
	CreatedAt  time.Time
}

// Update history outcomes (UpdateHistoryEntry.Outcome).
const (
	UpdateOutcomeUpdated     = "updated"
	UpdateOutcomeUnchanged   = "unchanged"
	UpdateOutcomeKeptStopped = "kept_stopped"
	UpdateOutcomeFailed      = "failed"
)

// UpdateHistoryEntry records one applied (or failed) digest change.
type UpdateHistoryEntry struct {
	ID                   string
	PolicyID             string
	EnvironmentID        string
	TargetType           UpdateTargetType
	TargetID             string
	Service              string
	Reference            string
	RegistryConnectionID string
	FromDigest           string
	ToDigest             string
	FromImageID          string
	ToImageID            string
	JobID                string
	Outcome              string
	ErrorClass           string
	SourceHashBefore     string
	SourceHashAfter      string
	At                   time.Time
}

// UpdatePreview is what a run would do (nothing is changed).
type UpdatePreview struct {
	Policy UpdatePolicy
	// Items are the candidates the run would apply.
	Items []UpdatePreviewItem
	// Skipped are candidates the run leaves out, with the reason.
	Skipped []UpdateCandidate
	// Restarted are dependents restarted with an updated service
	// (restart: true); Dependencies the stack's dependency graph.
	Restarted    []string
	Dependencies []StackServiceDef
	// SharedTag lists other consumers on the environment of the same tags:
	// they are not recreated, but the pull moves the tag for them (they
	// pick up the new image at their next recreate).
	SharedTag []UpdateSharedConsumer
	// SourceHash is the applied revision's hash the run requires on disk;
	// SourceDrift is set when the definition on disk differs (the run is
	// refused until the stack is deployed).
	SourceHash  string
	SourceDrift bool
	// InWindow reports whether now is inside the update window.
	InWindow bool
	// Fingerprint identifies this preview (candidates, digests, source):
	// a run given it is refused when anything changed since.
	Fingerprint string
}

// UpdatePreviewItem is one service or container a run would update.
type UpdatePreviewItem struct {
	Candidate UpdateCandidate
	// Running: it runs now (it is stopped, recreated and started; a
	// stopped service is kept stopped and not recreated).
	Running bool
	// Downtime explains the expected interruption.
	Downtime string
}

// UpdateSharedConsumer is another stack service or container on the
// environment using one of the run's tags.
type UpdateSharedConsumer struct {
	Reference string
	StackID   string
	StackName string
	Service   string
	Container string
}

// Update errors.
var (
	ErrUpdatePolicyNotFound   = errors.New("update policy not found")
	ErrUpdatePolicyTargetUsed = errors.New("the target already has an update policy")
	ErrUpdatePolicyNameTaken  = errors.New("another update policy in this environment already uses this name")
	ErrUpdateScopeOverlap     = errors.New("an update policy already covers this environment")
)

// UpdateError is a refused update operation with a stable code.
type UpdateError struct {
	Code    string
	Message string
}

func (e *UpdateError) Error() string { return e.Code + ": " + e.Message }

// Update error codes.
const (
	UpdateErrNoCandidates     = "no_update_candidates"
	UpdateErrSourceDrift      = "update_source_drift"
	UpdateErrPreviewStale     = "update_preview_stale"
	UpdateErrTargetIneligible = "update_target_ineligible"
)
