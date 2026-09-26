// Package updates is the manager side of digest-driven automatic updates
// (#20, #9): update policies, the digest model (candidates), manager-side
// registry checks (update.check), previews, runs (update.run on the
// agent), quarantine of failed candidate digests, applied digest history
// and the scheduled check and run policies (#13).
//
// A policy opts one Docker Manager-managed stack (all services, or listed ones
// minus exclusions) or one Docker Manager-managed standalone container with a
// saved recreate specification into following the digest behind its
// existing explicit tag. Checks resolve the tag's host-platform manifest
// digest through the manager-owned registry connection (#19,
// registries.Service.Check: cached, deduplicated, rate-limit aware) and
// compare it with the digest applied on the host; an index change that
// leaves the host-platform image unchanged is never an update. Runs pull
// the unchanged tagged reference on the agent and recreate what changed
// from exactly the applied definition bytes (or the saved specification);
// no Compose, override or env file is ever written. There is no automatic
// rollback: a failed run quarantines the candidate digest.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/permissions"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/registries"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/scheduler"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Jobs is the job engine as the update service uses it (*jobs.Engine).
type Jobs interface {
	Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error)
	OnFinish(kind domain.JobKind, h jobs.FinishHook)
	RegisterManagerExecutor(x jobexec.Executor) error
}

// Stacks reads stacks and records applied update images
// (*stacks.Service).
type Stacks interface {
	Get(ctx context.Context, id string) (domain.Stack, error)
	List(ctx context.Context, f domain.StackFilter) ([]domain.Stack, error)
	RecordUpdatedImages(ctx context.Context, db bun.IDB, stackID string, images []domain.StackImage, after []domain.StackServiceState) error
}

// Registries selects connections and checks digests (#19,
// *registries.Service).
type Registries interface {
	Select(ctx context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error)
	Check(ctx context.Context, req registries.CheckRequest) (registries.CheckResult, error)
}

// Agents sends requests to an environment's agent (*agents.Hub).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
}

// Environments reads environments (*agents.Service).
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// Resources is the Docker resource service (#6, #32,
// *resources.Service).
type Resources interface {
	ListContainers(ctx context.Context, env string) ([]protocol.ContainerSummary, error)
	InspectContainer(ctx context.Context, env, ref string) (protocol.ContainerDetails, error)
	InspectImage(ctx context.Context, env, ref string) (protocol.ImageDetails, error)
	ManagedSpec(ctx context.Context, env string, labels map[string]string) (*domain.ManagedContainer, *protocol.ContainerSpec, error)
	ContainerProtection(c protocol.ContainerSummary) *protocol.Protection
	ProjectProtection(ctx context.Context, env, project string) (*protocol.Protection, error)
}

