// Package maintenance is the manager side of Docker maintenance (#14):
// prune policies per environment (one rule per category, their own cron
// schedule), the instance's suggested default rules, previews through the
// environment's agent, manual runs and the scheduler's PolicySource for
// scheduled runs.
//
// Runs are prune.run jobs of the #26 engine: the job input carries the
// policy's enabled rules and every object the manager knows must survive
// (the Compose projects and images of DockYard stacks, the images, volumes
// and networks of saved container specifications, backup destinations);
// the agent adds DockYard's own objects (#32), lists the Engine, and
// revalidates every candidate right before its targeted removal.
//
// Safety defaults: every rule and every schedule starts disabled; a volume
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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/resources"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
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
	Default(ctx context.Context, kind string) (cronExpr, tz string, err error)
	Notify()
	Status(ctx context.Context, kind, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
}

// Stacks lists DockYard stacks (#7).
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
// (#10 installs it with SetBackupReferences): local repository volumes and
// destinations are never prune candidates.
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
	// ForgetResource drops the exact permission rules of a deleted policy.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
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

// Defaults returns the instance's default rules (the shipped suggestions
// until changed).
func (s *Service) Defaults(ctx context.Context) (domain.MaintenanceDefaults, error) {
	d, err := store.GetMaintenanceDefaults(ctx, s.opts.DB)
	if err != nil {
		return d, err
	}
	d.Rules = domain.CompleteRules(d.Rules, domain.SuggestedMaintenanceRules())
	return d, nil
}

// UpdateDefaults replaces default rules (by category) when the revision
// matches. Existing policies keep their rules.
func (s *Service) UpdateDefaults(ctx context.Context, revision int64, rules []domain.MaintenanceRule) (before, after domain.MaintenanceDefaults, err error) {
	if err := ValidateRules(rules, true); err != nil {
		return before, after, err
	}
	err = s.opts.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetMaintenanceDefaults(ctx, tx)
		if err != nil {
			return err
		}
		before = cur
		before.Rules = domain.CompleteRules(cur.Rules, domain.SuggestedMaintenanceRules())
		if cur.Revision != revision {
			return domain.ErrRevisionMismatch
		}
		next := domain.CompleteRules(rules, before.Rules)
		if err := store.UpdateMaintenanceDefaults(ctx, tx, next, revision, s.now()); err != nil {
			return err
		}
		after = domain.MaintenanceDefaults{Rules: next, Revision: revision + 1, UpdatedAt: s.now()}
		return nil
	})
	return before, after, err
}

