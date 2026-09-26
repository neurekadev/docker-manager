package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Docker maintenance (#14): prune policies per environment, their previews
// and runs, and the instance's suggested default rules. The flows live in
// internal/manager/maintenance; the agent computes and revalidates the
// candidates (internal/agent/prune).

const tagMaintenance = "Maintenance"

// Capability keys of the maintenance routes.
const (
	CapMaintenancePolicyRead   Capability = "maintenance_policy.read"
	CapMaintenancePolicyManage Capability = "maintenance_policy.manage"
	CapMaintenancePreview      Capability = "maintenance.preview"
	CapMaintenanceRun          Capability = "maintenance.run"
)

// MaintenanceService is the maintenance service as seen by the API
// (implemented by *maintenance.Service).
type MaintenanceService interface {
	Defaults(ctx context.Context) (domain.MaintenanceDefaults, error)
	UpdateDefaults(ctx context.Context, revision int64, rules []domain.MaintenanceRule) (before, after domain.MaintenanceDefaults, err error)
	Create(ctx context.Context, c domain.MaintenancePolicyCreate) (domain.MaintenancePolicy, error)
	Get(ctx context.Context, id string) (domain.MaintenancePolicy, error)
	List(ctx context.Context, envID, afterID string, limit int) ([]domain.MaintenancePolicy, error)
	Update(ctx context.Context, id string, revision int64, p domain.MaintenancePolicyPatch) (before, after domain.MaintenancePolicy, err error)
	Delete(ctx context.Context, id string, revision int64) error
	Preview(ctx context.Context, pol domain.MaintenancePolicy, rules []domain.MaintenanceRule, allRules bool) (protocol.PrunePreviewOutput, error)
	Run(ctx context.Context, p authz.Principal, pol domain.MaintenancePolicy, idempotencyKey string) (domain.Job, error)
	PreviewManual(ctx context.Context, envID string, rules []domain.MaintenanceRule) (protocol.PrunePreviewOutput, error)
	RunManual(ctx context.Context, p authz.Principal, envID string, rules []domain.MaintenanceRule, idempotencyKey string) (domain.Job, error)
	ScheduleStatus(ctx context.Context, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
}

type maintenanceEnvironmentService interface {
	PreviewEnvironments(ctx context.Context, pol domain.MaintenancePolicy) (map[string]protocol.PrunePreviewOutput, error)
	RunEnvironments(ctx context.Context, principal authz.Principal, pol domain.MaintenancePolicy, key string) ([]domain.Job, error)
}

type maintenanceEnvironmentPreviewInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
}
type maintenanceEnvironmentRunInput struct {
	PolicyID string `path:"policyId" maxLength:"64"`
	IdempotencyKeyParam
	Body struct {
		Confirm bool `json:"confirm" example:"true"`
	}
}
type maintenanceEnvironmentPreviewItem struct {
	EnvironmentID string       `json:"environmentId"`
	Preview       PrunePreview `json:"preview"`
}
type maintenanceEnvironmentPreviewOutput struct {
	Body struct {
		Items []maintenanceEnvironmentPreviewItem `json:"items"`
	}
}
type maintenanceEnvironmentJobsOutput struct {
	Body struct {
		Jobs []Job `json:"jobs"`
	}
}

// MaintenanceRule is one category rule of a policy.
type MaintenanceRule struct {
	Category string `json:"category" enum:"stopped_containers,dangling_images,unused_images,unused_networks,anonymous_volumes,named_volumes,build_cache"`
	Enabled  bool   `json:"enabled" doc:"The rule takes part in runs. Every rule starts disabled."`
	// MinAgeHours is required so a client never gets "any age" by omission.
	MinAgeHours     int64    `json:"minAgeHours" minimum:"0" maximum:"87600" example:"720" doc:"Only objects older than this are removed (0: any age). Age: since a container stopped (its creation if it never ran), an image's, network's or volume's creation, a build cache record's last use."`
	IncludeLabels   []string `json:"includeLabels,omitempty" maxItems:"32" doc:"Candidates must carry every label (key or key=value). Not for build cache."`
	ExcludeLabels   []string `json:"excludeLabels,omitempty" maxItems:"32" doc:"Objects carrying any of these labels (key or key=value) are never removed. Not for build cache."`
	Exclude         []string `json:"exclude,omitempty" maxItems:"256" doc:"IDs (full or at least 12 characters) and names that are never removed."`
	ContainerStates []string `json:"containerStates,omitempty" maxItems:"3" doc:"stopped_containers only: exited, created, dead (default exited and dead)."`
	BuildCacheAll   bool     `json:"buildCacheAll,omitempty" doc:"build_cache only: all unused records instead of dangling ones (not shared with images, not internal)."`
	// KeepStorageBytes caps the build cache kept (most recently used first).
	KeepStorageBytes int64 `json:"keepStorageBytes,omitempty" minimum:"0" doc:"build_cache only: keep the most recently used cache up to this size (0: no cap)."`
	VolumeOptIn      bool  `json:"volumeOptIn,omitempty" doc:"Volume rules only: explicit opt-in that removing volumes deletes their data. Required to enable an anonymous or named volume rule; each volume rule needs its own."`
}

func (r MaintenanceRule) domain() domain.MaintenanceRule {
	return domain.MaintenanceRule{Category: r.Category, Enabled: r.Enabled, MinAge: time.Duration(r.MinAgeHours) * time.Hour,
		IncludeLabels: r.IncludeLabels, ExcludeLabels: r.ExcludeLabels, Exclude: r.Exclude, ContainerStates: r.ContainerStates,
		BuildCacheAll: r.BuildCacheAll, KeepStorageBytes: r.KeepStorageBytes, VolumeOptIn: r.VolumeOptIn}
}

func newMaintenanceRule(r domain.MaintenanceRule) MaintenanceRule {
	return MaintenanceRule{Category: r.Category, Enabled: r.Enabled, MinAgeHours: int64(r.MinAge / time.Hour), IncludeLabels: r.IncludeLabels,
		ExcludeLabels: r.ExcludeLabels, Exclude: r.Exclude, ContainerStates: r.ContainerStates, BuildCacheAll: r.BuildCacheAll,
		KeepStorageBytes: r.KeepStorageBytes, VolumeOptIn: r.VolumeOptIn}
}

func pruneRulesOf(rs []MaintenanceRule) []domain.MaintenanceRule {
	out := make([]domain.MaintenanceRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.domain())
	}
	return out
}

