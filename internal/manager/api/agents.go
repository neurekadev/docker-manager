package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Agent enrollment and agent routes (#3). Environments: environments.go.
// Protocol and enrollment semantics: docs/protocol/agent-v1.md.

const (
	tagAgents       = "Agents"
	tagEnvironments = "Environments"
)

// Agent and environment capabilities (#17).
const (
	CapAgentEnroll           Capability = "agent.enroll"
	CapAgentRead             Capability = "agent.read"
	CapAgentManage           Capability = "agent.manage"
	CapAgentRemove           Capability = "agent.remove"
	CapEnvironmentRead       Capability = "environment.read"
	CapEnvironmentManage     Capability = "environment.manage"
	CapEnvironmentRemove     Capability = "environment.remove"
	CapEnvironmentSystemRead Capability = "environment.system.read"
	resourceTypeAgent                   = "agent"
	resourceTypeEnvironment             = "environment"
)

// AgentService is the agent/environment service as seen by the API
// (implemented by internal/manager/agents.Service).
type AgentService interface {
	CreateEnrollment(ctx context.Context, spec domain.EnrollmentSpec) (domain.CreatedEnrollment, error)
	ListEnrollments(ctx context.Context, beforeID string, limit int) ([]domain.Enrollment, error)
	GetEnrollment(ctx context.Context, id string) (domain.Enrollment, error)
	RevokeEnrollment(ctx context.Context, id string) (domain.Enrollment, error)

	ListAgents(ctx context.Context, f domain.AgentFilter) ([]domain.Agent, error)
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
	UpdateAgentLabel(ctx context.Context, id string, expectRevision int64, label string) (domain.Agent, error)
	RemoveAgent(ctx context.Context, id string, expectRevision int64) (domain.Agent, error)
	RotateCredential(ctx context.Context, agentID string) (domain.CredentialRotation, error)

	ListEnvironments(ctx context.Context, f domain.EnvironmentFilter) ([]domain.Environment, error)
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	UpdateEnvironment(ctx context.Context, id string, expectRevision int64, p domain.EnvironmentPatch) (domain.Environment, error)
	ArchiveEnvironment(ctx context.Context, id string, expectRevision int64) (domain.Environment, error)
	EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error)
}

// EnrollmentRejection is the last refused enrollment attempt, kept so the
// owner can resolve a duplicate agent or an Engine ID collision.
type EnrollmentRejection struct {
	Code                  string    `json:"code" enum:"engine_already_enrolled,engine_identity_conflict,environment_archived,environment_detached,engine_mismatch,enrollment_target_unavailable" doc:"Stable error code the agent received (409)."`
	Message               string    `json:"message"`
	At                    time.Time `json:"at"`
	EngineID              string    `json:"engineId,omitempty" doc:"Docker Engine ID the agent reported."`
	InstallID             string    `json:"installId,omitempty" doc:"The agent's install ID (generated per agent state volume)."`
	Hostname              string    `json:"hostname,omitempty" doc:"Engine host name the agent reported."`
	ConflictAgentID       string    `json:"conflictAgentId,omitempty" doc:"The agent already controlling the Engine."`
	ConflictEnvironmentID string    `json:"conflictEnvironmentId,omitempty"`
}

// AgentEnrollment is a one-use agent enrollment token (never its value).
type AgentEnrollment struct {
	ID                     string               `json:"id" example:"0190a6e0-0000-7000-8000-000000000011"`
	Intent                 string               `json:"intent" example:"new" doc:"new, replace:<agentId> or reattach:<environmentId>; fixed when the enrollment is created."`
	EnvironmentName        string               `json:"environmentName,omitempty" doc:"Display name preset for the environment."`
	AllowDuplicateEngineID bool                 `json:"allowDuplicateEngineId" doc:"The enrolling host may share an enrolled Engine's ID (a cloned machine)."`
	State                  string               `json:"state" enum:"pending,used,expired,revoked"`
	CreatedAt              time.Time            `json:"createdAt"`
	ExpiresAt              time.Time            `json:"expiresAt"`
	UsedAt                 *time.Time           `json:"usedAt,omitempty"`
	RevokedAt              *time.Time           `json:"revokedAt,omitempty"`
	AgentID                string               `json:"agentId,omitempty" doc:"The agent created by the enrollment."`
	CreatedBy              string               `json:"createdBy,omitempty" doc:"User who created it (audit metadata)."`
	LastRejection          *EnrollmentRejection `json:"lastRejection,omitempty" doc:"The last refused attempt with this token (the token stays usable until it expires)."`
}

