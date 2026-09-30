package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// input builds the agent input of a policy's rules (enabled ones, or all
// with allRules for previews of disabled rules).
func (s *Service) input(ctx context.Context, pol domain.MaintenancePolicy, rules []domain.MaintenanceRule, allRules bool) (protocol.PruneInput, error) {
	in := protocol.PruneInput{PolicyID: pol.ID, Rules: []protocol.PruneRule{}}
	for _, r := range rules {
		if r.Enabled || allRules {
			in.Rules = append(in.Rules, toProtocol(r))
		}
	}
	prot, err := s.protections(ctx, pol.EnvironmentID)
	if err != nil {
		return in, err
	}
	in.Protect = prot
	return in, in.Validate()
}

// Preview asks the environment's agent what a run of the policy would
// remove now. rules (optional) previews unsaved rule changes merged onto
// the policy's; allRules evaluates disabled rules too. Nothing is stored
// or removed.
func (s *Service) Preview(ctx context.Context, pol domain.MaintenancePolicy, rules []domain.MaintenanceRule, allRules bool) (protocol.PrunePreviewOutput, error) {
	var out protocol.PrunePreviewOutput
	if err := ValidateRules(rules, false); err != nil {
		return out, err
	}
	in, err := s.input(ctx, pol, domain.CompleteRules(rules, pol.Rules), allRules)
	if err != nil {
		return out, err
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, pol.EnvironmentID, protocol.ReqMaintenancePreview, in, s.opts.PreviewTimeout)
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

func (s *Service) request(ctx context.Context, pol domain.MaintenancePolicy) (jobs.Request, error) {
	in, err := s.input(ctx, pol, pol.Rules, false)
	if err != nil {
		return jobs.Request{}, err
	}
	return jobs.Request{Kind: jobspec.PruneRun, PolicyID: pol.ID, EnvironmentID: pol.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetMaintenancePolicy, ID: pol.ID}}, Input: in}, nil
}

// Run starts a manual run of the policy for principal p (the caller has
// confirmed it). The job is durable and manager-owned: whether the UI
// follows it in the foreground or not is presentation only. A repeated
// idempotency key returns the run it started; a second run while one is
// active is refused.
func (s *Service) Run(ctx context.Context, p authz.Principal, pol domain.MaintenancePolicy, key string) (domain.Job, error) {
	if pol.EnvironmentID == "" {
		return domain.Job{}, fieldErr("environmentId", "use the environment-runs endpoint for All Environments")
	}
	if key != "" {
		existing, found, err := store.FindJobByIdempotencyKey(ctx, s.opts.DB, &domain.Job{IdempotencyKey: key, InitiatorUserID: p.UserID,
			InitiatorTokenID: p.TokenID})
		if err != nil {
			return domain.Job{}, err
		}
		if found {
			if existing.Kind == jobspec.PruneRun && existing.PolicyID == pol.ID {
				return existing, nil
			}
			return domain.Job{}, domain.ErrJobIdempotencyConflict
		}
	}
	if _, err := s.activeEnvironment(ctx, pol.EnvironmentID); err != nil {
		return domain.Job{}, err
	}
	if len(domain.EnabledRules(pol.Rules)) == 0 {
		return domain.Job{}, domain.ErrMaintenancePolicyEmpty
	}
	active, err := store.ActivePolicyJob(ctx, s.opts.DB, pol.ID, []domain.JobKind{jobspec.PruneRun}, nil)
	if err != nil {
		return domain.Job{}, err
	}
	if active != "" {
		return domain.Job{}, &domain.MaintenanceRunActiveError{JobID: active}
	}
	req, err := s.request(ctx, pol)
	if err != nil {
		return domain.Job{}, err
	}
	req.Principal, req.IdempotencyKey = p, key
	j, _, err := s.opts.Jobs.Enqueue(ctx, req)
	return j, err
}

// ManualPolicyPrefix starts the synthetic policy ID of a one-off prune's
// agent input: agents require a policy ID, the job itself has none.
const ManualPolicyPrefix = "manual-"

// manualPolicy is the transient policy of a one-off prune of an
// environment (a resource page's "Prune"): the given rules, no schedule,
// nothing stored.
func manualPolicy(env string, rules []domain.MaintenanceRule) domain.MaintenancePolicy {
	return domain.MaintenancePolicy{ID: ManualPolicyPrefix + ids.New(), EnvironmentID: env,
		Rules: domain.CompleteRules(rules, nil)}
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
	pol := manualPolicy(env, rules)
	return s.Preview(ctx, pol, nil, false)
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
	pol := manualPolicy(env, rules)
	in, err := s.input(ctx, pol, pol.Rules, false)
	if err != nil {
		return domain.Job{}, err
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: env, Input: in, Principal: p,
		IdempotencyKey: key})
	return j, err
}

