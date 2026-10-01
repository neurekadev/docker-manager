package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Host health (#143): the manager asks every online environment whose
// agent serves host.health for its disk health (SMART devices, md arrays,
// ZFS pools) about once a minute, 5 s after it comes online and when a
// user presses "Check disks now" or "Check RAID now" (CheckHealth, rate
// limited per environment). A report is stored in metrics.db only when
// it changed, served while the environment is offline, and announced as
// inventory.updated with the attribute health=true (the System tab
// refetches). While the agent reads its disks (smart.checking) the
// manager asks again every 5 s. docs/internal/architecture/metrics.md,
// "Host health".

// Host health defaults.
const (
	DefaultHealthInterval = time.Minute
	// healthTimeout bounds one host.health request (the agent answers a
	// fresh SMART read within 3 s, else with checking set).
	healthTimeout = 20 * time.Second
	// healthOnlineDelay: the first report after an environment comes
	// online (or its agent's capabilities change).
	healthOnlineDelay = 5 * time.Second
	// healthFollowUp is how often the manager asks again while the agent
	// reads its disks; healthFollowUpMax ends that after a stuck read.
	healthFollowUp    = 5 * time.Second
	healthFollowUpMax = 10 * time.Minute
	// SMARTCheckSpacing and RAIDCheckSpacing are the minimum time between
	// two checks of one environment (CheckHealth answers
	// *HealthRateLimitError before).
	SMARTCheckSpacing = 30 * time.Second
	RAIDCheckSpacing  = 5 * time.Second
)

// Health check scopes (the API's "Check disks now" and "Check RAID now").
const (
	HealthScopeSMART = "smart"
	HealthScopeRAID  = "raid"
)

// Host health errors.
var (
	// ErrHealthUnsupported: the environment's agent predates host.health.
	ErrHealthUnsupported = errors.New("observe: the agent does not serve host.health")
	// ErrHealthOffline: the environment's agent is not connected.
	ErrHealthOffline = errors.New("observe: the environment is offline")
	// ErrHealthTimeout: the agent did not answer in time.
	ErrHealthTimeout = errors.New("observe: the agent did not answer in time")
	// ErrHealthScope: an unknown check scope.
	ErrHealthScope = errors.New("observe: unknown health check scope")
)

// HealthRateLimitError refuses a check that came too soon after the last
// one of the same environment and scope.
type HealthRateLimitError struct {
	RetryAfter time.Duration
}

func (e *HealthRateLimitError) Error() string {
	return fmt.Sprintf("observe: checked moments ago; try again in %s", e.RetryAfter.Round(time.Second))
}

// HostHealth is an environment's last disk health report.
type HostHealth struct {
	protocol.HostHealthOutput
	// ReceivedAt is the manager's clock when the report arrived.
	ReceivedAt time.Time
}

// healthState is the host health part of Service (its own maps under
// Service.mu; save serializes compare-and-store).
type healthState struct {
	reports   map[string]HostHealth
	sigs      map[string]string
	inflight  map[string]bool
	skip      map[string]time.Time
	checks    map[string]time.Time
	following map[string]time.Time
	follow    chan string
	save      sync.Mutex
}

func newHealthState() healthState {
	return healthState{reports: map[string]HostHealth{}, sigs: map[string]string{}, inflight: map[string]bool{},
		skip: map[string]time.Time{}, checks: map[string]time.Time{}, following: map[string]time.Time{}, follow: make(chan string, 64)}
}

// HostHealth returns an environment's last disk health report (also while
// it is offline).
func (s *Service) HostHealth(environmentID string) (HostHealth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.health.reports[environmentID]
	return h, ok
}

func (s *Service) servesHealth(env string) bool {
	if c, ok := s.opts.Agents.(servesChecker); ok {
		return c.EnvironmentServes(env, protocol.ReqHostHealth)
	}
	return true
}

// loadHealth restores the stored reports (Load).
func (s *Service) loadHealth(ctx context.Context) error {
	recs, err := s.opts.Store.HostHealths(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		var out protocol.HostHealthOutput
		if json.Unmarshal(r.Data, &out) != nil || out.Validate() != nil {
			continue
		}
		s.health.reports[r.EnvironmentID] = HostHealth{HostHealthOutput: out, ReceivedAt: r.ReceivedAt}
		s.health.sigs[r.EnvironmentID] = healthSignature(out)
		s.known[r.EnvironmentID] = true
	}
	return nil
}

// healthSignature identifies a report's content without the read times
// that change on every request (sampledAt, raid.readAt).
func healthSignature(out protocol.HostHealthOutput) string {
	out.SampledAt, out.RAID.ReadAt = time.Time{}, time.Time{}
	b, _ := json.Marshal(out)
	return string(b)
}

// CheckHealth serves "Check disks now" (scope smart: the agent reads every
// disk's SMART data now, never a self-test) and "Check RAID now" (scope
// raid: a fresh RAID read, never a scrub). A long SMART read answers with
// SMART.Checking set; the manager keeps asking until it ends and announces
// the result. Checks of one environment and scope closer than
// SMARTCheckSpacing / RAIDCheckSpacing get *HealthRateLimitError.
func (s *Service) CheckHealth(ctx context.Context, env, scope string) (HostHealth, error) {
	var refresh string
	var spacing time.Duration
	switch scope {
	case HealthScopeSMART:
		refresh, spacing = protocol.HealthRefreshSMART, SMARTCheckSpacing
	case HealthScopeRAID:
		refresh, spacing = protocol.HealthRefreshRAID, RAIDCheckSpacing
	default:
		return HostHealth{}, ErrHealthScope
	}
	if !s.opts.Agents.Online(env) {
		return HostHealth{}, ErrHealthOffline
	}
	if !s.servesHealth(env) {
		return HostHealth{}, ErrHealthUnsupported
	}
	now := s.opts.Clock.Now()
	key := env + "/" + scope
	s.mu.Lock()
	if last, ok := s.health.checks[key]; ok && now.Sub(last) < spacing {
		s.mu.Unlock()
		return HostHealth{}, &HealthRateLimitError{RetryAfter: spacing - now.Sub(last)}
	}
	s.health.checks[key] = now
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	h, err := s.RefreshHealth(ctx, env, refresh)
	if err != nil {
		// A failed check does not count: the user may retry at once (a
		// SMART check the agent already runs queues at most one more round).
		s.mu.Lock()
		if s.health.checks[key].Equal(now) {
			delete(s.health.checks, key)
		}
		s.mu.Unlock()
	}
	return h, err
}

// RefreshHealth asks an environment's agent for its disk health (refresh
// "", smart or raid), keeps the report, stores and announces it when it
// changed, and follows a running SMART read. An answer the agent gave
// before the kept report (a minute poll that arrives after a "Check disks
// now" answer) is dropped: the kept report is returned.
func (s *Service) RefreshHealth(ctx context.Context, env, refresh string) (HostHealth, error) {
	s.noteEnvironment(env)
	if refresh == protocol.HealthRefreshSMART {
		// A fresh read: follow it for up to healthFollowUpMax again, even
		// after an earlier read stopped being followed.
		s.mu.Lock()
		delete(s.health.following, env)
		s.mu.Unlock()
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqHostHealth, protocol.HostHealthInput{Refresh: refresh}, healthTimeout)
	if err != nil {
		return HostHealth{}, s.healthErr(env, err)
	}
	var out protocol.HostHealthOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return HostHealth{}, err
	}
	if err := out.Validate(); err != nil {
		return HostHealth{}, err
	}
	now := s.opts.Clock.Now().UTC()
	s.health.save.Lock()
	defer s.health.save.Unlock()
	s.mu.Lock()
	prev, had := s.health.reports[env]
	s.mu.Unlock()
	if had && out.SampledAt.Before(prev.SampledAt) && now.Sub(prev.ReceivedAt) < healthTimeout {
		// Overtaken by a newer answer. Only within healthTimeout of it: a
		// later answer with an older agent clock (the host's clock was
		// set back) is kept.
		return prev, nil
	}
	if had && out.SMART.Checking && out.SMART.CheckedAt == nil && prev.SMART.CheckedAt != nil {
		// A restarted agent reads its disks for the first time: keep the
		// last known devices until it is done.
		keep := prev.SMART
		keep.Checking = true
		out.SMART = keep
	}
	h := HostHealth{HostHealthOutput: out, ReceivedAt: now}
	sig := healthSignature(out)
	s.mu.Lock()
	changed := s.health.sigs[env] != sig
	s.health.reports[env] = h
	s.mu.Unlock()
	if changed {
		data, err := json.Marshal(out)
		if err != nil {
			return h, err
		}
		if err := s.opts.Store.SaveHostHealth(ctx, metrics.HostHealthRecord{EnvironmentID: env, Data: data, CollectedAt: out.SampledAt,
			ReceivedAt: now}); err != nil {
			return h, err
		}
		s.mu.Lock()
		s.health.sigs[env] = sig
		s.mu.Unlock()
		s.opts.Bus.Publish(events.Event{Type: events.InventoryUpdated, ResourceType: events.ResourceEnvironment, ResourceID: env,
			EnvironmentID: env, Attributes: map[string]string{"health": "true"}})
	}
	s.followHealth(env, out.SMART.Checking, now)
	return h, nil
}

