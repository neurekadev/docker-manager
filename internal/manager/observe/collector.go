package observe

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// unsupportedBackoff is how long an agent that does not serve host.metrics
// is skipped.
const unsupportedBackoff = 5 * time.Minute

// requestError is the agent error shape (agents.RequestError) without
// importing the agents package.
type requestError interface {
	error
	AgentCode() string
}

// nextRound is the collector's next round after now: a fifth of the
// interval after each interval boundary, when the agents' batches of that
// slot are ready (agents sample on the boundaries; a tick reads one-shot
// stats and takes well under a second), so new samples are stored within
// about 2 s instead of up to a whole interval later.
func nextRound(now time.Time, iv time.Duration) time.Time {
	next := now.Truncate(iv).Add(iv / 5)
	if !next.After(now) {
		next = next.Add(iv)
	}
	return next
}

func (s *Service) runCollector(ctx context.Context) {
	clk := s.opts.Clock
	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		now := clk.Now()
		t := clk.NewTimer(nextRound(now, s.opts.Interval).Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		now = clk.Now()
		for _, env := range s.environments() {
			s.mu.Lock()
			busy := s.inflight[env] || now.Before(s.skipUntil[env])
			if !busy {
				s.inflight[env] = true
			}
			s.mu.Unlock()
			if busy {
				continue // the previous fetch is still running: bounded under load
			}
			select {
			case sem <- struct{}{}:
			default:
				// Every worker is busy: skip this environment this round
				// (its agent keeps buffering; the next round catches up).
				s.mu.Lock()
				delete(s.inflight, env)
				s.mu.Unlock()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					<-sem
					s.mu.Lock()
					delete(s.inflight, env)
					s.mu.Unlock()
				}()
				if _, err := s.Collect(ctx, env); err != nil && ctx.Err() == nil {
					s.log.Debug("metrics collection failed", "environment_id", env, "error", err)
				}
			}()
		}
	}
}

// CollectResult reports one collection.
type CollectResult struct {
	Batches  int
	Inserted int
	Skew     time.Duration
	Epoch    string
}

// Collect fetches an environment's buffered sample batches after its cursor
// and stores them. Timestamps are shifted by the agent's clock offset when
// it exceeds the tolerance and clamped to the receive time; the cursor is
// advanced in the same transaction as the samples, and the (series, slot)
// key ignores anything delivered twice.
func (s *Service) Collect(ctx context.Context, env string) (CollectResult, error) {
	var res CollectResult
	cur, err := s.cursor(ctx, env)
	if err != nil {
		return res, err
	}
	for range maxPagesPerFetch {
		sent := s.opts.Clock.Now()
		raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqHostMetrics,
			protocol.HostMetricsInput{Epoch: cur.c.Epoch, AfterSeq: cur.c.LastSeq}, s.opts.FetchTimeout)
		recv := s.opts.Clock.Now()
		if err != nil {
			var re requestError
			if errors.As(err, &re) && re.AgentCode() == protocol.CodeUnsupportedRequest {
				s.mu.Lock()
				s.skipUntil[env] = recv.Add(unsupportedBackoff)
				s.mu.Unlock()
			}
			return res, err
		}
		var out protocol.HostMetricsOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			return res, err
		}
		if err := out.Validate(); err != nil {
			return res, err
		}
		next := cur.c
		if out.Epoch != next.Epoch {
			// A new agent process: its buffer starts at seq 1.
			next = domain.MetricCursor{Epoch: out.Epoch}
			cur.skewed = false
		}
		// The agent's clock at the midpoint of the request.
		est := sent.Add(recv.Sub(sent) / 2).Sub(out.Now)
		switch {
		case abs(est) <= s.opts.SkewTolerance:
			if cur.skewed || next.Skew != 0 {
				s.log.Info("agent clock back within tolerance", "environment_id", env)
			}
			next.Skew, cur.skewed = 0, false
		case abs(est-next.Skew) > s.opts.SkewTolerance:
			s.log.Warn("agent clock differs from the manager's; correcting sample timestamps", "environment_id", env,
				"skew", est.Round(time.Millisecond).String())
			next.Skew, cur.skewed = est, true
		}
		samples := make([]domain.MetricSample, 0, len(out.Batches))
		for _, b := range out.Batches {
			samples = append(samples, sampleFrom(b, next.Skew, recv))
			next.LastSeq = b.Seq
		}
		ir, err := s.opts.Store.Ingest(ctx, env, samples, &next)
		if err != nil {
			return res, err
		}
		s.mu.Lock()
		cur.c = next
		if n := len(out.Batches); n > 0 {
			last := out.Batches[n-1]
			s.host[env] = HostExtra{At: samples[n-1].At, CPUs: last.Host.CPUs, UptimeSeconds: last.Host.UptimeSeconds, NetworkScope: last.Host.NetworkScope}
		}
		s.mu.Unlock()
		res.Batches += len(out.Batches)
		res.Inserted += ir.Inserted
		res.Skew, res.Epoch = next.Skew, next.Epoch
		s.publishSampled(env, ir.Host, ir.Containers)
		if !out.More || len(out.Batches) == 0 {
			break
		}
	}
	return res, nil
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// cursor returns the cached cursor, loading it from the store once.
func (s *Service) cursor(ctx context.Context, env string) (*envCursor, error) {
	s.mu.Lock()
	c := s.cursors[env]
	if c == nil {
		c = &envCursor{}
		s.cursors[env] = c
	}
	loaded := c.loaded
	s.mu.Unlock()
	if loaded {
		return c, nil
	}
	stored, ok, err := s.opts.Store.Cursor(ctx, env)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if ok {
		c.c = stored
		c.skewed = stored.Skew != 0
	}
	c.loaded = true
	s.mu.Unlock()
	return c, nil
}

