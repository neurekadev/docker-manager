package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/imageref"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates/eligible"
)

// Digest-driven updates (#20): update policies, checks, candidates,
// previews and runs, and the image status of a standalone container. The
// flows live in internal/manager/updates.

const tagUpdates = "Updates"

// Update capabilities (#17).
const (
	CapUpdatePolicyRead   Capability = "update_policy.read"
	CapUpdatePolicyManage Capability = "update_policy.manage"
	CapUpdateCheck        Capability = "update.check"
	CapUpdateRun          Capability = "update.run"
)

// Update error codes.
const (
	CodeUpdatePolicyTargetUsed = "update_policy_target_used"
	CodeUpdatePolicyNameTaken  = "update_policy_name_taken"
	CodeUpdateTargetIneligible = domain.UpdateErrTargetIneligible
	CodeNoUpdateCandidates     = domain.UpdateErrNoCandidates
	CodeUpdateSourceDrift      = domain.UpdateErrSourceDrift
	CodeUpdatePreviewStale     = domain.UpdateErrPreviewStale
)

// UpdateService is the update service as seen by the API (implemented by
// *updates.Service).
type UpdateService interface {
	Get(ctx context.Context, id string) (domain.UpdatePolicy, error)
	List(ctx context.Context, environmentID, afterID string, limit int) ([]domain.UpdatePolicy, error)
	Create(ctx context.Context, np updates.NewPolicy) (domain.UpdatePolicy, error)
	Update(ctx context.Context, id string, revision int64, patch domain.UpdatePolicyPatch) (before, after domain.UpdatePolicy, err error)
	Delete(ctx context.Context, id string, revision int64) error
	Candidates(ctx context.Context, policyID string) ([]domain.UpdateCandidate, error)
	Quarantine(ctx context.Context, policyID string) ([]domain.UpdateQuarantine, error)
	History(ctx context.Context, policyID string, limit int) ([]domain.UpdateHistoryEntry, error)
	ScheduleStatus(ctx context.Context, kind, policyID string, runs int) (updates.ScheduleStatus, error)
	StartCheck(ctx context.Context, p authz.Principal, policyID, key string) (domain.Job, error)
	Preview(ctx context.Context, policyID string, selection []string) (domain.UpdatePreview, error)
	Run(ctx context.Context, r updates.RunRequest) (domain.Job, error)
	ForTarget(ctx context.Context, environmentID string, typ domain.UpdateTargetType, id string) (*domain.UpdatePolicy, error)
}

// --- DTOs ---

// UpdateTarget is what a policy updates.
type UpdateTarget struct {
	Type string `json:"type" enum:"stack,container" doc:"stack: a Docker Manager stack's services; container: a Docker Manager-managed standalone container with a saved recreate specification."`
	ID   string `json:"id" minLength:"1" maxLength:"128" doc:"Stack ID or container name (in the policy's environment)."`
}

// UpdateScheduleInput is a check or run schedule of a policy (#13).
type UpdateScheduleInput struct {
	Cron     string `json:"cron,omitempty" maxLength:"256" example:"0 3 * * *" doc:"Five-field cron expression; default: the instance default of the kind."`
	TimeZone string `json:"timeZone,omitempty" maxLength:"64" example:"Europe/Berlin" doc:"IANA time zone; default: the instance default."`
	Enabled  bool   `json:"enabled" doc:"Automatic checks/updates happen only when enabled (default false)."`
}

// UpdateSchedule is a policy's schedule with its scheduler state.
type UpdateSchedule struct {
	Cron          string           `json:"cron"`
	TimeZone      string           `json:"timeZone"`
	Enabled       bool             `json:"enabled"`
	NextRun       *ScheduleRunTime `json:"nextRun,omitempty"`
	InvalidReason string           `json:"invalidReason,omitempty"`
	RecentRuns    []ScheduleRun    `json:"recentRuns" doc:"Newest first (at most 5)."`
}

// UpdateWindow restricts scheduled runs (in the run schedule's zone).
type UpdateWindow struct {
	Days  []int  `json:"days,omitempty" maxItems:"7" doc:"Days of the week, 0 (Sunday) to 6; empty: every day."`
	Start string `json:"start" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" example:"02:00"`
	End   string `json:"end" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" example:"05:00" doc:"Exclusive; before start spans midnight."`
}

// UpdatePolicySummary counts a policy's candidates.
type UpdatePolicySummary struct {
	Available   int        `json:"available"`
	UpToDate    int        `json:"upToDate"`
	Quarantined int        `json:"quarantined"`
	Ineligible  int        `json:"ineligible"`
	Failed      int        `json:"failed" doc:"check_failed and run_failed candidates."`
	Unchecked   int        `json:"unchecked"`
	LastCheckAt *time.Time `json:"lastCheckAt,omitempty"`
}

