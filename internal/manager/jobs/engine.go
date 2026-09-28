// Package jobs is the manager-owned job engine (#26). Every long or
// mutating operation (pull, build, deploy, stack operations, update, prune,
// backup, restore, retention, verification, file archive/extract/metadata)
// runs as a durable job:
//
//   - Enqueue validates the kind (internal/jobspec), computes the lock set
//     from the targets, authorizes manual/API-token requests, applies
//     idempotency and stores a queued job.
//   - DispatchPending (driven by Run) acquires each job's full lock set
//     atomically and in sorted order in one transaction, rechecks the
//     initiator's grant, allocates a per-environment fencing token and hands
//     the job to its executor: the environment's agent via AgentDispatcher,
//     or an in-process manager executor. Jobs that cannot start show why
//     (blocked_by / blocked_reason).
//   - Agent frames (ack, progress, result, job_report) come back through
//     HandleAgentFrame; reconnect reports are reconciled, never re-run.
//   - Every state change goes through transition(), which enforces the
//     domain state machine and releases locks on terminal states.
//
// See docs/internal/architecture/job-engine.md.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Defaults.
const (
	DefaultPollInterval      = 5 * time.Second
	DefaultRetentionInterval = time.Hour
	DefaultMaxResumes        = 3
	DefaultHistoryMaxAge     = 30 * 24 * time.Hour
	DefaultHistoryMaxJobs    = 10000
	DefaultMaxEventsPerJob   = 500
	DefaultPullCap           = 2
	DefaultBuildCap          = 1
	// MaxInputSize bounds a job's JSON input.
	MaxInputSize = 64 << 10
)

// Limits are the configurable engine bounds.
type Limits struct {
	// ConcurrencyCaps limits active jobs per environment and concurrency
	// class (jobspec.ClassPull, jobspec.ClassBuild). Zero/missing: default.
	ConcurrencyCaps map[string]int
	// HistoryMaxAge deletes terminal jobs finished longer ago.
	HistoryMaxAge time.Duration
	// HistoryMaxJobs keeps at most this many terminal jobs.
	HistoryMaxJobs int
	// MaxEventsPerJob bounds each job's progress/event log.
	MaxEventsPerJob int
}

func (l Limits) withDefaults() Limits {
	caps := map[string]int{jobspec.ClassPull: DefaultPullCap, jobspec.ClassBuild: DefaultBuildCap}
	for k, v := range l.ConcurrencyCaps {
		if v > 0 {
			caps[k] = v
		}
	}
	l.ConcurrencyCaps = caps
	if l.HistoryMaxAge <= 0 {
		l.HistoryMaxAge = DefaultHistoryMaxAge
	}
	if l.HistoryMaxJobs <= 0 {
		l.HistoryMaxJobs = DefaultHistoryMaxJobs
	}
	if l.MaxEventsPerJob <= 0 {
		l.MaxEventsPerJob = DefaultMaxEventsPerJob
	}
	return l
}

// Options configures the engine.
type Options struct {
	DB     *bun.DB
	Clock  clock.Clock
	Logger *slog.Logger
	// Dispatcher reaches agents; NoAgents until the #3 transport exists.
	Dispatcher AgentDispatcher
	// Authorizer checks manual/API-token jobs; DenyAll when nil.
	Authorizer        authz.Authorizer
	Limits            Limits
	PollInterval      time.Duration
	RetentionInterval time.Duration
	// MaxResumes bounds automatic re-dispatches of one job.
	MaxResumes int
	// Audit records job lifecycle events (#30) inside the engine's
	// transactions; the manager always sets it (audit.go).
	Audit audit.TxRecorder
	// CommandSecrets resolves the credentials an agent command carries
	// (#19 registry connections, #33 Git credentials) at every dispatch,
	// including re-dispatches after a resume, so a rotated credential is
	// used by every later attempt and no credential is stored with the job.
	// nil: commands carry none. An error fails the dispatched job with
	// class credential_unavailable (the error's message must not contain
	// secrets) and nothing is sent.
	CommandSecrets func(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error)
	// CommandInput adapts a job's input to the agent receiving it at every
	// dispatch (an optional field only agents announcing its feature
	// accept, filled with the current setting, #10 backup compression).
	// It returns the input to send; nil sends j.Input as stored. It never
	// changes the stored input.
	CommandInput func(ctx context.Context, j *domain.Job) json.RawMessage
}

