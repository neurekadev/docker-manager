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
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Backup policies (#10): selections, schedule (#13), retention, previews
// and runs.

func backupPolicyResource(id string) authz.Resource {
	return authz.Resource{Type: catalog.TypeBackupPolicy, ID: id, Parents: []authz.ResourceRef{}}
}

// BackupStackSelection selects one stack of a policy.
type BackupStackSelection struct {
	StackID          string   `json:"stackId" maxLength:"64"`
	VolumeInclude    []string `json:"volumeInclude,omitempty" maxItems:"64" doc:"Back up only these named volumes (Compose keys or Docker names); default: every named volume of the stack."`
	VolumeExclude    []string `json:"volumeExclude,omitempty" maxItems:"64" doc:"Named volumes not backed up."`
	AnonymousVolumes bool     `json:"anonymousVolumes,omitempty" doc:"Also back up anonymous volumes (default off)."`
	PathExcludes     []string `json:"pathExcludes,omitempty" maxItems:"256" doc:"Paths relative to the project directory that are not backed up (relative bind sources inside the project directory are included by default)."`
	ExternalPaths    []string `json:"externalPaths,omitempty" maxItems:"64" doc:"Absolute bind sources outside the project directory to include (e.g. ../data resolved, or /srv/data). Each also needs the agent's DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST; never included implicitly."`
}

// BackupVolumeSelection selects one standalone named volume.
type BackupVolumeSelection struct {
	EnvironmentID string   `json:"environmentId" maxLength:"64"`
	Volume        string   `json:"volume" maxLength:"255"`
	PathExcludes  []string `json:"pathExcludes,omitempty" maxItems:"256" doc:"Paths relative to the volume root that are not backed up."`
}

// BackupRetention configures retention.
type BackupRetention struct {
	Last        int  `json:"last,omitempty" minimum:"0" maximum:"10000"`
	Hourly      int  `json:"hourly,omitempty" minimum:"0" maximum:"10000"`
	Daily       int  `json:"daily,omitempty" example:"7" minimum:"0" maximum:"10000"`
	Weekly      int  `json:"weekly,omitempty" minimum:"0" maximum:"10000"`
	Monthly     int  `json:"monthly,omitempty" minimum:"0" maximum:"10000"`
	Yearly      int  `json:"yearly,omitempty" minimum:"0" maximum:"10000"`
	WithinDays  int  `json:"withinDays,omitempty" minimum:"0" maximum:"10000" doc:"Keep every snapshot of the newest N days."`
	MinKeep     int  `json:"minKeep,omitempty" minimum:"0" maximum:"10000" doc:"Minimum recovery floor: the newest N snapshots of each stack/volume are always kept (at least 1 when rules are set)."`
	AfterBackup bool `json:"afterBackup,omitempty" doc:"Apply retention automatically after every successful backup of the policy."`
}

// BackupSchedule is a policy's schedule (#13).
type BackupSchedule struct {
	Cron     string     `json:"cron" maxLength:"128" example:"0 2 * * *"`
	TimeZone string     `json:"timeZone" maxLength:"64" example:"Europe/Berlin"`
	Enabled  bool       `json:"enabled" doc:"New policies start disabled; enabling needs every repository's Recovery Key confirmed."`
	NextRun  *time.Time `json:"nextRun,omitempty"`
}

// BackupSetSummary is a policy's recent run.
type BackupSetSummary struct {
	ID         string            `json:"id"`
	State      string            `json:"state" enum:"pending,complete,partial,failed" doc:"A set with failed or missing members is partial, never complete."`
	Origin     string            `json:"origin" enum:"manual,scheduled,api_token"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Members    []BackupSetMember `json:"members"`
}

// BackupSetMember is one planned snapshot of a set.
type BackupSetMember struct {
	Item          string     `json:"item" example:"stack/0190a6e0-..."`
	Kind          string     `json:"kind" enum:"manager_state,stack,volume"`
	Scope         string     `json:"scope"`
	EnvironmentID string     `json:"environmentId,omitempty"`
	StackID       string     `json:"stackId,omitempty"`
	StackName     string     `json:"stackName,omitempty"`
	Volume        string     `json:"volume,omitempty"`
	State         string     `json:"state" enum:"pending,complete,partial,failed,missing"`
	ErrorClass    string     `json:"errorClass,omitempty"`
	SnapshotTime  *time.Time `json:"snapshotTime,omitempty" doc:"Per-host snapshot time: multi-host sets are not atomic."`
	JobID         string     `json:"jobId,omitempty"`
}

func newBackupSet(s domain.BackupSet) BackupSetSummary {
	out := BackupSetSummary{ID: s.ID, State: s.State, Origin: string(s.Origin), StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
		Members: []BackupSetMember{}}
	for _, m := range s.Members {
		out.Members = append(out.Members, BackupSetMember{Item: m.Item, Kind: m.Kind, Scope: m.Scope, EnvironmentID: m.EnvironmentID,
			StackID: m.StackID, StackName: m.StackName, Volume: m.Volume, State: m.State, ErrorClass: m.ErrorClass,
			SnapshotTime: m.SnapshotTime, JobID: m.JobID})
	}
	return out
}

// BackupPolicy is a backup policy (instance resource).
//
// Shaping (#17): backup_policy.read shows it in full; any other capability
// on it only id, name and enabled.
type BackupPolicy struct {
	ID                      string                  `json:"id"`
	Name                    string                  `json:"name" example:"Nightly system backup"`
	Scope                   string                  `json:"scope" enum:"all,environment"`
	EnvironmentID           string                  `json:"environmentId,omitempty"`
	ExcludeStacks           []string                `json:"excludeStacks"`
	ExcludeVolumes          []string                `json:"excludeVolumes" doc:"Volumes not backed up: standalone ones and those of the selected stacks."`
	AnonymousVolumes        bool                    `json:"anonymousVolumes" doc:"Also back up anonymous volumes (default off)."`
	Enabled                 bool                    `json:"enabled"`
	View                    string                  `json:"view" enum:"minimal,full"`
	Actions                 []string                `json:"actions"`
	RepositoryID            string                  `json:"repositoryId,omitempty"`
	EnvironmentRepositories map[string]string       `json:"environmentRepositories,omitempty" doc:"Per-environment repository (local repositories live on each environment's agent)."`
	IncludeManagerState     bool                    `json:"includeManagerState"`
	IncludeMetrics          bool                    `json:"includeMetrics" doc:"Include the metrics database (excluded by default)."`
	Stacks                  []BackupStackSelection  `json:"stacks"`
	Volumes                 []BackupVolumeSelection `json:"volumes"`
	Shutdown                bool                    `json:"shutdown" doc:"Stop the affected containers during backups (default off)."`
	Schedule                *BackupSchedule         `json:"schedule,omitempty"`
	Retention               *BackupRetention        `json:"retention,omitempty"`
	RecentSets              []BackupSetSummary      `json:"recentSets,omitempty"`
	Revision                int64                   `json:"revision,omitempty"`
	CreatedAt               time.Time               `json:"createdAt,omitzero"`
	UpdatedAt               time.Time               `json:"updatedAt,omitzero"`
}

func newBackupPolicy(p domain.BackupPolicy, v authz.View) BackupPolicy {
	scope := "environment"
	if p.EnvironmentID == "" {
		scope = "all"
	}
	out := BackupPolicy{ID: p.ID, Name: p.Name, Scope: scope, EnvironmentID: p.EnvironmentID, Enabled: p.Enabled, View: v.Level.String(), Actions: Actions(v),
		Stacks: []BackupStackSelection{}, Volumes: []BackupVolumeSelection{}}
	if !v.Full() {
		return out
	}
	out.RepositoryID, out.EnvironmentRepositories = p.RepositoryID, p.EnvironmentRepos
	out.ExcludeStacks, out.ExcludeVolumes, out.AnonymousVolumes = p.ExcludeStacks, p.ExcludeVolumes, p.AnonymousVolumes
	if out.ExcludeStacks == nil {
		out.ExcludeStacks = []string{}
	}
	if out.ExcludeVolumes == nil {
		out.ExcludeVolumes = []string{}
	}
	out.IncludeManagerState, out.IncludeMetrics, out.Shutdown = p.IncludeManager, p.IncludeMetrics, p.Shutdown
	for _, s := range p.Stacks {
		out.Stacks = append(out.Stacks, BackupStackSelection{StackID: s.StackID, VolumeInclude: s.VolumeInclude, VolumeExclude: s.VolumeExclude,
			AnonymousVolumes: s.AnonymousVolumes, PathExcludes: s.PathExcludes, ExternalPaths: s.ExternalPaths})
	}
	for _, s := range p.Volumes {
		out.Volumes = append(out.Volumes, BackupVolumeSelection{EnvironmentID: s.EnvironmentID, Volume: s.Volume, PathExcludes: s.PathExcludes})
	}
	out.Schedule = &BackupSchedule{Cron: p.Cron, TimeZone: p.TimeZone, Enabled: p.Enabled}
	r := p.Retention
	out.Retention = &BackupRetention{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays, MinKeep: r.MinKeep, AfterBackup: r.AfterBackup}
	out.Revision, out.CreatedAt, out.UpdatedAt = p.Revision, p.CreatedAt, p.UpdatedAt
	return out
}

func toStackSelections(in []BackupStackSelection) []domain.BackupStackSelection {
	out := make([]domain.BackupStackSelection, 0, len(in))
	for _, s := range in {
		out = append(out, domain.BackupStackSelection{StackID: s.StackID, VolumeInclude: s.VolumeInclude, VolumeExclude: s.VolumeExclude,
			AnonymousVolumes: s.AnonymousVolumes, PathExcludes: s.PathExcludes, ExternalPaths: s.ExternalPaths})
	}
	return out
}

func toVolumeSelections(in []BackupVolumeSelection) []domain.BackupVolumeSelection {
	out := make([]domain.BackupVolumeSelection, 0, len(in))
	for _, s := range in {
		out = append(out, domain.BackupVolumeSelection{EnvironmentID: s.EnvironmentID, Volume: s.Volume, PathExcludes: s.PathExcludes})
	}
	return out
}

func toRetention(r *BackupRetention) domain.BackupRetention {
	if r == nil {
		return domain.BackupRetention{}
	}
	return domain.BackupRetention{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays, MinKeep: r.MinKeep, AfterBackup: r.AfterBackup}
}

// policyInputBody is the editable part of a policy.
type policyInputBody struct {
	Name                    string                  `json:"name" minLength:"1" maxLength:"100"`
	Scope                   string                  `json:"scope" enum:"all,environment"`
	EnvironmentID           string                  `json:"environmentId,omitempty" maxLength:"64"`
	ExcludeStacks           []string                `json:"excludeStacks,omitempty" maxItems:"256"`
	ExcludeVolumes          []string                `json:"excludeVolumes,omitempty" maxItems:"256" doc:"Volume names (environmentID/name for all environments) not backed up: standalone ones and those of the selected stacks."`
	AnonymousVolumes        bool                    `json:"anonymousVolumes,omitempty" doc:"Also back up anonymous volumes (default off)."`
	RepositoryID            string                  `json:"repositoryId" minLength:"1" maxLength:"64"`
	EnvironmentRepositories map[string]string       `json:"environmentRepositories,omitempty"`
	IncludeManagerState     bool                    `json:"includeManagerState,omitempty" doc:"Back up the manager's state (owner only: manager backups are owner-only)."`
	IncludeMetrics          bool                    `json:"includeMetrics,omitempty"`
	Stacks                  []BackupStackSelection  `json:"stacks,omitempty" maxItems:"128"`
	Volumes                 []BackupVolumeSelection `json:"volumes,omitempty" maxItems:"128"`
	Shutdown                bool                    `json:"shutdown,omitempty"`
	Schedule                *BackupSchedule         `json:"schedule,omitempty" doc:"Default: the instance default of the backup kind, disabled."`
	Retention               *BackupRetention        `json:"retention,omitempty"`
}

func (b policyInputBody) domain() domain.BackupPolicy {
	p := domain.BackupPolicy{Name: b.Name, EnvironmentID: b.EnvironmentID, ExcludeStacks: b.ExcludeStacks, ExcludeVolumes: b.ExcludeVolumes,
		AnonymousVolumes: b.AnonymousVolumes, RepositoryID: b.RepositoryID, EnvironmentRepos: b.EnvironmentRepositories,
		IncludeManager: b.IncludeManagerState, IncludeMetrics: b.IncludeMetrics, Stacks: toStackSelections(b.Stacks),
		Volumes: toVolumeSelections(b.Volumes), Shutdown: b.Shutdown, Retention: toRetention(b.Retention)}
	if b.Schedule != nil {
		p.Cron, p.TimeZone, p.Enabled = b.Schedule.Cron, b.Schedule.TimeZone, b.Schedule.Enabled
	}
	return p
}

type backupPolicyIDInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
}

type backupPolicyOutput struct {
	ETagHeader
	Body BackupPolicy
}

type backupPolicyListOutput struct{ Body Page[BackupPolicy] }

func backupPolicyETag(p BackupPolicy) ETagHeader {
	if p.Revision == 0 {
		return ETagHeader{}
	}
	return ETagHeader{ETag: RevisionETag(p.Revision)}
}

func (h *backupsAPI) listPolicies(ctx context.Context, in *struct{ PageParams }) (*backupPolicyListOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("backup-policies")
	var after agentCursor
	if in.Cursor != "" {
		if err := DecodeCursorFor(in.Cursor, fp, &after); err != nil {
			return nil, err
		}
	}
	items, next, err := ScanPage(ctx, Scan[domain.BackupPolicy]{
		Limit: in.PageLimit(), After: after.ID,
		Fetch: func(ctx context.Context, afterID string, n int) ([]domain.BackupPolicy, error) {
			return svc.ListPolicies(ctx, afterID, n)
		},
		Position: func(p domain.BackupPolicy) string { return p.ID },
		Visible:  func(p domain.BackupPolicy) bool { return authz.ViewOf(c, backupPolicyResource(p.ID)).Visible() },
	})
	if err != nil {
		return nil, Internal(err)
	}
	out := make([]BackupPolicy, 0, len(items))
	for _, p := range items {
		out = append(out, newBackupPolicy(p, authz.ViewOf(c, backupPolicyResource(p.ID))))
	}
	cursor, err := nextCursor(fp, next)
	if err != nil {
		return nil, err
	}
	return &backupPolicyListOutput{Body: NewPage(out, cursor, nil)}, nil
}

func (h *backupsAPI) visiblePolicy(ctx context.Context, id string) (BackupService, authz.Checker, authz.Principal, domain.BackupPolicy, authz.View, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, nil, p, domain.BackupPolicy{}, authz.View{}, err
	}
	pol, err := svc.GetPolicy(ctx, id)
	if err != nil {
		return nil, nil, p, pol, authz.View{}, backupError(err)
	}
	v := authz.ViewOf(c, backupPolicyResource(pol.ID))
	if !v.Visible() {
		return nil, nil, p, pol, v, NotFound("backup policy not found")
	}
	return svc, c, p, pol, v, nil
}

func (h *backupsAPI) requirePolicy(ctx context.Context, id string, cp Capability) (BackupService, authz.Checker, authz.Principal, domain.BackupPolicy, authz.View, error) {
	svc, c, p, pol, v, err := h.visiblePolicy(ctx, id)
	if err != nil {
		return svc, c, p, pol, v, err
	}
	if !v.Has(string(cp)) {
		return svc, c, p, pol, v, Forbidden("not permitted: " + string(cp))
	}
	return svc, c, p, pol, v, nil
}

func (h *backupsAPI) policyOut(ctx context.Context, svc BackupService, p domain.BackupPolicy, v authz.View) *backupPolicyOutput {
	body := newBackupPolicy(p, v)
	if v.Full() {
		if sets, err := svc.ListSets(ctx, p.ID, 5); err == nil {
			for _, s := range sets {
				body.RecentSets = append(body.RecentSets, newBackupSet(s))
			}
		}
		if body.Schedule != nil && p.Enabled {
			body.Schedule.NextRun = svc.NextRun(ctx, "backup", p.ID)
		}
	}
	return &backupPolicyOutput{ETagHeader: backupPolicyETag(body), Body: body}
}

func (h *backupsAPI) getPolicy(ctx context.Context, in *backupPolicyIDInput) (*backupPolicyOutput, error) {
	svc, _, _, p, v, err := h.visiblePolicy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	return h.policyOut(ctx, svc, p, v), nil
}

// requireManagerStateOwner: including the manager state is owner-only
// (manager.backup is an owner-only capability, #17).
func requireManagerStateOwner(c authz.Checker, include bool) error {
	if include && !c.Can("manager.backup", authz.Instance()).Allowed {
		return Forbidden("only the instance owner may back up the manager state")
	}
	return nil
}

type createBackupPolicyInput struct {
	Body policyInputBody
}

func (h *backupsAPI) createPolicy(ctx context.Context, in *createBackupPolicyInput) (*backupPolicyOutput, error) {
	svc, c, _, err := h.checker(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Can(string(CapBackupPolicyManage), authz.Instance()).Allowed {
		return nil, Forbidden("not permitted: " + string(CapBackupPolicyManage))
	}
	if err := requireManagerStateOwner(c, in.Body.IncludeManagerState); err != nil {
		return nil, err
	}
	if in.Body.Scope != "all" && in.Body.Scope != "environment" {
		return nil, Invalid("invalid backup scope", Field("body.scope", "choose all or environment"))
	}
	if (in.Body.Scope == "all") != (in.Body.EnvironmentID == "") {
		return nil, Invalid("invalid backup scope", Field("body.environmentId", "choose one environment or leave empty for all"))
	}
	if len(in.Body.Stacks) > 0 || len(in.Body.Volumes) > 0 {
		return nil, Invalid("invalid backup scope", Field("body.stacks", "use exclusions; managed stacks and standalone volumes are included by default"))
	}
	if s := in.Body.Schedule; s != nil && (s.Cron != "" || s.TimeZone != "") {
		if err := ValidateSchedule(s.Cron, s.TimeZone, "body.schedule"); err != nil {
			return nil, err
		}
	}
	p, err := svc.CreatePolicy(ctx, in.Body.domain())
	if err != nil {
		return nil, backupError(err)
	}
	v := authz.ViewOf(c, backupPolicyResource(p.ID))
	if !v.Full() {
		v = authz.View{Level: authz.Full, Actions: []string{string(CapBackupPolicyRead)}}
	}
	return h.policyOut(ctx, svc, p, v), nil
}

type updateBackupPolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	IfMatchParam
	Body struct {
		Name                    *string                  `json:"name,omitempty" minLength:"1" maxLength:"100"`
		EnvironmentID           *string                  `json:"environmentId,omitempty"`
		ExcludeStacks           *[]string                `json:"excludeStacks,omitempty" maxItems:"256"`
		ExcludeVolumes          *[]string                `json:"excludeVolumes,omitempty" maxItems:"256"`
		AnonymousVolumes        *bool                    `json:"anonymousVolumes,omitempty"`
		RepositoryID            *string                  `json:"repositoryId,omitempty" maxLength:"64"`
		EnvironmentRepositories *map[string]string       `json:"environmentRepositories,omitempty"`
		IncludeManagerState     *bool                    `json:"includeManagerState,omitempty"`
		IncludeMetrics          *bool                    `json:"includeMetrics,omitempty"`
		Stacks                  *[]BackupStackSelection  `json:"stacks,omitempty" maxItems:"128"`
		Volumes                 *[]BackupVolumeSelection `json:"volumes,omitempty" maxItems:"128"`
		Shutdown                *bool                    `json:"shutdown,omitempty"`
		Schedule                *BackupSchedule          `json:"schedule,omitempty"`
		Retention               *BackupRetention         `json:"retention,omitempty"`
	}
}

func (h *backupsAPI) updatePolicy(ctx context.Context, in *updateBackupPolicyInput) (*backupPolicyOutput, error) {
	svc, c, _, p, v, err := h.requirePolicy(ctx, in.PolicyID, CapBackupPolicyManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	b := in.Body
	if b.Stacks != nil || b.Volumes != nil {
		return nil, Invalid("invalid backup scope", Field("body.stacks", "use exclusions; managed stacks and standalone volumes are included by default"))
	}
	include := p.IncludeManager
	if b.IncludeManagerState != nil {
		include = *b.IncludeManagerState
	}
	// Editing a policy that backs up the manager state is owner-only too.
	if err := requireManagerStateOwner(c, include); err != nil {
		return nil, err
	}
	pp := backups.PolicyPatch{Name: b.Name, EnvironmentID: b.EnvironmentID, ExcludeStacks: b.ExcludeStacks, ExcludeVolumes: b.ExcludeVolumes,
		AnonymousVolumes: b.AnonymousVolumes, RepositoryID: b.RepositoryID, EnvironmentRepos: b.EnvironmentRepositories,
		IncludeManager: b.IncludeManagerState, IncludeMetrics: b.IncludeMetrics, Shutdown: b.Shutdown}
	if s := b.Schedule; s != nil {
		if err := ValidateSchedule(s.Cron, s.TimeZone, "body.schedule"); err != nil {
			return nil, err
		}
		pp.Cron, pp.TimeZone, pp.Enabled = &s.Cron, &s.TimeZone, &s.Enabled
	}
	if b.Retention != nil {
		r := toRetention(b.Retention)
		pp.Retention = &r
	}
	_, after, err := svc.UpdatePolicy(ctx, p.ID, p.Revision, pp)
	if errors.Is(err, domain.ErrRevisionMismatch) {
		cur, gerr := svc.GetPolicy(ctx, p.ID)
		if gerr != nil {
			return nil, backupError(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, backupError(err)
	}
	return h.policyOut(ctx, svc, after, v), nil
}

type deleteBackupPolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	IfMatchParam
}

func (h *backupsAPI) deletePolicy(ctx context.Context, in *deleteBackupPolicyInput) (*struct{}, error) {
	svc, c, _, p, _, err := h.requirePolicy(ctx, in.PolicyID, CapBackupPolicyManage)
	if err != nil {
		return nil, err
	}
	if err := requireManagerStateOwner(c, p.IncludeManager); err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(p.Revision)); err != nil {
		return nil, err
	}
	if err := svc.DeletePolicy(ctx, p.ID, p.Revision); err != nil {
		if errors.Is(err, domain.ErrRevisionMismatch) {
			return nil, stale(p.Revision)
		}
		return nil, backupError(err)
	}
	return &struct{}{}, nil
}

// --- previews ---

// ScopePreview is a policy's scope preview.
type ScopePreview struct {
	Shutdown     bool                 `json:"shutdown"`
	Manager      *ManagerScopePreview `json:"manager,omitempty"`
	Environments []EnvironmentPreview `json:"environments"`
	Warnings     []string             `json:"warnings,omitempty"`
}

// ManagerScopePreview previews the manager-state member.
type ManagerScopePreview struct {
	RepositoryID    string   `json:"repositoryId"`
	DatabaseBytes   int64    `json:"databaseBytes"`
	MetricsBytes    int64    `json:"metricsBytes"`
	MetricsIncluded bool     `json:"metricsIncluded"`
	Notes           []string `json:"notes"`
}

// EnvironmentPreview is one environment's preview (from its agent).
type EnvironmentPreview struct {
	EnvironmentID   string                      `json:"environmentId"`
	EnvironmentName string                      `json:"environmentName,omitempty" example:"nas"`
	RepositoryID    string                      `json:"repositoryId"`
	ErrorClass      string                      `json:"errorClass,omitempty" doc:"The agent could not preview (agent_offline, timeout, ...)."`
	Items           []protocol.ScopePreviewItem `json:"items,omitempty"`
	Downtime        string                      `json:"downtime,omitempty"`
}

func newScopePreview(p backups.ScopePreview) ScopePreview {
	out := ScopePreview{Shutdown: p.Shutdown, Environments: []EnvironmentPreview{}, Warnings: p.Warnings}
	if m := p.Manager; m != nil {
		out.Manager = &ManagerScopePreview{RepositoryID: m.RepositoryID, DatabaseBytes: m.DatabaseBytes, MetricsBytes: m.MetricsBytes,
			MetricsIncluded: m.MetricsIncluded, Notes: m.Notes}
	}
	for _, e := range p.Environments {
		ep := EnvironmentPreview{EnvironmentID: e.EnvironmentID, EnvironmentName: e.EnvironmentName, RepositoryID: e.RepositoryID,
			ErrorClass: e.ErrorClass}
		if e.Output != nil {
			ep.Items, ep.Downtime = e.Output.Items, e.Output.Downtime
		}
		out.Environments = append(out.Environments, ep)
	}
	return out
}

type scopePreviewInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	Body     *struct {
		Draft *policyInputBody `json:"draft,omitempty" doc:"Preview these unsaved settings instead of the stored policy."`
	}
}

type scopePreviewOutput struct{ Body ScopePreview }

func (h *backupsAPI) previewScope(ctx context.Context, in *scopePreviewInput) (*scopePreviewOutput, error) {
	svc, c, _, p, _, err := h.requirePolicy(ctx, in.PolicyID, CapBackupPolicyRead)
	if err != nil {
		return nil, err
	}
	var draft *domain.BackupPolicy
	if in.Body != nil && in.Body.Draft != nil {
		d := in.Body.Draft.domain()
		if err := requireManagerStateOwner(c, d.IncludeManager); err != nil {
			return nil, err
		}
		d.ID = p.ID
		draft = &d
	}
	out, err := svc.PreviewScope(ctx, p.ID, draft)
	if err != nil {
		return nil, backupError(err)
	}
	return &scopePreviewOutput{Body: newScopePreview(out)}, nil
}

// RetentionDecision is the fate of one snapshot in a retention preview.
type RetentionDecision struct {
	SnapshotID string    `json:"snapshotId"`
	Time       time.Time `json:"time"`
	Item       string    `json:"item"`
	Keep       bool      `json:"keep"`
	Reasons    []string  `json:"reasons,omitempty" doc:"Rules keeping it: last, hourly, daily, weekly, monthly, yearly, within, floor, newest."`
}

// RetentionLocationPreview is the preview of one repository location.
type RetentionLocationPreview struct {
	RepositoryID string              `json:"repositoryId"`
	Scope        string              `json:"scope"`
	Keep         int                 `json:"keep"`
	Forget       int                 `json:"forget"`
	Decisions    []RetentionDecision `json:"decisions"`
}

// RetentionPreview is a policy's retention preview.
type RetentionPreview struct {
	Retention BackupRetention            `json:"retention"`
	Locations []RetentionLocationPreview `json:"locations"`
}

type retentionPreviewInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	Body     *struct {
		Retention *BackupRetention `json:"retention,omitempty" doc:"Preview these rules instead of the stored ones."`
	}
}

type retentionPreviewOutput struct{ Body RetentionPreview }

func (h *backupsAPI) previewRetention(ctx context.Context, in *retentionPreviewInput) (*retentionPreviewOutput, error) {
	svc, _, _, p, _, err := h.requirePolicy(ctx, in.PolicyID, CapBackupPolicyRead)
	if err != nil {
		return nil, err
	}
	var override *domain.BackupRetention
	if in.Body != nil && in.Body.Retention != nil {
		r := toRetention(in.Body.Retention)
		override = &r
	}
	locs, pol, err := svc.PreviewRetention(ctx, p.ID, override)
	if err != nil {
		return nil, backupError(err)
	}
	r := pol.Retention
	out := RetentionPreview{Retention: BackupRetention{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly,
		Yearly: r.Yearly, WithinDays: r.WithinDays, MinKeep: r.MinKeep, AfterBackup: r.AfterBackup}, Locations: []RetentionLocationPreview{}}
	for _, l := range locs {
		lp := RetentionLocationPreview{RepositoryID: l.RepositoryID, Scope: l.Scope, Decisions: []RetentionDecision{}}
		for _, d := range l.Decisions {
			lp.Decisions = append(lp.Decisions, RetentionDecision{SnapshotID: d.ID, Time: d.Time, Item: d.Item, Keep: d.Keep, Reasons: d.Reasons})
			if d.Keep {
				lp.Keep++
			} else {
				lp.Forget++
			}
		}
		out.Locations = append(out.Locations, lp)
	}
	return &retentionPreviewOutput{Body: out}, nil
}

// --- runs ---

// BackupRun is a started run (a backup set and its jobs).
type BackupRun struct {
	Set  BackupSetSummary `json:"set"`
	Jobs []Job            `json:"jobs"`
}

type runPolicyInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	IdempotencyKeyParam
	Body *struct {
		RetrySetID string `json:"retrySetId,omitempty" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f" maxLength:"64" doc:"Re-run only the members of this set that did not complete (the set keeps its ID)."`
	}
}

