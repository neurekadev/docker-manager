package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

type managerRun struct {
	attempt int
	cancel  atomic.Bool
}

// RegisterManagerExecutor installs the implementation of a manager-executed
// kind (manager.backup, backup.retention, ...). Register before Recover/Run;
// jobs of manager kinds without an executor are refused at Enqueue.
func (e *Engine) RegisterManagerExecutor(x jobexec.Executor) error {
	if err := x.Validate(domain.ExecutorManager); err != nil {
		return err
	}
	e.mgrMu.Lock()
	defer e.mgrMu.Unlock()
	if _, dup := e.mgrExecs[x.Kind]; dup {
		return fmt.Errorf("jobs: executor for %s already registered", x.Kind)
	}
	e.mgrExecs[x.Kind] = x
	return nil
}

// dbJournal journals manager-local attempts in the jobs row.
type dbJournal struct{ e *Engine }

func (d dbJournal) Save(ctx context.Context, st *jobexec.State) error {
	err := d.e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		j, err := store.GetJob(ctx, tx, st.JobID)
		if err != nil {
			return err
		}
		if uint32(j.Attempt) != st.Attempt { //nolint:gosec // attempts are small
			return fmt.Errorf("jobs: attempt %d of %s superseded by %d", st.Attempt, st.JobID, j.Attempt)
		}
		j.CurrentStep, j.StepInFlight = st.CurrentStep, st.StepInFlight
		j.CompletedSteps = slices.Clone(st.Completed)
		j.Compensations = j.Compensations[:0]
		for _, c := range st.Compensations {
			j.Compensations = append(j.Compensations, domain.JobCompensation{Name: c.Name, Args: c.Args, Released: c.Released, Done: c.Done, Error: c.Error})
		}
		j.Items = j.Items[:0]
		for _, it := range st.Items {
			j.Items = append(j.Items, domain.JobItem{Name: it.Name, Status: it.Status, Message: it.Message})
		}
		j.UpdatedAt = d.e.now()
		return store.UpdateJob(ctx, tx, &j)
	})
	if err == nil {
		d.e.notify(st.JobID)
	}
	return err
}

func stateFromJob(j *domain.Job) jobexec.State {
	st := jobexec.State{JobID: j.ID, Attempt: uint32(j.Attempt), Kind: j.Kind, Input: j.Input, //nolint:gosec // attempts are small
		CurrentStep: j.CurrentStep, StepInFlight: j.StepInFlight, Completed: slices.Clone(j.CompletedSteps)}
	for _, c := range j.Compensations {
		st.Compensations = append(st.Compensations, jobexec.Compensation{Name: c.Name, Args: c.Args, Released: c.Released, Done: c.Done, Error: c.Error})
	}
	for _, it := range j.Items {
		st.Items = append(st.Items, protocol.ItemPayload{Name: it.Name, Status: it.Status, Message: it.Message})
	}
	return st
}

type managerReporter struct {
	e       *Engine
	attempt int
}

func (r managerReporter) Progress(ctx context.Context, st *jobexec.State, p protocol.ProgressPayload) {
	_, _ = r.e.applyProgress(ctx, st.JobID, func(j *domain.Job) bool { return j.Attempt == r.attempt }, p)
}

// startManagerJob runs a dispatched manager-local job in a goroutine.
func (e *Engine) startManagerJob(j domain.Job) {
	e.mgrMu.Lock()
	exec, ok := e.mgrExecs[j.Kind]
	run := &managerRun{attempt: j.Attempt}
	run.cancel.Store(j.CancelRequested)
	if ok {
		e.mgrRunning[j.ID] = run
	}
	e.mgrMu.Unlock()
	if !ok {
		err := e.tx(e.lifetime, func(ctx context.Context, tx bun.Tx) error {
			cur, err := store.GetJob(ctx, tx, j.ID)
			if err != nil || !cur.State.Active() {
				return err
			}
			return e.finish(ctx, tx, &cur, domain.JobFailed, domain.ErrorRejected,
				"this manager has no executor for "+string(j.Kind), "Nothing was changed.")
		})
		if err != nil {
			e.opts.Logger.Error("could not fail job without executor", "job_id", j.ID, "error", err)
		}
		return
	}
	e.wg.Add(1)
	go e.runManagerJob(j, exec, run)
}

