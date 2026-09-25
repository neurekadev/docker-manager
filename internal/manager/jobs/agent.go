package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/faultinject"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// AgentDispatcher is the engine's view of the agent session transport
// (#3 implements it). Contract:
//
//   - Send delivers a frame to the environment's connected agent. Frames for
//     one environment must be delivered in call order (the engine sends
//     commands in fencing-token order). It returns ErrAgentOffline (or any
//     error) when the frame cannot be handed to a session; the engine then
//     relies on the reconnect report or the offline deadline.
//   - On every agent (re)connect the transport passes the agent's job_report
//     frame to HandleAgentFrame, sends the returned replies, and only then
//     reports Online(env) = true and calls Wake.
//   - Every inbound ack, progress and result frame goes to HandleAgentFrame
//     with the session's environment ID; replies go back on the session.
//   - On disconnect Online(env) becomes false; call AgentDisconnected.
type AgentDispatcher interface {
	Online(environmentID string) bool
	Send(ctx context.Context, environmentID string, f *protocol.Frame) error
}

// ErrAgentOffline means no agent session exists for the environment.
var ErrAgentOffline = errors.New("jobs: agent offline")

// NoAgents is the dispatcher used until the agent transport exists: every
// environment is offline.
type NoAgents struct{}

// Online implements AgentDispatcher.
func (NoAgents) Online(string) bool { return false }

// Send implements AgentDispatcher.
func (NoAgents) Send(context.Context, string, *protocol.Frame) error { return ErrAgentOffline }

// AgentDisconnected tells the engine an environment's session ended.
func (e *Engine) AgentDisconnected(string) { e.Wake() }

// HandleAgentFrame processes a job frame (ack, progress, result, job_report)
// received from environmentID's agent and returns the frames to send back on
// the same session. An agent can only affect jobs of its own environment.
func (e *Engine) HandleAgentFrame(ctx context.Context, environmentID string, f *protocol.Frame) ([]*protocol.Frame, error) {
	switch f.Type {
	case protocol.TypeAck:
		return nil, e.handleAck(ctx, environmentID, f)
	case protocol.TypeProgress:
		return nil, e.handleProgress(ctx, environmentID, f)
	case protocol.TypeResult:
		return e.handleResult(ctx, environmentID, f)
	case protocol.TypeJobReport:
		return e.handleReport(ctx, environmentID, f)
	}
	return nil, fmt.Errorf("jobs: unexpected %s frame from agent", f.Type)
}

// current reports whether f addresses the job's current attempt from the
// right environment.
func current(j *domain.Job, env string, ref protocol.JobRef) bool {
	return j.Executor == domain.ExecutorAgent && j.EnvironmentID == env &&
		uint32(j.Attempt) == ref.Attempt && j.FencingToken == ref.FencingToken //nolint:gosec // attempts are small
}

func (e *Engine) handleAck(ctx context.Context, env string, f *protocol.Frame) error {
	p, err := protocol.DecodePayload[protocol.AckPayload](f)
	if err != nil {
		return err
	}
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	var resend *domain.Job
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		j, err := store.GetJob(ctx, tx, f.JobID)
		if errors.Is(err, domain.ErrJobNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !current(&j, env, f.Ref()) || !j.State.Active() {
			return nil // stale acknowledgement
		}
		if err := faultinject.Point(ctx, PointAckBeforeCommit); err != nil {
			return err
		}
		switch {
		case p.Accepted:
			if j.State == domain.JobDispatched {
				return e.transition(ctx, tx, &j, domain.JobRunning, "acknowledged by the agent")
			}
			return nil
		case p.Code == protocol.AckStaleToken:
			// The agent has seen newer tokens (e.g. the manager database was
			// restored): move the counter past them and try again.
			if err := store.RaiseFencingFloor(ctx, tx, env, p.HighWater); err != nil {
				return err
			}
			if j.Resumes >= e.opts.MaxResumes {
				return e.finish(ctx, tx, &j, domain.JobFailed, domain.ErrorResumeLimit,
					"the agent rejected the command's fencing token too many times", "")
			}
			if err := e.redispatch(ctx, tx, &j, j.CompletedSteps, "the agent rejected a stale fencing token"); err != nil {
				return err
			}
			resend = &j
			return nil
		default:
			return e.finish(ctx, tx, &j, domain.JobFailed, domain.ErrorRejected,
				"the agent rejected the command: "+p.Code+": "+p.Message,
				"Nothing was changed. Check the agent version and the job input, then run the job again.")
		}
	})
	if err != nil {
		return err
	}
	e.afterAgentChange(ctx, []string{f.JobID}, resend)
	return nil
}

