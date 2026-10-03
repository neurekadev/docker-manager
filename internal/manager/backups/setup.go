package backups

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// SetupName names the backup setup where a policy name is shown: sets,
// manifests, schedules and notifications.
const SetupName = "Backups"

// MaxExclusions bounds each exclusion list of the setup.
const MaxExclusions = 256

// Setup returns the backup setup.
func (s *Service) Setup(ctx context.Context) (domain.BackupSetup, error) {
	return store.GetBackupSetup(ctx, s.db)
}

// UpdateSetup edits the setup (If-Match revision). Enabling it needs a
// Primary repository; every repository it writes to must have its Recovery
// Key confirmed while it is enabled. Validation reads stacks and
// environments through their services, which use their own database
// handle: it runs outside any transaction, and the write is a
// compare-and-set on the revision.
func (s *Service) UpdateSetup(ctx context.Context, revision int64, p domain.BackupSetupPatch) (before, after domain.BackupSetup, err error) {
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return before, after, err
	}
	if st.Revision != revision {
		return before, after, domain.ErrRevisionMismatch
	}
	before = st
	if p.Enabled != nil {
		st.Enabled = *p.Enabled
	}
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
	for _, b := range []struct {
		from *bool
		to   *bool
	}{{p.AnonymousVolumes, &st.AnonymousVolumes}, {p.BuildxVolumes, &st.BuildxVolumes}, {p.ExternalBinds, &st.ExternalBinds},
		{p.IncludeMetrics, &st.IncludeMetrics}, {p.Shutdown, &st.Shutdown}} {
		if b.from != nil {
			*b.to = *b.from
		}
	}
	if p.Cron != nil {
		st.Cron = *p.Cron
	}
	if p.TimeZone != nil {
		st.TimeZone = *p.TimeZone
	}
	if p.Retention != nil {
		st.Retention = *p.Retention
	}
	st.Revision, st.UpdatedAt = revision+1, s.now()
	if err := s.validateSetup(ctx, s.db, &st); err != nil {
		return before, after, err
	}
	if err := store.UpdateBackupSetup(ctx, s.db, st, revision); err != nil {
		return before, after, err
	}
	audit.SetDiff(ctx, setupAuditView(before), setupAuditView(st))
	s.notify()
	s.primaryChanged(ctx, st)
	return before, st, nil
}

func setupAuditView(st domain.BackupSetup) map[string]any {
	return map[string]any{"enabled": st.Enabled, "primaryRepositoryId": st.PrimaryRepositoryID,
		"secondaryRepositoryId": st.SecondaryRepositoryID, "excludeEnvironments": st.ExcludeEnvironments,
		"excludeStacks": st.ExcludeStacks, "excludeVolumes": st.ExcludeVolumes, "anonymousVolumes": st.AnonymousVolumes,
		"buildxVolumes": st.BuildxVolumes, "externalBinds": st.ExternalBinds, "includeMetrics": st.IncludeMetrics,
		"shutdown": st.Shutdown, "cron": st.Cron, "timeZone": st.TimeZone, "retention": st.Retention}
}

// CheckPrimary tells the OnPrimaryMissing hook whether backups are on
// without a Primary repository now (at start).
func (s *Service) CheckPrimary(ctx context.Context) error {
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return err
	}
	s.primaryChanged(ctx, st)
	return nil
}

// primaryChanged tells the installed hook whether the setup is enabled
// without a Primary repository (runs are refused: an alert says so).
func (s *Service) primaryChanged(ctx context.Context, st domain.BackupSetup) {
	if s.opts.OnPrimaryMissing != nil {
		s.opts.OnPrimaryMissing(ctx, st.Enabled && st.PrimaryRepositoryID == "")
	}
}