// UpdateQuarantined is a quarantined candidate digest.
type UpdateQuarantined struct {
	Service    string    `json:"service"`
	Digest     string    `json:"digest"`
	JobID      string    `json:"jobId,omitempty"`
	ErrorClass string    `json:"errorClass,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// UpdateHistoryEntry is one applied (or failed) digest change.
type UpdateHistoryEntry struct {
	Service              string    `json:"service"`
	Reference            string    `json:"reference"`
	RegistryConnectionID string    `json:"registryConnectionId,omitempty"`
	FromDigest           string    `json:"fromDigest,omitempty"`
	ToDigest             string    `json:"toDigest,omitempty"`
	JobID                string    `json:"jobId,omitempty"`
	Outcome              string    `json:"outcome" enum:"updated,unchanged,kept_stopped,failed"`
	ErrorClass           string    `json:"errorClass,omitempty"`
	SourceHashBefore     string    `json:"sourceHashBefore,omitempty"`
	SourceHashAfter      string    `json:"sourceHashAfter,omitempty"`
	At                   time.Time `json:"at"`
}

// UpdatePolicy is an update policy. Shaping (#17): update_policy.read
// shows it in full; other capabilities on it show id, name, environment
// and target.
type UpdatePolicy struct {
	ID                 string               `json:"id"`
	EnvironmentID      string               `json:"environmentId"`
	Name               string               `json:"name"`
	Target             UpdateTarget         `json:"target"`
	View               string               `json:"view" enum:"minimal,full"`
	Actions            []string             `json:"actions"`
	Services           []string             `json:"services,omitempty" doc:"Opted-in services (empty: every service of the stack). Full view."`
	ExcludeServices    []string             `json:"excludeServices,omitempty" doc:"Services never updated. Full view."`
	CheckSchedule      *UpdateSchedule      `json:"checkSchedule,omitempty"`
	RunSchedule        *UpdateSchedule      `json:"runSchedule,omitempty"`
	Window             *UpdateWindow        `json:"window,omitempty"`
	WaitTimeoutSeconds int                  `json:"waitTimeoutSeconds,omitempty"`
	Summary            *UpdatePolicySummary `json:"summary,omitempty"`
	Quarantine         []UpdateQuarantined  `json:"quarantine,omitempty" doc:"Quarantined candidate digests (never applied automatically). Full view."`
	RecentHistory      []UpdateHistoryEntry `json:"recentHistory,omitempty" doc:"Newest first (at most 20). Full view."`
	Revision           int64                `json:"revision,omitempty"`
	CreatedAt          time.Time            `json:"createdAt,omitzero"`
	UpdatedAt          time.Time            `json:"updatedAt,omitzero"`
}

// UpdateCandidate is the digest model of one service or of the container.
type UpdateCandidate struct {
	ID                   string     `json:"id"`
	Service              string     `json:"service" example:"web" doc:"Compose service name, or the container name."`
	Reference            string     `json:"reference" example:"nginx:1.27" doc:"The resolved tagged reference (never rewritten)."`
	Registry             string     `json:"registry,omitempty"`
	Repository           string     `json:"repository,omitempty"`
	Tag                  string     `json:"tag,omitempty"`
	Platform             string     `json:"platform,omitempty" doc:"Host platform checked (os/arch[/variant])."`
	RegistryConnectionID string     `json:"registryConnectionId,omitempty" doc:"Connection used for the last check (metadata only)."`
	Eligible             bool       `json:"eligible"`
	Reason               string     `json:"reason,omitempty" enum:"build_only,digest_pinned,untagged,pull_policy_conflict,invalid_reference,not_deployed,no_applied_digest,excluded,protected,no_recreate_spec,stack_managed"`
	ReasonMessage        string     `json:"reasonMessage,omitempty" doc:"Why it is ineligible, or the warning of a non-version tag."`
	NonVersionTag        bool       `json:"nonVersionTag" doc:"Eligible, but the tag (latest, main, ...) can change meaning."`
	Status               string     `json:"status" enum:"ineligible,unchecked,up_to_date,update_available,quarantined,check_failed,run_failed"`
	CurrentDigest        string     `json:"currentDigest,omitempty" doc:"Digest applied on the host."`
	CurrentImageID       string     `json:"currentImageId,omitempty"`
	PreviousDigest       string     `json:"previousDigest,omitempty" doc:"Digest before the last update."`
	CandidateDigest      string     `json:"candidateDigest,omitempty" doc:"The registry's host-platform manifest digest."`
	CandidateIndexDigest string     `json:"candidateIndexDigest,omitempty" doc:"The tag's index digest (multi-platform images)."`
	ErrorClass           string     `json:"errorClass,omitempty" doc:"unauthorized, forbidden, rate_limited, registry_unavailable, not_found, platform_not_found, ambiguous_registry_connection, registry_connection_revoked, or a run's job error class."`
	ErrorMessage         string     `json:"errorMessage,omitempty"`
	RetryAfterSeconds    int        `json:"retryAfterSeconds,omitempty"`
	Guidance             string     `json:"guidance,omitempty" doc:"Manual recovery of a quarantined or failed candidate."`
	CheckedAt            *time.Time `json:"checkedAt,omitempty"`
	CheckJobID           string     `json:"checkJobId,omitempty"`
	SourceHashBefore     string     `json:"sourceHashBefore,omitempty" doc:"The stack's definition hash read before the last check."`
	SourceHashAfter      string     `json:"sourceHashAfter,omitempty"`
}

func newUpdateCandidate(c domain.UpdateCandidate) UpdateCandidate {
	out := UpdateCandidate{ID: c.ID, Service: c.Service, Reference: c.Reference, Registry: c.Registry, Repository: c.Repository, Tag: c.Tag,
		Platform: c.Platform, RegistryConnectionID: c.RegistryConnectionID, Eligible: c.Eligible, Reason: c.Reason,
		ReasonMessage: c.ReasonMessage, NonVersionTag: c.NonVersionTag, Status: string(c.Status), CurrentDigest: c.AppliedDigest,
		CurrentImageID: c.AppliedImageID, PreviousDigest: c.PreviousDigest, CandidateDigest: c.CandidateDigest,
		CandidateIndexDigest: c.CandidateIndexDigest, ErrorClass: c.ErrorClass, ErrorMessage: c.ErrorMessage,
		RetryAfterSeconds: c.RetryAfterSeconds, CheckedAt: c.CheckedAt, CheckJobID: c.CheckJobID, SourceHashBefore: c.SourceHashBefore,
		SourceHashAfter: c.SourceHashAfter}
	if c.Status == domain.CandidateQuarantined || c.Status == domain.CandidateRunFailed {
		out.Guidance = "There is no automatic rollback. To go back or stay on a known image, pin a digest in your own definition"
		if r, err := imageref.Parse(c.Reference); err == nil && c.AppliedDigest != "" {
			out.Guidance += " (for example image: " + r.Name() + "@" + c.AppliedDigest + ")"
		}
		out.Guidance += " and deploy it; Docker Manager never edits your Compose or env files."
	}
	return out
}

func updatePolicyResource(p domain.UpdatePolicy) authz.Resource { return updates.Resource(p) }

func newUpdateSchedule(p domain.UpdateSchedule, st updates.ScheduleStatus) *UpdateSchedule {
	out := &UpdateSchedule{Cron: p.Cron, TimeZone: p.TimeZone, Enabled: p.Enabled, RecentRuns: []ScheduleRun{}}
	if st.Known {
		out.InvalidReason = st.Schedule.InvalidReason
		if st.NextRun != nil {
			rt := newRunTime(*st.NextRun)
			out.NextRun = &rt
		}
		for _, r := range st.Runs {
			out.RecentRuns = append(out.RecentRuns, newScheduleRun(r))
		}
	}
	return out
}

func (h *updatesAPI) newPolicy(ctx context.Context, p domain.UpdatePolicy, v authz.View) (UpdatePolicy, error) {
	out := UpdatePolicy{ID: p.ID, EnvironmentID: p.EnvironmentID, Name: p.Name, Target: UpdateTarget{Type: string(p.TargetType), ID: p.TargetID},
		View: v.Level.String(), Actions: Actions(v)}
	if v.Has(string(CapUpdatePolicyManage)) {
		out.Revision = p.Revision
	}
	if !v.Full() {
		return out, nil
	}
	out.Services, out.ExcludeServices = p.Services, p.ExcludeServices
	out.WaitTimeoutSeconds, out.Revision, out.CreatedAt, out.UpdatedAt = p.WaitTimeoutSeconds, p.Revision, p.CreatedAt, p.UpdatedAt
	if p.Window != nil {
		out.Window = &UpdateWindow{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End}
	}
	check, err := h.svc.ScheduleStatus(ctx, scheduler.KindUpdateCheck, p.ID, 5)
	if err != nil {
		return out, err
	}
	run, err := h.svc.ScheduleStatus(ctx, scheduler.KindUpdateRun, p.ID, 5)
	if err != nil {
		return out, err
	}
	out.CheckSchedule, out.RunSchedule = newUpdateSchedule(p.Check, check), newUpdateSchedule(p.Run, run)
	cands, err := h.svc.Candidates(ctx, p.ID)
	if err != nil {
		return out, err
	}
	sum := &UpdatePolicySummary{}
	for _, c := range cands {
		switch c.Status {
		case domain.CandidateAvailable:
			sum.Available++
		case domain.CandidateUpToDate:
			sum.UpToDate++
		case domain.CandidateQuarantined:
			sum.Quarantined++
		case domain.CandidateIneligible:
			sum.Ineligible++
		case domain.CandidateCheckFailed, domain.CandidateRunFailed:
			sum.Failed++
		case domain.CandidateUnchecked:
			sum.Unchecked++
		}
		if c.CheckedAt != nil && (sum.LastCheckAt == nil || c.CheckedAt.After(*sum.LastCheckAt)) {
			sum.LastCheckAt = c.CheckedAt
		}
	}
	out.Summary = sum
	qs, err := h.svc.Quarantine(ctx, p.ID)
	if err != nil {
		return out, err
	}
	for _, q := range qs {
		out.Quarantine = append(out.Quarantine, UpdateQuarantined{Service: q.Service, Digest: q.Digest, JobID: q.JobID, ErrorClass: q.ErrorClass,
			CreatedAt: q.CreatedAt})
	}
	hist, err := h.svc.History(ctx, p.ID, 20)
	if err != nil {
		return out, err
	}
	for _, e := range hist {
		out.RecentHistory = append(out.RecentHistory, UpdateHistoryEntry{Service: e.Service, Reference: e.Reference,
			RegistryConnectionID: e.RegistryConnectionID, FromDigest: e.FromDigest, ToDigest: e.ToDigest, JobID: e.JobID, Outcome: e.Outcome,
			ErrorClass: e.ErrorClass, SourceHashBefore: e.SourceHashBefore, SourceHashAfter: e.SourceHashAfter, At: e.At})
	}
	return out, nil
}

// --- errors ---

func updateError(err error) error {
	var ue *domain.UpdateError
	var se *updates.ScheduleError
	var fe *domain.FieldError
	switch {
	case errors.As(err, &ue):
		return Conflict(ue.Code, ue.Message)
	case errors.As(err, &se):
		return ValidateSchedule(se.Cron, se.TimeZone, "body."+se.Field)
	case errors.Is(err, domain.ErrUpdatePolicyNotFound):
		return NotFound("update policy not found")
	case errors.Is(err, domain.ErrUpdatePolicyTargetUsed):
		return Conflict(CodeUpdatePolicyTargetUsed, "the target already has an update policy; edit that one")
	case errors.Is(err, domain.ErrUpdatePolicyNameTaken):
		return Conflict(CodeUpdatePolicyNameTaken, "another update policy in this environment already uses this name")
	case errors.Is(err, domain.ErrStackNotFound):
		return NotFound("stack not found")
	case errors.As(err, &fe):
		return Invalid("invalid update policy", Field("body."+fe.Field, fe.Message))
	case errors.Is(err, domain.ErrJobInvalid), errors.Is(err, domain.ErrJobIdempotencyConflict), errors.Is(err, domain.ErrJobNotFound),
		errors.Is(err, domain.ErrJobForbidden), errors.Is(err, domain.ErrJobUnknownKind), errors.Is(err, domain.ErrJobKindUnavailable):
		return JobErrorFor(err)
	}
	var de *domain.DockerError
	if errors.As(err, &de) {
		return dockerErr(err)
	}
	return registryError(err)
}

// --- handlers ---

type updatesAPI struct {
	svc    UpdateService
	authz  authz.Authorizer
	stacks StackService
	docker *dockerAPI
}

func (h *updatesAPI) checker(ctx context.Context) (authz.Checker, authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, err
	}
	if h.svc == nil {
		return nil, p, Unavailable(CodeUnavailable, "updates are not available")
	}
	return c, p, nil
}

// policy loads a visible policy and requires cp on it.
func (h *updatesAPI) policy(ctx context.Context, id string, cp Capability) (authz.Checker, authz.Principal, domain.UpdatePolicy, authz.View, error) {
	c, pr, err := h.checker(ctx)
	if err != nil {
		return nil, pr, domain.UpdatePolicy{}, authz.View{}, err
	}
	p, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, pr, p, authz.View{}, updateError(err)
	}
	v := authz.ViewOf(c, updatePolicyResource(p))
	if !v.Visible() {
		return nil, pr, p, v, NotFound("update policy not found")
	}
	if !v.Has(string(cp)) {
		return nil, pr, p, v, Forbidden("not permitted: " + string(cp))
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID})
	return c, pr, p, v, nil
}

type listUpdatePoliciesInput struct {
	PageParams
	EnvironmentID string `query:"environmentId" maxLength:"64" doc:"Only policies of this environment."`
}

type updatePolicyIDInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Update policy ID."`
}

