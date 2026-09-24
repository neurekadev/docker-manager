// Package jobs is the agent side of the job engine (#26): it accepts job
// commands from the manager, enforces fencing, journals every attempt in the
// agent state directory (fsync'd) and executes the kind's steps with
// internal/jobexec.
//
// Wiring (done by the session transport, #3):
//
//	r, err := jobs.New(ctx, jobs.Options{StateDir: cfg.StateDir, Sender: session, Executors: executors, ...})
//	// on every (re)connect, before anything else:
//	r.SendReport(ctx)
//	// for every inbound command, cancel and ack frame:
//	r.HandleFrame(ctx, frame)
//
// Implementing a kind: build a jobexec.Executor with one StepFunc per step
// declared in internal/jobspec (and one CompensationFunc per declared
// compensation) and pass it in Options.Executors.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Fault points of the agent command path (step points come from jobexec
// with prefix "agent").
const (
	PointCommandReceived  = "agent.command.received"
	PointCommandJournaled = "agent.command.journaled"
	PointCommandAcked     = "agent.command.acked"
	PointResultBeforeSend = "agent.result.before_send"
	PointResultSent       = "agent.result.sent"
)

// Sender delivers frames to the manager over the current session.
type Sender interface {
	Send(ctx context.Context, f *protocol.Frame) error
}

// Options configures a Runner.
type Options struct {
	// StateDir is the agent state directory; the journal lives in
	// <StateDir>/jobs/journal.json.
	StateDir  string
	Clock     clock.Clock
	Logger    *slog.Logger
	Sender    Sender
	Executors []jobexec.Executor
}

// Runner executes job commands on the agent.
type Runner struct {
	opts    Options
	ctx     context.Context
	journal *Journal
	execs   map[domain.JobKind]jobexec.Executor

	mu      sync.Mutex
	running map[string]*attempt
	wg      sync.WaitGroup

	// reportSeq serializes a job_report (snapshot and send) with the
	// "attempt finished, send result" transition of execute. Without it a
	// report could snapshot an attempt as running, the attempt could finish
	// and send its result before the report goes out (on a session the
	// manager is not reading yet, so it is lost), and the manager would then
	// wait for a result that never comes. With it an attempt is either
	// reported finished (the report carries the outcome) or its result is
	// sent after the report. A one-slot channel rather than a mutex so tests
	// can observe the wait with testing/synctest.
	reportSeq chan struct{}
}

type attempt struct {
	attempt uint32
	cancel  atomic.Bool
}

// New opens the journal, recovers attempts left in flight by a previous
// process (running their compensations and recording interrupted outcomes)
// and returns a Runner. ctx bounds the lifetime of job goroutines.
func New(ctx context.Context, opts Options) (*Runner, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Sender == nil {
		return nil, errors.New("agent jobs: sender is required")
	}
	r := &Runner{opts: opts, ctx: ctx, execs: map[domain.JobKind]jobexec.Executor{}, running: map[string]*attempt{},
		reportSeq: make(chan struct{}, 1)}
	for _, e := range opts.Executors {
		if err := e.Validate(domain.ExecutorAgent); err != nil {
			return nil, err
		}
		if _, dup := r.execs[e.Kind]; dup {
			return nil, fmt.Errorf("agent jobs: duplicate executor for %s", e.Kind)
		}
		r.execs[e.Kind] = e
	}
	j, err := OpenJournal(opts.StateDir)
	if err != nil {
		return nil, err
	}
	r.journal = j
	for _, st := range j.Entries() {
		if st.Outcome != nil {
			continue
		}
		var exec *jobexec.Executor
		if e, ok := r.execs[st.Kind]; ok {
			exec = &e
		}
		res, err := jobexec.Recover(ctx, exec, &st, jobexec.Options{Journal: j, FaultPrefix: "agent"})
		if err != nil {
			return nil, err
		}
		opts.Logger.Warn("recovered interrupted job attempt", "job_id", st.JobID, "attempt", st.Attempt,
			"kind", st.Kind, "interrupted_step", res.InterruptedStep, "resumable", res.Resumable)
	}
	return r, nil
}

// Journal exposes the journal (tests, diagnostics).
func (r *Runner) Journal() *Journal { return r.journal }

