package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func newAlert(key string, now time.Time) domain.Alert {
	return domain.Alert{ID: ids.New(), DedupeKey: key, Kind: domain.NotifyDiskHealth, Severity: domain.AlertWarning, State: domain.AlertFiring,
		EnvironmentID: "env-1", ResourceType: domain.AlertResourceDisk, ResourceID: "/dev/sda", Title: "Disk /dev/sda on homelab needs attention",
		Facts: map[string]string{"device": "/dev/sda"}, Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: "s1"}},
		Fingerprint: domain.Fingerprint("pending"), StartedAt: now, UpdatedAt: now, LastSeenAt: now, Revision: 1}
}

// At most one alert fires per dedupe key (alerts_firing_key); resolved
// ones stay for the history.
func TestOneFiringAlertPerKey(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	a := newAlert("disk_health/env-1/sda/sat", now)
	if err := store.InsertAlert(ctx, db, &a); err != nil {
		t.Fatal(err)
	}
	dup := newAlert(a.DedupeKey, now)
	if err := store.InsertAlert(ctx, db, &dup); !errors.Is(err, store.ErrAlertFiring) {
		t.Fatalf("second firing alert: %v", err)
	}
	got, found, err := store.FiringAlert(ctx, db, a.DedupeKey)
	if err != nil || !found || got.ID != a.ID || got.Facts["device"] != "/dev/sda" || len(got.Targets) != 1 || got.Targets[0].ID != "s1" {
		t.Fatalf("%+v %v %v", got, found, err)
	}
	// Resolved: the key is free again.
	res := got
	res.State, res.Resolution, res.ResolvedAt, res.Revision = domain.AlertResolved, domain.AlertResolvedFixed, &now, 2
	if err := store.UpdateAlert(ctx, db, &res, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAlert(ctx, db, &res, 1); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale update: %v", err)
	}
	if err := store.InsertAlert(ctx, db, &dup); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.FiringAlert(ctx, db, a.DedupeKey); !found {
		t.Fatal("the new alert does not fire")
	}
}

func TestListAlertsFiltersAndPages(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	var all []domain.Alert
	for i, key := range []string{"k1", "k2", "k3", "k4"} {
		a := newAlert(key, now.Add(time.Duration(i)*time.Second))
		switch key {
		case "k2":
			a.DismissedAt, a.DismissedBy = &now, "u1"
		case "k3":
			a.State, a.ResolvedAt, a.Resolution = domain.AlertResolved, &now, domain.AlertResolvedRemoved
		case "k4":
			a.Kind, a.EnvironmentID = domain.NotifyRAID, "env-2"
		}
		if err := store.InsertAlert(ctx, db, &a); err != nil {
			t.Fatal(err)
		}
		all = append(all, a)
	}
	count := func(f domain.AlertFilter) int {
		as, err := store.ListAlerts(ctx, db, f, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(as)
	}
	for _, c := range []struct {
		f    domain.AlertFilter
		want int
	}{
		{domain.AlertFilter{}, 4},
		{domain.AlertFilter{State: domain.AlertListActive}, 2},
		{domain.AlertFilter{State: domain.AlertListDismissed}, 1},
		{domain.AlertFilter{State: domain.AlertListFiring}, 3},
		{domain.AlertFilter{State: domain.AlertListResolved}, 1},
		{domain.AlertFilter{Kind: domain.NotifyRAID}, 1},
		{domain.AlertFilter{EnvironmentID: "env-1"}, 3},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%+v: %d, want %d", c.f, got, c.want)
		}
	}
	// Newest first, continuing before an ID.
	page, err := store.ListAlerts(ctx, db, domain.AlertFilter{}, all[2].ID, 1)
	if err != nil || len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestAlertRetention(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	old := newAlert("old", now)
	old.State, old.ResolvedAt, old.Resolution = domain.AlertResolved, &now, domain.AlertResolvedFixed
	firing := newAlert("firing", now)
	for _, a := range []*domain.Alert{&old, &firing} {
		if err := store.InsertAlert(ctx, db, a); err != nil {
			t.Fatal(err)
		}
	}
	sent := now
	ds := []domain.AlertDelivery{
		{ID: ids.New(), AlertID: firing.ID, ChannelID: "c1", Event: domain.AlertEventFiring, State: domain.DeliverySent, NextAttemptAt: now,
			CreatedAt: now, UpdatedAt: now, SentAt: &sent},
		{ID: ids.New(), AlertID: firing.ID, ChannelID: "c1", Event: domain.AlertEventWorse, State: domain.DeliveryPending, NextAttemptAt: now,
			CreatedAt: now, UpdatedAt: now},
		{ID: ids.New(), AlertID: old.ID, ChannelID: "c1", Event: domain.AlertEventResolved, State: domain.DeliveryPending, NextAttemptAt: now,
			CreatedAt: now, UpdatedAt: now},
	}
	if err := store.InsertAlertDeliveries(ctx, db, ds); err != nil {
		t.Fatal(err)
	}
	later := now.Add(8 * 24 * time.Hour)
	if n, err := store.PurgeAlertDeliveries(ctx, db, later.Add(-7*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	pending, err := store.PendingAlertDeliveries(ctx, db)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending deliveries were purged: %+v %v", pending, err)
	}
	if n, err := store.PurgeResolvedAlerts(ctx, db, now.Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	// 90 days later the resolved alert goes, with its deliveries; the
	// firing one stays.
	if n, err := store.PurgeResolvedAlerts(ctx, db, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if ds, _ := store.AlertDeliveries(ctx, db, old.ID); len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
	if _, err := store.GetAlert(ctx, db, firing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAlert(ctx, db, old.ID); !errors.Is(err, domain.ErrAlertNotFound) {
		t.Fatal(err)
	}
}
