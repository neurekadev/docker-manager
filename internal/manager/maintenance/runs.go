package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// protections collects what the manager knows must survive a prune of an
// environment: Docker Manager stacks (their Compose projects and images), the
// images, volumes and networks of saved container specifications, and the
// objects backups rely on, and what AddReferences sources keep (the stopped
// sources of migrated stacks, #35).
func (s *Service) protections(ctx context.Context, env string) (protocol.PruneProtection, error) {
	var p protocol.PruneProtection
	seen := map[string]bool{}
	add := func(list *[]protocol.ProtectedRef, kind, ref, reason string) {
		if ref == "" || seen[kind+"\x00"+ref] {
			return
		}
		seen[kind+"\x00"+ref] = true
		*list = append(*list, protocol.ProtectedRef{Ref: ref, Reason: reason})
	}
	if s.opts.Stacks != nil {
		after := ""
		for {
			page, err := s.opts.Stacks.List(ctx, domain.StackFilter{EnvironmentID: env, AfterID: after, Limit: 500})
			if err != nil {
				return p, err
			}
			for _, st := range page {
				name := st.Name
				add(&p.Projects, "project", st.Name, fmt.Sprintf("part of Docker Manager stack %q", name))
				reason := fmt.Sprintf("used by Docker Manager stack %q", name)
				for _, svc := range st.Services {
					add(&p.Images, "image", svc.Image, reason)
				}
				for _, im := range st.Images {
					add(&p.Images, "image", im.Image, reason)
					add(&p.Images, "image", im.ImageID, reason)
				}
			}
			if len(page) < 500 {
				break
			}
			after = page[len(page)-1].ID
		}
	}
	if s.specs != nil {
		refs, err := s.specs.ManagedSpecRefs(ctx, env)
		if err != nil {
			return p, err
		}
		for _, r := range refs {
			reason := fmt.Sprintf("used by the saved specification of container %q", r.Container)
			switch r.Kind {
			case "image":
				add(&p.Images, "image", r.Ref, reason)
			case "volume":
				add(&p.Volumes, "volume", r.Ref, reason)
			case "network":
				add(&p.Networks, "network", r.Ref, reason)
			}
		}
	}
	if s.backup != nil {
		refs, err := s.backup(ctx, env)
		if err != nil {
			return p, err
		}
		for _, r := range refs {
			reason := r.Reason
			if reason == "" {
				reason = "used by backups"
			}
			switch r.Kind {
			case "volume":
				add(&p.Volumes, "volume", r.Name, reason)
			case "network":
				add(&p.Networks, "network", r.Name, reason)
			}
		}
	}
	for _, fn := range s.refs {
		refs, err := fn(ctx, env)
		if err != nil {
			return p, err
		}
		for _, r := range refs {
			switch r.Kind {
			case "project":
				add(&p.Projects, "project", r.Name, r.Reason)
			case "volume":
				add(&p.Volumes, "volume", r.Name, r.Reason)
			case "network":
				add(&p.Networks, "network", r.Name, r.Reason)
			default:
				return p, fmt.Errorf("maintenance: unknown reference kind %q", r.Kind)
			}
		}
	}
	for _, l := range [][]protocol.ProtectedRef{p.Projects, p.Images, p.Volumes, p.Networks} {
		if len(l) > protocol.MaxPruneProtections {
			// Never drop a protection: refuse the run instead.
			return p, fmt.Errorf("maintenance: more than %d protected objects in one category", protocol.MaxPruneProtections)
		}
	}
	return p, nil
}

// target is what one prune of one environment runs: the policy ID the
// agent input and the job carry (the setup's, or a one-off prune's
// synthetic one) and the rules.
type target struct {
	policyID      string
	environmentID string
	rules         []domain.MaintenanceRule
}

func setupTarget(st domain.MaintenanceSetup, env string) target {
	return target{policyID: st.ID, environmentID: env, rules: st.Rules}
}

// input builds the agent input of a target's rules (enabled ones, or all
// with allRules for previews of disabled rules).
func (s *Service) input(ctx context.Context, t target, allRules bool) (protocol.PruneInput, error) {
	in := protocol.PruneInput{PolicyID: t.policyID, Rules: []protocol.PruneRule{}}
	for _, r := range t.rules {
		if r.Enabled || allRules {
			in.Rules = append(in.Rules, toProtocol(r))
		}
	}
	prot, err := s.protections(ctx, t.environmentID)
	if err != nil {
		return in, err
	}
	in.Protect = prot
	return in, in.Validate()
}

