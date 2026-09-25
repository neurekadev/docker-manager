package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Environment is one enrolled agent plus the Docker Engine it controls
// (#3). The UI and API say environment; "host" in prose means the same.
type Environment struct {
	ID                     string     `json:"id" example:"0190a6e0-3333-7000-8000-000000000003"`
	Name                   string     `json:"name" example:"NAS" doc:"Editable server/display name."`
	ServiceAddress         string     `json:"serviceAddress,omitempty" example:"nas.lan" doc:"Host name or IP address users browse to, for links to published ports."`
	Status                 string     `json:"status" enum:"active,archived" doc:"Archived environments are hidden from operations; their records are kept."`
	Online                 bool       `json:"online" doc:"The agent's session is established and its jobs were reconciled."`
	ConnectionChangedAt    *time.Time `json:"connectionChangedAt,omitempty" doc:"When the environment last went online or offline."`
	LastSeenAt             *time.Time `json:"lastSeenAt,omitempty"`
	AgentID                string     `json:"agentId,omitempty" doc:"The active agent; absent while the environment is detached (its agent was removed)."`
	EngineID               string     `json:"engineId" doc:"Docker Engine ID."`
	AllowDuplicateEngineID bool       `json:"allowDuplicateEngineId" doc:"The owner declared this a distinct host sharing another environment's Engine ID (cloned VM)."`
	Revision               int64      `json:"revision" doc:"Edit revision (the ETag)."`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	ArchivedAt             *time.Time `json:"archivedAt,omitempty"`
}

func newEnvironment(e domain.Environment) Environment {
	return Environment{ID: e.ID, Name: e.Name, ServiceAddress: e.ServiceAddress, Status: string(e.Status), Online: e.Online,
		ConnectionChangedAt: e.ConnectionChangedAt, LastSeenAt: e.LastSeenAt, AgentID: e.AgentID, EngineID: e.EngineID,
		AllowDuplicateEngineID: e.AllowDuplicateEngineID, Revision: e.Revision, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		ArchivedAt: e.ArchivedAt}
}

// SystemEngine is the controlled Docker Engine as reported by the agent.
type SystemEngine struct {
	ID            string `json:"id"`
	Version       string `json:"version" example:"28.5.2"`
	APIVersion    string `json:"apiVersion" example:"1.51" doc:"API version negotiated by the agent's Moby client."`
	MinAPIVersion string `json:"minApiVersion,omitempty"`
	OS            string `json:"os" example:"linux"`
	Arch          string `json:"arch" example:"amd64"`
	Rootless      bool   `json:"rootless"`
}