func (e *Engine) handleProgress(ctx context.Context, env string, f *protocol.Frame) error {
	p, err := protocol.DecodePayload[protocol.ProgressPayload](f)
	if err != nil {
		return err
	}
	ok, err := e.applyProgress(ctx, f.JobID, func(j *domain.Job) bool { return current(j, env, f.Ref()) }, p)
	if err == nil && ok {
		err = faultinject.Point(ctx, PointProgressCommitted)
	}
	return err
}

// applyProgress records progress for the job if match accepts it.
func (e *Engine) applyProgress(ctx context.Context, jobID string, match func(*domain.Job) bool, p protocol.ProgressPayload) (bool, error) {
	applied := false
	err := e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		j, err := store.GetJob(ctx, tx, jobID)
		if errors.Is(err, domain.ErrJobNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !match(&j) || !j.State.Active() {
			return nil
		}
		if j.State == domain.JobDispatched {
			if err := e.transition(ctx, tx, &j, domain.JobRunning, "running (progress received)"); err != nil {
				return err
			}
		}
		percent := p.Percent
		if percent < -1 || percent > 100 {
			percent = -1
		}
		ev := domain.JobEvent{JobID: j.ID, Type: domain.JobEventProgress, Percent: percent, Step: p.Step, Message: truncate(p.Message, 1024)}
		if p.Item != nil {
			it := item(*p.Item)
			j.Items = append(j.Items, it)
			ev = domain.JobEvent{JobID: j.ID, Type: domain.JobEventItem, Percent: -1, Step: p.Step, Item: &it, Message: it.Name + ": " + it.Status}
		} else {
			j.Progress = domain.JobProgress{Percent: percent, Step: p.Step, Message: ev.Message}
		}
		j.UpdatedAt = e.now()
		if err := store.UpdateJob(ctx, tx, &j); err != nil {
			return err
		}
		applied = true
		return e.event(ctx, tx, ev)
	})
	if applied {
		e.notify(jobID)
	}
	return applied, err
}

// Bounds for agent-supplied text (an agent is not trusted to keep job
// records small).
const (
	maxItemName    = 512
	maxMessage     = 2048
	maxRecovery    = 4096
	maxErrorClass  = 64
	maxResultItems = 10000
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// item normalizes an agent-reported item result.
func item(p protocol.ItemPayload) domain.JobItem {
	status := p.Status
	switch status {
	case domain.ItemSucceeded, domain.ItemFailed, domain.ItemSkipped:
	default:
		status = domain.ItemFailed
	}
	return domain.JobItem{Name: truncate(p.Name, maxItemName), Status: status, Message: truncate(p.Message, maxMessage)}
}

// sanitizeResult bounds agent-supplied result text.
func sanitizeResult(res protocol.ResultPayload) protocol.ResultPayload {
	res.ErrorClass = truncate(res.ErrorClass, maxErrorClass)
	res.Message = truncate(res.Message, maxMessage)
	res.Recovery = truncate(res.Recovery, maxRecovery)
	if len(res.Items) > maxResultItems {
		res.Items = res.Items[:maxResultItems]
	}
	if len(res.Output) > protocol.MaxResultOutput {
		res.Output = nil
	}
	return res
}

func (e *Engine) handleResult(ctx context.Context, env string, f *protocol.Frame) ([]*protocol.Frame, error) {
	res, err := protocol.DecodePayload[protocol.ResultPayload](f)
	if err != nil {
		return nil, err
	}
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	var resend *domain.Job
	forget := false
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		j, err := store.GetJob(ctx, tx, f.JobID)
		if errors.Is(err, domain.ErrJobNotFound) {
			forget = true
			return nil
		}
		if err != nil {
			return err
		}
		if j.Executor != domain.ExecutorAgent || j.EnvironmentID != env {
			e.opts.Logger.Warn("agent reported a result for a job of another environment", "job_id", j.ID, "environment_id", env)
			return nil
		}
		forget = true
		if j.State.Terminal() || !current(&j, env, f.Ref()) {
			return e.lateOutcome(ctx, tx, &j, f.Ref(), &res)
		}
		if err := faultinject.Point(ctx, PointResultBeforeCommit); err != nil {
			return err
		}
		again, err := e.applyResult(ctx, tx, &j, res)
		if again {
			resend = &j
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var replies []*protocol.Frame
	if forget {
		ack, err := protocol.NewFrame(protocol.TypeAck, ids.New(), f.ID, f.Ref(), protocol.AckPayload{Accepted: true, Forget: []string{f.JobID}})
		if err != nil {
			return nil, err
		}
		replies = append(replies, ack)
	}
	if err := faultinject.Point(ctx, PointResultCommitted); err != nil {
		return nil, err
	}
	e.afterAgentChange(ctx, []string{f.JobID}, resend)
	return replies, nil
}

// lateOutcome records an outcome reported for a finished job or a
// superseded attempt without changing the job.
func (e *Engine) lateOutcome(ctx context.Context, tx bun.IDB, j *domain.Job, ref protocol.JobRef, res *protocol.ResultPayload) error {
	if j.State.Terminal() && res.Outcome == string(j.State) && ref.Attempt == uint32(j.Attempt) { //nolint:gosec // attempts are small
		return nil // duplicate delivery
	}
	return e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventWarning, State: j.State,
		Message: fmt.Sprintf("the agent reported outcome %q for attempt %d after the job moved on (state %s, attempt %d)",
			res.Outcome, ref.Attempt, j.State, j.Attempt)})
}

