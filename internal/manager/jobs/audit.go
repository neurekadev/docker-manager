package jobs

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
)

// Job lifecycle audit records (#30). Every kind emits, in the same
// transaction as the state change:
//
//   - job.queued when a job is created (actor: the initiator, or the
//     service identity for scheduled jobs; with the request's client IP and
//     request ID when enqueued from the API);
//   - job.started when an attempt starts running;
//   - job.cancel_requested when cancellation is requested (actor: the
//     caller);
//   - job.finished when it reaches a terminal state (outcome from the
//     state, error class, item list such as the resources a prune deleted).
//
// started/finished act on the initiator's behalf but are not requests:
// they never carry a client IP or request ID. Records hold the job ID,
// kind, capability, origin, attempt and targets; never the job input,
// messages or item messages (they may carry data).

func (e *Engine) recordJob(ctx context.Context, db bun.IDB, j *domain.Job, action string, actor domain.AuditActor,
	outcome domain.AuditOutcome, errorClass string, extra map[string]any,
) error {
	if e.opts.Audit == nil {
		return nil
	}
	details := map[string]any{
		"kind": string(j.Kind), "origin": string(j.Origin), "executor": string(j.Executor), "attempt": j.Attempt,
	}
	if spec, ok := jobspec.Lookup(j.Kind); ok {
		details["capability"] = spec.Capability
	}
	if j.PolicyID != "" {
		details["policyId"] = j.PolicyID
	}
	for k, v := range extra {
		details[k] = v
	}
	targets := []domain.AuditTarget{{Type: "job", ID: j.ID}}
	for _, t := range j.Targets {
		env := t.EnvironmentID
		if env == "" {
			env = j.EnvironmentID
		}
		targets = append(targets, domain.AuditTarget{Type: string(t.Type), ID: t.ID, EnvironmentID: env})
	}
	return e.opts.Audit.RecordTx(ctx, db, domain.AuditEvent{
		At: e.now(), Category: domain.AuditOperations, Action: action, Actor: actor,
		EnvironmentID: j.EnvironmentID, Targets: targets, Outcome: outcome, ErrorClass: errorClass,
		JobID: j.ID, Details: details,
	})
}

// maxAuditItems bounds the item list of a job.finished record (itemCount
// has the full number; the job keeps every item result).
const maxAuditItems = 100

// initiatorActor is the principal a job runs for.
func initiatorActor(j *domain.Job) domain.AuditActor { return audit.ActorFor(principalOf(j)) }

// withoutRequest masks the request metadata of ctx (the job acts, not the
// request that happened to trigger the transition).
func withoutRequest(ctx context.Context) context.Context {
	return requestinfo.With(logging.WithRequestID(ctx, ""), requestinfo.Info{})
}

// auditTransition records job.started / job.finished for a state change.
func (e *Engine) auditTransition(ctx context.Context, db bun.IDB, j *domain.Job, to domain.JobState) error {
	switch {
	case to == domain.JobRunning:
		return e.recordJob(withoutRequest(ctx), db, j, audit.ActionJobStarted, initiatorActor(j), domain.AuditSuccess, "", nil)
	case to.Terminal():
		outcome := domain.AuditFailure
		switch to {
		case domain.JobSucceeded:
			outcome = domain.AuditSuccess
		case domain.JobPartial:
			outcome = domain.AuditPartial
		}
		extra := map[string]any{"state": string(to)}
		if len(j.Items) > 0 {
			n := min(len(j.Items), maxAuditItems)
			items := make([]map[string]any, 0, n)
			for _, it := range j.Items[:n] {
				items = append(items, map[string]any{"name": it.Name, "status": it.Status})
			}
			extra["items"], extra["itemCount"] = items, len(j.Items)
		}
		errorClass := j.ErrorClass
		if to == domain.JobSucceeded {
			errorClass = ""
		}
		return e.recordJob(withoutRequest(ctx), db, j, audit.ActionJobFinished, initiatorActor(j), outcome, errorClass, extra)
	}
	return nil
}

// cancelActor is whoever asked for the cancellation: the request principal,
// or the service identity for in-process callers.
func cancelActor(ctx context.Context) domain.AuditActor {
	if p, ok := authz.PrincipalFrom(ctx); ok {
		return audit.ActorFor(p)
	}
	return audit.ServiceActor()
}
