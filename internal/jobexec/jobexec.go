// Package jobexec runs the steps of one job attempt against a durable
// journal. It is shared by the agent (internal/agent/jobs, file journal) and
// manager-local jobs (internal/manager/jobs, database journal) so both follow
// the same rules (#26):
//
//   - Steps run in the order declared by the kind's jobspec.Spec; steps
//     completed by an earlier attempt are skipped (resume).
//   - Before a step starts it is journaled as in flight (durably); after it
//     returns it is journaled as completed. A crash in between leaves an
//     in-flight step whose outcome is unknown.
//   - Cancellation is honored only immediately before a step declared as a
//     safe point.
//   - Steps may register compensations (e.g. "start the containers I
//     stopped"), journaled before the step continues. Unreleased
//     compensations always run when the attempt does not succeed, including
//     during crash recovery (Recover).
//   - Recover turns a journal entry left behind by a crash into an
//     interrupted outcome: resumable only when the in-flight step (if any) is
//     idempotent and no compensation had to run; a non-idempotent step with
//     an unknown outcome is never retried and yields recovery guidance.
//
// Fault points (internal/faultinject) mark every stage boundary:
// <prefix>.step.before.<step>, .step.started.<step>, .step.ran.<step>,
// .step.after.<step> and <prefix>.compensation.before.<name>.
package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// StepFunc implements one step. It must honor ctx and should report
// progress and items through sc.
type StepFunc func(ctx context.Context, sc *StepContext) error

// CompensationFunc implements a compensating action. It must be idempotent:
// it may run again after a crash.
type CompensationFunc func(ctx context.Context, args json.RawMessage) error

// Executor implements a job kind: one StepFunc per declared step and one
// CompensationFunc per declared compensation.
type Executor struct {
	Kind          domain.JobKind
	Steps         map[string]StepFunc
	Compensations map[string]CompensationFunc
}

// Validate checks the executor against the kind's spec.
func (e Executor) Validate(executor domain.JobExecutor) error {
	spec, ok := jobspec.Lookup(e.Kind)
	if !ok {
		return fmt.Errorf("jobexec: unknown kind %q", e.Kind)
	}
	if spec.Executor != executor {
		return fmt.Errorf("jobexec: %s is executed by the %s, not the %s", e.Kind, spec.Executor, executor)
	}
	var errs []error
	for _, st := range spec.Steps {
		if e.Steps[st.Name] == nil {
			errs = append(errs, fmt.Errorf("jobexec: %s: step %q has no implementation", e.Kind, st.Name))
		}
	}
	for name := range e.Steps {
		if _, ok := spec.Step(name); !ok {
			errs = append(errs, fmt.Errorf("jobexec: %s: step %q is not declared in the spec", e.Kind, name))
		}
	}
	for _, c := range spec.Compensations {
		if e.Compensations[c.Name] == nil {
			errs = append(errs, fmt.Errorf("jobexec: %s: compensation %q has no implementation", e.Kind, c.Name))
		}
	}
	for name := range e.Compensations {
		if !spec.HasCompensation(name) {
			errs = append(errs, fmt.Errorf("jobexec: %s: compensation %q is not declared in the spec", e.Kind, name))
		}
	}
	return errors.Join(errs...)
}