func newMaintenanceRules(rs []domain.MaintenanceRule) []MaintenanceRule {
	out := make([]MaintenanceRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, newMaintenanceRule(r))
	}
	return out
}

// PruneCategoryInfo describes a category and the Engine's limitations.
type PruneCategoryInfo struct {
	Category    string   `json:"category"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Labels      bool     `json:"labels" doc:"Label include/exclude filters are supported."`
	DeletesData bool     `json:"deletesData" doc:"Removal deletes stored data (volume rules need their own opt-in)."`
	Limitations []string `json:"limitations"`
}

var pruneCategoryInfo = []PruneCategoryInfo{
	{Category: domain.PruneStoppedContainers, Label: "Stopped containers", Labels: true,
		Description: "Containers in the selected states (exited and dead by default, created on request). Running, paused and restarting containers are never removed.",
		Limitations: []string{"Containers of DockYard stacks and containers with a saved DockYard recreate specification are protected.",
			"Anonymous volumes of removed containers stay; the anonymous-volume rule removes them."}},
	{Category: domain.PruneDanglingImages, Label: "Dangling images", Labels: true,
		Description: "Untagged images no container uses.",
		Limitations: []string{"Image age is the creation time the Engine reports (like docker image prune --filter until)."}},
	{Category: domain.PruneUnusedImages, Label: "All unused images", Labels: true,
		Description: "Every image no container uses, tagged or not. Images left unused by an update are ordinary candidates: v1 has no rollback window.",
		Limitations: []string{"Images referenced by DockYard stacks and saved container specifications are protected.",
			"Sizes count layers shared with other images: the space freed can be smaller."}},
	{Category: domain.PruneUnusedNetworks, Label: "Unused networks", Labels: true,
		Description: "Custom networks no container (running or stopped) uses.",
		Limitations: []string{"Predefined (bridge, host, none), swarm and DockYard stack networks are protected."}},
	{Category: domain.PruneAnonymousVolumes, Label: "Anonymous volumes", Labels: true, DeletesData: true,
		Description: "Volumes the Engine created for anonymous mounts (label com.docker.volume.anonymous) that no container uses. Deletes their data.",
		Limitations: []string{"Anonymous volumes created before Docker 23 carry no label and count as named volumes.",
			"Needs its own explicit opt-in; volumes of DockYard stacks, DockYard's own and backup destinations are protected."}},
	{Category: domain.PruneNamedVolumes, Label: "Named volumes", Labels: true, DeletesData: true,
		Description: "Named volumes no container uses. Deletes their data.",
		Limitations: []string{"Needs its own explicit opt-in; volumes of DockYard stacks, DockYard's own and backup destinations are protected."}},
	{Category: domain.PruneBuildCache, Label: "Build cache", Labels: false,
		Description: "The BuildKit cache of the Engine's builder (the only builder DockYard uses). Default: dangling records only; all: every unused record. The keep-storage cap keeps the most recently used cache.",
		Limitations: []string{"Build cache records have no labels: exclude records by ID.",
			"The Engine has no per-record delete: DockYard prunes exactly one record ID per call, never the whole cache.",
			"A parent record is removed after its children, possibly in the next run."}},
}

func categoryInfo() []PruneCategoryInfo {
	out := make([]PruneCategoryInfo, len(pruneCategoryInfo))
	copy(out, pruneCategoryInfo)
	return out
}

// MaintenanceDefaults are the instance's suggested rules for new policies.
type MaintenanceDefaults struct {
	Rules      []MaintenanceRule   `json:"rules" doc:"One rule per category. New policies start with these rules; changing them never changes existing policies."`
	Categories []PruneCategoryInfo `json:"categories"`
	Revision   int64               `json:"revision"`
	UpdatedAt  time.Time           `json:"updatedAt"`
}

func newMaintenanceDefaults(d domain.MaintenanceDefaults) MaintenanceDefaults {
	return MaintenanceDefaults{Rules: newMaintenanceRules(d.Rules), Categories: categoryInfo(), Revision: d.Revision, UpdatedAt: d.UpdatedAt}
}

// MaintenanceSchedule is a policy's own schedule (#13) and its state.
type MaintenanceSchedule struct {
	Cron          string           `json:"cron" example:"0 3 * * 0"`
	TimeZone      string           `json:"timeZone" example:"Europe/Berlin"`
	Enabled       bool             `json:"enabled" doc:"Automatic runs; start disabled."`
	CatchUp       string           `json:"catchUp" enum:"skip" doc:"Prune runs missed while the manager was down are recorded, never run late."`
	InvalidReason string           `json:"invalidReason,omitempty"`
	NextRun       *ScheduleRunTime `json:"nextRun,omitempty"`
	RecentRuns    []ScheduleRun    `json:"recentRuns" doc:"Newest first: enqueued, missed, skipped (previous run active), rejected and failed scheduled runs."`
}

// MaintenanceRunSummary is the latest finished run of a policy.
type MaintenanceRunSummary struct {
	JobID          string    `json:"jobId"`
	State          string    `json:"state"`
	Origin         string    `json:"origin" enum:"manual,scheduled,api_token"`
	FinishedAt     time.Time `json:"finishedAt"`
	Removed        int       `json:"removed"`
	Skipped        int       `json:"skipped"`
	Failed         int       `json:"failed"`
	Deferred       int       `json:"deferred" doc:"Candidates beyond the per-run limit, left for the next run."`
	BytesReclaimed int64     `json:"bytesReclaimed" doc:"Approximate."`
}

// MaintenancePolicy is a prune policy. Shaping (#17):
// maintenance_policy.read shows it in full, other capabilities on it only
// id, name, environment and enabled.
type MaintenancePolicy struct {
	ID            string                 `json:"id"`
	Scope         string                 `json:"scope" enum:"all,environment"`
	EnvironmentID string                 `json:"environmentId"`
	Name          string                 `json:"name"`
	Enabled       bool                   `json:"enabled" doc:"Automatic (scheduled) runs are enabled."`
	View          string                 `json:"view" enum:"minimal,full"`
	Actions       []string               `json:"actions"`
	Description   string                 `json:"description,omitempty"`
	Schedule      *MaintenanceSchedule   `json:"schedule,omitempty" doc:"Full view."`
	Rules         []MaintenanceRule      `json:"rules,omitempty" doc:"Full view: one rule per category. The enabled ones together are the policy's system cleanup."`
	LastRun       *MaintenanceRunSummary `json:"lastRun,omitempty"`
	Revision      int64                  `json:"revision,omitempty"`
	CreatedAt     time.Time              `json:"createdAt,omitzero"`
	UpdatedAt     time.Time              `json:"updatedAt,omitzero"`
}

func policyResource(p domain.MaintenancePolicy) authz.Resource {
	return authz.Resource{Type: catalog.TypeMaintenancePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID, Parents: []authz.ResourceRef{}}
}

func (h *maintenanceAPI) newPolicy(ctx context.Context, p domain.MaintenancePolicy, v authz.View) MaintenancePolicy {
	scope := "environment"
	if p.EnvironmentID == "" {
		scope = "all"
	}
	out := MaintenancePolicy{ID: p.ID, Scope: scope, EnvironmentID: p.EnvironmentID, Name: p.Name, Enabled: p.ScheduleEnabled, View: v.Level.String(),
		Actions: Actions(v)}
	if v.Has(string(CapMaintenancePolicyManage)) {
		out.Revision = p.Revision
	}
	if !v.Full() {
		return out
	}
	out.Description, out.Rules, out.Revision = p.Description, newMaintenanceRules(p.Rules), p.Revision
	out.CreatedAt, out.UpdatedAt = p.CreatedAt, p.UpdatedAt
	sched := &MaintenanceSchedule{Cron: p.Cron, TimeZone: p.TimeZone, Enabled: p.ScheduleEnabled, CatchUp: "skip", RecentRuns: []ScheduleRun{}}
	if sc, runs, ok, err := h.svc.ScheduleStatus(ctx, p.ID, 10); err == nil && ok {
		sched.InvalidReason = sc.InvalidReason
		if h.sched != nil {
			if r, ok := h.sched.NextRun(sc); ok {
				rt := newRunTime(r)
				sched.NextRun = &rt
			}
		}
		for _, r := range runs {
			sched.RecentRuns = append(sched.RecentRuns, newScheduleRun(r))
		}
	}
	out.Schedule = sched
	if r := p.LastRun; r != nil {
		out.LastRun = &MaintenanceRunSummary{JobID: r.JobID, State: string(r.State), Origin: string(r.Origin), FinishedAt: r.FinishedAt,
			Removed: r.Removed, Skipped: r.Skipped, Failed: r.Failed, Deferred: r.Deferred, BytesReclaimed: r.BytesReclaimed}
	}
	return out
}

// PruneItemView is one object of a preview.
type PruneItemView struct {
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Decision string     `json:"decision" enum:"remove,protected,excluded,retained"`
	Reason   string     `json:"reason"`
	Bytes    int64      `json:"bytes" doc:"Approximate space freed; -1 when the Engine does not report it."`
	Since    *time.Time `json:"since,omitempty" doc:"The time the age threshold is measured from."`
}

// PruneCategoryPreview is one category of a preview.
type PruneCategoryPreview struct {
	Category     string          `json:"category" example:"dangling_images" enum:"stopped_containers,dangling_images,unused_images,unused_networks,anonymous_volumes,named_volumes,build_cache"`
	Remove       int             `json:"remove"`
	Protected    int             `json:"protected"`
	Excluded     int             `json:"excluded"`
	Retained     int             `json:"retained"`
	Bytes        int64           `json:"bytes" doc:"Approximate sum of the candidates' known sizes."`
	UnknownSizes int             `json:"unknownSizes"`
	Items        []PruneItemView `json:"items" doc:"Candidates first (in removal order), then protected, excluded and retained objects; at most 200."`
	Truncated    bool            `json:"truncated"`
}

// PrunePreview is what a run of a policy would remove now.
type PrunePreview struct {
	PolicyID      string                 `json:"policyId"`
	EnvironmentID string                 `json:"environmentId"`
	At            time.Time              `json:"at"`
	Remove        int                    `json:"remove"`
	Bytes         int64                  `json:"bytes"`
	Categories    []PruneCategoryPreview `json:"categories"`
	Notes         []string               `json:"notes"`
}

var previewNotes = []string{
	"Nothing was removed. A run recomputes the candidates and revalidates each one immediately before deleting it: objects that became used, protected, excluded or too recent are skipped with the reason.",
	"DockYard's own containers, images, volumes and networks, DockYard stacks, saved container specifications and backup destinations are never removed.",
	"Sizes are approximate: image sizes count shared layers, and build cache shared with images frees less.",
	"One run removes at most 300 candidates; the rest waits for the next run.",
}

func newPrunePreview(pol domain.MaintenancePolicy, p protocol.PrunePreviewOutput) PrunePreview {
	out := PrunePreview{PolicyID: pol.ID, EnvironmentID: pol.EnvironmentID, At: p.At, Categories: []PruneCategoryPreview{}, Notes: previewNotes}
	for _, c := range p.Categories {
		cp := PruneCategoryPreview{Category: c.Category, Remove: c.Remove, Protected: c.Protected, Excluded: c.Excluded, Retained: c.Retained,
			Bytes: c.Bytes, UnknownSizes: c.UnknownSizes, Truncated: c.Truncated, Items: []PruneItemView{}}
		for _, it := range c.Items {
			v := PruneItemView{ID: it.ID, Name: it.Name, Decision: it.Decision, Reason: it.Reason, Bytes: it.Bytes}
			if !it.Since.IsZero() {
				s := it.Since
				v.Since = &s
			}
			cp.Items = append(cp.Items, v)
		}
		out.Remove += c.Remove
		out.Bytes += c.Bytes
		out.Categories = append(out.Categories, cp)
	}
	return out
}

func maintenanceError(err error) error {
	var fe *domain.FieldError
	var ie *scheduler.InvalidError
	var active *domain.MaintenanceRunActiveError
	var de *domain.DockerError
	switch {
	case errors.Is(err, domain.ErrMaintenancePolicyNotFound):
		return NotFound("maintenance policy not found")
	case errors.Is(err, domain.ErrMaintenancePolicyNameTaken):
		return Conflict(CodeMaintenancePolicyNameTaken, "another maintenance policy in this environment already uses this name")
	case errors.Is(err, domain.ErrMaintenanceScopeOverlap):
		return Conflict("maintenance_scope_overlap", "a maintenance policy already covers this environment")
	case errors.Is(err, domain.ErrMaintenancePolicyEmpty):
		return Conflict(CodeMaintenancePolicyEmpty, "the policy has no enabled rule; enable at least one rule before running it")
	case errors.As(err, &active):
		return Conflict(CodeMaintenanceRunActive, active.Error()+"; follow it with GET /api/v1/jobs/"+active.JobID)
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived")
	case errors.As(err, &fe):
		return Invalid("invalid maintenance policy", Field("body."+fe.Field, fe.Message))
	case errors.As(err, &ie):
		details := make([]ErrorDetail, 0, len(ie.Problems))
		for _, p := range ie.Problems {
			details = append(details, cronDetail("body.schedule.cron", "body.schedule.timeZone", p))
		}
		return Invalid("invalid schedule", details...)
	case errors.As(err, &de):
		return dockerErr(err)
	}
	return JobErrorFor(err)
}

type maintenanceAPI struct {
	svc   MaintenanceService
	sched ScheduleService
	authz authz.Authorizer
}

func (h *maintenanceAPI) service() (MaintenanceService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "maintenance policies are not available")
	}
	return h.svc, nil
}

// policy loads a policy visible to the caller (404 otherwise).
func (h *maintenanceAPI) policy(ctx context.Context, id string) (MaintenanceService, authz.Checker, authz.Principal, domain.MaintenancePolicy, authz.View, error) {
	svc, err := h.service()
	if err != nil {
		return nil, nil, authz.Principal{}, domain.MaintenancePolicy{}, authz.View{}, err
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, domain.MaintenancePolicy{}, authz.View{}, err
	}
	pol, err := svc.Get(ctx, id)
	if err != nil {
		return nil, nil, p, pol, authz.View{}, maintenanceError(err)
	}
	v := authz.ViewOf(c, policyResource(pol))
	if !v.Visible() {
		return nil, nil, p, pol, v, NotFound("maintenance policy not found")
	}
	return svc, c, p, pol, v, nil
}

func policyETag(p MaintenancePolicy) ETagHeader {
	if p.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(p.Revision)}
}

// flatPolicy is the audit diff form of a policy (rules by category).
func flatPolicy(p domain.MaintenancePolicy) map[string]any {
	m := map[string]any{"name": p.Name, "description": p.Description, "cron": p.Cron, "timeZone": p.TimeZone, "scheduleEnabled": p.ScheduleEnabled}
	for _, r := range p.Rules {
		m["rules."+r.Category] = newMaintenanceRule(r)
	}
	return m
}

type maintenanceScheduleInput struct {
	Cron     *string `json:"cron,omitempty" minLength:"1" maxLength:"256" doc:"Five-field cron expression (default: the prune default of the schedule defaults)."`
	TimeZone *string `json:"timeZone,omitempty" minLength:"1" maxLength:"64" doc:"IANA time zone (default: the instance's default zone)."`
	Enabled  *bool   `json:"enabled,omitempty" doc:"Automatic runs (default false)."`
}

type listMaintenancePoliciesInput struct {
	PageParams
	EnvironmentID string `query:"environmentId" maxLength:"64" doc:"Only policies of this environment."`
}

type maintenancePolicyOutput struct {
	ETagHeader
	Body MaintenancePolicy
}

type maintenancePolicyListOutput struct{ Body Page[MaintenancePolicy] }

type createMaintenancePolicyInput struct {
	Body struct {
		Scope         string                    `json:"scope,omitempty" enum:"all,environment"`
		EnvironmentID string                    `json:"environmentId,omitempty" maxLength:"64"`
		Name          string                    `json:"name" minLength:"1" maxLength:"100" example:"Weekly cleanup"`
		Description   string                    `json:"description,omitempty" maxLength:"1000"`
		Schedule      *maintenanceScheduleInput `json:"schedule,omitempty"`
		Rules         []MaintenanceRule         `json:"rules,omitempty" maxItems:"7" doc:"Rules to set; categories not given start with the maintenance defaults (all disabled unless the defaults were changed)."`
	}
}

type maintenancePolicyIDInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Maintenance policy ID."`
}

type updateMaintenancePolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Maintenance policy ID."`
	IfMatchParam
	Body struct {
		Name        *string                   `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Description *string                   `json:"description,omitempty" maxLength:"1000"`
		Schedule    *maintenanceScheduleInput `json:"schedule,omitempty"`
		Rules       []MaintenanceRule         `json:"rules,omitempty" maxItems:"7" doc:"Each rule given replaces the rule of its category (send the whole rule)."`
	}
}

type deleteMaintenancePolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Maintenance policy ID."`
	IfMatchParam
}

type previewMaintenancePolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Maintenance policy ID."`
	Body     *struct {
		Rules           []MaintenanceRule `json:"rules,omitempty" maxItems:"7" doc:"Preview these rules (merged by category onto the saved ones) instead of the saved rules; nothing is saved."`
		IncludeDisabled bool              `json:"includeDisabled,omitempty" doc:"Evaluate disabled rules too (previewing enables nothing)."`
	}
}

type prunePreviewOutput struct{ Body PrunePreview }

type runMaintenancePolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Maintenance policy ID."`
	IdempotencyKeyParam
	Body *struct {
		Confirm    bool `json:"confirm,omitempty" example:"true" doc:"Must be true: a run deletes the candidates and a completed deletion cannot be undone (409 prune_confirmation_required otherwise)."`
		Background bool `json:"background,omitempty" doc:"Presentation preference only: the run is the same durable job either way, and leaving the UI never cancels it."`
	}
}

