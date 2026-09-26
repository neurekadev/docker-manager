package agents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
)

var alice = authz.Principal{Kind: authz.KindUser, UserID: "alice"}

func (f *fixture) enqueue(kind domain.JobKind, env string, targets ...domain.JobTarget) domain.Job {
	f.t.Helper()
	j, _, err := f.jobs.Enqueue(f.ctx, jobs.Request{Kind: kind, Principal: alice, EnvironmentID: env, Targets: targets})
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

// waitJob follows the job's change notifications until it reaches state.
func (f *fixture) waitJob(id string, want domain.JobState) domain.Job {
	f.t.Helper()
	changed, cancel := f.jobs.Subscribe(id)
	defer cancel()
	for {
		j, err := f.jobs.Get(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if j.State == want {
			return j
		}
		if j.State.Terminal() {
			f.t.Fatalf("job %s ended %s (%s: %s), want %s", id, j.State, j.ErrorClass, j.ErrorMessage, want)
		}
		select {
		case <-changed:
		case <-f.ctx.Done():
			f.t.Fatalf("job %s stuck in %s, want %s", id, j.State, want)
		}
	}
}

func (f *fixture) dispatch() {
	f.t.Helper()
	if err := f.jobs.DispatchPending(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

// TestJobDispatchedToEachEnvironment: jobs of two environments run on their
// own agents at the same time over the real transport.
func TestJobDispatchedToEachEnvironment(t *testing.T) {
	f := newFixture(t)
	gate := make(chan struct{})
	started := make(chan string, 2)
	block := func(ctx context.Context, sc *jobexec.StepContext) error {
		started <- sc.JobID
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var effects [2]jobstest.Effects
	envs := make([]string, 2)
	for i, eng := range []string{"ENG-1", "ENG-2"} {
		exec := jobstest.SimExecutor(jobspec.ContainerRestart, jobstest.SimOptions{Effects: &effects[i],
			Before: map[string]func(context.Context, *jobexec.StepContext) error{"restart": block}})
		a := f.newAgent(eng, "host-"+eng, exec)
		r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
		a.start()
		f.waitOnline(r.EnvironmentID)
		envs[i] = r.EnvironmentID
	}
	j1 := f.enqueue(jobspec.ContainerRestart, envs[0], domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	j2 := f.enqueue(jobspec.ContainerRestart, envs[1], domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	f.dispatch()
	// Both are running on their agents before either finishes.
	got := []string{<-started, <-started}
	slices.Sort(got)
	want := []string{j1.ID, j2.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
	f.waitJob(j1.ID, domain.JobRunning)
	f.waitJob(j2.ID, domain.JobRunning)
	close(gate)
	f.waitJob(j1.ID, domain.JobSucceeded)
	f.waitJob(j2.ID, domain.JobSucceeded)
	if e := effects[0].List(); !slices.Equal(e, []string{j1.ID + ":restart"}) {
		t.Fatalf("agent 1 effects %v", e)
	}
	if e := effects[1].List(); !slices.Equal(e, []string{j2.ID + ":restart"}) {
		t.Fatalf("agent 2 effects %v", e)
	}
}

// TestReconnectReconcilesRunningJob: the session drops while a job runs;
// the agent keeps executing, cannot send the result, reconnects with
// backoff and its job report completes the job exactly once.
func TestReconnectReconcilesRunningJob(t *testing.T) {
	f := newFixture(t)
	var effects jobstest.Effects
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	exec := jobstest.SimExecutor(jobspec.ContainerRestart, jobstest.SimOptions{Effects: &effects,
		Before: map[string]func(context.Context, *jobexec.StepContext) error{"restart": func(ctx context.Context, _ *jobexec.StepContext) error {
			started <- struct{}{}
			<-gate
			return nil
		}}})
	a := f.newAgent("ENG-A", "host-a", exec)
	a.redial = make(chan struct{})
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	f.waitOnline(r.EnvironmentID)
	a.waitState(session.StateOnline)
	j := f.enqueue(jobspec.ContainerRestart, r.EnvironmentID, domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	f.dispatch()
	<-started
	f.waitJob(j.ID, domain.JobRunning)

	// Drop the connection; the step is still running on the agent. The
	// agent's reconnect is held until the step finished, so the outcome can
	// only reach the manager through the job report.
	f.svc.Hub().Session(r.AgentID).closeWith(1011, "network drop")
	a.waitState(session.StateDisconnected)
	f.waitEvent(events.EnvironmentOffline, r.EnvironmentID)
	if got, _ := f.jobs.Get(f.ctx, j.ID); got.State != domain.JobRunning {
		t.Fatalf("job %s while the agent is away, want running", got.State)
	}
	close(gate)
	waitCond(t, f, func() bool { st, ok := a.runner.Journal().Get(j.ID); return ok && st.Outcome != nil })
	close(a.redial)
	f.waitOnline(r.EnvironmentID)
	done := f.waitJob(j.ID, domain.JobSucceeded)
	if done.Attempt != 1 {
		t.Fatalf("job re-dispatched: attempt %d", done.Attempt)
	}
	if e := effects.List(); !slices.Equal(e, []string{j.ID + ":restart"}) {
		t.Fatalf("effects %v (the step must run exactly once)", e)
	}
	// The agent's journal entry is forgotten after the manager's ack.
	waitCond(t, f, func() bool { _, ok := a.runner.Journal().Get(j.ID); return !ok })
}

// waitCond re-checks cond whenever the bus publishes or a job changes; it
// fails at the test deadline.
func waitCond(t *testing.T, f *fixture, cond func() bool) {
	t.Helper()
	sub := f.bus.Subscribe(64, nil)
	defer sub.Close()
	tick := time.NewTicker(10 * time.Millisecond) // polling a condition, not asserting on time
	defer tick.Stop()
	for !cond() {
		select {
		case <-sub.C():
		case <-tick.C:
		case <-f.ctx.Done():
			t.Fatal("condition not reached")
		}
	}
}

// TestOfflineJobFailsAfterDeadline: a job for an offline environment waits
// (blocked, agent_offline) and fails with agent_offline after its kind's
// offline deadline; nothing ran.
func TestOfflineJobFailsAfterDeadline(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	j := f.enqueue(jobspec.ContainerRestart, r.EnvironmentID, domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	f.dispatch()
	if got, _ := f.jobs.Get(f.ctx, j.ID); got.State != domain.JobBlocked || got.BlockedReason != "agent_offline" {
		t.Fatalf("waiting job %+v", got)
	}
	spec, _ := jobspec.Lookup(jobspec.ContainerRestart)
	f.clk.Advance(spec.OfflineDeadline + time.Second)
	f.dispatch()
	got, _ := f.jobs.Get(f.ctx, j.ID)
	if got.State != domain.JobFailed || got.ErrorClass != domain.ErrorAgentOffline || !strings.Contains(got.Recovery+got.ErrorMessage, "nothing was changed") {
		t.Fatalf("offline job %+v", got)
	}
}

// TestJobWaitsForReconnect: a job queued while the agent is offline is
// dispatched once the agent reconnects within the deadline.
func TestJobWaitsForReconnect(t *testing.T) {
	f := newFixture(t)
	var effects jobstest.Effects
	a := f.newAgent("ENG-A", "host-a", jobstest.SimExecutor(jobspec.ContainerRestart, jobstest.SimOptions{Effects: &effects}))
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	j := f.enqueue(jobspec.ContainerRestart, r.EnvironmentID, domain.JobTarget{Type: domain.TargetContainer, ID: "web"})
	f.dispatch()
	f.clk.Advance(time.Minute)
	a.start()
	f.waitOnline(r.EnvironmentID)
	f.dispatch()
	f.waitJob(j.ID, domain.JobSucceeded)
}

// TestCredentialRotationOverLiveSession: the new credential is handed over
// on the live session and persisted by the agent; the old one dies at once,
// the session keeps running and the next connection uses the new one.
func TestCredentialRotationOverLiveSession(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.start()
	f.waitOnline(r.EnvironmentID)
	a.waitState(session.StateOnline)
	old := r.Credential
	rot, err := f.svc.RotateCredential(f.ctx, r.AgentID)
	if err != nil || rot.State != domain.RotationCompleted || rot.CompletedAt == nil {
		t.Fatalf("rotation %+v %v", rot, err)
	}
	cred, _ := a.store.Credential()
	if cred.Credential == old {
		t.Fatal("agent did not persist the new credential")
	}
	if _, err := f.svc.Authenticate(f.ctx, old); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("old credential still valid: %v", err)
	}
	if p, err := f.svc.Authenticate(f.ctx, cred.Credential); err != nil || p.AgentID != r.AgentID {
		t.Fatalf("new credential: %+v %v", p, err)
	}
	if !f.svc.Hub().Online(r.EnvironmentID) || f.svc.Hub().Session(r.AgentID) == nil {
		t.Fatal("rotation interrupted the session")
	}
	if ag := mustAgent(t, f, r.AgentID); ag.RotationPending {
		t.Fatal("rotation still pending")
	}
	// A reconnect authenticates with the new credential.
	f.svc.Hub().Session(r.AgentID).closeWith(1011, "drop")
	a.waitState(session.StateDisconnected)
	a.waitState(session.StateOnline)
	if strings.Contains(f.logs.String(), strings.SplitN(strings.TrimPrefix(cred.Credential, "dya_"), "_", 2)[1]) {
		t.Fatal("rotated credential logged")
	}
}

// TestRotationWhileOfflineIsDeliveredOnReconnect: a rotation requested
// while the agent is offline stays pending (old credential valid) and is
// completed when the agent reconnects.
func TestRotationWhileOfflineIsDeliveredOnReconnect(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	rot, err := f.svc.RotateCredential(f.ctx, r.AgentID)
	if err != nil || rot.State != domain.RotationPending {
		t.Fatalf("offline rotation %+v %v", rot, err)
	}
	if ag := mustAgent(t, f, r.AgentID); !ag.RotationPending {
		t.Fatal("rotation not shown as pending")
	}
	if _, err := f.svc.Authenticate(f.ctx, r.Credential); err != nil {
		t.Fatalf("old credential must stay valid while pending: %v", err)
	}
	a.start()
	f.waitOnline(r.EnvironmentID)
	f.waitEvent(events.AgentCredentialRotated, r.AgentID)
	cred, _ := a.store.Credential()
	if cred.Credential == r.Credential {
		t.Fatal("pending credential not delivered")
	}
	if _, err := f.svc.Authenticate(f.ctx, r.Credential); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("old credential valid after delivery: %v", err)
	}
	if _, err := f.svc.RotateCredential(f.ctx, "0190a6e0-0000-7000-8000-000000000404"); !errors.Is(err, domain.ErrAgentNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
}

// TestPendingCredentialCompletesRotation: an agent that persisted a new
// credential whose confirmation was lost authenticates with it, which
// completes the rotation and revokes the old credential.
func TestPendingCredentialCompletesRotation(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	if _, err := f.svc.RotateCredential(f.ctx, r.AgentID); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := storePending(f, r.AgentID)
	if err != nil || !ok {
		t.Fatal("no pending credential", err)
	}
	if p, err := f.svc.Authenticate(f.ctx, pending); err != nil || p.AgentID != r.AgentID {
		t.Fatalf("pending credential: %v", err)
	}
	if _, err := f.svc.Authenticate(f.ctx, r.Credential); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("old credential after completion: %v", err)
	}
}

func storePending(f *fixture, agentID string) (string, bool, error) {
	var sealed, id string
	err := f.db.NewRaw(`SELECT id, sealed FROM agent_credentials WHERE agent_id = ? AND state = 'pending'`, agentID).Scan(f.ctx, &id, &sealed)
	if err != nil {
		return "", false, err
	}
	b, err := f.svc.keyring.Open(sealed, sealContext(id))
	return string(b), err == nil, err
}