type updatePolicyOutput struct {
	ETagHeader
	Body UpdatePolicy
}

type updatePolicyListOutput struct{ Body Page[UpdatePolicy] }

type createUpdateCheckInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Update policy ID."`
	IdempotencyKeyParam
}

type updateCandidatesOutput struct{ Body Page[UpdateCandidate] }

type previewUpdateInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Update policy ID."`
	Body     struct {
		Candidates []string `json:"candidates,omitempty" example:"web" maxItems:"64" doc:"Candidate IDs or service names (default: every update available)."`
	}
}

// UpdateServiceDependency is a stack service's depends_on list.
type UpdateServiceDependency struct {
	Service   string            `json:"service"`
	DependsOn []StackDependency `json:"dependsOn"`
}

// UpdateSharedConsumer uses one of the run's tags on the environment.
type UpdateSharedConsumer struct {
	Reference string `json:"reference"`
	StackID   string `json:"stackId,omitempty"`
	StackName string `json:"stackName,omitempty"`
	Service   string `json:"service,omitempty"`
	Container string `json:"container,omitempty"`
}

// UpdatePreviewItem is a service or container the run updates.
type UpdatePreviewItem struct {
	Candidate UpdateCandidate `json:"candidate"`
	Running   bool            `json:"running" doc:"It runs now (last observed state)."`
	Downtime  string          `json:"downtime" doc:"Expected interruption."`
}

