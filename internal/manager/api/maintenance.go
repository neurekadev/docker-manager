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
	"github.com/neurekadev/docker-manager/internal/manager/maintenance"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Docker maintenance (#14, #238): the one instance-wide maintenance setup,
// its previews and runs across the environments it covers, and one-off
// prunes of one environment. The flows live in internal/manager/maintenance;
// the agent computes and revalidates the candidates (internal/agent/prune).

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
	Setup(ctx context.Context) (domain.MaintenanceSetup, error)
	UpdateSetup(ctx context.Context, revision int64, p domain.MaintenanceSetupPatch) (before, after domain.MaintenanceSetup, err error)
	Preview(ctx context.Context, permit maintenance.Permit) ([]maintenance.EnvironmentPreview, error)
	Run(ctx context.Context, p authz.Principal, idempotencyKey string, permit maintenance.Permit) ([]domain.Job, error)
	PreviewManual(ctx context.Context, envID string, rules []domain.MaintenanceRule) (protocol.PrunePreviewOutput, error)
	RunManual(ctx context.Context, p authz.Principal, envID string, rules []domain.MaintenanceRule, idempotencyKey string) (domain.Job, error)
	ScheduleStatus(ctx context.Context, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
}

// MaintenanceRule is one category rule of maintenance or of a one-off prune.
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
	{Category: domain.PruneStoppedContainers, Label: "Stopped Containers", Labels: true,
		Description: "Containers in the selected states (exited and dead by default, created on request). Running, paused and restarting containers are never removed.",
		Limitations: []string{"Containers of Docker Manager stacks and containers with a saved Docker Manager recreate specification are protected.",
			"Anonymous volumes of removed containers stay; the anonymous-volume rule removes them."}},
	{Category: domain.PruneDanglingImages, Label: "Dangling Images", Labels: true,
		Description: "Untagged images no container uses.",
		Limitations: []string{"Image age is the creation time the Engine reports (like docker image prune --filter until)."}},
	{Category: domain.PruneUnusedImages, Label: "All Unused Images", Labels: true,
		Description: "Every image no container uses, tagged or not. Images left unused by an update are ordinary candidates: v1 has no rollback window.",
		Limitations: []string{"Images referenced by Docker Manager stacks and saved container specifications are protected.",
			"Sizes count layers shared with other images: the space freed can be smaller."}},
	{Category: domain.PruneUnusedNetworks, Label: "Unused Networks", Labels: true,
		Description: "Custom networks no container (running or stopped) uses.",
		Limitations: []string{"Predefined (bridge, host, none), swarm and Docker Manager stack networks are protected."}},
	{Category: domain.PruneAnonymousVolumes, Label: "Anonymous Volumes", Labels: true, DeletesData: true,
		Description: "Volumes the Engine created for anonymous mounts (label com.docker.volume.anonymous) that no container uses. Deletes their data.",
		Limitations: []string{"Anonymous volumes created before Docker 23 carry no label and count as named volumes.",
			"Needs its own explicit opt-in; volumes of Docker Manager stacks, Docker Manager's own and backup destinations are protected."}},
	{Category: domain.PruneNamedVolumes, Label: "Named Volumes", Labels: true, DeletesData: true,
		Description: "Named volumes no container uses. Deletes their data.",
		Limitations: []string{"Needs its own explicit opt-in; volumes of Docker Manager stacks, Docker Manager's own and backup destinations are protected."}},
	{Category: domain.PruneBuildCache, Label: "Build Cache", Labels: false,
		Description: "The BuildKit cache of the Engine's builder (the only builder Docker Manager uses). Default: dangling records only; all: every unused record. The keep-storage cap keeps the most recently used cache.",
		Limitations: []string{"Build cache records have no labels: exclude records by ID.",
			"The Engine has no per-record delete: Docker Manager prunes exactly one record ID per call, never the whole cache.",
			"A parent record is removed after its children, possibly in the next run."}},
}

func categoryInfo() []PruneCategoryInfo {
	out := make([]PruneCategoryInfo, len(pruneCategoryInfo))
	copy(out, pruneCategoryInfo)
	return out
}

