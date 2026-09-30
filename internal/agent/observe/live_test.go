package observe

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

func liveMetrics(t *testing.T, s *Sampler) protocol.LiveMetricsOutput {
	t.Helper()
	out, err := s.LiveMetrics(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	o := out.(protocol.LiveMetricsOutput)
	if err := o.Validate(); err != nil {
		t.Fatalf("output invalid: %v", err)
	}
	return o
}

func liveByName(o protocol.LiveMetricsOutput) map[string]protocol.LiveContainer {
	m := map[string]protocol.LiveContainer{}
	for _, c := range o.Containers {
		m[c.Name] = c
	}
	return m
}

// TestLiveMetricsDeltasAndBaseline: metrics.live reads the host and every
// running container when asked; CPU is the change since the previous
// read (none on the first), a container that could not be read keeps its
// baseline, and after a pause longer than LiveMetricsBaselineAge (the
// manager stopped asking) a new baseline starts.
func TestLiveMetricsDeltasAndBaseline(t *testing.T) {
	eng := &fakeEngine{
		id: engine.Identity{NCPU: 4, MemTotal: 1000},
		containers: []engine.Container{
			{ID: "c1", Names: []string{"/web"}, State: "running"},
			{ID: "c2", Names: []string{"/db"}, State: "running"},
			{ID: "c3", Names: []string{"/old"}, State: "exited"},
		},
		stats: map[string]engine.Stats{
			"c1": {CPUTotalUsage: 1000, SystemCPUUsage: 10_000, OnlineCPUs: 4, MemoryUsage: 300, MemoryLimit: 1000},
			"c2": {CPUTotalUsage: 500, SystemCPUUsage: 10_000, OnlineCPUs: 4, MemoryUsage: 100, MemoryLimit: 256},
		},
	}
	fsys := procFS(statA, meminfoText, "0 0 0", "1", netA)
	s := newTestSampler(t, fsys, eng)
	clk := s.opts.Clock.(*clock.Fake)

	o := liveMetrics(t, s)
	if !o.At.Equal(clk.Now()) || o.Flags != 0 || o.Host.CPUPercent != nil || o.Host.CPUs != 2 ||
		*o.Host.MemoryUsedBytes != 2000000*1024 || *o.Host.MemoryTotalBytes != 8000000*1024 {
		t.Fatalf("first read %+v", o)
	}
	web, db := liveByName(o)["web"], liveByName(o)["db"]
	if len(o.Containers) != 2 || web.CPUPercent != nil || *web.MemoryBytes != 300 || web.MemoryLimitBytes != nil ||
		db.CPUPercent != nil || *db.MemoryLimitBytes != 256 {
		t.Fatalf("first read containers %+v", o.Containers)
	}

	// One second later: host 75% busy; web +2000 of +4000 host CPU time on
	// 4 cores = 50%; db cannot be read this time.
	clk.Advance(time.Second)
	fsys["stat"] = &fstest.MapFile{Data: []byte(statB)}
	eng.mu.Lock()
	eng.stats["c1"] = engine.Stats{CPUTotalUsage: 3000, SystemCPUUsage: 14_000, OnlineCPUs: 4, MemoryUsage: 310, MemoryLimit: 1000}
	eng.statsErr = map[string]error{"c2": &engine.Error{Code: engine.CodeTimeout, Op: "stats.stream"}}
	eng.mu.Unlock()
	o = liveMetrics(t, s)
	web = liveByName(o)["web"]
	if o.Host.CPUPercent == nil || *o.Host.CPUPercent != 75 || len(o.Containers) != 1 || *web.CPUPercent != 50 ||
		*web.MemoryBytes != 310 || o.Flags != protocol.BatchContainersTruncated {
		t.Fatalf("second read %+v", o)
	}

	// db is back and still has its baseline from the first read: +400 of
	// +8000 on 4 cores = 5%.
	clk.Advance(time.Second)
	eng.mu.Lock()
	eng.statsErr = nil
	eng.stats["c2"] = engine.Stats{CPUTotalUsage: 900, SystemCPUUsage: 18_000, OnlineCPUs: 4, MemoryUsage: 100, MemoryLimit: 256}
	eng.mu.Unlock()
	o = liveMetrics(t, s)
	if db := liveByName(o)["db"]; db.CPUPercent == nil || *db.CPUPercent != 5 || o.Flags != 0 {
		t.Fatalf("third read %+v", o)
	}

	// Nobody asked for longer than the baseline age: a new baseline, no CPU.
	clk.Advance(protocol.LiveMetricsBaselineAge + time.Second)
	fsys["stat"] = &fstest.MapFile{Data: []byte(statA)}
	o = liveMetrics(t, s)
	if o.Host.CPUPercent != nil || liveByName(o)["web"].CPUPercent != nil || liveByName(o)["web"].MemoryBytes == nil {
		t.Fatalf("read after a pause %+v", o)
	}

	// Without an Engine the host values stay and the answer is flagged.
	s.opts.Engine = func() EngineAPI { return nil }
	clk.Advance(time.Second)
	if o = liveMetrics(t, s); o.Flags != protocol.BatchEngineUnavailable || len(o.Containers) != 0 || o.Host.MemoryUsedBytes == nil {
		t.Fatalf("no Engine %+v", o)
	}
}

func TestCPUShare(t *testing.T) {
	prev := ctrCounters{cpu: 1000, sys: 10_000}
	for _, tc := range []struct {
		name string
		prev ctrCounters
		cur  engine.Stats
		cpus int
		want *float64
	}{
		{"two of eight cores", prev, engine.Stats{CPUTotalUsage: 2000, SystemCPUUsage: 14_000, OnlineCPUs: 8}, 8, f64(25)},
		{"idle", prev, engine.Stats{CPUTotalUsage: 1000, SystemCPUUsage: 14_000, OnlineCPUs: 8}, 8, f64(0)},
		{"cores unknown: the online cores", prev, engine.Stats{CPUTotalUsage: 2000, SystemCPUUsage: 14_000, OnlineCPUs: 4}, 0, f64(25)},
		{"clamped", prev, engine.Stats{CPUTotalUsage: 99_000, SystemCPUUsage: 14_000, OnlineCPUs: 4}, 4, f64(100)},
		{"no previous read", ctrCounters{}, engine.Stats{CPUTotalUsage: 2000, SystemCPUUsage: 14_000, OnlineCPUs: 4}, 4, nil},
		{"restarted", prev, engine.Stats{CPUTotalUsage: 10, SystemCPUUsage: 14_000, OnlineCPUs: 4}, 4, nil},
		{"host counter did not advance", prev, engine.Stats{CPUTotalUsage: 2000, SystemCPUUsage: 10_000, OnlineCPUs: 4}, 4, nil},
		{"no system counter", prev, engine.Stats{CPUTotalUsage: 2000, OnlineCPUs: 4}, 4, nil},
	} {
		got := cpuShare(tc.prev, tc.cur, tc.cpus)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
