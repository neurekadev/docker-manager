package jobs_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/protocol"
)

func register(t *testing.T, e *jobs.Engine, execs ...jobexec.Executor) {
	t.Helper()
	for _, x := range execs {
		if err := e.RegisterManagerExecutor(x); err != nil {
			t.Fatal(err)
		}
	}
}

// TestManagerRestartRecovery stops the manager while manager-local jobs are
// mid-step and an agent job is running: after the restart the resumable kind
// resumes from its journal, the interrupt kind becomes interrupted with
// guidance, and the agent job keeps its locks until its agent reconciles.
func TestManagerRestartRecovery(t *testing.T) {
	h := newHarness(t)
	fx := &jobstest.Effects{}
	retEntered, bkEntered := make(chan struct{}), make(chan struct{})
	register(t, h.eng,
		simWithBlock(jobspec.ManagerRetention, fx, "prune_repository", retEntered, make(chan struct{})),
		simWithBlock(jobspec.ManagerBackup, fx, "backup", bkEntered, make(chan struct{})),
		sim(jobspec.ManagerVerify, fx))
	svc := authz.Service()
	ret := h.enqueue(jobs.Request{Kind: jobspec.ManagerRetention, Principal: svc, Targets: []domain.JobTarget{repo("r1")}})
	bk := h.enqueue(jobs.Request{Kind: jobspec.ManagerBackup, Principal: svc, Targets: []domain.JobTarget{repo("r2")}})
	h.disp.Connect("e1")
	ag := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	<-retEntered
	<-bkEntered
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	// verify waits behind retention's exclusive repository lock.
	ver := h.enqueue(jobs.Request{Kind: jobspec.ManagerVerify, Principal: svc, Targets: []domain.JobTarget{repo("r1")}})
	h.dispatch()
	h.wantState(ver.ID, domain.JobBlocked)

	h.eng.Close() // manager stops; steps in flight are abandoned
	h.disp.Disconnect("e1")
	if j := h.wantState(ret.ID, domain.JobRunning); !j.StepInFlight || j.CurrentStep != "prune_repository" {
		t.Fatalf("retention journal %+v", j)
	}

	// Restart.
	h.eng = h.newEngine()
	register(t, h.eng, sim(jobspec.ManagerRetention, fx), sim(jobspec.ManagerBackup, fx), sim(jobspec.ManagerVerify, fx))
	if err := h.eng.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.eng.Wait()
	rj := h.wantState(ret.ID, domain.JobSucceeded)
	if rj.Attempt != 2 || rj.Resumes != 1 {
		t.Fatalf("retention attempt %d resumes %d", rj.Attempt, rj.Resumes)
	}
	bj := h.wantState(bk.ID, domain.JobInterrupted)
	if bj.ErrorClass != domain.ErrorUnknownOutcome || !strings.Contains(bj.Recovery, "manager backup") {
		t.Fatalf("manager.backup %+v", bj)
	}
	// Per job (the two jobs ran concurrently): no step re-run except the
	// idempotent in-flight one, which had not recorded its effect yet.
	of := func(id string) []string {
		var out []string
		for _, e := range fx.List() {
			if rest, ok := strings.CutPrefix(e, id+":"); ok {
				out = append(out, rest)
			}
		}
		return out
	}
	if got := of(ret.ID); !slices.Equal(got, []string{"forget", "prune_repository"}) {
		t.Fatalf("retention effects %v", got)
	}
	if got := of(bk.ID); !slices.Equal(got, []string{"snapshot_database"}) {
		t.Fatalf("manager.backup effects %v", got)
	}
	// The agent job keeps state and locks until its agent reports.
	h.wantState(ag.ID, domain.JobRunning)
	h.dispatch()
	h.eng.Wait()
	h.wantState(ver.ID, domain.JobSucceeded)
	h.disp.Attach("e1")
	h.report("e1", protocol.JobReportPayload{HighWater: cmd.FencingToken, Jobs: []protocol.JobReportEntry{{JobID: ag.ID, Attempt: 1,
		FencingToken: cmd.FencingToken, Kind: string(jobspec.StackDeploy), Status: protocol.ReportFinished, Result: &protocol.ResultPayload{Outcome: "succeeded"}}}})
	h.wantState(ag.ID, domain.JobSucceeded)
	h.noLocksHeld()
}

