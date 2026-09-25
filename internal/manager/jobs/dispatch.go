package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Fault points of the manager engine (stage boundaries).
const (
	PointEnqueueCommitted      = "engine.enqueue.committed"
	PointDispatchLocked        = "engine.dispatch.locked"
	PointDispatchCommitted     = "engine.dispatch.committed"
	PointDispatchSent          = "engine.dispatch.sent"
	PointAckBeforeCommit       = "engine.ack.before_commit"
	PointProgressCommitted     = "engine.progress.committed"
	PointResultBeforeCommit    = "engine.result.before_commit"
	PointResultCommitted       = "engine.result.committed"
	PointReconcileBeforeCommit = "engine.reconcile.before_commit"
	PointReconcileCommitted    = "engine.reconcile.committed"
	PointManagerJobStarted     = "engine.manager_job.started"
	PointManagerJobCommitted   = "engine.manager_job.committed"
)

// errConflict aborts an acquisition transaction whose lock set is no longer
// free; the job stays waiting.
var errConflict = errors.New("jobs: lock conflict")

// waiting is a job that could not be dispatched in this pass. Its lock set
// is reserved for the rest of the pass so later jobs cannot overtake it on
// the same resources (FIFO per resource, no starvation).
type waiting struct {
	id    string
	locks []domain.JobLock
}

// DispatchPending runs one dispatch pass over queued and blocked jobs in
// FIFO order: offline deadlines, lock acquisition, concurrency caps,
// authorization recheck and hand-off to the executor. It also fails
// unacknowledged commands whose agent stayed offline past the deadline.
func (e *Engine) DispatchPending(ctx context.Context) error {
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()

	if err := e.expireUnacknowledged(ctx); err != nil {
		return err
	}
	pending, err := store.JobsInStates(ctx, e.db, domain.JobQueued, domain.JobBlocked)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	heldRows, err := store.HeldLocks(ctx, e.db)
	if err != nil {
		return err
	}
	active, err := store.JobsInStates(ctx, e.db, domain.JobDispatched, domain.JobRunning, domain.JobCancelling)
	if err != nil {
		return err
	}
	// Active jobs per (environment, concurrency class).
	classActive := map[[2]string][]string{}
	for _, a := range active {
		if s, ok := jobspec.Lookup(a.Kind); ok && s.ConcurrencyClass != "" {
			k := [2]string{a.EnvironmentID, s.ConcurrencyClass}
			classActive[k] = append(classActive[k], a.ID)
		}
	}
	var reserved []waiting
	var errs []error
	now := e.now()
	for i := range pending {
		j := &pending[i]
		spec, ok := jobspec.Lookup(j.Kind)
		if !ok {
			errs = append(errs, e.failWaiting(ctx, j, domain.ErrorRejected, "unknown job kind "+string(j.Kind),
				"This manager version does not know the job kind; it cannot run."))
			continue
		}
		if spec.Executor == domain.ExecutorAgent && !e.opts.Dispatcher.Online(j.EnvironmentID) {
			if !now.Before(j.CreatedAt.Add(spec.OfflineDeadline)) {
				errs = append(errs, e.failWaiting(ctx, j, domain.ErrorAgentOffline,
					"the environment's agent was offline for longer than the "+fmtDeadline(spec)+" deadline; nothing was changed",
					"Bring the environment's agent back online, then run the job again."))
				continue
			}
			errs = append(errs, e.setBlocked(ctx, j, domain.BlockedAgentOffline, ""))
			continue
		}
		if _, hi, conflict := firstHeldConflict(j, heldRows); conflict {
			reserved = append(reserved, waiting{j.ID, j.Locks})
			errs = append(errs, e.setBlocked(ctx, j, domain.BlockedLock, heldRows[hi].JobID))
			continue
		}
		if blocker := firstReservedConflict(j, reserved); blocker != "" {
			reserved = append(reserved, waiting{j.ID, j.Locks})
			errs = append(errs, e.setBlocked(ctx, j, domain.BlockedLock, blocker))
			continue
		}
		if spec.ConcurrencyClass != "" {
			k := [2]string{j.EnvironmentID, spec.ConcurrencyClass}
			if limit := e.limits.ConcurrencyCaps[spec.ConcurrencyClass]; limit > 0 && len(classActive[k]) >= limit {
				reserved = append(reserved, waiting{j.ID, j.Locks})
				errs = append(errs, e.setBlocked(ctx, j, domain.BlockedConcurrency, classActive[k][0]))
				continue
			}
		}
		if j.Origin != domain.OriginScheduled {
			caps, err := spec.Capabilities(j.Targets, j.Input)
			d := authz.Deny("the job's input no longer selects its capabilities")
			if err == nil {
				d = e.authorize(ctx, principalOf(j), caps, j.EnvironmentID, j.Targets)
			}
			if !d.Allowed {
				errs = append(errs, e.failWaiting(ctx, j, domain.ErrorAuthorizationRevoked,
					"the initiator no longer holds "+strings.Join(caps, ", ")+" for this job's targets",
					"Ask an administrator to restore the grant, then run the job again."))
				continue
			}
		}
		if reason, err := e.checkScheduled(ctx, *j); err != nil {
			reserved = append(reserved, waiting{j.ID, j.Locks})
			errs = append(errs, fmt.Errorf("revalidate scheduled job %s: %w", j.ID, err))
			continue
		} else if reason != "" {
			errs = append(errs, e.failWaiting(ctx, j, domain.ErrorPolicyRejected,
				"the schedule's policy refused this run when it was dispatched: "+reason+"; nothing was changed",
				"Check the policy's enabled state and targets; the next scheduled run is evaluated again."))
			continue
		}
		dispatched, err := e.acquire(ctx, j, spec)
		if errors.Is(err, errConflict) || errors.Is(err, errNotWaiting) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, l := range dispatched.Locks {
			heldRows = append(heldRows, store.HeldLock{JobID: dispatched.ID, Lock: l})
		}
		if spec.ConcurrencyClass != "" {
			k := [2]string{j.EnvironmentID, spec.ConcurrencyClass}
			classActive[k] = append(classActive[k], j.ID)
		}
		e.notify(dispatched.ID)
		if err := faultinject.Point(ctx, PointDispatchCommitted); err != nil {
			return err
		}
		if spec.Executor == domain.ExecutorAgent {
			e.sendCommand(ctx, &dispatched, spec)
			if err := faultinject.Point(ctx, PointDispatchSent); err != nil {
				return err
			}
		} else {
			e.startManagerJob(dispatched)
		}
	}
	return errors.Join(errs...)
}

