package compose

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/docker/compose/v5/pkg/api"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Engines before 26 may still list a container as running right after a
// stop returned; Stop waits until the list settles so a following Start
// does not skip the container.
func TestAwaitStoppedWaitsForTheContainerList(t *testing.T) {
	fake := enginetest.Start(t, enginetest.Options{APIVersion: "1.44"})
	var calls, staleFor atomic.Int32
	staleFor.Store(3)
	fake.Handle(http.MethodGet, "/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		state := "exited"
		if calls.Add(1) <= staleFor.Load() {
			state = "running"
		}
		enginetest.JSON(w, http.StatusOK, []map[string]any{
			{"Id": "db1", "Names": []string{"/p-db-1"}, "State": state, "Labels": map[string]string{api.ProjectLabel: "p", api.ServiceLabel: "db", api.OneoffLabel: "False"}},
			{"Id": "run1", "Names": []string{"/p-db-run-1"}, "State": "running", "Labels": map[string]string{api.ProjectLabel: "p", api.ServiceLabel: "db", api.OneoffLabel: "True"}},
			{"Id": "web1", "Names": []string{"/p-web-1"}, "State": "running", "Labels": map[string]string{api.ProjectLabel: "p", api.ServiceLabel: "web", api.OneoffLabel: "False"}},
		})
	})
	a := newAdapter(t, fake)
	clk := testutil.FakeClock()
	a.clock = clk
	ctx := testutil.Context(t)

	done := make(chan error, 1)
	go func() { done <- a.awaitStopped(ctx, "p", []string{"db"}) }()
	for i := 0; i < 3; i++ {
		if err := clk.BlockUntilWaiters(ctx, 2); err != nil { // deadline + poll timer
			t.Fatal(err)
		}
		clk.Advance(listSettlePoll)
	}
	if err := <-done; err != nil {
		t.Fatalf("awaitStopped = %v", err)
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("listed %d times, want 4 (3 stale + 1 settled)", n)
	}

	// A list that never settles ends with a timeout.
	calls.Store(0)
	staleFor.Store(1 << 30)
	go func() { done <- a.awaitStopped(ctx, "p", []string{"db"}) }()
	if err := clk.BlockUntilWaiters(ctx, 2); err != nil {
		t.Fatal(err)
	}
	clk.Advance(listSettleTimeout)
	if err := <-done; engine.CodeOf(err) != engine.CodeTimeout {
		t.Fatalf("never settling list: %v", err)
	}
}