// Engine is the job engine. Create it with New.
type Engine struct {
	opts   Options
	db     *bun.DB
	limits Limits

	// dispatchMu serializes fencing-token allocation with command sends so
	// the commands of one environment leave in token order.
	dispatchMu sync.Mutex
	wake       chan struct{}

	subsMu sync.Mutex
	subs   map[string]map[chan struct{}]struct{}
	// changeListeners observe every job change (OnChange).
	changeListeners []func(jobIDs []string)
	// activityListeners receive transient activity reports (OnActivity).
	activityListeners []func(environmentID, jobID string, a protocol.ActivityPayload)

	mgrMu      sync.Mutex
	mgrExecs   map[domain.JobKind]jobexec.Executor
	mgrRunning map[string]*managerRun

	hooksMu        sync.RWMutex
	finishHooks    map[domain.JobKind][]FinishHook
	retriers       map[domain.JobKind]Retrier
	scheduledCheck ScheduledCheck

	lifetime context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup

	// testHookLockInsert, when set, runs before inserting the i-th lock.
	testHookLockInsert func(i int) error
}

// New creates an engine. Call Recover once at startup, then Run.
func New(opts Options) (*Engine, error) {
	if opts.DB == nil {
		return nil, errors.New("jobs: DB is required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Dispatcher == nil {
		opts.Dispatcher = NoAgents{}
	}
	opts.Authorizer = authz.OrDenyAll(opts.Authorizer)
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.RetentionInterval <= 0 {
		opts.RetentionInterval = DefaultRetentionInterval
	}
	if opts.MaxResumes <= 0 {
		opts.MaxResumes = DefaultMaxResumes
	}
	lifetime, stop := context.WithCancel(context.Background())
	return &Engine{
		opts: opts, db: opts.DB, limits: opts.Limits.withDefaults(),
		wake: make(chan struct{}, 1), subs: map[string]map[chan struct{}]struct{}{},
		mgrExecs: map[domain.JobKind]jobexec.Executor{}, mgrRunning: map[string]*managerRun{},
		finishHooks: map[domain.JobKind][]FinishHook{}, retriers: map[domain.JobKind]Retrier{},
		lifetime: lifetime, stop: stop,
	}, nil
}

// Close stops manager-local job goroutines (they are recovered on the next
// start) and waits for them.
func (e *Engine) Close() {
	e.stop()
	e.wg.Wait()
}

// Wait blocks until all manager-local job goroutines have returned.
func (e *Engine) Wait() { e.wg.Wait() }

// Run drives dispatching, deadlines and retention until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	poll := e.opts.Clock.NewTicker(e.opts.PollInterval)
	defer poll.Stop()
	retain := e.opts.Clock.NewTicker(e.opts.RetentionInterval)
	defer retain.Stop()
	e.pass(ctx)
	e.retain(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-e.wake:
		case <-poll.C():
		case <-retain.C():
			e.retain(ctx)
		}
		e.pass(ctx)
	}
}

func (e *Engine) pass(ctx context.Context) {
	if err := e.DispatchPending(ctx); err != nil && ctx.Err() == nil {
		e.opts.Logger.Error("job dispatch pass failed", "error", err)
	}
}

func (e *Engine) retain(ctx context.Context) {
	if n, err := e.Retain(ctx); err != nil && ctx.Err() == nil {
		e.opts.Logger.Error("job retention failed", "error", err)
	} else if n > 0 {
		e.opts.Logger.Info("deleted old jobs", "count", n)
	}
}

// Wake schedules a dispatch pass (e.g. after an agent connected).
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) now() time.Time { return e.opts.Clock.Now().UTC().Truncate(time.Microsecond) }

// Get returns a job.
func (e *Engine) Get(ctx context.Context, id string) (domain.Job, error) {
	return store.GetJob(ctx, e.db, id)
}

