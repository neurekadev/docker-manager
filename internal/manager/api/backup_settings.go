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
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// The backup setup (#10, #246): what every environment backs up, the
// Primary and Secondary repositories, the schedule (#13), retention,
// previews and runs.

// backupSetupResource is the setup as an authorization resource:
// instance-wide, so only instance grants apply to it.
func backupSetupResource(st domain.BackupSetup) authz.Resource {
	return authz.Resource{Type: catalog.TypeBackupPolicy, ID: st.ID, Parents: []authz.ResourceRef{}}
}

// BackupRetention configures retention.
type BackupRetention struct {
	Last       int `json:"last,omitempty" minimum:"0" maximum:"10000"`
	Hourly     int `json:"hourly,omitempty" minimum:"0" maximum:"10000"`
	Daily      int `json:"daily,omitempty" example:"7" minimum:"0" maximum:"10000"`
	Weekly     int `json:"weekly,omitempty" minimum:"0" maximum:"10000"`
	Monthly    int `json:"monthly,omitempty" minimum:"0" maximum:"10000"`
	Yearly     int `json:"yearly,omitempty" minimum:"0" maximum:"10000"`
	WithinDays int `json:"withinDays,omitempty" minimum:"0" maximum:"10000" doc:"Keep every snapshot of the newest N days."`
	// MinKeep is the former minimum recovery floor: accepted (deprecated),
	// folded into Last, never returned.
	MinKeep int `json:"minKeep,omitempty" minimum:"0" maximum:"10000" deprecated:"true" doc:"Deprecated, never returned: the former minimum recovery floor, which did what last does. When rules are set, last is raised to it."`
	// ExpireDeletedDays removes the backups of deleted items.
	ExpireDeletedDays int  `json:"expireDeletedDays,omitempty" minimum:"0" maximum:"3650" doc:"Remove every backup of a stack Docker Manager no longer has or a standalone volume its environment no longer has once its newest backup is N days old (0 = off, the default). Nothing counts as deleted while the environment is offline or archived."`
	AfterBackup       bool `json:"afterBackup,omitempty" doc:"Apply retention automatically after every finished backup run."`
}

func newBackupRetention(r domain.BackupRetention) BackupRetention {
	return BackupRetention{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays, ExpireDeletedDays: r.ExpireDeletedDays, AfterBackup: r.AfterBackup}
}

func toRetention(r *BackupRetention) domain.BackupRetention {
	if r == nil {
		return domain.BackupRetention{}
	}
	out := domain.BackupRetention{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays, ExpireDeletedDays: r.ExpireDeletedDays, AfterBackup: r.AfterBackup}
	// The deprecated floor kept the newest N of each item on top of the
	// rules, which "last" does; without rules everything is kept anyway.
	rules := out.Last+out.Hourly+out.Daily+out.Weekly+out.Monthly+out.Yearly+out.WithinDays > 0
	if rules && r.MinKeep > out.Last {
		out.Last = r.MinKeep
	}
	return out
}

// BackupSchedule is the backup schedule (#13).
type BackupSchedule struct {
	Cron     string     `json:"cron" maxLength:"128" example:"0 2 * * *"`
	TimeZone string     `json:"timeZone" maxLength:"64" example:"Europe/Berlin"`
	NextRun  *time.Time `json:"nextRun,omitempty" doc:"While backups are on."`
}

// BackupSetSummary is a recent run.
type BackupSetSummary struct {
	ID         string            `json:"id"`
	State      string            `json:"state" enum:"pending,complete,partial,failed,skipped" doc:"A set with failed or missing members is partial, never complete. Skipped members (removed before their turn) count neither way; skipped: every member was skipped, nothing was backed up."`
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
	RepositoryID  string     `json:"repositoryId" doc:"The repository this copy goes to: a run backs every item up to the Primary and to the Secondary repository."`
	EnvironmentID string     `json:"environmentId,omitempty"`
	StackID       string     `json:"stackId,omitempty"`
	StackName     string     `json:"stackName,omitempty"`
	Volume        string     `json:"volume,omitempty"`
	State         string     `json:"state" enum:"pending,complete,partial,failed,missing,skipped" doc:"skipped: the stack or volume was removed before its turn (errorClass item_gone); not a failure."`
	ErrorClass    string     `json:"errorClass,omitempty"`
	SnapshotTime  *time.Time `json:"snapshotTime,omitempty" doc:"Per-host snapshot time: multi-host sets are not atomic."`
	JobID         string     `json:"jobId,omitempty"`
	BackupID      string     `json:"backupId,omitempty" doc:"The backup this member took (get-backup). Absent while it has none, after retention forgot it and when the caller cannot see it."`
}