func firstHeldConflict(j *domain.Job, held []store.HeldLock) (int, int, bool) {
	for wi, w := range j.Locks {
		for hi, h := range held {
			if h.JobID != j.ID && jobspec.Conflicts(w, h.Lock) {
				return wi, hi, true
			}
		}
	}
	return -1, -1, false
}

func firstReservedConflict(j *domain.Job, reserved []waiting) string {
	for _, w := range j.Locks {
		for _, r := range reserved {
			for _, l := range r.locks {
				if jobspec.Conflicts(w, l) {
					return r.id
				}
			}
		}
	}
	return ""
}

func fmtDeadline(s jobspec.Spec) string { return s.OfflineDeadline.String() }

var errNotWaiting = errors.New("jobs: job is no longer waiting")

// acquire atomically takes the job's full lock set (sorted, all or nothing)
// in one transaction, allocates the fencing token and marks the job
// dispatched.
func (e *Engine) acquire(ctx context.Context, j *domain.Job, spec jobspec.Spec) (domain.Job, error) {
	var out domain.Job
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if !cur.State.Waiting() {
			return errNotWaiting
		}
		held, err := store.HeldLocks(ctx, tx)
		if err != nil {
			return err
		}
		if _, _, conflict := firstHeldConflict(&cur, held); conflict {
			return errConflict
		}
		locks := jobspec.NormalizeLocks(cur.Locks) // sorted acquisition order
		now := e.now()
		for i, l := range locks {
			if e.testHookLockInsert != nil {
				if err := e.testHookLockInsert(i); err != nil {
					return err
				}
			}
			if err := store.InsertJobLock(ctx, tx, cur.ID, l, now); err != nil {
				return err
			}
		}
		if err := faultinject.Point(ctx, PointDispatchLocked); err != nil {
			return err
		}
		msg := "dispatched to the manager"
		if spec.Executor == domain.ExecutorAgent {
			tok, err := store.NextFencingToken(ctx, tx, cur.EnvironmentID)
			if err != nil {
				return err
			}
			cur.FencingToken = tok
			msg = "dispatched to the agent (attempt " + strconv.Itoa(cur.Attempt) + ", fencing token " + strconv.FormatUint(tok, 10) + ")"
		}
		if err := e.transition(ctx, tx, &cur, domain.JobDispatched, msg); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// setBlocked records why a waiting job cannot start (only when it changed).
func (e *Engine) setBlocked(ctx context.Context, j *domain.Job, reason, by string) error {
	if j.State == domain.JobBlocked && j.BlockedReason == reason && j.BlockedBy == by {
		return nil
	}
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if !cur.State.Waiting() {
			return nil
		}
		msg := "blocked: " + reason
		if by != "" {
			msg += " (by job " + by + ")"
		}
		if cur.State == domain.JobQueued {
			if err := e.transition(ctx, tx, &cur, domain.JobBlocked, msg); err != nil {
				return err
			}
		} else if err := e.event(ctx, tx, domain.JobEvent{JobID: cur.ID, Type: domain.JobEventState, State: domain.JobBlocked, Message: msg}); err != nil {
			return err
		}
		cur.BlockedReason, cur.BlockedBy, cur.UpdatedAt = reason, by, e.now()
		return store.UpdateJob(ctx, tx, &cur)
	})
	if err == nil {
		e.notify(j.ID)
	}
	return err
}