func (e *Engine) runManagerJob(j domain.Job, exec jobexec.Executor, run *managerRun) {
	defer e.wg.Done()
	defer func() {
		e.mgrMu.Lock()
		if e.mgrRunning[j.ID] == run {
			delete(e.mgrRunning, j.ID)
		}
		e.mgrMu.Unlock()
	}()
	ctx := e.lifetime
	log := e.opts.Logger.With("job_id", j.ID, "kind", j.Kind, "attempt", j.Attempt)
	var st jobexec.State
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if cur.Attempt != j.Attempt || !cur.State.Active() {
			return errNotWaiting
		}
		if cur.State == domain.JobDispatched {
			if err := e.transition(ctx, tx, &cur, domain.JobRunning, "started on the manager"); err != nil {
				return err
			}
		}
		if cur.CancelRequested {
			run.cancel.Store(true)
		}
		st = stateFromJob(&cur)
		return nil
	})
	if err != nil {
		if !errors.Is(err, errNotWaiting) && ctx.Err() == nil {
			log.Error("could not start manager job", "error", err)
		}
		return
	}
	e.notify(j.ID)
	if faultinject.Point(ctx, PointManagerJobStarted) != nil {
		return
	}
	res, err := jobexec.Run(ctx, exec, &st, jobexec.Options{Journal: dbJournal{e}, Reporter: managerReporter{e, j.Attempt},
		CancelRequested: run.cancel.Load, FaultPrefix: "manager"})
	if errors.Is(err, jobexec.ErrAbandoned) {
		return // recovered at the next start
	}
	if err != nil {
		log.Error("could not journal manager job outcome", "error", err)
	}
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if cur.Attempt != j.Attempt || cur.State.Terminal() {
			return nil
		}
		_, err = e.applyResult(ctx, tx, &cur, res)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			log.Error("could not record manager job outcome", "error", err)
		}
		return
	}
	e.notify(j.ID)
	_ = faultinject.Point(ctx, PointManagerJobCommitted)
	e.Wake()
}

// Recover handles jobs left active by a previous manager process. Call it
// once at startup, after registering manager executors and before Run.
// Manager-local jobs resume (kinds with RestartResume whose in-flight step
// is idempotent) or become interrupted after their compensations ran. Agent
// jobs keep their state and locks; they are reconciled when their agent
// reconnects and sends its job_report.
func (e *Engine) Recover(ctx context.Context) error {
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	active, err := store.JobsInStates(ctx, e.db, domain.JobDispatched, domain.JobRunning, domain.JobCancelling)
	if err != nil {
		return err
	}
	var errs []error
	agentJobs := 0
	for i := range active {
		j := active[i]
		if j.Executor != domain.ExecutorManager {
			agentJobs++
			continue
		}
		if err := e.recoverManagerJob(ctx, j); err != nil {
			errs = append(errs, err)
		}
	}
	if agentJobs > 0 {
		e.opts.Logger.Info("agent jobs await reconciliation when their agents reconnect", "count", agentJobs)
	}
	return errors.Join(errs...)
}