// manualPruneRules are the rules of a one-off prune: only the categories
// given take part (enabled ones), nothing is saved.
type manualPruneRules struct {
	Rules []MaintenanceRule `json:"rules" minItems:"1" maxItems:"7" doc:"The rules of this prune only (one per category); categories not given are not pruned. At least one must be enabled."`
}

type previewManualPruneInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	Body          manualPruneRules
}

type runManualPruneInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		Rules   []MaintenanceRule `json:"rules" minItems:"1" maxItems:"7" doc:"The rules of this prune only (one per category); categories not given are not pruned. At least one must be enabled."`
		Confirm bool              `json:"confirm,omitempty" example:"true" doc:"Must be true: the prune deletes its candidates and a completed deletion cannot be undone (409 prune_confirmation_required otherwise)."`
	}
}

type maintenanceDefaultsOutput struct {
	ETagHeader
	Body MaintenanceDefaults
}

type updateMaintenanceDefaultsInput struct {
	IfMatchParam
	Body struct {
		Rules []MaintenanceRule `json:"rules" minItems:"1" maxItems:"7" doc:"Each rule given replaces the default of its category."`
	}
}

func (h *maintenanceAPI) list(ctx context.Context, in *listMaintenancePoliciesInput) (*maintenancePolicyListOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("maintenance-policies", in.EnvironmentID)
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.MaintenancePolicy]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.MaintenancePolicy, error) {
			return svc.List(ctx, in.EnvironmentID, afterID, n)
		},
		Position: func(p domain.MaintenancePolicy) string { return p.ID },
		Visible:  func(p domain.MaintenancePolicy) bool { return authz.ViewOf(c, policyResource(p)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]MaintenancePolicy, 0, len(items))
	for _, p := range items {
		out = append(out, h.newPolicy(ctx, p, authz.ViewOf(c, policyResource(p))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &maintenancePolicyListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *maintenanceAPI) create(ctx context.Context, in *createMaintenancePolicyInput) (*maintenancePolicyOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	env := in.Body.EnvironmentID
	if in.Body.Scope == "all" && env != "" {
		return nil, Invalid("invalid maintenance scope", Field("body.environmentId", "leave empty for all environments"))
	}
	if in.Body.Scope == "environment" && env == "" {
		return nil, Invalid("invalid maintenance scope", Field("body.environmentId", "choose an environment"))
	}
	if in.Body.Scope != "" && in.Body.Scope != "all" && in.Body.Scope != "environment" {
		return nil, Invalid("invalid maintenance scope", Field("body.scope", "choose all or environment"))
	}
	if env == "" {
		if !c.Can("maintenance_policy.manage_all", authz.Instance()).Allowed {
			return nil, Forbidden("only the owner can manage maintenance across all environments")
		}
	} else if !authz.ViewOf(c, authz.EnvironmentResource(env)).Visible() {
		return nil, NotFound("environment not found")
	}
	if env != "" && !c.Can(string(CapMaintenancePolicyManage), authz.InEnvironment(catalog.TypeMaintenancePolicy, env)).Allowed {
		return nil, Forbidden("not permitted: maintenance_policy.manage in this environment")
	}
	cr := domain.MaintenancePolicyCreate{EnvironmentID: env, Name: in.Body.Name, Description: in.Body.Description, Rules: pruneRulesOf(in.Body.Rules)}
	if s := in.Body.Schedule; s != nil {
		if s.Cron != nil {
			cr.Cron = *s.Cron
		}
		if s.TimeZone != nil {
			cr.TimeZone = *s.TimeZone
		}
		if s.Enabled != nil {
			cr.ScheduleEnabled = *s.Enabled
		}
	}
	p, err := svc.Create(ctx, cr)
	if err != nil {
		return nil, maintenanceError(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeMaintenancePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID})
	audit.SetDiff(ctx, nil, flatPolicy(p))
	body := h.newPolicy(ctx, p, authz.ViewOf(c, policyResource(p)))
	return &maintenancePolicyOutput{ETagHeader: policyETag(body), Body: body}, nil
}

func (h *maintenanceAPI) get(ctx context.Context, in *maintenancePolicyIDInput) (*maintenancePolicyOutput, error) {
	_, _, _, p, v, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	body := h.newPolicy(ctx, p, v)
	return &maintenancePolicyOutput{ETagHeader: policyETag(body), Body: body}, nil
}

func (h *maintenanceAPI) update(ctx context.Context, in *updateMaintenancePolicyInput) (*maintenancePolicyOutput, error) {
	svc, c, _, p, v, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapMaintenancePolicyManage)) {
		return nil, Forbidden("not permitted to manage this maintenance policy")
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	patch := domain.MaintenancePolicyPatch{Name: in.Body.Name, Description: in.Body.Description, Rules: pruneRulesOf(in.Body.Rules)}
	if s := in.Body.Schedule; s != nil {
		patch.Cron, patch.TimeZone, patch.ScheduleEnabled = s.Cron, s.TimeZone, s.Enabled
	}
	before, after, err := svc.Update(ctx, p.ID, p.Revision, patch)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		cur, gerr := svc.Get(ctx, p.ID)
		if gerr != nil {
			return nil, maintenanceError(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, maintenanceError(err)
	}
	audit.SetDiff(ctx, flatPolicy(before), flatPolicy(after))
	body := h.newPolicy(ctx, after, authz.ViewOf(c, policyResource(after)))
	return &maintenancePolicyOutput{ETagHeader: policyETag(body), Body: body}, nil
}

func (h *maintenanceAPI) remove(ctx context.Context, in *deleteMaintenancePolicyInput) (*struct{}, error) {
	svc, _, _, p, v, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if !v.Has(string(CapMaintenancePolicyManage)) {
		return nil, Forbidden("not permitted to manage this maintenance policy")
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, p.ID, p.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			cur, gerr := svc.Get(ctx, p.ID)
			if gerr != nil {
				return nil, maintenanceError(gerr)
			}
			return nil, stale(cur.Revision)
		}
		return nil, maintenanceError(err)
	}
	audit.SetDetail(ctx, "name", p.Name)
	return nil, nil
}

func (h *maintenanceAPI) preview(ctx context.Context, in *previewMaintenancePolicyInput) (*prunePreviewOutput, error) {
	svc, c, _, p, _, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if p.EnvironmentID == "" {
		return nil, Conflict("maintenance_global_preview", "use environment-previews to preview this All Environments policy")
	}
	if !c.Can(string(CapMaintenancePreview), policyResource(p)).Allowed {
		return nil, Forbidden("not permitted to preview this maintenance policy (maintenance.preview)")
	}
	var rules []domain.MaintenanceRule
	all := false
	if in.Body != nil {
		rules, all = pruneRulesOf(in.Body.Rules), in.Body.IncludeDisabled
	}
	out, err := svc.Preview(ctx, p, rules, all)
	if err != nil {
		return nil, maintenanceError(err)
	}
	res := newPrunePreview(p, out)
	audit.SetDetail(ctx, "candidates", res.Remove)
	return &prunePreviewOutput{Body: res}, nil
}

func (h *maintenanceAPI) run(ctx context.Context, in *runMaintenancePolicyInput) (*JobAccepted, error) {
	svc, c, pr, p, _, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapMaintenanceRun), policyResource(p)).Allowed {
		return nil, Forbidden("not permitted to run this maintenance policy (maintenance.run)")
	}
	if in.Body == nil || !in.Body.Confirm {
		return nil, Conflict(CodePruneConfirmationRequired,
			"a prune run deletes the policy's candidates and cannot be undone; review a preview and repeat the request with confirm: true")
	}
	if p.EnvironmentID == "" {
		return nil, Conflict("maintenance_global_run", "use environment-runs to run this All Environments policy")
	}
	job, err := svc.Run(ctx, pr, p, in.IdempotencyKey)
	if err != nil {
		return nil, maintenanceError(err)
	}
	audit.SetDetail(ctx, "background", in.Body.Background)
	audit.SetDetail(ctx, "job_id", job.ID)
	return Accepted(job), nil
}

