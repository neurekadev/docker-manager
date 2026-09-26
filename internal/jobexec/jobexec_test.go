package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

type memJournal struct {
	saves []State
	fail  bool
}

func (m *memJournal) Save(_ context.Context, st *State) error {
	if m.fail {
		return errors.New("disk full")
	}
	m.saves = append(m.saves, st.Clone())
	return nil
}

type recorder struct{ progress []protocol.ProgressPayload }

func (r *recorder) Progress(_ context.Context, _ *State, p protocol.ProgressPayload) {
	r.progress = append(r.progress, p)
}

// backupExec simulates backup.run: effects are appended to *log.
func backupExec(log *[]string, failStep string, compFail bool) Executor {
	stepFn := func(name string) StepFunc {
		return func(ctx context.Context, sc *StepContext) error {
			switch name {
			case "stop_containers":
				if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, []string{"web"}); err != nil {
					return err
				}
			case "start_containers":
				if err := sc.ReleaseCompensation(ctx, jobspec.CompStartContainers); err != nil {
					return err
				}
			}
			*log = append(*log, name)
			if name == failStep {
				return errors.New("boom")
			}
			return nil
		}
	}
	steps := map[string]StepFunc{}
	spec, _ := jobspec.Lookup(jobspec.BackupRun)
	for _, s := range spec.Steps {
		steps[s.Name] = stepFn(s.Name)
	}
	return Executor{Kind: jobspec.BackupRun, Steps: steps, Compensations: map[string]CompensationFunc{
		jobspec.CompStartContainers: func(_ context.Context, args json.RawMessage) error {
			*log = append(*log, "compensate:"+string(args))
			if compFail {
				return errors.New("engine unreachable")
			}
			return nil
		},
	}}
}

func newState() *State {
	return &State{JobID: "j1", Attempt: 1, FencingToken: 1, Kind: jobspec.BackupRun, Input: json.RawMessage(`{}`)}
}

func TestRunSucceedsAndJournalsEveryStep(t *testing.T) {
	var log []string
	j, rec := &memJournal{}, &recorder{}
	st := newState()
	res, err := Run(context.Background(), backupExec(&log, "", false), st, Options{Journal: j, Reporter: rec})
	if err != nil || res.Outcome != OutcomeSucceeded {
		t.Fatalf("res %+v err %v", res, err)
	}
	want := []string{"prepare", "stop_containers", "snapshot", "start_containers", "record"}
	if !slices.Equal(log, want) || !slices.Equal(res.CompletedSteps, want) {
		t.Fatalf("log %v completed %v", log, res.CompletedSteps)
	}
	// Each step is journaled in flight before it runs.
	var inflight []string
	for _, s := range j.saves {
		if s.StepInFlight && (len(inflight) == 0 || inflight[len(inflight)-1] != s.CurrentStep) {
			inflight = append(inflight, s.CurrentStep)
		}
	}
	if !slices.Equal(inflight, want) {
		t.Fatalf("in-flight journal order %v", inflight)
	}
	if st.Outcome == nil || st.Outcome.Outcome != OutcomeSucceeded || j.saves[len(j.saves)-1].Outcome == nil {
		t.Fatal("outcome not journaled")
	}
	if len(rec.progress) < len(want) {
		t.Fatalf("progress = %v", rec.progress)
	}
}

func TestRunResumeSkipsCompletedSteps(t *testing.T) {
	var log []string
	st := newState()
	st.Completed = []string{"prepare", "stop_containers"}
	res, err := Run(context.Background(), backupExec(&log, "", false), st, Options{Journal: &memJournal{}})
	if err != nil || res.Outcome != OutcomeSucceeded {
		t.Fatal(res, err)
	}
	if !slices.Equal(log, []string{"snapshot", "start_containers", "record"}) {
		t.Fatalf("resumed run executed %v", log)
	}
}