// preview asks the environment's agent what a prune of t would remove now.
// Nothing is stored or removed.
func (s *Service) preview(ctx context.Context, t target, allRules bool) (protocol.PrunePreviewOutput, error) {
	var out protocol.PrunePreviewOutput
	in, err := s.input(ctx, t, allRules)
	if err != nil {
		return out, err
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, t.environmentID, protocol.ReqMaintenancePreview, in, s.opts.PreviewTimeout)
	if err != nil {
		return out, agentErr(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent answered with a malformed maintenance preview"}
	}
	return out, nil
}

// agentErr maps transport and agent errors to stable Docker errors.
func agentErr(err error) error {
	var re protocol.CodedError
	switch {
	case errors.Is(err, jobs.ErrAgentOffline):
		return &domain.DockerError{Code: domain.DockerEnvironmentOffline,
			Message: "the environment's agent is not connected; preview again when it is online"}
	case errors.Is(err, protocol.ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return &domain.DockerError{Code: domain.DockerTimeout, Message: "the environment's agent did not answer in time"}
	case errors.As(err, &re):
		switch re.ProtocolCode() {
		case protocol.CodeInvalidArgument:
			return &domain.DockerError{Code: domain.DockerInvalid, Message: re.ProtocolMessage()}
		case protocol.CodeEngineUnavailable:
			return &domain.DockerError{Code: domain.DockerEngineUnavailable, Message: "the agent cannot reach its Docker Engine"}
		case protocol.CodeDeadlineExceeded:
			return &domain.DockerError{Code: domain.DockerTimeout, Message: "the Docker Engine did not answer in time"}
		case protocol.CodeUnsupportedRequest:
			return &domain.DockerError{Code: domain.DockerAgentUnsupported,
				Message: "the environment's agent does not support prune previews; upgrade the agent"}
		case protocol.CodeUnsupportedAPIVersion:
			return &domain.DockerError{Code: domain.DockerUnsupportedAPIVersion,
				Message: "the environment's Docker Engine API version is too old for this operation"}
		case protocol.CodeBusy:
			return &domain.DockerError{Code: domain.DockerBusy, Message: "the agent is busy; retry"}
		}
		return &domain.DockerError{Code: domain.DockerEngineError, Message: "the Docker Engine failed: " + re.ProtocolMessage()}
	}
	return err
}

func (s *Service) request(ctx context.Context, t target) (jobs.Request, error) {
	in, err := s.input(ctx, t, false)
	if err != nil {
		return jobs.Request{}, err
	}
	return jobs.Request{Kind: jobspec.PruneRun, PolicyID: t.policyID, EnvironmentID: t.environmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetMaintenancePolicy, ID: t.policyID}}, Input: in}, nil
}

// ManualPolicyPrefix starts the synthetic policy ID of a one-off prune's
// agent input: agents require a policy ID, the job itself has none.
const ManualPolicyPrefix = "manual-"

// manualTarget is the transient target of a one-off prune of an
// environment (a resource page's "Prune"): the given rules, nothing
// stored.
func manualTarget(env string, rules []domain.MaintenanceRule) target {
	return target{policyID: ManualPolicyPrefix + ids.New(), environmentID: env, rules: domain.CompleteRules(rules, nil)}
}

// checkManual validates the rules of a one-off prune (at least one enabled;
// requireOptIn for runs) and the environment (exists, not archived).
func (s *Service) checkManual(ctx context.Context, env string, rules []domain.MaintenanceRule, requireOptIn bool) error {
	if err := ValidateRules(rules, requireOptIn); err != nil {
		return err
	}
	if len(domain.EnabledRules(rules)) == 0 {
		return fieldErr("rules", "turn on at least one rule")
	}
	_, err := s.activeEnvironment(ctx, env)
	return err
}

// PreviewManual asks the environment's agent what a one-off prune with
// these rules would remove now. Nothing is stored or removed.
func (s *Service) PreviewManual(ctx context.Context, env string, rules []domain.MaintenanceRule) (protocol.PrunePreviewOutput, error) {
	if err := s.checkManual(ctx, env, rules, false); err != nil {
		return protocol.PrunePreviewOutput{}, err
	}
	return s.preview(ctx, manualTarget(env, rules), false)
}

