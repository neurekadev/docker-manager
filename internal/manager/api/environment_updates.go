package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
)

// environmentUpdateService is the user-facing policy layer of updates.
// The existing target policy service remains an internal execution detail.
type environmentUpdateService interface {
	ListEnvironmentPolicies(context.Context) ([]domain.EnvironmentUpdatePolicy, error)
	GetEnvironmentPolicy(context.Context, string) (domain.EnvironmentUpdatePolicy, error)
	CreateEnvironmentPolicy(context.Context, updates.NewEnvironmentPolicy) (domain.EnvironmentUpdatePolicy, error)
	UpdateEnvironmentPolicy(context.Context, string, int64, updates.NewEnvironmentPolicy) (domain.EnvironmentUpdatePolicy, error)
	DeleteEnvironmentPolicy(context.Context, string, int64) error
	ManagedPolicies(context.Context, string) ([]domain.UpdatePolicy, error)
	CheckEnvironment(context.Context, authz.Principal, string, string) ([]domain.Job, error)
	PreviewEnvironment(context.Context, string) (updates.EnvironmentPreview, error)
	RunEnvironment(context.Context, authz.Principal, string, string, string) ([]domain.Job, error)
	Candidates(context.Context, string) ([]domain.UpdateCandidate, error)
}

type environmentUpdatesAPI struct {
	svc   environmentUpdateService
	authz authz.Authorizer
}

type environmentUpdatePolicy struct {
	ID                 string              `json:"id"`
	Scope              string              `json:"scope" enum:"all,environment"`
	EnvironmentID      string              `json:"environmentId,omitempty"`
	Name               string              `json:"name"`
	ExcludeStacks      []string            `json:"excludeStacks" doc:"Stack IDs excluded from automatic updates."`
	ExcludeContainers  []string            `json:"excludeContainers" doc:"Container names; for all environments, environmentID/containerName."`
	CheckSchedule      UpdateScheduleInput `json:"checkSchedule"`
	RunSchedule        UpdateScheduleInput `json:"runSchedule"`
	Window             *UpdateWindow       `json:"window,omitempty"`
	WaitTimeoutSeconds int                 `json:"waitTimeoutSeconds"`
	Revision           int64               `json:"revision"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
}

func newEnvironmentUpdatePolicy(p domain.EnvironmentUpdatePolicy) environmentUpdatePolicy {
	scope := "environment"
	if p.EnvironmentID == "" {
		scope = "all"
	}
	out := environmentUpdatePolicy{ID: p.ID, Scope: scope, EnvironmentID: p.EnvironmentID, Name: p.Name,
		ExcludeStacks: p.ExcludeStacks, ExcludeContainers: p.ExcludeContainers,
		CheckSchedule:      UpdateScheduleInput{Cron: p.Check.Cron, TimeZone: p.Check.TimeZone, Enabled: p.Check.Enabled},
		RunSchedule:        UpdateScheduleInput{Cron: p.Run.Cron, TimeZone: p.Run.TimeZone, Enabled: p.Run.Enabled},
		WaitTimeoutSeconds: p.WaitTimeoutSeconds, Revision: p.Revision, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if out.ExcludeStacks == nil {
		out.ExcludeStacks = []string{}
	}
	if out.ExcludeContainers == nil {
		out.ExcludeContainers = []string{}
	}
	if p.Window != nil {
		out.Window = &UpdateWindow{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End}
	}
	return out
}

type environmentPolicyBody struct {
	Scope              string               `json:"scope" enum:"all,environment"`
	EnvironmentID      string               `json:"environmentId,omitempty" maxLength:"64"`
	Name               string               `json:"name" minLength:"1" maxLength:"100"`
	ExcludeStacks      []string             `json:"excludeStacks,omitempty" maxItems:"256"`
	ExcludeContainers  []string             `json:"excludeContainers,omitempty" maxItems:"256"`
	CheckSchedule      *UpdateScheduleInput `json:"checkSchedule,omitempty"`
	RunSchedule        *UpdateScheduleInput `json:"runSchedule,omitempty"`
	Window             *UpdateWindow        `json:"window,omitempty"`
	WaitTimeoutSeconds int                  `json:"waitTimeoutSeconds,omitempty" minimum:"0" maximum:"3600"`
}

func (b environmentPolicyBody) input() (updates.NewEnvironmentPolicy, error) {
	if b.Scope != "all" && b.Scope != "environment" {
		return updates.NewEnvironmentPolicy{}, Invalid("invalid update scope", Field("body.scope", "choose all or environment"))
	}
	if b.Scope == "environment" && b.EnvironmentID == "" {
		return updates.NewEnvironmentPolicy{}, Invalid("invalid update scope", Field("body.environmentId", "choose an environment"))
	}
	if b.Scope == "all" && b.EnvironmentID != "" {
		return updates.NewEnvironmentPolicy{}, Invalid("invalid update scope", Field("body.environmentId", "leave empty for all environments"))
	}
	return updates.NewEnvironmentPolicy{EnvironmentID: b.EnvironmentID, Name: b.Name,
		ExcludeStacks: b.ExcludeStacks, ExcludeContainers: b.ExcludeContainers,
		Check: scheduleInput(b.CheckSchedule), Run: scheduleInput(b.RunSchedule), Window: windowInput(b.Window),
		WaitTimeoutSeconds: b.WaitTimeoutSeconds}, nil
}

type environmentPolicyID struct {
	PolicyID string `path:"policyId" maxLength:"64"`
}
type environmentPolicyListOutput struct{ Body Page[environmentUpdatePolicy] }
type environmentPolicyOutput struct {
	ETagHeader
	Body environmentUpdatePolicy
}
type createEnvironmentPolicyInput struct{ Body environmentPolicyBody }
type updateEnvironmentPolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
	IfMatchParam
	Body environmentPolicyBody
}
type deleteEnvironmentPolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
	IfMatchParam
}
type environmentPolicyCheckInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
	IdempotencyKeyParam
}
type environmentPolicyRunInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
	IdempotencyKeyParam
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" example:"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`
	}
}
type environmentPolicyJobsOutput struct {
	Body struct {
		Jobs []Job `json:"jobs"`
	}
}

