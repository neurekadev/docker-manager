package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/jobs/jobstest"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func TestEnqueueValidation(t *testing.T) {
	h := newHarness(t)
	cases := map[string]struct {
		req  jobs.Request
		want error
	}{
		"unknown kind":        {jobs.Request{Kind: "stack.frobnicate", Principal: user("a"), EnvironmentID: "e1"}, domain.ErrJobUnknownKind},
		"missing target":      {jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), EnvironmentID: "e1"}, domain.ErrJobInvalid},
		"missing environment": {jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), Targets: []domain.JobTarget{stack("s")}}, domain.ErrJobInvalid},
		"array input":         {jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}, Input: json.RawMessage(`[1]`)}, domain.ErrJobInvalid},
		"trailing input":      {jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}, Input: json.RawMessage(`{}{}`)}, domain.ErrJobInvalid},
		"invalid principal":   {jobs.Request{Kind: jobspec.StackDeploy, Principal: authz.Principal{Kind: authz.KindUser}, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}}, domain.ErrJobInvalid},
		"manager kind without executor": {jobs.Request{Kind: jobspec.ManagerRetention, Principal: user("a"),
			Targets: []domain.JobTarget{repo("r")}}, domain.ErrJobKindUnavailable},
		"long idempotency key": {jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")},
			IdempotencyKey: strings.Repeat("k", 129)}, domain.ErrJobInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := h.eng.Enqueue(h.ctx, c.req); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	h.az.revoke("mallory", "stack.deploy")
	_, _, err := h.eng.Enqueue(h.ctx, jobs.Request{Kind: jobspec.StackDeploy, Principal: user("mallory"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
	if !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("unauthorized enqueue: %v", err)
	}
	// Default authorizer denies everything (fail closed).
	e, _ := jobs.New(jobs.Options{DB: h.db, Clock: h.clk})
	if _, _, err := e.Enqueue(h.ctx, jobs.Request{Kind: jobspec.StackDeploy, Principal: user("a"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}}); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("default authorizer allowed: %v", err)
	}
	// ...but scheduled jobs run as the service identity.
	j, created, err := e.Enqueue(h.ctx, jobs.Request{Kind: jobspec.StackDeploy, Principal: authz.Service(), PolicyID: "p1", EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
	if err != nil || !created || j.Origin != domain.OriginScheduled || j.InitiatorUserID != "" || j.PolicyID != "p1" {
		t.Fatalf("scheduled enqueue: %+v %v %v", j, created, err)
	}
}

func TestEnqueueStoresModel(t *testing.T) {
	h := newHarness(t)
	tok := authz.Principal{Kind: authz.KindAPIToken, UserID: "bob", TokenID: "tok-1"}
	j := h.enqueue(jobs.Request{Kind: jobspec.BackupRun, Principal: tok, EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack("web"), volume("data"), repo("r1")}, Input: map[string]any{"b": 1, "a": "x"}})
	got := h.wantState(j.ID, domain.JobQueued)
	if got.Origin != domain.OriginAPIToken || got.InitiatorUserID != "bob" || got.InitiatorTokenID != "tok-1" ||
		got.Attempt != 1 || got.Executor != domain.ExecutorAgent || string(got.Input) != `{"a":"x","b":1}` || len(got.InputHash) != 64 {
		t.Fatalf("job %+v", got)
	}
	wantLocks, _ := mustSpec(jobspec.BackupRun).ComputeLocks("e1", got.Targets)
	if !slices.Equal(got.Locks, wantLocks) {
		t.Fatalf("locks %v, want %v", got.Locks, wantLocks)
	}
	ev, err := h.eng.Events(h.ctx, j.ID, 0, 10)
	if err != nil || len(ev) != 1 || ev[0].State != domain.JobQueued || ev[0].Seq != 1 {
		t.Fatalf("events %+v %v", ev, err)
	}
}

func mustSpec(k domain.JobKind) jobspec.Spec {
	s, ok := jobspec.Lookup(k)
	if !ok {
		panic(k)
	}
	return s
}

func TestIdempotency(t *testing.T) {
	h := newHarness(t)
	req := jobs.Request{Kind: jobspec.StackDeploy, Principal: user("alice"), EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack("web")}, Input: json.RawMessage(`{"pull":true,"force":false}`), IdempotencyKey: "k-1"}
	first, created, err := h.eng.Enqueue(h.ctx, req)
	if err != nil || !created {
		t.Fatal(err)
	}
	// Same key + same input (key order irrelevant) returns the existing job.
	req.Input = json.RawMessage(`{"force":false, "pull":true}`)
	again, created, err := h.eng.Enqueue(h.ctx, req)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %v created=%v id=%s", err, created, again.ID)
	}
	// Same key + different input is rejected.
	req.Input = json.RawMessage(`{"pull":false}`)
	if _, _, err := h.eng.Enqueue(h.ctx, req); !errors.Is(err, domain.ErrJobIdempotencyConflict) {
		t.Fatalf("conflict: %v", err)
	}
	// Same key + different target is rejected.
	req.Input = json.RawMessage(`{"pull":true,"force":false}`)
	req.Targets = []domain.JobTarget{stack("db")}
	if _, _, err := h.eng.Enqueue(h.ctx, req); !errors.Is(err, domain.ErrJobIdempotencyConflict) {
		t.Fatalf("target conflict: %v", err)
	}
	// Keys are scoped per principal.
	req.Targets = []domain.JobTarget{stack("web")}
	req.Principal = user("bob")
	other, created, err := h.eng.Enqueue(h.ctx, req)
	if err != nil || !created || other.ID == first.ID {
		t.Fatalf("other principal: %v %v", err, created)
	}
	// A finished job still answers its key.
	h.disp.Connect("e1")
	h.dispatch()
	h.completeAll("e1")
	req.Principal = user("alice")
	done, created, err := h.eng.Enqueue(h.ctx, req)
	if err != nil || created || done.ID != first.ID || done.State != domain.JobSucceeded {
		t.Fatalf("after finish: %+v %v %v", done, created, err)
	}
}

