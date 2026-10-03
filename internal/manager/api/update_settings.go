package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
)

// The updates setup (#20, #240): one instance-wide setup covers every
// stack and Docker Manager-managed standalone container of every
// environment except what it leaves out. Its target records stay
// read-only under /update-policies.

// updateSetupService is the setup layer of updates (implemented by
// *updates.Service).
type updateSetupService interface {
	Setup(context.Context) (domain.UpdateSetup, error)
	Environments(context.Context) ([]domain.Environment, error)
	UpdateSetup(context.Context, int64, updates.SetupChange) (domain.UpdateSetup, domain.UpdateSetup, error)
	Targets(context.Context) ([]updates.ManagedTarget, error)
	CheckSetup(context.Context, authz.Principal, string) ([]domain.Job, error)
	PreviewSetup(context.Context) (updates.SetupPreview, error)
	RunSetup(context.Context, authz.Principal, string, string) ([]domain.Job, error)
	Candidates(context.Context, string) ([]domain.UpdateCandidate, error)
}

type updateSettingsAPI struct {
	svc   updateSetupService
	authz authz.Authorizer
}

// UpdateSettings is the updates setup.
type UpdateSettings struct {
	ID                  string              `json:"id" doc:"The policy ID of the setup's schedules and jobs, and the parentId of its target records."`
	ExcludeEnvironments []string            `json:"excludeEnvironments" doc:"IDs of the environments left out; every other environment is covered, also ones added later."`
	ExcludeStacks       []string            `json:"excludeStacks" doc:"Stack IDs left out."`
	ExcludeContainers   []string            `json:"excludeContainers" doc:"Standalone containers left out, as environmentID/containerName."`
	CheckSchedule       UpdateScheduleInput `json:"checkSchedule" doc:"Check Automatically: when to look for newer images."`
	RunSchedule         UpdateScheduleInput `json:"runSchedule" doc:"Update Automatically: when to apply what the last check found."`
	Window              *UpdateWindow       `json:"window,omitempty"`
	WaitTimeoutSeconds  int                 `json:"waitTimeoutSeconds"`
	Actions             []string            `json:"actions" doc:"What the caller may do: update_policy.manage, update.check, update.run."`
	Revision            int64               `json:"revision"`
	UpdatedAt           time.Time           `json:"updatedAt"`
}

func newUpdateSettings(p domain.UpdateSetup, v authz.View) UpdateSettings {
	out := UpdateSettings{ID: p.ID, ExcludeEnvironments: orEmptyList(p.ExcludeEnvironments), ExcludeStacks: orEmptyList(p.ExcludeStacks),
		ExcludeContainers:  orEmptyList(p.ExcludeContainers),
		CheckSchedule:      UpdateScheduleInput{Cron: p.Check.Cron, TimeZone: p.Check.TimeZone, Enabled: p.Check.Enabled},
		RunSchedule:        UpdateScheduleInput{Cron: p.Run.Cron, TimeZone: p.Run.TimeZone, Enabled: p.Run.Enabled},
		WaitTimeoutSeconds: p.WaitTimeoutSeconds, Actions: Actions(v), Revision: p.Revision, UpdatedAt: p.UpdatedAt}
	if p.Window != nil {
		out.Window = &UpdateWindow{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End}
	}
	return out
}

func orEmptyList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// flatUpdateSetup is the audit diff form of the setup.
func flatUpdateSetup(p domain.UpdateSetup) map[string]any {
	return map[string]any{"excludeEnvironments": p.ExcludeEnvironments, "excludeStacks": p.ExcludeStacks,
		"excludeContainers": p.ExcludeContainers, "checkSchedule": p.Check, "runSchedule": p.Run, "window": p.Window,
		"waitTimeoutSeconds": p.WaitTimeoutSeconds}
}

// updateSetupResource is the setup as an authorization resource:
// instance-wide, so only instance grants apply to it.
func updateSetupResource(p domain.UpdateSetup) authz.Resource {
	return authz.Resource{Type: catalog.TypeUpdatePolicy, ID: p.ID, Parents: []authz.ResourceRef{}}
}

