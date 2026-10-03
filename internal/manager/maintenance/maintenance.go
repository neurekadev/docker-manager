// Package maintenance is the manager side of Docker maintenance (#14,
// #238): one instance-wide setup (one rule per category, a cron schedule,
// environments left out), previews through the environments' agents,
// manual runs, one-off prunes of one environment and the scheduler's
// PolicySource for scheduled runs.
//
// Runs are prune.run jobs of the #26 engine, one per environment: the job
// input carries the enabled rules and every object the manager knows must survive
// (the Compose projects and images of Docker Manager stacks, the images, volumes
// and networks of saved container specifications, backup destinations);
// the agent adds Docker Manager's own objects (#32), lists the Engine, and
// revalidates every candidate right before its targeted removal.
//
// Safety defaults: every rule and the schedule start disabled; a volume
// rule needs its own explicit opt-in; a manual run needs confirmation;
// scheduled runs run as the manager's service identity.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/resources"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// JobEngine is the part of the #26 engine the service uses.
type JobEngine interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	List(ctx context.Context, f domain.JobFilter) ([]domain.Job, error)
	Cancel(ctx context.Context, id string) (domain.Job, error)
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
}

// Requester sends named requests to an environment's agent.
type Requester interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
}

// Environments looks environments up.
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// Scheduler is the part of the #13 scheduler the service uses.
type Scheduler interface {
	Notify()
	Status(ctx context.Context, kind, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
}

// Stacks lists Docker Manager stacks (#7).
type Stacks interface {
	List(ctx context.Context, f domain.StackFilter) ([]domain.Stack, error)
}

// Specs lists what saved container specifications reference (#6).
type Specs interface {
	ManagedSpecRefs(ctx context.Context, env string) ([]resources.SpecRef, error)
}

// BackupRef is an object a backup destination or policy uses (#10).
type BackupRef struct {
	// Kind is "volume" or "network".
	Kind   string
	Name   string
	Reason string
}

// BackupReferences reports the objects of an environment backups rely on
// (#10 installs it with SetBackupReferences): volumes a backup policy
// selects are never prune candidates.
type BackupReferences func(ctx context.Context, environmentID string) ([]BackupRef, error)

// Reference is an object another feature keeps: Kind "project" (a Compose
// project: its containers, networks and volumes), "volume" or "network".
type Reference struct {
	Kind   string
	Name   string
	Reason string
}

// References reports the objects of an environment a feature keeps
// (#35 installs the stopped sources of migrated stacks with AddReferences).
type References func(ctx context.Context, environmentID string) ([]Reference, error)

// Options configures the service.
type Options struct {
	DB           *bun.DB
	Clock        clock.Clock
	Logger       *slog.Logger
	Jobs         JobEngine
	Agents       Requester
	Environments Environments
	Scheduler    Scheduler
	Stacks       Stacks
	// PreviewTimeout bounds a preview request (default 3 minutes: volume
	// sizes are computed by walking the volumes).
	PreviewTimeout time.Duration
}

// Service is the maintenance service.
type Service struct {
	opts   Options
	clk    clock.Clock
	log    *slog.Logger
	specs  Specs
	backup BackupReferences
	refs   []References
}

// New returns a Service and registers its finish hook on the job engine
// (call before the engine recovers or runs).
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Jobs == nil || opts.Agents == nil || opts.Environments == nil || opts.Scheduler == nil {
		return nil, errors.New("maintenance: DB, Jobs, Agents, Environments and Scheduler are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.PreviewTimeout <= 0 {
		opts.PreviewTimeout = 3 * time.Minute
	}
	s := &Service{opts: opts, clk: opts.Clock, log: opts.Logger}
	opts.Jobs.OnFinish(jobspec.PruneRun, s.onRunFinished)
	return s, nil
}

// SetSpecs installs the saved-specification lister (#6).
func (s *Service) SetSpecs(sp Specs) { s.specs = sp }

// SetBackupReferences installs the backup protection hook (#10).
func (s *Service) SetBackupReferences(fn BackupReferences) { s.backup = fn }

// AddReferences installs another source of objects prune runs keep (call
// while wiring, before the service is used).
func (s *Service) AddReferences(fn References) { s.refs = append(s.refs, fn) }

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

// Setup returns the maintenance setup; categories without a saved rule
// take the shipped suggestions.
func (s *Service) Setup(ctx context.Context) (domain.MaintenanceSetup, error) {
	return setup(ctx, s.opts.DB)
}

func setup(ctx context.Context, db bun.IDB) (domain.MaintenanceSetup, error) {
	st, err := store.GetMaintenanceSetup(ctx, db)
	if err != nil {
		return st, err
	}
	st.Rules = domain.CompleteRules(st.Rules, domain.SuggestedMaintenanceRules())
	return st, nil
}

func fieldErr(field, format string, args ...any) error {
	return &domain.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// ValidateRules checks rules (field names "rules.<category>.<member>").
// requireOptIn refuses enabled volume rules without their explicit opt-in
// (the setup and one-off prune runs; previews evaluate them without it).
func ValidateRules(rules []domain.MaintenanceRule, requireOptIn bool) error {
	seen := map[string]bool{}
	for _, r := range rules {
		if !slices.Contains(domain.PruneCategories(), r.Category) {
			return fieldErr("rules", "unknown category %q", r.Category)
		}
		if seen[r.Category] {
			return fieldErr("rules", "category %s appears twice", r.Category)
		}
		seen[r.Category] = true
		if r.MinAge%time.Second != 0 {
			return fieldErr("rules."+r.Category+".minAgeHours", "must be whole seconds")
		}
		if err := toProtocol(r).Validate(); err != nil {
			var fe *protocol.FieldError
			if errors.As(err, &fe) {
				return fieldErr("rules."+r.Category+"."+fe.Field, "%s", fe.Message)
			}
			return err
		}
		if !domain.IsVolumeCategory(r.Category) && r.VolumeOptIn {
			return fieldErr("rules."+r.Category+".volumeOptIn", "only volume rules take the volume opt-in")
		}
		if requireOptIn && r.Enabled && domain.IsVolumeCategory(r.Category) && !r.VolumeOptIn {
			return fieldErr("rules."+r.Category+".volumeOptIn",
				"removing volumes deletes their data: enabling this rule needs its own explicit opt-in (volumeOptIn: true)")
		}
	}
	return nil
}

func toProtocol(r domain.MaintenanceRule) protocol.PruneRule {
	return protocol.PruneRule{Category: r.Category, MinAgeSeconds: int64(r.MinAge / time.Second), IncludeLabels: r.IncludeLabels,
		ExcludeLabels: r.ExcludeLabels, Exclude: r.Exclude, ContainerStates: r.ContainerStates, BuildCacheAll: r.BuildCacheAll,
		KeepStorageBytes: r.KeepStorageBytes}
}

func (s *Service) activeEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	env, err := s.opts.Environments.GetEnvironment(ctx, id)
	if err != nil {
		return env, err
	}
	if env.Status == domain.EnvironmentArchived {
		return env, domain.ErrEnvironmentArchived
	}
	return env, nil
}

// ScopeEnvironments resolves the environments the setup covers now: every
// active environment (offline ones included) it does not leave out.
func (s *Service) ScopeEnvironments(ctx context.Context, st domain.MaintenanceSetup) ([]domain.Environment, error) {
	envs, err := store.ListEnvironments(ctx, s.opts.DB, domain.EnvironmentFilter{Statuses: []domain.EnvironmentStatus{domain.EnvironmentActive}})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(envs, func(e domain.Environment) bool { return st.Excludes(e.ID) }), nil
}

// UpdateSetup changes the setup when the revision matches. Environments
// left out that no longer exist are dropped. Queued runs that have not
// started are cancelled when the rules or the environments change: they
// carry the old ones.
func (s *Service) UpdateSetup(ctx context.Context, revision int64, patch domain.MaintenanceSetupPatch) (before, after domain.MaintenanceSetup, err error) {
	if err := ValidateRules(patch.Rules, true); err != nil {
		return before, after, err
	}
	err = s.opts.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		cur, err := setup(ctx, tx)
		if err != nil {
			return err
		}
		before = cur
		if cur.Revision != revision {
			return domain.ErrRevisionMismatch
		}
		after = cur
		if patch.Enabled != nil {
			after.Enabled = *patch.Enabled
		}
		if patch.Cron != nil {
			after.Cron = *patch.Cron
		}
		if patch.TimeZone != nil {
			after.TimeZone = *patch.TimeZone
		}
		after.Rules = domain.CompleteRules(patch.Rules, cur.Rules)
		if patch.ExcludeEnvironments != nil {
			excluded, err := existingEnvironments(ctx, tx, *patch.ExcludeEnvironments)
			if err != nil {
				return err
			}
			after.ExcludeEnvironments = excluded
		}
		if err := scheduler.ValidateSpec(after.Cron, after.TimeZone); err != nil {
			return err
		}
		after.Revision, after.UpdatedAt = cur.Revision+1, s.now()
		return store.UpdateMaintenanceSetup(ctx, tx, &after, revision)
	})
	if err != nil {
		return before, after, err
	}
	s.opts.Scheduler.Notify()
	if !rulesEqual(before.Rules, after.Rules) || !slices.Equal(before.ExcludeEnvironments, after.ExcludeEnvironments) {
		s.cancelWaiting(ctx, after.ID, "maintenance changed")
	}
	return before, after, nil
}