// MaintenanceSchedule is the maintenance schedule (#13) and its state.
type MaintenanceSchedule struct {
	Cron          string           `json:"cron" example:"0 3 * * 0"`
	TimeZone      string           `json:"timeZone" example:"Europe/Berlin"`
	Enabled       bool             `json:"enabled" doc:"Scheduled runs (the setup's enabled flag); start disabled."`
	CatchUp       string           `json:"catchUp" enum:"skip" doc:"Prune runs missed while the manager was down are recorded, never run late."`
	InvalidReason string           `json:"invalidReason,omitempty"`
	NextRun       *ScheduleRunTime `json:"nextRun,omitempty"`
	RecentRuns    []ScheduleRun    `json:"recentRuns" doc:"Newest first: enqueued, missed, skipped (previous run active), rejected and failed scheduled runs."`
}

// MaintenanceRunSummary is the latest finished prune job of maintenance.
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

// MaintenanceSettings is the one maintenance setup: it covers every
// environment except the ones left out.
type MaintenanceSettings struct {
	ID                  string                 `json:"id" doc:"The policy ID of the setup's prune jobs and schedule (a job's policyId)."`
	Enabled             bool                   `json:"enabled" doc:"Runs on the schedule. Starts disabled; Run Now works either way."`
	Schedule            MaintenanceSchedule    `json:"schedule"`
	Rules               []MaintenanceRule      `json:"rules" doc:"One rule per category. The enabled ones together are the system cleanup."`
	SuggestedRules      []MaintenanceRule      `json:"suggestedRules" doc:"Docker Manager's shipped suggestions, one per category (every rule off, 30 days)."`
	Categories          []PruneCategoryInfo    `json:"categories"`
	ExcludeEnvironments []string               `json:"excludeEnvironments" doc:"IDs of the environments left out; every other environment is covered, also ones added later."`
	LastRun             *MaintenanceRunSummary `json:"lastRun,omitempty" doc:"The latest finished prune job (one environment's)."`
	Actions             []string               `json:"actions" doc:"What the caller may do: maintenance_policy.manage, maintenance.preview, maintenance.run."`
	Revision            int64                  `json:"revision"`
	UpdatedAt           time.Time              `json:"updatedAt"`
}

// setupResource is the setup as an authorization resource: instance-wide,
// so only instance grants apply to it.
func setupResource(st domain.MaintenanceSetup) authz.Resource {
	return authz.Resource{Type: catalog.TypeMaintenancePolicy, ID: st.ID, Parents: []authz.ResourceRef{}}
}