func (h *maintenanceAPI) previewEnvironments(ctx context.Context, in *maintenanceEnvironmentPreviewInput) (*maintenanceEnvironmentPreviewOutput, error) {
	svc, c, _, p, _, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapMaintenancePreview), policyResource(p)).Allowed {
		return nil, Forbidden("not permitted to preview this maintenance policy")
	}
	batch, ok := svc.(maintenanceEnvironmentService)
	if !ok {
		return nil, Unavailable(CodeUnavailable, "environment maintenance preview is not available")
	}
	previews, err := batch.PreviewEnvironments(ctx, p)
	if err != nil {
		return nil, maintenanceError(err)
	}
	out := &maintenanceEnvironmentPreviewOutput{}
	out.Body.Items = []maintenanceEnvironmentPreviewItem{}
	for env, preview := range previews {
		target := p
		target.EnvironmentID = env
		out.Body.Items = append(out.Body.Items, maintenanceEnvironmentPreviewItem{EnvironmentID: env, Preview: newPrunePreview(target, preview)})
	}
	slices.SortFunc(out.Body.Items, func(a, b maintenanceEnvironmentPreviewItem) int {
		return strings.Compare(a.EnvironmentID, b.EnvironmentID)
	})
	return out, nil
}

func (h *maintenanceAPI) runEnvironments(ctx context.Context, in *maintenanceEnvironmentRunInput) (*maintenanceEnvironmentJobsOutput, error) {
	svc, c, principal, p, _, err := h.policy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapMaintenanceRun), policyResource(p)).Allowed {
		return nil, Forbidden("not permitted to run this maintenance policy")
	}
	if !in.Body.Confirm {
		return nil, Conflict(CodePruneConfirmationRequired, "review a preview and repeat with confirm: true")
	}
	batch, ok := svc.(maintenanceEnvironmentService)
	if !ok {
		return nil, Unavailable(CodeUnavailable, "environment maintenance runs are not available")
	}
	jobs, err := batch.RunEnvironments(ctx, principal, p, in.IdempotencyKey)
	if err != nil {
		return nil, maintenanceError(err)
	}
	out := &maintenanceEnvironmentJobsOutput{}
	out.Body.Jobs = make([]Job, 0, len(jobs))
	for _, job := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, NewJob(job))
	}
	return out, nil
}

