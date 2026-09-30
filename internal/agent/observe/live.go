package observe

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Live metrics (#5): metrics.live answers the current CPU and memory of
// the host (procfs) and of every running container (one-shot stats
// through the Moby adapter), read when the manager asks: about once a
// second while a browser is watching, never otherwise. Nothing is
// buffered or stored. CPU is the change since the previous metrics.live
// read; a previous read older than protocol.LiveMetricsBaselineAge (the
// manager stopped asking) is dropped, so the first answer after a pause
// starts a new baseline and has memory but no CPU. The sampling state is
// separate from the 10 s sampler's, so neither disturbs the other's
// deltas. docs/internal/architecture/metrics.md, "Live metrics".

// liveState is the metrics.live sampling state. Requests run on session
// goroutines: its lock serializes them (the manager sends one at a time).
type liveState struct {
	mu sync.Mutex
	// at is when the previous read started; cpu and ctr are its counters
	// (host /proc/stat and per container ID).
	at  time.Time
	cpu *cpuTimes
	ctr map[string]ctrCounters
	// resume is the first container not reached by the previous read (the
	// next one starts there).
	resume string
}

// LiveMetrics serves the metrics.live request (its input is ignored).
func (s *Sampler) LiveMetrics(ctx context.Context, _ json.RawMessage) (any, error) {
	return s.sampleLive(ctx), nil
}

// sampleLive reads the current host and container CPU and memory.
func (s *Sampler) sampleLive(ctx context.Context) protocol.LiveMetricsOutput {
	l := &s.live
	l.mu.Lock()
	defer l.mu.Unlock()
	now := s.opts.Clock.Now()
	if l.at.IsZero() || now.Sub(l.at) > protocol.LiveMetricsBaselineAge {
		// Nobody asked for a while: CPU over that whole pause is not
		// "current"; start a new baseline.
		l.cpu, l.ctr = nil, nil
	}
	l.at = now
	out := protocol.LiveMetricsOutput{At: now.UTC()}
	if cpu, cpus, err := readCPU(s.opts.Proc); err == nil {
		out.Host.CPUs = cpus
		out.Host.CPUPercent = hostCPUPercent(l.cpu, cpu)
		l.cpu = &cpu
	} else {
		l.cpu = nil
	}
	var memTotal int64
	if m, err := readMem(s.opts.Proc); err == nil {
		out.Host.MemoryTotalBytes, out.Host.MemoryUsedBytes = i64(m.total), i64(m.total-m.available)
		memTotal = m.total
	}
	out.Containers, out.Flags = s.liveContainers(ctx, out.Host.CPUs, memTotal)
	return out
}

// liveContainers reads every running container's CPU and memory within
// protocol.LiveMetricsBudget (l.mu held). Containers not reached in time
// are left out, flagged, keep their counters and are read first next time.
func (s *Sampler) liveContainers(ctx context.Context, hostCPUs int, memTotal int64) ([]protocol.LiveContainer, int) {
	l := &s.live
	eng := s.opts.Engine()
	if eng == nil {
		l.ctr = nil
		return nil, protocol.BatchEngineUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.LiveMetricsBudget)
	defer cancel()
	list, flags, err := runningContainers(ctx, eng)
	if err != nil {
		return nil, protocol.BatchEngineUnavailable
	}
	id := eng.Identity()
	cpus := id.NCPU
	if cpus <= 0 {
		cpus = hostCPUs
	}
	if id.MemTotal > 0 {
		memTotal = id.MemTotal
	}
	results, errs, stopped := readStats(ctx, eng, list, startAt(list, l.resume), s.opts.StatsConcurrency)
	l.resume = stopped
	out := make([]protocol.LiveContainer, 0, len(list))
	seen := make(map[string]ctrCounters, len(list))
	for i, c := range list {
		st := results[i]
		p, had := l.ctr[c.ID]
		if st == nil {
			if missing(errs[i]) {
				flags |= protocol.BatchContainersTruncated
				if had {
					seen[c.ID] = p
				}
			}
			continue
		}
		out = append(out, protocol.LiveContainer{Name: containerName(c), ID: c.ID, CPUPercent: cpuShare(p, *st, cpus),
			MemoryBytes: i64(clampInt64(st.MemoryUsage)), MemoryLimitBytes: memoryLimit(*st, memTotal)})
		seen[c.ID] = ctrCounters{cpu: st.CPUTotalUsage, sys: st.SystemCPUUsage}
	}
	l.ctr = seen
	return out, flags
}
