package updates_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
)

// change applies a change to the updates setup.
func (h *harness) change(c updates.SetupChange) domain.UpdateSetup {
	h.t.Helper()
	st, err := h.svc.Setup(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	_, after, err := h.svc.UpdateSetup(h.ctx, st.Revision, c)
	if err != nil {
		h.t.Fatal(err)
	}
	return after
}

func (h *harness) targets() []updates.ManagedTarget {
	h.t.Helper()
	ts, err := h.svc.Targets(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	return ts
}

// The setup starts with both schedules off; it covers every environment's
// stacks except what it leaves out (a stack, or its whole environment),
// keeps the records of what it no longer covers, and a scheduled check
// asks for every covered target.
func TestSetupCoversEverythingButWhatItLeavesOut(t *testing.T) {
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

	st, err := h.svc.Setup(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID == "" || st.Check.Enabled || st.Run.Enabled || st.Check.Cron != "0 3 * * *" || st.Run.Cron != "0 4 * * *" ||
		len(st.ExcludeEnvironments)+len(st.ExcludeStacks)+len(st.ExcludeContainers) != 0 {
		t.Fatalf("new setup %+v", st)
	}
	var rej *scheduler.Rejection
	if err := h.svc.CheckSource().Validate(h.ctx, st.ID); !errors.As(err, &rej) || rej.Class != scheduler.RejectPolicyDisabled {
		t.Fatalf("check while disabled: %v", err)
	}
	if _, err := h.svc.CheckSetup(h.ctx, authz.Service(), "first"); err != nil {
		t.Fatal(err)
	}
	children := h.targets()
	if len(children) != 1 || children[0].Policy.TargetID != "st-1" || children[0].Policy.ParentID != st.ID || children[0].Policy.Inactive ||
		children[0].InactiveReason != "" || children[0].Policy.Name != "Automatic updates for shop" {
		t.Fatalf("discovered stack: %+v", children)
	}

	window := &domain.UpdateWindow{Start: "01:00", End: "02:00"}
	h.change(updates.SetupChange{ExcludeStacks: &[]string{"st-1"}, Window: window})
	children = h.targets()
	if len(children) != 1 || !children[0].Policy.Inactive || children[0].InactiveReason != domain.UpdateTargetExcluded {
		t.Fatalf("excluded stack: %+v", children)
	}
	// The list of records leaves out the ones the setup no longer covers.
	if listed, err := h.svc.List(h.ctx, env, "", 0); err != nil || len(listed) != 0 {
		t.Fatalf("excluded record listed: %+v, %v", listed, err)
	}

	// Leaving the environment out leaves its stacks out as well; unknown
	// environments are dropped.
	st = h.change(updates.SetupChange{ExcludeStacks: &[]string{}, ExcludeEnvironments: &[]string{env, "gone"}})
	if !slices.Equal(st.ExcludeEnvironments, []string{env}) {
		t.Fatalf("environments left out %v", st.ExcludeEnvironments)
	}
	children = h.targets()
	if len(children) != 1 || !children[0].Policy.Inactive || children[0].InactiveReason != domain.UpdateTargetExcluded {
		t.Fatalf("stack of a left-out environment: %+v", children)
	}

	on := domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC", Enabled: true}
	st = h.change(updates.SetupChange{ExcludeEnvironments: &[]string{}, Check: &on})
	children = h.targets()
	if len(children) != 1 || children[0].Policy.Inactive || children[0].Policy.Window == nil || children[0].Policy.Window.Start != "01:00" {
		t.Fatalf("covered again with the window: %+v", children)
	}
	if listed, err := h.svc.List(h.ctx, env, "", 0); err != nil || len(listed) != 1 {
		t.Fatalf("covered record not listed: %+v, %v", listed, err)
	}
	if err := h.svc.CheckSource().Validate(h.ctx, st.ID); err != nil {
		t.Fatalf("enabled check: %v", err)
	}
	reqs, err := h.svc.CheckSource().Jobs(h.ctx, scheduler.Due{PolicyID: st.ID})
	if err != nil || len(reqs) != 1 {
		t.Fatalf("scheduled check: %+v %v", reqs, err)
	}
	scheds, err := h.svc.CheckSource().Schedules(h.ctx)
	if err != nil || !slices.ContainsFunc(scheds, func(s scheduler.PolicySchedule) bool {
		return s.PolicyID == st.ID && s.Enabled && s.EnvironmentID == "" && s.Name == updates.ScheduleName
	}) {
		t.Fatalf("schedules %+v %v", scheds, err)
	}

	// Container exclusions name their environment; a stale revision is
	// refused.
	if _, _, err := h.svc.UpdateSetup(h.ctx, st.Revision, updates.SetupChange{ExcludeContainers: &[]string{"api"}}); err == nil {
		t.Fatal("container exclusion without its environment accepted")
	}
	if _, _, err := h.svc.UpdateSetup(h.ctx, st.Revision-1, updates.SetupChange{}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
}

// Target records are named after their stack or container in plain words,
// follow renames, never contain IDs (records named after their ID by
// earlier versions are renamed), stay unique in the environment, and
// explain why they are no longer covered.
func TestSetupTargetNames(t *testing.T) {
	h := newHarness(t)
	now := h.clk.Now()
	e := domain.Environment{ID: env, Name: env, Status: domain.EnvironmentActive, Online: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertEnvironment(h.ctx, h.db, &e); err != nil {
		t.Fatal(err)
	}
	h.stacks.put(domain.Stack{ID: "st-a", EnvironmentID: env, Name: "zerobyte"})
	h.stacks.put(domain.Stack{ID: "st-b", EnvironmentID: env, Name: "media", DisplayName: "Media"})
	h.stacks.put(domain.Stack{ID: "st-c", EnvironmentID: env, Name: "media-2", DisplayName: "Media"})
	p, err := h.svc.Setup(h.ctx)
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
		out := map[string]updates.ManagedTarget{}
		for _, target := range h.targets() {
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