// TestManagerLocalResumeKeepsOutput (#26): a manager-local job resumed
// after a manager restart continues from the output its completed steps
// journaled (the jobs row), like a resumed agent attempt.
func TestManagerLocalResumeKeepsOutput(t *testing.T) {
	h := newHarness(t)
	entered := make(chan struct{})
	first := jobexec.Executor{Kind: jobspec.ManagerRetention, Steps: map[string]jobexec.StepFunc{
		"forget": func(ctx context.Context, sc *jobexec.StepContext) error {
			return sc.SetOutput(ctx, map[string]int{"forgotten": 3})
		},
		"prune_repository": func(ctx context.Context, _ *jobexec.StepContext) error {
			close(entered)
			<-ctx.Done() // the manager stops mid-step
			return ctx.Err()
		},
	}}
	register(t, h.eng, first)
	j := h.enqueue(jobs.Request{Kind: jobspec.ManagerRetention, Principal: authz.Service(), Targets: []domain.JobTarget{repo("r1")}})
	h.dispatch()
	<-entered
	h.eng.Close()

	h.eng = h.newEngine()
	var seen string
	register(t, h.eng, jobexec.Executor{Kind: jobspec.ManagerRetention, Steps: map[string]jobexec.StepFunc{
		"forget": func(context.Context, *jobexec.StepContext) error { t.Error("the completed step ran again"); return nil },
		"prune_repository": func(_ context.Context, sc *jobexec.StepContext) error {
			seen = string(sc.Output())
			return nil
		},
	}})
	if err := h.eng.Recover(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.eng.Wait()
	if rj := h.wantState(j.ID, domain.JobSucceeded); rj.Attempt != 2 {
		t.Fatalf("attempt %d", rj.Attempt)
	}
	if seen != `{"forgotten":3}` {
		t.Fatalf("the resumed step saw output %q", seen)
	}
}

func TestManagerJobLifecycle(t *testing.T) {
	h := newHarness(t)
	fx := &jobstest.Effects{}
	entered, release := make(chan struct{}), make(chan struct{})
	register(t, h.eng, simWithBlock(jobspec.BackupImport, fx, "scan", entered, release))
	j := h.enqueue(jobs.Request{Kind: jobspec.BackupImport, Targets: []domain.JobTarget{repo("r")}})
	h.dispatch()
	<-entered
	h.wantState(j.ID, domain.JobRunning)
	// scan is running; cancellation is honored before import_index (a safe point).
	if _, err := h.eng.Cancel(h.ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	h.eng.Wait()
	cj := h.wantState(j.ID, domain.JobCancelled)
	if !slices.Equal(cj.CompletedSteps, []string{"scan"}) || cj.Recovery == "" {
		t.Fatalf("job %+v", cj)
	}
	h.noLocksHeld()

	// Duplicate/invalid executor registration is refused.
	if err := h.eng.RegisterManagerExecutor(sim(jobspec.BackupImport, fx)); err == nil {
		t.Fatal("duplicate executor accepted")
	}
	if err := h.eng.RegisterManagerExecutor(sim(jobspec.StackDeploy, fx)); err == nil {
		t.Fatal("agent kind registered as manager executor")
	}

	// A failing step fails the job with guidance.
	failing := jobstest.SimExecutor(jobspec.ManagerVerify, jobstest.SimOptions{Effects: fx, Before: map[string]func(context.Context, *jobexec.StepContext) error{
		"check": func(context.Context, *jobexec.StepContext) error { return context.DeadlineExceeded },
	}})
	register(t, h.eng, failing)
	v := h.enqueue(jobs.Request{Kind: jobspec.ManagerVerify, Targets: []domain.JobTarget{repo("r")}})
	h.dispatch()
	h.eng.Wait()
	if fj := h.wantState(v.ID, domain.JobFailed); fj.ErrorClass != domain.ErrorStepFailed || fj.Recovery == "" {
		t.Fatalf("job %+v", fj)
	}
}