// sampleFrom converts an agent batch: skew-corrected and clamped to the
// receive time (a sample from the future is impossible).
func sampleFrom(b protocol.MetricBatch, skew time.Duration, recv time.Time) domain.MetricSample {
	at := b.At.Add(skew)
	flags := 0
	if skew != 0 {
		flags |= domain.SampleSkewCorrected
	}
	if at.After(recv) {
		at, flags = recv, flags|domain.SampleClamped
	}
	if b.Flags&protocol.BatchContainersTruncated != 0 {
		flags |= domain.SampleContainersIncomplete
	}
	if b.Flags&protocol.BatchEngineUnavailable != 0 {
		flags |= domain.SampleEngineUnavailable
	}
	h := b.Host
	out := domain.MetricSample{At: at.UTC(), Flags: flags, Host: &domain.HostValues{CPUPercent: h.CPUPercent, IOWaitPercent: h.IOWaitPercent,
		MemoryUsedBytes:  h.MemoryUsedBytes,
		MemoryTotalBytes: h.MemoryTotalBytes, MemoryCacheBytes: h.MemoryCacheBytes, MemoryZFSARCBytes: h.MemoryZFSARCBytes,
		SwapUsedBytes: h.SwapUsedBytes, SwapTotalBytes: h.SwapTotalBytes, Load1: h.Load1, Load5: h.Load5, Load15: h.Load15,
		NetworkRxBPS: h.NetworkRxBytesPerSecond, NetworkTxBPS: h.NetworkTxBytesPerSecond, DiskReadBPS: h.DiskReadBytesPerSecond,
		DiskWriteBPS: h.DiskWriteBytesPerSecond}}
	for _, d := range b.Disks {
		out.Disks = append(out.Disks, domain.DiskValues{Mount: d.Mount, UsedBytes: d.UsedBytes, TotalBytes: d.TotalBytes})
	}
	for _, t := range b.Temperatures {
		out.Temperatures = append(out.Temperatures, domain.TemperatureValues{Sensor: t.Sensor, Celsius: t.Celsius})
	}
	for _, c := range b.Containers {
		out.Containers = append(out.Containers, domain.ContainerValues{Name: c.Name, CPUPercent: c.CPUPercent, MemoryBytes: c.MemoryBytes,
			MemoryLimitBytes: c.MemoryLimitBytes, NetworkRxBPS: c.NetworkRxBytesPerSecond, NetworkTxBPS: c.NetworkTxBytesPerSecond,
			BlockReadBPS: c.BlockReadBytesPerSecond, BlockWriteBPS: c.BlockWriteBytesPerSecond, PIDs: c.PIDs})
	}
	return out
}

// publishSampled announces new samples on the bus (metric invalidations:
// open charts refetch).
func (s *Service) publishSampled(env string, host bool, containers []string) {
	if !host && len(containers) == 0 {
		return
	}
	members := append([]string(nil), containers...)
	sort.Strings(members)
	s.opts.Bus.Publish(events.Event{Type: events.MetricsSampled, ResourceType: events.ResourceEnvironment, ResourceID: env,
		EnvironmentID: env, Attributes: map[string]string{"host": strconv.FormatBool(host)}, Members: members})
}
