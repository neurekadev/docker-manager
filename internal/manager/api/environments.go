package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Environment is one enrolled agent plus the Docker Engine it controls
// (#3). The UI and API say environment; "host" in prose means the same.
//
// Shaping (#17): with environment.read the environment is returned in
// full (view "full"). Any other capability applying in it (host stats, a
// container grant, ...) shows only id, name, status, online, view and
// actions (view "minimal"; revision too when an edit action is granted).
type Environment struct {
	ID                     string     `json:"id" example:"0190a6e0-3333-7000-8000-000000000003"`
	Name                   string     `json:"name" example:"NAS" doc:"Editable server/display name."`
	ServiceAddress         string     `json:"serviceAddress,omitempty" example:"nas.lan" doc:"Host name or IP address users browse to, for links to published ports."`
	Status                 string     `json:"status" enum:"active,archived" doc:"Archived environments are hidden from operations; their records are kept."`
	Online                 bool       `json:"online" doc:"The agent's session is established and its jobs were reconciled."`
	View                   string     `json:"view" enum:"minimal,full" doc:"full: environment.read; minimal: only identity, status and the granted actions (#17)."`
	Actions                []string   `json:"actions" doc:"Granted environment capabilities (e.g. environment.metrics.read)."`
	ConnectionChangedAt    *time.Time `json:"connectionChangedAt,omitempty" doc:"When the environment last went online or offline."`
	LastSeenAt             *time.Time `json:"lastSeenAt,omitempty" doc:"When its agent was last heard from: refreshed about every 60 s while connected, and on disconnect."`
	AgentID                string     `json:"agentId,omitempty" doc:"The active agent; absent while the environment is detached (its agent was removed)."`
	EngineID               string     `json:"engineId,omitempty" doc:"Docker Engine ID (full view)."`
	AllowDuplicateEngineID bool       `json:"allowDuplicateEngineId,omitempty" doc:"The owner declared this a distinct host sharing another environment's Engine ID (cloned VM)."`
	Revision               int64      `json:"revision,omitempty" doc:"Edit revision (the ETag); full view, or minimal view with an edit action."`
	CreatedAt              time.Time  `json:"createdAt,omitzero" doc:"Full view."`
	UpdatedAt              time.Time  `json:"updatedAt,omitzero" doc:"Full view."`
	ArchivedAt             *time.Time `json:"archivedAt,omitempty"`
	// Version compatibility of the active agent (#34, full view).
	AgentVersion        string `json:"agentVersion,omitempty" example:"0.0.0-edge" doc:"Version the active agent reported (full view; absent while detached)."`
	Compatibility       string `json:"compatibility,omitempty" enum:"current,outdated,unsupported" doc:"The active agent against this manager (full view): current; outdated (previous minor release, works, upgrade it); unsupported (its sessions are refused until it is upgraded)."`
	UpgradeInstructions string `json:"upgradeInstructions,omitempty" doc:"How to upgrade an outdated or unsupported agent (there is no in-app self-update in v1)."`
}

// agentCompatibility fills the version compatibility fields of a
// full-view environment from its active agent (#34).
func (e *Environment) agentCompatibility(a *domain.Agent, managerVersion string) {
	if e.View != authz.Full.String() || a == nil || a.ID != e.AgentID {
		return
	}
	e.AgentVersion = a.Version
	e.Compatibility, e.UpgradeInstructions = protocol.AgentCompatibility(managerVersion, a.Version)
}

