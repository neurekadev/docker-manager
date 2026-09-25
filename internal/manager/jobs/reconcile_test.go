package jobs_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// TestStaleCommandReplayedAfterReconnectIsRejected: a command lost with a
// dropped connection is re-dispatched on reconnect under a new fencing
// token; when the old frame is replayed later (also after an agent restart)
// the agent rejects it by its token and nothing runs twice.
func TestStaleCommandReplayedAfterReconnectIsRejected(t *testing.T) {
	h := newHarness(t)
	fx := &jobstest.Effects{}
	l := newLoop(h, "e1", sim(jobspec.StackDeploy, fx))
	l.connect()
	j := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	dropped := h.disp.Disconnect("e1") // the command was in flight on the lost connection
	h.eng.AgentDisconnected("e1")
	if len(dropped) != 1 || dropped[0].FencingToken != 1 {
		t.Fatalf("dropped %+v", dropped)
	}

	l.connect() // job_report: the agent never saw the job -> re-dispatch
	rj := h.wantState(j.ID, domain.JobDispatched)
	if rj.Attempt != 2 || rj.FencingToken != 2 {
		t.Fatalf("re-dispatch attempt %d token %d", rj.Attempt, rj.FencingToken)
	}
	l.settle()
	h.wantState(j.ID, domain.JobSucceeded)

	// The stale frame arrives late: rejected by its fencing token.
	if err := l.runner.HandleFrame(h.ctx, dropped[0]); err != nil {
		t.Fatal(err)
	}
	staleAck := l.takeOutbox()
	if len(staleAck) != 1 {
		t.Fatalf("frames %+v", staleAck)
	}
	p, _ := protocol.DecodePayload[protocol.AckPayload](staleAck[0])
	if p.Accepted || p.Code != protocol.AckStaleToken || p.HighWater != 2 {
		t.Fatalf("stale ack %+v", p)
	}
	h.agentFrame("e1", staleAck[0]) // the engine ignores it (superseded attempt)
	h.wantState(j.ID, domain.JobSucceeded)

	// Still rejected after an agent restart (persisted high-water mark).
	l.kill()
	l.start()
	if err := l.runner.HandleFrame(h.ctx, dropped[0]); err != nil {
		t.Fatal(err)
	}
	p, _ = protocol.DecodePayload[protocol.AckPayload](l.takeOutbox()[0])
	if p.Code != protocol.AckStaleToken {
		t.Fatalf("after restart: %+v", p)
	}
	want := []string{j.ID + ":resolve_sources", j.ID + ":pull_images", j.ID + ":build_images", j.ID + ":apply"}
	if got := fx.List(); !slices.Equal(got, want) {
		t.Fatalf("effects %v, want each step exactly once %v", got, want)
	}
	h.noLocksHeld()
}

