package domain

import (
	"errors"
	"time"
)

// Environments, agents and enrollments (#3). An Environment is one enrolled
// agent plus the Docker Engine it controls. The agent instance ID (Agent.ID)
// is distinct from the Engine ID; the pair (Engine ID, agent-generated
// install ID) identifies an installation because Engine IDs can collide on
// cloned VMs. V1 allows one active agent per Engine.

// EnvironmentStatus is the lifecycle state of an environment.
type EnvironmentStatus string

// Environment statuses.
const (
	EnvironmentActive   EnvironmentStatus = "active"
	EnvironmentArchived EnvironmentStatus = "archived"
)

// Environment is an enrolled Docker Engine with its display name.
type Environment struct {
	ID string
	// Name is the editable server/display name.
	Name string
	// ServiceAddress is the optional host name or IP users browse to (for
	// links to published ports, #22).
	ServiceAddress string
	// EngineID and InstallID identify the installation (the current or
	// last agent's).
	EngineID  string
	InstallID string
	// AgentID is the active agent; empty while detached (agent removed).
	AgentID string
	Status  EnvironmentStatus
	// Online is true while the agent's session is established and its jobs
	// were reconciled (persisted on every transition).
	Online              bool
	ConnectionChangedAt *time.Time
	LastSeenAt          *time.Time
	// AllowDuplicateEngineID records that the owner declared this
	// installation a distinct host sharing an Engine ID (cloned VM).
	AllowDuplicateEngineID bool
	Revision               int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ArchivedAt             *time.Time
}

// AgentStatus is the lifecycle state of an agent instance.
type AgentStatus string

// Agent statuses.
const (
	AgentActive  AgentStatus = "active"
	AgentRevoked AgentStatus = "revoked"
)

// Agent is one enrolled agent instance.
type Agent struct {
	ID            string
	EnvironmentID string
	EnrollmentID  string
	InstallID     string
	EngineID      string
	// Hostname is the Engine host name reported at enrollment.
	Hostname string
	// Label is an optional operator note.
	Label string
	// Version is the agent version last reported; VersionStatus is current,
	// outdated (previous minor, still supported) or unsupported.
	Version       string
	VersionStatus string
	Status        AgentStatus
	RevokedReason string
	// Capabilities is the last capabilities payload (JSON of
	// protocol.CapabilitiesPayload), "" before the first session.
	Capabilities    string
	CapabilitiesAt  *time.Time
	SessionID       string
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastConnectedAt *time.Time
	LastSeenAt      *time.Time
	RevokedAt       *time.Time
	// RotationPending is true while a new credential waits for the
	// agent's confirmation.
	RotationPending bool
}

// CredentialState is the state of an agent credential.
type CredentialState string

// Credential states.
const (
	CredentialActive  CredentialState = "active"
	CredentialPending CredentialState = "pending"
	CredentialRevoked CredentialState = "revoked"
)

// AgentCredential is the stored (verifier-only) form of an agent credential.
type AgentCredential struct {
	ID       string
	AgentID  string
	Verifier string
	State    CredentialState
	// Sealed holds a pending rotation's credential, sealed with the
	// secret-protection key, until the agent confirms it.
	Sealed      string
	CreatedAt   time.Time
	ActivatedAt *time.Time
	RevokedAt   *time.Time
}

// EnrollmentIntent is fixed when the owner creates an enrollment (#25).
type EnrollmentIntent string

// Enrollment intents.
const (
	// IntentNew creates a new environment.
	IntentNew EnrollmentIntent = "new"
	// IntentReplace replaces the target agent for the same Engine; its
	// credential is revoked when the enrollment succeeds.
	IntentReplace EnrollmentIntent = "replace"
	// IntentReattach re-attaches the target archived environment (#34).
	IntentReattach EnrollmentIntent = "reattach"
)

// EnrollmentState is derived from an enrollment's timestamps.
type EnrollmentState string

// Enrollment states.
const (
	EnrollmentPending EnrollmentState = "pending"
	EnrollmentUsed    EnrollmentState = "used"
	EnrollmentExpired EnrollmentState = "expired"
	EnrollmentRevoked EnrollmentState = "revoked"
)

// Enrollment is a one-use agent enrollment token (stored as a verifier).
type Enrollment struct {
	ID       string
	Verifier string
	Intent   EnrollmentIntent
	// TargetID is the agent (replace) or environment (reattach) ID.
	TargetID string
	// EnvironmentName presets the new environment's name.
	EnvironmentName string
	// AllowDuplicateEngineID lets a distinct host that shares an enrolled
	// Engine's ID (a cloned VM) enroll as its own environment.
	AllowDuplicateEngineID bool
	// CreatedBy is the creating user (audit metadata only).
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
	// AgentID is the agent created by the successful enrollment.
	AgentID string
	// Rejection is the last refused enrollment attempt (surfaced so the
	// owner can resolve duplicate Engines and Engine ID collisions).
	Rejection *EnrollmentRejection
}

// State derives the enrollment state at now.
func (e Enrollment) State(now time.Time) EnrollmentState {
	switch {
	case e.UsedAt != nil:
		return EnrollmentUsed
	case e.RevokedAt != nil:
		return EnrollmentRevoked
	case !now.Before(e.ExpiresAt):
		return EnrollmentExpired
	}
	return EnrollmentPending
}

