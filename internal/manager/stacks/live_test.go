package stacks_test

import (
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
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

// TestLiveServicesCarryVolumeMounts: each container reports its volume
// mounts (anonymous ones marked), never its bind mounts.
func TestLiveServicesCarryVolumeMounts(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	anon := strings.Repeat("0f", 32)
	h.engine.setProject("shop", []engine.Container{{ID: "x", Names: []string{"/shop-web-1"}, ImageID: "sha256:web", State: "running",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web"},
		Mounts: []engine.Mount{
			{Type: "volume", Name: "shop_data", Destination: "/data", ReadWrite: true},
			{Type: "bind", Source: "/srv/shop", Destination: "/srv"},
			{Type: "volume", Name: anon, Destination: "/cache"},
		}}})
	v, err := h.svc.Services(h.ctx, st)
	if err != nil || !v.Live {
		t.Fatalf("services %+v %v", v, err)
	}
	want := []domain.ContainerVolume{{Name: anon, Destination: "/cache", ReadOnly: true, Anonymous: true}, {Name: "shop_data", Destination: "/data"}}
	for _, sv := range v.Services {
		if sv.Name != "web" {
			continue
		}
		if len(sv.Containers) != 1 || !slices.Equal(sv.Containers[0].Volumes, want) {
			t.Errorf("web containers %+v, want volumes %+v", sv.Containers, want)
		}
		return
	}
	t.Errorf("no web service in %+v", v.Services)
}
