package updates_test

import (
	"errors"
	"testing"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/updates"
)

func TestEnvironmentPolicyScopesAndExclusions(t *testing.T) {
	h := newHarness(t)
	now := h.clk.Now()
	for _, id := range []string{env, "other-env"} {
		e := domain.Environment{ID: id, Name: id, Status: domain.EnvironmentActive,
			Online: id == env, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(h.ctx, h.db, &e); err != nil {
			t.Fatal(err)
		}
	}
	h.publish("acme/web", "1", "")
	h.deployStack("st-1", "shop", "services:\n  web:\n    image: "+h.ref("acme/web:1")+"\n", nil,
		[]stackService{{name: "web", image: h.ref("acme/web:1"), running: true}})

	p, err := h.svc.CreateEnvironmentPolicy(h.ctx, updates.NewEnvironmentPolicy{EnvironmentID: env, Name: "updates"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateEnvironmentPolicy(h.ctx, updates.NewEnvironmentPolicy{Name: "all"}); !errors.Is(err, domain.ErrUpdateScopeOverlap) {
		t.Fatalf("global policy overlapping a single environment: %v", err)
	}
	if _, err := h.svc.CheckEnvironment(h.ctx, authz.Service(), p.ID, "first"); err != nil {
		t.Fatal(err)
	}
	children, err := h.svc.ManagedPolicies(h.ctx, p.ID)
	if err != nil || len(children) != 1 || children[0].TargetID != "st-1" || children[0].Inactive {
		t.Fatalf("discovered stack: %+v, %v", children, err)
	}
	window := &domain.UpdateWindow{Start: "01:00", End: "02:00"}
	p, err = h.svc.UpdateEnvironmentPolicy(h.ctx, p.ID, p.Revision, updates.NewEnvironmentPolicy{
		EnvironmentID: env, Name: p.Name, ExcludeStacks: []string{"st-1"}, Window: window})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CheckEnvironment(h.ctx, authz.Service(), p.ID, "excluded"); err != nil {
		t.Fatal(err)
	}
	children, err = h.svc.ManagedPolicies(h.ctx, p.ID)
	if err != nil || len(children) != 1 || !children[0].Inactive {
		t.Fatalf("excluded stack: %+v, %v", children, err)
	}
	p, err = h.svc.UpdateEnvironmentPolicy(h.ctx, p.ID, p.Revision, updates.NewEnvironmentPolicy{
		EnvironmentID: env, Name: p.Name, Window: window})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CheckEnvironment(h.ctx, authz.Service(), p.ID, "included"); err != nil {
		t.Fatal(err)
	}
	children, err = h.svc.ManagedPolicies(h.ctx, p.ID)
	if err != nil || len(children) != 1 || children[0].Inactive || children[0].Window == nil || children[0].Window.Start != "01:00" {
		t.Fatalf("reactivated stack with updated window: %+v, %v", children, err)
	}
	if _, err := h.svc.CreateEnvironmentPolicy(h.ctx, updates.NewEnvironmentPolicy{EnvironmentID: "other-env", Name: "other"}); err != nil {
		t.Fatalf("disjoint environment: %v", err)
	}
}