func TestCancellationOnlyAtSafePointsAndCompensates(t *testing.T) {
	var log []string
	cancel := false
	exec := backupExec(&log, "", false)
	// Request cancellation while stop_containers runs: honored before
	// snapshot (a safe point), compensation restarts the containers.
	orig := exec.Steps["stop_containers"]
	exec.Steps["stop_containers"] = func(ctx context.Context, sc *StepContext) error {
		cancel = true
		return orig(ctx, sc)
	}
	res, err := Run(context.Background(), exec, newState(), Options{Journal: &memJournal{}, CancelRequested: func() bool { return cancel }})
	if err != nil || res.Outcome != OutcomeCancelled || res.ErrorClass != domain.ErrorCancelled {
		t.Fatalf("res %+v %v", res, err)
	}
	if !slices.Equal(log, []string{"prepare", "stop_containers", `compensate:["web"]`}) {
		t.Fatalf("log %v", log)
	}
	if len(res.Compensations) != 1 || !res.Compensations[0].Done {
		t.Fatalf("compensations %+v", res.Compensations)
	}

	// start_containers is NOT a safe point: a cancel arriving during the
	// snapshot is ignored until record (safe point) - the containers are
	// started normally first.
	log, cancel = nil, false
	exec = backupExec(&log, "", false)
	orig = exec.Steps["snapshot"]
	exec.Steps["snapshot"] = func(ctx context.Context, sc *StepContext) error {
		cancel = true
		return orig(ctx, sc)
	}
	res, _ = Run(context.Background(), exec, newState(), Options{Journal: &memJournal{}, CancelRequested: func() bool { return cancel }})
	if res.Outcome != OutcomeCancelled || !slices.Equal(log, []string{"prepare", "stop_containers", "snapshot", "start_containers"}) {
		t.Fatalf("res %+v log %v", res, log)
	}
}

func TestFailureRunsCompensationAndReportsCompensationFailure(t *testing.T) {
	var log []string
	res, err := Run(context.Background(), backupExec(&log, "snapshot", false), newState(), Options{Journal: &memJournal{}})
	if err != nil || res.Outcome != OutcomeFailed || res.ErrorClass != domain.ErrorStepFailed || res.Recovery == "" {
		t.Fatalf("res %+v", res)
	}
	if log[len(log)-1] != `compensate:["web"]` {
		t.Fatalf("compensation not run: %v", log)
	}

	log = nil
	res, _ = Run(context.Background(), backupExec(&log, "snapshot", true), newState(), Options{Journal: &memJournal{}})
	if res.ErrorClass != domain.ErrorStepFailed || !strings.Contains(res.Recovery, "start_containers") {
		t.Fatalf("compensation failure not surfaced: %+v", res)
	}
	log = nil
	res, _ = Run(context.Background(), backupExec(&log, "record", false), newState(), Options{Journal: &memJournal{}})
	if len(res.Compensations) != 0 {
		t.Fatalf("released compensation ran again: %+v %v", res, log)
	}
}

func TestPartialWhenItemsFail(t *testing.T) {
	var log []string
	exec := backupExec(&log, "", false)
	exec.Steps["record"] = func(ctx context.Context, sc *StepContext) error {
		sc.Item(ctx, "vol-a", domain.ItemSucceeded, "")
		sc.Item(ctx, "vol-b", domain.ItemFailed, "permission denied")
		return nil
	}
	res, _ := Run(context.Background(), exec, newState(), Options{Journal: &memJournal{}})
	if res.Outcome != OutcomePartial || len(res.Items) != 2 {
		t.Fatalf("res %+v", res)
	}
}

func TestJournalFailureFailsBeforeStepRuns(t *testing.T) {
	var log []string
	res, _ := Run(context.Background(), backupExec(&log, "", false), newState(), Options{Journal: &memJournal{fail: true}})
	if res.Outcome != OutcomeFailed || res.ErrorClass != domain.ErrorInternal || len(log) != 0 {
		t.Fatalf("res %+v log %v", res, log)
	}
}

func TestAbandonedOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var log []string
	exec := backupExec(&log, "", false)
	exec.Steps["snapshot"] = func(ctx context.Context, _ *StepContext) error {
		cancel()
		return ctx.Err()
	}
	st := newState()
	if _, err := Run(ctx, exec, st, Options{Journal: &memJournal{}}); !errors.Is(err, ErrAbandoned) {
		t.Fatalf("err = %v", err)
	}
	if !st.StepInFlight || st.CurrentStep != "snapshot" || st.Outcome != nil {
		t.Fatalf("state not left in flight: %+v", st)
	}
}

