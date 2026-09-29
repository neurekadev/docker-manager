package domain

import (
	"errors"
	"time"
)

// UserSession is one signed-in browser session of a user: a device (#16).
// It never carries the session token; ID is a public identifier.
type UserSession struct {
	ID     string
	UserID string
	// Epoch is the user's session epoch the session was made in.
	Epoch int64
	// StaySignedIn selects the longer "Stay signed in" limits.
	StaySignedIn bool
	CreatedAt    time.Time
	// LastSeenAt and IP are of the latest request (written at most once a
	// minute).
	LastSeenAt time.Time
	IP         string
	// UserAgent is the browser's User-Agent header (at most 256 bytes).
	UserAgent string
	// ExpiresAt is the end of the session at the latest; IdleExpiresAt the
	// end if there is no further activity (filled in by the identity
	// service from the session limits).
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	// Current marks the caller's own session.
	Current bool
}

// ErrUserSessionNotFound reports that the user has no such session (or it
// ended).
var ErrUserSessionNotFound = errors.New("session not found")