// Scheduler is the shared cron scheduler (#13, *scheduler.Service).
type Scheduler interface {
	Default(ctx context.Context, kind string) (cronExpr, tz string, err error)
	Notify()
	Status(ctx context.Context, kind, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
	NextRun(sc domain.Schedule) (scheduler.Run, bool)
}

// ScheduleStatus is the scheduler's state of a policy's check or run
// schedule (Known is false before the scheduler synchronized it).
type ScheduleStatus struct {
	Known    bool
	Schedule domain.Schedule
	Runs     []domain.ScheduleRun
	NextRun  *scheduler.Run
}

// ScheduleStatus returns the check (scheduler.KindUpdateCheck) or run
// (KindUpdateRun) schedule state of a policy with its newest runs.
func (s *Service) ScheduleStatus(ctx context.Context, kind, policyID string, runs int) (ScheduleStatus, error) {
	if s.opts.Scheduler == nil {
		return ScheduleStatus{}, nil
	}
	sc, rs, ok, err := s.opts.Scheduler.Status(ctx, kind, policyID, runs)
	if err != nil || !ok {
		return ScheduleStatus{}, err
	}
	out := ScheduleStatus{Known: true, Schedule: sc, Runs: rs}
	if r, ok := s.opts.Scheduler.NextRun(sc); ok {
		out.NextRun = &r
	}
	return out, nil
}

// Auditor records events outside requests (*audit.Log).
type Auditor interface {
	RecordTx(ctx context.Context, db bun.IDB, ev domain.AuditEvent) error
}

// Options configures the service.
type Options struct {
	DB           *bun.DB
	Clock        clock.Clock
	Logger       *slog.Logger
	Jobs         Jobs
	Stacks       Stacks
	Registries   Registries
	Agents       Agents
	Environments Environments
	// Resources is installed later with SetResources (created after the
	// job engine's recovery); container policies need it.
	Resources Resources
	Scheduler Scheduler
	Audit     Auditor
	// ForgetResource removes permission rules naming a deleted policy.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
	// RequestTimeout bounds agent requests (default 30 s).
	RequestTimeout time.Duration
}

// Service manages update policies.
type Service struct {
	opts Options
	db   *bun.DB
	clk  clock.Clock
	log  *slog.Logger
}

// New creates the service: it registers the update.check executor and the
// finish hooks (call before the job engine's recovery).
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Jobs == nil || opts.Stacks == nil || opts.Registries == nil || opts.Agents == nil || opts.Environments == nil {
		return nil, errors.New("updates: DB, Jobs, Stacks, Registries, Agents and Environments are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	s := &Service{opts: opts, db: opts.DB, clk: opts.Clock, log: opts.Logger}
	if err := opts.Jobs.RegisterManagerExecutor(jobexec.Executor{Kind: jobspec.UpdateCheck,
		Steps: map[string]jobexec.StepFunc{"check": s.checkStep}}); err != nil {
		return nil, err
	}
	opts.Jobs.OnFinish(jobspec.UpdateRun, s.onRunFinished)
	opts.Jobs.OnFinish(jobspec.StackDeploy, s.onDeployFinished)
	return s, nil
}

// SetResources installs the Docker resource service.
func (s *Service) SetResources(r Resources) { s.opts.Resources = r }

// SetScheduler installs the scheduler (Notify after policy changes,
// defaults of new policies).
func (s *Service) SetScheduler(sc Scheduler) { s.opts.Scheduler = sc }

func (s *Service) now() time.Time { return s.clk.Now().UTC().Truncate(time.Microsecond) }

func (s *Service) notify() {
	if s.opts.Scheduler != nil {
		s.opts.Scheduler.Notify()
	}
}

// Get returns a policy.
func (s *Service) Get(ctx context.Context, id string) (domain.UpdatePolicy, error) {
	return store.GetUpdatePolicy(ctx, s.db, id)
}

// List returns policies in ID order (environment "" = all).
func (s *Service) List(ctx context.Context, environmentID, afterID string, limit int) ([]domain.UpdatePolicy, error) {
	return store.ListUpdatePolicies(ctx, s.db, environmentID, afterID, limit)
}

// Candidates returns a policy's digest model per service.
func (s *Service) Candidates(ctx context.Context, policyID string) ([]domain.UpdateCandidate, error) {
	if _, err := store.GetUpdatePolicy(ctx, s.db, policyID); err != nil {
		return nil, err
	}
	return store.UpdateCandidates(ctx, s.db, policyID)
}

// Quarantine returns a policy's quarantined digests.
func (s *Service) Quarantine(ctx context.Context, policyID string) ([]domain.UpdateQuarantine, error) {
	return store.UpdateQuarantine(ctx, s.db, policyID)
}

// History returns a policy's newest history entries.
func (s *Service) History(ctx context.Context, policyID string, limit int) ([]domain.UpdateHistoryEntry, error) {
	return store.UpdateHistory(ctx, s.db, policyID, limit)
}

// ForTarget returns the policy of a stack or container (nil when none).
func (s *Service) ForTarget(ctx context.Context, environmentID string, typ domain.UpdateTargetType, id string) (*domain.UpdatePolicy, error) {
	ps, err := store.UpdatePoliciesForTarget(ctx, s.db, environmentID, typ, id)
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	return &ps[0], nil
}

// Locator places a policy in its environment below its stack or
// container (#17: a stack-scoped update.run grant covers the stack's
// policy).
func (s *Service) Locator() permissions.Locator {
	return permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		p, err := store.GetUpdatePolicy(ctx, s.db, ref.ID)
		if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, EnvironmentID: p.EnvironmentID, Parents: Parents(p)}, nil
	})
}