// validateSetup normalizes and checks the setup.
func (s *Service) validateSetup(ctx context.Context, db bun.IDB, st *domain.BackupSetup) error {
	if err := scheduler.ValidateSpec(st.Cron, st.TimeZone); err != nil {
		return fieldErr("schedule", "%s", err.Error())
	}
	if err := validateRetention(st.Retention); err != nil {
		return err
	}
	for _, l := range []struct {
		field string
		list  []string
	}{{"excludeEnvironments", st.ExcludeEnvironments}, {"excludeStacks", st.ExcludeStacks}, {"excludeVolumes", st.ExcludeVolumes}} {
		if len(l.list) > MaxExclusions {
			return fieldErr(l.field, "at most %d entries", MaxExclusions)
		}
		for _, v := range l.list {
			if v == "" {
				return fieldErr(l.field, "entries cannot be empty")
			}
		}
	}
	for _, v := range st.ExcludeVolumes {
		env, name, ok := strings.Cut(v, "/")
		if !ok || env == "" || !protocol.ValidVolumeName(name) {
			return fieldErr("excludeVolumes", "%q is not environmentID/volumeName", v)
		}
	}
	slices.Sort(st.ExcludeEnvironments)
	st.ExcludeEnvironments = slices.Compact(st.ExcludeEnvironments)
	if st.SecondaryRepositoryID != "" && st.PrimaryRepositoryID == "" {
		return fieldErr("secondaryRepositoryId", "choose a Primary repository first")
	}
	if st.SecondaryRepositoryID != "" && st.SecondaryRepositoryID == st.PrimaryRepositoryID {
		return fieldErr("secondaryRepositoryId", "the Secondary repository must differ from the Primary one")
	}
	if st.Enabled && st.PrimaryRepositoryID == "" {
		return fieldErr("primaryRepositoryId", "choose a Primary repository before turning backups on")
	}
	for _, r := range []struct{ field, id string }{{"primaryRepositoryId", st.PrimaryRepositoryID},
		{"secondaryRepositoryId", st.SecondaryRepositoryID}} {
		if r.id == "" {
			continue
		}
		repo, err := store.GetBackupRepository(ctx, db, r.id)
		if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
			return fieldErr(r.field, "unknown backup repository")
		}
		if err != nil {
			return err
		}
		if st.Enabled && repo.State != domain.BackupRepositoryReady {
			return domain.ErrRecoveryKeyNotConfirmed
		}
	}
	return nil
}

// scopeSelections resolves the managed stacks and standalone volumes of
// every active environment the setup covers. Its volume exclusions and
// anonymous-volume switch apply to the stacks' volumes too.
func (s *Service) scopeSelections(ctx context.Context, st domain.BackupSetup) ([]domain.BackupStackSelection, []domain.BackupVolumeSelection, error) {
	envs, err := store.ListEnvironments(ctx, s.db, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return nil, nil, err
	}
	var stacksOut []domain.BackupStackSelection
	var volumesOut []domain.BackupVolumeSelection
	for _, env := range envs {
		if st.Excludes(env.ID) {
			continue
		}
		stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: env.ID})
		if err != nil {
			return nil, nil, err
		}
		excluded := excludedVolumes(st, env.ID)
		for _, stack := range stacks {
			if !slices.Contains(st.ExcludeStacks, stack.ID) {
				sel := domain.BackupStackSelection{StackID: stack.ID, VolumeExclude: excluded, AnonymousVolumes: st.AnonymousVolumes}
				if st.ExternalBinds {
					sel.ExternalPaths = externalBindSources(stack.Binds)
				}
				stacksOut = append(stacksOut, sel)
			}
		}
		names, err := s.standaloneVolumes(ctx, st, env.ID, stacks)
		if err != nil {
			return nil, nil, err
		}
		for _, name := range names {
			volumesOut = append(volumesOut, domain.BackupVolumeSelection{EnvironmentID: env.ID, Volume: name})
		}
	}
	return stacksOut, volumesOut, nil
}

// maxExternalPaths is the agent's limit of external paths per stack
// (protocol.BackupRules.Validate).
const maxExternalPaths = 64

// externalBindSources lists a stack's bind sources outside its project
// directory, each once: the setup's ExternalBinds opts them in, and the
// agent backs up only those below its external allowlist. Sources the
// agent would refuse as rules (not a clean absolute path, "/") are left
// out so they never fail the stack's backup.
func externalBindSources(binds []domain.StackBind) []string {
	var out []string
	for _, b := range binds {
		if len(out) == maxExternalPaths {
			break
		}
		if b.External && backup.AbsPath(b.Source) && !slices.Contains(out, b.Source) {
			out = append(out, b.Source)
		}
	}
	return out
}

