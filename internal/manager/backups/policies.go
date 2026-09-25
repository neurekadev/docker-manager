package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Policy limits.
const (
	MaxPolicyStacks  = 128
	MaxPolicyVolumes = 128
)

// ListPolicies returns policies in creation order.
func (s *Service) ListPolicies(ctx context.Context, afterID string, limit int) ([]domain.BackupPolicy, error) {
	return store.ListBackupPolicies(ctx, s.db, afterID, limit)
}

// GetPolicy returns one policy.
func (s *Service) GetPolicy(ctx context.Context, id string) (domain.BackupPolicy, error) {
	return store.GetBackupPolicy(ctx, s.db, id)
}

// CreatePolicy stores a new policy. An empty schedule takes the instance
// default (#13); policies start disabled unless Enabled is set, and can
// only be enabled once every repository they use is confirmed.
func (s *Service) CreatePolicy(ctx context.Context, p domain.BackupPolicy) (domain.BackupPolicy, error) {
	var err error
	if p.Cron == "" && s.opts.Scheduler != nil {
		if p.Cron, p.TimeZone, err = s.opts.Scheduler.Default(ctx, scheduler.KindBackup); err != nil {
			return domain.BackupPolicy{}, err
		}
	}
	if p.Cron == "" {
		p.Cron, p.TimeZone = "0 2 * * *", "UTC"
	}
	now := s.now()
	p.ID, p.Revision, p.CreatedAt, p.UpdatedAt = ids.New(), 1, now, now
	if err := s.validatePolicy(ctx, s.db, &p); err != nil {
		return domain.BackupPolicy{}, err
	}
	if err := store.InsertBackupPolicy(ctx, s.db, &p); err != nil {
		return domain.BackupPolicy{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeBackupPolicy, ID: p.ID})
	audit.SetDetail(ctx, "enabled", p.Enabled)
	s.notify()
	return p, nil
}

// PolicyPatch edits a policy (nil = unchanged).
type PolicyPatch struct {
	Name             *string
	RepositoryID     *string
	EnvironmentRepos *map[string]string
	IncludeManager   *bool
	IncludeMetrics   *bool
	Stacks           *[]domain.BackupStackSelection
	Volumes          *[]domain.BackupVolumeSelection
	Shutdown         *bool
	Cron             *string
	TimeZone         *string
	Enabled          *bool
	Retention        *domain.BackupRetention
}

// UpdatePolicy edits a policy (If-Match revision).
func (s *Service) UpdatePolicy(ctx context.Context, id string, revision int64, pp PolicyPatch) (before, after domain.BackupPolicy, err error) {
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		p, err := store.GetBackupPolicy(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Revision != revision {
			return domain.ErrRevisionMismatch
		}
		before = p
		if pp.Name != nil {
			p.Name = *pp.Name
		}
		if pp.RepositoryID != nil {
			p.RepositoryID = *pp.RepositoryID
		}
		if pp.EnvironmentRepos != nil {
			p.EnvironmentRepos = *pp.EnvironmentRepos
		}
		if pp.IncludeManager != nil {
			p.IncludeManager = *pp.IncludeManager
		}
		if pp.IncludeMetrics != nil {
			p.IncludeMetrics = *pp.IncludeMetrics
		}
		if pp.Stacks != nil {
			p.Stacks = *pp.Stacks
		}
		if pp.Volumes != nil {
			p.Volumes = *pp.Volumes
		}
		if pp.Shutdown != nil {
			p.Shutdown = *pp.Shutdown
		}
		if pp.Cron != nil {
			p.Cron = *pp.Cron
		}
		if pp.TimeZone != nil {
			p.TimeZone = *pp.TimeZone
		}
		if pp.Enabled != nil {
			p.Enabled = *pp.Enabled
		}
		if pp.Retention != nil {
			p.Retention = *pp.Retention
		}
		p.Revision, p.UpdatedAt = revision+1, s.now()
		if err := s.validatePolicy(ctx, tx, &p); err != nil {
			return err
		}
		if err := store.UpdateBackupPolicy(ctx, tx, &p, revision); err != nil {
			return err
		}
		after = p
		return nil
	})
	if err != nil {
		return before, after, err
	}
	audit.SetDiff(ctx, policyAuditView(before), policyAuditView(after))
	s.notify()
	return before, after, nil
}

