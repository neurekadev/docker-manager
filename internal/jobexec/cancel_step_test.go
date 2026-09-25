package jobexec

import (
	"context"
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
