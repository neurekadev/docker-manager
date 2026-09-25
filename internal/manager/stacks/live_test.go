package stacks_test

import (
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/stacks"
)

// TestLiveServicesPublishOnlyChanges: reading the services records the
// observed Engine state but announces it only when it changed, so clients
// refetching on stack.updated cannot loop.
func TestLiveServicesPublishOnlyChanges(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.deploy(st)
	h.run()
	st = h.get(st.ID)
	sub := h.bus.Subscribe(0, func(e events.Event) bool { return e.Type == events.StackUpdated })
	defer sub.Close()
	for range 3 {
		v, err := h.svc.Services(h.ctx, st)
		if err != nil || !v.Live || v.Drift {
			t.Fatalf("services %+v %v", v, err)
		}
	}
	select {
	case e := <-sub.C():
		t.Fatalf("unchanged Engine state published %+v", e)
	default:
	}
	// A change is published once.
	h.engine.setProject("shop", nil)
	v, err := h.svc.Services(h.ctx, st)
	if err != nil || !v.Drift {
		t.Fatalf("services after the containers vanished %+v %v", v, err)
	}
	for _, sv := range v.Services {
		if len(sv.Drift) != 1 || sv.Drift[0] != stacks.DriftMissing {
			t.Errorf("%s drift %v", sv.Name, sv.Drift)
		}
	}
	if e := <-sub.C(); e.Attributes["change"] != "engine_state" {
		t.Errorf("event %+v", e)
	}
	if got := h.get(st.ID); got.EngineState != domain.EngineStateMissing {
		t.Errorf("engine state %s", got.EngineState)
	}
}
