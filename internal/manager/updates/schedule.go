package updates

import (
	"context"
	"errors"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Scheduled checks and runs (#13): each policy has a check schedule
// (update_check) and a run schedule (update_run), both disabled until the
// user enables them — no automatic check or update happens before.

// Rejection classes of scheduled update work.
const (
	RejectOutsideWindow = "outside_update_window"
	RejectSourceDrift   = domain.UpdateErrSourceDrift
	RejectIneligible    = domain.UpdateErrTargetIneligible
)

// CheckSource is the scheduler.PolicySource of update checks.
func (s *Service) CheckSource() scheduler.PolicySource { return checkSource{s} }

// RunSource is the scheduler.PolicySource of update runs.
func (s *Service) RunSource() scheduler.PolicySource { return runSource{s} }

// Register installs both sources in the scheduler and keeps it for
// Notify and the defaults of new policies.
func (s *Service) Register(sched *scheduler.Service) error {
	s.SetScheduler(sched)
	if err := sched.Register(scheduler.KindUpdateCheck, s.CheckSource()); err != nil {
		return err
	}
	return sched.Register(scheduler.KindUpdateRun, s.RunSource())
}

func (s *Service) schedules(ctx context.Context, kind string) ([]scheduler.PolicySchedule, error) {
	ps, err := store.ListUpdatePolicies(ctx, s.db, "", "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.PolicySchedule, 0, len(ps))
	for _, p := range ps {
		if p.ParentID != "" || p.Inactive {
			continue
		}
		sc := p.Check
		if kind == scheduler.KindUpdateRun {
			sc = p.Run
		}
		out = append(out, scheduler.PolicySchedule{PolicyID: p.ID, Name: p.Name, EnvironmentID: p.EnvironmentID,
			Cron: sc.Cron, TimeZone: sc.TimeZone, Enabled: sc.Enabled})
	}
	configs, err := store.ListEnvironmentUpdatePolicies(ctx, s.db)
	if err != nil {
		return nil, err
	}
	for _, p := range configs {
		sc := p.Check
		if kind == scheduler.KindUpdateRun {
			sc = p.Run
		}
		out = append(out, scheduler.PolicySchedule{PolicyID: p.ID, Name: p.Name, EnvironmentID: p.EnvironmentID,
			Cron: sc.Cron, TimeZone: sc.TimeZone, Enabled: sc.Enabled})
	}
	return out, nil
}

func (s *Service) environmentPolicy(ctx context.Context, id string) (domain.EnvironmentUpdatePolicy, bool, error) {
	p, err := store.GetEnvironmentUpdatePolicy(ctx, s.db, id)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return p, false, nil
	}
	return p, err == nil, err
}

func (s *Service) environmentTarget(ctx context.Context, p domain.EnvironmentUpdatePolicy) error {
	if p.EnvironmentID == "" {
		return nil
	}
	env, err := s.opts.Environments.GetEnvironment(ctx, p.EnvironmentID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return scheduler.Reject(scheduler.RejectTargetNotFound, "the environment no longer exists")
	}
	if err != nil {
		return err
	}
	if env.Status == domain.EnvironmentArchived {
		return scheduler.Reject(RejectEnvironmentArchived, "the environment is archived")
	}
	return nil
}

// policy loads a policy for the scheduler: a deleted one is rejected.
func (s *Service) policy(ctx context.Context, id string) (domain.UpdatePolicy, error) {
	p, err := store.GetUpdatePolicy(ctx, s.db, id)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return p, scheduler.Reject(scheduler.RejectPolicyNotFound, "the update policy was deleted")
	}
	return p, err
}

// RejectEnvironmentArchived refuses scheduled checks and runs while the
// policy's environment is archived (#34); they resume after a re-attach.
const RejectEnvironmentArchived = "environment_archived"

// target rejects a policy whose environment is archived or whose target
// is gone.
func (s *Service) target(ctx context.Context, p domain.UpdatePolicy) error {
	env, err := s.opts.Environments.GetEnvironment(ctx, p.EnvironmentID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return scheduler.Reject(scheduler.RejectTargetNotFound, "the policy's environment no longer exists")
	}
	if err != nil {
		return err
	}
	if env.Status == domain.EnvironmentArchived {
		return scheduler.Reject(RejectEnvironmentArchived, "the policy's environment is archived")
	}
	if p.TargetType != domain.UpdateTargetStack {
		return nil // container targets are revalidated by the run itself
	}
	st, err := s.opts.Stacks.Get(ctx, p.TargetID)
	if errors.Is(err, domain.ErrStackNotFound) || (err == nil && st.EnvironmentID != p.EnvironmentID) {
		return scheduler.Reject(scheduler.RejectTargetNotFound, "the policy's stack no longer exists in its environment")
	}
	return err
}

type checkSource struct{ s *Service }

func (c checkSource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	return c.s.schedules(ctx, scheduler.KindUpdateCheck)
}

func (c checkSource) Validate(ctx context.Context, policyID string) error {
	if config, ok, err := c.s.environmentPolicy(ctx, policyID); err != nil {
		return err
	} else if ok {
		if !config.Check.Enabled {
			return scheduler.Reject(scheduler.RejectPolicyDisabled, "automatic checks are disabled")
		}
		return c.s.environmentTarget(ctx, config)
	}
	p, err := c.s.policy(ctx, policyID)
	if err != nil {
		return err
	}
	if !p.Check.Enabled {
		return scheduler.Reject(scheduler.RejectPolicyDisabled, "automatic update checks of this policy are disabled")
	}
	return c.s.target(ctx, p)
}

func (c checkSource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	if config, ok, err := c.s.environmentPolicy(ctx, due.PolicyID); err != nil {
		return nil, err
	} else if ok {
		children, err := c.s.syncEnvironmentPolicy(ctx, config)
		if err != nil {
			return nil, err
		}
		out := make([]jobs.Request, 0, len(children))
		for _, child := range children {
			out = append(out, checkRequest(child))
		}
		return out, nil
	}
	p, err := c.s.policy(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	return []jobs.Request{checkRequest(p)}, nil
}

type runSource struct{ s *Service }

func (r runSource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	return r.s.schedules(ctx, scheduler.KindUpdateRun)
}

// Validate refuses a disabled policy, a vanished target and a run outside
// the update window (when due and again when the queued job is
// dispatched, so a run that waited for locks past the window is refused).
func (r runSource) Validate(ctx context.Context, policyID string) error {
	if config, ok, err := r.s.environmentPolicy(ctx, policyID); err != nil {
		return err
	} else if ok {
		if !config.Run.Enabled {
			return scheduler.Reject(scheduler.RejectPolicyDisabled, "automatic updates are disabled")
		}
		if !InWindow(domain.UpdatePolicy{Run: config.Run, Window: config.Window}, r.s.clk.Now()) {
			return scheduler.Reject(RejectOutsideWindow, "outside the policy's update window")
		}
		return r.s.environmentTarget(ctx, config)
	}
	p, err := r.s.policy(ctx, policyID)
	if err != nil {
		return err
	}
	if !p.Run.Enabled {
		return scheduler.Reject(scheduler.RejectPolicyDisabled, "automatic updates of this policy are disabled")
	}
	if !InWindow(p, r.s.clk.Now()) {
		return scheduler.Reject(RejectOutsideWindow, "outside the policy's update window")
	}
	return r.s.target(ctx, p)
}

// Jobs plans the run from the latest check: nothing to update skips the
// run; undeployed source changes refuse it (an update never deploys an
// edit).
func (r runSource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	if config, ok, err := r.s.environmentPolicy(ctx, due.PolicyID); err != nil {
		return nil, err
	} else if ok {
		children, err := r.s.syncEnvironmentPolicy(ctx, config)
		if err != nil {
			return nil, err
		}
		var out []jobs.Request
		for _, child := range children {
			pl, err := r.s.planRun(ctx, child, nil)
			if err != nil {
				return nil, err
			}
			if pl.drift {
				return nil, scheduler.Reject(RejectSourceDrift, "a stack has undeployed changes: deploy it before updating")
			}
			if len(pl.items) == 0 {
				continue
			}
			req, err := r.s.request(ctx, pl)
			if err != nil {
				return nil, err
			}
			out = append(out, req)
		}
		return out, nil
	}
	p, err := r.s.policy(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	pl, err := r.s.planRun(ctx, p, nil)
	var ue *domain.UpdateError
	if errors.As(err, &ue) {
		return nil, scheduler.Reject(ue.Code, ue.Message)
	}
	if err != nil {
		return nil, err
	}
	if len(pl.items) == 0 {
		return nil, nil
	}
	if pl.drift {
		return nil, scheduler.Reject(RejectSourceDrift, "the stack's definition on disk differs from the applied revision: deploy it first")
	}
	req, err := r.s.request(ctx, pl)
	if err != nil {
		return nil, err
	}
	return []jobs.Request{req}, nil
}