// manualScope checks a one-off prune of an environment: the environment
// is visible and the caller holds the capability on it (where the job
// engine authorizes a prune.run without targets).
func (h *maintenanceAPI) manualScope(ctx context.Context, env string, capability Capability) (MaintenanceService, authz.Principal, error) {
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, p, err
	}
	res := authz.EnvironmentResource(env)
	if !authz.ViewOf(c, res).Visible() {
		return nil, p, NotFound("environment not found")
	}
	if !c.Can(string(capability), res).Allowed {
		return nil, p, Forbidden("not permitted: " + string(capability) + " in this environment")
	}
	svc, err := h.service()
	return svc, p, err
}

func (h *maintenanceAPI) previewManual(ctx context.Context, in *previewManualPruneInput) (*prunePreviewOutput, error) {
	svc, _, err := h.manualScope(ctx, in.EnvironmentID, CapMaintenancePreview)
	if err != nil {
		return nil, err
	}
	out, err := svc.PreviewManual(ctx, in.EnvironmentID, pruneRulesOf(in.Body.Rules))
	if err != nil {
		return nil, maintenanceError(err)
	}
	res := newPrunePreview(domain.MaintenancePolicy{EnvironmentID: in.EnvironmentID}, out)
	audit.SetDetail(ctx, "candidates", res.Remove)
	return &prunePreviewOutput{Body: res}, nil
}