// PreviewEnvironments previews each currently active environment in scope.
func (s *Service) PreviewEnvironments(ctx context.Context, pol domain.MaintenancePolicy) (map[string]protocol.PrunePreviewOutput, error) {
	envs, err := s.ScopeEnvironments(ctx, pol.EnvironmentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]protocol.PrunePreviewOutput, len(envs))
	for _, env := range envs {
		target := pol
		target.EnvironmentID = env.ID
		preview, err := s.Preview(ctx, target, nil, false)
		if err != nil {
			return nil, err
		}
		out[env.ID] = preview
	}
	return out, nil
}

// RunEnvironments starts one prune job per active environment in scope.
func (s *Service) RunEnvironments(ctx context.Context, principal authz.Principal, pol domain.MaintenancePolicy, key string) ([]domain.Job, error) {
	if len(domain.EnabledRules(pol.Rules)) == 0 {
		return nil, domain.ErrMaintenancePolicyEmpty
	}
	envs, err := s.ScopeEnvironments(ctx, pol.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if len(envs) > scheduler.MaxJobsPerRun {
		return nil, fieldErr("environmentId", "too many environments for one run")
	}
	active, err := store.ActivePolicyJob(ctx, s.opts.DB, pol.ID, []domain.JobKind{jobspec.PruneRun}, nil)
	if err != nil {
		return nil, err
	}
	if active != "" {
		return nil, &domain.MaintenanceRunActiveError{JobID: active}
	}
	out := make([]domain.Job, 0, len(envs))
	for _, env := range envs {
		target := pol
		target.EnvironmentID = env.ID
		req, err := s.request(ctx, target)
		if err != nil {
			return out, err
		}
		req.Principal, req.IdempotencyKey = principal, key+"/"+env.ID
		job, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			return out, err
		}
		out = append(out, job)
	}
	return out, nil
}

// PolicySource returns the scheduler.PolicySource of prune policies
// (register with scheduler.Service.Register(scheduler.KindPrune, ...)).
func (s *Service) PolicySource() scheduler.PolicySource { return policySource{s} }

type policySource struct{ s *Service }

func (ps policySource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	pols, err := ps.s.List(ctx, "", "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.PolicySchedule, 0, len(pols))
	for _, p := range pols {
		out = append(out, scheduler.PolicySchedule{PolicyID: p.ID, Name: p.Name, EnvironmentID: p.EnvironmentID, Cron: p.Cron,
			TimeZone: p.TimeZone, Enabled: p.ScheduleEnabled})
	}
	return out, nil
}

// Rejection classes of scheduled prune runs besides the scheduler's.
const (
	RejectNoRules             = "no_rules_enabled"
	RejectEnvironmentArchived = "environment_archived"
)

func (ps policySource) Validate(ctx context.Context, policyID string) error {
	_, err := ps.valid(ctx, policyID)
	return err
}

func (ps policySource) valid(ctx context.Context, policyID string) (domain.MaintenancePolicy, error) {
	pol, err := ps.s.Get(ctx, policyID)
	if errors.Is(err, domain.ErrMaintenancePolicyNotFound) {
		return pol, scheduler.Reject(scheduler.RejectPolicyNotFound, "the maintenance policy was deleted")
	}
	if err != nil {
		return pol, err
	}
	if !pol.ScheduleEnabled {
		return pol, scheduler.Reject(scheduler.RejectPolicyDisabled, "the policy's schedule is disabled")
	}
	if _, err := ps.s.ScopeEnvironments(ctx, pol.EnvironmentID); err != nil {
		if errors.Is(err, domain.ErrEnvironmentNotFound) {
			return pol, scheduler.Reject(scheduler.RejectTargetNotFound, "the policy's environment no longer exists")
		}
		if errors.Is(err, domain.ErrEnvironmentArchived) {
			return pol, scheduler.Reject(RejectEnvironmentArchived, "the policy's environment is archived")
		}
		return pol, err
	}
	if len(domain.EnabledRules(pol.Rules)) == 0 {
		return pol, scheduler.Reject(RejectNoRules, "the policy has no enabled rule")
	}
	return pol, nil
}

func (ps policySource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	pol, err := ps.valid(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	envs, err := ps.s.ScopeEnvironments(ctx, pol.EnvironmentID)
	if err != nil {
		return nil, err
	}
	if len(envs) > scheduler.MaxJobsPerRun {
		return nil, scheduler.Reject("too_many_environments", "too many environments for one scheduled prune")
	}
	out := make([]jobs.Request, 0, len(envs))
	for _, env := range envs {
		target := pol
		target.EnvironmentID = env.ID
		req, err := ps.s.request(ctx, target)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// onRunFinished records the latest run's summary on its policy (inside the
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