// UpdatePreview is what a run would do now (nothing changes).
type UpdatePreview struct {
	PolicyID     string                    `json:"policyId"`
	Fingerprint  string                    `json:"fingerprint" example:"3f9a0c1d2e4b5a6c" doc:"Send as previewFingerprint to refuse the run when anything changed since."`
	Items        []UpdatePreviewItem       `json:"items"`
	Skipped      []UpdateCandidate         `json:"skipped" doc:"Candidates left out, with their status and reason."`
	Restarted    []string                  `json:"restarted" doc:"Running dependents restarted with an updated service (depends_on restart: true)."`
	Dependencies []UpdateServiceDependency `json:"dependencies" doc:"The stack's dependency graph (as deployed)."`
	SharedTag    []UpdateSharedConsumer    `json:"sharedTag" doc:"Other consumers of the tags on this environment: the pull moves the tag for them too; they pick up the new image at their next recreate."`
	SourceHash   string                    `json:"sourceHash,omitempty" doc:"The applied revision's hash the definition on disk must have."`
	SourceDrift  bool                      `json:"sourceDrift" doc:"The definition on disk differs from the applied revision: the run is refused until the stack is deployed."`
	InWindow     bool                      `json:"inWindow" doc:"Now is inside the policy's update window (scheduled runs only run inside it)."`
	Notes        []string                  `json:"notes"`
}