// List returns jobs matching f, newest first.
func (e *Engine) List(ctx context.Context, f domain.JobFilter) ([]domain.Job, error) {
	return store.ListJobs(ctx, e.db, f)
}

// Count returns the number of jobs matching f (one COUNT query; f.BeforeID
// and f.Limit are ignored).
func (e *Engine) Count(ctx context.Context, f domain.JobFilter) (int64, error) {
	return store.CountMatchingJobs(ctx, e.db, f)
}

// Events returns up to limit events of a job after afterSeq.
func (e *Engine) Events(ctx context.Context, jobID string, afterSeq int64, limit int) ([]domain.JobEvent, error) {
	return store.JobEvents(ctx, e.db, jobID, afterSeq, limit)
}

// Subscribe returns a channel signaled (coalesced) whenever the job or its
// events change. Call cancel when done.
func (e *Engine) Subscribe(jobID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.subsMu.Lock()
	if e.subs[jobID] == nil {
		e.subs[jobID] = map[chan struct{}]struct{}{}
	}
	e.subs[jobID][ch] = struct{}{}
	e.subsMu.Unlock()
	return ch, func() {
		e.subsMu.Lock()
		defer e.subsMu.Unlock()
		delete(e.subs[jobID], ch)
		if len(e.subs[jobID]) == 0 {
			delete(e.subs, jobID)
		}
	}
}