// excludedVolumes returns the setup's excluded volume names in one
// environment (they are keyed environmentID/name).
func excludedVolumes(st domain.BackupSetup, environmentID string) []string {
	var out []string
	for _, key := range st.ExcludeVolumes {
		if env, name, ok := strings.Cut(key, "/"); ok && env == environmentID {
			out = append(out, name)
		}
	}
	return out
}

// standaloneVolumes lists the environment's volumes the setup selects: not
// Docker Manager's own, not a managed stack's (by label or by a stack
// container using it), not excluded (by the setup, or by the backup
// exclude label on the volume or on a container using it), and anonymous
// and buildx builder volumes only when the setup includes them.
// Docker Manager's temporary objects are left out too: temporary
// containers (protocol.IsHelperContainer) never count as users, so a
// volume only they use is not selected, and neither is a volume an
// environment migration created (protocol.LabelMigration) unless that
// migration succeeded.
func (s *Service) standaloneVolumes(ctx context.Context, st domain.BackupSetup, environmentID string, stacks []domain.Stack) ([]string, error) {
	return s.selectVolumes(ctx, st, environmentID, stacks, true)
}

// selectVolumes is standaloneVolumes; without skipTemporary it keeps the
// volumes of temporary containers and unfinished migrations (the prune
// protection of VolumeReferences, unchanged by that rule).
func (s *Service) selectVolumes(ctx context.Context, p domain.BackupSetup, environmentID string, stacks []domain.Stack,
	skipTemporary bool) ([]string, error) {
	if s.volumes == nil {
		return nil, nil
	}
	volumes, err := s.volumes.ListVolumes(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	containers, err := s.volumes.ListContainers(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	stackNames := map[string]bool{}
	for _, stack := range stacks {
		stackNames[stack.Name] = true
	}
	managedContainer, labeled, helper := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, container := range containers {
		if skipTemporary && protocol.IsHelperContainer(container.Name, container.Labels) {
			helper[container.ID] = true
			continue
		}
		if stackNames[container.Labels[protocol.ComposeProjectLabel]] {
			managedContainer[container.ID] = true
		}
		if protocol.BackupExcluded(container.Labels) {
			labeled[container.ID] = true
		}
	}
	excluded := excludedVolumes(p, environmentID)
	migrations := map[string]bool{}
	var out []string
	for _, volume := range volumes {
		_, anonymous := volume.Labels[protocol.AnonymousVolumeLabel]
		if volume.Stack != nil || volume.Protection != nil || stackNames[volume.Labels[protocol.ComposeProjectLabel]] ||
			slices.ContainsFunc(volume.UsedBy, func(ref protocol.ContainerRef) bool { return managedContainer[ref.ID] }) ||
			slices.Contains(excluded, volume.Name) || (anonymous && !p.AnonymousVolumes) ||
			(protocol.IsBuildxVolume(volume.Name) && !p.BuildxVolumes) || protocol.BackupExcluded(volume.Labels) ||
			slices.ContainsFunc(volume.UsedBy, func(ref protocol.ContainerRef) bool { return labeled[ref.ID] }) {
			continue
		}
		if skipTemporary {
			// Used, but only by temporary containers.
			if len(volume.UsedBy) > 0 && !slices.ContainsFunc(volume.UsedBy, func(ref protocol.ContainerRef) bool { return !helper[ref.ID] }) {
				continue
			}
			if id := protocol.LabelValue(volume.Labels, protocol.LabelMigration); id != "" {
				ok, known := migrations[id]
				if !known {
					if ok, err = s.migrationSucceeded(ctx, id); err != nil {
						return nil, err
					}
					migrations[id] = ok
				}
				if !ok {
					continue
				}
			}
		}
		out = append(out, volume.Name)
	}
	return out, nil
}

// migrationSucceeded reports whether the environment migration that
// created a volume (its protocol.LabelMigration value, the migration's job
// ID) succeeded. A failed, cancelled, interrupted or still running one
// left a partial copy the next migration removes; an unknown one is
// treated the same.
func (s *Service) migrationSucceeded(ctx context.Context, id string) (bool, error) {
	m, err := store.GetMigration(ctx, s.db, id)
	if errors.Is(err, domain.ErrMigrationNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return m.State == domain.MigrationCompleted || m.State == domain.MigrationSourceRemoved, nil
}

// VolumeReferences lists the standalone volumes of an environment the
// backup setup selects while backups are on: Docker maintenance (#14)
// never prunes them. Prune keeps protecting the volumes of temporary
// containers and unfinished migrations as before, although backups leave
// them out.
func (s *Service) VolumeReferences(ctx context.Context, environmentID string) ([]string, error) {
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if !st.Enabled || st.Excludes(environmentID) {
		return nil, nil
	}
	stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: environmentID})
	if err != nil {
		return nil, err
	}
	return s.selectVolumes(ctx, st, environmentID, stacks, false)
}

func (s *Service) environment(ctx context.Context, id string) (domain.Environment, error) {
	if s.opts.Environments == nil {
		return domain.Environment{ID: id}, nil
	}
	return s.opts.Environments.GetEnvironment(ctx, id)
}

func (s *Service) stack(ctx context.Context, id string) (domain.Stack, error) {
	if s.opts.Stacks == nil {
		return domain.Stack{}, domain.ErrStackNotFound
	}
	return s.opts.Stacks.Get(ctx, id)
}

// validateRetention checks the retention rules and expiry.
func validateRetention(r domain.BackupRetention) error {
	for f, msg := range retentionRules(r).Validate() {
		return fieldErr("retention."+f, "%s", msg)
	}
	if r.ExpireDeletedDays < 0 || r.ExpireDeletedDays > domain.MaxExpireDeletedDays {
		return fieldErr("retention.expireDeletedDays", "must be between 0 and %d", domain.MaxExpireDeletedDays)
	}
	return nil
}

// retentionActive reports whether retention would remove anything: rules
// are set or backups of deleted items expire.
func retentionActive(r domain.BackupRetention) bool {
	return !retentionRules(r).Empty() || r.ExpireDeletedDays > 0
}

// expiredItems lists the items of one location (snaps are its indexed
// snapshots) whose backups the setup's expiry removes: stacks Docker
// Manager no longer knows and standalone volumes the environment no longer
// has, whose newest backup is older than ExpireDeletedDays. Nothing is
// judged deleted when the environment is archived or unknown or its
// volumes cannot be listed (offline agent), and the manager's own state
// never is.
func (s *Service) expiredItems(ctx context.Context, p domain.BackupSetup, scope string, snaps []domain.BackupSnapshot) []string {
	days := p.Retention.ExpireDeletedDays
	envID, ok := backup.ScopeEnvironment(scope)
	if days <= 0 || !ok {
		return nil
	}
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	newest := map[string]domain.BackupSnapshot{}
	for _, sn := range snaps {
		if cur, seen := newest[sn.Item]; sn.Scope == scope && (!seen || sn.SnapshotTime.After(cur.SnapshotTime)) {
			newest[sn.Item] = sn
		}
	}
	var present map[string]bool // the environment's volumes, listed once
	listed := false
	var out []string
	for item, sn := range newest {
		if !sn.SnapshotTime.Before(cutoff) {
			continue
		}
		switch sn.Kind {
		case backup.MemberStack:
			if s.opts.Stacks == nil || sn.StackID == "" {
				continue
			}
			if _, err := s.stack(ctx, sn.StackID); errors.Is(err, domain.ErrStackNotFound) {
				out = append(out, item)
			}
		case backup.MemberVolume:
			if !listed {
				listed, present = true, s.environmentVolumes(ctx, envID)
			}
			if present != nil && sn.Volume != "" && !present[sn.Volume] {
				out = append(out, item)
			}
		}
	}
	slices.Sort(out)
	return out
}

// environmentVolumes lists the volume names of an active environment, nil
// when they cannot be known.
func (s *Service) environmentVolumes(ctx context.Context, envID string) map[string]bool {
	if s.volumes == nil || s.opts.Environments == nil {
		return nil
	}
	if env, err := s.environment(ctx, envID); err != nil || env.Status != domain.EnvironmentActive {
		return nil
	}
	volumes, err := s.volumes.ListVolumes(ctx, envID)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(volumes))
	for _, v := range volumes {
		out[v.Name] = true
	}
	return out
}