// failWaiting fails a queued/blocked job (offline deadline, lost grant).
func (e *Engine) failWaiting(ctx context.Context, j *domain.Job, class, message, recovery string) error {
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if !cur.State.Waiting() {
			return nil
		}
		return e.finish(ctx, tx, &cur, domain.JobFailed, class, message, recovery)
	})
	if err == nil {
		e.notify(j.ID)
	}
	return err
}

// expireUnacknowledged fails dispatched agent jobs whose command was never
// acknowledged while the agent stayed offline past the kind's deadline.
func (e *Engine) expireUnacknowledged(ctx context.Context) error {
	jobs, err := store.JobsInStates(ctx, e.db, domain.JobDispatched)
	if err != nil {
		return err
	}
	now := e.now()
	var errs []error
	for i := range jobs {
		j := &jobs[i]
		spec, ok := jobspec.Lookup(j.Kind)
		if !ok || j.Executor != domain.ExecutorAgent || e.opts.Dispatcher.Online(j.EnvironmentID) || j.DispatchedAt == nil {
			continue
		}
		if now.Before(j.DispatchedAt.Add(spec.OfflineDeadline)) {
			continue
		}
		err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
			cur, err := store.GetJob(ctx, tx, j.ID)
			if err != nil || cur.State != domain.JobDispatched || cur.Attempt != j.Attempt {
				return err
			}
			return e.finish(ctx, tx, &cur, domain.JobFailed, domain.ErrorAgentOffline,
				"the agent went offline before acknowledging the command and did not return within the "+fmtDeadline(spec)+" deadline",
				"If the agent received the command before going offline it may still report an outcome when it reconnects; "+
					"check the job's targets once the environment is back, then run the job again if needed.")
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		e.notify(j.ID)
	}
	return errors.Join(errs...)
}

