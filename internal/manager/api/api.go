// Package api defines DockYard's public /api/v1 contract with Huma: the
// shared error shape, the operation registration helper with mandatory
// capability/scope metadata, pagination types and the operations themselves.
//
// Types in this package are transport DTOs. Convert to and from domain types
// (internal/domain) in handlers; never expose database models or Docker SDK
// types directly.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// Contract constants.
const (
	// BasePath prefixes every public API route.
	BasePath = "/api/v1"
	// Version is the public API major version (also the OpenAPI info.version).
	Version = "v1"
	// OpenAPIPath serves the spec at OpenAPIPath + ".json" and ".yaml".
	OpenAPIPath = BasePath + "/openapi"
	title       = "DockYard API"
	description = "Public control API of the DockYard manager. All routes live under /api/v1 on the single public origin. " +
		"Errors use the Error schema with media type application/problem+json. " +
		"Every operation declares the capability it requires (x-dockyard-capability) and the scope at which it is checked (x-dockyard-scope). " +
		"Browsers authenticate with the session cookie, other clients with a bearer API token. " +
		"Conventions, errors, streams and versioning: docs/api/README.md in the DockYard repository."

	// SessionCookieName is the browser session cookie (#16). The __Host-
	// prefix pins it to the single public origin: Secure, Path=/, no Domain.
	SessionCookieName = "__Host-dockyard_session"
)

// Deps are the collaborators operations need. Zero values are valid for spec
// generation (handlers are never called then).
type Deps struct {
	Build buildinfo.Info
	// Readiness reports readiness checks; nil means always ready.
	Readiness func(ctx context.Context) []Check
	// Features are stable feature-flag keys advertised by /capabilities.
	Features []string
	// Jobs is the job engine (#26); nil answers job routes with 503.
	Jobs JobService
	// Authorizer decides capability checks (the #17 permission service);
	// nil denies everything.
	Authorizer authz.Authorizer
	// Clock drives stream heartbeats; nil means the wall clock.
	Clock clock.Clock
	// SSEHeartbeat overrides DefaultSSEHeartbeat.
	SSEHeartbeat time.Duration
	// Idempotency stores responses of IdempotencyStored operations; nil
	// answers keyed requests to those operations with 503.
	Idempotency IdempotencyStore
	// Audit is the audit trail (#30): every audited operation records into
	// it, and the audit routes read it. The manager always sets it; nil
	// (spec generation, focused tests) records nothing and answers the
	// audit routes with 503.
	Audit AuditService
	// Identity serves setup, sign-in, factors, invitations, users and the
	// sign-in policy (#16); nil answers those routes with 503.
	Identity IdentityService
	// Agents is the agent/environment service (#3); nil answers its
	// routes with 503.
	Agents AgentService
	// Permissions serves the catalog, groups, rule documents, effective
	// permissions and previews (#17); nil answers those routes with 503.
	// The manager also sets Authorizer to it.
	Permissions PermissionService
	// APITokens serves the API token routes (#31); nil answers them with
	// 503. The identity middleware authenticates bearer tokens.
	APITokens APITokenService
}

func (d Deps) clock() clock.Clock {
	if d.Clock == nil {
		return clock.Real()
	}
	return d.Clock
}

// Check is one readiness check result.
type Check struct {
	Name    string
	OK      bool
	Message string
}

// Config returns the Huma configuration for the DockYard API.
func Config() huma.Config {
	cfg := huma.DefaultConfig(title, Version)
	cfg.Info.Description = description
	cfg.OpenAPIPath = OpenAPIPath
	cfg.DocsPath = ""    // interactive docs load third-party scripts; not served.
	cfg.SchemasPath = "" // no $schema links in responses.
	cfg.CreateHooks = nil
	cfg.Transformers = []huma.Transformer{errorTransformer}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SecurityCookie: {
			Type: "apiKey", In: "cookie", Name: SessionCookieName,
			Description: "Browser session (#16). Set by POST /api/v1/auth/session as an HttpOnly, Secure, SameSite=Strict cookie. " +
				"Unsafe methods authenticated by this cookie must come from the manager's own origin (Origin/Sec-Fetch-Site checks).",
		},
		SecurityBearer: { //nolint:gosec // G101: a security scheme description, not a credential
			Type: "http", Scheme: "bearer", BearerFormat: "DockYard API token",
			Description: "Scoped, expiring API token (#31) sent as Authorization: Bearer <token>. " +
				"A token carries a subset of its owner's capabilities; each request is evaluated as token scope intersected with the owner's current effective permissions.",
		},
	}
	return cfg
}

// New creates the Huma API on mux and registers every operation.
func New(mux *http.ServeMux, deps Deps) huma.API {
	a := humago.New(mux, Config())
	a.UseMiddleware(withDeps(deps))
	registerSystem(a, deps)
	registerJobs(a, deps)
	registerAudit(a, deps)
	registerIdentity(a, deps)
	registerAgents(a, deps)
	registerEnvironments(a, deps)
	registerPermissions(a, deps)
	registerAPITokens(a, deps)
	return a
}

// SpecJSON returns the OpenAPI 3.1 document as indented JSON with a trailing
// newline. It is the source of api/openapi.json.
func SpecJSON() ([]byte, error) {
	a := New(http.NewServeMux(), Deps{})
	raw, err := json.Marshal(a.OpenAPI())
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil { // Encode appends the trailing newline
		return nil, err
	}
	return buf.Bytes(), nil
}

// SpecYAML returns the OpenAPI 3.1 document as YAML.
func SpecYAML() ([]byte, error) {
	return New(http.NewServeMux(), Deps{}).OpenAPI().YAML()
}