type runPolicyOutput struct{ Body BackupRun }

// authorizeRun checks backup.run on the policy and on everything a run
// touches (stacks, volumes, repositories), and owner authority for the
// manager state; the job engine checks every job again.
func (h *backupsAPI) authorizeRun(c authz.Checker, p domain.BackupPolicy, v authz.View, stacksEnv map[string]string) error {
	if !v.Has(string(CapBackupRun)) {
		return Forbidden("not permitted: " + string(CapBackupRun))
	}
	if err := requireManagerStateOwner(c, p.IncludeManager); err != nil {
		return err
	}
	repos := map[string]bool{p.RepositoryID: true}
	for _, r := range p.EnvironmentRepos {
		repos[r] = true
	}
	for id := range repos {
		if !c.Can(string(CapBackupRun), backupRepositoryResource(id)).Allowed {
			return Forbidden("not permitted: backup.run on repository " + id)
		}
	}
	for _, s := range p.Stacks {
		if !c.Can(string(CapBackupRun), authz.Resource{Type: catalog.TypeStack, ID: s.StackID, EnvironmentID: stacksEnv[s.StackID]}).Allowed {
			return Forbidden("not permitted: backup.run on stack " + s.StackID)
		}
	}
	for _, vol := range p.Volumes {
		if !c.Can(string(CapBackupRun), authz.Resource{Type: catalog.TypeVolume, ID: vol.Volume, EnvironmentID: vol.EnvironmentID}).Allowed {
			return Forbidden("not permitted: backup.run on volume " + vol.Volume)
		}
	}
	return nil
}