// applyResult applies an attempt's outcome. It returns true when the job was
// re-dispatched (resume) and its command must be sent.
func (e *Engine) applyResult(ctx context.Context, tx bun.IDB, j *domain.Job, res protocol.ResultPayload) (bool, error) {
	res = sanitizeResult(res)
	j.ResultOutput = res.Output
	if len(res.Items) > 0 {
		j.Items = j.Items[:0]
		for _, it := range res.Items {
			j.Items = append(j.Items, item(it))
		}
	}
	if res.CompletedSteps != nil {
		j.CompletedSteps = slices.Clone(res.CompletedSteps)
	}
	for _, c := range res.Compensations {
		if c.Error != "" {
			_ = e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventWarning, Message: "compensation " + c.Name + " failed: " + c.Error})
		} else if c.Done {
			_ = e.event(ctx, tx, domain.JobEvent{JobID: j.ID, Type: domain.JobEventLog, Message: "compensation " + c.Name + " ran"})
		}
	}
	switch res.Outcome {
	case string(domain.JobSucceeded), string(domain.JobFailed), string(domain.JobPartial), string(domain.JobCancelled):
		class := res.ErrorClass
		if res.Outcome == string(domain.JobCancelled) && class == "" {
			class = domain.ErrorCancelled
		}
		if res.Outcome == string(domain.JobFailed) && class == "" {
			class = domain.ErrorStepFailed
		}
		return false, e.finish(ctx, tx, j, domain.JobState(res.Outcome), class, res.Message, res.Recovery)
	case string(domain.JobInterrupted):
		if res.Resumable {
			switch {
			case j.CancelRequested:
				return false, e.finish(ctx, tx, j, domain.JobCancelled, domain.ErrorCancelled,
					"cancelled: the interrupted job was not resumed", "The job stopped between steps and was not resumed. Run it again if needed.")
			case j.Resumes >= e.opts.MaxResumes:
				return false, e.finish(ctx, tx, j, domain.JobInterrupted, domain.ErrorResumeLimit,
					fmt.Sprintf("interrupted %d times; not resumed again", j.Resumes+1), "")
			case j.Executor == domain.ExecutorAgent:
				return true, e.redispatch(ctx, tx, j, res.CompletedSteps, "resuming after the agent restarted: "+res.Message)
			}
		}
		class := res.ErrorClass
		if class == "" {
			class = domain.ErrorExecutorRestarted
		}
		msg := res.Message
		if res.InterruptedStep != "" && msg == "" {
			msg = "interrupted during step " + res.InterruptedStep
		}
		return false, e.finish(ctx, tx, j, domain.JobInterrupted, class, msg, res.Recovery)
	}
	return false, e.finish(ctx, tx, j, domain.JobInterrupted, domain.ErrorInternal, "unknown outcome "+res.Outcome, "")
}

// afterAgentChange notifies subscribers, re-sends a re-dispatched command
// (caller holds dispatchMu) and wakes the dispatcher.
func (e *Engine) afterAgentChange(ctx context.Context, jobIDs []string, resend *domain.Job) {
	e.notify(jobIDs...)
	if resend != nil {
		if spec, ok := jobspec.Lookup(resend.Kind); ok {
			e.sendCommand(ctx, resend, spec)
		}
	}
	e.Wake()
}

