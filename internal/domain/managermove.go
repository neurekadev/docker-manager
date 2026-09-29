package domain

import (
	"errors"
	"strconv"
	"time"
)

// ManagerMoveState is the state of a move of the manager to a new server
// (docs/internal/architecture/manager-move.md).
type ManagerMoveState string

// Move states. open → draining → handed_off → confirmed on the old
// manager; cancelled and expired end a move that did not happen; arrived is
// the state of the move's row in the copy the new manager runs.
const (
	MoveOpen      ManagerMoveState = "open"
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
	return []ManagerMoveState{MoveOpen, MoveDraining, MoveHandedOff, MoveConfirmed}
}

// Active reports whether s is one of ActiveMoveStates.
func (s ManagerMoveState) Active() bool {
	switch s {
	case MoveOpen, MoveDraining, MoveHandedOff, MoveConfirmed:
		return true
	}
	return false
}

// ManagerMove is one move of the manager. The move code is never part of
// it: the database keeps only its verifier (and, on the new manager, the
// code sealed with the secret key until the old manager confirmed).
type ManagerMove struct {
	ID        string
	State     ManagerMoveState
	CreatedBy string
	CreatedAt time.Time
	// ExpiresAt ends an open or draining move (one hour after creation).
	ExpiresAt   time.Time
	DrainingAt  *time.Time
	HandedOffAt *time.Time
	// ConfirmedAt: old manager, when the new manager confirmed; new
	// manager (arrived), when the old manager accepted the confirmation.
	ConfirmedAt *time.Time
	// EndedAt is when a move was cancelled or expired.
	EndedAt *time.Time
	// HandoffAddress is the client IP of the handoff request.
	HandoffAddress string
	// New manager: the old manager's address, when this copy started as
	// the instance, and the confirmation attempts.
	SourceURL       string
	ArrivedAt       *time.Time
	ConfirmAttempts int
	// ConfirmError is the class of the last failed confirmation ("" none).
	ConfirmError  string
	LastConfirmAt *time.Time
	UpdatedAt     time.Time
}

// Move errors.
var (
	ErrManagerMoveNotFound = errors.New("no manager move")
	// ErrManagerMoveExists: another move is open or in progress.
	ErrManagerMoveExists = errors.New("another move of this manager is open or in progress")
	// ErrManagerMoveState: the move's state does not allow the operation.
	ErrManagerMoveState = errors.New("the move is not in a state that allows this")
	// ErrMoveCodeInvalid: the move code is unknown, wrong, expired,
	// cancelled or no longer usable (indistinguishable on purpose).
	ErrMoveCodeInvalid = errors.New("the move code is not valid")
	// ErrManagerMoveInProgress: a receive or an import is running or staged
	// on this (new) manager.
	ErrManagerMoveInProgress = errors.New("a move or an import is already running on this manager")
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
