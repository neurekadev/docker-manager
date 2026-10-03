// Package removal previews what depends on an environment before it is
// removed (archived, #34): managed stacks and standalone containers,
// update/backup/maintenance policies, backup repositories and sets, registry
// and Git bindings, permission rules scoped to it, schedules and unfinished
// jobs. It only reads; the archive itself is agents.Service.
// ArchiveEnvironment (whose hook removes the permission rules).
package removal

import (
	"context"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Service computes removal previews.
type Service struct{ db bun.IDB }

// New returns the service.
func New(db bun.IDB) *Service { return &Service{db: db} }

// unfinished are the job states an archive interrupts.
var unfinished = []domain.JobState{domain.JobQueued, domain.JobBlocked, domain.JobDispatched, domain.JobRunning, domain.JobCancelling}

// Preview lists every record that depends on the environment (active or
// already archived). Unknown environments are domain.ErrEnvironmentNotFound.
func (s *Service) Preview(ctx context.Context, environmentID string) (domain.EnvironmentRemovalPreview, error) {
	env, err := store.GetEnvironment(ctx, s.db, environmentID)
	if err != nil {
		return domain.EnvironmentRemovalPreview{}, err
	}
	p := domain.EnvironmentRemovalPreview{Environment: env}
	add := func(d domain.EnvironmentDependent) { p.Dependents = append(p.Dependents, d) }

	stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: env.ID})
	if err != nil {
		return p, err
	}
	stackIDs := map[string]string{}
	for _, st := range stacks {
		stackIDs[st.ID] = st.Name
		add(domain.EnvironmentDependent{Kind: domain.DependentStack, ID: st.ID, Name: st.Name, OnArchive: domain.OnArchiveKept,
			Detail:       "kept with its revisions; containers keep running on the host; migrate it first to keep operating it",
			ResourceType: catalog.TypeStack, ResourceID: st.ID})
	}
	managed, err := store.ListManagedContainers(ctx, s.db, env.ID)
	if err != nil {
		return p, err
	}
	for _, m := range managed {
		add(domain.EnvironmentDependent{Kind: domain.DependentManagedContainer, ID: m.ID, Name: m.Name, OnArchive: domain.OnArchiveKept,
			Detail: "saved recreate specification kept", ResourceType: catalog.TypeContainer, ResourceID: m.Name})
	}
	updates, err := store.ListEnvironmentUpdatePolicies(ctx, s.db)
	if err != nil {
		return p, err
	}
	for _, u := range updates {
		if u.EnvironmentID != "" && u.EnvironmentID != env.ID {
			continue
		}
		add(domain.EnvironmentDependent{Kind: domain.DependentUpdatePolicy, ID: u.ID, Name: u.Name, OnArchive: domain.OnArchivePaused,
			Detail:       "this host is skipped while it is archived",
			ResourceType: catalog.TypeUpdatePolicy, ResourceID: u.ID})
	}
	backupPolicies, err := s.backupPolicies(ctx, env.ID, stackIDs)
	if err != nil {
		return p, err
	}
	for _, bp := range backupPolicies {
		add(domain.EnvironmentDependent{Kind: domain.DependentBackupPolicy, ID: bp.ID, Name: bp.Name, OnArchive: domain.OnArchivePaused,
			Detail: "this host's stacks and volumes are skipped while it is archived", ResourceType: catalog.TypeBackupPolicy, ResourceID: bp.ID})
	}
	repos, err := s.repositories(ctx, env.ID)
	if err != nil {
		return p, err
	}
	for _, r := range repos {
		add(r)
	}
	snaps, err := store.ListBackupSnapshots(ctx, s.db, domain.BackupSnapshotFilter{EnvironmentID: env.ID})
	if err != nil {
		return p, err
	}
	p.BackupSnapshots = len(snaps)
	sets := map[string]int{}
	var setOrder []string
	for _, sn := range snaps {
		if sn.SetID == "" {
			continue
		}
		if _, ok := sets[sn.SetID]; !ok {
			setOrder = append(setOrder, sn.SetID)
		}
		sets[sn.SetID]++
	}
	for _, id := range setOrder {
		set, err := store.GetBackupSet(ctx, s.db, id)
		name := id
		if err == nil && set.PolicyName != "" {
			name = set.PolicyName + " " + set.StartedAt.UTC().Format("2006-01-02 15:04Z")
		}
		add(domain.EnvironmentDependent{Kind: domain.DependentBackupSet, ID: id, Name: name, OnArchive: domain.OnArchiveKept,
			Detail:       fmt.Sprintf("%d snapshot(s) of this host, kept and restorable after a re-attach", sets[id]),
			ResourceType: catalog.TypeBackupPolicy, ResourceID: set.PolicyID})
	}
	regs, err := store.ListRegistryConnections(ctx, s.db, "", "", 0)
	if err != nil {
		return p, err
	}
	for _, r := range regs {
		switch {
		case r.EnvironmentID == env.ID && r.StackID == "":
			add(domain.EnvironmentDependent{Kind: domain.DependentRegistryConnection, ID: r.ID, Name: r.Name, OnArchive: domain.OnArchiveKept,
				Detail: "bound to this host", ResourceType: catalog.TypeRegistry, ResourceID: r.ID})
		case r.StackID != "" && stackIDs[r.StackID] != "":
			add(domain.EnvironmentDependent{Kind: domain.DependentRegistryConnection, ID: r.ID, Name: r.Name, OnArchive: domain.OnArchiveKept,
				Detail: "bound to stack " + stackIDs[r.StackID], ResourceType: catalog.TypeRegistry, ResourceID: r.ID})
		}
	}
	defs, err := store.ListBuildDefinitions(ctx, s.db, env.ID, "", 0)
	if err != nil {
		return p, err
	}
	for _, d := range defs {
		detail := "Git build definition"
		if d.Source.GitCredentialID != "" {
			detail = "Git build definition bound to Git credential " + d.Source.GitCredentialID
		}
		add(domain.EnvironmentDependent{Kind: domain.DependentBuildDefinition, ID: d.ID, Name: d.Name, OnArchive: domain.OnArchiveKept,
			Detail: detail, ResourceType: catalog.TypeBuildDefinition, ResourceID: d.ID})
	}
	rules, err := store.EnvironmentRules(ctx, s.db, env.ID)
	if err != nil {
		return p, err
	}
	for _, r := range rules {
		add(domain.EnvironmentDependent{Kind: domain.DependentPermissionRule, ID: r.SubjectKind + ":" + r.SubjectID, Name: ruleText(r.Rule),
			Detail: r.SubjectKind + " " + r.SubjectID, OnArchive: domain.OnArchiveRemoved})
	}
	scheds, err := s.schedules(ctx, env.ID, p.Dependents)
	if err != nil {
		return p, err
	}
	p.Dependents = append(p.Dependents, scheds...)
	jobs, err := store.ListJobs(ctx, s.db, domain.JobFilter{EnvironmentID: env.ID, States: unfinished})
	if err != nil {
		return p, err
	}
	for i := range jobs {
		j := jobs[i]
		add(domain.EnvironmentDependent{Kind: domain.DependentJob, ID: j.ID, Name: string(j.Kind), OnArchive: domain.OnArchiveInterrupted,
			Detail: string(j.State) + "; ends by the offline rules once the agent is disconnected", Job: &j})
	}
	return p, nil
}