// followHealth schedules the next request while the agent reads its
// disks (at most healthFollowUpMax).
func (s *Service) followHealth(env string, checking bool, now time.Time) {
	s.mu.Lock()
	since, following := s.health.following[env]
	switch {
	case !checking:
		delete(s.health.following, env)
		s.mu.Unlock()
		return
	case !following:
		s.health.following[env] = now
	case now.Sub(since) > healthFollowUpMax:
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	select {
	case s.health.follow <- env:
	default: // the loop is busy with many; the minute poll catches up
	}
}

// healthErr maps a failed request to the host health errors.
func (s *Service) healthErr(env string, err error) error {
	var re requestError
	switch {
	case errors.As(err, &re) && re.AgentCode() == protocol.CodeUnsupportedRequest:
		s.mu.Lock()
		s.health.skip[env] = s.opts.Clock.Now().Add(unsupportedBackoff)
		s.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrHealthUnsupported, err)
	case errors.Is(err, protocol.ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrHealthTimeout, err)
	case !s.opts.Agents.Online(env):
		return fmt.Errorf("%w: %w", ErrHealthOffline, err)
	}
	return err
}

// healthTrigger: an environment came online or its agent's capabilities
// changed (an upgraded agent may serve host.health now).
func healthTrigger(e events.Event) bool {
	switch e.Type {
	case events.EnvironmentOnline, events.AgentCapabilitiesUpdate:
		return e.EnvironmentID != "" || e.ResourceType == events.ResourceEnvironment
	}
	return false
}