// TestConflictingJobsSerializeUnrelatedRunConcurrently: two conflicting jobs
// on one stack serialize (the second shows blocked_by the first); unrelated
// jobs on another environment run concurrently.
func TestConflictingJobsSerializeUnrelatedRunConcurrently(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	h.disp.Connect("e2")
	a := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	b := h.enqueue(jobs.Request{Kind: jobspec.UpdateRun, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	c := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e2", Targets: []domain.JobTarget{stack("web")}})
	d := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("db")}})
	h.dispatch()
	h.wantState(a.ID, domain.JobDispatched)
	bj := h.wantState(b.ID, domain.JobBlocked)
	if bj.BlockedBy != a.ID || bj.BlockedReason != domain.BlockedLock {
		t.Fatalf("b blocked by %q (%s), want %s", bj.BlockedBy, bj.BlockedReason, a.ID)
	}
	h.wantState(c.ID, domain.JobDispatched)
	h.wantState(d.ID, domain.JobDispatched)
	cmds1, cmds2 := h.commands("e1"), h.commands("e2")
	if len(cmds1) != 2 || len(cmds2) != 1 {
		t.Fatalf("commands e1=%d e2=%d", len(cmds1), len(cmds2))
	}
	// Fencing tokens are per environment and increase in send order.
	if cmds1[0].FencingToken != 1 || cmds1[1].FencingToken != 2 || cmds2[0].FencingToken != 1 {
		t.Fatalf("tokens %d %d %d", cmds1[0].FencingToken, cmds1[1].FencingToken, cmds2[0].FencingToken)
	}
	// The blocked job does not move while a runs.
	h.dispatch()
	h.wantState(b.ID, domain.JobBlocked)
	h.ack("e1", cmds1[0], protocol.AckPayload{Accepted: true})
	h.wantState(a.ID, domain.JobRunning)
	replies := h.result("e1", cmds1[0], protocol.ResultPayload{Outcome: "succeeded"})
	if p, _ := protocol.DecodePayload[protocol.AckPayload](replies[0]); !slices.Equal(p.Forget, []string{a.ID}) {
		t.Fatalf("result ack %+v", p)
	}
	h.wantState(a.ID, domain.JobSucceeded)
	h.dispatch()
	h.wantState(b.ID, domain.JobDispatched)
	if cmds := h.commands("e1"); len(cmds) != 1 || cmds[0].JobID != b.ID || cmds[0].FencingToken != 3 {
		t.Fatalf("b command %+v", cmds)
	}
}