// handleReport reconciles the agent's journal after a (re)connect. Outcomes
// the agent recorded are applied (never re-run); commands the agent never
// received are re-dispatched with a new fencing token; acknowledged jobs the
// agent has no record of become interrupted.
func (e *Engine) handleReport(ctx context.Context, env string, f *protocol.Frame) ([]*protocol.Frame, error) {
	rep, err := protocol.DecodePayload[protocol.JobReportPayload](f)
	if err != nil {
		return nil, err
	}
	e.dispatchMu.Lock()
	defer e.dispatchMu.Unlock()
	var forget []string
	var resend, cancels []domain.Job
	var touched []string
	err = e.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		forget, resend, cancels, touched = nil, nil, nil, nil
		if err := store.RaiseFencingFloor(ctx, tx, env, rep.HighWater); err != nil {
			return err
		}
		reported := map[string]bool{}
		for _, entry := range rep.Jobs {
			j, err := store.GetJob(ctx, tx, entry.JobID)
			if errors.Is(err, domain.ErrJobNotFound) {
				if entry.Status == protocol.ReportFinished {
					forget = append(forget, entry.JobID)
				}
				continue
			}
			if err != nil {
				return err
			}
			if j.Executor != domain.ExecutorAgent || j.EnvironmentID != env {
				continue
			}
			ref := protocol.JobRef{JobID: entry.JobID, Attempt: entry.Attempt, FencingToken: entry.FencingToken}
			if j.State.Terminal() || !current(&j, env, ref) {
				if entry.Status == protocol.ReportFinished {
					forget = append(forget, entry.JobID)
					if entry.Result != nil {
						if err := e.lateOutcome(ctx, tx, &j, ref, entry.Result); err != nil {
							return err
						}
					}
				}
				continue
			}
			reported[j.ID] = true
			touched = append(touched, j.ID)
			if entry.Status == protocol.ReportRunning || entry.Result == nil {
				if j.State == domain.JobDispatched {
					if err := e.transition(ctx, tx, &j, domain.JobRunning, "running on the agent (reported after reconnect)"); err != nil {
						return err
					}
				}
				if j.CancelRequested {
					cancels = append(cancels, j)
				}
				continue
			}
			again, err := e.applyResult(ctx, tx, &j, *entry.Result)
			if err != nil {
				return err
			}
			forget = append(forget, j.ID)
			if again {
				resend = append(resend, j)
			}
		}
		active, err := store.JobsInStates(ctx, tx, domain.JobDispatched, domain.JobRunning, domain.JobCancelling)
		if err != nil {
			return err
		}
		for i := range active {
			a := &active[i]
			if a.Executor != domain.ExecutorAgent || a.EnvironmentID != env || reported[a.ID] {
				continue
			}
			touched = append(touched, a.ID)
			switch {
			case a.StartedAt == nil && a.CancelRequested:
				// Never acknowledged (the agent journals before acking) and
				// cancelled meanwhile: nothing ran.
				if err := e.finish(ctx, tx, a, domain.JobCancelled, domain.ErrorCancelled,
					"cancelled before the agent received the command", "Nothing was changed."); err != nil {
					return err
				}
			case a.StartedAt == nil:
				if a.Resumes >= e.opts.MaxResumes {
					if err := e.finish(ctx, tx, a, domain.JobFailed, domain.ErrorResumeLimit,
						"the command could not be delivered to the agent", "Nothing was changed. Check the agent connection and run the job again."); err != nil {
						return err
					}
					continue
				}
				if err := e.redispatch(ctx, tx, a, a.CompletedSteps, "the agent never received the command"); err != nil {
					return err
				}
				resend = append(resend, *a)
			default:
				if err := e.finish(ctx, tx, a, domain.JobInterrupted, domain.ErrorJournalLost,
					"the agent acknowledged this job but has no record of it (its state directory may have been reset)",
					"The job's outcome is unknown and it is not retried automatically. Check the state of its targets on the environment, then run it again if needed."); err != nil {
					return err
				}
			}
		}
		return faultinject.Point(ctx, PointReconcileBeforeCommit)
	})
	if err != nil {
		return nil, err
	}
	if err := faultinject.Point(ctx, PointReconcileCommitted); err != nil {
		return nil, err
	}
	ack, err := protocol.NewFrame(protocol.TypeAck, ids.New(), f.ID, protocol.JobRef{}, protocol.AckPayload{Accepted: true, Forget: forget})
	if err != nil {
		return nil, err
	}
	e.notify(touched...)
	for i := range resend {
		if spec, ok := jobspec.Lookup(resend[i].Kind); ok {
			e.sendCommand(ctx, &resend[i], spec)
		}
	}
	for i := range cancels {
		e.sendCancel(ctx, &cancels[i])
	}
	e.Wake()
	return []*protocol.Frame{ack}, nil
}
