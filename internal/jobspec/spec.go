// Package jobspec is the job kind registry shared by the manager job engine
// (internal/manager/jobs) and the agent executor (internal/agent/jobs).
//
// Every kind declares, in one Spec: its executor (agent or manager), the
// capability a manual request needs, its lock definition (the kind's row of
// the lock matrix, computed from the job's targets), the offline deadline,
// its ordered steps with idempotency and cancellation safe points, the
// compensating actions its steps may register, a per-host concurrency class
// and what happens to a manager-local job when the manager restarts.
//
// The catalog lives in catalog.go. The lock-matrix table in
// docs/architecture/job-engine.md is generated from it (scripts/generate.sh)
// and TestEveryJobKindHasLockDefinition fails when a declared kind has no
// lock definition.
package jobspec

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/neurekadev/dockyard/internal/domain"
)

// LockSource says which resources a lock rule covers.
type LockSource string

// Lock sources.
const (
	// FromEnvironments: one lock per environment the job touches (the job's
	// environment plus any target environment overrides). Used for host.
	FromEnvironments LockSource = "environments"
	// FromTargets: one lock per target of TargetType.
	FromTargets LockSource = "targets"
	// AllInEnvironments: one lock on every resource of the scope (name "*")
	// in each environment the job touches.
	AllInEnvironments LockSource = "all"
)

// LockRule is one entry of a kind's lock definition.
type LockRule struct {
	Scope  domain.LockScope
	Mode   domain.LockMode
	Source LockSource
	// TargetType selects the targets for FromTargets.
	TargetType domain.TargetType
	// Optional FromTargets rules produce no lock when the job has no target
	// of that type; required ones reject the job.
	Optional bool
}

// Step is one step of a kind's execution plan.
type Step struct {
	// Name is a stable snake_case identifier (journaled by executors).
	Name string
	// Idempotent steps may be re-run when their outcome is unknown (the
	// executor died mid-step). Non-idempotent steps with an unknown outcome
	// are never retried: the job becomes interrupted with Recovery guidance.
	Idempotent bool
	// SafePoint: a pending cancellation is honored immediately before this
	// step. Cancellation is never honored anywhere else.
	SafePoint bool
	// Recovery is operator guidance when this step fails or its outcome is
	// unknown.
	Recovery string
}

// Compensation declares a compensating action a step may register. The
// executor always attempts unreleased compensations when a job does not
// succeed (failure, cancellation, crash recovery).
type Compensation struct {
	Name        string
	Description string
}

// RestartPolicy says what happens to an active manager-local job when the
// manager restarts.
type RestartPolicy string

// Restart policies.
const (
	// RestartResume resumes the job from its journal when the in-flight step
	// (if any) is idempotent; otherwise it becomes interrupted.
	RestartResume RestartPolicy = "resume"
	// RestartInterrupt always marks the job interrupted (after running
	// compensations).
	RestartInterrupt RestartPolicy = "interrupt"
)

// Concurrency classes limited per environment (configurable caps).
const (
	ClassPull  = "pull"
	ClassBuild = "build"
)

// Spec declares one job kind.
type Spec struct {
	Kind    domain.JobKind
	Summary string
	// Capability is the #17 capability key a manual/API-token request needs
	// on every target. Scheduled jobs run as the manager service identity.
	Capability string
	Executor   domain.JobExecutor
	// Locks is the kind's lock definition (row of the lock matrix).
	Locks []LockRule
	// OfflineDeadline bounds how long a queued agent job waits for an
	// offline agent before failing with agent_offline. Agent kinds only.
	OfflineDeadline time.Duration
	// ConcurrencyClass, when set, is capped per environment (pulls, builds).
	ConcurrencyClass string
	Steps            []Step
	Compensations    []Compensation
	// OnManagerRestart applies to manager-executed kinds.
	OnManagerRestart RestartPolicy
}

// Step returns the named step.
func (s Spec) Step(name string) (Step, bool) {
	for _, st := range s.Steps {
		if st.Name == name {
			return st, true
		}
	}
	return Step{}, false
}

// HasCompensation reports whether the kind declares the named compensation.
func (s Spec) HasCompensation(name string) bool {
	return slices.ContainsFunc(s.Compensations, func(c Compensation) bool { return c.Name == name })
}

// RequiresEnvironment reports whether jobs of this kind need an environment.
func (s Spec) RequiresEnvironment() bool {
	if s.Executor == domain.ExecutorAgent {
		return true
	}
	for _, r := range s.Locks {
		if r.Source != FromTargets || r.Scope != domain.LockRepository {
			return true
		}
	}
	return false
}

var (
	kindRE     = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)
	stepNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Validate checks a spec for completeness.
