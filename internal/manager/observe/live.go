package observe

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Live metrics (#5): while at least one browser has a live stream open
// (Options.LiveDemand), the manager asks every online environment whose
// agent serves metrics.live for its current CPU and memory about once a
// second and keeps the newest answer per environment in memory; nothing
// is stored. The "latest" reads (Latest, LatestContainers: the overview,
// capacity and the current container usage) prefer a live value younger
// than LiveFresh over the newest stored 10 s sample; charts keep reading
// the store. Every answer publishes metrics.live on the bus (IDs only) so
// open views refetch the current values. When nobody watches the manager
// stops asking and forgets the values; the agent then drops its CPU
// baseline too. docs/internal/architecture/metrics.md, "Live metrics".

// Live metrics defaults.
const (
	DefaultLiveInterval = protocol.LiveMetricsInterval
	DefaultLiveTimeout  = 2 * time.Second
	// DefaultLiveFresh: an older live value is not served (the stored
	// sample is).
	DefaultLiveFresh = 3 * time.Second
	// liveKeep: live values older than this are forgotten (the
	// environment went offline or stopped answering).
	liveKeep = time.Minute
)

// liveValues is one metrics.live answer (immutable once kept).
type liveValues struct {
	// at is the manager's clock when the answer arrived.
	at         time.Time
	flags      int
	host       protocol.LiveHostSample
	containers map[string]protocol.LiveContainer
}

// servesChecker is implemented by agents.Hub: whether an environment's
// agent advertised a request (an older agent closes the session on an
// unknown request name).
type servesChecker interface {
	EnvironmentServes(environmentID, name string) bool
}

func (s *Service) servesLive(env string) bool {
	if c, ok := s.opts.Agents.(servesChecker); ok {
		return c.EnvironmentServes(env, protocol.ReqMetricsLive)
	}
	return true
}

// runLive asks for live metrics every LiveInterval while LiveDemand holds.
func (s *Service) runLive(ctx context.Context) {
	if s.opts.LiveDemand == nil {
		return
	}
	t := s.opts.Clock.NewTicker(s.opts.LiveInterval)
	defer t.Stop()
	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
		s.liveRound(ctx, sem, &wg)
	}
}

// liveRound starts one round of live requests: one per online environment
// serving metrics.live, skipping environments whose previous request is
// still running and those beyond the free workers. Without demand it
// forgets every live value instead.
func (s *Service) liveRound(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	now := s.opts.Clock.Now()
	if !s.opts.LiveDemand() {
		s.mu.Lock()
		clear(s.live)
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	for env, v := range s.live {
		if now.Sub(v.at) > liveKeep {
			delete(s.live, env)
		}
	}
	s.mu.Unlock()
	for _, env := range s.environments() {
		if !s.servesLive(env) {
			continue
		}
		s.mu.Lock()
		busy := s.liveInflight[env] || now.Before(s.liveSkip[env])
		if !busy {
			s.liveInflight[env] = true
		}
		s.mu.Unlock()
		if busy {
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			s.mu.Lock()
			delete(s.liveInflight, env)
			s.mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				<-sem
				s.mu.Lock()
				delete(s.liveInflight, env)
				s.mu.Unlock()
			}()
			if err := s.CollectLive(ctx, env); err != nil && ctx.Err() == nil {
				s.log.Debug("live metrics request failed", "environment_id", env, "error", err)
			}
		}()
	}
}

