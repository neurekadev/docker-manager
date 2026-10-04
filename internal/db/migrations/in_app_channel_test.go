package migrations

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The In App channel is added first in the list with every outcome, a
// channel already named "In App" is renamed, and the other channels keep
// their address and environments through the rebuild.
func TestInAppChannelIsBuiltIn(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261003090000", dir)

	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at)
		VALUES ('env-1', 'homelab', 'engine-1', 'install-1', 'active', 1, '2026-10-03 09:00:00+00:00', '2026-10-03 09:00:00+00:00')`); err != nil {
		t.Fatal(err)
	}
	c := domain.NotificationChannel{ID: "0192f0c4-1a2b-7c3d-8e4f-5a6b7c8d9e0f", Name: "In App", Service: "generic", Enabled: true,
		Subscriptions:      domain.NotificationSubscriptions{domain.NotifyBackup: {domain.OutcomeFailure}},
		EnvironmentIDs:     []string{"env-1"},
		AddressFingerprint: "fp_test", AddressVersion: 2, AddressUpdatedAt: now, Revision: 3, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertNotificationChannel(ctx, db, &c, "sealed-address"); err != nil {
		t.Fatal(err)
	}

	migrateTo(t, db, "", dir)
	list, err := store.ListNotificationChannels(ctx, db, "", 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	in := list[0]
	if !in.InApp() || in.Name != domain.InAppChannelName || in.Service != domain.InAppService || !in.Enabled || !in.AllEnvironments ||
		!in.Subscriptions.Equal(domain.AllNotificationSubscriptions()) || in.AddressVersion != 0 || in.Revision != 1 {
		t.Fatalf("in app %+v", in)
	}
	got, sealed, err := store.NotificationChannelWithSecret(ctx, db, c.ID)
	if err != nil || got.Name != "In App (Renamed)" || sealed != "sealed-address" || got.AddressVersion != 2 || got.Revision != 3 ||
		len(got.EnvironmentIDs) != 1 || got.EnvironmentIDs[0] != "env-1" {
		t.Fatalf("%+v %q %v", got, sealed, err)
	}
	// The environment filter still follows its environment.
	if _, err := db.ExecContext(ctx, `DELETE FROM environments WHERE id = 'env-1'`); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetNotificationChannel(ctx, db, c.ID); err != nil || len(got.EnvironmentIDs) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// Only the In App channel may be without an address.
	if _, err := db.ExecContext(ctx, `INSERT INTO notification_channels (id, name, name_key, service, secret_sealed,
		secret_fingerprint, secret_version, secret_updated_at, revision, created_at, updated_at)
		VALUES ('c-2', 'No address', 'no address', 'generic', '', '', 0, '', 1, '', '')`); err == nil {
		t.Fatal("a channel without an address was stored")
	}

	if _, err := migrate.NewMigrator(db, Migrations).Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListNotificationChannels(ctx, db, "", 0); err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("down: %+v %v", list, err)
	}
}