// recentSets is the number of recent sets the settings carry.
const recentSets = 5

// memberBackups maps set members to the IDs of the backups they took.
type memberBackups map[string]string

func memberBackupKey(setID, repositoryID, scope, item, resticSnapshotID string) string {
	return setID + "\x00" + repositoryID + "\x00" + scope + "\x00" + item + "\x00" + resticSnapshotID
}

// of returns the backup ID of a member of set setID ("" when none).
func (b memberBackups) of(setID string, m domain.BackupSetMember) string {
	if m.SnapshotID == "" {
		return ""
	}
	return b[memberBackupKey(setID, m.RepositoryID, m.Scope, m.Item, m.SnapshotID)]
}

// setMemberBackups looks up the backups the members of sets took, those
// the caller can see, in one query (nil when there are none).
func setMemberBackups(ctx context.Context, svc BackupService, c authz.Checker, sets []domain.BackupSet) memberBackups {
	ids := make([]string, 0, len(sets))
	for _, s := range sets {
		ids = append(ids, s.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	snaps, err := svc.SetBackups(ctx, ids)
	if err != nil {
		return nil
	}
	out := memberBackups{}
	for _, sn := range snaps {
		if authz.ViewOf(c, backupResource(sn)).Visible() {
			out[memberBackupKey(sn.SetID, sn.RepositoryID, sn.Scope, sn.Item, sn.ResticSnapshotID)] = sn.ID
		}
	}
	return out
}

func newBackupSet(s domain.BackupSet, backups memberBackups) BackupSetSummary {
	out := BackupSetSummary{ID: s.ID, State: s.State, Origin: string(s.Origin), StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
		Members: []BackupSetMember{}}
	for _, m := range s.Members {
		out.Members = append(out.Members, BackupSetMember{Item: m.Item, Kind: m.Kind, Scope: m.Scope, RepositoryID: m.RepositoryID,
			EnvironmentID: m.EnvironmentID, StackID: m.StackID, StackName: m.StackName, Volume: m.Volume, State: m.State,
			ErrorClass: m.ErrorClass, SnapshotTime: m.SnapshotTime, JobID: m.JobID, BackupID: backups.of(s.ID, m)})
	}
	return out
}

// BackupSettings is the one backup setup: it backs up every managed stack
// and standalone volume of every environment it does not leave out, and
// the manager state, to the Primary repository and then to the Secondary.
type BackupSettings struct {
	ID                    string             `json:"id" doc:"The policy ID of the setup's runs: sets, backups and jobs carry it as policyId."`
	Enabled               bool               `json:"enabled" doc:"Runs on the schedule. Needs a Primary repository with a confirmed Recovery Key; Back Up Now works either way."`
	PrimaryRepositoryID   string             `json:"primaryRepositoryId" doc:"Where every run backs up first; empty: runs are refused (409 backup_no_primary)."`
	SecondaryRepositoryID string             `json:"secondaryRepositoryId" doc:"Where every run backs up a second, independent copy afterwards (empty: none). Removing the Primary repository promotes it."`
	Schedule              BackupSchedule     `json:"schedule"`
	ExcludeEnvironments   []string           `json:"excludeEnvironments" doc:"IDs of the environments left out; every other environment is covered, also ones added later."`
	ExcludeStacks         []string           `json:"excludeStacks" doc:"IDs of the stacks not backed up."`
	ExcludeVolumes        []string           `json:"excludeVolumes" doc:"environmentID/volumeName of the volumes not backed up: standalone ones and those of the stacks."`
	AnonymousVolumes      bool               `json:"anonymousVolumes" doc:"Also back up anonymous volumes (default off)."`
	BuildxVolumes         bool               `json:"buildxVolumes" doc:"Also back up buildx builder volumes (buildx_buildkit_<builder>_state: rebuildable build cache; default off)."`
	ExternalBinds         bool               `json:"externalBinds" doc:"Also back up the stacks' bind mounts outside their project directories (default off). Each agent backs up only those below its DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST."`
	IncludeMetrics        bool               `json:"includeMetrics" doc:"Include the metrics database in the manager state (excluded by default)."`
	Shutdown              bool               `json:"shutdown" doc:"Stop the affected containers during backups (default off; once per repository)."`
	Retention             BackupRetention    `json:"retention"`
	RecentSets            []BackupSetSummary `json:"recentSets" doc:"The newest runs, newest first."`
	Actions               []string           `json:"actions" doc:"What the caller may do: backup_policy.manage, backup.run, backup.retention."`
	Revision              int64              `json:"revision"`
	UpdatedAt             time.Time          `json:"updatedAt"`
}

func (h *backupsAPI) newSettings(ctx context.Context, svc BackupService, c authz.Checker, st domain.BackupSetup, v authz.View) BackupSettings {
	out := BackupSettings{ID: st.ID, Enabled: st.Enabled, PrimaryRepositoryID: st.PrimaryRepositoryID,
		SecondaryRepositoryID: st.SecondaryRepositoryID, Schedule: BackupSchedule{Cron: st.Cron, TimeZone: st.TimeZone},
		ExcludeEnvironments: orEmptyList(st.ExcludeEnvironments), ExcludeStacks: orEmptyList(st.ExcludeStacks),
		ExcludeVolumes: orEmptyList(st.ExcludeVolumes), AnonymousVolumes: st.AnonymousVolumes, BuildxVolumes: st.BuildxVolumes,
		ExternalBinds: st.ExternalBinds, IncludeMetrics: st.IncludeMetrics, Shutdown: st.Shutdown,
		Retention: newBackupRetention(st.Retention), RecentSets: []BackupSetSummary{}, Actions: Actions(v), Revision: st.Revision,
		UpdatedAt: st.UpdatedAt}
	// Running and retention are backup capabilities, not the setup's own.
	for _, cp := range []Capability{CapBackupRun, CapBackupRetention} {
		if c.Can(string(cp), backupSetupResource(st)).Allowed {
			out.Actions = append(out.Actions, string(cp))
		}
	}
	if sets, err := svc.RecentSets(ctx, []string{st.ID}, recentSets); err == nil {
		backups := setMemberBackups(ctx, svc, c, sets[st.ID])
		for _, s := range sets[st.ID] {
			out.RecentSets = append(out.RecentSets, newBackupSet(s, backups))
		}
	}
	if st.Enabled {
		if t, ok := svc.NextRuns(ctx, "backup", []string{st.ID})[st.ID]; ok {
			out.Schedule.NextRun = &t
		}
	}
	return out
}

// backupSetup loads the setup and the caller's view of it; capability must
// be granted on the instance (403 otherwise).
func (h *backupsAPI) backupSetup(ctx context.Context, capability Capability) (BackupService, authz.Checker, authz.Principal,
	domain.BackupSetup, authz.View, error) {
	svc, c, p, err := h.checker(ctx)
	if err != nil {
		return nil, nil, p, domain.BackupSetup{}, authz.View{}, err
	}
	st, err := svc.Setup(ctx)
	if err != nil {
		return nil, nil, p, st, authz.View{}, Internal(err)
	}
	if !c.Can(string(capability), backupSetupResource(st)).Allowed {
		return nil, nil, p, st, authz.View{}, Forbidden("requires " + string(capability) + " on all environments")
	}
	return svc, c, p, st, authz.ViewOf(c, backupSetupResource(st)), nil
}

type backupSettingsOutput struct {
	ETagHeader
	Body BackupSettings
}

func (h *backupsAPI) getSettings(ctx context.Context, _ *struct{}) (*backupSettingsOutput, error) {
	svc, c, _, st, v, err := h.backupSetup(ctx, CapBackupPolicyRead)
	if err != nil {
		return nil, err
	}
	return &backupSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(st.Revision)}, Body: h.newSettings(ctx, svc, c, st, v)}, nil
}

