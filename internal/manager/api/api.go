// Package api defines Docker Manager's public /api/v1 contract with Huma: the
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

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
)

// Contract constants.
const (
	// BasePath prefixes every public API route.
	BasePath = "/api/v1"
	// Version is the public API major version (also the OpenAPI info.version).
	Version = "v1"
	// OpenAPIPath serves the spec at OpenAPIPath + ".json" and ".yaml".
	OpenAPIPath = BasePath + "/openapi"
	title       = "Docker Manager API"
	description = "Public control API of the Docker Manager. All routes live under /api/v1 on the single public origin. " +
		"Errors use the Error schema with media type application/problem+json. " +
		"Every operation declares the capability it requires (x-docker-manager-capability) and the scope at which it is checked (x-docker-manager-scope). " +
		"Browsers authenticate with the session cookie, other clients with a bearer API token. " +
		"Conventions, errors, streams and versioning: docs/internal/api/README.md in the Docker Manager repository."

	// SessionCookieName is the browser session cookie (#16). The __Host-
	// prefix pins it to the single public origin: Secure, Path=/, no Domain.
	SessionCookieName = "__Host-docker_manager_session"
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
	// Stacks is the Compose stack service (#7); nil answers its routes
	// with 503.
	Stacks StackService
	// Events is the in-process event bus (stack event streams); nil
	// answers those streams with 503.
	Events *events.Bus
	// APITokens serves the API token routes (#31); nil answers them with
	// 503. The identity middleware authenticates bearer tokens.
	APITokens APITokenService
	// Observe serves metrics, capacity, the overview, the Engine inventory
	// of system information and the environment event stream (#5); nil
	// answers those routes with 503 (system information without inventory).
	Observe ObserveService
	// StreamMaxAge overrides DefaultStreamMaxAge (tests).
	StreamMaxAge time.Duration
	// Registries serves registry connections (#19); nil answers those
	// routes with 503.
	Registries RegistryService
	// Docker serves the containers, images, volumes and networks of each
	// environment (#6); nil answers those routes with 503.
	Docker DockerService
	// InstanceID is the manager instance ID (containers it created are
	// marked thisInstance).
	InstanceID string
	// Files is the scoped file manager (#15); nil answers the file routes
	// with 503. FilesMaxUpload bounds one upload (default DefaultMaxUpload,
	// DOCKER_MANAGER_FILES_MAX_UPLOAD).
	Files          FilesService
	FilesMaxUpload int64
	// GitCredentials serves Git credentials (#33); nil answers with 503.
	GitCredentials GitCredentialService
	// Builds serves image builds and build definitions (#33); nil
	// answers with 503.
	Builds BuildService
	// ContainerIO serves container logs and exec terminals (#8); nil
	// answers those routes with 503 (after authorization).
	ContainerIO ContainerIOService
	// Schedules is the shared cron scheduler (#13): schedule defaults,
	// previews and the cross-policy schedule view; nil answers those
	// routes with 503 (after authorization).
	Schedules ScheduleService
	// Maintenance serves prune policies, previews, runs and the suggested
	// default rules (#14); nil answers those routes with 503.
	Maintenance MaintenanceService
	// Migrations moves stacks and volumes between environments (#35); nil
	// answers those routes with 503 (after authorization).
	Migrations MigrationService
	// Updates serves update policies, checks, candidates, previews and
	// runs (#20); nil answers those routes with 503 (after
	// authentication).
	Updates UpdateService
	// Backups serves backup repositories, the Recovery Key, policies and
	// backups (#10); nil answers those routes with 503.
	Backups BackupService
	// Templates serves the instance's stack templates (template registry).
	Templates TemplateService
	// Removal previews environment removals (#34); nil answers the
	// preview with 503 (after authorization).
	Removal RemovalService
	// Diagnostics serves the internal metrics endpoint and the support
	// bundle (#34); nil answers them with 404 / 503.
	Diagnostics DiagnosticsService
	// Live is the live invalidation stream hub (#23); nil answers
	// GET /live/stream with 503.
	Live LiveHub
	// FileWatch keeps volumes with an open file view watched while a live
	// stream names them (#23); nil watches only stacks.
	FileWatch FileWatch
	// Settings serves the editable instance settings (GET/PATCH
	// /settings); nil answers them with 503 (after authorization).
	Settings SettingsService
	// Deployment is the read-only deployment configuration GET /settings
	// shows.
	Deployment DeploymentInfo
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

// Config returns the Huma configuration for the Docker Manager API.
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
			Type: "http", Scheme: "bearer", BearerFormat: "Docker Manager API token",
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
	registerRemoval(a, deps)
	registerDiagnostics(a, deps)
	registerPermissions(a, deps)
	registerStacks(a, deps)
	registerAPITokens(a, deps)
	registerObserve(a, deps)
	registerRegistries(a, deps)
	registerContainers(a, deps)
	registerContainerMetrics(a, deps)
	registerImages(a, deps)
	registerVolumes(a, deps)
	registerMigrations(a, deps)
	registerNetworks(a, deps)
	registerFiles(a, deps)
	registerGitCredentials(a, deps)
	registerBuilds(a, deps)
	registerContainerIO(a, deps)
	registerSchedules(a, deps)
	registerSettings(a, deps)
	registerMaintenance(a, deps)
	registerUpdates(a, deps)
	registerBackups(a, deps)
	registerTemplates(a, deps)
	registerLive(a, deps)
	registerSearch(a, deps)
	addExamples(a)
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