func (e *Engine) notify(jobIDs ...string) {
	e.subsMu.Lock()
	for _, id := range jobIDs {
		for ch := range e.subs[id] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	listeners := e.changeListeners
	e.subsMu.Unlock()
	for _, fn := range listeners {
		fn(jobIDs)
	}
}

// OnActivity registers fn, called with every activity report (#10) of an
// agent session (environmentID) or a manager-local job (""). Reports are
// transient: nothing is stored, no job event is written and OnChange does
// not fire. The job ID is the reporter's claim; fn must check it against
// the job's environment. fn runs on the reporting goroutine and must not
// block.
func (e *Engine) OnActivity(fn func(environmentID, jobID string, a protocol.ActivityPayload)) {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	e.activityListeners = append(slices.Clone(e.activityListeners), fn)
}

func (e *Engine) activity(env, jobID string, a protocol.ActivityPayload) {
	e.subsMu.Lock()
	listeners := e.activityListeners
	e.subsMu.Unlock()
	for _, fn := range listeners {
		fn(env, jobID, a)
	}
}

// OnChange registers fn, called with the IDs of jobs that were created,
// changed state or reported progress (the live stream's job source, #23).
// fn runs on the engine's goroutines: it must not block or call back into
// the engine synchronously.
func (e *Engine) OnChange(fn func(jobIDs []string)) {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	e.changeListeners = append(slices.Clone(e.changeListeners), fn)
}

var errIllegalTransition = errors.New("jobs: illegal state transition")

// event appends an event to the job's bounded log.
func (e *Engine) event(ctx context.Context, db bun.IDB, ev domain.JobEvent) error {
	if ev.At.IsZero() {
		ev.At = e.now()
	}
	if ev.Type != domain.JobEventProgress && ev.Percent == 0 {
		ev.Percent = -1
	}
	return store.AppendJobEvent(ctx, db, &ev, e.limits.MaxEventsPerJob)
}

// transition is the ONLY place a job changes state. It enforces the state
// machine, stamps timestamps, releases locks on terminal states, persists
// the job, records a state event and the job.started / job.finished audit
// records (#30).
func (e *Engine) transition(ctx context.Context, db bun.IDB, j *domain.Job, to domain.JobState, message string) error {
	if !domain.CanTransition(j.State, to) {
		return fmt.Errorf("%w: job %s %s -> %s", errIllegalTransition, j.ID, j.State, to)
	}
	now := e.now()
	j.State = to
	j.UpdatedAt = now
	switch to {
	case domain.JobDispatched:
		j.DispatchedAt = &now
	case domain.JobRunning:
		if j.StartedAt == nil {
			j.StartedAt = &now
		}
	}
	if to != domain.JobBlocked {
		j.BlockedBy, j.BlockedReason = "", ""
	}
	if to.Terminal() {
		j.FinishedAt = &now
		j.StepInFlight = false
		if err := store.DeleteJobLocks(ctx, db, j.ID); err != nil {
			return err
		}
	}
	if err := store.UpdateJob(ctx, db, j); err != nil {
		return err
	}
	if to.Terminal() {
		if err := e.runFinishHooks(ctx, db, j); err != nil {
			return err
		}
	}
	if err := e.auditTransition(ctx, db, j, to); err != nil {
		return err
	}
	return e.event(ctx, db, domain.JobEvent{JobID: j.ID, Type: domain.JobEventState, State: to, Message: message})
}

// FinishHook lets a feature react to a job of its kind reaching a terminal
// state, inside the transaction that finishes the job (j.State is the
// terminal state; j.ResultOutput the executor's result output, nil when the
// job ended without one). Use db for every write so the feature's state and
// the job's outcome commit together. A hook must tolerate malformed output
// (log it and record what it can): its error aborts the transaction, and an
// agent's result is then delivered again later.
type FinishHook func(ctx context.Context, db bun.IDB, j domain.Job) error

// OnFinish registers a finish hook for kind (call before Run).
func (e *Engine) OnFinish(kind domain.JobKind, h FinishHook) {
	e.hooksMu.Lock()
	defer e.hooksMu.Unlock()
	e.finishHooks[kind] = append(e.finishHooks[kind], h)
}

// ScheduledCheck revalidates a queued scheduled job right before it is
// dispatched (#13, #17: policy enabled state and target scope). A non-empty
// reason fails the job with class policy_rejected; an error leaves it
// waiting for the next pass (logged).
type ScheduledCheck func(ctx context.Context, j domain.Job) (reason string, err error)

// SetScheduledCheck installs the dispatch-time revalidation of scheduled
// jobs (the scheduler installs it; call before Run).
func (e *Engine) SetScheduledCheck(fn ScheduledCheck) {
	e.hooksMu.Lock()
	defer e.hooksMu.Unlock()
	e.scheduledCheck = fn
}

func (e *Engine) checkScheduled(ctx context.Context, j domain.Job) (string, error) {
	e.hooksMu.RLock()
	fn := e.scheduledCheck
	e.hooksMu.RUnlock()
	if fn == nil || j.Origin != domain.OriginScheduled {
		return "", nil
	}
	return fn(ctx, j)
}

func (e *Engine) runFinishHooks(ctx context.Context, db bun.IDB, j *domain.Job) error {
	e.hooksMu.RLock()
	hooks := e.finishHooks[j.Kind]
	e.hooksMu.RUnlock()
	for _, h := range hooks {
		if err := h(ctx, db, *j); err != nil {
			return fmt.Errorf("jobs: finish hook of %s: %w", j.Kind, err)
		}
	}
	return nil
}

// finish moves a job to a terminal state with its error class, message and
// recovery guidance. Non-successful terminal states always carry guidance.
func (e *Engine) finish(ctx context.Context, db bun.IDB, j *domain.Job, to domain.JobState, class, message, recovery string) error {
	j.ErrorClass, j.ErrorMessage, j.Recovery = class, message, recovery
	if to != domain.JobSucceeded && j.Recovery == "" {
		j.Recovery = defaultRecovery(to)
	}
	if to == domain.JobSucceeded {
		j.ErrorClass, j.ErrorMessage, j.Recovery = "", "", ""
	}
	msg := message
	if msg == "" {
		msg = string(to)
	}
	return e.transition(ctx, db, j, to, msg)
}

func defaultRecovery(s domain.JobState) string {
	switch s {
	case domain.JobCancelled:
		return "The job was cancelled at a safe point; compensating actions ran. Run it again if needed."
	case domain.JobPartial:
		return "Review the failed items and run the job again for them."
	case domain.JobInterrupted:
		return "The job was interrupted. Check the state of its targets, then run it again."
	}
	return "Fix the cause shown in the error and run the job again."
}

// tx runs fn in a transaction.
func (e *Engine) tx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return e.db.RunInTx(ctx, nil, fn)
}
