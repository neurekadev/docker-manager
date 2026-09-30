package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
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

// TestWatchCancelEndsContextOnRequest: the watched context ends at a poll
// after cancellation is requested; the parent stays untouched and stop
// ends the watch.
func TestWatchCancelEndsContextOnRequest(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC))
	var requested atomic.Bool
	sc := &StepContext{opts: Options{CancelRequested: requested.Load}}
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	ctx, stop := sc.WatchCancel(parent, clk, DefaultCancelPoll)
	defer stop()
	clk.Advance(DefaultCancelPoll)
	if ctx.Err() != nil {
		t.Fatal("ended without a request")
	}
	requested.Store(true)
	clk.Advance(DefaultCancelPoll)
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the context did not end after the request")
	}
	if !errors.Is(ctx.Err(), context.Canceled) || parent.Err() != nil {
		t.Fatalf("ctx %v, parent %v", ctx.Err(), parent.Err())
	}
	stop()
	if clk.Waiters() != 0 {
		t.Errorf("the watch left %d tickers", clk.Waiters())
	}
}

// TestWatchCancelStopWithoutRequest: polls without a request leave the
// context running; stop (safe to call twice) ends it and the watch.
func TestWatchCancelStopWithoutRequest(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC))
	sc := &StepContext{opts: Options{CancelRequested: func() bool { return false }}}
	ctx, stop := sc.WatchCancel(context.Background(), clk, DefaultCancelPoll)
	clk.Advance(3 * DefaultCancelPoll)
	if ctx.Err() != nil {
		t.Fatalf("ended without a request: %v", ctx.Err())
	}
	stop()
	stop() // idempotent
	if ctx.Err() == nil || clk.Waiters() != 0 {
		t.Fatalf("ctx %v, waiters %d", ctx.Err(), clk.Waiters())
	}
}