type previewUpdateOutput struct{ Body UpdatePreview }

type createUpdateRunInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Update policy ID."`
	IdempotencyKeyParam
	Body struct {
		Candidates         []string `json:"candidates,omitempty" example:"web" maxItems:"64" doc:"Candidate IDs or service names (default: every update available)."`
		PreviewFingerprint string   `json:"previewFingerprint,omitempty" maxLength:"64" doc:"The preview's fingerprint: 409 update_preview_stale when anything changed since."`
	}
}

// ContainerImageStatus is a container's image and update state.
type ContainerImageStatus struct {
	Container     string     `json:"container" example:"web"`
	Image         string     `json:"image" example:"nginx:1.27" doc:"The saved reference of a Docker Manager-managed container, else the container's image."`
	ImageID       string     `json:"imageId,omitempty"`
	Digest        string     `json:"digest,omitempty" doc:"Repository digest of the running image."`
	Platform      string     `json:"platform,omitempty"`
	Managed       bool       `json:"managed" doc:"A Docker Manager-managed standalone container with a saved recreate specification."`
	Eligible      bool       `json:"eligible" doc:"The container could follow its tag's digest (#20)."`
	Reason        string     `json:"reason,omitempty" enum:"build_only,digest_pinned,untagged,pull_policy_conflict,invalid_reference,protected,no_recreate_spec,stack_managed"`
	ReasonMessage string     `json:"reasonMessage,omitempty"`
	NonVersionTag bool       `json:"nonVersionTag"`
	PolicyID      string     `json:"policyId,omitempty" doc:"The container's update policy."`
	Update        string     `json:"update" enum:"no_policy,ineligible,unchecked,up_to_date,update_available,quarantined,check_failed,run_failed"`
	Candidate     string     `json:"candidateDigest,omitempty"`
	CheckedAt     *time.Time `json:"checkedAt,omitempty"`
}

