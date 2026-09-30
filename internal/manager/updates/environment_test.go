package updates_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
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
	if err != nil || len(children) != 1 || children[0].Policy.TargetID != "st-1" || children[0].Policy.Inactive ||
		children[0].InactiveReason != "" || children[0].Policy.Name != "Automatic updates for shop" {
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
	if err != nil || len(children) != 1 || !children[0].Policy.Inactive || children[0].InactiveReason != domain.UpdateTargetExcluded {
		t.Fatalf("excluded stack: %+v, %v", children, err)
	}
	// The list of policies leaves out records the policy no longer covers.
	if listed, err := h.svc.List(h.ctx, env, "", 0); err != nil || len(listed) != 0 {
		t.Fatalf("excluded record listed: %+v, %v", listed, err)
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
	if err != nil || len(children) != 1 || children[0].Policy.Inactive || children[0].Policy.Window == nil ||
		children[0].Policy.Window.Start != "01:00" {
		t.Fatalf("reactivated stack with updated window: %+v, %v", children, err)
	}
	if listed, err := h.svc.List(h.ctx, env, "", 0); err != nil || len(listed) != 1 {
		t.Fatalf("covered record not listed: %+v, %v", listed, err)
	}
	if _, err := h.svc.CreateEnvironmentPolicy(h.ctx, updates.NewEnvironmentPolicy{EnvironmentID: "other-env", Name: "other"}); err != nil {
		t.Fatalf("disjoint environment: %v", err)
	}
}

// Target records are named after their stack or container in plain words,
// follow renames, never contain IDs (records named after their ID by
// earlier versions are renamed), stay unique in the environment, and
// explain why they are no longer covered.
func TestEnvironmentTargetNames(t *testing.T) {
	h := newHarness(t)
	now := h.clk.Now()
	e := domain.Environment{ID: env, Name: env, Status: domain.EnvironmentActive, Online: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertEnvironment(h.ctx, h.db, &e); err != nil {
		t.Fatal(err)
	}
	h.stacks.put(domain.Stack{ID: "st-a", EnvironmentID: env, Name: "zerobyte"})
	h.stacks.put(domain.Stack{ID: "st-b", EnvironmentID: env, Name: "media", DisplayName: "Media"})
	h.stacks.put(domain.Stack{ID: "st-c", EnvironmentID: env, Name: "media-2", DisplayName: "Media"})
	p, err := h.svc.CreateEnvironmentPolicy(h.ctx, updates.NewEnvironmentPolicy{EnvironmentID: env, Name: "updates"})
	if err != nil {
		t.Fatal(err)
	}
	// An earlier version named its records after their ID.
	old := domain.UpdatePolicy{ID: "0190a6e0-1122-7788-aabb-ccddeeff0011", ParentID: p.ID, EnvironmentID: env,
		TargetType: domain.UpdateTargetStack, TargetID: "st-a", Name: "Automatic update 0190a6e0-1122-7788-aabb-ccddeeff0011",
		Check: p.Check, Run: p.Run, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertUpdatePolicy(h.ctx, h.db, &old); err != nil {
		t.Fatal(err)
	}
	names := func() map[string]updates.ManagedTarget {
		t.Helper()
		targets, err := h.svc.ManagedPolicies(h.ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]updates.ManagedTarget{}
		for _, target := range targets {
			if strings.Contains(target.Policy.Name, target.Policy.ID) || strings.Contains(target.Policy.Name, target.Policy.TargetID) &&
				target.Policy.TargetType == domain.UpdateTargetStack {
				t.Errorf("name contains an ID: %q", target.Policy.Name)
			}
			out[target.Policy.TargetID] = target
		}
		return out
	}
	got := names()
	if got["st-a"].Policy.ID != old.ID || got["st-a"].Policy.Name != "Automatic updates for zerobyte" {
		t.Fatalf("renamed earlier record: %+v", got["st-a"].Policy)
	}
	media := []string{got["st-b"].Policy.Name, got["st-c"].Policy.Name}
	slices.Sort(media)
	if !slices.Equal(media, []string{"Automatic updates for Media", "Automatic updates for Media (stack)"}) {
		t.Fatalf("equal display names: %q", media)
	}
	// Stable while nothing changes.
	again := names()
	if again["st-b"].Policy.Name != got["st-b"].Policy.Name || again["st-b"].Policy.Revision != got["st-b"].Policy.Revision {
		t.Fatalf("name changed without a rename: %+v -> %+v", got["st-b"].Policy, again["st-b"].Policy)
	}
	// A rename of the stack renames its record.
	h.stacks.put(domain.Stack{ID: "st-a", EnvironmentID: env, Name: "zerobyte", DisplayName: "Zerobyte backups"})
	if got := names(); got["st-a"].Policy.Name != "Automatic updates for Zerobyte backups" {
		t.Fatalf("after the rename: %+v", got["st-a"].Policy)
	}
	// A deleted stack's record is inactive (missing) and keeps its name.
	h.stacks.mu.Lock()
	delete(h.stacks.stacks, "st-a")
	h.stacks.mu.Unlock()
	got = names()
	if a := got["st-a"]; !a.Policy.Inactive || a.InactiveReason != domain.UpdateTargetMissing || a.Policy.Name != "Automatic updates for Zerobyte backups" {
		t.Fatalf("deleted stack: %+v", a)
	}
	if listed, err := h.svc.List(h.ctx, env, "", 0); err != nil || len(listed) != 2 {
		t.Fatalf("listed records: %+v, %v", listed, err)
	}
}
