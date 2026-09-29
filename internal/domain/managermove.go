package domain

import (
	"errors"
	"strconv"
	"time"
)

// ManagerMoveState is the state of a move of the manager to a new server
// (docs/internal/architecture/manager-move.md).
type ManagerMoveState string

// Move states. Old manager: open (waiting for the new server) → moving
// (manager.move moves the apps) → ready (the handoff is allowed) →
// draining → handed_off → confirmed; a failed manager.move goes back to
// open; cancelled and expired end a move that did not happen. arrived is
// the state of the move's row in the copy the new manager runs.
const (
	MoveOpen      ManagerMoveState = "open"
	MoveMoving    ManagerMoveState = "moving"
	MoveReady     ManagerMoveState = "ready"
	MoveDraining  ManagerMoveState = "draining"
	MoveHandedOff ManagerMoveState = "handed_off"
	MoveConfirmed ManagerMoveState = "confirmed"
	MoveCancelled ManagerMoveState = "cancelled"
	MoveExpired   ManagerMoveState = "expired"
	MoveArrived   ManagerMoveState = "arrived"
)

// ActiveMoveStates are the states of the one current move of an old
// manager (at most one move is in them at a time).
func ActiveMoveStates() []ManagerMoveState {
	return []ManagerMoveState{MoveOpen, MoveMoving, MoveReady, MoveDraining, MoveHandedOff, MoveConfirmed}
}

// Active reports whether s is one of ActiveMoveStates.
func (s ManagerMoveState) Active() bool {
	switch s {
	case MoveOpen, MoveMoving, MoveReady, MoveDraining, MoveHandedOff, MoveConfirmed:
		return true
	}
	return false
}

// Redirect roles: which server's agent a manager.redirect was sent to.
const (
	// RedirectNewServer: the new server's agent (enrolled with the move's
	// token), told the new manager's address on its own server.
	RedirectNewServer = "new_server"
	// RedirectOldServer: the agent next to the old manager, told the new
	// server's address.
	RedirectOldServer = "old_server"
)

// ManagerMoveRedirect is one manager.redirect the old manager sent just
// before it handed off (best effort).
type ManagerMoveRedirect struct {
	EnvironmentID   string
	EnvironmentName string
	// Role is RedirectNewServer or RedirectOldServer.
	Role string
	// URL is the address the agent was told to dial.
	URL string
	// Sent: the agent accepted the redirect.
	Sent bool
	// ErrorClass says why it was not sent (offline, unsupported, refused,
	// timeout); "" when sent.
	ErrorClass string
}

// ManagerMove is one move of the manager. The move code is never part of
// it: the database keeps it only sealed with the secret key (old manager:
// to authenticate the new one and encrypt the handoff; new manager: until
// the old one confirmed).
type ManagerMove struct {
	ID        string
	State     ManagerMoveState
	CreatedBy string
	CreatedAt time.Time
	// ExpiresAt ends a move that was not handed off (seven days after
	// creation).
	ExpiresAt time.Time
	// ThisServerAddress and NewServerAddress are host:port of the old and
	// the new server as the owner entered them (port 8080 by default).
	ThisServerAddress string
	NewServerAddress  string
	// EnrollmentID is the enrollment token of the new server's agent.
	EnrollmentID string
	// SourceEnvironmentID is the environment next to the old manager
	// (whose apps move); "" when none is known.
	SourceEnvironmentID string
	// TargetEnvironmentID is the new server's environment, once its agent
	// enrolled.
	TargetEnvironmentID string
	// CheckedInAt is the last time the waiting manager asked for the
	// handoff.
	CheckedInAt *time.Time
	// MoveJobID is the latest manager.move job; MigrationID the
	// environment migration it ran.
	MoveJobID   string
	MigrationID string
	ReadyAt     *time.Time
	DrainingAt  *time.Time
	HandedOffAt *time.Time
	// ConfirmedAt: old manager, when the new manager confirmed; new
	// manager (arrived), when the old manager accepted the confirmation.
	ConfirmedAt *time.Time
	// EndedAt is when a move was cancelled or expired.
	EndedAt *time.Time
	// HandoffAddress is the client IP of the waiting manager's requests.
	HandoffAddress string
	// Redirects are the manager.redirect requests sent before the handoff.
	Redirects []ManagerMoveRedirect
	// New manager: the old manager's address, when this copy started as
	// the instance, and the confirmation attempts.
	SourceURL       string
	ArrivedAt       *time.Time
	ConfirmAttempts int
	// ConfirmError is the class of the last failed confirmation ("" none).
	ConfirmError  string
	LastConfirmAt *time.Time
	// ConfirmAcknowledgedAt is when the owner stated that the old manager
	// no longer runs the instance although it never confirmed (arrived).
	ConfirmAcknowledgedAt *time.Time
	UpdatedAt             time.Time
}

// Move errors.
var (
	ErrManagerMoveNotFound = errors.New("no manager move")
	// ErrManagerMoveExists: another move is open or in progress.
	ErrManagerMoveExists = errors.New("another move of this manager is open or in progress")
	// ErrManagerMoveState: the move's state does not allow the operation.
	ErrManagerMoveState = errors.New("the move is not in a state that allows this")
	// ErrMoveCodeInvalid: the move's authentication is unknown, wrong,
	// replayed, expired, cancelled or no longer usable (indistinguishable
	// on purpose).
	ErrMoveCodeInvalid = errors.New("the move code is not valid")
	// ErrMoveClockSkew: a correctly signed move request whose time is more
	// than five minutes off this manager's clock.
	ErrMoveClockSkew = errors.New("the clocks of the two servers differ by more than five minutes")
	// ErrManagerMoveNewServerMissing: the apps cannot move yet: the new
	// server's agent is not connected or its waiting manager has not
	// checked in.
	ErrManagerMoveNewServerMissing = errors.New("the new server is not ready: its agent must be connected and its Docker Manager must have checked in")
	// ErrManagerMoved: the manager is moving (or moved) to a new server;
	// it is read-only and starts no job.
	ErrManagerMoved = errors.New("the manager is moving to a new server; it is read-only and starts no job")
)

// JobsRunningError answers a handoff while jobs still run on the old
// manager (the caller retries after RetryAfter).
type JobsRunningError struct {
	Count      int
	RetryAfter time.Duration
}

func (e *JobsRunningError) Error() string {
	return strconv.Itoa(e.Count) + " jobs are still running on the old manager"
}

// MoveNotReadyError answers a handoff before the apps moved: the move is
// open (waiting for Move everything) or moving (StacksMoved of
// StacksTotal, CurrentStack moving now). The caller asks again after
// RetryAfter.
type MoveNotReadyError struct {
	State        ManagerMoveState
	StacksMoved  int
	StacksTotal  int
	CurrentStack string
	RetryAfter   time.Duration
}

func (e *MoveNotReadyError) Error() string {
	return "the old manager is not ready to hand over (" + string(e.State) + ")"
}