type containerImageStatusOutput struct{ Body ContainerImageStatus }

func (h *updatesAPI) list(ctx context.Context, in *listUpdatePoliciesInput) (*updatePolicyListOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("update-policies", in.EnvironmentID)
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.UpdatePolicy]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.UpdatePolicy, error) {
			return h.svc.List(ctx, in.EnvironmentID, afterID, n)
		},
		Position: func(p domain.UpdatePolicy) string { return p.ID },
		Visible:  func(p domain.UpdatePolicy) bool { return authz.ViewOf(c, updatePolicyResource(p)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]UpdatePolicy, 0, len(items))
	for _, p := range items {
		dto, err := h.newPolicy(ctx, p, authz.ViewOf(c, updatePolicyResource(p)))
		if err != nil {
			return nil, Internal(err)
		}
		out = append(out, dto)
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &updatePolicyListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func scheduleInput(in *UpdateScheduleInput) *domain.UpdateSchedule {
	if in == nil {
		return nil
	}
	return &domain.UpdateSchedule{Cron: in.Cron, TimeZone: in.TimeZone, Enabled: in.Enabled}
}

func windowInput(in *UpdateWindow) *domain.UpdateWindow {
	if in == nil {
		return nil
	}
	return &domain.UpdateWindow{Days: append([]int{}, in.Days...), Start: in.Start, End: in.End}
}

func (h *updatesAPI) get(ctx context.Context, in *updatePolicyIDInput) (*updatePolicyOutput, error) {
	c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.svc.Get(ctx, in.PolicyID)
	if err != nil {
		return nil, updateError(err)
	}
	v := authz.ViewOf(c, updatePolicyResource(p))
	if !v.Visible() {
		return nil, NotFound("update policy not found")
	}
	body, err := h.newPolicy(ctx, p, v)
	if err != nil {
		return nil, Internal(err)
	}
	out := &updatePolicyOutput{Body: body}
	if body.Revision != 0 {
		out.ETag = RevisionETag(body.Revision)
	}
	return out, nil
}

func (h *updatesAPI) check(ctx context.Context, in *createUpdateCheckInput) (*JobAccepted, error) {
	_, pr, p, _, err := h.policy(ctx, in.PolicyID, CapUpdateCheck)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.StartCheck(ctx, pr, p.ID, in.IdempotencyKey)
	if err != nil {
		return nil, updateError(err)
	}
	return Accepted(j), nil
}

func (h *updatesAPI) candidates(ctx context.Context, in *updatePolicyIDInput) (*updateCandidatesOutput, error) {
	_, _, p, _, err := h.policy(ctx, in.PolicyID, CapUpdatePolicyRead)
	if err != nil {
		return nil, err
	}
	cands, err := h.svc.Candidates(ctx, p.ID)
	if err != nil {
		return nil, updateError(err)
	}
	out := make([]UpdateCandidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, newUpdateCandidate(c))
	}
	return &updateCandidatesOutput{Body: NewPage(out, "", nil)}, nil
}

// updatePreviewNotes explain v1 behavior in every preview.
var updatePreviewNotes = []string{
	"The literal image reference in your Compose file or saved specification is never changed; Docker Manager pulls the same tag and recreates only what runs another image afterwards.",
	"Your Compose, override and env files are never written: the run is refused if they differ from the applied revision (deploy first).",
	"Pulling a tag moves it for every stack and container on this environment that uses it; those not in this run keep their container until their next recreate.",
	"There is no automatic rollback: a failed update is reported and its digest quarantined; to go back, pin the previous digest (@sha256) in your own definition and deploy it.",
}

