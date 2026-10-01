package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func newNotification(kind domain.NotificationEventKind, outcome domain.NotificationOutcome, env string, at time.Time) domain.Notification {
	return domain.Notification{ID: ids.New(), Kind: kind, Outcome: outcome, EnvironmentID: env, JobID: ids.New(), JobKind: "prune.run",
		Targets: []domain.JobTarget{{Type: "maintenance_policy", ID: "mp-1"}}, Origin: domain.OriginScheduled,
		Title: "Prune on homelab reclaimed 1 GiB", Facts: map[string]string{"reclaimedBytes": "1073741824"}, CreatedAt: at}
}

func TestNotificationsAreKeptFilteredAndPurged(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	old := newNotification(domain.NotifyPrune, domain.OutcomeSuccess, "env-1", now.Add(-100*24*time.Hour))
	failed := newNotification(domain.NotifyBackup, domain.OutcomeFailure, "env-2", now)
	pruned := newNotification(domain.NotifyPrune, domain.OutcomeSuccess, "env-1", now)
	for _, n := range []*domain.Notification{&old, &failed, &pruned} {
		if err := store.InsertNotification(ctx, db, n); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetNotification(ctx, db, pruned.ID)
	if err != nil || got.Facts["reclaimedBytes"] != "1073741824" || len(got.Targets) != 1 || got.Targets[0].ID != "mp-1" ||
		got.Origin != domain.OriginScheduled || !got.CreatedAt.Equal(now) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := store.GetNotification(ctx, db, "nope"); !errors.Is(err, domain.ErrNotificationNotFound) {
		t.Fatalf("%v", err)
	}
	list := func(f domain.NotificationFilter, before string, limit int) []string {
		ns, err := store.ListNotifications(ctx, db, f, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}
	// Newest first, by kind, outcome and environment, in pages.
	if got := list(domain.NotificationFilter{}, "", 0); len(got) != 3 || got[0] != pruned.ID || got[2] != old.ID {
		t.Fatalf("%v", got)
	}
	if got := list(domain.NotificationFilter{Kind: domain.NotifyBackup}, "", 0); len(got) != 1 || got[0] != failed.ID {
		t.Fatalf("%v", got)
	}
	if got := list(domain.NotificationFilter{Outcome: domain.OutcomeSuccess, EnvironmentID: "env-1"}, pruned.ID, 1); len(got) != 1 || got[0] != old.ID {
		t.Fatalf("%v", got)
	}
	// A message of a purged notification goes with it.
	d := domain.AlertDelivery{ID: ids.New(), NotificationID: old.ID, ChannelID: "c-1", Event: domain.DeliveryEventNotification,
		Kind: domain.NotifyPrune, Severity: domain.AlertInfo, Outcome: domain.OutcomeSuccess, Title: old.Title,
		Fields: []domain.NotificationField{{Name: "Reclaimed", Value: "1 GiB", Inline: true}}, State: domain.DeliverySent,
		NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertAlertDeliveries(ctx, db, []domain.AlertDelivery{d}); err != nil {
		t.Fatal(err)
	}
	n, err := store.PurgeNotifications(ctx, db, now.Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var left int
	if err := db.NewSelect().Table("alert_deliveries").ColumnExpr("COUNT(*)").Scan(ctx, &left); err != nil || left != 0 {
		t.Fatalf("%d %v", left, err)
	}
}

func TestADeliveryKeepsItsFieldsAndOutcome(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	a := newAlert("disk_health/env-1/sda/sat", now)
	if err := store.InsertAlert(ctx, db, &a); err != nil {
		t.Fatal(err)
	}
	d := domain.AlertDelivery{ID: ids.New(), AlertID: a.ID, ChannelID: "c-1", Event: domain.AlertEventFiring, Kind: a.Kind,
		Severity: a.Severity, Outcome: domain.OutcomeWarning, Title: a.Title, Body: "body",
		Fields: []domain.NotificationField{{Name: "Disk", Value: "/dev/sda", Inline: true, Link: "/environments/env-1"},
			{Name: "Services", Value: "web: 1a → 2b", Items: []domain.NotificationItem{{Text: "web", Link: "/stacks/s1", From: "1a", To: "2b"}}}},
		State: domain.DeliveryPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertAlertDeliveries(ctx, db, []domain.AlertDelivery{d}); err != nil {
		t.Fatal(err)
	}
	ds, err := store.AlertDeliveries(ctx, db, a.ID)
	if err != nil || len(ds) != 1 || ds[0].Outcome != domain.OutcomeWarning || ds[0].NotificationID != "" || len(ds[0].Fields) != 2 ||
		!reflect.DeepEqual(ds[0].Fields, d.Fields) {
		t.Fatalf("%+v %v", ds, err)
	}
	// Finished messages of a firing alert are kept (they say which
	// channels were told); those of a resolved one go.
	ds[0].State, ds[0].UpdatedAt = domain.DeliverySent, now
	if err := store.SetAlertDeliveries(ctx, db, ds); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PurgeAlertDeliveries(ctx, db, now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	res := a
	res.State, res.Resolution, res.ResolvedAt, res.Revision = domain.AlertResolved, domain.AlertResolvedFixed, &now, 2
	if err := store.UpdateAlert(ctx, db, &res, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PurgeAlertDeliveries(ctx, db, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestAlertSettingsAndOverrides(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := testutil.Epoch
	for _, id := range []string{"env-1", "env-2"} {
		e := domain.Environment{ID: id, Name: id, EngineID: "E-" + id, InstallID: "i-" + id, AgentID: "agent-" + id,
			Status: domain.EnvironmentActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &e); err != nil {
			t.Fatal(err)
		}
	}
	set, err := store.GetAlertSettings(ctx, db)
	if err != nil || set.Thresholds != domain.DefaultAlertThresholds() || set.Revision != 1 || len(set.Overrides) != 0 {
		t.Fatalf("%+v %v", set, err)
	}
	zero, ninety := 0, 90
	next := set
	next.Thresholds.TemperatureWarning = 70
	next.Overrides = []domain.AlertThresholdOverride{{EnvironmentID: "env-2", DiskSpaceWarning: &ninety, MemoryWarning: &zero}}
	if err := store.ReplaceAlertSettings(ctx, db, 1, next, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAlertSettings(ctx, db, 1, next, now); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale: %v", err)
	}
	got, err := store.GetAlertSettings(ctx, db)
	if err != nil || got.Revision != 2 || got.Thresholds.TemperatureWarning != 70 || len(got.Overrides) != 1 ||
		got.Overrides[0].MemoryWarning == nil || *got.Overrides[0].MemoryWarning != 0 || got.Overrides[0].MemoryCritical != nil {
		t.Fatalf("%+v %v", got, err)
	}
	// Absent levels keep the defaults; 0 is off.
	if eff := got.For("env-2"); eff.DiskSpaceWarning != 90 || eff.MemoryWarning != 0 || eff.MemoryCritical != 95 || eff.TemperatureWarning != 70 {
		t.Fatalf("%+v", eff)
	}
	if got.For("env-1") != got.Thresholds {
		t.Fatal("env-1 has no override")
	}
	// A removed environment takes its override with it.
	if _, err := db.NewDelete().Table("environments").Where("id = ?", "env-2").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAlertSettings(ctx, db); len(got.Overrides) != 0 {
		t.Fatalf("%+v", got.Overrides)
	}
}