func (h *maintenanceAPI) newSettings(ctx context.Context, st domain.MaintenanceSetup, v authz.View) MaintenanceSettings {
	out := MaintenanceSettings{ID: st.ID, Enabled: st.Enabled, Rules: newMaintenanceRules(st.Rules),
		SuggestedRules: newMaintenanceRules(domain.SuggestedMaintenanceRules()), Categories: categoryInfo(),
		ExcludeEnvironments: append([]string{}, st.ExcludeEnvironments...), Actions: Actions(v), Revision: st.Revision,
		UpdatedAt: st.UpdatedAt}
	sched := MaintenanceSchedule{Cron: st.Cron, TimeZone: st.TimeZone, Enabled: st.Enabled, CatchUp: "skip", RecentRuns: []ScheduleRun{}}
	if sc, runs, ok, err := h.svc.ScheduleStatus(ctx, st.ID, 10); err == nil && ok {
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
	if r := st.LastRun; r != nil {
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

// PrunePreview is what a prune of one environment would remove now.
type PrunePreview struct {
	EnvironmentID string                 `json:"environmentId"`
	At            time.Time              `json:"at"`
	Remove        int                    `json:"remove"`
	Bytes         int64                  `json:"bytes"`
	Categories    []PruneCategoryPreview `json:"categories"`
	Notes         []string               `json:"notes"`
}

var previewNotes = []string{
	"Nothing was removed. A run recomputes the candidates and revalidates each one immediately before deleting it: objects that became used, protected, excluded or too recent are skipped with the reason.",
	"Docker Manager's own containers, images, volumes and networks, Docker Manager stacks, saved container specifications and backup destinations are never removed.",
	"Sizes are approximate: image sizes count shared layers, and build cache shared with images frees less.",
	"One run removes at most 300 candidates; the rest waits for the next run.",
}

func newPrunePreview(env string, p protocol.PrunePreviewOutput) PrunePreview {
	out := PrunePreview{EnvironmentID: env, At: p.At, Categories: []PruneCategoryPreview{}, Notes: previewNotes}
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
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	var fe *domain.FieldError
	var ie *scheduler.InvalidError
	var active *domain.MaintenanceRunActiveError
	var de *domain.DockerError
	switch {
	case errors.Is(err, domain.ErrMaintenanceEmpty):
		return Conflict(CodeMaintenanceEmpty, "every maintenance rule is off; turn on at least one rule before running it")
	case errors.Is(err, domain.ErrMaintenanceNoEnvironments):
		return Conflict(CodeMaintenanceNoEnvironments, "maintenance leaves every environment out")
	case errors.As(err, &active):
		return Conflict(CodeMaintenanceRunActive, active.Error()+"; follow it with GET /api/v1/jobs/"+active.JobID)
	case errors.Is(err, domain.ErrEnvironmentNotFound):
		return NotFound("environment not found")
	case errors.Is(err, domain.ErrEnvironmentArchived):
		return Conflict(CodeEnvironmentArchived, "the environment is archived")
	case errors.As(err, &fe):
		return Invalid("invalid maintenance settings", Field("body."+fe.Field, fe.Message))
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

// setup loads the setup and the caller's view of it; capability must be
// granted on the instance (403 otherwise).
func (h *maintenanceAPI) setup(ctx context.Context, capability Capability) (MaintenanceService, authz.Principal, domain.MaintenanceSetup, authz.View, error) {
	svc, _, p, st, v, err := h.checked(ctx, capability)
	return svc, p, st, v, err
}

func (h *maintenanceAPI) checked(ctx context.Context, capability Capability) (MaintenanceService, authz.Checker, authz.Principal, domain.MaintenanceSetup, authz.View, error) {
	if h.svc == nil {
		return nil, nil, authz.Principal{}, domain.MaintenanceSetup{}, authz.View{}, Unavailable(CodeUnavailable, "maintenance is not available")
	}
	c, p, err := CheckerFor(ctx, h.authz)
	if err != nil {
		return nil, nil, p, domain.MaintenanceSetup{}, authz.View{}, err
	}
	st, err := h.svc.Setup(ctx)
	if err != nil {
		return nil, nil, p, st, authz.View{}, Internal(err)
	}
	if !c.Can(string(capability), setupResource(st)).Allowed {
		return nil, nil, p, st, authz.View{}, Forbidden("requires " + string(capability) + " on all environments")
	}
	return h.svc, c, p, st, authz.ViewOf(c, setupResource(st)), nil
}

// covering loads the setup like setup, and returns the check of every
// environment the setup covers: the service refuses the preview or run
// when a rule keeps the caller from capability in one of them (a deny
// rule on the environment), checking the very environments it acts on (a
// preview shows their objects, a run prunes them).
func (h *maintenanceAPI) covering(ctx context.Context, capability Capability) (MaintenanceService, authz.Principal, maintenance.Permit, error) {
	svc, c, p, st, _, err := h.checked(ctx, capability)
	if err != nil {
		return nil, p, nil, err
	}
	permit := func(env domain.Environment) error {
		in := authz.Resource{Type: catalog.TypeMaintenancePolicy, ID: st.ID, EnvironmentID: env.ID, Parents: []authz.ResourceRef{}}
		if !c.Can(string(capability), in).Allowed {
			return Forbidden("not permitted: " + string(capability) + " in environment " + env.Name +
				"; leave it out of maintenance or ask for access there")
		}
		return nil
	}
	return svc, p, permit, nil
}

// flatSetup is the audit diff form of the setup (rules by category).
func flatSetup(st domain.MaintenanceSetup) map[string]any {
	m := map[string]any{"enabled": st.Enabled, "cron": st.Cron, "timeZone": st.TimeZone, "excludeEnvironments": st.ExcludeEnvironments}
	for _, r := range st.Rules {
		m["rules."+r.Category] = newMaintenanceRule(r)
	}
	return m
}

type maintenanceSettingsOutput struct {
	ETagHeader
	Body MaintenanceSettings
}

type updateMaintenanceSettingsInput struct {
	IfMatchParam
	Body struct {
		Enabled  *bool `json:"enabled,omitempty" doc:"Run on the schedule."`
		Schedule *struct {
			Cron     *string `json:"cron,omitempty" minLength:"1" maxLength:"256" doc:"Five-field cron expression."`
			TimeZone *string `json:"timeZone,omitempty" minLength:"1" maxLength:"64" doc:"IANA time zone."`
		} `json:"schedule,omitempty"`
		Rules               []MaintenanceRule `json:"rules,omitempty" maxItems:"7" doc:"Each rule given replaces the rule of its category (send the whole rule). Enabling a volume rule needs volumeOptIn."`
		ExcludeEnvironments *[]string         `json:"excludeEnvironments,omitempty" maxItems:"256" doc:"The environments to leave out (replaces the list); IDs of environments that do not exist are dropped."`
	}
}

type maintenancePreviewItem struct {
	EnvironmentID string        `json:"environmentId"`
	Preview       *PrunePreview `json:"preview,omitempty"`
	ErrorClass    string        `json:"errorClass,omitempty" doc:"Why this environment could not be previewed: the error code (environment_offline, timeout, ...)."`
	ErrorMessage  string        `json:"errorMessage,omitempty" doc:"The error's message (not stable)."`
}

type maintenancePreviewOutput struct {
	Body struct {
		Items []maintenancePreviewItem `json:"items" doc:"One item per environment maintenance covers."`
	}
}

type runMaintenanceInput struct {
	IdempotencyKeyParam
	Body struct {
		Confirm bool `json:"confirm,omitempty" example:"true" doc:"Must be true: a run deletes the candidates and a completed deletion cannot be undone (409 prune_confirmation_required otherwise)."`
	}
}

type maintenanceJobsOutput struct {
	Body struct {
		Jobs []Job `json:"jobs"`
	}
}

type prunePreviewOutput struct{ Body PrunePreview }

type previewManualPruneInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	Body          struct {
		Rules []MaintenanceRule `json:"rules" minItems:"1" maxItems:"7" doc:"The rules of this prune only (one per category); categories not given are not pruned. At least one must be enabled."`
	}
}

type runManualPruneInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body struct {
		Rules   []MaintenanceRule `json:"rules" minItems:"1" maxItems:"7" doc:"The rules of this prune only (one per category); categories not given are not pruned. At least one must be enabled."`
		Confirm bool              `json:"confirm,omitempty" example:"true" doc:"Must be true: the prune deletes its candidates and a completed deletion cannot be undone (409 prune_confirmation_required otherwise)."`
	}
}

func (h *maintenanceAPI) get(ctx context.Context, _ *struct{}) (*maintenanceSettingsOutput, error) {
	_, _, st, v, err := h.setup(ctx, CapMaintenancePolicyRead)
	if err != nil {
		return nil, err
	}
	return &maintenanceSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)}, Body: h.newSettings(ctx, st, v)}, nil
}

