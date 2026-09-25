package domain

import (
	"errors"
	"slices"
	"time"
)

// Audit trail (#30): one append-only, hash-chained record per security
// relevant or mutating action. The recording, redaction, chain and
// retention rules live in internal/manager/audit; these are the shared
// shapes.

// AuditActorKind says who acted.
type AuditActorKind string

// Actor kinds.
const (
	// AuditActorUser: a signed-in user (browser session).
	AuditActorUser AuditActorKind = "user"
	// AuditActorAPIToken: an API token (#31); the record also names the
	// token's owning user.
	AuditActorAPIToken AuditActorKind = "api_token"
	// AuditActorService: the manager's internal service identity
	// (scheduled work, retention, recovery).
	AuditActorService AuditActorKind = "service"
	// AuditActorAgent: an agent (#3), identified by its agent ID.
	AuditActorAgent AuditActorKind = "agent"
	// AuditActorAnonymous: no authenticated principal (failed sign-in,
	// setup, invitation redemption).
	AuditActorAnonymous AuditActorKind = "anonymous"
)

// AuditActorKinds returns every actor kind.
func AuditActorKinds() []AuditActorKind {
	return []AuditActorKind{AuditActorUser, AuditActorAPIToken, AuditActorService, AuditActorAgent, AuditActorAnonymous}
}

// Valid reports whether k is known.
func (k AuditActorKind) Valid() bool { return slices.Contains(AuditActorKinds(), k) }

// AuditActor identifies who acted.
type AuditActor struct {
	Kind AuditActorKind
	// UserID is the user (for api_token: the token's owner).
	UserID string
	// TokenID is set for api_token actors.
	TokenID string
	// AgentID is set for agent actors.
	AgentID string
}

// AuditOutcome is the result of an audited action.
type AuditOutcome string

// Outcomes.
const (
	// AuditSuccess: the action took effect (or was accepted, e.g. a job).
	AuditSuccess AuditOutcome = "success"
	// AuditPartial: some items succeeded, some failed (partial jobs).
	AuditPartial AuditOutcome = "partial"
	// AuditFailure: the action was rejected or failed (validation,
	// conflict, failed/cancelled/interrupted job).
	AuditFailure AuditOutcome = "failure"
	// AuditDenied: authentication or authorization refused the action.
	AuditDenied AuditOutcome = "denied"
	// AuditError: an internal error stopped the action.
	AuditError AuditOutcome = "error"
)

// AuditOutcomes returns every outcome.
func AuditOutcomes() []AuditOutcome {
	return []AuditOutcome{AuditSuccess, AuditPartial, AuditFailure, AuditDenied, AuditError}
}

// Valid reports whether o is known.
func (o AuditOutcome) Valid() bool { return slices.Contains(AuditOutcomes(), o) }

// AuditCategory groups events as in #30.
type AuditCategory string

// Categories.
const (
	// AuditIdentity: sign-in, step-up, sessions, factors, passwords, setup,
	// ownership, invitations, user enable/disable.
	AuditIdentity AuditCategory = "identity"
	// AuditAuthorization: group and permission changes.
	AuditAuthorization AuditCategory = "authorization"
	// AuditCredentials: API tokens, agent enrollment/rotation/revocation,
	// registry and Git credentials (and their use), backup keys.
	AuditCredentials AuditCategory = "credentials"
	// AuditOperations: Docker, stack, build, file, settings and policy
	// mutations, jobs, restores, prunes, exec sessions, downloads.
	AuditOperations AuditCategory = "operations"
	// AuditSystem: the audit trail's own maintenance (retention purges,
	// exports) and other manager-internal events.
	AuditSystem AuditCategory = "system"
)

// AuditCategories returns every category.
func AuditCategories() []AuditCategory {
	return []AuditCategory{AuditIdentity, AuditAuthorization, AuditCredentials, AuditOperations, AuditSystem}
}

// Valid reports whether c is known.
func (c AuditCategory) Valid() bool { return slices.Contains(AuditCategories(), c) }

// AuditTarget is one resource an audited action touched.
type AuditTarget struct {
	// Type is a lower snake_case resource type (stack, container, job, user, ...).
	Type string
	// ID is the resource's stable identifier (never a secret, never file contents).
	ID string
	// EnvironmentID is the environment ("host") of environment-scoped resources.
	EnvironmentID string
}

// AuditEvent is what callers record. Zero fields are filled from the
// context where possible (actor, client IP, request ID) by the recorder.
type AuditEvent struct {
	// At defaults to the recorder's clock.
	At       time.Time
	Category AuditCategory
	// Action is a dotted key: a #17 capability key for authorized
	// operations, or a reserved key for lifecycle/identity events
	// (audit.Actions documents them).
	Action string
	// OperationID is the public API operation, when recorded for a request.
	OperationID string
	Actor       AuditActor
	ClientIP    string
	UserAgent   string
	// EnvironmentID is the environment the action ran in, if any.
	EnvironmentID string
	Targets       []AuditTarget
	Outcome       AuditOutcome
	// ErrorClass is a stable snake_case code (API error code or job error
	// class). Messages are never recorded: they may carry data.
	ErrorClass string
	JobID      string
	RequestID  string
	// Details is a small JSON-compatible object (rule diffs, counts,
	// origins). It passes the redaction layer before it is stored.
	Details map[string]any
}

// AuditRecord is a stored, chained audit record.
type AuditRecord struct {
	// Seq is the record's position in the chain (1, 2, 3, ...).
	Seq           int64
	ID            string
	At            time.Time
	Category      AuditCategory
	Action        string
	OperationID   string
	Actor         AuditActor
	ClientIP      string
	UserAgent     string
	EnvironmentID string
	Targets       []AuditTarget
	Outcome       AuditOutcome
	ErrorClass    string
	JobID         string
	RequestID     string
	// Details is the redacted details object as canonical JSON.
	Details []byte
	// PrevHash is the hash of the preceding record (or the purge anchor).
	PrevHash string
	// Hash is SHA-256 over PrevHash and the canonical record.
	Hash string
}

// AuditFilter selects audit records. Empty fields match everything;
// repeated values OR, different fields AND.
type AuditFilter struct {
	ActorKinds []AuditActorKind
	// ActorID matches the actor's user, token or agent ID.
	ActorID       string
	Actions       []string
	Categories    []AuditCategory
	Outcomes      []AuditOutcome
	EnvironmentID string
	// Resource matches a target (Type and ID); Type "job" also matches the
	// record's job ID.
	Resource *AuditTarget
	JobID    string
	// Since (inclusive) and Until (exclusive) bound the timestamp.
	Since time.Time
	Until time.Time
	// AfterSeq/BeforeSeq are exclusive cursor bounds (0 = unbounded).
	AfterSeq  int64
	BeforeSeq int64
	// Ascending lists oldest first (exports); the default is newest first.
	Ascending bool
	Limit     int
}

// ErrAuditChainBroken reports failed chain verification.
var ErrAuditChainBroken = errors.New("audit chain verification failed")