// SystemAgent is the environment's active agent as reported by it.
type SystemAgent struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	VersionStatus string   `json:"versionStatus" enum:"current,outdated"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Protocols     []string `json:"protocols"`
	Connected     bool     `json:"connected"`
}

// SystemRoot is a verified file root served by the agent (#28); host paths
// are not exposed.
type SystemRoot struct {
	Kind  string `json:"kind" enum:"stacks,volumes,bind"`
	Watch string `json:"watch" enum:"inotify,poll,none"`
}

// SystemDiagnostic explains what the agent cannot do and why.
type SystemDiagnostic struct {
	Area    string `json:"area" enum:"engine,storage"`
	Code    string `json:"code" example:"storage_path_mismatch"`
	Message string `json:"message"`
}

// EnvironmentSystem is the last reported agent/Engine state.
type EnvironmentSystem struct {
	EnvironmentID string             `json:"environmentId"`
	Online        bool               `json:"online"`
	Agent         *SystemAgent       `json:"agent,omitempty" doc:"Absent while detached."`
	Engine        *SystemEngine      `json:"engine,omitempty" doc:"Absent before the agent's first session."`
	Transport     *AgentTransport    `json:"transport,omitempty"`
	Features      []string           `json:"features"`
	Commands      []string           `json:"commands" doc:"Job kinds this agent executes."`
	Requests      []string           `json:"requests"`
	Streams       []string           `json:"streams"`
	Roots         []SystemRoot       `json:"roots"`
	Diagnostics   []SystemDiagnostic `json:"diagnostics"`
	ReportedAt    *time.Time         `json:"reportedAt,omitempty" doc:"When the agent last reported its capabilities."`
}

// capabilitiesOf decodes an agent's stored capabilities (nil when none).
func capabilitiesOf(a domain.Agent) *protocol.CapabilitiesPayload {
	if a.Capabilities == "" {
		return nil
	}
	var c protocol.CapabilitiesPayload
	if json.Unmarshal([]byte(a.Capabilities), &c) != nil {
		return nil
	}
	return &c
}

func newSystem(s domain.EnvironmentSystem) EnvironmentSystem {
	out := EnvironmentSystem{EnvironmentID: s.Environment.ID, Online: s.Environment.Online, Features: []string{}, Commands: []string{},
		Requests: []string{}, Streams: []string{}, Roots: []SystemRoot{}, Diagnostics: []SystemDiagnostic{}}
	a := s.Agent
	if a == nil {
		return out
	}
	out.Agent = &SystemAgent{ID: a.ID, Version: a.Version, VersionStatus: a.VersionStatus, Protocols: []string{},
		Connected: a.SessionID != "" && a.Status == domain.AgentActive}
	c := capabilitiesOf(*a)
	if c == nil {
		return out
	}
	out.ReportedAt = a.CapabilitiesAt
	out.Agent.OS, out.Agent.Arch = c.OS, c.Arch
	out.Agent.Protocols = append(out.Agent.Protocols, c.Protocols...)
	out.Engine = &SystemEngine{ID: c.Engine.ID, Version: c.Engine.Version, APIVersion: c.Engine.APIVersion,
		MinAPIVersion: c.Engine.MinAPIVersion, OS: c.Engine.OS, Arch: c.Engine.Arch, Rootless: c.Engine.Rootless}
	out.Transport = &AgentTransport{ManagerURL: c.Transport.ManagerURL, PlainHTTP: c.Transport.PlainHTTP, CustomCA: c.Transport.CustomCA}
	out.Features = append(out.Features, c.Features...)
	out.Commands = append(out.Commands, c.Commands...)
	out.Requests = append(out.Requests, c.Requests...)
	out.Streams = append(out.Streams, c.Streams...)
	for _, r := range c.Roots {
		out.Roots = append(out.Roots, SystemRoot{Kind: r.Kind, Watch: r.Watch})
	}
	for _, d := range c.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, SystemDiagnostic{Area: d.Area, Code: d.Code, Message: d.Message})
	}
	return out
}

type listEnvironmentsInput struct {
	PageParams
	Status []string `query:"status" enum:"active,archived" doc:"Only environments in these states (default: active)."`
}

type environmentIDInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
}

type updateEnvironmentInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IfMatchParam
	Body struct {
		Name           *string `json:"name,omitempty" minLength:"1" maxLength:"63" example:"NAS" doc:"Server/display name."`
		ServiceAddress *string `json:"serviceAddress,omitempty" maxLength:"253" example:"nas.lan" doc:"Host name or IP address users browse to; empty clears it."`
	}
}

type deleteEnvironmentInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IfMatchParam
}

type listEnvironmentAgentsInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
}

type environmentOutput struct {
	ETagHeader
	Body Environment
}

type listEnvironmentsOutput struct{ Body Page[Environment] }
type environmentSystemOutput struct{ Body EnvironmentSystem }

func environmentResource(e domain.Environment) authz.Resource {
	return authz.Resource{Type: resourceTypeEnvironment, ID: e.ID, EnvironmentID: e.ID}
}

// visibleEnvironment loads an environment the caller may read (404 otherwise).
func (h *agentsAPI) visibleEnvironment(ctx context.Context, id string) (authz.Principal, domain.Environment, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return p, domain.Environment{}, err
	}
	env, err := h.svc.GetEnvironment(ctx, id)
	if err != nil {
		return p, domain.Environment{}, agentErr(err)
	}
	if !h.can(ctx, p, CapEnvironmentRead, environmentResource(env)) {
		return p, domain.Environment{}, NotFound("environment not found")
	}
	return p, env, nil
}

// stale answers a lost compare-and-swap: 412 with the current ETag.
func stale(current int64) error {
	return PreconditionFailed("the resource was changed since you loaded it",
		Field("header.If-Match", "stale revision; reload the resource, reapply your change and retry with its current ETag")).
		WithHeader("ETag", RevisionETag(current))
}

func (h *agentsAPI) listEnvironments(ctx context.Context, in *listEnvironmentsInput) (*listEnvironmentsOutput, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return nil, err
	}
	f := domain.EnvironmentFilter{}
	for _, s := range in.Status {
		f.Statuses = append(f.Statuses, domain.EnvironmentStatus(s))
	}
	if len(f.Statuses) == 0 {
		f.Statuses = []domain.EnvironmentStatus{domain.EnvironmentActive}
	}
	fingerprint := QueryFingerprint(strings.Join(in.Status, ","))
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Environment]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.Environment, error) {
			f.AfterID, f.Limit = afterID, n
			return h.svc.ListEnvironments(ctx, f)
		},
		Position: func(e domain.Environment) string { return e.ID },
		Visible:  func(e domain.Environment) bool { return h.can(ctx, p, CapEnvironmentRead, environmentResource(e)) },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Environment, 0, len(items))
	for _, e := range items {
		out = append(out, newEnvironment(e))
	}
	cursor, err := nextCursor(fingerprint, next)
	if err != nil {
		return nil, err
	}
	return &listEnvironmentsOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *agentsAPI) getEnvironment(ctx context.Context, in *environmentIDInput) (*environmentOutput, error) {
	_, env, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return &environmentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(env.Revision)}, Body: newEnvironment(env)}, nil
}

func (h *agentsAPI) updateEnvironment(ctx context.Context, in *updateEnvironmentInput) (*environmentOutput, error) {
	p, env, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !h.can(ctx, p, CapEnvironmentManage, environmentResource(env)) {
		return nil, Forbidden("not permitted to manage this environment")
	}
	if err := in.CheckIfMatch(RevisionETag(env.Revision)); err != nil {
		return nil, err
	}
	env, err = h.svc.UpdateEnvironment(ctx, env.ID, env.Revision, domain.EnvironmentPatch{Name: in.Body.Name, ServiceAddress: in.Body.ServiceAddress})
	if errors.Is(err, domain.ErrRevisionMismatch) {
		cur, gerr := h.svc.GetEnvironment(ctx, in.EnvironmentID)
		if gerr != nil {
			return nil, agentErr(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, agentErr(err)
	}
	return &environmentOutput{ETagHeader: ETagHeader{ETag: RevisionETag(env.Revision)}, Body: newEnvironment(env)}, nil
}

func (h *agentsAPI) deleteEnvironment(ctx context.Context, in *deleteEnvironmentInput) (*struct{}, error) {
	p, env, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !h.can(ctx, p, CapEnvironmentRemove, environmentResource(env)) {
		return nil, Forbidden("not permitted to remove this environment")
	}
	if err := in.CheckIfMatch(RevisionETag(env.Revision)); err != nil {
		return nil, err
	}
	if _, err := h.svc.ArchiveEnvironment(ctx, env.ID, env.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			cur, gerr := h.svc.GetEnvironment(ctx, env.ID)
			if gerr != nil {
				return nil, agentErr(gerr)
			}
			return nil, stale(cur.Revision)
		}
		return nil, agentErr(err)
	}
	logging.FromContext(ctx).Info("environment archived", "environment_id", env.ID, "principal", p.Key())
	return nil, nil
}

func (h *agentsAPI) listEnvironmentAgents(ctx context.Context, in *listEnvironmentAgentsInput) (*listAgentsOutput, error) {
	p, env, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return h.agentPage(ctx, p, domain.AgentFilter{EnvironmentID: env.ID}, in.PageParams, QueryFingerprint("env-agents", env.ID))
}

func (h *agentsAPI) environmentSystem(ctx context.Context, in *environmentIDInput) (*environmentSystemOutput, error) {
	p, env, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !h.can(ctx, p, CapEnvironmentSystemRead, environmentResource(env)) {
		return nil, Forbidden("not permitted to read this environment's system information")
	}
	sys, err := h.svc.EnvironmentSystem(ctx, env.ID)
	if err != nil {
		return nil, agentErr(err)
	}
	return &environmentSystemOutput{Body: newSystem(sys)}, nil
}

func registerEnvironments(a huma.API, deps Deps) {
	h := &agentsAPI{svc: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-environments", Method: http.MethodGet, Path: BasePath + "/environments",
			Summary: "List environments", Description: "Environments in creation order, filtered per item by environment.read. " +
				"Archived environments are listed only with ?status=archived.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		},
		Capability: CapEnvironmentRead, Scope: ScopeResource,
	}, h.listEnvironments)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-environment", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}",
			Summary: "Get an environment", Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
		},
		Capability: CapEnvironmentRead, Scope: ScopeEnvironment,
	}, h.getEnvironment)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-environment", Method: http.MethodPatch, Path: BasePath + "/environments/{environmentId}",
			Summary: "Update an environment", Description: "Edits the server/display name and the service address. Requires If-Match. " +
				"409 environment_archived for archived environments.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
				http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
		},
		Capability: CapEnvironmentManage, Scope: ScopeEnvironment,
	}, h.updateEnvironment)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-environment", Method: http.MethodDelete, Path: BasePath + "/environments/{environmentId}",
			Summary: "Archive an environment",
			Description: "Archives the environment: it is hidden from operations while its history and records are kept, its agent's " +
				"credential is revoked (a live session closes with 4403) and nothing on the host is touched. Enrolling its Engine again " +
				"with intent reattach:<environmentId> re-attaches it. Requires If-Match. The dependency preview before removal is #34.",
			Tags: []string{tagEnvironments}, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapEnvironmentRemove, Scope: ScopeEnvironment,
	}, h.deleteEnvironment)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-environment-agents", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/agents",
			Summary: "List an environment's agents", Description: "The environment's current and former (revoked) agents, newest first.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapAgentRead, Scope: ScopeEnvironment,
	}, h.listEnvironmentAgents)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-environment-system", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}/system",
			Summary: "Get an environment's system information",
			Description: "The agent's last capabilities report: Engine identity and negotiated API version, agent version and window status, " +
				"transport (plain-HTTP flag), verified file roots and diagnostics (#21, #27, #28). Live host metrics are #5.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapEnvironmentSystemRead, Scope: ScopeEnvironment,
	}, h.environmentSystem)
}
