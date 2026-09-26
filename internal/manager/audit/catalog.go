package audit

import (
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Action keys. Authorized operations record their #17 capability key
// (stack.deploy, container.restart, job.cancel, audit.export, ...);
// operations guarded by a pseudo-capability (public, authenticated, owner)
// record a key derived from their operation ID (create-invitation ->
// invitation.create) unless they declare Operation.AuditAction. The
// reserved keys below name lifecycle events that are not requests.
const (
	// ActionJobQueued: a job was created (manual, API token or scheduled).
	ActionJobQueued = "job.queued"
	// ActionJobStarted: an attempt of a job started running.
	ActionJobStarted = "job.started"
	// ActionJobCancelRequested: cancellation of a job was requested.
	ActionJobCancelRequested = "job.cancel_requested"
	// ActionJobFinished: a job reached a terminal state (details.state).
	ActionJobFinished = "job.finished"
	// ActionAuditPurge: the retention purge deleted old records.
	ActionAuditPurge = "audit.purge"
)

// Capabilities this workstream adds to the #17 catalog. Both are
// instance-scoped, owner-only by default and all-or-nothing: audit records
// reveal activity on resources the reader may not otherwise see, so they
// are flagged high-risk when delegated.
const (
	CapabilityRead   = "audit.read"
	CapabilityExport = "audit.export"
)

// CapabilityInfo describes a capability for the #17 permission catalog.
type CapabilityInfo struct {
	Key         string
	Label       string
	Description string
	// Scope is the only scope the capability can be granted at.
	Scope string
	// HighRisk marks capabilities the editor shows distinctly (#17).
	HighRisk bool
	// OwnerOnlyByDefault: no group holds it until the owner grants it.
	OwnerOnlyByDefault bool
}

// Capabilities returns the audit capabilities for the #17 catalog.
func Capabilities() []CapabilityInfo {
	return []CapabilityInfo{
		{Key: CapabilityRead, Label: "View audit log", Scope: "instance", HighRisk: true, OwnerOnlyByDefault: true,
			Description: "Read every audit record. All-or-nothing: records reveal activity on resources the reader cannot otherwise see."},
		{Key: CapabilityExport, Label: "Export audit log", Scope: "instance", HighRisk: true, OwnerOnlyByDefault: true,
			Description: "Download audit records as NDJSON or CSV. All-or-nothing, like View audit log."},
	}
}

// ActionForOperation derives the action key of an operation guarded by a
// pseudo-capability from its operation ID: the leading verb becomes the
// last segment (create-invitation -> invitation.create,
// delete-my-passkey -> my_passkey.delete).
func ActionForOperation(operationID string) string {
	verb, rest, ok := strings.Cut(operationID, "-")
	if !ok || rest == "" {
		return "api." + strings.ReplaceAll(operationID, "-", "_")
	}
	return strings.ReplaceAll(rest, "-", "_") + "." + verb
}

// CategoryFor classifies an action into the #30 event categories, using
// the words of the action key and the operation ID.
func CategoryFor(action, operationID string) domain.AuditCategory {
	switch {
	case strings.HasPrefix(action, "audit."):
		return domain.AuditSystem
	case strings.HasPrefix(action, "job."):
		return domain.AuditOperations
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(action+" "+operationID, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	}) {
		words[w] = true
	}
	hasAny := func(ws ...string) bool {
		for _, w := range ws {
			if words[w] {
				return true
			}
		}
		return false
	}
	switch {
	case hasAny("group", "groups", "permission", "permissions"):
		return domain.AuditAuthorization
	case hasAny("token", "tokens", "agent", "agents", "enrollment", "enrollments", "enroll", "registry", "registries",
		"credential", "credentials", "rotation", "rotations", "confirmation", "confirmations"):
		return domain.AuditCredentials
	case hasAny("exec", "backup", "backups", "stack", "stacks", "container", "containers", "volume", "volumes"):
		return domain.AuditOperations
	case hasAny("auth", "setup", "owner", "ownership", "session", "sessions", "password", "passwords", "totp", "passkey",
		"passkeys", "recovery", "invitation", "invitations", "user", "users", "factor", "factors", "step", "me", "my", "sign"):
		return domain.AuditIdentity
	}
	return domain.AuditOperations
}