// TestFIFOReservationPreventsStarvation: a queued exclusive job reserves
// its locks so a later shared job cannot overtake it.
func TestFIFOReservationPreventsStarvation(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	ctr := func(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetContainer, ID: id} }
	r := h.enqueue(jobs.Request{Kind: jobspec.ContainerRestart, EnvironmentID: "e1", Targets: []domain.JobTarget{ctr("web-1"), stack("web")}})
	h.dispatch()
	a := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	b := h.enqueue(jobs.Request{Kind: jobspec.ContainerRestart, EnvironmentID: "e1", Targets: []domain.JobTarget{ctr("web-2"), stack("web")}})
	h.dispatch()
	h.wantState(r.ID, domain.JobDispatched)
	if j := h.wantState(a.ID, domain.JobBlocked); j.BlockedBy != r.ID {
		t.Fatalf("a blocked by %s", j.BlockedBy)
	}
	if j := h.wantState(b.ID, domain.JobBlocked); j.BlockedBy != a.ID {
		t.Fatalf("b blocked by %s, want the earlier waiting job %s", j.BlockedBy, a.ID)
	}
	h.completeAll("e1")
	h.dispatch()
	h.wantState(a.ID, domain.JobDispatched)
	h.wantState(b.ID, domain.JobBlocked)
	h.completeAll("e1")
	h.dispatch()
	h.wantState(b.ID, domain.JobDispatched)
}

func TestLocksHeldSortedAndReleasedOnlyOnTerminal(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.RestoreRun, EnvironmentID: "e1",
		Targets: []domain.JobTarget{volume("zeta"), volume("alpha"), stack("web"), repo("r1")}})
	h.dispatch()
	held := h.heldLocks()
	var got []domain.JobLock
	for _, l := range held {
		if l.JobID != j.ID {
			t.Fatalf("foreign lock %+v", l)
		}
		got = append(got, l.Lock)
	}
	want := h.job(j.ID).Locks
	if !slices.Equal(got, want) || !slices.IsSortedFunc(got, jobspec.CompareLocks) {
		t.Fatalf("held %v, want sorted %v", got, want)
	}
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	h.agentFrame("e1", progressFrame(t, cmd, protocol.ProgressPayload{Step: "restore_data", Percent: 50, Message: "half"}))
	if j := h.wantState(j.ID, domain.JobRunning); j.Progress.Percent != 50 || j.Progress.Step != "restore_data" {
		t.Fatalf("progress %+v", j.Progress)
	}
	if len(h.heldLocks()) != len(want) {
		t.Fatal("locks released before terminal state")
	}
	h.result("e1", cmd, protocol.ResultPayload{Outcome: "failed", Message: "restic exited 1"})
	fj := h.wantState(j.ID, domain.JobFailed)
	if fj.ErrorClass != domain.ErrorStepFailed || fj.Recovery == "" || fj.FinishedAt == nil {
		t.Fatalf("failed job %+v", fj)
	}
	h.noLocksHeld()
}