// projectRef locates a stack's Compose project on the agent (like
// stacks.Ref; the update service does not import the stack service).
func projectRef(st domain.Stack) protocol.ProjectRef {
	return protocol.ProjectRef{Root: st.Root, RootPath: st.RootPath, Dir: st.Dir, ProjectName: st.Name,
		ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
}

// Parents are a policy's authorization parents: its stack, or its
// container.
func Parents(p domain.UpdatePolicy) []authz.ResourceRef {
	if p.TargetType == domain.UpdateTargetContainer {
		return []authz.ResourceRef{{Type: catalog.TypeContainer, ID: p.TargetID, EnvironmentID: p.EnvironmentID}}
	}
	return []authz.ResourceRef{{Type: catalog.TypeStack, ID: p.TargetID}}
}

// Resource is the authorization resource of a policy.
func Resource(p domain.UpdatePolicy) authz.Resource {
	return authz.Resource{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID, Parents: Parents(p)}
}

// NewPolicy is a create request.
type NewPolicy struct {
	EnvironmentID      string
	Name               string
	TargetType         domain.UpdateTargetType
	TargetID           string
	Services           []string
	ExcludeServices    []string
	Check              *domain.UpdateSchedule
	Run                *domain.UpdateSchedule
	Window             *domain.UpdateWindow
	WaitTimeoutSeconds int
}

var serviceNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func fieldErr(field, format string, args ...any) error {
	return &domain.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

func validServices(field string, list []string) ([]string, error) {
	if len(list) > protocol.MaxUpdateServices {
		return nil, fieldErr(field, "at most %d services", protocol.MaxUpdateServices)
	}
	out := slices.Clone(list)
	for _, n := range out {
		if !serviceNameRE.MatchString(n) {
			return nil, fieldErr(field, "invalid service name %q", n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

var hhmmRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func validWindow(w *domain.UpdateWindow) error {
	if w == nil {
		return nil
	}
	if !hhmmRE.MatchString(w.Start) {
		return fieldErr("window.start", "must be HH:MM (24 h)")
	}
	if !hhmmRE.MatchString(w.End) || w.End == w.Start {
		return fieldErr("window.end", "must be HH:MM (24 h) and differ from the start")
	}
	if len(w.Days) > 7 {
		return fieldErr("window.days", "at most 7 days")
	}
	for _, d := range w.Days {
		if d < 0 || d > 6 {
			return fieldErr("window.days", "days are 0 (Sunday) to 6 (Saturday)")
		}
	}
	slices.Sort(w.Days)
	w.Days = slices.Compact(w.Days)
	return nil
}

func validSchedule(field string, sc domain.UpdateSchedule) error {
	if err := scheduler.ValidateSpec(sc.Cron, sc.TimeZone); err != nil {
		var ie *scheduler.InvalidError
		if errors.As(err, &ie) {
			return &ScheduleError{Field: field, Cron: sc.Cron, TimeZone: sc.TimeZone, Err: ie}
		}
		return err
	}
	return nil
}

// ScheduleError is an invalid check or run schedule (the API maps it to
// 422 details under Field: body.checkSchedule / body.runSchedule).
type ScheduleError struct {
	Field    string
	Cron     string
	TimeZone string
	Err      *scheduler.InvalidError
}

func (e *ScheduleError) Error() string { return e.Field + ": " + e.Err.Error() }

// defaultSchedule prefills a schedule from the instance defaults (#13):
// disabled until the user enables it.
func (s *Service) defaultSchedule(ctx context.Context, kind string, in *domain.UpdateSchedule) (domain.UpdateSchedule, error) {
	out := domain.UpdateSchedule{}
	if in != nil {
		out = *in
	}
	if out.Cron == "" || out.TimeZone == "" {
		if s.opts.Scheduler == nil {
			return out, errors.New("updates: no scheduler for default schedules")
		}
		cronExpr, tz, err := s.opts.Scheduler.Default(ctx, kind)
		if err != nil {
			return out, err
		}
		if out.Cron == "" {
			out.Cron = cronExpr
		}
		if out.TimeZone == "" {
			out.TimeZone = tz
		}
	}
	return out, nil
}

// Create opts a target in. The target must be a Docker Manager stack of the
// environment, or a Docker Manager-managed standalone container with a saved
// recreate specification; Docker Manager's own resources are refused (#32).
// Both schedules start disabled unless the request enables them.
func (s *Service) Create(ctx context.Context, np NewPolicy) (domain.UpdatePolicy, error) {
	name := strings.TrimSpace(np.Name)
	if name == "" || len(name) > 100 {
		return domain.UpdatePolicy{}, fieldErr("name", "must be 1 to 100 characters")
	}
	p := domain.UpdatePolicy{ID: ids.New(), EnvironmentID: np.EnvironmentID, Name: name, TargetType: np.TargetType, TargetID: np.TargetID,
		Window: np.Window, WaitTimeoutSeconds: np.WaitTimeoutSeconds, Revision: 1}
	var err error
	if p.Services, err = validServices("services", np.Services); err != nil {
		return p, err
	}
	if p.ExcludeServices, err = validServices("excludeServices", np.ExcludeServices); err != nil {
		return p, err
	}
	if np.WaitTimeoutSeconds < 0 || np.WaitTimeoutSeconds > 3600 {
		return p, fieldErr("waitTimeoutSeconds", "must be between 0 and 3600")
	}
	if err := validWindow(p.Window); err != nil {
		return p, err
	}
	if p.Check, err = s.defaultSchedule(ctx, scheduler.KindUpdateCheck, np.Check); err != nil {
		return p, err
	}
	if p.Run, err = s.defaultSchedule(ctx, scheduler.KindUpdateRun, np.Run); err != nil {
		return p, err
	}
	if err := validSchedule("checkSchedule", p.Check); err != nil {
		return p, err
	}
	if err := validSchedule("runSchedule", p.Run); err != nil {
		return p, err
	}
	if err := s.checkTarget(ctx, &p); err != nil {
		return p, err
	}
	now := s.now()
	p.CreatedAt, p.UpdatedAt = now, now
	if err := store.InsertUpdatePolicy(ctx, s.db, &p); err != nil {
		return p, err
	}
	s.notify()
	return p, nil
}

// checkTarget verifies a new policy's target.
func (s *Service) checkTarget(ctx context.Context, p *domain.UpdatePolicy) error {
	switch p.TargetType {
	case domain.UpdateTargetStack:
		st, err := s.opts.Stacks.Get(ctx, p.TargetID)
		if errors.Is(err, domain.ErrStackNotFound) || (err == nil && st.EnvironmentID != p.EnvironmentID) {
			return fieldErr("target.id", "no such stack in this environment")
		}
		if err != nil {
			return err
		}
		if s.opts.Resources != nil {
			pr, err := s.opts.Resources.ProjectProtection(ctx, st.EnvironmentID, st.Name)
			if err == nil && pr != nil {
				return &domain.UpdateError{Code: domain.UpdateErrTargetIneligible, Message: "Docker Manager's own Compose project is never updated by a policy: " + pr.Reason}
			}
		}
		return nil
	case domain.UpdateTargetContainer:
		if len(p.Services) > 0 || len(p.ExcludeServices) > 0 {
			return fieldErr("services", "services apply to stack targets only")
		}
		if !protocol.ValidDockerName(p.TargetID) {
			return fieldErr("target.id", "invalid container name")
		}
		if s.opts.Resources == nil {
			return errors.New("updates: the Docker resource service is not available")
		}
		d, err := s.opts.Resources.InspectContainer(ctx, p.EnvironmentID, p.TargetID)
		if err != nil {
			return err
		}
		if reason, msg := s.containerIneligible(ctx, p.EnvironmentID, d); reason != "" {
			return &domain.UpdateError{Code: domain.UpdateErrTargetIneligible, Message: msg}
		}
		p.TargetID = d.Name
		return nil
	}
	return fieldErr("target.type", "must be stack or container")
}

// containerIneligible explains why a container cannot be a target:
// Docker Manager's own (#32), a stack member, or no saved recreate
// specification (#6: unmanaged containers are never recreated).
func (s *Service) containerIneligible(ctx context.Context, env string, d protocol.ContainerDetails) (reason, message string) {
	if pr := s.opts.Resources.ContainerProtection(d.ContainerSummary); pr != nil {
		return domain.UpdateReasonProtected, "Docker Manager's own containers are never updated by a policy: " + pr.Reason
	}
	if d.Stack != nil {
		return domain.UpdateReasonStackManaged, "The container belongs to the Compose project " + d.Stack.Project +
			": update it through its stack's policy."
	}
	m, _, err := s.opts.Resources.ManagedSpec(ctx, env, d.Labels)
	if err != nil || m == nil {
		return domain.UpdateReasonNoRecreateSpec, "Only containers created through Docker Manager have a complete saved recreate " +
			"specification; other containers are never recreated automatically."
	}
	return "", ""
}

// Update changes a policy (revision compare-and-set).
func (s *Service) Update(ctx context.Context, id string, revision int64, patch domain.UpdatePolicyPatch) (before, after domain.UpdatePolicy, err error) {
	cur, err := store.GetUpdatePolicy(ctx, s.db, id)
	if err != nil {
		return cur, cur, err
	}
	if cur.Revision != revision {
		return cur, cur, domain.ErrRevisionMismatch
	}
	next := cur
	if patch.Name != nil {
		n := strings.TrimSpace(*patch.Name)
		if n == "" || len(n) > 100 {
			return cur, cur, fieldErr("name", "must be 1 to 100 characters")
		}
		next.Name = n
	}
	if patch.Services != nil {
		if next.Services, err = validServices("services", *patch.Services); err != nil {
			return cur, cur, err
		}
	}
	if patch.ExcludeServices != nil {
		if next.ExcludeServices, err = validServices("excludeServices", *patch.ExcludeServices); err != nil {
			return cur, cur, err
		}
	}
	if next.TargetType == domain.UpdateTargetContainer && (len(next.Services) > 0 || len(next.ExcludeServices) > 0) {
		return cur, cur, fieldErr("services", "services apply to stack targets only")
	}
	if patch.Check != nil {
		next.Check = *patch.Check
		if err := validSchedule("checkSchedule", next.Check); err != nil {
			return cur, cur, err
		}
	}
	if patch.Run != nil {
		next.Run = *patch.Run
		if err := validSchedule("runSchedule", next.Run); err != nil {
			return cur, cur, err
		}
	}
	if patch.ClearWindow {
		next.Window = nil
	}
	if patch.Window != nil {
		w := *patch.Window
		if err := validWindow(&w); err != nil {
			return cur, cur, err
		}
		next.Window = &w
	}
	if patch.WaitTimeoutSeconds != nil {
		if *patch.WaitTimeoutSeconds < 0 || *patch.WaitTimeoutSeconds > 3600 {
			return cur, cur, fieldErr("waitTimeoutSeconds", "must be between 0 and 3600")
		}
		next.WaitTimeoutSeconds = *patch.WaitTimeoutSeconds
	}
	next.Revision, next.UpdatedAt = cur.Revision+1, s.now()
	if err := store.UpdateUpdatePolicy(ctx, s.db, &next, cur.Revision); err != nil {
		return cur, cur, err
	}
	s.notify()
	return cur, next, nil
}

// Delete removes a policy with its candidates, quarantine and history,
// and the permission rules naming it.
func (s *Service) Delete(ctx context.Context, id string, revision int64) error {
	p, err := store.GetUpdatePolicy(ctx, s.db, id)
	if err != nil {
		return err
	}
	if err := store.DeleteUpdatePolicy(ctx, s.db, id, revision); err != nil {
		return err
	}
	if s.opts.ForgetResource != nil {
		if _, err := s.opts.ForgetResource(ctx, authz.ResourceRef{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: p.EnvironmentID}); err != nil {
			s.log.Warn("could not remove the permission rules of a deleted update policy", "policy_id", p.ID, "error", err)
		}
	}
	s.notify()
	return nil
}

// selected reports whether a stack service is opted in by the policy.
func selected(p domain.UpdatePolicy, service string) bool {
	if slices.Contains(p.ExcludeServices, service) {
		return false
	}
	return len(p.Services) == 0 || slices.Contains(p.Services, service)
}

// InWindow reports whether t is inside the policy's update window (always
// true without one). The window is evaluated in the run schedule's zone;
// an end before the start spans midnight (the day is the start's day).
func InWindow(p domain.UpdatePolicy, t time.Time) bool {
	w := p.Window
	if w == nil {
		return true
	}
	loc, err := time.LoadLocation(p.Run.TimeZone)
	if err != nil {
		return false
	}
	lt := t.In(loc)
	minutes := func(hhmm string) int {
		var h, m int
		_, _ = fmt.Sscanf(hhmm, "%d:%d", &h, &m)
		return h*60 + m
	}
	start, end, now := minutes(w.Start), minutes(w.End), lt.Hour()*60+lt.Minute()
	day := int(lt.Weekday())
	dayOK := func(d int) bool { return len(w.Days) == 0 || slices.Contains(w.Days, d) }
	if start < end {
		return now >= start && now < end && dayOK(day)
	}
	// Spans midnight: the evening part belongs to today, the morning part
	// to the window that started yesterday.
	if now >= start {
		return dayOK(day)
	}
	return now < end && dayOK((day+6)%7)
}