type environmentTarget struct {
	PolicyID         string              `json:"policyId" example:"0190a6e0-1122-7788-aabb-ccddeeff0011"`
	EnvironmentID    string              `json:"environmentId"`
	Type             string              `json:"type" enum:"stack,container"`
	ID               string              `json:"id"`
	CandidateSummary UpdatePolicySummary `json:"candidateSummary"`
	Inactive         bool                `json:"inactive"`
}
type environmentTargetsOutput struct {
	Body struct {
		Items []environmentTarget `json:"items"`
	}
}
type environmentPreviewTarget struct {
	PolicyID      string            `json:"policyId"`
	EnvironmentID string            `json:"environmentId"`
	Type          string            `json:"type" enum:"stack,container"`
	ID            string            `json:"id"`
	Items         []UpdateCandidate `json:"items"`
	SourceDrift   bool              `json:"sourceDrift"`
}
type environmentPreviewOutput struct {
	Body struct {
		Fingerprint string                     `json:"fingerprint"`
		Targets     []environmentPreviewTarget `json:"targets"`
	}
}

func (h *environmentUpdatesAPI) checker(ctx context.Context) (authz.Checker, authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, err
	}
	if h.svc == nil {
		return nil, p, Unavailable(CodeUnavailable, "updates are not available")
	}
	return c, p, nil
}

func (h *environmentUpdatesAPI) allowed(c authz.Checker, p domain.EnvironmentUpdatePolicy, capability string) bool {
	if p.EnvironmentID == "" {
		return c.Can("update_policy.manage_all", authz.Resource{Type: catalog.TypeAdministration}).Allowed
	}
	return c.Can(capability, authz.InEnvironment(catalog.TypeUpdatePolicy, p.EnvironmentID)).Allowed
}

func (h *environmentUpdatesAPI) policy(ctx context.Context, id, capability string) (authz.Principal, domain.EnvironmentUpdatePolicy, error) {
	c, pr, err := h.checker(ctx)
	if err != nil {
		return pr, domain.EnvironmentUpdatePolicy{}, err
	}
	p, err := h.svc.GetEnvironmentPolicy(ctx, id)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return pr, p, NotFound("update policy not found")
	}
	if err != nil {
		return pr, p, Internal(err)
	}
	if !h.allowed(c, p, "update_policy.read") {
		return pr, p, NotFound("update policy not found")
	}
	if !h.allowed(c, p, capability) {
		return pr, p, Forbidden("not permitted: " + capability)
	}
	return pr, p, nil
}

func environmentPolicyError(err error) error {
	if errors.Is(err, domain.ErrUpdateScopeOverlap) {
		return Conflict("update_scope_overlap", "an update policy already covers this environment")
	}
	return updateError(err)
}

