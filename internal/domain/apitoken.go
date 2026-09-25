package domain

import (
	"errors"
	"time"
)

// API tokens (#31): scoped, expiring bearer credentials of a user for
// scripts and integrations. A token carries an explicit list of allow
// grants chosen at creation; every request is evaluated as the token's
// scope intersected with the owning user's current effective permissions.

// APITokenStatus is derived from a token's timestamps.
type APITokenStatus string

// API token statuses.
const (
	APITokenActive  APITokenStatus = "active"
	APITokenExpired APITokenStatus = "expired"
	APITokenRevoked APITokenStatus = "revoked"
)

// APITokenRevocation says why a token was revoked.
type APITokenRevocation string

// Revocation reasons.
const (
	// RevokedByUser: the token's user revoked it.
	RevokedByUser APITokenRevocation = "user"
	// RevokedByOwner: the instance owner revoked it.
	RevokedByOwner APITokenRevocation = "owner"
	// RevokedUserDisabled: the account was disabled.
	RevokedUserDisabled APITokenRevocation = "user_disabled"
	// RevokedCredentialReset: revoked together with a password or factor
	// reset (an explicit choice of the reset).
	RevokedCredentialReset APITokenRevocation = "credential_reset" //nolint:gosec // G101: a reason name, not a credential
	// RevokedRestore: revoked after a disaster restore of the manager (#24).
	RevokedRestore APITokenRevocation = "restore"
)

// APIToken is a stored API token (never its secret: only a verifier is
// kept, in the store).
type APIToken struct {
	ID     string
	UserID string
	// Username is filled in by the owner's token list.
	Username  string
	Name      string
	CreatedAt time.Time
	// ExpiresAt is nil for a non-expiring token (only when the owner
	// allows them).
	ExpiresAt     *time.Time
	LastUsedAt    *time.Time
	LastUsedIP    string
	RevokedAt     *time.Time
	RevokedBy     string
	RevokedReason APITokenRevocation
	// Scopes are the token's allow grants (Effect is always allow).
	Scopes []PermissionRule
}

// Status at now.
func (t APIToken) Status(now time.Time) APITokenStatus {
	switch {
	case t.RevokedAt != nil:
		return APITokenRevoked
	case t.ExpiresAt != nil && !now.Before(*t.ExpiresAt):
		return APITokenExpired
	}
	return APITokenActive
}

// NewAPIToken is the input of token creation. Exactly one of ExpiresAt
// and NeverExpires is set.
type NewAPIToken struct {
	Name         string
	ExpiresAt    *time.Time
	NeverExpires bool
	Scopes       []PermissionRule
}

// APITokenCredential is what authenticating a token needs (store).
type APITokenCredential struct {
	TokenID   string
	UserID    string
	Verifier  string
	ExpiresAt *time.Time
	Revoked   bool
	// UserActive is false for disabled accounts.
	UserActive bool
	LastUsedAt *time.Time
	LastUsedIP string
}

// API token limits.
const (
	// DefaultAPITokenMaxDays is the default instance maximum lifetime.
	DefaultAPITokenMaxDays = 90
	// MaxAPITokenScopes bounds a token's scope list.
	MaxAPITokenScopes = 200
)

// API token errors.
var (
	ErrAPITokenNotFound = errors.New("api token not found")
	// ErrAPITokensDisabled: the owner disabled API tokens instance-wide.
	ErrAPITokensDisabled = errors.New("api tokens are disabled on this instance")
	// ErrAPITokenInvalid: a presented token is unknown, malformed, expired,
	// revoked, disabled instance-wide, or its account is disabled or does
	// not meet the sign-in policy. Callers never learn which.
	ErrAPITokenInvalid = errors.New("api token invalid")
)