// existingEnvironments returns the IDs of environments that exist among
// ids, sorted and without duplicates.
func existingEnvironments(ctx context.Context, db bun.IDB, ids []string) ([]string, error) {
	envs, err := store.ListEnvironments(ctx, db, domain.EnvironmentFilter{})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range envs {
		if slices.Contains(ids, e.ID) {
			out = append(out, e.ID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func rulesEqual(a, b []domain.MaintenanceRule) bool {
	ea, _ := json.Marshal(a)
	eb, _ := json.Marshal(b)
	return string(ea) == string(eb)
}

// cancelWaiting cancels queued or blocked runs of the setup (best effort).
func (s *Service) cancelWaiting(ctx context.Context, policyID, why string) {
	js, err := s.opts.Jobs.List(ctx, domain.JobFilter{States: []domain.JobState{domain.JobQueued, domain.JobBlocked},
		Kinds: []domain.JobKind{jobspec.PruneRun}, Target: &domain.JobTarget{Type: domain.TargetMaintenancePolicy, ID: policyID}})
	if err != nil {
		s.log.Warn("could not list waiting prune runs", "policy_id", policyID, "error", err)
		return
	}
	for _, j := range js {
		if _, err := s.opts.Jobs.Cancel(ctx, j.ID); err != nil && !errors.Is(err, domain.ErrJobFinished) {
			s.log.Warn("could not cancel a waiting prune run", "policy_id", policyID, "job_id", j.ID, "error", err)
			continue
		}
		s.log.Info("cancelled a waiting prune run", "policy_id", policyID, "job_id", j.ID, "reason", why)
	}
}

// ScheduleStatus returns the setup's schedule state and newest runs (ok is
// false before the scheduler synchronized it).
func (s *Service) ScheduleStatus(ctx context.Context, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error) {
	return s.opts.Scheduler.Status(ctx, scheduler.KindPrune, policyID, runs)
}