func (s Spec) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !kindRE.MatchString(string(s.Kind)) {
		bad("kind %q must be a dotted lowercase name like stack.deploy", s.Kind)
	}
	if !kindRE.MatchString(s.Capability) {
		bad("%s: capability %q must be a dotted key", s.Kind, s.Capability)
	}
	if s.Summary == "" {
		bad("%s: summary is required", s.Kind)
	}
	switch s.Executor {
	case domain.ExecutorAgent:
		if s.OfflineDeadline <= 0 {
			bad("%s: agent kinds need an offline deadline", s.Kind)
		}
		if s.OnManagerRestart != "" {
			bad("%s: OnManagerRestart applies to manager kinds only (agent jobs reconcile on reconnect)", s.Kind)
		}
	case domain.ExecutorManager:
		if s.OnManagerRestart != RestartResume && s.OnManagerRestart != RestartInterrupt {
			bad("%s: manager kinds need OnManagerRestart resume|interrupt", s.Kind)
		}
		if s.OfflineDeadline != 0 {
			bad("%s: offline deadline applies to agent kinds only", s.Kind)
		}
	default:
		bad("%s: executor %q must be agent or manager", s.Kind, s.Executor)
	}
	if len(s.Locks) == 0 {
		bad("%s: no lock definition (every kind needs a row in the lock matrix)", s.Kind)
	}
	alwaysLocks := false
	for _, r := range s.Locks {
		if !slices.Contains(domain.LockScopes(), r.Scope) {
			bad("%s: unknown lock scope %q", s.Kind, r.Scope)
		}
		if r.Mode != domain.LockShared && r.Mode != domain.LockExclusive {
			bad("%s: lock mode %q must be shared or exclusive", s.Kind, r.Mode)
		}
		switch r.Source {
		case FromEnvironments, AllInEnvironments:
			if r.TargetType != "" || r.Optional {
				bad("%s: %s rule takes no target type/optional flag", s.Kind, r.Source)
			}
			if r.Scope == domain.LockRepository {
				bad("%s: repository locks come from repository targets", s.Kind)
			}
			if r.Source == FromEnvironments && r.Scope != domain.LockHost {
				bad("%s: only host locks come from environments", s.Kind)
			}
			alwaysLocks = true
		case FromTargets:
			if !slices.Contains(domain.TargetTypes(), r.TargetType) {
				bad("%s: unknown target type %q", s.Kind, r.TargetType)
			}
			if r.Scope == domain.LockHost {
				bad("%s: host locks come from environments", s.Kind)
			}
			if !r.Optional {
				alwaysLocks = true
			}
		default:
			bad("%s: unknown lock source %q", s.Kind, r.Source)
		}
	}
	if len(s.Locks) > 0 && !alwaysLocks {
		bad("%s: lock definition must always produce a lock (a required target or an environment rule)", s.Kind)
	}
	if len(s.Steps) == 0 {
		bad("%s: at least one step is required", s.Kind)
	}
	seen := map[string]bool{}
	for _, st := range s.Steps {
		if !stepNameRE.MatchString(st.Name) {
			bad("%s: step name %q must be snake_case", s.Kind, st.Name)
		}
		if seen[st.Name] {
			bad("%s: duplicate step %q", s.Kind, st.Name)
		}
		seen[st.Name] = true
		if !st.Idempotent && st.Recovery == "" {
			bad("%s: non-idempotent step %q needs recovery guidance", s.Kind, st.Name)
		}
	}
	for _, c := range s.Compensations {
		if !stepNameRE.MatchString(c.Name) || c.Description == "" {
			bad("%s: compensation %q needs a snake_case name and a description", s.Kind, c.Name)
		}
	}
	return errors.Join(errs...)
}

// Target validation limits.
const (
	MaxTargets      = 64
	MaxTargetIDLen  = 1024
	MaxTargetEnvLen = 128
)

// ValidateTargets checks the targets of a job of this kind.
func (s Spec) ValidateTargets(environmentID string, targets []domain.JobTarget) error {
	if s.RequiresEnvironment() && environmentID == "" {
		return fmt.Errorf("%w: %s requires an environment", domain.ErrJobInvalid, s.Kind)
	}
	if len(targets) > MaxTargets {
		return fmt.Errorf("%w: at most %d targets", domain.ErrJobInvalid, MaxTargets)
	}
	for _, t := range targets {
		if !slices.Contains(domain.TargetTypes(), t.Type) {
			return fmt.Errorf("%w: unknown target type %q", domain.ErrJobInvalid, t.Type)
		}
		if err := validName(t.ID, MaxTargetIDLen); err != nil {
			return fmt.Errorf("%w: target %s id: %v", domain.ErrJobInvalid, t.Type, err)
		}
		if t.EnvironmentID != "" {
			if err := validName(t.EnvironmentID, MaxTargetEnvLen); err != nil {
				return fmt.Errorf("%w: target %s environment: %v", domain.ErrJobInvalid, t.Type, err)
			}
		}
		if t.Type == domain.TargetPath || t.Type == domain.TargetDestinationPath {
			if !strings.HasPrefix(t.ID, "/") || path.Clean(t.ID) != t.ID {
				return fmt.Errorf("%w: path target %q must be a clean absolute path", domain.ErrJobInvalid, t.ID)
			}
		}
		if t.ID == domain.LockAll {
			return fmt.Errorf("%w: target id %q is reserved", domain.ErrJobInvalid, t.ID)
		}
	}
	for _, r := range s.Locks {
		if r.Source == FromTargets && !r.Optional && !slices.ContainsFunc(targets, func(t domain.JobTarget) bool { return t.Type == r.TargetType }) {
			return fmt.Errorf("%w: %s requires a %s target", domain.ErrJobInvalid, s.Kind, r.TargetType)
		}
	}
	return nil
}