// InstallCommand is a generated way to start an agent with the token.
type InstallCommand struct {
	Variant     string `json:"variant" enum:"colocated,remote,remote_compose"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Command     string `json:"command" doc:"Shell text; contains the token (show once, never log)."`
}

// CreatedAgentEnrollment is the one response that contains the token.
type CreatedAgentEnrollment struct {
	Enrollment AgentEnrollment `json:"enrollment"`
	Token      string          `json:"token" example:"dye_0190a6e0-0000-7000-8000-000000000011_q2V1c..." doc:"The one-use enrollment token. Returned only in this response (and in an Idempotency-Key replay); only its SHA-256 verifier is stored. Never put it in a URL."`
	ManagerURL string          `json:"managerUrl" example:"https://docker.example.com" doc:"DOCKYARD_PUBLIC_URL, the origin remote agents dial."`
	// InstallCommands contain the token.
	InstallCommands []InstallCommand `json:"installCommands"`
}

func newEnrollment(e domain.Enrollment, now time.Time) AgentEnrollment {
	out := AgentEnrollment{ID: e.ID, Intent: string(e.Intent), EnvironmentName: e.EnvironmentName,
		AllowDuplicateEngineID: e.AllowDuplicateEngineID, State: string(e.State(now)), CreatedAt: e.CreatedAt,
		ExpiresAt: e.ExpiresAt, UsedAt: e.UsedAt, RevokedAt: e.RevokedAt, AgentID: e.AgentID, CreatedBy: e.CreatedBy}
	if e.Intent != domain.IntentNew && e.TargetID != "" {
		out.Intent += ":" + e.TargetID
	}
	if r := e.Rejection; r != nil {
		out.LastRejection = &EnrollmentRejection{Code: r.Code, Message: r.Message, At: r.At, EngineID: r.EngineID, InstallID: r.InstallID,
			Hostname: r.Hostname, ConflictAgentID: r.ConflictAgentID, ConflictEnvironmentID: r.ConflictEnvironmentID}
	}
	return out
}

// AgentTransport is how the agent reaches the manager (#27).
type AgentTransport struct {
	ManagerURL string `json:"managerUrl"`
	PlainHTTP  bool   `json:"plainHttp" doc:"The agent uses a plain-HTTP internal URL (DOCKYARD_MANAGER_ALLOW_HTTP); flagged on the host page."`
	CustomCA   bool   `json:"customCa"`
}

// Agent is one enrolled agent instance. Its ID is distinct from the Docker
// Engine ID; engineId plus installId identify the installation.
//
// Shaping (#17): agent.read shows the agent in full; any other agent
// capability shows only id, environmentId, status, connected, view and
// actions (revision too when an edit action is granted).
type Agent struct {
	ID            string   `json:"id" example:"0190a6e0-2222-7000-8000-000000000002"`
	EnvironmentID string   `json:"environmentId"`
	Status        string   `json:"status" enum:"active,revoked"`
	Connected     bool     `json:"connected" doc:"A session is established. The environment is reported online once the session's jobs were reconciled."`
	View          string   `json:"view" enum:"minimal,full" doc:"full: agent.read; minimal: only identity, status and the granted actions (#17)."`
	Actions       []string `json:"actions" doc:"Granted agent capabilities."`
	Label         string   `json:"label,omitempty" doc:"Optional operator note."`
	RevokedReason string   `json:"revokedReason,omitempty" example:"removed"`
	Hostname      string   `json:"hostname,omitempty" doc:"Engine host name reported at enrollment."`
	InstallID     string   `json:"installId,omitempty" doc:"Generated by the agent once per state volume (full view)."`
	EngineID      string   `json:"engineId,omitempty" doc:"Docker Engine ID (full view)."`
	Version       string   `json:"version,omitempty" example:"0.0.0-edge"`
	VersionStatus string   `json:"versionStatus,omitempty" enum:"current,outdated" doc:"Recorded when its last session started. outdated: previous minor release, still supported; upgrade it."`
	// Compatibility compares the version with this manager now (#34).
	Compatibility       string          `json:"compatibility,omitempty" enum:"current,outdated,unsupported" doc:"The version against this manager now (full view of an active agent, #34): unsupported agents are refused until upgraded."`
	UpgradeInstructions string          `json:"upgradeInstructions,omitempty" doc:"How to upgrade an outdated or unsupported agent (no in-app self-update in v1)."`
	RotationPending     bool            `json:"rotationPending,omitempty" doc:"A new credential waits for the agent's confirmation; the old one stays valid until then."`
	Transport           *AgentTransport `json:"transport,omitempty"`
	Revision            int64           `json:"revision,omitempty" doc:"Edit revision (the ETag); full view, or minimal view with an edit action."`
	CreatedAt           time.Time       `json:"createdAt,omitzero" doc:"Full view."`
	UpdatedAt           time.Time       `json:"updatedAt,omitzero" doc:"Full view."`
	LastConnectedAt     *time.Time      `json:"lastConnectedAt,omitempty"`
	LastSeenAt          *time.Time      `json:"lastSeenAt,omitempty"`
	RevokedAt           *time.Time      `json:"revokedAt,omitempty"`
}

// newAgent shapes an agent for a caller's view (#17).
func newAgent(a domain.Agent, v authz.View, managerVersion string) Agent {
	connected := a.SessionID != "" && a.Status == domain.AgentActive
	if !v.Full() {
		out := Agent{ID: a.ID, EnvironmentID: a.EnvironmentID, Status: string(a.Status), Connected: connected,
			View: v.Level.String(), Actions: Actions(v)}
		if v.Has(string(CapAgentManage)) || v.Has(string(CapAgentRemove)) {
			out.Revision = a.Revision
		}
		return out
	}
	out := Agent{ID: a.ID, EnvironmentID: a.EnvironmentID, Label: a.Label, Status: string(a.Status), RevokedReason: a.RevokedReason,
		View: v.Level.String(), Actions: Actions(v),
		Connected: connected, Hostname: a.Hostname, InstallID: a.InstallID, EngineID: a.EngineID,
		Version: a.Version, VersionStatus: a.VersionStatus, RotationPending: a.RotationPending, Revision: a.Revision,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, LastConnectedAt: a.LastConnectedAt, LastSeenAt: a.LastSeenAt, RevokedAt: a.RevokedAt}
	if a.Status == domain.AgentActive {
		out.Compatibility, out.UpgradeInstructions = protocol.AgentCompatibility(managerVersion, a.Version)
	}
	if c := capabilitiesOf(a); c != nil {
		out.Transport = &AgentTransport{ManagerURL: c.Transport.ManagerURL, PlainHTTP: c.Transport.PlainHTTP, CustomCA: c.Transport.CustomCA}
	}
	return out
}

// CredentialRotation is the result of POST .../credential-rotations.
type CredentialRotation struct {
	AgentID     string     `json:"agentId" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f"`
	State       string     `json:"state" enum:"completed,pending" doc:"completed: the agent persisted the new credential and the old one is revoked. pending: the agent is offline or did not confirm yet; the old credential stays valid and the new one is delivered when the agent reconnects."`
	RequestedAt time.Time  `json:"requestedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// --- inputs/outputs ---

type listEnrollmentsInput struct {
	PageParams
}

type createEnrollmentInput struct {
	IdempotencyKeyParam
	Body struct {
		Intent                 string `json:"intent,omitempty" maxLength:"64" pattern:"^(new|replace:[0-9a-f-]{36}|reattach:[0-9a-f-]{36})$" example:"new" doc:"new (default): a new environment. replace:<agentId>: the new agent replaces that agent for the same Engine and its credential is revoked when enrollment succeeds. reattach:<environmentId>: re-attach that archived or detached environment."`
		EnvironmentName        string `json:"environmentName,omitempty" maxLength:"63" example:"NAS" doc:"Preset display name (otherwise the agent's DOCKYARD_ENVIRONMENT_NAME or the Engine host name)."`
		ExpiresInSeconds       int    `json:"expiresInSeconds,omitempty" minimum:"60" maximum:"86400" example:"3600" doc:"Token lifetime; default 3600 (1 h), at most 86400 (24 h)."`
		AllowDuplicateEngineID bool   `json:"allowDuplicateEngineId,omitempty" doc:"Only with intent new: the enrolling host is a different machine that reports the same Docker Engine ID as an enrolled one (a cloned VM). Prefer regenerating the clone's Engine ID."`
	}
}

type enrollmentIDInput struct {
	EnrollmentID string `path:"enrollmentId" maxLength:"64" doc:"Enrollment ID."`
}

type listEnrollmentsOutput struct{ Body Page[AgentEnrollment] }
type createEnrollmentOutput struct{ Body CreatedAgentEnrollment }

type listAgentsInput struct {
	PageParams
	Status        []string `query:"status" enum:"active,revoked" doc:"Only agents in these states."`
	EnvironmentID string   `query:"environmentId" maxLength:"64" doc:"Only agents of this environment."`
}

type agentIDInput struct {
	AgentID string `path:"agentId" maxLength:"64" doc:"Agent ID."`
}

type updateAgentInput struct {
	AgentID string `path:"agentId" maxLength:"64" doc:"Agent ID."`
	IfMatchParam
	Body struct {
		Label *string `json:"label,omitempty" example:"Rack 2, left" maxLength:"200" doc:"Operator note; empty clears it."`
	}
}

type deleteAgentInput struct {
	AgentID string `path:"agentId" maxLength:"64" doc:"Agent ID."`
	IfMatchParam
}

type rotateCredentialInput struct {
	AgentID string `path:"agentId" maxLength:"64" doc:"Agent ID."`
	IdempotencyKeyParam
}

type agentOutput struct {
	ETagHeader
	Body Agent
}

type listAgentsOutput struct{ Body Page[Agent] }
type rotationOutput struct{ Body CredentialRotation }

type agentCursor struct {
	ID string `json:"i"`
}

// --- handlers ---

type agentsAPI struct {
	svc   AgentService
	authz authz.Authorizer
	deps  Deps
}

func (h *agentsAPI) principal(ctx context.Context) (authz.Principal, error) {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return p, Unauthenticated("authentication required")
	}
	if h.svc == nil {
		return p, Unavailable(CodeUnavailable, "the agent service is not available")
	}
	return p, nil
}

// checker is the caller's per-request permission checker (#17).
func (h *agentsAPI) checker(ctx context.Context) (authz.Checker, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return nil, err
	}
	return authz.For(ctx, h.authz, p), nil
}

func agentResource(a domain.Agent) authz.Resource {
	return authz.Resource{Type: resourceTypeAgent, ID: a.ID, EnvironmentID: a.EnvironmentID, Parents: []authz.ResourceRef{}}
}

// requireInstance checks an instance-scoped capability (403: the caller
// knows these routes exist).
func (h *agentsAPI) requireInstance(ctx context.Context, c Capability) (authz.Principal, error) {
	p, err := h.principal(ctx)
	if err != nil {
		return p, err
	}
	if !h.authz.Can(ctx, p, string(c), authz.Instance()).Allowed {
		return p, Forbidden("not permitted to manage agent enrollments")
	}
	return p, nil
}

// agentErr maps service errors of agent/environment routes.
func agentErr(err error) error {
	var in *domain.InputError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &in):
		return Invalid(in.Message, Field("body."+in.Field, in.Message))
	case errors.Is(err, domain.ErrAgentNotFound):
		return NotFound("agent not found")
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	case errors.Is(err, domain.ErrEnrollmentNotFound):
		return NotFound("enrollment not found")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived")
	case errors.Is(err, domain.ErrAgentRevoked):
		return Conflict(CodeAgentRevoked, "the agent was removed or replaced")
	}
	return Internal(err)
}

func (h *agentsAPI) listEnrollments(ctx context.Context, in *listEnrollmentsInput) (*listEnrollmentsOutput, error) {
	if _, err := h.requireInstance(ctx, CapAgentEnroll); err != nil {
		return nil, err
	}
	fingerprint := QueryFingerprint("enrollments")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Enrollment]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, before string, n int) ([]domain.Enrollment, error) {
			return h.svc.ListEnrollments(ctx, before, n)
		},
		Position: func(e domain.Enrollment) string { return e.ID },
		Visible:  func(domain.Enrollment) bool { return true },
	})
	if err != nil {
		return nil, Internal(err)
	}
	now := h.deps.clock().Now()
	out := make([]AgentEnrollment, 0, len(items))
	for _, e := range items {
		out = append(out, newEnrollment(e, now))
	}
	cursor, err := nextCursor(fingerprint, next)
	if err != nil {
		return nil, err
	}
	return &listEnrollmentsOutput{Body: NewPage(out, cursor, nil)}, nil
}

func nextCursor(fingerprint, next string) (string, error) {
	if next == "" {
		return "", nil
	}
	c, err := CursorFor(fingerprint, agentCursor{ID: next})
	if err != nil {
		return "", Internal(err)
	}
	return c, nil
}

func (h *agentsAPI) createEnrollment(ctx context.Context, in *createEnrollmentInput) (*createEnrollmentOutput, error) {
	p, err := h.requireInstance(ctx, CapAgentEnroll)
	if err != nil {
		return nil, err
	}
	spec := domain.EnrollmentSpec{Intent: domain.IntentNew, EnvironmentName: in.Body.EnvironmentName,
		TTL: time.Duration(in.Body.ExpiresInSeconds) * time.Second, AllowDuplicateEngineID: in.Body.AllowDuplicateEngineID, CreatedBy: p.UserID}
	if kind, target, ok := strings.Cut(in.Body.Intent, ":"); ok {
		spec.Intent, spec.TargetID = domain.EnrollmentIntent(kind), target
		if !ids.Valid(target) {
			return nil, Invalid("invalid intent", Field("body.intent", "the target must be a DockYard ID"))
		}
	}
	created, err := h.svc.CreateEnrollment(ctx, spec)
	if err != nil {
		return nil, agentErr(err)
	}
	logging.FromContext(ctx).Info("agent enrollment created", "enrollment_id", created.Enrollment.ID, "intent", created.Enrollment.Intent, "principal", p.Key())
	body := CreatedAgentEnrollment{Enrollment: newEnrollment(created.Enrollment, h.deps.clock().Now()), Token: created.Token,
		ManagerURL: created.ManagerURL, InstallCommands: make([]InstallCommand, 0, len(created.Install))}
	for _, c := range created.Install {
		body.InstallCommands = append(body.InstallCommands, InstallCommand(c))
	}
	return &createEnrollmentOutput{Body: body}, nil
}

func (h *agentsAPI) deleteEnrollment(ctx context.Context, in *enrollmentIDInput) (*struct{}, error) {
	p, err := h.requireInstance(ctx, CapAgentEnroll)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.RevokeEnrollment(ctx, in.EnrollmentID); err != nil {
		return nil, agentErr(err)
	}
	logging.FromContext(ctx).Info("agent enrollment revoked", "enrollment_id", in.EnrollmentID, "principal", p.Key())
	return nil, nil
}

// visibleAgent loads an agent the caller may see at least minimally (404
// otherwise), with the caller's view.
func (h *agentsAPI) visibleAgent(ctx context.Context, id string) (domain.Agent, authz.View, error) {
	c, err := h.checker(ctx)
	if err != nil {
		return domain.Agent{}, authz.View{}, err
	}
	a, err := h.svc.GetAgent(ctx, id)
	if err != nil {
		return domain.Agent{}, authz.View{}, agentErr(err)
	}
	v := authz.ViewOf(c, agentResource(a))
	if !v.Visible() {
		return domain.Agent{}, authz.View{}, NotFound("agent not found")
	}
	return a, v, nil
}

func (h *agentsAPI) listAgents(ctx context.Context, in *listAgentsInput) (*listAgentsOutput, error) {
	c, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	f := domain.AgentFilter{EnvironmentID: in.EnvironmentID}
	for _, s := range in.Status {
		f.Statuses = append(f.Statuses, domain.AgentStatus(s))
	}
	return h.agentPage(ctx, c, f, in.PageParams, QueryFingerprint(strings.Join(in.Status, ","), in.EnvironmentID))
}

func (h *agentsAPI) agentPage(ctx context.Context, c authz.Checker, f domain.AgentFilter, page PageParams, fingerprint string) (*listAgentsOutput, error) {
	var after agentCursor
	if page.Cursor != "" {
		if err := DecodeCursorFor(page.Cursor, fingerprint, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.Agent]{
		Limit: page.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, before string, n int) ([]domain.Agent, error) {
			f.BeforeID, f.Limit = before, n
			return h.svc.ListAgents(ctx, f)
		},
		Position: func(a domain.Agent) string { return a.ID },
		Visible:  func(a domain.Agent) bool { return authz.ViewOf(c, agentResource(a)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]Agent, 0, len(items))
	for _, a := range items {
		out = append(out, newAgent(a, authz.ViewOf(c, agentResource(a)), h.deps.Build.Version))
	}
	cursor, err := nextCursor(fingerprint, next)
	if err != nil {
		return nil, err
	}
	return &listAgentsOutput{Body: NewPage(out, cursor, nil)}, nil
}

func agentETag(a Agent) ETagHeader {
	if a.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(a.Revision)}
}

func (h *agentsAPI) getAgent(ctx context.Context, in *agentIDInput) (*agentOutput, error) {
	a, v, err := h.visibleAgent(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	body := newAgent(a, v, h.deps.Build.Version)
	return &agentOutput{ETagHeader: agentETag(body), Body: body}, nil
}

func (h *agentsAPI) updateAgent(ctx context.Context, in *updateAgentInput) (*agentOutput, error) {
	a, v, err := h.visibleAgent(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapAgentManage)) {
		return nil, Forbidden("not permitted to manage this agent")
	}
	if err := in.CheckIfMatch(RevisionETag(a.Revision)); err != nil {
		return nil, err
	}
	label := a.Label
	if in.Body.Label != nil {
		label = *in.Body.Label
	}
	a, err = h.svc.UpdateAgentLabel(ctx, a.ID, a.Revision, label)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, h.staleAgent(ctx, in.AgentID)
	}
	if err != nil {
		return nil, agentErr(err)
	}
	body := newAgent(a, v, h.deps.Build.Version)
	return &agentOutput{ETagHeader: agentETag(body), Body: body}, nil
}

// staleAgent answers a lost compare-and-swap with 412 and the current ETag.
func (h *agentsAPI) staleAgent(ctx context.Context, id string) error {
	cur, err := h.svc.GetAgent(ctx, id)
	if err != nil {
		return agentErr(err)
	}
	return stale(cur.Revision)
}

func (h *agentsAPI) deleteAgent(ctx context.Context, in *deleteAgentInput) (*struct{}, error) {
	a, v, err := h.visibleAgent(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapAgentRemove)) {
		return nil, Forbidden("not permitted to remove this agent")
	}
	if err := in.CheckIfMatch(RevisionETag(a.Revision)); err != nil {
		return nil, err
	}
	if _, err := h.svc.RemoveAgent(ctx, a.ID, a.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			return nil, h.staleAgent(ctx, a.ID)
		}
		return nil, agentErr(err)
	}
	logging.FromContext(ctx).Info("agent removed", "agent_id", a.ID)
	return nil, nil
}

func (h *agentsAPI) rotateCredential(ctx context.Context, in *rotateCredentialInput) (*rotationOutput, error) {
	a, v, err := h.visibleAgent(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapAgentManage)) {
		return nil, Forbidden("not permitted to manage this agent")
	}
	r, err := h.svc.RotateCredential(ctx, a.ID)
	if err != nil {
		return nil, agentErr(err)
	}
	logging.FromContext(ctx).Info("agent credential rotation", "agent_id", a.ID, "state", r.State)
	return &rotationOutput{Body: CredentialRotation{AgentID: r.AgentID, State: r.State, RequestedAt: r.RequestedAt, CompletedAt: r.CompletedAt}}, nil
}

func registerAgents(a huma.API, deps Deps) {
	h := &agentsAPI{svc: deps.Agents, authz: authz.OrDenyAll(deps.Authorizer), deps: deps}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-agent-enrollments", Method: http.MethodGet, Path: BasePath + "/agent-enrollments",
			Summary: "List agent enrollments", Description: "Enrollment tokens newest first, with their state and the last refused attempt. " +
				"Token values are never returned again after creation.",
			Tags: []string{tagAgents}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapAgentEnroll, Scope: ScopeInstance,
	}, h.listEnrollments)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-agent-enrollment", Method: http.MethodPost, Path: BasePath + "/agent-enrollments",
			Summary: "Create an agent enrollment token",
			Description: "Creates a one-use, short-lived enrollment token with a fixed intent and returns it once, with install commands for a " +
				"co-located agent and for another Docker host. The manager stores only a verifier. The agent exchanges the token on " +
				"POST /agent/v1/enroll (docs/protocol/agent-v1.md). Stored-idempotent: a retry with the same Idempotency-Key replays the response.",
			Tags: []string{tagAgents}, DefaultStatus: http.StatusCreated,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapAgentEnroll, Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, h.createEnrollment)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-agent-enrollment", Method: http.MethodDelete, Path: BasePath + "/agent-enrollments/{enrollmentId}",
			Summary: "Revoke an agent enrollment", Description: "Revokes an unused token at once. Used, expired or already revoked enrollments are unchanged.",
			Tags: []string{tagAgents}, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapAgentEnroll, Scope: ScopeInstance,
	}, h.deleteEnrollment)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-agents", Method: http.MethodGet, Path: BasePath + "/agents",
			Summary: "List agents", Description: "Agents newest first (active and revoked), filtered per item (#17): agent.read shows an " +
				"agent in full, any other agent capability only its identity, status and granted actions (view minimal).",
			Tags: []string{tagAgents}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		},
		Capability: CapAgentRead, Scope: ScopeResource,
	}, h.listAgents)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-agent", Method: http.MethodGet, Path: BasePath + "/agents/{agentId}",
			Summary: "Get an agent", Tags: []string{tagAgents}, Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
		},
		Capability: CapAgentRead, Scope: ScopeResource,
	}, h.getAgent)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-agent", Method: http.MethodPatch, Path: BasePath + "/agents/{agentId}",
			Summary: "Update an agent", Description: "Sets the operator label. Requires If-Match.",
			Tags: []string{tagAgents}, Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
				http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
		},
		Capability: CapAgentManage, Scope: ScopeResource,
	}, h.updateAgent)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-agent", Method: http.MethodDelete, Path: BasePath + "/agents/{agentId}",
			Summary: "Remove an agent",
			Description: "Revokes the agent's credential at once and closes its live session (close code 4403); the environment stays " +
				"offline and detached until an agent is enrolled with intent reattach:<environmentId>. Nothing on the host is touched. " +
				"Requires If-Match. Removing an already revoked agent changes nothing.",
			Tags: []string{tagAgents}, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapAgentRemove, Scope: ScopeResource,
	}, h.deleteAgent)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-agent-credential-rotation", Method: http.MethodPost, Path: BasePath + "/agents/{agentId}/credential-rotations",
			Summary: "Rotate an agent credential",
			Description: "Issues a new credential and hands it to the agent over its live session; the agent persists it and confirms, then " +
				"the old credential stops working (state completed). If the agent is offline or does not confirm within 30 s the rotation " +
				"is pending: the old credential stays valid and the new one is delivered when the agent reconnects. If you suspect the " +
				"credential was stolen, remove the agent and enroll it again instead. Stored-idempotent.",
			Tags: []string{tagAgents}, DefaultStatus: http.StatusCreated,
			Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapAgentManage, Scope: ScopeResource, Idempotency: IdempotencyStored,
	}, h.rotateCredential)
}