func retentionRules(r domain.BackupRetention) backup.RetentionRules {
	return backup.RetentionRules{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays}
}

// envPlan is one environment's part of a run.
type envPlan struct {
	EnvironmentID string
	Items         []protocol.BackupItem
	Stacks        []string
	Volumes       []string
}

// planItems groups the setup's selections by environment (sorted).
func (s *Service) planItems(ctx context.Context, st domain.BackupSetup) ([]envPlan, error) {
	stacks, volumes, err := s.scopeSelections(ctx, st)
	if err != nil {
		return nil, err
	}
	byEnv := map[string]*envPlan{}
	get := func(env string) *envPlan {
		if e, ok := byEnv[env]; ok {
			return e
		}
		e := &envPlan{EnvironmentID: env}
		byEnv[env] = e
		return e
	}
	for _, sel := range stacks {
		stack, err := s.stack(ctx, sel.StackID)
		if err != nil {
			return nil, errors.Join(domain.ErrStackNotFound, errors.New("stack "+sel.StackID))
		}
		e := get(stack.EnvironmentID)
		ref := protocol.ProjectRef{Root: stack.Root, RootPath: stack.RootPath, Dir: stack.Dir, ProjectName: stack.Name,
			ConfigFiles: stack.ConfigFiles, EnvFiles: stack.EnvFiles}
		e.Items = append(e.Items, protocol.BackupItem{Kind: backup.MemberStack, StackID: stack.ID, StackName: stack.Name, Project: &ref,
			Rules: protocol.BackupRules{VolumeInclude: sel.VolumeInclude, VolumeExclude: sel.VolumeExclude, AnonymousVolumes: sel.AnonymousVolumes,
				PathExcludes: sel.PathExcludes, ExternalPaths: sel.ExternalPaths}})
		e.Stacks = append(e.Stacks, stack.ID)
	}
	for _, v := range volumes {
		e := get(v.EnvironmentID)
		e.Items = append(e.Items, protocol.BackupItem{Kind: backup.MemberVolume, Volume: v.Volume, Rules: protocol.BackupRules{PathExcludes: v.PathExcludes}})
		e.Volumes = append(e.Volumes, v.Volume)
	}
	out := make([]envPlan, 0, len(byEnv))
	for _, e := range byEnv {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EnvironmentID < out[j].EnvironmentID })
	return out, nil
}