func (h *updatesAPI) preview(ctx context.Context, in *previewUpdateInput) (*previewUpdateOutput, error) {
	_, _, p, _, err := h.policy(ctx, in.PolicyID, CapUpdateCheck)
	if err != nil {
		return nil, err
	}
	pv, err := h.svc.Preview(ctx, p.ID, in.Body.Candidates)
	if err != nil {
		return nil, updateError(err)
	}
	out := UpdatePreview{PolicyID: p.ID, Fingerprint: pv.Fingerprint, Items: []UpdatePreviewItem{}, Skipped: []UpdateCandidate{},
		Restarted: append([]string{}, pv.Restarted...), Dependencies: []UpdateServiceDependency{}, SharedTag: []UpdateSharedConsumer{},
		SourceHash: pv.SourceHash, SourceDrift: pv.SourceDrift, InWindow: pv.InWindow, Notes: append([]string{}, updatePreviewNotes...)}
	for _, it := range pv.Items {
		out.Items = append(out.Items, UpdatePreviewItem{Candidate: newUpdateCandidate(it.Candidate), Running: it.Running, Downtime: it.Downtime})
	}
	for _, c := range pv.Skipped {
		out.Skipped = append(out.Skipped, newUpdateCandidate(c))
	}
	for _, d := range pv.Dependencies {
		dep := UpdateServiceDependency{Service: d.Name, DependsOn: []StackDependency{}}
		for _, x := range d.DependsOn {
			dep.DependsOn = append(dep.DependsOn, StackDependency{Service: x.Service, Condition: x.Condition, Required: x.Required, Restart: x.Restart})
		}
		out.Dependencies = append(out.Dependencies, dep)
	}
	for _, s := range pv.SharedTag {
		out.SharedTag = append(out.SharedTag, UpdateSharedConsumer(s))
	}
	return &previewUpdateOutput{Body: out}, nil
}

func (h *updatesAPI) run(ctx context.Context, in *createUpdateRunInput) (*JobAccepted, error) {
	_, pr, p, _, err := h.policy(ctx, in.PolicyID, CapUpdateRun)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.Run(ctx, updates.RunRequest{Principal: pr, PolicyID: p.ID, Candidates: in.Body.Candidates,
		Fingerprint: in.Body.PreviewFingerprint, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return nil, updateError(err)
	}
	audit.SetDetail(ctx, "candidates", len(in.Body.Candidates))
	return Accepted(j), nil
}

func (h *updatesAPI) containerImageStatus(ctx context.Context, in *ContainerPath) (*containerImageStatusOutput, error) {
	if h.docker == nil {
		return nil, Unavailable(CodeUnavailable, "the Docker resource service is not available")
	}
	sc, err := h.docker.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	d, v, err := h.docker.visibleContainer(ctx, sc, in.ContainerID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapContainerDetailsRead)) {
		return nil, Forbidden("not permitted: " + string(CapContainerDetailsRead))
	}
	out := ContainerImageStatus{Container: d.Name, Image: d.Image, ImageID: d.ImageID, Update: "no_policy"}
	m, spec, err := h.docker.svc.ManagedSpec(ctx, sc.env.ID, d.Labels)
	if err != nil {
		return nil, Internal(err)
	}
	if m != nil {
		out.Managed, out.Image = true, spec.Image
	}
	if img, err := h.docker.svc.InspectImage(ctx, sc.env.ID, d.ImageID); err == nil {
		out.Digest = imageref.DigestFor(out.Image, img.RepoDigests)
		if img.OS != "" {
			out.Platform = img.OS + "/" + img.Architecture
			if img.Variant != "" {
				out.Platform += "/" + img.Variant
			}
		}
	}
	r := eligible.Check(eligible.Subject{Reference: out.Image})
	out.Eligible, out.Reason, out.ReasonMessage, out.NonVersionTag = r.Eligible, r.Reason, r.Message, r.NonVersionTag
	switch pr := d.Protection; {
	case d.Stack != nil:
		out.Eligible, out.Reason, out.ReasonMessage = false, domain.UpdateReasonStackManaged, "The container belongs to a Compose project; see its stack's image status."
	case pr != nil:
		out.Eligible, out.Reason, out.ReasonMessage = false, domain.UpdateReasonProtected, "Docker Manager's own containers are never updated by a policy: "+pr.Reason
	case m == nil:
		out.Eligible, out.Reason, out.ReasonMessage = false, domain.UpdateReasonNoRecreateSpec,
			"Only containers created through Docker Manager have a complete saved recreate specification; others are never recreated automatically."
	}
	if h.svc != nil {
		if p, err := h.svc.ForTarget(ctx, sc.env.ID, domain.UpdateTargetContainer, d.Name); err == nil && p != nil {
			out.PolicyID = p.ID
			if cands, err := h.svc.Candidates(ctx, p.ID); err == nil && len(cands) > 0 {
				out.Update, out.Candidate, out.CheckedAt = string(cands[0].Status), cands[0].CandidateDigest, cands[0].CheckedAt
			} else {
				out.Update = string(domain.CandidateUnchecked)
			}
		}
	}
	return &containerImageStatusOutput{Body: out}, nil
}