func validName(v string, maxLen int) error {
	if v == "" {
		return errors.New("must not be empty")
	}
	if len(v) > maxLen {
		return fmt.Errorf("must be at most %d bytes", maxLen)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// ComputeLocks returns the job's lock set: deduplicated (exclusive wins
// over shared for the same resource) and sorted, the order in which locks
// are acquired.
func (s Spec) ComputeLocks(environmentID string, targets []domain.JobTarget) ([]domain.JobLock, error) {
	if err := s.ValidateTargets(environmentID, targets); err != nil {
		return nil, err
	}
	envs := []string{}
	if environmentID != "" {
		envs = append(envs, environmentID)
	}
	envOf := func(t domain.JobTarget) string {
		if t.EnvironmentID != "" {
			return t.EnvironmentID
		}
		return environmentID
	}
	for _, t := range targets {
		if e := envOf(t); e != "" && !slices.Contains(envs, e) {
			envs = append(envs, e)
		}
	}
	var out []domain.JobLock
	for _, r := range s.Locks {
		switch r.Source {
		case FromEnvironments:
			for _, e := range envs {
				out = append(out, domain.JobLock{Scope: r.Scope, EnvironmentID: e, Mode: r.Mode})
			}
		case AllInEnvironments:
			for _, e := range envs {
				out = append(out, domain.JobLock{Scope: r.Scope, EnvironmentID: e, Name: domain.LockAll, Mode: r.Mode})
			}
		case FromTargets:
			for _, t := range targets {
				if t.Type != r.TargetType {
					continue
				}
				env := envOf(t)
				if r.Scope == domain.LockRepository {
					env = ""
				} else if env == "" {
					return nil, fmt.Errorf("%w: %s target %q needs an environment", domain.ErrJobInvalid, t.Type, t.ID)
				}
				out = append(out, domain.JobLock{Scope: r.Scope, EnvironmentID: env, Name: t.ID, Mode: r.Mode})
			}
		}
	}
	return NormalizeLocks(out), nil
}

// NormalizeLocks deduplicates (exclusive wins) and sorts a lock set.
func NormalizeLocks(locks []domain.JobLock) []domain.JobLock {
	type key struct {
		scope     domain.LockScope
		env, name string
	}
	idx := map[key]int{}
	out := make([]domain.JobLock, 0, len(locks))
	for _, l := range locks {
		k := key{l.Scope, l.EnvironmentID, l.Name}
		if i, ok := idx[k]; ok {
			if l.Mode == domain.LockExclusive {
				out[i].Mode = domain.LockExclusive
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, l)
	}
	SortLocks(out)
	return out
}

// SortLocks orders locks by (scope, environment, name): the global
// acquisition order.
func SortLocks(locks []domain.JobLock) {
	slices.SortFunc(locks, CompareLocks)
}

// CompareLocks is the total order used for acquisition.
func CompareLocks(a, b domain.JobLock) int {
	if c := strings.Compare(string(a.Scope), string(b.Scope)); c != 0 {
		return c
	}
	if c := strings.Compare(a.EnvironmentID, b.EnvironmentID); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

// Overlaps reports whether two locks cover a common resource: same scope and
// environment, and equal names, a "*" name, or (file_path) one path equal to
// or an ancestor of the other.
func Overlaps(a, b domain.JobLock) bool {
	if a.Scope != b.Scope || a.EnvironmentID != b.EnvironmentID {
		return false
	}
	if a.Name == b.Name || a.Name == domain.LockAll || b.Name == domain.LockAll {
		return true
	}
	if a.Scope == domain.LockFilePath {
		return pathContains(a.Name, b.Name) || pathContains(b.Name, a.Name)
	}
	return false
}

// pathContains reports whether child is parent or below it.
func pathContains(parent, child string) bool {
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

// Conflicts reports whether two locks of different jobs exclude each other:
// they overlap and at least one is exclusive.
func Conflicts(a, b domain.JobLock) bool {
	return Overlaps(a, b) && (a.Mode == domain.LockExclusive || b.Mode == domain.LockExclusive)
}

// FirstConflict returns the index of the first lock in want (in acquisition
// order) that conflicts with any lock in held, and the index into held.
func FirstConflict(want, held []domain.JobLock) (int, int, bool) {
	for i, w := range want {
		for j, h := range held {
			if Conflicts(w, h) {
				return i, j, true
			}
		}
	}
	return -1, -1, false
}