// backupSettingsBody is the editable part of the setup (nil = unchanged).
type backupSettingsBody struct {
	Enabled               *bool   `json:"enabled,omitempty" doc:"Run on the schedule. Needs a Primary repository with a confirmed Recovery Key."`
	PrimaryRepositoryID   *string `json:"primaryRepositoryId,omitempty" maxLength:"64" doc:"Making the Secondary repository the Primary one swaps the two (send both)."`
	SecondaryRepositoryID *string `json:"secondaryRepositoryId,omitempty" maxLength:"64" doc:"Empty: no Secondary repository."`
	Schedule              *struct {
		Cron     *string `json:"cron,omitempty" minLength:"1" maxLength:"128" doc:"Five-field cron expression."`
		TimeZone *string `json:"timeZone,omitempty" minLength:"1" maxLength:"64" doc:"IANA time zone."`
	} `json:"schedule,omitempty"`
	ExcludeEnvironments *[]string        `json:"excludeEnvironments,omitempty" maxItems:"256" doc:"The environments to leave out (replaces the list)."`
	ExcludeStacks       *[]string        `json:"excludeStacks,omitempty" maxItems:"256"`
	ExcludeVolumes      *[]string        `json:"excludeVolumes,omitempty" maxItems:"256" doc:"environmentID/volumeName."`
	AnonymousVolumes    *bool            `json:"anonymousVolumes,omitempty"`
	BuildxVolumes       *bool            `json:"buildxVolumes,omitempty"`
	ExternalBinds       *bool            `json:"externalBinds,omitempty"`
	IncludeMetrics      *bool            `json:"includeMetrics,omitempty"`
	Shutdown            *bool            `json:"shutdown,omitempty"`
	Retention           *BackupRetention `json:"retention,omitempty" doc:"Replaces the retention (send all of it)."`
}

