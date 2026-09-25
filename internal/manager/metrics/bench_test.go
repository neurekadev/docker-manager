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

// Scale budget (#5, docs/architecture/metrics.md "Scale budget"): 25
// environments, 1 000 containers in total, 20 concurrent UI sessions on a
// 2 vCPU / 2 GB manager. One 10 s tick is 25 host samples, 25 disk samples
// and 1 000 container samples, each environment ingested in its own
// transaction as the collector does.

const (
	budgetEnvs       = 25
	budgetContainers = 1000
	budgetSessions   = 20
)

func budgetTick(at time.Time) map[string][]domain.MetricSample {
	out := make(map[string][]domain.MetricSample, budgetEnvs)
	per := budgetContainers / budgetEnvs
	for e := range budgetEnvs {
		smp := domain.MetricSample{At: at, Host: &domain.HostValues{CPUPercent: f(12.5), MemoryUsedBytes: i(3 << 30), MemoryTotalBytes: i(8 << 30),
			Load1: f(0.4), Load5: f(0.3), Load15: f(0.2), NetworkRxBPS: f(12345), NetworkTxBPS: f(2345)},
			Disks: []domain.DiskValues{{Mount: "docker", UsedBytes: 40 << 30, TotalBytes: 100 << 30}}}
		for c := range per {
			smp.Containers = append(smp.Containers, domain.ContainerValues{Name: fmt.Sprintf("stack-%02d-service-%02d-1", c/4, c),
				CPUPercent: f(float64(c%7) * 0.37), MemoryBytes: i(int64(50+c) << 20), NetworkRxBPS: f(1024), NetworkTxBPS: f(512),
				BlockReadBPS: f(0), BlockWriteBPS: f(4096), PIDs: i(12)})
		}
		out[fmt.Sprintf("env-%02d", e)] = []domain.MetricSample{smp}
	}
	return out
}

func ingestTick(b testing.TB, s *Store, at time.Time) {
	ctx := context.Background()
	for envID, smp := range budgetTick(at) {
		if _, err := s.Ingest(ctx, envID, smp, &domain.MetricCursor{Epoch: "e", LastSeq: uint64(at.Unix())}); err != nil { //nolint:gosec // G115: test
			b.Fatal(err)
		}
	}
}

// BenchmarkIngestTick measures one full tick (1 000 containers). The budget
// is one tick per 10 s: ns/op divided by 1e10 is the share of one core.
func BenchmarkIngestTick(b *testing.B) {
	clk := testutil.FakeClock()
	s := openTest(b, clk)
	at := clk.Now().Truncate(10 * time.Second)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		at = at.Add(10 * time.Second)
		clk.Set(at)
		ingestTick(b, s, at)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e10*100, "%core/tick")
}

// BenchmarkLoadBudget runs the budget's steady state per 10 s: one ingest
// tick, one rollup pass and 20 dashboard sessions refreshing a host chart
// (1 h at 10 s) and a container chart (1 h) at the same time, over a store
// already holding 6 h of samples.
func BenchmarkLoadBudget(b *testing.B) {
	clk := testutil.FakeClock()
	s := openTest(b, clk)
	at := clk.Now().Truncate(time.Hour)
	for k := range 6 * 360 {
		t := at.Add(time.Duration(k) * 10 * time.Second)
		clk.Set(t)
		ingestTick(b, s, t)
	}
	ctx := context.Background()
	if err := s.Rollup(ctx); err != nil {
		b.Fatal(err)
	}
	at = at.Add(6 * time.Hour)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		at = at.Add(10 * time.Second)
		clk.Set(at)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ingestTick(b, s, at)
			if err := s.Rollup(ctx); err != nil {
				b.Error(err)
			}
		}()
		for u := range budgetSessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				envID := fmt.Sprintf("env-%02d", u%budgetEnvs)
				if _, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: envID, Kind: domain.MetricHost, From: at.Add(-time.Hour), To: at}); err != nil {
					b.Error(err)
				}
				if _, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: envID, Kind: domain.MetricContainer, Name: "stack-01-service-05-1",
					From: at.Add(-time.Hour), To: at}); err != nil {
					b.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e10*100, "%core/10s")
}

// BenchmarkQueryRanges measures single dashboard queries over a store with
// 24 h of one environment's host and 40 containers (per-series index range
// scans: the other environments do not change the cost).
func BenchmarkQueryRanges(b *testing.B) {
	clk := testutil.FakeClock()
	s := openTest(b, clk)
	ctx := context.Background()
	start := clk.Now().Truncate(time.Hour)
	var batch []domain.MetricSample
	for k := range 8640 {
		t := start.Add(time.Duration(k) * 10 * time.Second)
		batch = append(batch, budgetTick(t)["env-00"]...)
		if len(batch) == 360 {
			clk.Set(t)
			if _, err := s.Ingest(ctx, "env-00", batch, nil); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	now := start.Add(24 * time.Hour)
	clk.Set(now)
	if err := s.Rollup(ctx); err != nil {
		b.Fatal(err)
	}
	for _, span := range []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour} {
		b.Run(span.String(), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				r, err := s.Query(ctx, domain.MetricQuery{EnvironmentID: "env-00", Kind: domain.MetricHost, From: now.Add(-span), To: now})
				if err != nil || r.Series[0].Values[len(r.Series[0].Values)-2] == nil {
					b.Fatalf("%v", err)
				}
			}
		})
	}
}

// BenchmarkStorageFootprint measures the stored bytes per sample row of
// each level for containers (the dominant series) and reports them with
// the projected size for the budget.
func BenchmarkStorageFootprint(b *testing.B) {
	for n := 0; n < b.N; n++ {
		clk := testutil.FakeClock()
		s := openTest(b, clk)
		ctx := context.Background()
		start := clk.Now().Truncate(time.Hour)
		base, _ := s.Size(ctx)
		// 2 h of 1 000 containers in 25 environments.
		for k := range 720 {
			t := start.Add(time.Duration(k) * 10 * time.Second)
			clk.Set(t)
			ingestTick(b, s, t)
		}
		raw, _ := s.Size(ctx)
		clk.Set(start.Add(2*time.Hour + time.Minute))
		if err := s.Rollup(ctx); err != nil {
			b.Fatal(err)
		}
		rolled, _ := s.Size(ctx)
		st, _ := s.Stats(ctx)
		rows := st.Rows["container_raw"] + st.Rows["host_raw"] + st.Rows["disk_raw"]
		rollRows := st.Rows["container_1m"] + st.Rows["container_15m"] + st.Rows["host_1m"] + st.Rows["host_15m"] + st.Rows["disk_1m"] + st.Rows["disk_15m"]
		perRaw := float64(raw-base) / float64(rows)
		perRoll := float64(rolled-raw) / float64(rollRows)
		b.ReportMetric(perRaw, "B/raw-row")
		b.ReportMetric(perRoll, "B/rollup-row")
		// Budget: (1000 containers + 25 hosts + 25 disks) series, raw 24 h
		// (8640 rows), 1 min 7 d (10080), 15 min 90 d (8640).
		series := float64(budgetContainers + 2*budgetEnvs)
		projected := series * (8640*perRaw + (10080+8640)*perRoll)
		b.ReportMetric(projected/(1<<20), "MiB-projected")
	}
}