// TestReconcileEachReportedOutcome feeds every kind of job_report entry to
// the engine and checks that it reconciles instead of re-running.
func TestReconcileEachReportedOutcome(t *testing.T) {
	type tc struct {
		acked      bool
		cancel     bool
		entry      func(id string, cmd *protocol.Frame) *protocol.JobReportEntry // nil: not reported
		wantState  domain.JobState
		wantClass  string
		wantForget bool
		wantResend bool
		check      func(t *testing.T, j domain.Job, frames []*protocol.Frame)
	}
	finished := func(res protocol.ResultPayload) func(string, *protocol.Frame) *protocol.JobReportEntry {
		return func(id string, cmd *protocol.Frame) *protocol.JobReportEntry {
			return &protocol.JobReportEntry{JobID: id, Attempt: cmd.Attempt, FencingToken: cmd.FencingToken, Kind: string(jobspec.BackupRun),
				Status: protocol.ReportFinished, Result: &res}
		}
	}
	running := func(id string, cmd *protocol.Frame) *protocol.JobReportEntry {
		return &protocol.JobReportEntry{JobID: id, Attempt: cmd.Attempt, FencingToken: cmd.FencingToken, Kind: string(jobspec.BackupRun),
			Status: protocol.ReportRunning, CurrentStep: "snapshot"}
	}
	cases := map[string]tc{
		"finished succeeded": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "succeeded"}),
			wantState: domain.JobSucceeded, wantForget: true},
		"finished failed": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "failed", ErrorClass: domain.ErrorStepFailed, Recovery: "fix it"}),
			wantState: domain.JobFailed, wantClass: domain.ErrorStepFailed, wantForget: true,
			check: func(t *testing.T, j domain.Job, _ []*protocol.Frame) {
				if j.Recovery != "fix it" {
					t.Fatalf("recovery %q", j.Recovery)
				}
			}},
		"finished partial": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "partial", Items: []protocol.ItemPayload{{Name: "v1", Status: "failed"}}}),
			wantState: domain.JobPartial, wantForget: true,
			check: func(t *testing.T, j domain.Job, _ []*protocol.Frame) {
				if len(j.Items) != 1 || j.Recovery == "" {
					t.Fatalf("job %+v", j)
				}
			}},
		"finished cancelled": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "cancelled",
			Compensations: []protocol.CompensationPayload{{Name: "start_containers", Done: true}}}),
			wantState: domain.JobCancelled, wantClass: domain.ErrorCancelled, wantForget: true},
		"interrupted with unknown outcome": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "interrupted",
			ErrorClass: domain.ErrorUnknownOutcome, InterruptedStep: "snapshot", Recovery: "check the snapshot list"}),
			wantState: domain.JobInterrupted, wantClass: domain.ErrorUnknownOutcome, wantForget: true,
			check: func(t *testing.T, j domain.Job, frames []*protocol.Frame) {
				if j.Recovery != "check the snapshot list" || len(frames) != 0 {
					t.Fatalf("job %+v frames %d", j, len(frames))
				}
			}},
		"interrupted resumable": {acked: true, entry: finished(protocol.ResultPayload{Outcome: "interrupted", Resumable: true,
			CompletedSteps: []string{"prepare", "stop_containers"}, Output: json.RawMessage(`{"members":[{"item":"stack/web"}]}`)}),
			wantState: domain.JobDispatched, wantForget: true, wantResend: true,
			check: func(t *testing.T, j domain.Job, frames []*protocol.Frame) {
				p, _ := protocol.DecodePayload[protocol.CommandPayload](frames[0])
				if j.Attempt != 2 || !slices.Equal(p.CompletedSteps, []string{"prepare", "stop_containers"}) || frames[0].Attempt != 2 {
					t.Fatalf("job attempt %d payload %+v", j.Attempt, p)
				}
				// The resumed attempt continues from what the completed
				// steps recorded (found by the #26 real-executor harness:
				// a resumed prune.run lost its collected candidates).
				if string(p.Output) != `{"members":[{"item":"stack/web"}]}` {
					t.Fatalf("resumed command output %s", p.Output)
				}
				// Persisted with the job: a command re-sent after a
				// manager restart carries it too.
				if string(j.ResumeOutput) != string(p.Output) {
					t.Fatalf("stored resume output %s", j.ResumeOutput)
				}
			}},
		"interrupted resumable but cancelled": {acked: true, cancel: true, entry: finished(protocol.ResultPayload{Outcome: "interrupted", Resumable: true}),
			wantState: domain.JobCancelled, wantClass: domain.ErrorCancelled, wantForget: true},
		"still running":         {acked: true, entry: running, wantState: domain.JobRunning},
		"running, ack was lost": {entry: running, wantState: domain.JobRunning},
		"running with cancel pending": {acked: true, cancel: true, entry: running, wantState: domain.JobCancelling,
			check: func(t *testing.T, _ domain.Job, frames []*protocol.Frame) {
				if len(frames) != 1 || frames[0].Type != protocol.TypeCancel {
					t.Fatalf("cancel not re-sent: %+v", frames)
				}
			}},
		"never received": {entry: nil, wantState: domain.JobDispatched, wantResend: true,
			check: func(t *testing.T, j domain.Job, frames []*protocol.Frame) {
				if j.Attempt != 2 || frames[0].FencingToken != 2 {
					t.Fatalf("attempt %d token %d", j.Attempt, frames[0].FencingToken)
				}
			}},
		"never received and cancelled": {cancel: true, entry: nil, wantState: domain.JobCancelled, wantClass: domain.ErrorCancelled},
		"acknowledged but journal lost": {acked: true, entry: nil, wantState: domain.JobInterrupted, wantClass: domain.ErrorJournalLost,
			check: func(t *testing.T, j domain.Job, frames []*protocol.Frame) {
				if j.Recovery == "" || len(frames) != 0 {
					t.Fatalf("job %+v frames %d", j, len(frames))
				}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.disp.Connect("e1")
			j := h.enqueue(jobs.Request{Kind: jobspec.BackupRun, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web"), repo("r")}})
			h.dispatch()
			cmd := h.commands("e1")[0]
			if c.acked {
				h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
			}
			if c.cancel {
				if _, err := h.eng.Cancel(h.ctx, j.ID); err != nil {
					t.Fatal(err)
				}
			}
			h.disp.Disconnect("e1")
			h.disp.Attach("e1")
			rep := protocol.JobReportPayload{HighWater: cmd.FencingToken}
			if c.entry != nil {
				rep.Jobs = append(rep.Jobs, *c.entry(j.ID, cmd))
			}
			ack := h.report("e1", rep)
			got := h.wantState(j.ID, c.wantState)
			if got.ErrorClass != c.wantClass {
				t.Fatalf("class %q, want %q", got.ErrorClass, c.wantClass)
			}
			if slices.Contains(ack.Forget, j.ID) != c.wantForget {
				t.Fatalf("forget %v", ack.Forget)
			}
			frames := h.disp.Drain("e1")
			var cmds int
			for _, f := range frames {
				if f.Type == protocol.TypeCommand {
					cmds++
				}
			}
			if (cmds == 1) != c.wantResend || cmds > 1 {
				t.Fatalf("commands re-sent: %d", cmds)
			}
			if got.State.Terminal() {
				h.noLocksHeld()
				if got.State != domain.JobSucceeded && got.Recovery == "" {
					t.Fatal("terminal state without recovery guidance")
				}
			} else if len(h.heldLocks()) == 0 {
				t.Fatal("active job lost its locks")
			}
			if c.check != nil {
				c.check(t, got, frames)
			}
		})
	}
}