func (h *maintenanceAPI) runManual(ctx context.Context, in *runManualPruneInput) (*JobAccepted, error) {
	svc, p, err := h.manualScope(ctx, in.EnvironmentID, CapMaintenanceRun)
	if err != nil {
		return nil, err
	}
	if !in.Body.Confirm {
		return nil, Conflict(CodePruneConfirmationRequired,
			"a prune deletes its candidates and cannot be undone; review a preview and repeat the request with confirm: true")
	}
	rules := pruneRulesOf(in.Body.Rules)
	job, err := svc.RunManual(ctx, p, in.EnvironmentID, rules, in.IdempotencyKey)
	if err != nil {
		return nil, maintenanceError(err)
	}
	categories := []string{}
	for _, r := range domain.EnabledRules(rules) {
		categories = append(categories, r.Category)
	}
	audit.SetDetail(ctx, "categories", categories)
	audit.SetDetail(ctx, "job_id", job.ID)
	return Accepted(job), nil
}

func (h *maintenanceAPI) defaults(ctx context.Context, _ *struct{}) (*maintenanceDefaultsOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapSettingsRead), authz.Instance()).Allowed {
		return nil, Forbidden("requires settings.read")
	}
	d, err := svc.Defaults(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	return &maintenanceDefaultsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(d.Revision)}, Body: newMaintenanceDefaults(d)}, nil
}

func (h *maintenanceAPI) updateDefaults(ctx context.Context, in *updateMaintenanceDefaultsInput) (*maintenanceDefaultsOutput, error) {
	svc, err := h.service()
	if err != nil {
		return nil, err
	}
	c, _, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapSettingsManage), authz.Instance()).Allowed {
		return nil, Forbidden("requires settings.manage")
	}
	cur, err := svc.Defaults(ctx)
	if err != nil {
		return nil, Internal(err)
	}
	if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
		return nil, err
	}
	before, after, err := svc.UpdateDefaults(ctx, cur.Revision, pruneRulesOf(in.Body.Rules))
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, stale(before.Revision)
	}
	if err != nil {
		return nil, maintenanceError(err)
	}
	flat := func(d domain.MaintenanceDefaults) map[string]any {
		m := map[string]any{}
		for _, r := range d.Rules {
			m[r.Category] = newMaintenanceRule(r)
		}
		return m
	}
	audit.SetDiff(ctx, flat(before), flat(after))
	return &maintenanceDefaultsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: newMaintenanceDefaults(after)}, nil
}