func (h *maintenanceAPI) update(ctx context.Context, in *updateMaintenanceSettingsInput) (*maintenanceSettingsOutput, error) {
	svc, _, st, v, err := h.setup(ctx, CapMaintenancePolicyManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(st.Revision)); err != nil {
		return nil, err
	}
	patch := domain.MaintenanceSetupPatch{Enabled: in.Body.Enabled, Rules: pruneRulesOf(in.Body.Rules), ExcludeEnvironments: in.Body.ExcludeEnvironments}
	if s := in.Body.Schedule; s != nil {
		patch.Cron, patch.TimeZone = s.Cron, s.TimeZone
	}
	before, after, err := svc.UpdateSetup(ctx, st.Revision, patch)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		return nil, stale(before.Revision)
	}
	if err != nil {
		return nil, maintenanceError(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeMaintenancePolicy, ID: after.ID})
	audit.SetDiff(ctx, flatSetup(before), flatSetup(after))
	return &maintenanceSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: h.newSettings(ctx, after, v)}, nil
}

func (h *maintenanceAPI) preview(ctx context.Context, _ *struct{}) (*maintenancePreviewOutput, error) {
	svc, _, permit, err := h.covering(ctx, CapMaintenancePreview)
	if err != nil {
		return nil, err
	}
	previews, err := svc.Preview(ctx, permit)
	if err != nil {
		return nil, maintenanceError(err)
	}
	out := &maintenancePreviewOutput{}
	out.Body.Items = make([]maintenancePreviewItem, 0, len(previews))
	candidates := 0
	for _, p := range previews {
		item := maintenancePreviewItem{EnvironmentID: p.EnvironmentID}
		if p.Err != nil {
			item.ErrorClass, item.ErrorMessage = CodeInternal, "the preview failed"
			var e *Error
			if errors.As(maintenanceError(p.Err), &e) && e.Code != CodeInternal {
				item.ErrorClass, item.ErrorMessage = e.Code, e.Message
			}
		} else {
			pv := newPrunePreview(p.EnvironmentID, p.Preview)
			item.Preview = &pv
			candidates += pv.Remove
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	audit.SetDetail(ctx, "candidates", candidates)
	return out, nil
}

func (h *maintenanceAPI) run(ctx context.Context, in *runMaintenanceInput) (*maintenanceJobsOutput, error) {
	svc, principal, permit, err := h.covering(ctx, CapMaintenanceRun)
	if err != nil {
		return nil, err
	}
	if !in.Body.Confirm {
		return nil, Conflict(CodePruneConfirmationRequired,
			"a prune run deletes its candidates and cannot be undone; review a preview and repeat the request with confirm: true")
	}
	jobs, err := svc.Run(ctx, principal, in.IdempotencyKey, permit)
	if err != nil {
		return nil, maintenanceError(err)
	}
	out := &maintenanceJobsOutput{}
	out.Body.Jobs = make([]Job, 0, len(jobs))
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		out.Body.Jobs = append(out.Body.Jobs, NewJob(job))
		ids = append(ids, job.ID)
	}
	audit.SetDetail(ctx, "job_ids", ids)
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
	if h.svc == nil {
		return nil, p, Unavailable(CodeUnavailable, "maintenance is not available")
	}
	return h.svc, p, nil
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
	res := newPrunePreview(in.EnvironmentID, out)
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

func registerMaintenance(a huma.API, deps Deps) {
	h := &maintenanceAPI{svc: deps.Maintenance, sched: deps.Schedules, authz: authz.OrDenyAll(deps.Authorizer)}
	path := BasePath + "/maintenance-settings"

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-maintenance-settings", Method: http.MethodGet, Path: path,
			Summary: "Get the maintenance settings",
			Description: "The one maintenance setup: whether it runs on its schedule, its rules (one per category: stopped " +
				"containers, dangling and all unused images, unused networks, anonymous and named volumes, build cache), the " +
				"environments it leaves out, the schedule's state and the latest run's result. It covers every other environment, " +
				"also ones added later.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapMaintenancePolicyRead, Scope: ScopeInstance,
	}, h.get)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-maintenance-settings", Method: http.MethodPatch, Path: path,
			Summary: "Change the maintenance settings",
			Description: "Each rule given replaces its category's rule; enabling a volume rule needs volumeOptIn. Waiting runs " +
				"are cancelled when the rules or the environments left out change (they carry the old ones). Requires If-Match.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired,
				http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenancePolicyManage, Scope: ScopeInstance,
	}, h.update)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-maintenance-preview", Method: http.MethodPost, Path: path + "/previews",
			Summary: "Preview maintenance",
			Description: "Asks the agent of every environment maintenance covers which objects a run would remove now: candidate " +
				"IDs with reasons, protected, excluded and retained objects, and approximate reclaimed bytes. An environment that " +
				"cannot answer (environment_offline, a timeout) reports its error; the others are previewed. Nothing is removed.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapMaintenancePreview, Scope: ScopeInstance,
	}, h.preview)

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-maintenance-run", Method: http.MethodPost, Path: path + "/runs",
			Summary: "Run maintenance",
			Description: "Starts one prune.run job per environment maintenance covers with the enabled rules: candidates are " +
				"recomputed and each is revalidated right before its targeted removal; progress, skipped reasons, errors and bytes " +
				"reclaimed are job items and output. Needs confirm: true (409 prune_confirmation_required). Leaving the UI never " +
				"cancels the jobs; cancel with POST /jobs/{id}/cancellations (between items). 409 maintenance_run_active while " +
				"another run is not finished, maintenance_empty without enabled rules, maintenance_no_environments when every " +
				"environment is left out.",
			Tags: []string{tagMaintenance}, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenanceRun, Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, h.run)

	envPath := BasePath + "/environments/{environmentId}"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-prune-preview", Method: http.MethodPost, Path: envPath + "/prune-previews",
			Summary: "Preview a one-off prune",
			Description: "Asks the environment's agent which objects a prune with these rules would remove now, with the same " +
				"protections as maintenance (#32, stacks, saved specifications, backups). Nothing is removed or saved. 503 " +
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
			Description: "Starts a prune.run job (202 + job) with these rules only, without the maintenance setup: candidates are " +
				"recomputed and each is revalidated right before its targeted removal. Needs confirm: true (409 " +
				"prune_confirmation_required); enabling a volume rule needs volumeOptIn.",
			Tags: []string{tagMaintenance}, DefaultStatus: http.StatusAccepted, Errors: []int{http.StatusForbidden, http.StatusNotFound,
				http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapMaintenanceRun, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.runManual)
}