// CollectLive asks an environment's agent for its current CPU and memory,
// keeps the answer in memory and announces it (metrics.live).
func (s *Service) CollectLive(ctx context.Context, env string) error {
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqMetricsLive, struct{}{}, s.opts.LiveTimeout)
	recv := s.opts.Clock.Now()
	if err != nil {
		var re requestError
		if errors.As(err, &re) && re.AgentCode() == protocol.CodeUnsupportedRequest {
			s.mu.Lock()
			s.liveSkip[env] = recv.Add(unsupportedBackoff)
			s.mu.Unlock()
		}
		return err
	}
	var out protocol.LiveMetricsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if err := out.Validate(); err != nil {
		return err
	}
	v := &liveValues{at: recv, flags: out.Flags, host: out.Host, containers: make(map[string]protocol.LiveContainer, len(out.Containers))}
	members := make([]string, 0, len(out.Containers))
	for _, c := range out.Containers {
		v.containers[c.Name] = c
		members = append(members, c.Name)
	}
	s.mu.Lock()
	s.live[env] = v
	s.mu.Unlock()
	host := out.Host.CPUPercent != nil || out.Host.MemoryUsedBytes != nil
	if !host && len(members) == 0 {
		return nil
	}
	sort.Strings(members)
	s.opts.Bus.Publish(events.Event{Type: events.MetricsLive, ResourceType: events.ResourceEnvironment, ResourceID: env,
		EnvironmentID: env, Attributes: map[string]string{"host": strconv.FormatBool(host)}, Members: members})
	return nil
}

// liveFor returns an environment's live values when fresh, else nil.
func (s *Service) liveFor(env string) *liveValues {
	s.mu.Lock()
	v := s.live[env]
	s.mu.Unlock()
	if v == nil || s.opts.Clock.Now().Sub(v.at) > s.opts.LiveFresh {
		return nil
	}
	return v
}

// withLiveHost overlays fresh live host CPU and memory on the latest
// stored sample (v may be nil). Values the live answer lacks (the first
// answer has no CPU) keep the stored ones.
func withLiveHost(l domain.LatestMetrics, ok bool, v *liveValues) (domain.LatestMetrics, bool) {
	if v == nil || (v.host.CPUPercent == nil && v.host.MemoryUsedBytes == nil) {
		return l, ok
	}
	l.At = v.at
	if v.host.CPUPercent != nil {
		l.Host.CPUPercent = v.host.CPUPercent
	}
	if v.host.MemoryUsedBytes != nil {
		l.Host.MemoryUsedBytes = v.host.MemoryUsedBytes
	}
	if v.host.MemoryTotalBytes != nil {
		l.Host.MemoryTotalBytes = v.host.MemoryTotalBytes
	}
	return l, true
}

// withLiveContainers overlays fresh live container CPU and memory on the
// latest stored samples (v may be nil): a container the live answer has
// gets its values (CPU only when known), one only live (just started) is
// added, and one missing from a complete live answer (stopped since) is
// dropped. The result is sorted by name.
func withLiveContainers(stored []domain.LatestContainerMetrics, v *liveValues) []domain.LatestContainerMetrics {
	if v == nil {
		return stored
	}
	complete := v.flags == 0
	out := make([]domain.LatestContainerMetrics, 0, max(len(stored), len(v.containers)))
	seen := make(map[string]bool, len(v.containers))
	for _, m := range stored {
		c, ok := v.containers[m.Values.Name]
		switch {
		case ok:
			seen[c.Name] = true
			out = append(out, liveContainer(m, c, v.at))
		case !complete:
			out = append(out, m)
		}
	}
	for name, c := range v.containers {
		if !seen[name] {
			out = append(out, liveContainer(domain.LatestContainerMetrics{Values: domain.ContainerValues{Name: name}}, c, v.at))
		}
	}
	slices.SortFunc(out, func(a, b domain.LatestContainerMetrics) int { return strings.Compare(a.Values.Name, b.Values.Name) })
	return out
}

func liveContainer(m domain.LatestContainerMetrics, c protocol.LiveContainer, at time.Time) domain.LatestContainerMetrics {
	m.At = at
	if c.CPUPercent != nil {
		m.Values.CPUPercent = c.CPUPercent
	}
	if c.MemoryBytes != nil {
		m.Values.MemoryBytes = c.MemoryBytes
		m.Values.MemoryLimitBytes = c.MemoryLimitBytes
	}
	return m
}