// RunManual starts a one-off prune of an environment with the given rules
// for principal p (the caller has confirmed it): a prune.run job without
// policy or targets, authorized on the environment. Volume rules need
// their opt-in. A repeated idempotency key returns the run it started (the
// input is rebuilt on every call, so the key is resolved here, not by
// the engine's input hash).
func (s *Service) RunManual(ctx context.Context, p authz.Principal, env string, rules []domain.MaintenanceRule, key string) (domain.Job, error) {
	if key != "" {
		existing, found, err := store.FindJobByIdempotencyKey(ctx, s.opts.DB, &domain.Job{IdempotencyKey: key, InitiatorUserID: p.UserID,
			InitiatorTokenID: p.TokenID})
		if err != nil {
			return domain.Job{}, err
		}
		if found {
			if existing.Kind == jobspec.PruneRun && existing.PolicyID == "" && existing.EnvironmentID == env {
				return existing, nil
			}
			return domain.Job{}, domain.ErrJobIdempotencyConflict
		}
	}
	if err := s.checkManual(ctx, env, rules, true); err != nil {
		return domain.Job{}, err
	}
	in, err := s.input(ctx, manualTarget(env, rules), false)
	if err != nil {
		return domain.Job{}, err
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: env, Input: in, Principal: p,
		IdempotencyKey: key})
	return j, err
}

// EnvironmentPreview is the preview of one environment the setup covers:
// what a run would remove there now, or why it could not be computed
// (Err: an offline agent, a timeout).
type EnvironmentPreview struct {
	EnvironmentID string
	Preview       protocol.PrunePreviewOutput
	Err           error
}

// Permit refuses (with its error) an environment the caller may not act
// on; nil permits every one.
type Permit func(domain.Environment) error

// permitted resolves the environments the setup covers now and refuses
// all of them when permit refuses one: the caller is checked against the
// very environments acted on.
func (s *Service) permitted(ctx context.Context, st domain.MaintenanceSetup, permit Permit) ([]domain.Environment, error) {
	envs, err := s.ScopeEnvironments(ctx, st)
	if err != nil {
		return nil, err
	}
	if permit != nil {
		for _, env := range envs {
			if err := permit(env); err != nil {
				return nil, err
			}
		}
	}
	return envs, nil
}

// previewParallel bounds the agents a preview asks at once.
const previewParallel = 8

// rollbackTimeout bounds cancelling one job of a start that failed part
// way; rollbackBudget bounds cancelling all of them.
const (
	rollbackTimeout = 10 * time.Second
	rollbackBudget  = 30 * time.Second
)

// Preview asks each environment the setup covers what a run would remove
// now, several at once (each answers within PreviewTimeout). An
// environment that cannot answer reports its error; the others are
// previewed all the same. Nothing is stored or removed. permit refuses
// the preview when the caller may not see one of the environments.
func (s *Service) Preview(ctx context.Context, permit Permit) ([]EnvironmentPreview, error) {
	st, err := s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	envs, err := s.permitted(ctx, st, permit)
	if err != nil {
		return nil, err
	}
	out := make([]EnvironmentPreview, len(envs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, previewParallel)
	for i, env := range envs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			p, err := s.preview(ctx, setupTarget(st, env.ID), false)
			out[i] = EnvironmentPreview{EnvironmentID: env.ID, Preview: p, Err: err}
		})
	}
	wg.Wait()
	return out, nil
}

// Run starts a manual run of the setup for principal p (the caller has
// confirmed it): one prune job per environment it covers. The jobs are
// durable and manager-owned: whether the UI follows them or not is
// presentation only. A second run while one is active is refused. Every
// request is built before the first is enqueued, and when an enqueue
// fails the jobs already queued are cancelled: a run starts everywhere or
// nowhere. permit refuses the run when the caller may not prune one of
// the environments.
func (s *Service) Run(ctx context.Context, principal authz.Principal, key string, permit Permit) ([]domain.Job, error) {
	st, err := s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	if len(domain.EnabledRules(st.Rules)) == 0 {
		return nil, domain.ErrMaintenanceEmpty
	}
	envs, err := s.permitted(ctx, st, permit)
	if err != nil {
		return nil, err
	}
	if len(envs) == 0 {
		return nil, domain.ErrMaintenanceNoEnvironments
	}
	if len(envs) > scheduler.MaxJobsPerRun {
		return nil, fieldErr("excludeEnvironments", "too many environments for one run")
	}
	active, err := store.ActivePolicyJob(ctx, s.opts.DB, st.ID, []domain.JobKind{jobspec.PruneRun}, nil)
	if err != nil {
		return nil, err
	}
	if active != "" {
		return nil, &domain.MaintenanceRunActiveError{JobID: active}
	}
	reqs := make([]jobs.Request, 0, len(envs))
	for _, env := range envs {
		req, err := s.request(ctx, setupTarget(st, env.ID))
		if err != nil {
			return nil, err
		}
		req.Principal = principal
		if key != "" {
			req.IdempotencyKey = key + "/" + env.ID
		}
		reqs = append(reqs, req)
	}
	out := make([]domain.Job, 0, len(reqs))
	for _, req := range reqs {
		job, created, err := s.opts.Jobs.Enqueue(ctx, req)
		if err == nil && !created && job.State.Terminal() {
			// The key of a start that failed (its jobs were cancelled):
			// replaying it would report cancelled jobs as a new run.
			err = domain.ErrJobIdempotencyConflict
		}
		if err != nil {
			// The request may be cancelled already: cancel what was
			// queued regardless, each within a bound of its own so one
			// slow cancel does not skip the others, all within
			// rollbackBudget.
			base, stop := context.WithTimeout(context.WithoutCancel(ctx), rollbackBudget)
			defer stop()
			for _, j := range out {
				cctx, cancel := context.WithTimeout(base, rollbackTimeout)
				_, cerr := s.opts.Jobs.Cancel(cctx, j.ID)
				cancel()
				if cerr != nil && !errors.Is(cerr, domain.ErrJobFinished) {
					s.log.Warn("could not cancel a prune run of a failed start", "job_id", j.ID, "error", cerr)
				}
			}
			return nil, err
		}
		out = append(out, job)
	}
	return out, nil
}