// EnvironmentPreview is one environment's scope preview.
type EnvironmentPreview struct {
	EnvironmentID   string
	EnvironmentName string
	RepositoryID    string
	Output          *protocol.BackupScopePreviewOutput
	// ErrorClass is set when the agent could not preview (offline, ...).
	ErrorClass string
}

// ManagerPreview previews the manager-state member.
type ManagerPreview struct {
	RepositoryID  string
	DatabaseBytes int64
	MetricsBytes  int64
	// MetricsIncluded: the metrics database is excluded by default (#5).
	MetricsIncluded bool
	Notes           []string
}

// ScopePreview is the preview of the setup (or of unsaved settings).
type ScopePreview struct {
	Manager      *ManagerPreview
	Environments []EnvironmentPreview
	Shutdown     bool
	Warnings     []string
}

// PreviewScope asks each environment's agent which sources, exclusions,
// sizes, affected containers and conflicts a run would have. draft, when
// set, previews unsaved settings.
func (s *Service) PreviewScope(ctx context.Context, draft *domain.BackupSetup) (ScopePreview, error) {
	p, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return ScopePreview{}, err
	}
	if draft != nil {
		d := *draft
		d.ID, d.Enabled = p.ID, false
		if d.Cron == "" {
			d.Cron, d.TimeZone = p.Cron, p.TimeZone
		}
		if err := s.validateSetup(ctx, s.db, &d); err != nil {
			return ScopePreview{}, err
		}
		p = d
	}
	out := ScopePreview{Shutdown: p.Shutdown}
	{
		mp := &ManagerPreview{RepositoryID: p.PrimaryRepositoryID, MetricsIncluded: p.IncludeMetrics,
			Notes: []string{"The manager database is captured as a consistent SQLite snapshot (VACUUM INTO), not a copy of the live file.",
				"The manager's secret-protection key is included, sealed under a key derived from the Recovery Key."}}
		if fi, err := os.Stat(s.opts.DatabasePath); err == nil {
			mp.DatabaseBytes = fi.Size()
		}
		if fi, err := os.Stat(s.opts.MetricsPath); err == nil {
			mp.MetricsBytes = fi.Size()
		}
		if !p.IncludeMetrics {
			mp.Notes = append(mp.Notes, "The metrics database is excluded (it is expendable and rebuilt from new samples).")
		}
		out.Manager = mp
	}
	plans, err := s.planItems(ctx, p)
	if err != nil {
		return ScopePreview{}, err
	}
	key, err := s.KeyState(ctx)
	if err != nil {
		return ScopePreview{}, err
	}
	var primary *domain.BackupRepository
	if p.PrimaryRepositoryID != "" {
		if r, err := store.GetBackupRepository(ctx, s.db, p.PrimaryRepositoryID); err == nil {
			primary = &r
		}
	}
	for _, e := range plans {
		ep := EnvironmentPreview{EnvironmentID: e.EnvironmentID, RepositoryID: p.PrimaryRepositoryID}
		if env, err := s.environment(ctx, e.EnvironmentID); err == nil {
			ep.EnvironmentName = env.Name
		}
		if s.opts.Agents == nil {
			ep.ErrorClass = "agent_offline"
			out.Environments = append(out.Environments, ep)
			continue
		}
		in := protocol.BackupScopePreviewInput{Items: e.Items, Shutdown: p.Shutdown}
		if primary != nil {
			ref := repositoryRef(*primary, backup.EnvironmentScope(e.EnvironmentID), key)
			in.Repository = &ref
		}
		raw, err := s.opts.Agents.RequestEnvironment(ctx, e.EnvironmentID, protocol.ReqBackupScopePreview, in, 2*time.Minute)
		if err != nil {
			ep.ErrorClass = agentErrorClass(err)
		} else {
			var po protocol.BackupScopePreviewOutput
			if err := json.Unmarshal(raw, &po); err != nil {
				ep.ErrorClass = "invalid_response"
			} else {
				ep.Output = &po
			}
		}
		out.Environments = append(out.Environments, ep)
	}
	if p.Shutdown {
		out.Warnings = append(out.Warnings, "Containers affected by the selected stacks are stopped during each backup (downtime); "+
			"only the previously running ones are started again afterwards, in dependency order.")
	}
	return out, nil
}

