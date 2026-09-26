package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

type hookCall struct {
	id     string
	state  domain.JobState
	output string
}

type hookRecorder struct {
	mu    sync.Mutex
	calls []hookCall
	fail  error
}

func (r *hookRecorder) hook(_ context.Context, db bun.IDB, j domain.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	if db == nil {
		return errors.New("no transaction")
	}
	r.calls = append(r.calls, hookCall{j.ID, j.State, string(j.ResultOutput)})
	return nil
}

func (r *hookRecorder) get() []hookCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookCall(nil), r.calls...)
}

// TestFinishHooksSeeTheResultOutput (#7): features receive the executor's
// result output in the transaction that finishes the job, on every
// terminal path; a failing hook keeps the job unfinished.
func TestFinishHooksSeeTheResultOutput(t *testing.T) {
	h := newHarness(t)
	rec := &hookRecorder{}
	h.eng.OnFinish(jobspec.StackDeploy, rec.hook)
	h.disp.Connect("env-1")

	j := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s1")}})
	h.dispatch()
	cmd := h.commands("env-1")[0]
	h.ack("env-1", cmd, protocol.AckPayload{Accepted: true})

	// A hook error aborts the finishing transaction: the job stays running
	// and the agent's result is delivered again later.
	rec.fail = errors.New("database busy")
	f, err := protocol.NewFrame(protocol.TypeResult, "res-1", cmd.ID, cmd.Ref(),
		protocol.ResultPayload{Outcome: "succeeded", Output: json.RawMessage(`{"sources":{"hash":"h"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.HandleAgentFrame(h.ctx, "env-1", f); err == nil {
		t.Fatal("a failing hook did not abort the result")
	}
	h.wantState(j.ID, domain.JobRunning)
	rec.fail = nil
	h.agentFrame("env-1", f)
	h.wantState(j.ID, domain.JobSucceeded)
	if got := rec.get(); len(got) != 1 || got[0].state != domain.JobSucceeded || got[0].output != `{"sources":{"hash":"h"}}` {
		t.Fatalf("hook calls %+v", got)
	}

	// A job cancelled while queued finishes without an output.
	j2 := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s2")}})
	if _, err := h.eng.Cancel(h.ctx, j2.ID); err != nil {
		t.Fatal(err)
	}
	if got := rec.get(); len(got) != 2 || got[1].id != j2.ID || got[1].state != domain.JobCancelled || got[1].output != "" {
		t.Fatalf("hook calls %+v", got)
	}

	// Outputs reported in a job_report after a reconnect reach the hook too.
	j3 := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s3")}})
	h.dispatch()
	cmd3 := h.commands("env-1")[0]
	h.ack("env-1", cmd3, protocol.AckPayload{Accepted: true})
	h.report("env-1", protocol.JobReportPayload{HighWater: cmd3.FencingToken, Jobs: []protocol.JobReportEntry{{
		JobID: j3.ID, Attempt: cmd3.Attempt, FencingToken: cmd3.FencingToken, Kind: string(jobspec.StackDeploy), Status: protocol.ReportFinished,
		Result: &protocol.ResultPayload{Outcome: "failed", ErrorClass: "step_failed", Message: "x", Output: json.RawMessage(`{"after":[]}`)}}}})
	h.wantState(j3.ID, domain.JobFailed)
	if got := rec.get(); len(got) != 3 || got[2].state != domain.JobFailed || got[2].output != `{"after":[]}` {
		t.Fatalf("hook calls %+v", got)
	}
	// Hooks of other kinds are not called.
	j4 := h.enqueue(jobs.Request{Kind: jobspec.StackStop, EnvironmentID: "env-1", Targets: []domain.JobTarget{stack("s4")}})
	if _, err := h.eng.Cancel(h.ctx, j4.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.get()) != 3 {
		t.Error("a stack.deploy hook ran for stack.stop")
	}
}