func registerMaintenance(a huma.API, deps Deps) {
	h := &maintenanceAPI{svc: deps.Maintenance, sched: deps.Schedules, authz: authz.OrDenyAll(deps.Authorizer)}
	path := BasePath + "/maintenance-policies"
	policyErrs := []int{http.StatusForbidden, http.StatusNotFound}

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-maintenance-defaults", Method: http.MethodGet, Path: BasePath + "/maintenance-defaults",
			Summary:     "Get the suggested prune rules",
			Description: "The rules new maintenance policies start with (one per category: all disabled, 30 days) and each category's Engine limitations.",
			Tags:        []string{tagMaintenance}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapSettingsRead, Scope: ScopeInstance,
	}, h.defaults)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-maintenance-defaults", Method: http.MethodPatch, Path: BasePath + "/maintenance-defaults",
			Summary:     "Change the suggested prune rules",
			Description: "Each rule given replaces the default of its category; existing policies keep their rules. Enabling a volume rule needs volumeOptIn. Requires If-Match.",
			Tags:        []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusUnprocessableEntity},
		},
		Capability: CapSettingsManage, Scope: ScopeInstance,
	}, h.updateDefaults)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-maintenance-policies", Method: http.MethodGet, Path: path,
			Summary: "List maintenance policies", Description: "Prune policies, filtered per item (#17); optionally of one environment.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenancePolicyRead, Scope: ScopeResource,
	}, h.list)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-maintenance-policy", Method: http.MethodPost, Path: path,
			Summary: "Create a maintenance policy", DefaultStatus: http.StatusCreated,
			Description: "A prune policy of one environment with one rule per category (stopped containers, dangling and all unused images, " +
				"unused networks, anonymous and named volumes, build cache). Rules not given start with the maintenance defaults; the " +
				"schedule starts with the prune default of the schedule defaults and stays disabled unless enabled. Enabling a volume rule " +
				"needs volumeOptIn. 409 maintenance_policy_name_taken.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenancePolicyManage, Scope: ScopeEnvironment,
	}, h.create)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-maintenance-policy", Method: http.MethodGet, Path: path + "/{policyId}",
			Summary: "Get a maintenance policy", Description: "With its schedule (next run, recent scheduled runs) and the latest run's result.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapMaintenancePolicyRead, Scope: ScopeResource,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-maintenance-policy", Method: http.MethodPatch, Path: path + "/{policyId}",
			Summary: "Update a maintenance policy",
			Description: "Each rule given replaces its category's rule. Waiting runs of the policy are cancelled when its rules change " +
				"(they carry the old rules). Requires If-Match.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
				http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenancePolicyManage, Scope: ScopeResource,
	}, h.update)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-maintenance-policy", Method: http.MethodDelete, Path: path + "/{policyId}",
			Summary: "Delete a maintenance policy", DefaultStatus: http.StatusNoContent,
			Description: "Its schedule and waiting runs go with it; a run already in progress finishes. Requires If-Match.",
			Tags:        []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		},
		Capability: CapMaintenancePolicyManage, Scope: ScopeResource,
	}, h.remove)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-maintenance-policy-preview", Method: http.MethodPost, Path: path + "/{policyId}/previews",
			Summary: "Preview a maintenance policy",
			Description: "Asks the environment's agent which objects a run would remove now: candidate IDs with reasons, protected, " +
				"excluded and retained objects, and approximate reclaimed bytes. Optionally previews unsaved rules. Nothing is removed " +
				"or saved. 503 environment_offline when the agent is not connected.",
			Tags: []string{tagMaintenance}, Errors: append(slices.Clone(policyErrs), http.StatusUnprocessableEntity, http.StatusServiceUnavailable,
				http.StatusGatewayTimeout),
		},
		Capability: CapMaintenancePreview, Scope: ScopeResource,
	}, h.preview)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-maintenance-policy-run", Method: http.MethodPost, Path: path + "/{policyId}/runs",
			Summary: "Run a maintenance policy",
			Description: "Starts a prune.run job (202 + job) with the policy's enabled rules: candidates are recomputed and each is " +
				"revalidated right before its targeted removal; progress, skipped reasons, errors and bytes reclaimed are job items " +
				"and output (GET /api/v1/jobs/{id}, events stream). Needs confirm: true (409 prune_confirmation_required). background is " +
				"a presentation preference only: foreground and background runs are the same durable job and leaving the UI never " +
				"cancels it; cancel with POST /jobs/{id}/cancellations (between items). 409 maintenance_run_active while another run " +
				"of the policy is not finished, maintenance_policy_empty without enabled rules.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenanceRun, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.run)

	envPath := BasePath + "/environments/{environmentId}"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-prune-preview", Method: http.MethodPost, Path: envPath + "/prune-previews",
			Summary: "Preview a one-off prune",
			Description: "Asks the environment's agent which objects a prune with these rules would remove now, with the same " +
				"protections as policies (#32, stacks, saved specifications, backups). Nothing is removed or saved. 503 " +
				"environment_offline when the agent is not connected.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
				http.StatusUnprocessableEntity, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		},
		Capability: CapMaintenancePreview, Scope: ScopeEnvironment,
	}, h.previewManual)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-prune", Method: http.MethodPost, Path: envPath + "/prunes",
			Summary: "Run a one-off prune",
			Description: "Starts a prune.run job (202 + job) with these rules only, without a policy: candidates are recomputed and " +
				"each is revalidated right before its targeted removal. Needs confirm: true (409 prune_confirmation_required); " +
				"enabling a volume rule needs volumeOptIn.",
			Tags: []string{tagMaintenance}, DefaultStatus: http.StatusAccepted, Errors: []int{http.StatusForbidden, http.StatusNotFound,
				http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenanceRun, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.runManual)

	Register(a, Operation{Operation: huma.Operation{
		OperationID: "preview-maintenance-environments", Method: http.MethodPost, Path: path + "/{policyId}/environment-previews",
		Summary: "Preview a maintenance policy across its environments", Tags: []string{tagMaintenance},
		Errors: policyErrs,
	}, Capability: CapMaintenancePreview, Scope: ScopeResource}, h.previewEnvironments)
	Register(a, Operation{Operation: huma.Operation{
		OperationID: "run-maintenance-environments", Method: http.MethodPost, Path: path + "/{policyId}/environment-runs",
		Summary: "Run a maintenance policy across its environments", Tags: []string{tagMaintenance},
		Errors: policyErrs,
	}, Capability: CapMaintenanceRun, Scope: ScopeResource, Idempotency: IdempotencyStored}, h.runEnvironments)
}