func policyAuditView(p domain.BackupPolicy) map[string]any {
	stackIDs := make([]string, 0, len(p.Stacks))
	for _, st := range p.Stacks {
		stackIDs = append(stackIDs, st.StackID)
	}
	vols := make([]string, 0, len(p.Volumes))
	for _, v := range p.Volumes {
		vols = append(vols, v.EnvironmentID+"/"+v.Volume)
	}
	return map[string]any{"name": p.Name, "repositoryId": p.RepositoryID, "environmentRepositories": p.EnvironmentRepos,
		"includeManager": p.IncludeManager, "includeMetrics": p.IncludeMetrics, "stacks": stackIDs, "volumes": vols,
		"shutdown": p.Shutdown, "cron": p.Cron, "timeZone": p.TimeZone, "enabled": p.Enabled, "retention": p.Retention}
}

// DeletePolicy removes a policy; its sets and snapshots stay.
func (s *Service) DeletePolicy(ctx context.Context, id string, revision int64) error {
	if err := store.DeleteBackupPolicy(ctx, s.db, id, revision); err != nil {
		return err
	}
	if s.opts.ForgetResource != nil {
		_, _ = s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeBackupPolicy, ID: id})
	}
	s.notify()
	return nil
}

// validatePolicy normalizes and checks a policy.
func (s *Service) validatePolicy(ctx context.Context, db bun.IDB, p *domain.BackupPolicy) error {
	name, err := validName(p.Name)
	if err != nil {
		return err
	}
	p.Name = name
	if err := scheduler.ValidateSpec(p.Cron, p.TimeZone); err != nil {
		return fieldErr("schedule", "%s", err.Error())
	}
	rules := retentionRules(p.Retention)
	for f, msg := range rules.Validate() {
		return fieldErr("retention."+f, "%s", msg)
	}
	if !p.IncludeManager && len(p.Stacks) == 0 && len(p.Volumes) == 0 {
		return fieldErr("stacks", "select the manager state, at least one stack or at least one volume")
	}
	if len(p.Stacks) > MaxPolicyStacks || len(p.Volumes) > MaxPolicyVolumes {
		return fieldErr("stacks", "at most %d stacks and %d volumes", MaxPolicyStacks, MaxPolicyVolumes)
	}
	repos := map[string]domain.BackupRepository{}
	repo := func(id, field string) (domain.BackupRepository, error) {
		if r, ok := repos[id]; ok {
			return r, nil
		}
		r, err := store.GetBackupRepository(ctx, db, id)
		if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
			return r, fieldErr(field, "unknown backup repository")
		}
		if err != nil {
			return r, err
		}
		repos[id] = r
		return r, nil
	}
	if _, err := repo(p.RepositoryID, "repositoryId"); err != nil {
		return err
	}
	if p.IncludeManager {
		if r := repos[p.RepositoryID]; !Serves(r, backup.ScopeManager) {
			return fieldErr("repositoryId", "the manager state needs an S3 repository or a local repository on the manager")
		}
	}
	for env, id := range p.EnvironmentRepos {
		if _, err := s.environment(ctx, env); err != nil {
			return fieldErr("environmentRepositories", "unknown environment %s", env)
		}
		r, err := repo(id, "environmentRepositories")
		if err != nil {
			return err
		}
		if !Serves(r, backup.EnvironmentScope(env)) {
			return fieldErr("environmentRepositories", "repository %s cannot hold environment %s's data (a local repository lives on one executor)", r.Name, env)
		}
	}
	envItems := map[string]int{}
	seenStacks := map[string]bool{}
	for i, sel := range p.Stacks {
		field := fmt.Sprintf("stacks[%d]", i)
		if seenStacks[sel.StackID] {
			return fieldErr(field, "stack selected twice")
		}
		seenStacks[sel.StackID] = true
		st, err := s.stack(ctx, sel.StackID)
		if err != nil {
			return fieldErr(field+".stackId", "unknown stack")
		}
		if err := (protocol.BackupRules{VolumeInclude: sel.VolumeInclude, VolumeExclude: sel.VolumeExclude, AnonymousVolumes: sel.AnonymousVolumes,
			PathExcludes: sel.PathExcludes, ExternalPaths: sel.ExternalPaths}).Validate(); err != nil {
			return fieldErr(field, "%s", err.Error())
		}
		r, err := repo(p.RepositoryFor(st.EnvironmentID), "repositoryId")
		if err != nil {
			return err
		}
		if !Serves(r, backup.EnvironmentScope(st.EnvironmentID)) {
			return fieldErr(field, "no repository of this policy can hold environment %s's data; add an environment repository", st.EnvironmentID)
		}
		envItems[st.EnvironmentID]++
	}
	seenVols := map[string]bool{}
	for i, v := range p.Volumes {
		field := fmt.Sprintf("volumes[%d]", i)
		if !protocol.ValidVolumeName(v.Volume) {
			return fieldErr(field+".volume", "not a volume name")
		}
		if seenVols[v.EnvironmentID+"/"+v.Volume] {
			return fieldErr(field, "volume selected twice")
		}
		seenVols[v.EnvironmentID+"/"+v.Volume] = true
		if _, err := s.environment(ctx, v.EnvironmentID); err != nil {
			return fieldErr(field+".environmentId", "unknown environment")
		}
		if err := (protocol.BackupRules{PathExcludes: v.PathExcludes}).Validate(); err != nil {
			return fieldErr(field, "%s", err.Error())
		}
		r, err := repo(p.RepositoryFor(v.EnvironmentID), "repositoryId")
		if err != nil {
			return err
		}
		if !Serves(r, backup.EnvironmentScope(v.EnvironmentID)) {
			return fieldErr(field, "no repository of this policy can hold environment %s's data", v.EnvironmentID)
		}
		envItems[v.EnvironmentID]++
	}
	for env, n := range envItems {
		if n > protocol.MaxBackupItems {
			return fieldErr("stacks", "at most %d stacks and volumes per environment (%s has %d)", protocol.MaxBackupItems, env, n)
		}
	}
	if p.Enabled {
		for _, r := range repos {
			if r.State != domain.BackupRepositoryReady {
				return domain.ErrRecoveryKeyNotConfirmed
			}
		}
	}
	if p.EnvironmentRepos == nil {
		p.EnvironmentRepos = map[string]string{}
	}
	return nil
}