func (e *Engine) recoverManagerJob(ctx context.Context, j domain.Job) error {
	spec, _ := jobspec.Lookup(j.Kind)
	e.mgrMu.Lock()
	exec, hasExec := e.mgrExecs[j.Kind]
	e.mgrMu.Unlock()
	var execPtr *jobexec.Executor
	if hasExec {
		execPtr = &exec
	}
	st := stateFromJob(&j)
	res, err := jobexec.Recover(ctx, execPtr, &st, jobexec.Options{Journal: dbJournal{e}, FaultPrefix: "manager"})
	if err != nil {
		return err
	}
	// A job that never started a step has done nothing: it is started
	// again whatever its restart policy.
	neverStarted := len(res.CompletedSteps) == 0 && res.InterruptedStep == "" && len(res.Compensations) == 0
	// A job whose every step completed only lacks its recorded outcome:
	// "resuming" it runs no step and records success.
	allDone := res.InterruptedStep == "" && !slices.ContainsFunc(spec.Steps, func(s jobspec.Step) bool {
		return !slices.Contains(res.CompletedSteps, s.Name)
	})
	neverStarted = neverStarted || allDone
	var restart *domain.Job
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, j.ID)
		if err != nil || !cur.State.Active() {
			return err
		}
		switch {
		case res.Resumable && cur.CancelRequested:
			return e.finish(ctx, tx, &cur, domain.JobCancelled, domain.ErrorCancelled,
				"cancelled: not resumed after the manager restarted", "The job stopped between steps. Run it again if needed.")
		case res.Resumable && hasExec && (spec.OnManagerRestart == jobspec.RestartResume || neverStarted) && cur.Resumes < e.opts.MaxResumes:
			cur.Attempt++
			cur.Resumes++
			cur.CompletedSteps = slices.Clone(res.CompletedSteps)
			cur.CurrentStep, cur.StepInFlight = "", false
			cur.StartedAt = nil
			msg := fmt.Sprintf("resuming as attempt %d after a manager restart", cur.Attempt)
			if cur.State == domain.JobRunning {
				if err := e.transition(ctx, tx, &cur, domain.JobDispatched, msg); err != nil {
					return err
				}
			} else {
				cur.UpdatedAt = e.now()
				if err := store.UpdateJob(ctx, tx, &cur); err != nil {
					return err
				}
				if err := e.event(ctx, tx, domain.JobEvent{JobID: cur.ID, Type: domain.JobEventState, State: cur.State, Message: msg}); err != nil {
					return err
				}
			}
			restart = &cur
			return nil
		}
		class, msg, rec := res.ErrorClass, res.Message, res.Recovery
		if class == "" {
			class = domain.ErrorExecutorRestarted
		}
		if res.Resumable {
			msg = "the manager restarted while the job ran; " + string(spec.OnManagerRestart) + " policy: not resumed"
			if cur.Resumes >= e.opts.MaxResumes {
				class = domain.ErrorResumeLimit
			}
		}
		if rec == "" {
			rec = "The manager restarted while this job ran. Check its targets, then run it again."
		}
		return e.finish(ctx, tx, &cur, domain.JobInterrupted, class, msg, rec)
	})
	if err != nil {
		return err
	}
	e.notify(j.ID)
	if restart != nil {
		e.startManagerJob(*restart)
	}
	return nil
}

// Cancel requests cancellation. Waiting jobs are cancelled immediately;
// active jobs move to cancelling and stop at their kind's next safe point
// (compensations always run). Authorization is the caller's job (API).
func (e *Engine) Cancel(ctx context.Context, id string) (domain.Job, error) {
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	var out domain.Job
	signal := false
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetJob(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case cur.State.Terminal():
			return domain.ErrJobFinished
		case cur.State.Waiting():
			cur.CancelRequested = true
			if err := e.finish(ctx, tx, &cur, domain.JobCancelled, domain.ErrorCancelled,
				"cancelled before it started", "Nothing was changed."); err != nil {
				return err
			}
		case !cur.CancelRequested:
			cur.CancelRequested = true
			signal = true
			if cur.State != domain.JobCancelling {
				if err := e.transition(ctx, tx, &cur, domain.JobCancelling, "cancellation requested; honored at the next safe point"); err != nil {
					return err
				}
			}
		}
		out = cur
		return nil
	})
	if err != nil {
		return domain.Job{}, err
	}
	if signal {
		if out.Executor == domain.ExecutorAgent {
			e.sendCancel(ctx, &out)
		} else {
			e.mgrMu.Lock()
			if r, ok := e.mgrRunning[out.ID]; ok && r.attempt == out.Attempt {
				r.cancel.Store(true)
			}
			e.mgrMu.Unlock()
		}
	}
	e.notify(id)
	e.Wake()
	return out, nil
}

// Retain deletes terminal jobs beyond the history bounds (age and count),
// with their events. Audit records (#30) are separate and unaffected.
func (e *Engine) Retain(ctx context.Context) (int, error) {
	return store.DeleteFinishedJobs(ctx, e.db, e.now().Add(-e.limits.HistoryMaxAge), e.limits.HistoryMaxJobs)
}