// newEnvironment shapes an environment for a caller's view (#17).
func newEnvironment(e domain.Environment, v authz.View) Environment {
	if !v.Full() {
		out := Environment{ID: e.ID, Name: e.Name, Status: string(e.Status), Online: e.Online, View: v.Level.String(), Actions: Actions(v)}
		if v.Has(string(CapEnvironmentManage)) || v.Has(string(CapEnvironmentRemove)) {
			out.Revision = e.Revision
		}
		return out
	}
	return Environment{ID: e.ID, Name: e.Name, ServiceAddress: e.ServiceAddress, Status: string(e.Status), Online: e.Online,
		View: v.Level.String(), Actions: Actions(v),
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
	// From the Engine inventory (#5).
	MaxAPIVersion string `json:"maxApiVersion,omitempty" example:"1.51" doc:"Highest API version the Engine serves (Engine inventory)."`
	StorageDriver string `json:"storageDriver,omitempty" example:"overlay2"`
	CgroupVersion string `json:"cgroupVersion,omitempty" example:"2"`
	DockerDesktop bool   `json:"dockerDesktop,omitempty" doc:"The Engine runs inside Docker Desktop (unsupported, #25)."`
}

// SystemHost is the host as reported by the Engine inventory (#5).
type SystemHost struct {
	Hostname        string `json:"hostname" example:"nas" doc:"Engine host name (the environment name is editable separately)."`
	OperatingSystem string `json:"operatingSystem,omitempty" example:"Debian GNU/Linux 12 (bookworm)"`
	KernelVersion   string `json:"kernelVersion,omitempty"`
	OS              string `json:"os" example:"linux"`
	Arch            string `json:"arch" example:"amd64"`
	CPUs            int    `json:"cpus"`
	MemoryBytes     int64  `json:"memoryBytes"`
	UptimeSeconds   *int64 `json:"uptimeSeconds,omitempty" doc:"From the latest metrics sample."`
}

// SystemAgent is the environment's active agent as reported by it.
type SystemAgent struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	VersionStatus string `json:"versionStatus" enum:"current,outdated" doc:"Recorded when its last session started."`
	// Compatibility and UpgradeInstructions compare the version with this
	// manager now (#34).
	Compatibility       string   `json:"compatibility" enum:"current,outdated,unsupported" doc:"Against this manager now: unsupported agents are refused until upgraded."`
	UpgradeInstructions string   `json:"upgradeInstructions,omitempty"`
	OS                  string   `json:"os"`
	Arch                string   `json:"arch"`
	Protocols           []string `json:"protocols"`
	Connected           bool     `json:"connected"`
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
	// Engine inventory (#5): refreshed on reconnect, on Docker changes and
	// every 5 minutes; the last known one while offline.
	Host             *SystemHost   `json:"host,omitempty" doc:"Host identity and capacity (Engine inventory)."`
	Docker           *DockerCounts `json:"docker,omitempty" doc:"Docker object counts (Engine inventory; -1 = unknown)."`
	InventoryAt      *time.Time    `json:"inventoryAt,omitempty" doc:"When the agent read the Engine inventory."`
	ClockSkewSeconds *float64      `json:"clockSkewSeconds,omitempty" doc:"The agent clock's offset (manager minus agent) applied to its samples; absent within 2 s."`
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

func newSystem(s domain.EnvironmentSystem, managerVersion string) EnvironmentSystem {
	out := EnvironmentSystem{EnvironmentID: s.Environment.ID, Online: s.Environment.Online, Features: []string{}, Commands: []string{},
		Requests: []string{}, Streams: []string{}, Roots: []SystemRoot{}, Diagnostics: []SystemDiagnostic{}}
	a := s.Agent
	if a == nil {
		return out
	}
	out.Agent = &SystemAgent{ID: a.ID, Version: a.Version, VersionStatus: a.VersionStatus, Protocols: []string{},
		Connected: a.SessionID != "" && a.Status == domain.AgentActive}
	out.Agent.Compatibility, out.Agent.UpgradeInstructions = protocol.AgentCompatibility(managerVersion, a.Version)
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

// addInventory fills in the Engine inventory and clock skew (#5).
func addInventory(out *EnvironmentSystem, obs ObserveService, envID string) {
	if skew := obs.Skew(envID); skew != 0 {
		v := skew.Seconds()
		out.ClockSkewSeconds = &v
	}
	inv, ok := obs.Inventory(envID)
	if !ok {
		return
	}
	at := inv.CollectedAt
	out.InventoryAt = &at
	out.Host = &SystemHost{Hostname: inv.Hostname, OperatingSystem: inv.OperatingSystem, KernelVersion: inv.KernelVersion, OS: inv.OS,
		Arch: inv.Arch, CPUs: inv.CPUs, MemoryBytes: inv.MemoryBytes}
	if hx, ok := obs.Host(envID); ok {
		out.Host.UptimeSeconds = hx.UptimeSeconds
	}
	out.Docker = dockerCounts(inv)
	if out.Engine == nil {
		out.Engine = &SystemEngine{ID: inv.EngineID, Version: inv.Version, APIVersion: inv.NegotiatedAPIVersion, MinAPIVersion: inv.MinAPIVersion,
			OS: inv.OS, Arch: inv.Arch, Rootless: inv.Rootless}
	}
	out.Engine.MaxAPIVersion, out.Engine.StorageDriver, out.Engine.CgroupVersion = inv.APIVersion, inv.StorageDriver, inv.CgroupVersion
	out.Engine.DockerDesktop = inv.DockerDesktop
	if inv.Version != "" {
		out.Engine.Version = inv.Version
	}
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

func environmentResource(e domain.Environment) authz.Resource { return authz.EnvironmentResource(e.ID) }

// visibleEnvironment loads an environment the caller may see at least
// minimally (404 otherwise, so existence does not leak), with the
// caller's per-request checker and view.
func (h *agentsAPI) visibleEnvironment(ctx context.Context, id string) (authz.Checker, domain.Environment, authz.View, error) {
	c, err := h.checker(ctx)
	if err != nil {
		return nil, domain.Environment{}, authz.View{}, err
	}
	env, err := h.svc.GetEnvironment(ctx, id)
	if err != nil {
		return nil, domain.Environment{}, authz.View{}, agentErr(err)
	}
	v := authz.ViewOf(c, environmentResource(env))
	if !v.Visible() {
		return nil, domain.Environment{}, authz.View{}, NotFound("environment not found")
	}
	return c, env, v, nil
}

// stale answers a lost compare-and-swap: 412 with the current ETag.
func stale(current int64) error {
	return PreconditionFailed("the resource was changed since you loaded it",
		Field("header.If-Match", "stale revision; reload the resource, reapply your change and retry with its current ETag")).
		WithHeader("ETag", RevisionETag(current))
}

func (h *agentsAPI) listEnvironments(ctx context.Context, in *listEnvironmentsInput) (*listEnvironmentsOutput, error) {
	c, err := h.checker(ctx)
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
		Visible:  func(e domain.Environment) bool { return authz.ViewOf(c, environmentResource(e)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	active, err := h.activeAgents(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Environment, 0, len(items))
	for _, e := range items {
		env := newEnvironment(e, authz.ViewOf(c, environmentResource(e)))
		env.agentCompatibility(active[e.AgentID], h.deps.Build.Version)
		out = append(out, env)
	}
	cursor, err := nextCursor(fingerprint, next)
	if err != nil {
		return nil, err
	}
	return &listEnvironmentsOutput{Body: NewPage(out, cursor, nil)}, nil
}

func environmentETag(e Environment) ETagHeader {
	if e.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(e.Revision)}
}

func (h *agentsAPI) getEnvironment(ctx context.Context, in *environmentIDInput) (*environmentOutput, error) {
	_, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	body := newEnvironment(env, v)
	if err := h.addCompatibility(ctx, &body); err != nil {
		return nil, err
	}
	return &environmentOutput{ETagHeader: environmentETag(body), Body: body}, nil
}

// activeAgents maps agent ID to active agent (the version compatibility
// of listed environments, #34).
func (h *agentsAPI) activeAgents(ctx context.Context) (map[string]*domain.Agent, error) {
	list, err := h.svc.ListAgents(ctx, domain.AgentFilter{Statuses: []domain.AgentStatus{domain.AgentActive}})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*domain.Agent, len(list))
	for i := range list {
		out[list[i].ID] = &list[i]
	}
	return out, nil
}

// addCompatibility fills the version compatibility of one full-view
// environment (#34).
func (h *agentsAPI) addCompatibility(ctx context.Context, e *Environment) error {
	if e.View != authz.Full.String() || e.AgentID == "" {
		return nil
	}
	a, err := h.svc.GetAgent(ctx, e.AgentID)
	if errors.Is(err, domain.ErrAgentNotFound) {
		return nil
	}
	if err != nil {
		return Internal(err)
	}
	e.agentCompatibility(&a, h.deps.Build.Version)
	return nil
}

func (h *agentsAPI) updateEnvironment(ctx context.Context, in *updateEnvironmentInput) (*environmentOutput, error) {
	c, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapEnvironmentManage)) {
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
	body := newEnvironment(env, authz.ViewOf(c, environmentResource(env)))
	if err := h.addCompatibility(ctx, &body); err != nil {
		return nil, err
	}
	return &environmentOutput{ETagHeader: environmentETag(body), Body: body}, nil
}

func (h *agentsAPI) deleteEnvironment(ctx context.Context, in *deleteEnvironmentInput) (*struct{}, error) {
	_, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapEnvironmentRemove)) {
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
	logging.FromContext(ctx).Info("environment archived", "environment_id", env.ID)
	return nil, nil
}

func (h *agentsAPI) listEnvironmentAgents(ctx context.Context, in *listEnvironmentAgentsInput) (*listAgentsOutput, error) {
	c, env, _, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return h.agentPage(ctx, c, domain.AgentFilter{EnvironmentID: env.ID}, in.PageParams, QueryFingerprint("env-agents", env.ID))
}

func (h *agentsAPI) environmentSystem(ctx context.Context, in *environmentIDInput) (*environmentSystemOutput, error) {
	_, env, v, err := h.visibleEnvironment(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapEnvironmentSystemRead)) {
		return nil, Forbidden("not permitted to read this environment's system information")
	}
	sys, err := h.svc.EnvironmentSystem(ctx, env.ID)
	if err != nil {
		return nil, agentErr(err)
	}
	out := newSystem(sys, h.deps.Build.Version)
	if obs := h.deps.Observe; obs != nil {
		addInventory(&out, obs, env.ID)
	}
	return &environmentSystemOutput{Body: out}, nil
}