func ruleText(r domain.PermissionRule) string {
	scope := "environment"
	if r.Scope.Kind == domain.ScopeKindResource {
		scope = r.Scope.ResourceType + " " + r.Scope.ResourceID
	}
	return string(r.Effect) + " " + r.Capability + " on " + scope
}

// backupPolicies are the policies selecting stacks or volumes of the
// environment or naming a repository for it.
func (s *Service) backupPolicies(ctx context.Context, envID string, stacks map[string]string) ([]domain.BackupPolicy, error) {
	all, err := store.ListBackupPolicies(ctx, s.db, "", 0)
	if err != nil {
		return nil, err
	}
	var out []domain.BackupPolicy
	for _, p := range all {
		_, named := p.EnvironmentRepos[envID]
		hit := named || p.EnvironmentID == "" || p.EnvironmentID == envID
		for _, st := range p.Stacks {
			hit = hit || stacks[st.StackID] != ""
		}
		for _, v := range p.Volumes {
			hit = hit || v.EnvironmentID == envID
		}
		if hit {
			out = append(out, p)
		}
	}
	return out, nil
}

// repositories are the local repositories on the environment's agent and
// every repository holding a location (restic repository) for it.
func (s *Service) repositories(ctx context.Context, envID string) ([]domain.EnvironmentDependent, error) {
	all, err := store.ListBackupRepositories(ctx, s.db, "", 0)
	if err != nil {
		return nil, err
	}
	scope := backup.EnvironmentScope(envID)
	var out []domain.EnvironmentDependent
	for _, r := range all {
		detail := ""
		if r.Kind == "local" && r.Executor == envID {
			detail = "local repository on this host (kept; reachable again after a re-attach)"
		} else {
			locs, err := store.ListBackupLocations(ctx, s.db, r.ID)
			if err != nil {
				return nil, err
			}
			if slices.ContainsFunc(locs, func(l domain.BackupLocation) bool { return l.Scope == scope }) {
				detail = "holds this host's backups (kept)"
			}
		}
		if detail != "" {
			out = append(out, domain.EnvironmentDependent{Kind: domain.DependentBackupRepository, ID: r.ID, Name: r.Name,
				OnArchive: domain.OnArchiveKept, Detail: detail, ResourceType: catalog.TypeBackupRepository, ResourceID: r.ID})
		}
	}
	return out, nil
}

// schedules are the schedules of the environment's policies and of the
// backup policies and repositories listed above.
func (s *Service) schedules(ctx context.Context, envID string, deps []domain.EnvironmentDependent) ([]domain.EnvironmentDependent, error) {
	kinds := map[string]scheduler.Kind{}
	for _, k := range scheduler.BuiltinKinds() {
		kinds[k.Key] = k
	}
	listed := map[string]bool{}
	for _, d := range deps {
		listed[d.ResourceType+"/"+d.ResourceID] = true
	}
	all, err := store.Schedules(ctx, s.db, domain.ScheduleFilter{})
	if err != nil {
		return nil, err
	}
	var out []domain.EnvironmentDependent
	for _, sc := range all {
		k, ok := kinds[sc.Kind]
		if !ok || (sc.EnvironmentID != envID && !listed[k.PolicyType+"/"+sc.PolicyID]) {
			continue
		}
		state := "disabled"
		if sc.Enabled {
			state = "enabled"
		}
		out = append(out, domain.EnvironmentDependent{Kind: domain.DependentSchedule, ID: sc.ID, Name: k.Label + ": " + sc.Name,
			Detail: sc.Cron + " " + sc.TimeZone + " (" + state + ")", OnArchive: domain.OnArchivePaused,
			ResourceType: k.PolicyType, ResourceID: sc.PolicyID})
	}
	return out, nil
}