func progressFrame(t *testing.T, cmd *protocol.Frame, p protocol.ProgressPayload) *protocol.Frame {
	t.Helper()
	f, err := protocol.NewFrame(protocol.TypeProgress, "p-"+cmd.ID, cmd.ID, cmd.Ref(), p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestConcurrencyCapsPerEnvironment(t *testing.T) {
	h := newHarness(t, func(o *jobs.Options) { o.Limits.ConcurrencyCaps = map[string]int{jobspec.ClassPull: 2} })
	h.disp.Connect("e1")
	h.disp.Connect("e2")
	p1 := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e1", Targets: []domain.JobTarget{image("a")}})
	p2 := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e1", Targets: []domain.JobTarget{image("b")}})
	p3 := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e1", Targets: []domain.JobTarget{image("c")}})
	p4 := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e2", Targets: []domain.JobTarget{image("a")}})
	b1 := h.enqueue(jobs.Request{Kind: jobspec.ImageBuild, EnvironmentID: "e1", Targets: []domain.JobTarget{image("x")}})
	b2 := h.enqueue(jobs.Request{Kind: jobspec.StackBuild, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
	h.dispatch()
	h.wantState(p1.ID, domain.JobDispatched)
	h.wantState(p2.ID, domain.JobDispatched)
	if j := h.wantState(p3.ID, domain.JobBlocked); j.BlockedReason != domain.BlockedConcurrency || j.BlockedBy != p1.ID {
		t.Fatalf("p3 %s by %s", j.BlockedReason, j.BlockedBy)
	}
	h.wantState(p4.ID, domain.JobDispatched)
	h.wantState(b1.ID, domain.JobDispatched)
	h.wantState(b2.ID, domain.JobBlocked) // default build cap is 1
	h.completeAll("e1")
	h.dispatch()
	h.wantState(p3.ID, domain.JobDispatched)
	h.wantState(b2.ID, domain.JobDispatched)
}

// TestOfflineDeadline: a queued job waits for an offline agent up to the
// kind's deadline, then fails with the stable agent_offline class; nothing
// was dispatched.
func TestOfflineDeadline(t *testing.T) {
	h := newHarness(t)
	deadline := mustSpec(jobspec.StackDeploy).OfflineDeadline
	j := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	if bj := h.wantState(j.ID, domain.JobBlocked); bj.BlockedReason != domain.BlockedAgentOffline {
		t.Fatalf("blocked %q", bj.BlockedReason)
	}
	h.clk.Advance(deadline - time.Second)
	h.dispatch()
	h.wantState(j.ID, domain.JobBlocked)
	h.clk.Advance(time.Second)
	h.dispatch()
	fj := h.wantState(j.ID, domain.JobFailed)
	if fj.ErrorClass != domain.ErrorAgentOffline || fj.Recovery == "" || !strings.Contains(fj.ErrorMessage, "offline") {
		t.Fatalf("job %+v", fj)
	}
	h.noLocksHeld()
	if h.disp.Pending("e1") != 0 {
		t.Fatal("command sent to an offline agent")
	}

	// A dispatched command that is never acknowledged because the agent went
	// offline also fails after the deadline.
	h.disp.Connect("e1")
	k := h.enqueue(jobs.Request{Kind: jobspec.StackStop, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	h.wantState(k.ID, domain.JobDispatched)
	h.disp.Disconnect("e1")
	h.clk.Advance(mustSpec(jobspec.StackStop).OfflineDeadline)
	h.dispatch()
	if kj := h.wantState(k.ID, domain.JobFailed); kj.ErrorClass != domain.ErrorAgentOffline || kj.Recovery == "" {
		t.Fatalf("job %+v", kj)
	}
	h.noLocksHeld()
}

// TestRecheckAtDispatch: a queued manual job whose initiator lost the grant
// is rejected at dispatch; scheduled jobs run as the service identity; a
// running job completes after the initiator lost access.
func TestRecheckAtDispatch(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	blocker := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, Principal: user("alice"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	manual := h.enqueue(jobs.Request{Kind: jobspec.StackRestart, Principal: user("bob"), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	sched := h.enqueue(jobs.Request{Kind: jobspec.StackRestart, Principal: authz.Service(), EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	h.wantState(manual.ID, domain.JobBlocked)
	h.az.revoke("bob", "stack.restart")
	h.az.revoke("alice", "stack.deploy") // the running job must still finish
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	h.result("e1", cmd, protocol.ResultPayload{Outcome: "succeeded"})
	h.wantState(blocker.ID, domain.JobSucceeded)
	h.dispatch()
	mj := h.wantState(manual.ID, domain.JobFailed)
	if mj.ErrorClass != domain.ErrorAuthorizationRevoked || mj.Recovery == "" {
		t.Fatalf("manual job %+v", mj)
	}
	h.wantState(sched.ID, domain.JobDispatched)
}

func TestCancellation(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	q := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e9", Targets: []domain.JobTarget{stack("web")}})
	got, err := h.eng.Cancel(h.ctx, q.ID)
	if err != nil || got.State != domain.JobCancelled || got.ErrorClass != domain.ErrorCancelled {
		t.Fatalf("cancel queued: %+v %v", got, err)
	}
	if _, err := h.eng.Cancel(h.ctx, q.ID); !errors.Is(err, domain.ErrJobFinished) {
		t.Fatalf("cancel finished: %v", err)
	}
	if _, err := h.eng.Cancel(h.ctx, "nope"); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("cancel unknown: %v", err)
	}

	r := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	got, err = h.eng.Cancel(h.ctx, r.ID)
	if err != nil || got.State != domain.JobCancelling || !got.CancelRequested {
		t.Fatalf("cancel running: %+v %v", got, err)
	}
	frames := h.disp.Drain("e1")
	if len(frames) != 1 || frames[0].Type != protocol.TypeCancel || frames[0].JobID != r.ID || frames[0].FencingToken != cmd.FencingToken {
		t.Fatalf("cancel frames %+v", frames)
	}
	// Cancelling twice is idempotent and sends nothing new.
	if _, err := h.eng.Cancel(h.ctx, r.ID); err != nil || h.disp.Pending("e1") != 0 {
		t.Fatalf("second cancel: %v", err)
	}
	// The job finished before reaching a safe point: success stands.
	h.result("e1", cmd, protocol.ResultPayload{Outcome: "succeeded"})
	h.wantState(r.ID, domain.JobSucceeded)
	h.noLocksHeld()
}

// TestCancellationSafePointsAndCompensationEndToEnd runs backup.run on a real
// agent runner: cancellation requested while containers are being stopped
// takes effect before the snapshot and the containers are restarted.
func TestCancellationSafePointsAndCompensationEndToEnd(t *testing.T) {
	h := newHarness(t)
	fx := &jobstest.Effects{}
	entered, release := make(chan struct{}), make(chan struct{})
	exec := simWithBlock(jobspec.BackupRun, fx, "stop_containers", entered, release)
	l := newLoop(h, "e1", exec)
	l.connect()
	j := h.enqueue(jobs.Request{Kind: jobspec.BackupRun, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web"), repo("r")}})
	h.dispatch()
	l.toAgent()
	<-entered // stop_containers is running
	if _, err := h.eng.Cancel(h.ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	l.toAgent() // deliver the cancel frame
	close(release)
	l.runner.Wait()
	l.toManager()
	cj := h.wantState(j.ID, domain.JobCancelled)
	if cj.ErrorClass != domain.ErrorCancelled || cj.Recovery == "" {
		t.Fatalf("job %+v", cj)
	}
	want := []string{j.ID + ":prepare", j.ID + ":stop_containers", j.ID + ":compensate:start_containers"}
	if got := fx.List(); !slices.Equal(got, want) {
		t.Fatalf("effects %v, want %v", got, want)
	}
	h.noLocksHeld()
	if rep := l.runner.Report(); len(rep.Jobs) != 0 {
		t.Fatalf("agent journal not cleared after the result ack: %+v", rep)
	}
}

func TestAgentCannotAffectOtherEnvironments(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	cmd := h.commands("e1")[0]
	h.ack("e2", cmd, protocol.AckPayload{Accepted: true})
	h.agentFrame("e2", progressFrame(t, cmd, protocol.ProgressPayload{Percent: 10}))
	if replies := h.result("e2", cmd, protocol.ResultPayload{Outcome: "succeeded"}); len(replies) != 0 {
		t.Fatalf("foreign result acknowledged: %v", replies)
	}
	h.report("e2", protocol.JobReportPayload{Jobs: []protocol.JobReportEntry{{JobID: j.ID, Attempt: 1, FencingToken: cmd.FencingToken,
		Status: protocol.ReportFinished, Result: &protocol.ResultPayload{Outcome: "failed"}}}})
	h.wantState(j.ID, domain.JobDispatched)
	// A stale attempt/token from the right environment is ignored too.
	stale := *cmd
	stale.FencingToken = 99
	h.ack("e1", &stale, protocol.AckPayload{Accepted: true})
	h.wantState(j.ID, domain.JobDispatched)
}

func TestAgentRejectsCommand(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Code: protocol.AckUnsupportedKind, Message: "old agent"})
	fj := h.wantState(j.ID, domain.JobFailed)
	if fj.ErrorClass != domain.ErrorRejected || !strings.Contains(fj.ErrorMessage, "unsupported_kind") {
		t.Fatalf("job %+v", fj)
	}
	h.noLocksHeld()

	// A stale-token rejection (manager counter behind the agent) moves the
	// counter past the agent's high-water mark and re-dispatches.
	k := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.dispatch()
	cmd = h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Code: protocol.AckStaleToken, HighWater: 40})
	kj := h.wantState(k.ID, domain.JobDispatched)
	next := h.commands("e1")
	if kj.Attempt != 2 || len(next) != 1 || next[0].FencingToken != 41 || next[0].Attempt != 2 {
		t.Fatalf("job %+v commands %+v", kj, next)
	}
}

func TestEventsBoundedAndSubscribe(t *testing.T) {
	h := newHarness(t, func(o *jobs.Options) { o.Limits.MaxEventsPerJob = 5 })
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: "e1"})
	ch, cancel := h.eng.Subscribe(j.ID)
	defer cancel()
	h.dispatch()
	select {
	case <-ch:
	default:
		t.Fatal("subscriber not notified on dispatch")
	}
	cmd := h.commands("e1")[0]
	for i := 0; i < 20; i++ {
		h.agentFrame("e1", progressFrame(t, cmd, protocol.ProgressPayload{Step: "delete_candidates", Percent: i * 5,
			Item: &protocol.ItemPayload{Name: "image-" + string(rune('a'+i)), Status: domain.ItemSucceeded}}))
	}
	ev, err := h.eng.Events(h.ctx, j.ID, 0, 100)
	if err != nil || len(ev) != 5 || ev[len(ev)-1].Seq != h.job(j.ID).LastEventSeq {
		t.Fatalf("events %d %v (last seq %d)", len(ev), err, h.job(j.ID).LastEventSeq)
	}
	if ev[0].Seq != h.job(j.ID).LastEventSeq-4 || ev[4].Item == nil {
		t.Fatalf("events %+v", ev)
	}
	if len(h.job(j.ID).Items) != 20 {
		t.Fatalf("items %d", len(h.job(j.ID).Items))
	}
}

func TestRetention(t *testing.T) {
	h := newHarness(t, func(o *jobs.Options) {
		o.Limits.HistoryMaxAge = 48 * time.Hour
		o.Limits.HistoryMaxJobs = 3
	})
	var ids []string
	for i := 0; i < 6; i++ {
		j := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
		if _, err := h.eng.Cancel(h.ctx, j.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		h.clk.Advance(24 * time.Hour)
	}
	live := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
	n, err := h.eng.Retain(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Finished at day 0..5, now day 6: older than 48h are days 0..3 (4 jobs);
	// the remaining 2 are within the count bound.
	if n != 4 {
		t.Fatalf("deleted %d", n)
	}
	for i, id := range ids {
		_, err := h.eng.Get(h.ctx, id)
		if gone := errors.Is(err, domain.ErrJobNotFound); gone != (i < 4) {
			t.Fatalf("job %d gone=%v err=%v", i, gone, err)
		}
	}
	if ev, _ := h.eng.Events(h.ctx, ids[0], 0, 10); len(ev) != 0 {
		t.Fatal("events of deleted job remain")
	}
	h.wantState(live.ID, domain.JobQueued) // never deletes unfinished jobs

	// Count bound.
	h2 := newHarness(t, func(o *jobs.Options) { o.Limits.HistoryMaxJobs = 2 })
	for i := 0; i < 4; i++ {
		j := h2.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("s")}})
		_, _ = h2.eng.Cancel(h2.ctx, j.ID)
		h2.clk.Advance(time.Minute)
	}
	if n, _ := h2.eng.Retain(h2.ctx); n != 2 {
		t.Fatalf("count bound deleted %d", n)
	}
}

func TestListFilters(t *testing.T) {
	h := newHarness(t)
	a := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	b := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e2", Targets: []domain.JobTarget{stack("web")}})
	c := h.enqueue(jobs.Request{Kind: jobspec.PruneRun, EnvironmentID: "e1"})
	mig := h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e3", Targets: []domain.JobTarget{stack("api")}})
	_, _ = h.eng.Cancel(h.ctx, c.ID)
	ids := func(f domain.JobFilter) []string {
		js, err := h.eng.List(h.ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, j := range js {
			out = append(out, j.ID)
		}
		return out
	}
	if got := ids(domain.JobFilter{}); !slices.Equal(got, []string{mig.ID, c.ID, b.ID, a.ID}) {
		t.Fatalf("all newest first: %v", got)
	}
	if got := ids(domain.JobFilter{EnvironmentID: "e1"}); !slices.Equal(got, []string{c.ID, a.ID}) {
		t.Fatalf("env: %v", got)
	}
	if got := ids(domain.JobFilter{States: []domain.JobState{domain.JobCancelled}}); !slices.Equal(got, []string{c.ID}) {
		t.Fatalf("state: %v", got)
	}
	if got := ids(domain.JobFilter{Kinds: []domain.JobKind{jobspec.StackDeploy}}); !slices.Equal(got, []string{b.ID, a.ID}) {
		t.Fatalf("kind: %v", got)
	}
	tgt := stack("web")
	if got := ids(domain.JobFilter{Target: &tgt}); !slices.Equal(got, []string{b.ID, a.ID}) {
		t.Fatalf("target: %v", got)
	}
	tgt.EnvironmentID = "e2"
	if got := ids(domain.JobFilter{Target: &tgt}); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("target+env: %v", got)
	}
	if got := ids(domain.JobFilter{BeforeID: b.ID, Limit: 1}); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("cursor: %v", got)
	}
}

// TestOnChangeObservesEveryJobChange (#23): change listeners see a job's
// creation, dispatch and completion (the live stream's job source).
func TestOnChangeObservesEveryJobChange(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	seen := map[string]int{}
	h.eng.OnChange(func(ids []string) {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range ids {
			seen[id]++
		}
	})
	j := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen[j.ID]
	}
	if count() != 1 {
		t.Fatalf("creation not observed: %d", count())
	}
	h.disp.Connect("e1")
	h.dispatch()
	afterDispatch := count()
	if afterDispatch < 2 {
		t.Fatalf("dispatch not observed: %d", afterDispatch)
	}
	h.completeAll("e1")
	if count() <= afterDispatch {
		t.Fatalf("completion not observed: %d", count())
	}
}

// TestRestoreRefusesStarts (#10): while a restore has not ended, kinds that
// start containers are refused on its data instead of waiting behind it;
// other kinds (a stop) and unrelated targets queue as usual.
func TestRestoreRefusesStarts(t *testing.T) {
	h := newHarness(t)
	h.disp.Connect("e1")
	container := func(id string) domain.JobTarget { return domain.JobTarget{Type: domain.TargetContainer, ID: id} }
	r := h.enqueue(jobs.Request{Kind: jobspec.RestoreRun, EnvironmentID: "e1",
		Targets: []domain.JobTarget{stack("web"), volume("web_data"), container("worker"), repo("r1")}})
	refused := func(kind domain.JobKind, targets ...domain.JobTarget) {
		t.Helper()
		_, _, err := h.eng.Enqueue(h.ctx, jobs.Request{Kind: kind, Principal: user("alice"), EnvironmentID: "e1", Targets: targets})
		if !errors.Is(err, domain.ErrRestoreInProgress) {
			t.Errorf("%s %v while restoring: %v", kind, targets, err)
		}
	}
	// Queued, not dispatched yet: already refused.
	refused(jobspec.StackStart, stack("web"))
	h.dispatch()
	refused(jobspec.StackDeploy, stack("web"))
	refused(jobspec.StackRestart, stack("web"))
	refused(jobspec.ContainerStart, container("web-1"), stack("web"))
	refused(jobspec.ContainerStart, container("worker"))
	refused(jobspec.UpdateRun, stack("web"))
	h.enqueue(jobs.Request{Kind: jobspec.StackStop, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
	h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("other")}})
	cmd := h.commands("e1")[0]
	if cmd.Ref().JobID != r.ID {
		t.Fatalf("first command %+v", cmd)
	}
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	h.result("e1", cmd, protocol.ResultPayload{Outcome: "succeeded"})
	h.wantState(r.ID, domain.JobSucceeded)
	h.enqueue(jobs.Request{Kind: jobspec.StackStart, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}})
}

// TestActivityFramesStayInMemory: a progress frame carrying only activity
// (#10) reaches the OnActivity listeners with the session's environment
// and changes nothing in the job record or its events.
func TestActivityFramesStayInMemory(t *testing.T) {
	h := newHarness(t)
	type report struct {
		env, job string
		a        protocol.ActivityPayload
	}
	var got []report
	h.eng.OnActivity(func(env, jobID string, a protocol.ActivityPayload) { got = append(got, report{env, jobID, a}) })
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.RestoreRun, EnvironmentID: "e1", Targets: []domain.JobTarget{volume("media"), repo("r1")}})
	h.dispatch()
	cmd := h.commands("e1")[0]
	h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
	h.agentFrame("e1", progressFrame(t, cmd, protocol.ProgressPayload{Step: "restore_data", Percent: 40, Message: "restoring"}))
	before, err := h.eng.Events(h.ctx, j.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	a := protocol.ActivityPayload{Item: "volume/media", ItemCount: 1, Percent: 70, FilesDone: 7, FilesTotal: 10, CurrentFile: "media/a.jpg"}
	h.agentFrame("e1", progressFrame(t, cmd, protocol.ProgressPayload{Step: "restore_data", Percent: -1, Activity: &a}))
	if len(got) != 1 || got[0].env != "e1" || got[0].job != j.ID || got[0].a != a {
		t.Fatalf("listener got %+v", got)
	}
	after, err := h.eng.Events(h.ctx, j.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("activity wrote job events: %d -> %d", len(before), len(after))
	}
	if cur := h.job(j.ID); cur.Progress.Percent != 40 || cur.Progress.Message != "restoring" {
		t.Errorf("activity changed the job progress: %+v", cur.Progress)
	}
}

// TestCommandInputAdaptsTheSentInputOnly: the CommandInput hook shapes the
// input of the command sent to the agent; the stored job keeps its input,
// and a nil answer sends the stored input.
func TestCommandInputAdaptsTheSentInputOnly(t *testing.T) {
	stored := json.RawMessage(`{"name":"web"}`)
	h := newHarness(t, func(o *jobs.Options) {
		o.CommandInput = func(_ context.Context, j *domain.Job) json.RawMessage {
			if j.Targets[0].ID == "web" {
				return json.RawMessage(`{"name":"web","extra":true}`)
			}
			return nil
		}
	})
	h.disp.Connect("e1")
	adapted := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("web")}, Input: stored})
	plain := h.enqueue(jobs.Request{Kind: jobspec.StackDeploy, EnvironmentID: "e1", Targets: []domain.JobTarget{stack("db")}, Input: stored})
	h.dispatch()
	sent := map[string]string{}
	for _, f := range h.commands("e1") {
		p, err := protocol.DecodePayload[protocol.CommandPayload](f)
		if err != nil {
			t.Fatal(err)
		}
		sent[f.JobID] = string(p.Input)
	}
	if sent[adapted.ID] != `{"name":"web","extra":true}` || sent[plain.ID] != string(stored) {
		t.Errorf("sent inputs = %v", sent)
	}
	if got := h.job(adapted.ID).Input; string(got) != string(stored) {
		t.Errorf("stored input = %s, want %s", got, stored)
	}
}