func fieldErr(field, format string, args ...any) error {
	return &domain.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// ValidateRules checks rules (field names "rules.<category>.<member>").
// requireOptIn refuses enabled volume rules without their explicit opt-in
// (policies and defaults; previews evaluate them without it).
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

func validateName(name, description string) error {
	if n := strings.TrimSpace(name); n == "" || utf8.RuneCountInString(n) > 100 || n != name {
		return fieldErr("name", "must be 1 to 100 characters without leading or trailing spaces")
	}
	if utf8.RuneCountInString(description) > 1000 {
		return fieldErr("description", "must be at most 1000 characters")
	}
	return nil
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

// Create stores a new policy. Empty schedule fields take the instance's
// schedule defaults (#13), missing rules the maintenance defaults; the
// schedule stays disabled unless the request enables it.
func (s *Service) Create(ctx context.Context, c domain.MaintenancePolicyCreate) (domain.MaintenancePolicy, error) {
	if err := validateName(c.Name, c.Description); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	if err := ValidateRules(c.Rules, true); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	if _, err := s.activeEnvironment(ctx, c.EnvironmentID); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	cronExpr, tz, err := s.opts.Scheduler.Default(ctx, scheduler.KindPrune)
	if err != nil {
		return domain.MaintenancePolicy{}, err
	}
	if c.Cron != "" {
		cronExpr = c.Cron
	}
	if c.TimeZone != "" {
		tz = c.TimeZone
	}
	if err := scheduler.ValidateSpec(cronExpr, tz); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	defaults, err := s.Defaults(ctx)
	if err != nil {
		return domain.MaintenancePolicy{}, err
	}
	now := s.now()
	p := domain.MaintenancePolicy{ID: ids.New(), EnvironmentID: c.EnvironmentID, Name: c.Name, Description: c.Description, Cron: cronExpr,
		TimeZone: tz, ScheduleEnabled: c.ScheduleEnabled, Rules: domain.CompleteRules(c.Rules, defaults.Rules), Revision: 1,
		CreatedAt: now, UpdatedAt: now}
	if err := ValidateRules(p.Rules, true); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	if err := store.InsertMaintenancePolicy(ctx, s.opts.DB, &p); err != nil {
		return domain.MaintenancePolicy{}, err
	}
	s.opts.Scheduler.Notify()
	return p, nil
}

// Get returns a policy.
func (s *Service) Get(ctx context.Context, id string) (domain.MaintenancePolicy, error) {
	return store.GetMaintenancePolicy(ctx, s.opts.DB, id)
}

// List returns policies in ID order (every environment when envID is "").
func (s *Service) List(ctx context.Context, envID, afterID string, limit int) ([]domain.MaintenancePolicy, error) {
	return store.ListMaintenancePolicies(ctx, s.opts.DB, envID, afterID, limit)
}

// Update changes a policy when the revision matches. Queued runs of the
// policy that have not started are cancelled when its rules change: they
// carry the old rules.
func (s *Service) Update(ctx context.Context, id string, revision int64, patch domain.MaintenancePolicyPatch) (before, after domain.MaintenancePolicy, err error) {
	before, err = s.Get(ctx, id)
	if err != nil {
		return before, after, err
	}
	if before.Revision != revision {
		return before, after, domain.ErrRevisionMismatch
	}
	after = before
	after.Rules = slices.Clone(before.Rules)
	if patch.Name != nil {
		after.Name = *patch.Name
	}
	if patch.Description != nil {
		after.Description = *patch.Description
	}
	if patch.Cron != nil {
		after.Cron = *patch.Cron
	}
	if patch.TimeZone != nil {
		after.TimeZone = *patch.TimeZone
	}
	if patch.ScheduleEnabled != nil {
		after.ScheduleEnabled = *patch.ScheduleEnabled
	}
	if err := validateName(after.Name, after.Description); err != nil {
		return before, after, err
	}
	if err := ValidateRules(patch.Rules, true); err != nil {
		return before, after, err
	}
	after.Rules = domain.CompleteRules(patch.Rules, before.Rules)
	if err := scheduler.ValidateSpec(after.Cron, after.TimeZone); err != nil {
		return before, after, err
	}
	if _, err := s.activeEnvironment(ctx, after.EnvironmentID); err != nil {
		return before, after, err
	}
	after.Revision, after.UpdatedAt = before.Revision+1, s.now()
	if err := store.UpdateMaintenancePolicy(ctx, s.opts.DB, &after, revision); err != nil {
		return before, after, err
	}
	s.opts.Scheduler.Notify()
	if !rulesEqual(before.Rules, after.Rules) {
		s.cancelWaiting(ctx, id, "its rules changed")
	}
	return before, after, nil
}

func rulesEqual(a, b []domain.MaintenanceRule) bool {
	ea, _ := json.Marshal(a)
	eb, _ := json.Marshal(b)
	return string(ea) == string(eb)
}

// Delete removes a policy when the revision matches; its waiting runs are
// cancelled (a started run finishes: cancellation is best effort at item
// boundaries through /jobs).
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	if err := store.DeleteMaintenancePolicy(ctx, s.opts.DB, id, revision); err != nil {
		return err
	}
	s.opts.Scheduler.Notify()
	s.cancelWaiting(ctx, id, "the policy was deleted")
	if s.opts.ForgetResource != nil {
		if _, err := s.opts.ForgetResource(ctx, authz.ResourceRef{Type: "maintenance_policy", ID: id}); err != nil {
			s.log.Warn("could not forget the permission rules of a deleted maintenance policy", "policy_id", id, "error", err)
		}
	}
	return nil
}

// cancelWaiting cancels queued or blocked runs of a policy (best effort).
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

// ScheduleStatus returns the policy's schedule state and newest runs (ok
// is false before the scheduler synchronized a new policy).
func (s *Service) ScheduleStatus(ctx context.Context, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error) {
	return s.opts.Scheduler.Status(ctx, scheduler.KindPrune, policyID, runs)
}
