//go:build !race

package metrics

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestScaleBudget runs the budget's steady state (one 10 s tick of 25
// environments and 1 000 containers, a rollup pass, 20 sessions each
// refreshing a host and a container chart) over 30 minutes of history and
// requires every cycle to finish in a fraction of the 10 s interval. The
// bound is generous (shared CI runners); the measured times are in
// docs/architecture/metrics.md and the metrics-budget job's benchmarks.
func TestScaleBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	clk := testutil.FakeClock()
	s := openTest(t, clk)
	ctx := context.Background()
	at := clk.Now().Truncate(time.Hour)
	for k := range 180 {
		t0 := at.Add(time.Duration(k) * 10 * time.Second)
		clk.Set(t0)
		ingestTick(t, s, t0)
	}
	at = at.Add(30 * time.Minute)
	var worst time.Duration
	for range 6 {
		at = at.Add(10 * time.Second)
		clk.Set(at)
		start := time.Now()
		var wg sync.WaitGroup
		wg.Go(func() {
			ingestTick(t, s, at)
			if err := s.Rollup(ctx); err != nil {
				t.Error(err)
			}
		})
		for u := range budgetSessions {
			wg.Go(func() {
				envID := fmt.Sprintf("env-%02d", u%budgetEnvs)
				if _, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: envID, Kind: domain.MetricHost, From: at.Add(-time.Hour), To: at}); err != nil {
					t.Error(err)
				}
				if _, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: envID, Kind: domain.MetricContainer, Name: "stack-01-service-05-1",
					From: at.Add(-time.Hour), To: at}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		worst = max(worst, time.Since(start))
	}
	t.Logf("worst 10 s cycle (1 000 containers ingested, 40 dashboard queries): %s", worst)
	if worst > 5*time.Second {
		t.Fatalf("a 10 s budget cycle took %s", worst)
	}
}