// Wait blocks until all job goroutines have returned.
func (r *Runner) Wait() { r.wg.Wait() }

// Report builds the job_report payload: the fencing high-water mark and every
// journaled attempt.
func (r *Runner) Report() protocol.JobReportPayload {
	p := protocol.JobReportPayload{HighWater: r.journal.HighWater(), Jobs: []protocol.JobReportEntry{}}
	for _, st := range r.journal.Entries() {
		e := protocol.JobReportEntry{JobID: st.JobID, Attempt: st.Attempt, FencingToken: st.FencingToken,
			Kind: string(st.Kind), CurrentStep: st.CurrentStep, CompletedSteps: st.Completed}
		r.mu.Lock()
		a, running := r.running[st.JobID]
		running = running && a.attempt == st.Attempt
		r.mu.Unlock()
		if running || st.Outcome == nil {
			e.Status = protocol.ReportRunning
		} else {
			e.Status = protocol.ReportFinished
			e.Result = st.Outcome
		}
		p.Jobs = append(p.Jobs, e)
	}
	return p
}

// SendReport sends the job_report frame. Call it on every (re)connect.
// Every attempt it reports as running sends its result after the report.
func (r *Runner) SendReport(ctx context.Context) error {
	r.reportSeq <- struct{}{}
	defer func() { <-r.reportSeq }()
	f, err := protocol.NewFrame(protocol.TypeJobReport, ids.New(), "", protocol.JobRef{}, r.Report())
	if err != nil {
		return err
	}
	return r.opts.Sender.Send(ctx, f)
}

// HandleFrame processes a command, cancel or ack frame from the manager.
func (r *Runner) HandleFrame(ctx context.Context, f *protocol.Frame) error {
	switch f.Type {
	case protocol.TypeCommand:
		return r.handleCommand(ctx, f)
	case protocol.TypeCancel:
		r.mu.Lock()
		if a, ok := r.running[f.JobID]; ok && a.attempt == f.Attempt {
			a.cancel.Store(true)
		}
		r.mu.Unlock()
		return nil
	case protocol.TypeAck:
		p, err := protocol.DecodePayload[protocol.AckPayload](f)
		if err != nil {
			return err
		}
		return r.journal.Forget(p.Forget...)
	}
	return fmt.Errorf("agent jobs: unexpected %s frame", f.Type)
}

func (r *Runner) ack(ctx context.Context, f *protocol.Frame, p protocol.AckPayload) error {
	a, err := protocol.NewFrame(protocol.TypeAck, ids.New(), f.ID, f.Ref(), p)
	if err != nil {
		return err
	}
	return r.opts.Sender.Send(ctx, a)
}