func (b backupSettingsBody) patch() domain.BackupSetupPatch {
	p := domain.BackupSetupPatch{Enabled: b.Enabled, PrimaryRepositoryID: b.PrimaryRepositoryID, SecondaryRepositoryID: b.SecondaryRepositoryID,
		ExcludeEnvironments: b.ExcludeEnvironments, ExcludeStacks: b.ExcludeStacks, ExcludeVolumes: b.ExcludeVolumes,
		AnonymousVolumes: b.AnonymousVolumes, BuildxVolumes: b.BuildxVolumes, ExternalBinds: b.ExternalBinds,
		IncludeMetrics: b.IncludeMetrics, Shutdown: b.Shutdown}
	if s := b.Schedule; s != nil {
		p.Cron, p.TimeZone = s.Cron, s.TimeZone
	}
	if b.Retention != nil {
		r := toRetention(b.Retention)
		p.Retention = &r
	}
	return p
}

// draft applies the body to st (an unsaved copy for previews).
func (b backupSettingsBody) draft(st domain.BackupSetup) domain.BackupSetup {
	p := b.patch()
	if p.PrimaryRepositoryID != nil {
		st.PrimaryRepositoryID = *p.PrimaryRepositoryID
	}
	if p.SecondaryRepositoryID != nil {
		st.SecondaryRepositoryID = *p.SecondaryRepositoryID
	}
	for _, l := range []struct {
		from *[]string
		to   *[]string
	}{{p.ExcludeEnvironments, &st.ExcludeEnvironments}, {p.ExcludeStacks, &st.ExcludeStacks}, {p.ExcludeVolumes, &st.ExcludeVolumes}} {
		if l.from != nil {
			*l.to = *l.from
		}
	}
	for _, f := range []struct {
		from *bool
		to   *bool
	}{{p.AnonymousVolumes, &st.AnonymousVolumes}, {p.BuildxVolumes, &st.BuildxVolumes}, {p.ExternalBinds, &st.ExternalBinds},
		{p.IncludeMetrics, &st.IncludeMetrics}, {p.Shutdown, &st.Shutdown}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	if p.Retention != nil {
		st.Retention = *p.Retention
	}
	return st
}

type updateBackupSettingsInput struct {
	IfMatchParam
	Body backupSettingsBody
}

func (h *backupsAPI) updateSettings(ctx context.Context, in *updateBackupSettingsInput) (*backupSettingsOutput, error) {
	svc, c, _, st, v, err := h.backupSetup(ctx, CapBackupPolicyManage)
	if err != nil {
		return nil, err
	}
	if err := in.CheckIfMatch(RevisionETag(st.Revision)); err != nil {
		return nil, err
	}
	if s := in.Body.Schedule; s != nil {
		cron, tz := st.Cron, st.TimeZone
		if s.Cron != nil {
			cron = *s.Cron
		}
		if s.TimeZone != nil {
			tz = *s.TimeZone
		}
		if err := ValidateSchedule(cron, tz, "body.schedule"); err != nil {
			return nil, err
		}
	}
	_, after, err := svc.UpdateSetup(ctx, st.Revision, in.Body.patch())
	if errors.Is(err, domain.ErrRevisionMismatch) {
		cur, gerr := svc.Setup(ctx)
		if gerr != nil {
			return nil, Internal(gerr)
		}
		return nil, stale(cur.Revision)
	}
	if err != nil {
		return nil, backupError(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeBackupPolicy, ID: after.ID})
	return &backupSettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(after.Revision)}, Body: h.newSettings(ctx, svc, c, after, v)}, nil
}