type updateSettingsOutput struct {
	ETagHeader
	Body UpdateSettings
}
type updateUpdateSettingsInput struct {
	IfMatchParam
	Body struct {
		ExcludeEnvironments *[]string            `json:"excludeEnvironments,omitempty" maxItems:"256" doc:"The environments to leave out (replaces the list); IDs of environments that do not exist are dropped."`
		ExcludeStacks       *[]string            `json:"excludeStacks,omitempty" maxItems:"256" doc:"The stacks to leave out (replaces the list)."`
		ExcludeContainers   *[]string            `json:"excludeContainers,omitempty" maxItems:"256" doc:"The standalone containers to leave out, environmentID/containerName (replaces the list)."`
		CheckSchedule       *UpdateScheduleInput `json:"checkSchedule,omitempty"`
		RunSchedule         *UpdateScheduleInput `json:"runSchedule,omitempty"`
		Window              *UpdateWindow        `json:"window,omitempty" doc:"Restricts scheduled runs (replaces the window)."`
		ClearWindow         bool                 `json:"clearWindow,omitempty" doc:"Remove the update window: scheduled runs may start at any time."`
		WaitTimeoutSeconds  *int                 `json:"waitTimeoutSeconds,omitempty" minimum:"0" maximum:"3600" doc:"How long a run waits for an updated container to become healthy (0: the agent's default)."`
	}
}
type updateSettingsCheckInput struct {
	IdempotencyKeyParam
}
type updateSettingsRunInput struct {
	IdempotencyKeyParam
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" example:"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`
	}
}
type updateSettingsJobsOutput struct {
	Body struct {
		Jobs []Job `json:"jobs"`
	}
}

// UpdateSettingsTarget is one target record of the updates setup.
type UpdateSettingsTarget struct {
	PolicyID         string              `json:"policyId" example:"0190a6e0-1122-7788-aabb-ccddeeff0011"`
	EnvironmentID    string              `json:"environmentId"`
	Type             string              `json:"type" enum:"stack,container"`
	ID               string              `json:"id"`
	CandidateSummary UpdatePolicySummary `json:"candidateSummary"`
	Inactive         bool                `json:"inactive" doc:"The setup no longer covers the target; the record stays for its history."`
	InactiveReason   string              `json:"inactiveReason,omitempty" enum:"excluded,missing" doc:"Why an inactive target is not covered: excluded (the setup leaves it or its environment out, or the container has the docker-manager.update.exclude=true label) or missing (the stack or container no longer exists or no longer qualifies)."`
}
type updateTargetsOutput struct {
	Body struct {
		Items []UpdateSettingsTarget `json:"items"`
	}
}

// UpdateSettingsPreviewTarget is one target of an updates preview.
type UpdateSettingsPreviewTarget struct {
	PolicyID      string            `json:"policyId"`
	EnvironmentID string            `json:"environmentId"`
	Type          string            `json:"type" enum:"stack,container"`
	ID            string            `json:"id"`
	Items         []UpdateCandidate `json:"items"`
	SourceDrift   bool              `json:"sourceDrift"`
}
type updateSettingsPreviewOutput struct {
	Body struct {
		Fingerprint string                        `json:"fingerprint"`
		Targets     []UpdateSettingsPreviewTarget `json:"targets"`
	}
}

// setup loads the setup and the caller's view of it; capability must be
// granted on the instance (403 otherwise).
func (h *updateSettingsAPI) setup(ctx context.Context, capability string) (authz.Principal, domain.UpdateSetup, authz.View, error) {
	_, p, st, v, err := h.checked(ctx, capability)
	return p, st, v, err
}

func (h *updateSettingsAPI) checked(ctx context.Context, capability string) (authz.Checker, authz.Principal, domain.UpdateSetup, authz.View, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, domain.UpdateSetup{}, authz.View{}, err
	}
	if h.svc == nil {
		return nil, p, domain.UpdateSetup{}, authz.View{}, Unavailable(CodeUnavailable, "updates are not available")
	}
	st, err := h.svc.Setup(ctx)
	if err != nil {
		return nil, p, st, authz.View{}, Internal(err)
	}
	if !c.Can(capability, updateSetupResource(st)).Allowed {
		return nil, p, st, authz.View{}, Forbidden("requires " + capability + " on all environments")
	}
	return c, p, st, authz.ViewOf(c, updateSetupResource(st)), nil
}

// covering loads the setup like setup, and also refuses when a rule keeps
// the caller from capability in one of the environments the setup covers
// (a deny rule on the environment): checks, previews and runs act on
// (and show) their stacks and containers.
func (h *updateSettingsAPI) covering(ctx context.Context, capability string) (authz.Principal, error) {
	c, p, st, _, err := h.checked(ctx, capability)
	if err != nil {
		return p, err
	}
	envs, err := h.svc.Environments(ctx)
	if err != nil {
		return p, Internal(err)
	}
	for _, env := range envs {
		in := authz.Resource{Type: catalog.TypeUpdatePolicy, ID: st.ID, EnvironmentID: env.ID, Parents: []authz.ResourceRef{}}
		if !c.Can(capability, in).Allowed {
			return p, Forbidden("not permitted: " + capability + " in environment " + env.Name +
				"; leave it out of the update settings or ask for access there")
		}
	}
	return p, nil
}

func (h *updateSettingsAPI) get(ctx context.Context, _ *struct{}) (*updateSettingsOutput, error) {
	_, st, v, err := h.setup(ctx, string(CapUpdatePolicyRead))
	if err != nil {
		return nil, err
	}
	return &updateSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)}, Body: newUpdateSettings(st, v)}, nil
}

func (h *updateSettingsAPI) update(ctx context.Context, in *updateUpdateSettingsInput) (*updateSettingsOutput, error) {
	_, st, v, err := h.setup(ctx, string(CapUpdatePolicyManage))
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(st.Revision)); err != nil {
		return nil, err
	}
	b := in.Body
	before, after, err := h.svc.UpdateSetup(ctx, st.Revision, updates.SetupChange{ExcludeEnvironments: b.ExcludeEnvironments,
		ExcludeStacks: b.ExcludeStacks, ExcludeContainers: b.ExcludeContainers, Check: scheduleInput(b.CheckSchedule),
		Run: scheduleInput(b.RunSchedule), Window: windowInput(b.Window), ClearWindow: b.ClearWindow, WaitTimeoutSeconds: b.WaitTimeoutSeconds})
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, stale(before.Revision)
	}
	if err != nil {
		return nil, updateError(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeUpdatePolicy, ID: after.ID})
	audit.SetDiff(ctx, flatUpdateSetup(before), flatUpdateSetup(after))
	return &updateSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: newUpdateSettings(after, v)}, nil
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

func (h *updateSettingsAPI) targets(ctx context.Context, _ *struct{}) (*updateTargetsOutput, error) {
	if _, _, _, err := h.setup(ctx, string(CapUpdatePolicyRead)); err != nil {
		return nil, err
	}
	children, err := h.svc.Targets(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	out := &updateTargetsOutput{}
	out.Body.Items = []UpdateSettingsTarget{}
	for _, t := range children {
		child := t.Policy
		candidates, err := h.svc.Candidates(ctx, child.ID)
		if err != nil {
			return nil, Internal(err)
		}
		out.Body.Items = append(out.Body.Items, UpdateSettingsTarget{PolicyID: child.ID, EnvironmentID: child.EnvironmentID,
			Type: string(child.TargetType), ID: child.TargetID, CandidateSummary: candidateSummary(candidates),
			Inactive: child.Inactive, InactiveReason: t.InactiveReason})
	}
	return out, nil
}

func jobBatch(jobs []domain.Job) *updateSettingsJobsOutput {
	out := &updateSettingsJobsOutput{}
	out.Body.Jobs = make([]Job, 0, len(jobs))
	for _, job := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, NewJob(job))
	}
	return out
}

func (h *updateSettingsAPI) check(ctx context.Context, in *updateSettingsCheckInput) (*updateSettingsJobsOutput, error) {
	pr, err := h.covering(ctx, string(CapUpdateCheck))
	if err != nil {
		return nil, err
	}
	jobs, err := h.svc.CheckSetup(ctx, pr, in.IdempotencyKey)
	if err != nil {
		return nil, JobErrorFor(err)
	}
	return jobBatch(jobs), nil
}

func (h *updateSettingsAPI) preview(ctx context.Context, _ *struct{}) (*updateSettingsPreviewOutput, error) {
	if _, err := h.covering(ctx, string(CapUpdateCheck)); err != nil {
		return nil, err
	}
	preview, err := h.svc.PreviewSetup(ctx)
	if err != nil {
		return nil, updateError(err)
	}
	out := &updateSettingsPreviewOutput{}
	out.Body.Fingerprint, out.Body.Targets = preview.Fingerprint, []UpdateSettingsPreviewTarget{}
	for _, target := range preview.Targets {
		t := UpdateSettingsPreviewTarget{PolicyID: target.Policy.ID, EnvironmentID: target.Policy.EnvironmentID,
			Type: string(target.Policy.TargetType), ID: target.Policy.TargetID, SourceDrift: target.Preview.SourceDrift,
			Items: []UpdateCandidate{}}
		for _, item := range target.Preview.Items {
			t.Items = append(t.Items, newUpdateCandidate(item.Candidate))
		}
		out.Body.Targets = append(out.Body.Targets, t)
	}
	return out, nil
}

func (h *updateSettingsAPI) run(ctx context.Context, in *updateSettingsRunInput) (*updateSettingsJobsOutput, error) {
	pr, err := h.covering(ctx, string(CapUpdateRun))
	if err != nil {
		return nil, err
	}
	jobs, err := h.svc.RunSetup(ctx, pr, in.Body.Fingerprint, in.IdempotencyKey)
	if err != nil {
		return nil, updateError(err)
	}
	return jobBatch(jobs), nil
}

func registerUpdateSettings(a huma.API, deps Deps) {
	svc, _ := deps.Updates.(updateSetupService)
	h := &updateSettingsAPI{svc: svc, authz: authz.OrDenyAll(deps.Authorizer)}
	base := BasePath + "/update-settings"
	read := []int{http.StatusForbidden, http.StatusServiceUnavailable}
	mutate := []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable}
	Register(a, Operation{Operation: huma.Operation{OperationID: "get-update-settings", Method: http.MethodGet, Path: base,
		Summary: "Get the update settings", Description: "The one updates setup: Check Automatically and Update Automatically " +
			"with their schedules, the update window, the health wait and what it leaves out. It covers every Docker Manager stack " +
			"and managed standalone container of every other environment, also ones added later.",
		Tags: []string{tagUpdates}, Errors: read}, Capability: CapUpdatePolicyRead, Scope: ScopeInstance}, h.get)
	Register(a, Operation{Operation: huma.Operation{OperationID: "update-update-settings", Method: http.MethodPatch, Path: base,
		Summary: "Change the update settings", Description: "Fields given replace their value. Requires If-Match.",
		Tags: []string{tagUpdates}, Errors: append(mutate, http.StatusPreconditionFailed, http.StatusPreconditionRequired)},
		Capability: CapUpdatePolicyManage, Scope: ScopeInstance}, h.update)
	Register(a, Operation{Operation: huma.Operation{OperationID: "list-update-targets", Method: http.MethodGet, Path: base + "/targets",
		Summary: "List the update targets", Description: "Every target record of the setup, covered or not (inactive, with the reason), " +
			"with its candidate summary.", Tags: []string{tagUpdates}, Errors: read},
		Capability: CapUpdatePolicyRead, Scope: ScopeInstance}, h.targets)
	Register(a, Operation{Operation: huma.Operation{OperationID: "create-update-check", Method: http.MethodPost, Path: base + "/checks",
		Summary: "Check every covered target for image updates", Tags: []string{tagUpdates}, Errors: mutate},
		Capability: CapUpdateCheck, Scope: ScopeInstance, Idempotency: IdempotencyStored}, h.check)
	Register(a, Operation{Operation: huma.Operation{OperationID: "create-update-preview", Method: http.MethodPost, Path: base + "/previews",
		Summary: "Preview the updates of every covered target", Tags: []string{tagUpdates}, Errors: mutate},
		Capability: CapUpdateCheck, Scope: ScopeInstance}, h.preview)
	Register(a, Operation{Operation: huma.Operation{OperationID: "create-update-run", Method: http.MethodPost, Path: base + "/runs",
		Summary: "Apply the previewed updates of every covered target", Description: "Needs the fingerprint of the preview: 409 " +
			"update_preview_stale when the plan changed since.", Tags: []string{tagUpdates}, Errors: mutate},
		Capability: CapUpdateRun, Scope: ScopeInstance, Idempotency: IdempotencyStored}, h.run)
}