func (h *backupsAPI) runPolicy(ctx context.Context, in *runPolicyInput) (*runPolicyOutput, error) {
	svc, c, pr, p, v, err := h.visiblePolicy(ctx, in.PolicyID)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeRun(c, p, v, h.stackEnvironments(ctx, p)); err != nil {
		return nil, err
	}
	o := backups.RunOptions{Principal: pr, IdempotencyKey: in.IdempotencyKey}
	if in.Body != nil {
		o.RetrySetID = in.Body.RetrySetID
	}
	res, err := svc.RunPolicy(ctx, p.ID, o)
	if err != nil {
		return nil, backupError(err)
	}
	out := BackupRun{Set: newBackupSet(res.Set), Jobs: []Job{}}
	for _, j := range res.Jobs {
		out.Jobs = append(out.Jobs, NewJob(j))
	}
	return &runPolicyOutput{Body: out}, nil
}

// stackEnvironments maps the policy's stacks to their environments for
// authorization (unknown stacks map to "": the resource locator decides).
func (h *backupsAPI) stackEnvironments(ctx context.Context, p domain.BackupPolicy) map[string]string {
	out := map[string]string{}
	if h.deps.Stacks == nil {
		return out
	}
	for _, s := range p.Stacks {
		if st, err := h.deps.Stacks.Get(ctx, s.StackID); err == nil {
			out[s.StackID] = st.EnvironmentID
		}
	}
	return out
}

