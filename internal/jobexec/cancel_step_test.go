package jobexec

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
)

// TestStepCancelledEndsCancelled: a step that stops early on request
// (ErrStepCancelled, e.g. an aborted build) ends the attempt cancelled,
// not failed.
func TestStepCancelledEndsCancelled(t *testing.T) {
	requested := false
	ran := false
	exec := Executor{Kind: jobspec.ImageBuild, Steps: map[string]StepFunc{
		"fetch_context": func(context.Context, *StepContext) error { return nil },
		"build": func(_ context.Context, sc *StepContext) error {
			ran = true
			requested = true // the user cancels while the build runs
			if !sc.CancelRequested() {
				t.Error("cancellation not visible to the step")
			}
			return fmt.Errorf("build aborted: %w", ErrStepCancelled)
		},
	}}
	j := &memJournal{}
	st := &State{JobID: "j", Attempt: 1, Kind: jobspec.ImageBuild}
	res, err := Run(context.Background(), exec, st, Options{Journal: j, CancelRequested: func() bool { return requested }})
	if err != nil {
		t.Fatal(err)
	}
	if !ran || res.Outcome != OutcomeCancelled || res.ErrorClass != domain.ErrorCancelled || len(res.CompletedSteps) != 1 ||
		res.Message != "cancelled during step build" {
		t.Fatalf("%+v", res)
	}
}

// TestStepInterruptedEndsInterrupted: a step that gives up on a lost party
// (ErrStepInterrupted, e.g. a migration's agent stayed disconnected) ends
// the attempt interrupted with agent_offline and runs the compensations.
func TestStepInterruptedEndsInterrupted(t *testing.T) {
	compensated := false
	exec := Executor{Kind: jobspec.StackMigrate, Steps: map[string]StepFunc{
		"prepare": func(context.Context, *StepContext) error { return nil },
		"stop_source": func(ctx context.Context, sc *StepContext) error {
			return sc.AddCompensation(ctx, jobspec.CompStartSource, map[string]any{"services": []string{"web"}})
		},
		"transfer": func(context.Context, *StepContext) error {
			return fmt.Errorf("the source agent disconnected: %w", ErrStepInterrupted)
		},
		"deploy_destination": func(context.Context, *StepContext) error { t.Error("ran after the interruption"); return nil },
		"finalize":           func(context.Context, *StepContext) error { return nil },
	}, Compensations: map[string]CompensationFunc{
		jobspec.CompStartSource: func(context.Context, json.RawMessage) error { compensated = true; return nil },
	}}
	st := &State{JobID: "j", Attempt: 1, Kind: jobspec.StackMigrate}
	res, err := Run(context.Background(), exec, st, Options{Journal: &memJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInterrupted || res.ErrorClass != domain.ErrorAgentOffline || res.InterruptedStep != "transfer" ||
		!compensated || res.Resumable {
		t.Fatalf("%+v", res)
	}
}