func registerUpdates(a huma.API, deps Deps) {
	registerEnvironmentUpdates(a, deps)
	h := &updatesAPI{svc: deps.Updates, authz: authz.OrDenyAll(deps.Authorizer), stacks: deps.Stacks, docker: newDockerAPI(deps)}
	base := BasePath + "/update-policies"
	one := base + "/{policyId}"
	read := []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusServiceUnavailable}
	mutate := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
		http.StatusServiceUnavailable}

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-update-policies", Method: http.MethodGet, Path: base, Summary: "List update policies",
		Description: "Update policies opt a Docker Manager stack (all or selected services) or a Docker Manager-managed standalone container into " +
			"digest-driven updates (#20): the existing explicit tag is followed by its host-platform digest; the tag text and the user's " +
			"files never change. Entries the caller cannot see are omitted; other capabilities than update_policy.read show id, name, " +
			"environment and target.",
		Tags: []string{tagUpdates}, Errors: read,
	}, Capability: CapUpdatePolicyRead, Scope: ScopeResource}, h.list)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-update-policy", Method: http.MethodGet, Path: one, Summary: "Get an update policy",
		Description: "The policy with its schedules (next run, recent runs), candidate summary, quarantined digests and applied digest " +
			"history (full view).",
		Tags: []string{tagUpdates}, Errors: read,
	}, Capability: CapUpdatePolicyRead, Scope: ScopeResource}, h.get)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-update-policy-check", Method: http.MethodPost, Path: one + "/checks", Summary: "Check for updates",
		Description: "Starts an update.check job on the manager: each eligible candidate's tag is resolved through its registry " +
			"connection (#19; cached, rate-limit aware; 401/403/429 recorded on the candidate, never retried in a loop) and its " +
			"host-platform manifest digest compared with the digest applied on the host. Nothing is pulled; an index change that " +
			"leaves the host-platform image unchanged is no update. For stacks the definition's hash is read before and after.",
		Tags: []string{tagUpdates}, Errors: mutate,
	}, Capability: CapUpdateCheck, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.check)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "list-update-policy-candidates", Method: http.MethodGet, Path: one + "/candidates", Summary: "List update candidates",
		Description: "The digest model per service (or the container): reference, platform, registry connection, current, previous and " +
			"candidate digests, status, ineligibility reason, errors and recovery guidance. All candidates on one page.",
		Tags: []string{tagUpdates}, Errors: read,
	}, Capability: CapUpdatePolicyRead, Scope: ScopeResource}, h.candidates)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-update-policy-preview", Method: http.MethodPost, Path: one + "/previews", Summary: "Preview an update run",
		Description: "What a run would do now, from the latest check: services/containers to recreate with current and candidate " +
			"digests, dependents restarted (restart: true), the dependency graph, expected downtime, other consumers of the same tags " +
			"on the environment and source drift. Nothing changes.",
		Tags: []string{tagUpdates}, Errors: mutate,
	}, Capability: CapUpdateCheck, Scope: ScopeResource}, h.preview)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "create-update-policy-run", Method: http.MethodPost, Path: one + "/runs", Summary: "Run an update",
		Description: "Starts an update.run job on the environment's agent: pulls the unchanged tagged references (credentials per " +
			"dispatch, #19), verifies the tag still names the checked candidate, then recreates what changed through the Compose SDK " +
			"from exactly the applied definition bytes (dependency order and conditions, restart propagation, stopped services kept " +
			"stopped) or from the container's saved specification, and confirms health. No automatic rollback: a failure quarantines " +
			"the candidate. 409 no_update_candidates, update_source_drift (undeployed changes: deploy first), update_preview_stale.",
		Tags: []string{tagUpdates}, Errors: mutate,
	}, Capability: CapUpdateRun, Scope: ScopeResource, Idempotency: IdempotencyJob}, h.run)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "get-container-image-status", Method: http.MethodGet,
		Path:    BasePath + "/environments/{environmentId}/containers/{containerId}/image-status",
		Summary: "Get a container's image status",
		Description: "The container's image, applied digest and platform, whether it can follow its tag's digest (#20; only " +
			"Docker Manager-managed standalone containers with a saved recreate specification) and its update policy's state.",
		Tags: []string{tagContainers}, Errors: read,
	}, Capability: CapContainerDetailsRead, Scope: ScopeResource}, h.containerImageStatus)
}