// sendCommand sends the job's command frame. Failures leave the job
// dispatched: the reconnect report or the offline deadline resolves it.
// Callers hold dispatchMu (token order == send order).
func (e *Engine) sendCommand(ctx context.Context, j *domain.Job, spec jobspec.Spec) {
	ref := protocol.JobRef{JobID: j.ID, Attempt: uint32(j.Attempt), FencingToken: j.FencingToken} //nolint:gosec // attempts are small
	var secrets *protocol.CommandSecrets
	if e.opts.CommandSecrets != nil {
		s, err := e.opts.CommandSecrets(ctx, j)
		if err != nil {
			e.failUnsent(ctx, j, err)
			return
		}
		secrets = s
	}
	f, err := protocol.NewCommandFrame(ref, e.now().Add(spec.OfflineDeadline),
		protocol.CommandPayload{Kind: string(j.Kind), Input: j.Input, CompletedSteps: j.CompletedSteps, Secrets: secrets})
	if err != nil {
		e.opts.Logger.Error("could not build job command", "job_id", j.ID, "error", err)
		return
	}
	if err := e.opts.Dispatcher.Send(ctx, j.EnvironmentID, f); err != nil {
		e.opts.Logger.Warn("could not deliver job command; it is reconciled when the agent reconnects",
			"job_id", j.ID, "environment_id", j.EnvironmentID, "error", err)
	}
}

// failUnsent fails a dispatched attempt whose command could not be built
// because a credential it needs is unavailable; the agent never saw it.
func (e *Engine) failUnsent(ctx context.Context, j *domain.Job, cause error) {
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil || cur.State != domain.JobDispatched || cur.Attempt != j.Attempt {
			return err
		}
		return e.finish(ctx, tx, &cur, domain.JobFailed, domain.ErrorCredentialUnavailable,
			"a credential this job needs is unavailable: "+cause.Error()+"; nothing was sent to the agent",
			"Ask the instance owner to restore or re-select the registry connection or Git credential, then run the job again.")
	})
	if err != nil {
		e.opts.Logger.Error("could not fail a job whose credential is unavailable", "job_id", j.ID, "error", err)
		return
	}
	e.notify(j.ID)
}

// sendCancel asks the agent to cancel the job at its next safe point.
func (e *Engine) sendCancel(ctx context.Context, j *domain.Job) {
	ref := protocol.JobRef{JobID: j.ID, Attempt: uint32(j.Attempt), FencingToken: j.FencingToken} //nolint:gosec // attempts are small
	f, err := protocol.NewFrame(protocol.TypeCancel, "cancel."+j.ID+"."+strconv.Itoa(j.Attempt),
		protocol.CommandFrameID(j.ID, ref.Attempt), ref, nil)
	if err != nil {
		e.opts.Logger.Error("could not build cancel frame", "job_id", j.ID, "error", err)
		return
	}
	if err := e.opts.Dispatcher.Send(ctx, j.EnvironmentID, f); err != nil {
		e.opts.Logger.Warn("could not deliver cancellation; it is re-sent when the agent reconnects", "job_id", j.ID, "error", err)
	}
}

// redispatch starts a new attempt of an active agent job with a fresh
// fencing token (resume after an interruption, or a command that never
// reached the agent). The caller holds dispatchMu and sends afterwards.
func (e *Engine) redispatch(ctx context.Context, tx bun.IDB, j *domain.Job, completed []string, reason string) error {
	tok, err := store.NextFencingToken(ctx, tx, j.EnvironmentID)
	if err != nil {
		return err
	}
	j.Attempt++
	j.Resumes++
	j.FencingToken = tok
	j.CompletedSteps = completed
	j.CurrentStep, j.StepInFlight = "", false
	// startedAt tracks the current attempt: nil until the agent acknowledges
	// it, which reconciliation relies on to tell "never received" from
	// "acknowledged but lost".
	j.StartedAt = nil
	msg := fmt.Sprintf("re-dispatched as attempt %d (fencing token %d): %s", j.Attempt, tok, reason)
	if j.State == domain.JobRunning {
		return e.transition(ctx, tx, j, domain.JobDispatched, msg)
	}
	now := e.now()
	j.DispatchedAt, j.UpdatedAt = &now, now
	if err := store.UpdateJob(ctx, tx, j); err != nil {
		return err
	}
	return e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventState, State: j.State, Message: msg})
}