func TestReportForUnknownJobsAndFencingFloor(t *testing.T) {
	h := newHarness(t)
	h.disp.Attach("e1")
	ack := h.report("e1", protocol.JobReportPayload{HighWater: 50, Jobs: []protocol.JobReportEntry{
		{JobID: "gone", Attempt: 1, FencingToken: 3, Status: protocol.ReportFinished, Result: &protocol.ResultPayload{Outcome: "succeeded"}},
		{JobID: "gone-running", Attempt: 1, FencingToken: 4, Status: protocol.ReportRunning},
	}})
	if !slices.Equal(ack.Forget, []string{"gone"}) {
		t.Fatalf("forget %v", ack.Forget)
	}
	h.disp.Connect("e1")
	h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	if cmd := h.commands("e1")[0]; cmd.FencingToken != 51 {
		t.Fatalf("token %d, want above the agent's high-water mark 50", cmd.FencingToken)
	}
}

// TestAgentCrashMidStepEndToEnd kills a real agent runner in the middle of
// a non-idempotent step (backup snapshot after the containers were stopped)
// and in the middle of an idempotent step (deploy pull): the first ends
// interrupted with recovery guidance after the containers were restarted,
// the second resumes without repeating completed steps.
func TestAgentCrashMidStepEndToEnd(t *testing.T) {
	t.Run("non-idempotent snapshot", func(t *testing.T) {
		h := newHarness(t)
		fx := &jobstest.Effects{}
		entered := make(chan struct{})
		l := newLoop(h, "e1", simWithBlock(jobspec.BackupRun, fx, "snapshot", entered, make(chan struct{})))
		l.connect()
		j := h.enqueue(jobs.Request{Kind: jobspec.BackupRun, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web"), repo("r")}})
		h.dispatch()
		l.toAgent()
		<-entered
		l.toManager()
		h.wantState(j.ID, domain.JobRunning)
		l.kill()
		l.execs = []jobexec.Executor{sim(jobspec.BackupRun, fx)} // the restarted agent process
		l.start()
		l.connect()
		l.settle()
		ij := h.wantState(j.ID, domain.JobInterrupted)
		if ij.ErrorClass != domain.ErrorUnknownOutcome || !strings.Contains(ij.Recovery, "snapshot") {
			t.Fatalf("job %+v", ij)
		}
		want := []string{j.ID + ":prepare", j.ID + ":stop_containers", j.ID + ":compensate:start_containers"}
		if got := fx.List(); !slices.Equal(got, want) {
			t.Fatalf("effects %v, want %v", got, want)
		}
		h.noLocksHeld()
		if rep := l.runner.Report(); len(rep.Jobs) != 0 {
			t.Fatalf("journal not cleaned: %+v", rep)
		}
	})
	t.Run("idempotent pull resumes", func(t *testing.T) {
		h := newHarness(t)
		fx := &jobstest.Effects{}
		entered := make(chan struct{})
		l := newLoop(h, "e1", simWithBlock(jobspec.StackDeploy, fx, "pull_images", entered, make(chan struct{})))
		l.connect()
		j := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
		h.dispatch()
		l.toAgent()
		<-entered
		l.kill()
		l.execs = []jobexec.Executor{sim(jobspec.StackDeploy, fx)}
		l.start()
		l.connect()
		l.settle()
		sj := h.wantState(j.ID, domain.JobSucceeded)
		if sj.Attempt != 2 || sj.Resumes != 1 {
			t.Fatalf("attempt %d resumes %d", sj.Attempt, sj.Resumes)
		}
		want := []string{j.ID + ":resolve_sources", j.ID + ":pull_images", j.ID + ":build_images", j.ID + ":apply"}
		if got := fx.List(); !slices.Equal(got, want) {
			t.Fatalf("effects %v, want %v", got, want)
		}
		h.noLocksHeld()
	})
}

// TestResumedAttemptLostAgainIsResentNotLost: the command of a resumed
// attempt that never reached the agent is re-sent on the next reconnect
// (the earlier attempt's acknowledgement must not count for it), and item
// statuses from the agent are normalized.
func TestResumedAttemptLostAgainIsResentNotLost(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: "e1"})
	h.dispatch()
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	h.disp.Disconnect("e1")
	h.disp.Attach("e1")
	h.report("e1", protocol.JobReportPayload{HighWater: cmd.FencingToken, Jobs: []protocol.JobReportEntry{{JobID: j.ID, Attempt: 1,
		FencingToken: cmd.FencingToken, Status: protocol.ReportFinished,
		Result: &protocol.ResultPayload{Outcome: "interrupted", Resumable: true, CompletedSteps: []string{"collect_candidates"},
			Output: json.RawMessage(`{"items":[{"name":"img"}]}`)}}}})
	if rj := h.wantState(j.ID, domain.JobDispatched); rj.Attempt != 2 || rj.StartedAt != nil {
		t.Fatalf("resumed job %+v", rj)
	}
	h.disp.Disconnect("e1") // attempt 2's command is lost too
	h.disp.Attach("e1")
	h.report("e1", protocol.JobReportPayload{HighWater: cmd.FencingToken})
	rj := h.wantState(j.ID, domain.JobDispatched)
	cmds := h.commands("e1")
	if rj.Attempt != 3 || len(cmds) != 1 || cmds[0].Attempt != 3 {
		t.Fatalf("attempt %d commands %+v", rj.Attempt, cmds)
	}
	p, _ := protocol.DecodePayload[protocol.CommandPayload](cmds[0])
	if !slices.Equal(p.CompletedSteps, []string{"collect_candidates"}) {
		t.Fatalf("completed steps %v", p.CompletedSteps)
	}
	// The command re-built from the stored job keeps the collected
	// candidates (a resumed prune without them removed nothing, #26).
	if string(p.Output) != `{"items":[{"name":"img"}]}` {
		t.Fatalf("re-sent command output %s", p.Output)
	}
	h.ack("e1", cmds[0], protocol.AckPayload{Accepted: true})
	h.result("e1", cmds[0], protocol.ResultPayload{Outcome: "partial", Message: strings.Repeat("x", 10000),
		Items: []protocol.ItemPayload{{Name: "img", Status: "exploded"}}})
	pj := h.wantState(j.ID, domain.JobPartial)
	if len(pj.Items) != 1 || pj.Items[0].Status != domain.ItemFailed || len(pj.ErrorMessage) > 2048 {
		t.Fatalf("items %+v message %d", pj.Items, len(pj.ErrorMessage))
	}
	h.noLocksHeld()
}