func (h *environmentUpdatesAPI) list(ctx context.Context, _ *struct{}) (*environmentPolicyListOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	policies, err := h.svc.ListEnvironmentPolicies(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	items := make([]environmentUpdatePolicy, 0, len(policies))
	for _, p := range policies {
		if h.allowed(c, p, "update_policy.read") {
			items = append(items, newEnvironmentUpdatePolicy(p))
		}
	}
	return &environmentPolicyListOutput{Body: NewPage(items, "", nil)}, nil
}

func (h *environmentUpdatesAPI) create(ctx context.Context, in *createEnvironmentPolicyInput) (*environmentPolicyOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	np, err := in.Body.input()
	if err != nil {
		return nil, err
	}
	if !h.allowed(c, domain.EnvironmentUpdatePolicy{EnvironmentID: np.EnvironmentID}, "update_policy.manage") {
		return nil, Forbidden("not permitted to manage updates in this scope")
	}
	p, err := h.svc.CreateEnvironmentPolicy(ctx, np)
	if err != nil {
		return nil, environmentPolicyError(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID})
	return &environmentPolicyOutput{ETagHeader: ETagHeader{ETag: RevisionETag(p.Revision)}, Body: newEnvironmentUpdatePolicy(p)}, nil
}

func (h *environmentUpdatesAPI) get(ctx context.Context, in *environmentPolicyID) (*environmentPolicyOutput, error) {
	_, p, err := h.policy(ctx, in.PolicyID, "update_policy.read")
	if err != nil {
		return nil, err
	}
	return &environmentPolicyOutput{ETagHeader: ETagHeader{ETag: RevisionETag(p.Revision)}, Body: newEnvironmentUpdatePolicy(p)}, nil
}

func (h *environmentUpdatesAPI) update(ctx context.Context, in *updateEnvironmentPolicyInput) (*environmentPolicyOutput, error) {
	_, p, err := h.policy(ctx, in.PolicyID, "update_policy.manage")
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	np, err := in.Body.input()
	if err != nil {
		return nil, err
	}
	updated, err := h.svc.UpdateEnvironmentPolicy(ctx, p.ID, p.Revision, np)
	if err != nil {
		return nil, environmentPolicyError(err)
	}
	return &environmentPolicyOutput{ETagHeader: ETagHeader{ETag: RevisionETag(updated.Revision)}, Body: newEnvironmentUpdatePolicy(updated)}, nil
}

func (h *environmentUpdatesAPI) delete(ctx context.Context, in *deleteEnvironmentPolicyInput) (*struct{}, error) {
	_, p, err := h.policy(ctx, in.PolicyID, "update_policy.manage")
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	if err := h.svc.DeleteEnvironmentPolicy(ctx, p.ID, p.Revision); err != nil {
		return nil, environmentPolicyError(err)
	}
	return &struct{}{}, nil
}

func candidateSummary(candidates []domain.UpdateCandidate) UpdatePolicySummary {
	s := UpdatePolicySummary{}
	for _, c := range candidates {
		switch c.Status {
		case domain.CandidateAvailable:
			s.Available++
		case domain.CandidateUpToDate:
			s.UpToDate++
		case domain.CandidateUnchecked:
			s.Unchecked++
		case domain.CandidateIneligible:
			s.Ineligible++
		case domain.CandidateQuarantined:
			s.Quarantined++
		case domain.CandidateCheckFailed, domain.CandidateRunFailed:
			s.Failed++
		}
		if c.CheckedAt != nil && (s.LastCheckAt == nil || c.CheckedAt.After(*s.LastCheckAt)) {
			s.LastCheckAt = c.CheckedAt
		}
	}
	return s
}

func (h *environmentUpdatesAPI) targets(ctx context.Context, in *environmentPolicyID) (*environmentTargetsOutput, error) {
	_, p, err := h.policy(ctx, in.PolicyID, "update_policy.read")
	if err != nil {
		return nil, err
	}
	children, err := h.svc.ManagedPolicies(ctx, p.ID)
	if err != nil {
		return nil, Internal(err)
	}
	out := &environmentTargetsOutput{}
	out.Body.Items = []environmentTarget{}
	for _, child := range children {
		candidates, err := h.svc.Candidates(ctx, child.ID)
		if err != nil {
			return nil, Internal(err)
		}
		out.Body.Items = append(out.Body.Items, environmentTarget{PolicyID: child.ID, EnvironmentID: child.EnvironmentID,
			Type: string(child.TargetType), ID: child.TargetID, CandidateSummary: candidateSummary(candidates), Inactive: child.Inactive})
	}
	return out, nil
}

func jobBatch(jobs []domain.Job) *environmentPolicyJobsOutput {
	out := &environmentPolicyJobsOutput{}
	out.Body.Jobs = make([]Job, 0, len(jobs))
	for _, job := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, NewJob(job))
	}
	return out
}

func (h *environmentUpdatesAPI) check(ctx context.Context, in *environmentPolicyCheckInput) (*environmentPolicyJobsOutput, error) {
	pr, p, err := h.policy(ctx, in.PolicyID, "update.check")
	if err != nil {
		return nil, err
	}
	jobs, err := h.svc.CheckEnvironment(ctx, pr, p.ID, in.IdempotencyKey)
	if err != nil {
		return nil, JobErrorFor(err)
	}
	return jobBatch(jobs), nil
}

