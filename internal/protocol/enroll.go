package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Enrollment and credential wire formats (docs/protocol/agent-v1.md,
// "Enrollment"). Shared by the manager (internal/manager/agents) and the
// agent (internal/agent/enroll); the manager-side helpers that mint and
// verify these secrets live in internal/manager/authsep.

// Secret prefixes. An agent credential is "dya_<credentialId>_<secret>", an
// enrollment token "dye_<enrollmentId>_<secret>" (secret: 32 random bytes,
// base64url). The prefixes let the manager refuse them on /api/v1 before
// any handler runs (#27).
const (
	CredentialPrefix      = "dya_"
	EnrollmentTokenPrefix = "dye_"
)

// Agent route paths below the manager origin.
const (
	EnrollPath  = "/agent/v1/enroll"
	SessionPath = "/agent/v1/session"
)

// MaxEnrollBody bounds the enrollment request body (bytes).
const MaxEnrollBody = 64 << 10

// Bounds of agent-reported names.
const (
	MaxHostname        = 255
	MaxEnvironmentName = 63
)

// EnrollRequest is the body of POST /agent/v1/enroll.
type EnrollRequest struct {
	// Protocol must equal Version.
	Protocol     string `json:"protocol"`
	AgentVersion string `json:"agentVersion"`
	// InstallID is generated once per agent state volume; together with
	// Engine.ID it identifies the installation (Engine IDs can collide on
	// cloned VMs).
	InstallID string     `json:"installId"`
	Engine    EngineInfo `json:"engine"`
	// Hostname is the Engine's host name (docker info Name).
	Hostname string `json:"hostname,omitempty"`
	// EnvironmentName is DOCKER_AGENT_ENVIRONMENT_NAME, the proposed display
	// name (a name preset on the enrollment takes precedence).
	EnvironmentName string `json:"environmentName,omitempty"`
}

// Validate checks an enrollment request (the protocol version is checked
// separately so it can be answered with 426).
func (r EnrollRequest) Validate() error {
	switch {
	case !versionRE.MatchString(r.AgentVersion):
		return invalid("agentVersion is malformed")
	case !idRE.MatchString(r.InstallID):
		return invalid("installId must match %s", idRE)
	case !idRE.MatchString(r.Engine.ID):
		return invalid("engine.id must match %s", idRE)
	case !versionRE.MatchString(r.Engine.Version) || !versionRE.MatchString(r.Engine.APIVersion):
		return invalid("engine.version and engine.apiVersion are required")
	case r.Engine.MinAPIVersion != "" && !versionRE.MatchString(r.Engine.MinAPIVersion):
		return invalid("engine.minApiVersion is malformed")
	case len(r.Engine.OS) > 32 || len(r.Engine.Arch) > 32:
		return invalid("engine.os and engine.arch are too long")
	case !cleanText(r.Hostname, MaxHostname):
		return invalid("hostname must be at most %d characters without control characters", MaxHostname)
	case !cleanText(r.EnvironmentName, MaxEnvironmentName):
		return invalid("environmentName must be at most %d characters without control characters", MaxEnvironmentName)
	}
	return nil
}

// cleanText reports whether s is valid UTF-8 of at most n bytes without
// control characters.
func cleanText(s string, n int) bool {
	if len(s) > n || !utf8.ValidString(s) {
		return false
	}
	return strings.IndexFunc(s, unicode.IsControl) < 0
}

// DecodeEnrollRequest strictly decodes an enrollment body: unknown fields
// and trailing data are rejected.
func DecodeEnrollRequest(b []byte) (EnrollRequest, error) {
	var r EnrollRequest
	if len(b) > MaxEnrollBody {
		return r, invalid("enrollment body exceeds %d bytes", MaxEnrollBody)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("%w: enrollment body: %v", ErrInvalidFrame, err)
	}
	if rest := b[dec.InputOffset():]; len(bytes.TrimSpace(rest)) != 0 {
		return r, invalid("trailing data after the enrollment body")
	}
	return r, nil
}

// EnrollResponse is the 201 body of POST /agent/v1/enroll.
type EnrollResponse struct {
	AgentID         string `json:"agentId"`
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	// Credential is the agent's bearer credential; shown only here.
	Credential  string `json:"credential"`
	SessionPath string `json:"sessionPath"`
	// Reattached is true when an archived environment was re-attached.
	Reattached bool `json:"reattached"`
}

// Validate checks an enrollment response (agent side).
func (r EnrollResponse) Validate() error {
	switch {
	case !idRE.MatchString(r.AgentID) || !idRE.MatchString(r.EnvironmentID):
		return invalid("enrollment response IDs are malformed")
	case !strings.HasPrefix(r.Credential, CredentialPrefix) || len(r.Credential) > 256:
		return invalid("enrollment response carries no agent credential")
	case r.SessionPath != SessionPath:
		return invalid("enrollment response names session path %q", r.SessionPath)
	}
	return nil
}

// CredentialRotateInput is the input of the agent.credential.rotate
// request: the new credential, which the agent persists atomically before
// answering.
type CredentialRotateInput struct {
	Credential string `json:"credential"`
}

// CredentialRotateOutput is the output of agent.credential.rotate.
type CredentialRotateOutput struct {
	Persisted bool `json:"persisted"`
}