// Compensation is a registered compensating action.
type Compensation struct {
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args,omitempty"`
	Released bool            `json:"released,omitempty"`
	Done     bool            `json:"done,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// State is the durable record of one attempt (a journal entry).
type State struct {
	JobID         string                  `json:"jobId"`
	Attempt       uint32                  `json:"attempt"`
	FencingToken  uint64                  `json:"fencingToken"`
	Kind          domain.JobKind          `json:"kind"`
	Input         json.RawMessage         `json:"input,omitempty"`
	CurrentStep   string                  `json:"currentStep,omitempty"`
	StepInFlight  bool                    `json:"stepInFlight,omitempty"`
	Completed     []string                `json:"completed,omitempty"`
	Compensations []Compensation          `json:"compensations,omitempty"`
	Items         []protocol.ItemPayload  `json:"items,omitempty"`
	Outcome       *protocol.ResultPayload `json:"outcome,omitempty"`
	// Secrets are the attempt's credentials (#19, #33). They are never
	// serialized and never cloned, so a journal (file or database) cannot
	// hold them; only the running attempt sees them.
	Secrets *protocol.CommandSecrets `json:"-"`
}

// Clone returns a deep copy without Secrets.
func (s *State) Clone() State {
	c := *s
	c.Secrets = nil
	c.Input = slices.Clone(s.Input)
	c.Completed = slices.Clone(s.Completed)
	c.Compensations = slices.Clone(s.Compensations)
	c.Items = slices.Clone(s.Items)
	if s.Outcome != nil {
		o := *s.Outcome
		c.Outcome = &o
	}
	return c
}

// Journal durably stores attempt state. Save must not return before the
// state is persisted (fsync / committed).
type Journal interface {
	Save(ctx context.Context, st *State) error
}

// Reporter receives best-effort progress (it may drop reports).
type Reporter interface {
	Progress(ctx context.Context, st *State, p protocol.ProgressPayload)
}

// Options configures Run and Recover.
type Options struct {
	Journal  Journal
	Reporter Reporter
	// CancelRequested is polled before safe-point steps.
	CancelRequested func() bool
	// FaultPrefix prefixes fault point names ("agent", "manager").
	FaultPrefix string
}

// Outcome states (ResultPayload.Outcome).
const (
	OutcomeSucceeded   = string(domain.JobSucceeded)
	OutcomeFailed      = string(domain.JobFailed)
	OutcomePartial     = string(domain.JobPartial)
	OutcomeCancelled   = string(domain.JobCancelled)
	OutcomeInterrupted = string(domain.JobInterrupted)
)

// ErrStepCancelled is returned (wrapped) by a step that stopped early
// because cancellation was requested (sc.CancelRequested) and whose
// interruption is safe, e.g. an aborted image build. The attempt then ends
// cancelled instead of failed; compensations run as for any cancellation.
var ErrStepCancelled = errors.New("jobexec: step cancelled on request")

// ErrAbandoned is returned by Run when ctx ended mid-attempt (process
// shutdown). The journal keeps the in-flight state; Recover handles it on
// the next start.
var ErrAbandoned = errors.New("jobexec: attempt abandoned (context ended)")

// StepContext is passed to steps.
type StepContext struct {
	Kind    domain.JobKind
	JobID   string
	Attempt uint32
	// Input is the job's JSON input object.
	Input json.RawMessage
	// Secrets are the credentials the manager sent with this attempt (nil
	// when none). Use them for this attempt only; never log, journal or
	// persist them.
	Secrets *protocol.CommandSecrets

	st   *State
	opts Options
}

// Progress reports progress (percent 0..100, -1 unknown).
func (sc *StepContext) Progress(ctx context.Context, percent int, message string) {
	if sc.opts.Reporter != nil {
		sc.opts.Reporter.Progress(ctx, sc.st, protocol.ProgressPayload{Step: sc.st.CurrentStep, Percent: percent, Message: message})
	}
}

// Item records a per-item result (journaled with the step).
func (sc *StepContext) Item(ctx context.Context, name, status, message string) {
	it := protocol.ItemPayload{Name: name, Status: status, Message: message}
	sc.st.Items = append(sc.st.Items, it)
	if sc.opts.Reporter != nil {
		sc.opts.Reporter.Progress(ctx, sc.st, protocol.ProgressPayload{Step: sc.st.CurrentStep, Percent: -1, Item: &it})
	}
}

// AddCompensation registers a compensating action and journals it before
// returning. Register BEFORE causing the effect it undoes (e.g. before
// stopping containers), so a crash in between still compensates.
func (sc *StepContext) AddCompensation(ctx context.Context, name string, args any) error {
	spec, _ := jobspec.Lookup(sc.Kind)
	if !spec.HasCompensation(name) {
		return fmt.Errorf("jobexec: %s does not declare compensation %q", sc.Kind, name)
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	sc.st.Compensations = append(sc.st.Compensations, Compensation{Name: name, Args: b})
	return sc.opts.Journal.Save(ctx, sc.st)
}

// ReleaseCompensation marks registered compensations with this name as no
// longer needed (the normal flow undid the effect) and journals it.
func (sc *StepContext) ReleaseCompensation(ctx context.Context, name string) error {
	for i := range sc.st.Compensations {
		if sc.st.Compensations[i].Name == name {
			sc.st.Compensations[i].Released = true
		}
	}
	return sc.opts.Journal.Save(ctx, sc.st)
}

// CancelRequested reports whether cancellation was requested. Steps may use
// it for information; cancellation only takes effect at safe points.
func (sc *StepContext) CancelRequested() bool {
	return sc.opts.CancelRequested != nil && sc.opts.CancelRequested()
}

func point(ctx context.Context, o Options, stage, name string) error {
	prefix := o.FaultPrefix
	if prefix == "" {
		prefix = "exec"
	}
	return faultinject.Point(ctx, prefix+"."+stage+"."+name)
}

// Run executes the attempt described by st (resuming after st.Completed)
// and returns its outcome, which is also journaled in st.Outcome. It returns
// ErrAbandoned when ctx ends mid-attempt; st then stays in flight.
func Run(ctx context.Context, exec Executor, st *State, o Options) (protocol.ResultPayload, error) {
	spec, ok := jobspec.Lookup(st.Kind)
	if !ok || exec.Kind != st.Kind {
		return finish(ctx, st, o, protocol.ResultPayload{Outcome: OutcomeFailed, ErrorClass: domain.ErrorRejected,
			Message: fmt.Sprintf("no executor for kind %q", st.Kind)})
	}
	sc := &StepContext{Kind: st.Kind, JobID: st.JobID, Attempt: st.Attempt, Input: st.Input, Secrets: st.Secrets, st: st, opts: o}
	var res *protocol.ResultPayload
	for _, step := range spec.Steps {
		if slices.Contains(st.Completed, step.Name) {
			continue
		}
		if step.SafePoint && o.CancelRequested != nil && o.CancelRequested() {
			res = &protocol.ResultPayload{Outcome: OutcomeCancelled, ErrorClass: domain.ErrorCancelled,
				Message: "cancelled before step " + step.Name}
			break
		}
		if err := point(ctx, o, "step.before", step.Name); err != nil {
			res = stepFailure(step, err)
			break
		}
		st.CurrentStep, st.StepInFlight = step.Name, true
		if err := o.Journal.Save(ctx, st); err != nil {
			st.StepInFlight = false
			res = &protocol.ResultPayload{Outcome: OutcomeFailed, ErrorClass: domain.ErrorInternal,
				Message:  "could not journal step " + step.Name + ": " + err.Error(),
				Recovery: "The step did not start. Check the executor's state directory and run the job again."}
			break
		}
		if err := point(ctx, o, "step.started", step.Name); err != nil {
			return abandonOr(ctx, st, o, step, err)
		}
		if o.Reporter != nil {
			o.Reporter.Progress(ctx, st, protocol.ProgressPayload{Step: step.Name, Percent: -1, Message: "step " + step.Name + " started"})
		}
		err := exec.Steps[step.Name](ctx, sc)
		if err == nil {
			err = point(ctx, o, "step.ran", step.Name)
		}
		if err != nil {
			if ctx.Err() != nil {
				// Shutdown mid-step: outcome unknown, leave it to Recover.
				return protocol.ResultPayload{}, ErrAbandoned
			}
			st.StepInFlight = false
			if errors.Is(err, ErrStepCancelled) {
				res = &protocol.ResultPayload{Outcome: OutcomeCancelled, ErrorClass: domain.ErrorCancelled,
					Message: "cancelled during step " + step.Name}
				break
			}
			res = stepFailure(step, err)
			break
		}
		st.Completed = append(st.Completed, step.Name)
		st.CurrentStep, st.StepInFlight = "", false
		if err := o.Journal.Save(ctx, st); err != nil {
			res = &protocol.ResultPayload{Outcome: OutcomeFailed, ErrorClass: domain.ErrorInternal,
				Message: "could not journal completion of step " + step.Name + ": " + err.Error(), Recovery: step.Recovery}
			break
		}
		if err := point(ctx, o, "step.after", step.Name); err != nil {
			res = stepFailure(step, err)
			break
		}
	}
	if res == nil {
		res = &protocol.ResultPayload{Outcome: OutcomeSucceeded}
		for _, it := range st.Items {
			if it.Status == domain.ItemFailed {
				res.Outcome = OutcomePartial
				res.ErrorClass = domain.ErrorStepFailed
				res.Message = "some items failed"
				res.Recovery = "Review the failed items and run the job again for them."
				break
			}
		}
	}
	if res.Outcome != OutcomeSucceeded && res.Outcome != OutcomePartial {
		compensate(ctx, exec.Compensations, st, o, res)
	}
	return finish(ctx, st, o, *res)
}

func abandonOr(ctx context.Context, st *State, o Options, step jobspec.Step, err error) (protocol.ResultPayload, error) {
	if ctx.Err() != nil {
		return protocol.ResultPayload{}, ErrAbandoned
	}
	// An injected error after the step was journaled as in flight: the
	// step did not run.
	st.StepInFlight = false
	res := stepFailure(step, err)
	return finish(ctx, st, o, *res)
}

// ClassedError is a step failure with its own stable error class instead
// of step_failed, e.g. an Engine or registry failure (rate_limited,
// unauthorized, not_found, #6) or a refusal (stack_managed, in_use). The
// class is part of the job's public error ({class, message, recovery}) and
// its audit record; Recovery, when not empty, replaces the step's guidance.
type ClassedError interface {
	error
	ErrorClass() string
	Recovery() string
}

func stepFailure(step jobspec.Step, err error) *protocol.ResultPayload {
	rec := step.Recovery
	if rec == "" {
		rec = "Step " + step.Name + " failed. Fix the cause and run the job again."
	}
	class := domain.ErrorStepFailed
	var ce ClassedError
	if errors.As(err, &ce) && ce.ErrorClass() != "" {
		class = ce.ErrorClass()
		if r := ce.Recovery(); r != "" {
			rec = r
		}
	}
	return &protocol.ResultPayload{Outcome: OutcomeFailed, ErrorClass: class,
		Message: "step " + step.Name + " failed: " + err.Error(), Recovery: rec}
}

// compensate runs every unreleased, not yet done compensation (latest
// first) and records failures in res.
func compensate(ctx context.Context, funcs map[string]CompensationFunc, st *State, o Options, res *protocol.ResultPayload) (ran bool) {
	var failed []string
	for i := len(st.Compensations) - 1; i >= 0; i-- {
		c := &st.Compensations[i]
		if c.Released || c.Done {
			if c.Done {
				res.Compensations = append(res.Compensations, protocol.CompensationPayload{Name: c.Name, Done: true})
			}
			continue
		}
		ran = true
		var err error
		if f := funcs[c.Name]; f == nil {
			err = errors.New("no implementation available")
		} else if err = point(ctx, o, "compensation.before", c.Name); err == nil {
			err = f(ctx, c.Args)
		}
		if err != nil {
			c.Error = err.Error()
			failed = append(failed, c.Name+": "+err.Error())
		} else {
			c.Done, c.Error = true, ""
		}
		res.Compensations = append(res.Compensations, protocol.CompensationPayload{Name: c.Name, Done: c.Done, Error: c.Error})
		_ = o.Journal.Save(ctx, st) // best effort; compensations are idempotent
	}
	if len(failed) > 0 {
		if res.ErrorClass == "" || res.ErrorClass == domain.ErrorCancelled {
			res.ErrorClass = domain.ErrorCompensationFailed
		}
		res.Recovery = strings.TrimSpace(res.Recovery + " Compensation failed (" + strings.Join(failed, "; ") +
			"): undo its effect manually, e.g. start the affected containers.")
	}
	return ran
}

func finish(ctx context.Context, st *State, o Options, res protocol.ResultPayload) (protocol.ResultPayload, error) {
	res.CompletedSteps = slices.Clone(st.Completed)
	res.Items = slices.Clone(st.Items)
	st.Outcome = &res
	st.CurrentStep, st.StepInFlight = "", false
	if err := o.Journal.Save(ctx, st); err != nil {
		return res, fmt.Errorf("jobexec: journal outcome: %w", err)
	}
	return res, nil
}

// Recover converts the journal entry of an attempt that was in progress when
// its executor died into an interrupted outcome, running unreleased
// compensations first. exec may be nil when the kind has no executor in this
// process (compensations are then reported as not run).
func Recover(ctx context.Context, exec *Executor, st *State, o Options) (protocol.ResultPayload, error) {
	if st.Outcome != nil {
		return *st.Outcome, nil
	}
	res := protocol.ResultPayload{Outcome: OutcomeInterrupted, Resumable: true}
	spec, _ := jobspec.Lookup(st.Kind)
	if st.StepInFlight {
		res.InterruptedStep = st.CurrentStep
		step, ok := spec.Step(st.CurrentStep)
		if !ok || !step.Idempotent {
			res.Resumable = false
			res.ErrorClass = domain.ErrorUnknownOutcome
			res.Message = "the executor stopped during step " + st.CurrentStep + "; its outcome is unknown and it is not retried automatically"
			res.Recovery = step.Recovery
		}
	}
	var funcs map[string]CompensationFunc
	if exec != nil {
		funcs = exec.Compensations
	}
	if compensate(ctx, funcs, st, o, &res) {
		// The world was rolled back; resuming mid-plan would be inconsistent.
		res.Resumable = false
		if res.ErrorClass == "" {
			res.ErrorClass = domain.ErrorExecutorRestarted
		}
		if res.Message == "" {
			res.Message = "the executor stopped mid-job; compensating actions were run"
		}
		if res.Recovery == "" {
			res.Recovery = "Compensating actions ran. Run the job again."
		}
	}
	if res.Resumable && res.Message == "" {
		res.Message = "the executor stopped mid-job; the job can resume"
	}
	return finish(ctx, st, o, res)
}