func registerEnvironments(a huma.API, deps Deps) {
	h := &agentsAPI{svc: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-environments", Method: http.MethodGet, Path: BasePath + "/environments",
			Summary: "List environments", Description: "Environments in creation order, filtered per item (#17): environment.read shows " +
				"an environment in full; any other capability applying in it (host stats, a grant on one of its stacks or containers) " +
				"shows only its identity, status and granted actions (view minimal). Archived environments are listed only with ?status=archived.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		},
		Capability: CapEnvironmentRead, Scope: ScopeResource,
	}, h.listEnvironments)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-environment", Method: http.MethodGet, Path: BasePath + "/environments/{environmentId}",
			Summary: "Get an environment", Description: "Full with environment.read, minimal (identity, status, actions) with any other " +
				"capability applying in it, 404 otherwise. The ETag is sent when the revision is visible.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
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
			Description: "Archives the environment: it is hidden from operations while its history and records are kept (stacks, " +
				"policies, backup repositories, sets and snapshots, registry and Git bindings), its agent's credential is revoked (a live " +
				"session closes with 4403), the permission rules scoped to it are removed (audited as environment.permission_rules_remove) " +
				"and nothing on the host is touched. Scheduled update, backup and prune runs for it are refused while it is archived. " +
				"Preview the dependent records first with POST …/removal-previews. Enrolling its Engine again with intent " +
				"reattach:<environmentId> re-attaches it and its stacks and policies resume. Requires If-Match.",
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
				"transport (plain-HTTP flag), verified file roots and diagnostics (#21, #27, #28), plus the Engine inventory: host identity, " +
				"capacity and Docker counts, refreshed on change (#5). Host metrics: GET …/metrics and …/capacity.",
			Tags: []string{tagEnvironments}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapEnvironmentSystemRead, Scope: ScopeEnvironment,
	}, h.environmentSystem)
}
