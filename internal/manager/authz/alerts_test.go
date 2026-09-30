package authz_test

import (
	"context"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/events"
)

// Alerts (#159) are shown through their source's permission and dismissed
// with alert.dismiss scoped like that source.
func TestAlertVisibilityFollowsTheSource(t *testing.T) {
	ctx := context.Background()
	disk := domain.Alert{ID: "a1", Kind: domain.NotifyDiskHealth, EnvironmentID: "e1", ResourceType: domain.AlertResourceDisk, ResourceID: "/dev/sda"}
	raid := domain.Alert{ID: "a2", Kind: domain.NotifyRAID, EnvironmentID: "e1", ResourceType: domain.AlertResourceRAID, ResourceID: "md0"}
	offline := domain.Alert{ID: "a3", Kind: domain.NotifyEnvironmentOffline, EnvironmentID: "e1", ResourceType: domain.AlertResourceEnvironment, ResourceID: "e1"}
	job := domain.Alert{ID: "a4", Kind: domain.NotifyJobFailed, EnvironmentID: "e1", ResourceType: domain.AlertResourceJob, ResourceID: "j1",
		JobKind: "stack.deploy", Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}}}
	updates := domain.Alert{ID: "a5", Kind: domain.NotifyUpdatesAvailable, EnvironmentID: "e1", ResourceType: domain.AlertResourceUpdatePolicy,
		ResourceID: "p1", Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}}}
	all := []domain.Alert{disk, raid, offline, job, updates}

	cases := []struct {
		name string
		pol  *authztest.Policy
		// visible and dismissible per alert of all.
		visible, dismiss [5]bool
		// otherEnv: the grant is on resources whose environment the test
		// does not locate, so the environment's own visibility (the
		// offline alert) is not asserted.
		otherEnv bool
	}{
		{"no grants", authztest.Only("u"), [5]bool{}, [5]bool{}, false},
		{"system information", authztest.Only("u", "allow environment.system.read @env:e1"),
			[5]bool{true, true, true, false, false}, [5]bool{}, false},
		{"system information elsewhere", authztest.Only("u", "allow environment.system.read @env:e2"),
			[5]bool{}, [5]bool{}, false},
		{"environment visible only", authztest.Only("u", "allow environment.read @env:e1"),
			[5]bool{false, false, true, false, false}, [5]bool{}, false},
		{"system and dismiss in the environment", authztest.Only("u", "allow environment.system.read @env:e1", "allow alert.dismiss @env:e1"),
			[5]bool{true, true, true, false, false}, [5]bool{true, true, true, false, false}, false},
		{"job read on the stack", authztest.Only("u", "allow job.read @stack:s1"),
			[5]bool{false, false, false, true, false}, [5]bool{}, true},
		{"the job kind's capability shows the job", authztest.Only("u", "allow stack.deploy @stack:s1", "allow alert.dismiss @stack:s1"),
			[5]bool{false, false, false, true, false}, [5]bool{false, false, false, true, false}, true},
		{"update policy read", authztest.Only("u", "allow update_policy.read @update_policy:p1", "allow alert.dismiss @update_policy:p1"),
			[5]bool{false, false, false, false, true}, [5]bool{false, false, false, false, true}, true},
		{"update policy read through the stack", authztest.Only("u", "allow update_policy.read @stack:s1"),
			[5]bool{false, false, false, false, true}, [5]bool{}, true},
		{"dismiss without seeing", authztest.Only("u", "allow alert.dismiss @stack:s9"),
			[5]bool{}, [5]bool{}, true},
		{"owner", authztest.New().Owner("u"), [5]bool{true, true, true, true, true}, [5]bool{true, true, true, true, true}, false},
	}
	for _, c := range cases {
		ch := authz.For(ctx, c.pol, principal("u"))
		for i, a := range all {
			if c.otherEnv && a.Kind == domain.NotifyEnvironmentOffline {
				continue
			}
			if got := authz.AlertVisible(ch, a); got != c.visible[i] {
				t.Errorf("%s: %s visible %v, want %v", c.name, a.Kind, got, c.visible[i])
			}
			if got := authz.AlertDismissible(ch, a); got != c.dismiss[i] {
				t.Errorf("%s: %s dismissible %v, want %v", c.name, a.Kind, got, c.dismiss[i])
			}
			// The live event follows the same rule.
			e := events.Event{Type: events.AlertUpdated, ResourceType: events.ResourceAlert, ResourceID: a.ID, EnvironmentID: a.EnvironmentID, Alert: &a}
			if got := authz.EventVisible(ch, e); got != c.visible[i] {
				t.Errorf("%s: %s event visible %v, want %v", c.name, a.Kind, got, c.visible[i])
			}
		}
	}
	// A multi-target job needs alert.dismiss on every target.
	multi := job
	multi.Targets = []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}, {Type: domain.TargetStack, ID: "s2"}}
	ch := authz.For(ctx, authztest.Only("u", "allow job.read @all", "allow alert.dismiss @stack:s1"), principal("u"))
	if !authz.AlertVisible(ch, multi) || authz.AlertDismissible(ch, multi) {
		t.Fatal("dismissing a job alert needs alert.dismiss on every target")
	}
	// An event without its alert reaches the owner only; the audited
	// dismissal's resource.changed reaches nobody (alert.updated does).
	bare := events.Event{Type: events.AlertUpdated, ResourceID: "a1", EnvironmentID: "e1"}
	sys := authz.For(ctx, authztest.Only("u", "allow environment.system.read @env:e1"), principal("u"))
	if authz.EventVisible(sys, bare) {
		t.Fatal("an alert event without its alert reached a non-owner")
	}
	changed := events.Event{Type: events.ResourceChanged, ResourceType: "alert", ResourceID: "a1", EnvironmentID: "e1"}
	if authz.EventVisible(authz.For(ctx, authztest.New().Owner("o"), principal("o")), changed) {
		t.Fatal("resource.changed of an alert is relayed")
	}
}