// RetentionRun is a started retention run.
type RetentionRun struct {
	Jobs []Job `json:"jobs"`
}

type retentionRunInput struct {
	PolicyID string `path:"policyId" maxLength:"64" doc:"Backup policy ID."`
	IdempotencyKeyParam
	Body *struct {
		Confirm bool `json:"confirm" example:"true" doc:"Must be true: retention permanently forgets snapshots (preview them first)."`
	}
}

type retentionRunOutput struct{ Body RetentionRun }

func (h *backupsAPI) runRetention(ctx context.Context, in *retentionRunInput) (*retentionRunOutput, error) {
	svc, _, pr, p, _, err := h.requirePolicy(ctx, in.PolicyID, CapBackupRetention)
	if err != nil {
		return nil, err
	}
	if in.Body == nil || !in.Body.Confirm {
		return nil, Invalid("retention forgets snapshots permanently: confirm it", Field("body.confirm", "must be true"))
	}
	js, err := svc.RetentionRun(ctx, p.ID, pr, in.IdempotencyKey)
	if err != nil {
		return nil, backupError(err)
	}
	out := RetentionRun{Jobs: []Job{}}
	for _, j := range js {
		out.Jobs = append(out.Jobs, NewJob(j))
	}
	audit.SetDetail(ctx, "policyId", p.ID)
	return &retentionRunOutput{Body: out}, nil
}