// runHealth asks every online environment serving host.health for its
// report every HealthInterval, healthOnlineDelay after it comes online and
// every healthFollowUp while its agent reads its disks.
func (s *Service) runHealth(ctx context.Context) {
	sub := s.opts.Bus.Subscribe(256, healthTrigger)
	defer sub.Close()
	clk := s.opts.Clock
	periodic := clk.NewTicker(s.opts.HealthInterval)
	defer periodic.Stop()
	due := map[string]time.Time{}
	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	schedule := func(env string, at time.Time) {
		if env == "" {
			return
		}
		if d, ok := due[env]; !ok || at.Before(d) {
			due[env] = at
		}
	}
	for {
		var timer clock.Timer
		var tc <-chan time.Time
		if len(due) > 0 {
			earliest := time.Time{}
			for _, d := range due {
				if earliest.IsZero() || d.Before(earliest) {
					earliest = d
				}
			}
			timer = clk.NewTimer(max(earliest.Sub(clk.Now()), 0))
			tc = timer.C()
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case e := <-sub.C():
			env := e.EnvironmentID
			if env == "" {
				env = e.ResourceID
			}
			s.noteEnvironment(env)
			schedule(env, clk.Now().Add(healthOnlineDelay))
		case env := <-s.health.follow:
			schedule(env, clk.Now().Add(healthFollowUp))
		case <-periodic.C():
			for _, env := range s.environments() {
				schedule(env, clk.Now())
			}
		case <-tc:
		}
		if timer != nil {
			timer.Stop()
		}
		now := clk.Now()
		for env, d := range due {
			if d.After(now) {
				continue
			}
			delete(due, env)
			s.mu.Lock()
			skip := s.health.inflight[env] || now.Before(s.health.skip[env])
			s.mu.Unlock()
			if skip || !s.opts.Agents.Online(env) || !s.servesHealth(env) {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				due[env] = now.Add(healthFollowUp) // busy: later
				continue
			}
			s.mu.Lock()
			s.health.inflight[env] = true
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					<-sem
					s.mu.Lock()
					delete(s.health.inflight, env)
					s.mu.Unlock()
				}()
				rctx, cancel := context.WithTimeout(ctx, healthTimeout)
				defer cancel()
				if _, err := s.RefreshHealth(rctx, env, ""); err != nil && ctx.Err() == nil {
					s.log.Debug("host health refresh failed", "environment_id", env, "error", err)
				}
			}()
		}
	}
}
