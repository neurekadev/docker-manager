package updates

import (
	"context"
	"errors"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/store"
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

func (s *Service) schedules(ctx context.Context, pick func(domain.UpdatePolicy) domain.UpdateSchedule) ([]scheduler.PolicySchedule, error) {
	ps, err := store.ListUpdatePolicies(ctx, s.db, "", "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.PolicySchedule, 0, len(ps))
	for _, p := range ps {
		sc := pick(p)
		out = append(out, scheduler.PolicySchedule{PolicyID: p.ID, Name: p.Name, EnvironmentID: p.EnvironmentID,
			Cron: sc.Cron, TimeZone: sc.TimeZone, Enabled: sc.Enabled})
	}
	return out, nil
}

// policy loads a policy for the scheduler: a deleted one is rejected.
func (s *Service) policy(ctx context.Context, id string) (domain.UpdatePolicy, error) {
	p, err := store.GetUpdatePolicy(ctx, s.db, id)
	if errors.Is(err, domain.ErrUpdatePolicyNotFound) {
		return p, scheduler.Reject(scheduler.RejectPolicyNotFound, "the update policy was deleted")
	}
	return p, err
}

// target rejects a policy whose target is gone.
func (s *Service) target(ctx context.Context, p domain.UpdatePolicy) error {
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
	return c.s.schedules(ctx, func(p domain.UpdatePolicy) domain.UpdateSchedule { return p.Check })
}

func (c checkSource) Validate(ctx context.Context, policyID string) error {
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
	p, err := c.s.policy(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	return []jobs.Request{checkRequest(p)}, nil
}

type runSource struct{ s *Service }

func (r runSource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	return r.s.schedules(ctx, func(p domain.UpdatePolicy) domain.UpdateSchedule { return p.Run })
}

// Validate refuses a disabled policy, a vanished target and a run outside
// the update window (when due and again when the queued job is
// dispatched, so a run that waited for locks past the window is refused).
func (r runSource) Validate(ctx context.Context, policyID string) error {
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