// EnrollmentRejection describes a refused enrollment attempt.
type EnrollmentRejection struct {
	// Code is the stable error code (engine_already_enrolled,
	// engine_identity_conflict, environment_archived, environment_detached,
	// engine_mismatch, enrollment_target_unavailable).
	Code                  string
	Message               string
	At                    time.Time
	EngineID              string
	InstallID             string
	Hostname              string
	ConflictAgentID       string
	ConflictEnvironmentID string
}

// Agent/environment errors.
var (
	// ErrEnrollmentInvalid: the token is unknown, expired, revoked, used or
	// wrong. Deliberately one error so responses cannot tell them apart.
	ErrEnrollmentInvalid = errors.New("enrollment token is not valid")
	// ErrCredentialInvalid: the agent credential is unknown, revoked or
	// rotated out (one error for the same reason).
	ErrCredentialInvalid = errors.New("agent credential is not valid")
	// ErrAgentNotFound, ErrEnvironmentNotFound, ErrEnrollmentNotFound.
	ErrAgentNotFound       = errors.New("agent not found")
	ErrEnvironmentNotFound = errors.New("environment not found")
	ErrEnrollmentNotFound  = errors.New("enrollment not found")
	// ErrRevisionMismatch: a compare-and-swap edit lost against another edit.
	ErrRevisionMismatch = errors.New("revision changed")
	// ErrEnvironmentArchived: the environment is archived.
	ErrEnvironmentArchived = errors.New("environment is archived")
	// ErrAgentRevoked: the agent was removed or replaced.
	ErrAgentRevoked = errors.New("agent is revoked")
	// ErrEnrollmentFinished: the enrollment was already used or revoked.
	ErrEnrollmentFinished = errors.New("enrollment already used or revoked")
)

// EnrollConflict is a refused enrollment (409) with a stable code.
type EnrollConflict struct {
	Rejection EnrollmentRejection
}

func (e *EnrollConflict) Error() string { return e.Rejection.Code + ": " + e.Rejection.Message }

// Enrollment conflict codes.
const (
	ConflictEngineAlreadyEnrolled = "engine_already_enrolled"
	ConflictEngineIdentity        = "engine_identity_conflict"
	ConflictEnvironmentArchived   = "environment_archived"
	ConflictEnvironmentDetached   = "environment_detached"
	ConflictEngineMismatch        = "engine_mismatch"
	ConflictTargetUnavailable     = "enrollment_target_unavailable"
)

// AgentFilter selects agents; lists are ordered by ID, newest first.
type AgentFilter struct {
	EnvironmentID string
	EngineID      string
	Statuses      []AgentStatus
	// BeforeID continues before this ID; Limit bounds the result (0: all).
	BeforeID string
	Limit    int
}

// EnvironmentFilter selects environments; lists are ordered by ID.
type EnvironmentFilter struct {
	Statuses []EnvironmentStatus
	EngineID string
	// AfterID continues after this ID; Limit bounds the result (0: all).
	AfterID string
	Limit   int
}

// EnrollmentSpec creates an enrollment token.
type EnrollmentSpec struct {
	// Intent is new (default), replace (TargetID = agent) or reattach
	// (TargetID = archived or detached environment).
	Intent   EnrollmentIntent
	TargetID string
	// EnvironmentName presets the new environment's display name.
	EnvironmentName string
	// TTL is the token lifetime (0: the default of one hour).
	TTL time.Duration
	// AllowDuplicateEngineID: see Enrollment. Only with IntentNew.
	AllowDuplicateEngineID bool
	// CreatedBy is the creating user (audit metadata).
	CreatedBy string
}

// InstallCommand is one generated way to start an agent with an enrollment
// token (the token travels in an environment variable, a .env file or on
// stdin, never in a URL).
type InstallCommand struct {
	// Variant is colocated, remote or remote_compose.
	Variant     string
	Title       string
	Description string
	Command     string
}

// CreatedEnrollment is a new enrollment with its token, shown once.
type CreatedEnrollment struct {
	Enrollment Enrollment
	Token      string
	ManagerURL string
	Install    []InstallCommand
}

// EnvironmentPatch is a partial environment edit; nil fields are unchanged.
type EnvironmentPatch struct {
	Name           *string
	ServiceAddress *string
}

// Credential rotation states.
const (
	RotationCompleted = "completed"
	RotationPending   = "pending"
)

// CredentialRotation is the result of rotating an agent credential.
type CredentialRotation struct {
	AgentID      string
	CredentialID string
	// State is completed (the agent persisted the new credential and the
	// old one is revoked) or pending (offline or unconfirmed; the old
	// credential stays valid until the agent confirms).
	State       string
	RequestedAt time.Time
	CompletedAt *time.Time
}

// EnvironmentSystem is an environment's last reported agent/Engine state.
type EnvironmentSystem struct {
	Environment Environment
	// Agent is the active agent (nil while detached).
	Agent *Agent
}

// InputError is an invalid input reported as 422 on Field (a JSON body
// member such as "name").
type InputError struct {
	Field   string
	Message string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Message }