func (r *Runner) handleCommand(ctx context.Context, f *protocol.Frame) error {
	log := r.opts.Logger.With("job_id", f.JobID, "attempt", f.Attempt, "fencing_token", f.FencingToken)
	if err := faultinject.Point(ctx, PointCommandReceived); err != nil {
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckJournalFailed, Message: err.Error()})
	}
	p, err := protocol.DecodePayload[protocol.CommandPayload](f)
	if err != nil {
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckInvalid, Message: err.Error()})
	}

	r.mu.Lock()
	hw := r.journal.HighWater()
	existing, has := r.journal.Get(f.JobID)
	same := has && existing.Attempt == f.Attempt && existing.FencingToken == f.FencingToken
	switch {
	case f.FencingToken == hw && same, f.FencingToken < hw && same:
		// Exact duplicate of a command already journaled: never run again.
		_, running := r.running[f.JobID]
		r.mu.Unlock()
		log.Info("duplicate job command acknowledged without re-running")
		if err := r.ack(ctx, f, protocol.AckPayload{Accepted: true, Code: protocol.AckDuplicate}); err != nil {
			return err
		}
		if !running && existing.Outcome != nil {
			return r.sendResult(ctx, &existing)
		}
		return nil
	case f.FencingToken <= hw:
		r.mu.Unlock()
		log.Warn("rejected job command with stale fencing token", "high_water", hw)
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckStaleToken, HighWater: hw,
			Message: "fencing token " + strconv.FormatUint(f.FencingToken, 10) + " is not above " + strconv.FormatUint(hw, 10)})
	}
	if a, busy := r.running[f.JobID]; busy {
		r.mu.Unlock()
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckBusy,
			Message: "attempt " + strconv.FormatUint(uint64(a.attempt), 10) + " is still running"})
	}
	exec, ok := r.execs[domain.JobKind(p.Kind)]
	if !ok {
		r.mu.Unlock()
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckUnsupportedKind, Message: "this agent cannot run " + p.Kind})
	}
	if f.Deadline != nil && r.opts.Clock.Now().After(*f.Deadline) {
		r.mu.Unlock()
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckDeadlineExceeded, Message: "command deadline passed before it arrived"})
	}
	st := jobexec.State{JobID: f.JobID, Attempt: f.Attempt, FencingToken: f.FencingToken, Kind: exec.Kind,
		Input: p.Input, Completed: p.CompletedSteps}
	if err := r.journal.Accept(&st); err != nil {
		r.mu.Unlock()
		log.Error("could not journal job command", "error", err)
		return r.ack(ctx, f, protocol.AckPayload{Code: protocol.AckJournalFailed, Message: "could not persist the command"})
	}
	a := &attempt{attempt: f.Attempt}
	r.running[f.JobID] = a
	r.mu.Unlock()

	skipAck := faultinject.Point(ctx, PointCommandJournaled) != nil // simulate a lost ack
	if !skipAck {
		if err := r.ack(ctx, f, protocol.AckPayload{Accepted: true}); err != nil {
			log.Warn("could not send job ack; the job_report on reconnect carries its state", "error", err)
		}
		_ = faultinject.Point(ctx, PointCommandAcked)
	}
	r.wg.Add(1)
	go r.execute(exec, st, a)
	return nil
}

type reporter struct {
	r   *Runner
	ref protocol.JobRef
}

func (rp reporter) Progress(ctx context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	f, err := protocol.NewFrame(protocol.TypeProgress, ids.New(), protocol.CommandFrameID(rp.ref.JobID, rp.ref.Attempt), rp.ref, p)
	if err != nil {
		return
	}
	_ = rp.r.opts.Sender.Send(ctx, f) // best effort
}

func (r *Runner) execute(exec jobexec.Executor, st jobexec.State, a *attempt) {
	defer r.wg.Done()
	ref := protocol.JobRef{JobID: st.JobID, Attempt: st.Attempt, FencingToken: st.FencingToken}
	_, err := jobexec.Run(r.ctx, exec, &st, jobexec.Options{Journal: r.journal, Reporter: reporter{r, ref},
		CancelRequested: a.cancel.Load, FaultPrefix: "agent"})
	// Leaving the running set and sending the result happen as one step
	// relative to SendReport (see reportSeq).
	r.reportSeq <- struct{}{}
	defer func() { <-r.reportSeq }()
	r.mu.Lock()
	if cur, ok := r.running[st.JobID]; ok && cur == a {
		delete(r.running, st.JobID)
	}
	r.mu.Unlock()
	if errors.Is(err, jobexec.ErrAbandoned) {
		return // recovered from the journal on the next start
	}
	if err != nil {
		r.opts.Logger.Error("could not journal job outcome", "job_id", st.JobID, "error", err)
	}
	if faultinject.Point(r.ctx, PointResultBeforeSend) != nil {
		return // simulate a lost result; the job_report carries it
	}
	if err := r.sendResult(r.ctx, &st); err != nil {
		r.opts.Logger.Warn("could not send job result; the job_report on reconnect carries it", "job_id", st.JobID, "error", err)
	}
	_ = faultinject.Point(r.ctx, PointResultSent)
}

func (r *Runner) sendResult(ctx context.Context, st *jobexec.State) error {
	if st.Outcome == nil {
		return nil
	}
	ref := protocol.JobRef{JobID: st.JobID, Attempt: st.Attempt, FencingToken: st.FencingToken}
	f, err := protocol.NewFrame(protocol.TypeResult, "res."+st.JobID+"."+strconv.FormatUint(uint64(st.Attempt), 10),
		protocol.CommandFrameID(st.JobID, st.Attempt), ref, st.Outcome)
	if err != nil {
		return err
	}
	return r.opts.Sender.Send(ctx, f)
}