func (h *environmentUpdatesAPI) preview(ctx context.Context, in *environmentPolicyID) (*environmentPreviewOutput, error) {
	_, p, err := h.policy(ctx, in.PolicyID, "update.check")
	if err != nil {
		return nil, err
	}
	preview, err := h.svc.PreviewEnvironment(ctx, p.ID)
	if err != nil {
		return nil, environmentPolicyError(err)
	}
	out := &environmentPreviewOutput{}
	out.Body.Fingerprint, out.Body.Targets = preview.Fingerprint, []environmentPreviewTarget{}
	for _, target := range preview.Targets {
		t := environmentPreviewTarget{PolicyID: target.Policy.ID, EnvironmentID: target.Policy.EnvironmentID,
			Type: string(target.Policy.TargetType), ID: target.Policy.TargetID, SourceDrift: target.Preview.SourceDrift,
			Items: []UpdateCandidate{}}
		for _, item := range target.Preview.Items {
			t.Items = append(t.Items, newUpdateCandidate(item.Candidate))
		}
		out.Body.Targets = append(out.Body.Targets, t)
	}
	return out, nil
}

func (h *environmentUpdatesAPI) run(ctx context.Context, in *environmentPolicyRunInput) (*environmentPolicyJobsOutput, error) {
	pr, p, err := h.policy(ctx, in.PolicyID, "update.run")
	if err != nil {
		return nil, err
	}
	jobs, err := h.svc.RunEnvironment(ctx, pr, p.ID, in.Body.Fingerprint, in.IdempotencyKey)
	if err != nil {
		return nil, environmentPolicyError(err)
	}
	return jobBatch(jobs), nil
}

func registerEnvironmentUpdates(a huma.API, deps Deps) {
	svc, _ := deps.Updates.(environmentUpdateService)
	h := &environmentUpdatesAPI{svc: svc, authz: authz.OrDenyAll(deps.Authorizer)}
	base := BasePath + "/environment-update-policies"
	one := base + "/{policyId}"
	read := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable}
	mutate := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable}
	Register(a, Operation{Operation: huma.Operation{OperationID: "list-environment-update-policies", Method: http.MethodGet, Path: base, Summary: "List environment update policies", Tags: []string{tagUpdates}, Errors: read}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.list)
	Register(a, Operation{Operation: huma.Operation{OperationID: "create-environment-update-policy", Method: http.MethodPost, Path: base, Summary: "Create an environment update policy", Description: "Covers all environments or one environment, including future eligible stacks and Docker Manager-managed standalone containers. Scopes cannot overlap; schedules start disabled unless enabled.", Tags: []string{tagUpdates}, DefaultStatus: http.StatusCreated, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.create)
	Register(a, Operation{Operation: huma.Operation{OperationID: "get-environment-update-policy", Method: http.MethodGet, Path: one, Summary: "Get an environment update policy", Tags: []string{tagUpdates}, Errors: read}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.get)
	Register(a, Operation{Operation: huma.Operation{OperationID: "update-environment-update-policy", Method: http.MethodPatch, Path: one, Summary: "Edit an environment update policy", Tags: []string{tagUpdates}, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.update)
	Register(a, Operation{Operation: huma.Operation{OperationID: "delete-environment-update-policy", Method: http.MethodDelete, Path: one, Summary: "Delete an environment update policy", Tags: []string{tagUpdates}, DefaultStatus: http.StatusNoContent, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.delete)
	Register(a, Operation{Operation: huma.Operation{OperationID: "list-environment-update-targets", Method: http.MethodGet, Path: one + "/targets", Summary: "List covered update targets", Tags: []string{tagUpdates}, Errors: read}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.targets)
	Register(a, Operation{Operation: huma.Operation{OperationID: "check-environment-update-policy", Method: http.MethodPost, Path: one + "/checks", Summary: "Check every covered target for image updates", Tags: []string{tagUpdates}, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone, Idempotency: IdempotencyStored}, h.check)
	Register(a, Operation{Operation: huma.Operation{OperationID: "preview-environment-update-policy", Method: http.MethodPost, Path: one + "/previews", Summary: "Preview updates across an environment policy", Tags: []string{tagUpdates}, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone}, h.preview)
	Register(a, Operation{Operation: huma.Operation{OperationID: "run-environment-update-policy", Method: http.MethodPost, Path: one + "/runs", Summary: "Apply previewed updates across an environment policy", Tags: []string{tagUpdates}, Errors: mutate}, Capability: CapabilityAuthenticated, Scope: ScopeNone, Idempotency: IdempotencyStored}, h.run)
}
