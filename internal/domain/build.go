package domain

import (
	"errors"
	"strings"
	"time"
)

// Image builds (#33): manager-owned Git credentials, manual Git URL builds,
// saved build definitions and their build records.

// GitCredential is an HTTPS Git credential (username + token) for one host,
// administered by the owner and used by builds without being readable. The
// token is write-only (sealed at rest; only a keyed fingerprint is shown).
type GitCredential struct {
	ID   string
	Name string
	// Host is host[:port] (lower case) the credential is sent to.
	Host string
	// PathPrefix restricts the credential to repositories below a path
	// ("acme" or "acme/team"); empty matches every repository on Host.
	PathPrefix string
	Username   string
	// PlainHTTP allows sending the credential to http:// repositories
	// (self-hosted servers only; it then travels unencrypted).
	PlainHTTP         bool
	Status            RegistryConnectionStatus
	SecretFingerprint string
	SecretVersion     int
	SecretUpdatedAt   time.Time
	LastUsedAt        *time.Time
	LastCheckAt       *time.Time
	LastCheckResult   string
	RevokedAt         *time.Time
	Revision          int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Active reports whether the credential can authenticate.
func (c GitCredential) Active() bool { return c.Status == RegistryConnectionActive }

// GitCredentialInput creates a Git credential.
type GitCredentialInput struct {
	Name       string
	Host       string
	PathPrefix string
	Username   string
	Secret     string
	PlainHTTP  bool
}

// GitCredentialPatch edits a Git credential; a Secret rotates the token
// (and re-activates a revoked credential). Status may only become revoked.
type GitCredentialPatch struct {
	Name       *string
	PathPrefix *string
	Username   *string
	Secret     *string
	PlainHTTP  *bool
	Status     *RegistryConnectionStatus
}

// Git credential errors.
var (
	ErrGitCredentialNotFound  = errors.New("git credential not found")
	ErrGitCredentialNameTaken = errors.New("git credential name taken")
	// ErrGitCredentialRevoked: the selected credential has no token; there
	// is no anonymous fallback.
	ErrGitCredentialRevoked = errors.New("git credential is revoked")
	// ErrGitCredentialMismatch: an explicitly selected credential is not
	// for the repository's host or path.
	ErrGitCredentialMismatch = errors.New("git credential does not apply to this repository")
)

// AmbiguousGitCredentialError means several credentials match the
// repository equally well; the request must name one.
type AmbiguousGitCredentialError struct {
	Host          string
	CredentialIDs []string
}

func (e *AmbiguousGitCredentialError) Error() string {
	return "several Git credentials match " + e.Host + " equally (" + strings.Join(e.CredentialIDs, ", ") + "); select one explicitly"
}

// BuildSource describes what to build (manual builds and definitions).
type BuildSource struct {
	GitURL      string
	Ref         string
	ContextPath string
	Dockerfile  string
	Target      string
	// BuildArgs end up in the image history; they are shown to readers of
	// a definition but never recorded in the audit trail.
	BuildArgs map[string]string
	Tags      []string
	Platform  string
	NoCache   bool
	Pull      bool
	// GitCredentialID selects a Git credential explicitly (otherwise the
	// matching one, if any, is used).
	GitCredentialID string
	// RegistryConnectionIDs select registry connections for base images
	// explicitly (otherwise every host-wide connection that applies in the
	// environment is offered).
	RegistryConnectionIDs []string
	TimeoutSeconds        int
}

// BuildDefinition is a saved build that can be re-run on demand.
type BuildDefinition struct {
	ID            string
	EnvironmentID string
	Name          string
	Description   string
	Source        BuildSource
	LastBuildID   string
	Revision      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BuildDefinitionPatch edits a definition (nil = unchanged; Source replaces
// the whole source).
type BuildDefinitionPatch struct {
	Name        *string
	Description *string
	Source      *BuildSource
}

// ImageBuildStatus is the state of a build record (mirrors its job).
type ImageBuildStatus string

// Build statuses.
const (
	BuildQueued      ImageBuildStatus = "queued"
	BuildRunning     ImageBuildStatus = "running"
	BuildSucceeded   ImageBuildStatus = "succeeded"
	BuildFailed      ImageBuildStatus = "failed"
	BuildCancelled   ImageBuildStatus = "cancelled"
	BuildInterrupted ImageBuildStatus = "interrupted"
)

// Terminal reports whether the build finished.
func (s ImageBuildStatus) Terminal() bool {
	switch s {
	case BuildSucceeded, BuildFailed, BuildCancelled, BuildInterrupted:
		return true
	}
	return false
}

// ImageBuild is the record of one build: what was requested (without
// credentials or build argument values), the job, and the outcome.
type ImageBuild struct {
	ID            string
	EnvironmentID string
	JobID         string
	DefinitionID  string
	GitURL        string
	Ref           string
	ContextPath   string
	Dockerfile    string
	Target        string
	Tags          []string
	Platform      string
	NoCache       bool
	Pull          bool
	// BuildArgKeys are the names of the build arguments (values are not
	// recorded here).
	BuildArgKeys          []string
	GitCredentialID       string
	RegistryConnectionIDs []string
	Status                ImageBuildStatus
	ResolvedCommit        string
	ResolvedRef           string
	ImageID               string
	ErrorClass            string
	ErrorMessage          string
	InitiatorUserID       string
	CreatedAt             time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
}

// Build errors.
var (
	ErrImageBuildNotFound       = errors.New("image build not found")
	ErrBuildDefinitionNotFound  = errors.New("build definition not found")
	ErrBuildDefinitionNameTaken = errors.New("build definition name taken")
)
