package domain

import (
	"errors"
	"strings"
	"time"
)

// Registry connections (#19): manager-owned registry credentials shared by
// the instance. The owner administers them; authorized pull, deploy, build
// and update jobs use a matching connection without ever reading its
// secret. The secret is write-only: it is sealed at rest and only a keyed
// fingerprint is shown.

// RegistryCredentialType says what the secret of a connection is. Both are
// sent to registries as username + secret; the type only guides the UI
// (least-privilege pull tokens are recommended).
type RegistryCredentialType string

// Credential types.
const (
	RegistryCredentialPassword RegistryCredentialType = "password"
	RegistryCredentialToken    RegistryCredentialType = "token"
)

// Valid reports whether t is a known credential type.
func (t RegistryCredentialType) Valid() bool {
	return t == RegistryCredentialPassword || t == RegistryCredentialToken
}

// RegistryConnectionStatus is the lifecycle state of a connection.
type RegistryConnectionStatus string

// Connection statuses. A revoked connection has no secret; it still
// matches its images (so jobs fail visibly instead of pulling anonymously)
// until the owner rotates a new credential into it or deletes it.
const (
	RegistryConnectionActive  RegistryConnectionStatus = "active"
	RegistryConnectionRevoked RegistryConnectionStatus = "revoked"
)

// RegistryCheckOK is the LastCheckResult of a successful test or use;
// other values are registry error classes.
const RegistryCheckOK = "ok"

// RegistryConnection is one stored registry credential with its matching
// rules.
type RegistryConnection struct {
	ID   string
	Name string
	// Host is the normalized registry host (docker.io for every Docker Hub
	// alias, host:port for self-hosted registries).
	Host           string
	CredentialType RegistryCredentialType
	Username       string
	// RepositoryPattern restricts the connection to one repository
	// ("org/app") or a namespace ("org/*"); empty matches every repository
	// on Host.
	RepositoryPattern string
	// EnvironmentID and StackID bind the connection to one environment or
	// one stack (optional; a bound connection is preferred there and not
	// used elsewhere).
	EnvironmentID string
	StackID       string
	// Priority breaks ties between equally specific connections (higher
	// wins); an exact tie is ambiguous and needs explicit selection.
	Priority int
	// PlainHTTP allows plain HTTP for manager-side registry checks of a
	// self-hosted registry (credentials then travel unencrypted).
	PlainHTTP bool
	Status    RegistryConnectionStatus
	// SecretFingerprint is a keyed, non-reversible fingerprint of the
	// current secret ("" when revoked); SecretVersion increases with every
	// rotation.
	SecretFingerprint string
	SecretVersion     int
	SecretUpdatedAt   time.Time
	// LastUsedAt is the last successful use (check, pull, build).
	LastUsedAt *time.Time
	// LastCheckAt / LastCheckResult report the last connection test or
	// use: RegistryCheckOK or an error class.
	LastCheckAt     *time.Time
	LastCheckResult string
	RevokedAt       *time.Time
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Active reports whether the connection can authenticate.
func (c RegistryConnection) Active() bool { return c.Status == RegistryConnectionActive }

// RegistryConnectionInput creates a connection.
type RegistryConnectionInput struct {
	Name              string
	Host              string
	CredentialType    RegistryCredentialType
	Username          string
	Secret            string
	RepositoryPattern string
	EnvironmentID     string
	StackID           string
	Priority          int
	PlainHTTP         bool
}

// RegistryConnectionPatch edits a connection's metadata. Nil fields are
// unchanged. Status may only move to revoked (rotation re-activates).
type RegistryConnectionPatch struct {
	Name              *string
	RepositoryPattern *string
	EnvironmentID     *string
	StackID           *string
	Priority          *int
	PlainHTTP         *bool
	Status            *RegistryConnectionStatus
}

// RegistryCredentialRotation replaces a connection's credential.
type RegistryCredentialRotation struct {
	// Username and CredentialType replace the current values when set.
	Username       *string
	CredentialType *RegistryCredentialType
	Secret         string
}

// RegistrySelectRequest asks which connection an image reference uses.
type RegistrySelectRequest struct {
	Reference     string
	EnvironmentID string
	StackID       string
	// ConnectionID selects a connection explicitly (it must be a candidate
	// for the reference).
	ConnectionID string
}

// RegistryCandidate is a connection that could authenticate a reference.
type RegistryCandidate struct {
	Connection RegistryConnection
	// Binding is 2 for a stack binding, 1 for an environment binding, 0
	// for an unbound connection; Pattern is the repository matcher's
	// specificity (imageref.Pattern.Match).
	Binding int
	Pattern int
}

// RegistrySelection is the outcome of matching a reference.
type RegistrySelection struct {
	// Reference is the normalized reference; Host and Repository its parts.
	Reference  string
	Host       string
	Repository string
	// Selected is the connection used; nil means anonymous access (no
	// connection matches).
	Selected *RegistryConnection
	// Explicit reports that the caller chose Selected.
	Explicit bool
	// Candidates are every matching connection, best first.
	Candidates []RegistryCandidate
}

// Anonymous reports whether no connection applies.
func (s RegistrySelection) Anonymous() bool { return s.Selected == nil }

// Registry connection errors.
var (
	ErrRegistryConnectionNotFound  = errors.New("registry connection not found")
	ErrRegistryConnectionNameTaken = errors.New("registry connection name taken")
	// ErrRegistryConnectionRevoked: the selected connection has no
	// credential; there is no anonymous fallback.
	ErrRegistryConnectionRevoked = errors.New("registry connection is revoked")
	// ErrRegistryConnectionMismatch: an explicitly selected connection is
	// not a candidate for the reference (other host, repository matcher or
	// binding).
	ErrRegistryConnectionMismatch = errors.New("registry connection does not apply to this image")
)

// AmbiguousRegistryError means several connections match equally well; the
// request must select one explicitly.
type AmbiguousRegistryError struct {
	Host         string
	CandidateIDs []string
}

func (e *AmbiguousRegistryError) Error() string {
	return "several registry connections match " + e.Host + " equally (" + strings.Join(e.CandidateIDs, ", ") +
		"); select one explicitly"
}