func registerBackupPolicies(a huma.API, h *backupsAPI) {
	editErrs := []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
		http.StatusPreconditionRequired, http.StatusUnprocessableEntity}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-backup-policies", Method: http.MethodGet, Path: BasePath + "/backup-policies",
			Summary: "List backup policies", Description: "Filtered per item (#17); minimal view: id, name, enabled.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeResource,
	}, h.listPolicies)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-policy", Method: http.MethodPost, Path: BasePath + "/backup-policies",
			Summary: "Create a backup policy", DefaultStatus: http.StatusCreated,
			Description: "A system backup policy: the manager state (owner only) and any managed stacks and standalone volumes across " +
				"environments, with per-stack volume/path rules (anonymous volumes and container shutdown default off; bind sources " +
				"outside a project directory only by explicit opt-in plus the agent's allowlist), a schedule (#13, starts disabled) " +
				"and retention with a minimum recovery floor. Policies belong to the instance: scheduled runs continue after their " +
				"creator is disabled or removed.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyManage, Scope: ScopeInstance,
	}, h.createPolicy)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup-policy", Method: http.MethodGet, Path: BasePath + "/backup-policies/{policyId}",
			Summary: "Get a backup policy", Description: "With the next run and the most recent sets (per-host snapshot times, partial sets).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusNotFound},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeResource,
	}, h.getPolicy)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-backup-policy", Method: http.MethodPatch, Path: BasePath + "/backup-policies/{policyId}",
			Summary: "Update a backup policy", Description: "Requires If-Match. Enabling needs every repository's Recovery Key confirmed.",
			Tags: []string{tagBackups}, Errors: editErrs,
		},
		Capability: CapBackupPolicyManage, Scope: ScopeResource,
	}, h.updatePolicy)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-backup-policy", Method: http.MethodDelete, Path: BasePath + "/backup-policies/{policyId}",
			Summary: "Delete a backup policy", DefaultStatus: http.StatusNoContent,
			Description: "Its sets and snapshots stay (backup history). Requires If-Match.",
			Tags:        []string{tagBackups}, Errors: editErrs,
		},
		Capability: CapBackupPolicyManage, Scope: ScopeResource,
	}, h.deletePolicy)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-policy-scope-preview", Method: http.MethodPost, Path: BasePath + "/backup-policies/{policyId}/scope-previews",
			Summary: "Preview a backup policy's scope",
			Description: "Asks each environment's agent for the effective sources (project directories, relative binds, volumes), the " +
				"excluded and blocked paths with reasons, sources that need an opt-in, estimated size, and with shutdown on the " +
				"containers that stop (stop order), the downtime warning and shared-volume conflicts. Docker Manager's own containers and " +
				"volumes are excluded (#32). Nothing is stored.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeResource,
	}, h.previewScope)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-policy-run", Method: http.MethodPost, Path: BasePath + "/backup-policies/{policyId}/runs",
			Summary: "Run a backup policy now", DefaultStatus: http.StatusCreated,
			Description: "Starts one backup set: a backup.run job per environment and, with the manager state, a manager.backup job " +
				"(owner only). Needs backup.run on the policy, its repositories, stacks and volumes. retrySetId re-runs only the " +
				"members of that set that did not complete (409 nothing_to_retry). Idempotency-Key covers every job of the run.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRun, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.runPolicy)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-policy-retention-preview", Method: http.MethodPost,
			Path: BasePath + "/backup-policies/{policyId}/retention-previews", Summary: "Preview a backup policy's retention",
			Description: "Which of the policy's snapshots its retention (or the rules in the body) would forget, per location and " +
				"stack/volume, with the rules that keep each one; the minimum recovery floor and the newest snapshot are always kept. " +
				"Execution applies the same decision.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeResource,
	}, h.previewRetention)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-policy-retention-run", Method: http.MethodPost,
			Path: BasePath + "/backup-policies/{policyId}/retention-runs", Summary: "Apply a backup policy's retention now",
			DefaultStatus: http.StatusCreated,
			Description: "Queues retention (forget, then prune) on every location holding the policy's snapshots, serialized with " +
				"backups and restores on the repository. Requires confirm: true. The jobs report reclaimed space and failures " +
				"(for example Object Lock refusing deletions).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRetention, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.runRetention)
}