func TestRecover(t *testing.T) {
	cases := []struct {
		name          string
		st            State
		wantResumable bool
		wantClass     string
		wantComp      bool
	}{
		{"accepted not started", State{}, true, "", false},
		{"between steps", State{Completed: []string{"prepare"}}, true, "", false},
		{"idempotent step in flight", State{Completed: []string{}, CurrentStep: "prepare", StepInFlight: true}, true, "", false},
		{"non-idempotent in flight with compensation", State{Completed: []string{"prepare", "stop_containers"}, CurrentStep: "snapshot", StepInFlight: true,
			Compensations: []Compensation{{Name: jobspec.CompStartContainers, Args: json.RawMessage(`["web"]`)}}}, false, domain.ErrorUnknownOutcome, true},
		{"idempotent in flight but compensated", State{Completed: []string{"prepare"}, CurrentStep: "stop_containers", StepInFlight: true,
			Compensations: []Compensation{{Name: jobspec.CompStartContainers}}}, false, domain.ErrorExecutorRestarted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var log []string
			exec := backupExec(&log, "", false)
			st := c.st
			st.JobID, st.Attempt, st.Kind = "j", 1, jobspec.BackupRun
			res, err := Recover(context.Background(), &exec, &st, Options{Journal: &memJournal{}})
			if err != nil || res.Outcome != OutcomeInterrupted {
				t.Fatalf("res %+v err %v", res, err)
			}
			if res.Resumable != c.wantResumable || res.ErrorClass != c.wantClass {
				t.Fatalf("resumable=%v class=%q, want %v %q (%+v)", res.Resumable, res.ErrorClass, c.wantResumable, c.wantClass, res)
			}
			if (len(log) == 1) != c.wantComp {
				t.Fatalf("compensation log %v", log)
			}
			if !c.wantResumable && res.Recovery == "" {
				t.Fatal("no recovery guidance")
			}
			if st.Outcome == nil {
				t.Fatal("outcome not journaled")
			}
			// Recovering again is a no-op returning the same outcome.
			again, _ := Recover(context.Background(), &exec, &st, Options{Journal: &memJournal{}})
			if again.Outcome != res.Outcome || len(log) > 1 {
				t.Fatal("second recover changed state")
			}
		})
	}
	// Without an executor, compensations cannot run: reported, not resumable.
	st := State{JobID: "j", Attempt: 1, Kind: jobspec.BackupRun, Compensations: []Compensation{{Name: jobspec.CompStartContainers}}}
	res, _ := Recover(context.Background(), nil, &st, Options{Journal: &memJournal{}})
	if res.Resumable || res.ErrorClass != domain.ErrorCompensationFailed || !strings.Contains(res.Recovery, "start_containers") {
		t.Fatalf("res %+v", res)
	}
}

func TestExecutorValidate(t *testing.T) {
	var log []string
	e := backupExec(&log, "", false)
	if err := e.Validate(domain.ExecutorAgent); err != nil {
		t.Fatal(err)
	}
	if err := e.Validate(domain.ExecutorManager); err == nil {
		t.Fatal("wrong executor side accepted")
	}
	delete(e.Steps, "snapshot")
	e.Steps["extra"] = e.Steps["prepare"]
	delete(e.Compensations, jobspec.CompStartContainers)
	err := e.Validate(domain.ExecutorAgent)
	for _, want := range []string{"snapshot", "extra", "start_containers"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %v lacks %q", err, want)
		}
	}
	if err := (Executor{Kind: "nope.kind"}).Validate(domain.ExecutorAgent); err == nil {
		t.Fatal("unknown kind accepted")
	}
	sc := &StepContext{Kind: jobspec.BackupRun, st: newState(), opts: Options{Journal: &memJournal{}}}
	if err := sc.AddCompensation(context.Background(), "undeclared", nil); err == nil {
		t.Fatal("undeclared compensation accepted")
	}
}

type classedErr struct{ class, recovery string }

func (e classedErr) Error() string      { return "registry says slow down" }
func (e classedErr) ErrorClass() string { return e.class }
func (e classedErr) Recovery() string   { return e.recovery }

// TestClassedStepErrors: a step error with its own class (an Engine or
// registry code, #6) becomes the job's error class and recovery; without a
// recovery the step's guidance stays; compensations still run.
func TestClassedStepErrors(t *testing.T) {
	var log []string
	exec := backupExec(&log, "", false)
	exec.Steps["snapshot"] = func(context.Context, *StepContext) error {
		return fmt.Errorf("wrapped: %w", classedErr{class: "rate_limited", recovery: "Wait before retrying."})
	}
	res, _ := Run(context.Background(), exec, newState(), Options{Journal: &memJournal{}})
	if res.Outcome != OutcomeFailed || res.ErrorClass != "rate_limited" || res.Recovery != "Wait before retrying." ||
		!strings.Contains(res.Message, "registry says slow down") {
		t.Fatalf("res %+v", res)
	}
	if log[len(log)-1] != `compensate:["web"]` {
		t.Fatalf("compensation not run: %v", log)
	}
	exec.Steps["snapshot"] = func(context.Context, *StepContext) error { return classedErr{class: "not_found"} }
	res, _ = Run(context.Background(), exec, newState(), Options{Journal: &memJournal{}})
	spec, _ := jobspec.Lookup(jobspec.BackupRun)
	st, _ := spec.Step("snapshot")
	if res.ErrorClass != "not_found" || res.Recovery != st.Recovery {
		t.Fatalf("res %+v", res)
	}
}