// --- previews ---

// ScopePreview is the scope preview of the setup.
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
	Body *struct {
		Draft *backupSettingsBody `json:"draft,omitempty" doc:"Preview these unsaved changes instead of the stored settings."`
	}
}

type scopePreviewOutput struct{ Body ScopePreview }

func (h *backupsAPI) previewScope(ctx context.Context, in *scopePreviewInput) (*scopePreviewOutput, error) {
	svc, _, _, st, _, err := h.backupSetup(ctx, CapBackupPolicyRead)
	if err != nil {
		return nil, err
	}
	var draft *domain.BackupSetup
	if in.Body != nil && in.Body.Draft != nil {
		d := in.Body.Draft.draft(st)
		draft = &d
	}
	out, err := svc.PreviewScope(ctx, draft)
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
	Reasons    []string  `json:"reasons,omitempty" doc:"Rules keeping it: last, hourly, daily, weekly, monthly, yearly, within, newest; or deleted for a snapshot removed because its stack or volume was deleted."`
}

// RetentionLocationPreview is the preview of one repository location.
type RetentionLocationPreview struct {
	RepositoryID string              `json:"repositoryId"`
	Scope        string              `json:"scope"`
	Keep         int                 `json:"keep"`
	Forget       int                 `json:"forget"`
	Decisions    []RetentionDecision `json:"decisions"`
}

// RetentionPreview is the retention preview.
type RetentionPreview struct {
	Retention BackupRetention            `json:"retention"`
	Locations []RetentionLocationPreview `json:"locations"`
}

type retentionPreviewInput struct {
	Body *struct {
		Retention *BackupRetention `json:"retention,omitempty" doc:"Preview these rules instead of the stored ones."`
	}
}

type retentionPreviewOutput struct{ Body RetentionPreview }

