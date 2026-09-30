package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func notificationChannel(id, name string, envs ...string) domain.NotificationChannel {
	now := testutil.Epoch
	return domain.NotificationChannel{ID: id, Name: name, Service: "ntfy", Target: "ntfy.example.com", Enabled: true,
		EventKinds: []domain.NotificationEventKind{domain.NotifyJobFailed, domain.NotifyRAID}, SendResolved: true, EnvironmentIDs: envs,
		AddressFingerprint: "fp_0000000000000001", AddressVersion: 1, AddressUpdatedAt: now, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func insertNotificationChannel(t *testing.T, ctx context.Context, db *bun.DB, c *domain.NotificationChannel, sealed string) error {
	t.Helper()
	return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.InsertNotificationChannel(ctx, tx, c, sealed)
	})
}

func TestNotificationChannelsRoundTrip(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	for _, id := range []string{"env-1", "env-2"} {
		env := domain.Environment{ID: id, Name: id, EngineID: "E-" + id, InstallID: "i-" + id, Status: domain.EnvironmentActive,
			Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
		if err := store.InsertEnvironment(ctx, db, &env); err != nil {
			t.Fatal(err)
		}
	}
	a := notificationChannel("c-1", "Ops", "env-2", "env-1")
	b := notificationChannel("c-2", "Everyone")
	if err := insertNotificationChannel(t, ctx, db, &a, "dy1.k.sealed-a"); err != nil {
		t.Fatal(err)
	}
	if err := insertNotificationChannel(t, ctx, db, &b, "dy1.k.sealed-b"); err != nil {
		t.Fatal(err)
	}
	dup := notificationChannel("c-3", " OPS ")
	if err := insertNotificationChannel(t, ctx, db, &dup, "dy1.k.sealed-c"); !errors.Is(err, domain.ErrNotificationChannelNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	got, err := store.GetNotificationChannel(ctx, db, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ops" || got.Service != "ntfy" || got.Target != "ntfy.example.com" || !got.Enabled || !got.SendResolved ||
		len(got.EventKinds) != 2 || got.EventKinds[0] != domain.NotifyJobFailed || len(got.EnvironmentIDs) != 2 ||
		got.EnvironmentIDs[0] != "env-1" || got.AddressFingerprint != "fp_0000000000000001" || got.Revision != 1 {
		t.Fatalf("%+v", got)
	}
	list, err := store.ListNotificationChannels(ctx, db, "", 0)
	if err != nil || len(list) != 2 || list[0].ID != "c-1" || len(list[1].EnvironmentIDs) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	if page, _ := store.ListNotificationChannels(ctx, db, "c-1", 1); len(page) != 1 || page[0].ID != "c-2" {
		t.Fatalf("page %+v", page)
	}
	sealed, version, err := store.NotificationChannelSecret(ctx, db, "c-1")
	if err != nil || sealed != "dy1.k.sealed-a" || version != 1 {
		t.Fatalf("%q %d %v", sealed, version, err)
	}

	// Settings only: the sealed address stays.
	next := got
	next.Name, next.Enabled, next.EnvironmentIDs, next.Revision = "Ops team", false, nil, 2
	if err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.UpdateNotificationChannel(ctx, tx, &next, "", 1)
	}); err != nil {
		t.Fatal(err)
	}
	if sealed, _, _ := store.NotificationChannelSecret(ctx, db, "c-1"); sealed != "dy1.k.sealed-a" {
		t.Fatalf("address changed: %q", sealed)
	}
	got, _ = store.GetNotificationChannel(ctx, db, "c-1")
	if got.Name != "Ops team" || got.Enabled || len(got.EnvironmentIDs) != 0 || got.Revision != 2 {
		t.Fatalf("%+v", got)
	}
	// A stale revision is refused.
	if err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.UpdateNotificationChannel(ctx, tx, &next, "", 1)
	}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale: %v", err)
	}

	// Results are status, not configuration.
	at := testutil.Epoch.Add(time.Minute)
	if err := store.RecordNotificationResult(ctx, db, "c-1", at, domain.NotifyErrHTTP4xx, false); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetNotificationChannel(ctx, db, "c-1")
	if got.LastResult != domain.NotifyErrHTTP4xx || got.LastAttemptAt == nil || !got.LastAttemptAt.Equal(at) || got.LastSuccessAt != nil ||
		got.Revision != 2 {
		t.Fatalf("%+v", got)
	}

	// A new address resets the result.
	next = got
	next.AddressVersion, next.AddressFingerprint, next.LastResult, next.LastAttemptAt, next.Revision = 2, "fp_2", "", nil, 3
	if err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.UpdateNotificationChannel(ctx, tx, &next, "dy1.k.sealed-new", 2)
	}); err != nil {
		t.Fatal(err)
	}
	sealed, version, _ = store.NotificationChannelSecret(ctx, db, "c-1")
	got, _ = store.GetNotificationChannel(ctx, db, "c-1")
	if sealed != "dy1.k.sealed-new" || version != 2 || got.LastResult != "" || got.LastAttemptAt != nil {
		t.Fatalf("%q %d %+v", sealed, version, got)
	}

	if err := store.DeleteNotificationChannel(ctx, db, "c-1", 2); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := store.DeleteNotificationChannel(ctx, db, "c-1", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetNotificationChannel(ctx, db, "c-1"); !errors.Is(err, domain.ErrNotificationChannelNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	var envRows int
	if err := db.NewSelect().Table("notification_channel_environments").ColumnExpr("count(*)").Scan(ctx, &envRows); err != nil || envRows != 0 {
		t.Fatalf("environment rows left: %d %v", envRows, err)
	}
	if _, _, err := store.NotificationChannelSecret(ctx, db, "c-1"); !errors.Is(err, domain.ErrNotificationChannelNotFound) {
		t.Fatalf("secret of a deleted channel: %v", err)
	}
}