// PolicySource returns the scheduler.PolicySource of the maintenance
// setup (register with scheduler.Service.Register(scheduler.KindPrune, ...)).
func (s *Service) PolicySource() scheduler.PolicySource { return policySource{s} }

type policySource struct{ s *Service }

// ScheduleName is the name of the maintenance schedule.
const ScheduleName = "Maintenance"

func (ps policySource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	st, err := ps.s.Setup(ctx)
	if err != nil {
		return nil, err
	}
	return []scheduler.PolicySchedule{{PolicyID: st.ID, Name: ScheduleName, Cron: st.Cron, TimeZone: st.TimeZone,
		Enabled: st.Enabled}}, nil
}

// Rejection classes of scheduled prune runs besides the scheduler's.
const (
	RejectNoRules        = "no_rules_enabled"
	RejectNoEnvironments = "no_environments"
)

func (ps policySource) Validate(ctx context.Context, policyID string) error {
	_, _, err := ps.valid(ctx, policyID)
	return err
}

func (ps policySource) valid(ctx context.Context, policyID string) (domain.MaintenanceSetup, []domain.Environment, error) {
	st, err := ps.s.Setup(ctx)
	if err != nil {
		return st, nil, err
	}
	if st.ID != policyID {
		return st, nil, scheduler.Reject(scheduler.RejectPolicyNotFound, "the maintenance schedule no longer exists")
	}
	if !st.Enabled {
		return st, nil, scheduler.Reject(scheduler.RejectPolicyDisabled, "maintenance is disabled")
	}
	if len(domain.EnabledRules(st.Rules)) == 0 {
		return st, nil, scheduler.Reject(RejectNoRules, "maintenance has no enabled rule")
	}
	envs, err := ps.s.ScopeEnvironments(ctx, st)
	if err != nil {
		return st, nil, err
	}
	if len(envs) == 0 {
		return st, nil, scheduler.Reject(RejectNoEnvironments, "maintenance leaves every environment out")
	}
	return st, envs, nil
}

func (ps policySource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	st, envs, err := ps.valid(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	if len(envs) > scheduler.MaxJobsPerRun {
		return nil, scheduler.Reject("too_many_environments", "too many environments for one scheduled prune")
	}
	out := make([]jobs.Request, 0, len(envs))
	for _, env := range envs {
		req, err := ps.s.request(ctx, setupTarget(st, env.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// onRunFinished records the latest run's summary on the setup (inside the
// job's terminal transaction; malformed output is tolerated).
func (s *Service) onRunFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	if j.PolicyID == "" {
		return nil
	}
	sum := domain.MaintenanceRunSummary{JobID: j.ID, State: j.State, Origin: j.Origin}
	if j.FinishedAt != nil {
		sum.FinishedAt = *j.FinishedAt
	} else {
		sum.FinishedAt = s.now()
	}
	if len(j.ResultOutput) > 0 {
		var out protocol.PruneRunOutput
		if err := json.Unmarshal(j.ResultOutput, &out); err != nil {
			s.log.Warn("malformed prune run output", "job_id", j.ID, "error", err)
		} else {
			sum.Removed, sum.Skipped, sum.Failed, sum.Deferred, sum.BytesReclaimed = out.Removed, out.Skipped, out.Failed, out.Deferred,
				out.BytesReclaimed
		}
	}
	return store.SetMaintenanceLastRun(ctx, db, j.PolicyID, sum)
}