func (h *backupsAPI) previewRetention(ctx context.Context, in *retentionPreviewInput) (*retentionPreviewOutput, error) {
	svc, _, _, _, _, err := h.backupSetup(ctx, CapBackupPolicyRead)
	if err != nil {
		return nil, err
	}
	var override *domain.BackupRetention
	if in.Body != nil && in.Body.Retention != nil {
		r := toRetention(in.Body.Retention)
		override = &r
	}
	locs, st, err := svc.PreviewRetention(ctx, override)
	if err != nil {
		return nil, backupError(err)
	}
	out := RetentionPreview{Retention: newBackupRetention(st.Retention), Locations: []RetentionLocationPreview{}}
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

type runBackupInput struct {
	IdempotencyKeyParam
	Body *struct {
		RetrySetID string `json:"retrySetId,omitempty" example:"0192f5e4-8b7a-7c3e-9d2f-1a2b3c4d5e6f" maxLength:"64" doc:"Re-run only the members of this set that did not complete (the set keeps its ID)."`
	}
}

type runBackupOutput struct{ Body BackupRun }

func (h *backupsAPI) runBackup(ctx context.Context, in *runBackupInput) (*runBackupOutput, error) {
	svc, c, pr, _, _, err := h.backupSetup(ctx, CapBackupRun)
	if err != nil {
		return nil, err
	}
	// The manager state is owner-only (manager.backup, #17): a run by
	// anyone else leaves it out.
	o := backups.RunOptions{Principal: pr, IdempotencyKey: in.IdempotencyKey,
		WithoutManager: !c.Can("manager.backup", authz.Instance()).Allowed}
	if in.Body != nil {
		o.RetrySetID = in.Body.RetrySetID
	}
	res, err := svc.RunNow(ctx, o)
	if err != nil {
		return nil, backupError(err)
	}
	out := BackupRun{Set: newBackupSet(res.Set, nil), Jobs: []Job{}}
	for _, j := range res.Jobs {
		out.Jobs = append(out.Jobs, NewJob(j))
	}
	return &runBackupOutput{Body: out}, nil
}

// RetentionRun is a started retention run.
type RetentionRun struct {
	Jobs []Job `json:"jobs"`
}

type retentionRunInput struct {
	IdempotencyKeyParam
	Body *struct {
		Confirm bool `json:"confirm" example:"true" doc:"Must be true: retention permanently forgets snapshots (preview them first)."`
	}
}

type retentionRunOutput struct{ Body RetentionRun }

func (h *backupsAPI) runRetention(ctx context.Context, in *retentionRunInput) (*retentionRunOutput, error) {
	svc, _, pr, _, _, err := h.backupSetup(ctx, CapBackupRetention)
	if err != nil {
		return nil, err
	}
	if in.Body == nil || !in.Body.Confirm {
		return nil, Invalid("retention forgets snapshots permanently: confirm it", Field("body.confirm", "must be true"))
	}
	js, err := svc.RetentionRun(ctx, pr, in.IdempotencyKey)
	if err != nil {
		return nil, backupError(err)
	}
	out := RetentionRun{Jobs: []Job{}}
	for _, j := range js {
		out.Jobs = append(out.Jobs, NewJob(j))
	}
	audit.SetDetail(ctx, "locations", len(js))
	return &retentionRunOutput{Body: out}, nil
}

func registerBackupSettings(a huma.API, h *backupsAPI) {
	const path = BasePath + "/backup-settings"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-backup-settings", Method: http.MethodGet, Path: path,
			Summary: "Get the backup settings",
			Description: "The one backup setup (#246): what every environment it does not leave out backs up, the Primary and " +
				"Secondary repositories, the schedule, retention and the recent runs (members with their backupId). Needs " +
				"backup_policy.read on the instance.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeInstance,
	}, h.getSettings)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-backup-settings", Method: http.MethodPatch, Path: path,
			Summary: "Update the backup settings",
			Description: "Requires If-Match. Turning backups on needs a Primary repository; every repository the setup writes to " +
				"must have its Recovery Key confirmed while backups are on. Making the Secondary repository the Primary one swaps " +
				"the two (send both).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusPreconditionFailed,
				http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyManage, Scope: ScopeInstance,
	}, h.updateSettings)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-scope-preview", Method: http.MethodPost, Path: path + "/scope-previews",
			Summary: "Preview what gets backed up",
			Description: "Asks each covered environment's agent for the effective sources (project directories, relative binds, " +
				"volumes), the excluded and blocked paths with reasons, sources that need an opt-in, estimated size, and with " +
				"shutdown on the containers that stop (stop order), the downtime warning and shared-volume conflicts. Docker " +
				"Manager's own containers and volumes are excluded (#32). draft previews unsaved changes. Nothing is stored.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeInstance,
	}, h.previewScope)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-run", Method: http.MethodPost, Path: path + "/runs",
			Summary: "Back up now", DefaultStatus: http.StatusCreated,
			Description: "Starts one backup set: a backup.run job per environment and a manager.backup job, for the Primary " +
				"repository and then for the Secondary one. The manager state is left out when the caller may not back it up " +
				"(owner only). retrySetId re-runs only the members of that set that did not complete (409 nothing_to_retry). " +
				"A new run is refused with 409 backup_run_active while a run is still queued or running (a retry is not), and " +
				"with 409 backup_no_primary without a Primary repository. Idempotency-Key covers every job of the run.",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRun, Scope: ScopeInstance, Idempotency: IdempotencyJob,
	}, h.runBackup)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-retention-preview", Method: http.MethodPost, Path: path + "/retention-previews",
			Summary: "Preview retention",
			Description: "Which Docker Manager backups the retention (or the rules in the body) would forget, per location and " +
				"stack/volume, with the rules that keep each one; the newest snapshot is always kept. It covers every backup run's " +
				"backups, also those of earlier backup policies. Execution applies the same decision (agents too old for that " +
				"apply it to the setup's own backups only).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupPolicyRead, Scope: ScopeInstance,
	}, h.previewRetention)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-backup-retention-run", Method: http.MethodPost, Path: path + "/retention-runs",
			Summary: "Apply retention now", DefaultStatus: http.StatusCreated,
			Description: "Queues retention (forget, then prune) on every location holding Docker Manager backups, serialized with " +
				"backups and restores on the repository. Requires confirm: true. The jobs report reclaimed space and failures " +
				"(for example Object Lock refusing deletions).",
			Tags: []string{tagBackups}, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapBackupRetention, Scope: ScopeInstance, Idempotency: IdempotencyJob,
	}, h.runRetention)
}