// RetentionLocation is the retention preview of one location.
type RetentionLocation struct {
	RepositoryID string
	Scope        string
	Decisions    []backup.RetentionDecision
}

// PreviewRetention shows which of the indexed Docker Manager backups the
// retention (or override) would forget, per location and item, with the
// reasons kept. It covers every location, also the backups of earlier
// backup policies (agents that cannot do that apply it to the setup's own
// backups only).
func (s *Service) PreviewRetention(ctx context.Context, override *domain.BackupRetention) ([]RetentionLocation, domain.BackupSetup, error) {
	p, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return nil, p, err
	}
	if override != nil {
		p.Retention = *override
		if err := validateRetention(p.Retention); err != nil {
			return nil, p, err
		}
	}
	snaps, err := s.retainedSnapshots(ctx, "")
	if err != nil {
		return nil, p, err
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	groups := map[[2]string][]backup.RetentionSnapshot{}
	indexed := map[[2]string][]domain.BackupSnapshot{}
	for _, sn := range snaps {
		k := [2]string{sn.RepositoryID, sn.Scope}
		groups[k] = append(groups[k], backup.RetentionSnapshot{ID: sn.ResticSnapshotID, Time: sn.SnapshotTime, Item: sn.Item})
		indexed[k] = append(indexed[k], sn)
	}
	keys := make([][2]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	var out []RetentionLocation
	for _, k := range keys {
		plan := backup.Plan(retentionRules(p.Retention), groups[k], loc).Expire(s.expiredItems(ctx, p, k[1], indexed[k]))
		out = append(out, RetentionLocation{RepositoryID: k[0], Scope: k[1], Decisions: plan.Decisions})
	}
	return out, p, nil
}

// retainedSnapshots lists the indexed backups retention covers: every one
// a backup run took (it carries a policy ID), in every repository still
// known; setID limits them to that set's.
func (s *Service) retainedSnapshots(ctx context.Context, setID string) ([]domain.BackupSnapshot, error) {
	snaps, err := store.ListBackupSnapshots(ctx, s.db, domain.BackupSnapshotFilter{SetID: setID})
	if err != nil {
		return nil, err
	}
	out := snaps[:0]
	for _, sn := range snaps {
		if sn.PolicyID != "" {
			out = append(out, sn)
		}
	}
	return out, nil
}