// VolumeReference is a standalone volume a backup policy selects.
type VolumeReference struct {
	Volume     string
	PolicyName string
}

// VolumeReferences lists the standalone volumes of an environment that
// backup policies select: Docker maintenance (#14) never prunes them.
func (s *Service) VolumeReferences(ctx context.Context, environmentID string) ([]VolumeReference, error) {
	pols, err := store.ListBackupPolicies(ctx, s.db, "", 0)
	if err != nil {
		return nil, err
	}
	var out []VolumeReference
	for _, p := range pols {
		for _, v := range p.Volumes {
			if v.EnvironmentID == environmentID {
				out = append(out, VolumeReference{Volume: v.Volume, PolicyName: p.Name})
			}
		}
	}
	return out, nil
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

func retentionRules(r domain.BackupRetention) backup.RetentionRules {
	return backup.RetentionRules{Last: r.Last, Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Monthly: r.Monthly, Yearly: r.Yearly,
		WithinDays: r.WithinDays, MinKeep: r.MinKeep}
}

// envPlan is one environment's part of a run.
type envPlan struct {
	EnvironmentID string
	Repository    domain.BackupRepository
	Items         []protocol.BackupItem
	Stacks        []string
	Volumes       []string
}

// planItems groups a policy's selections by environment (sorted).
func (s *Service) planItems(ctx context.Context, db bun.IDB, p domain.BackupPolicy) ([]envPlan, error) {
	byEnv := map[string]*envPlan{}
	get := func(env string) (*envPlan, error) {
		if e, ok := byEnv[env]; ok {
			return e, nil
		}
		r, err := store.GetBackupRepository(ctx, db, p.RepositoryFor(env))
		if err != nil {
			return nil, err
		}
		e := &envPlan{EnvironmentID: env, Repository: r}
		byEnv[env] = e
		return e, nil
	}
	for _, sel := range p.Stacks {
		st, err := s.stack(ctx, sel.StackID)
		if err != nil {
			return nil, fmt.Errorf("%w: stack %s", domain.ErrStackNotFound, sel.StackID)
		}
		e, err := get(st.EnvironmentID)
		if err != nil {
			return nil, err
		}
		ref := protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name,
			ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
		e.Items = append(e.Items, protocol.BackupItem{Kind: backup.MemberStack, StackID: st.ID, StackName: st.Name, Project: &ref,
			Rules: protocol.BackupRules{VolumeInclude: sel.VolumeInclude, VolumeExclude: sel.VolumeExclude, AnonymousVolumes: sel.AnonymousVolumes,
				PathExcludes: sel.PathExcludes, ExternalPaths: sel.ExternalPaths}})
		e.Stacks = append(e.Stacks, st.ID)
	}
	for _, v := range p.Volumes {
		e, err := get(v.EnvironmentID)
		if err != nil {
			return nil, err
		}
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

// ScopePreview is the preview of a policy (or an unsaved draft).
type ScopePreview struct {
	Manager      *ManagerPreview
	Environments []EnvironmentPreview
	Shutdown     bool
	Warnings     []string
}

// PreviewScope asks each environment's agent which sources, exclusions,
// sizes, affected containers and conflicts a run would have. draft, when
// set, previews unsaved settings.
func (s *Service) PreviewScope(ctx context.Context, id string, draft *domain.BackupPolicy) (ScopePreview, error) {
	var p domain.BackupPolicy
	var err error
	if draft != nil {
		p = *draft
		if p.Cron == "" {
			p.Cron, p.TimeZone = "0 2 * * *", "UTC"
		}
		if err := s.validatePolicy(ctx, s.db, &p); err != nil {
			return ScopePreview{}, err
		}
	} else if p, err = store.GetBackupPolicy(ctx, s.db, id); err != nil {
		return ScopePreview{}, err
	}
	out := ScopePreview{Shutdown: p.Shutdown}
	if p.IncludeManager {
		mp := &ManagerPreview{RepositoryID: p.RepositoryID, MetricsIncluded: p.IncludeMetrics,
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
	plans, err := s.planItems(ctx, s.db, p)
	if err != nil {
		return ScopePreview{}, err
	}
	key, err := s.KeyState(ctx)
	if err != nil {
		return ScopePreview{}, err
	}
	for _, e := range plans {
		ep := EnvironmentPreview{EnvironmentID: e.EnvironmentID, RepositoryID: e.Repository.ID}
		if env, err := s.environment(ctx, e.EnvironmentID); err == nil {
			ep.EnvironmentName = env.Name
		}
		if s.opts.Agents == nil {
			ep.ErrorClass = "agent_offline"
			out.Environments = append(out.Environments, ep)
			continue
		}
		ref := repositoryRef(e.Repository, backup.EnvironmentScope(e.EnvironmentID), key)
		raw, err := s.opts.Agents.RequestEnvironment(ctx, e.EnvironmentID, protocol.ReqBackupScopePreview,
			protocol.BackupScopePreviewInput{Items: e.Items, Shutdown: p.Shutdown, Repository: &ref}, 2*time.Minute)
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

// PreviewRetention shows which of a policy's indexed snapshots its
// retention would forget (per location and item, with the reasons kept).
func (s *Service) PreviewRetention(ctx context.Context, id string, override *domain.BackupRetention) ([]RetentionLocation, domain.BackupPolicy, error) {
	p, err := store.GetBackupPolicy(ctx, s.db, id)
	if err != nil {
		return nil, p, err
	}
	if override != nil {
		p.Retention = *override
		for f, msg := range retentionRules(p.Retention).Validate() {
			return nil, p, fieldErr("retention."+f, "%s", msg)
		}
	}
	snaps, err := store.ListBackupSnapshots(ctx, s.db, domain.BackupSnapshotFilter{PolicyID: id})
	if err != nil {
		return nil, p, err
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	groups := map[[2]string][]backup.RetentionSnapshot{}
	for _, sn := range snaps {
		k := [2]string{sn.RepositoryID, sn.Scope}
		groups[k] = append(groups[k], backup.RetentionSnapshot{ID: sn.ResticSnapshotID, Time: sn.SnapshotTime, Item: sn.Item})
	}
	keys := make([][2]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int {
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		if a[1] < b[1] {
			return -1
		}
		if a[1] > b[1] {
			return 1
		}
		return 0
	})
	var out []RetentionLocation
	for _, k := range keys {
		plan := backup.Plan(retentionRules(p.Retention), groups[k], loc)
		out = append(out, RetentionLocation{RepositoryID: k[0], Scope: k[1], Decisions: plan.Decisions})
	}
	return out, p, nil
}
