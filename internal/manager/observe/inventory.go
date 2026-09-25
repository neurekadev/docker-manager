package observe

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/metrics"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// RefreshInventory reads an environment's Engine inventory from its agent
// (engine.info), stores it and announces it (inventory.updated). The
// session hub's reconciler calls it before the environment is reported
// online; changes (Docker events, capability updates) and a periodic
// timer refresh it later.
func (s *Service) RefreshInventory(ctx context.Context, env string) error {
	s.noteEnvironment(env)
	ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
	defer cancel()
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqEngineInfo, nil, inventoryTimeout)
	if err != nil {
		return err
	}
	var inv protocol.EngineInventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return err
	}
	if err := inv.Validate(); err != nil {
		return err
	}
	now := s.opts.Clock.Now().UTC()
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	if err := s.opts.Store.SaveInventory(ctx, metrics.InventoryRecord{EnvironmentID: env, Data: data, CollectedAt: inv.CollectedAt,
		ReceivedAt: now}); err != nil {
		return err
	}
	s.mu.Lock()
	s.inv[env] = Inventory{EngineInventory: inv, ReceivedAt: now}
	s.mu.Unlock()
	s.opts.Bus.Publish(events.Event{Type: events.InventoryUpdated, ResourceType: events.ResourceEnvironment, ResourceID: env, EnvironmentID: env})
	return nil
}

// Reconcile is the session hub reconciler (#3): refresh the inventory of a
// reconnected environment. A failure never blocks the environment: the
// inventory is refreshed again on the next change or timer.
func (s *Service) Reconcile(ctx context.Context, env string) error {
	if err := s.RefreshInventory(ctx, env); err != nil && ctx.Err() == nil {
		var re requestError
		if errors.As(err, &re) && re.AgentCode() == protocol.CodeUnsupportedRequest {
			s.log.Info("agent does not serve engine.info; no Engine inventory", "environment_id", env)
		} else {
			s.log.Warn("could not read the Engine inventory", "environment_id", env, "error", err)
		}
	}
	return nil
}

// inventoryTrigger reports whether a bus event changes an inventory.
func inventoryTrigger(e events.Event) bool {
	switch e.Type {
	case events.DockerEvent, events.AgentCapabilitiesUpdate, events.EnvironmentResync:
		return e.EnvironmentID != ""
	}
	return false
}

// runInventory debounces inventory refreshes: at most one per environment
// per InventoryDebounce after a change, plus one per InventoryInterval for
// every online environment.
func (s *Service) runInventory(ctx context.Context) {
	sub := s.opts.Bus.Subscribe(1024, inventoryTrigger)
	defer sub.Close()
	clk := s.opts.Clock
	periodic := clk.NewTicker(s.opts.InventoryInterval)
	defer periodic.Stop()
	due := map[string]time.Time{}
	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, s.opts.Concurrency)
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
			if _, ok := due[env]; !ok {
				due[env] = clk.Now().Add(s.opts.InventoryDebounce)
			}
		case <-periodic.C():
			for _, env := range s.environments() {
				if _, ok := due[env]; !ok {
					due[env] = clk.Now()
				}
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
			if !s.opts.Agents.Online(env) {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				due[env] = now.Add(s.opts.InventoryDebounce) // busy: later
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := s.RefreshInventory(ctx, env); err != nil && ctx.Err() == nil {
					s.log.Debug("inventory refresh failed", "environment_id", env, "error", err)
				}
			}()
		}
	}
}
