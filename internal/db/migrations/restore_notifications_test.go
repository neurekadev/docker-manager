package migrations

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestRestoreSubscriptionsFollowBackups(t *testing.T) {
	for _, c := range []struct {
		in, want string
		changed  bool
	}{
		// Backups' outcomes restores have.
		{`{"backup":["failure","warning","success"],"prune":["failure"]}`,
			`{"backup":["failure","warning","success"],"prune":["failure"],"restore":["failure","success"]}`, true},
		{`{"backup":["warning","success"]}`, `{"backup":["warning","success"],"restore":["success"]}`, true},
		// Warnings only: restores have none, so none.
		{`{"backup":["warning"]}`, `{"backup":["warning"]}`, false},
		// No backups: no restores.
		{`{"prune":["failure"]}`, `{"prune":["failure"]}`, false},
	} {
		got, changed, err := restoreSubscriptions(c.in)
		if err != nil || got != c.want || changed != c.changed {
			t.Errorf("%s: %s %v (%v), want %s", c.in, got, changed, err, c.want)
		}
	}
	if got, changed, err := withoutRestores(`{"backup":["failure"],"restore":["failure"]}`); err != nil || !changed || got != `{"backup":["failure"]}` {
		t.Errorf("down: %s %v %v", got, changed, err)
	}
	if _, _, err := restoreSubscriptions("not json"); err == nil {
		t.Error("broken subscriptions accepted")
	}
}

// migrateTo applies the migrations named before name (all of them when
// name is "") to db.
func migrateTo(t *testing.T, db *bun.DB, name, dir string) {
	t.Helper()
	ms := Migrations
	if name != "" {
		ms = migrate.NewMigrations()
		for _, m := range Migrations.Sorted() {
			if m.Name < name {
				ms.Add(m)
			}
		}
	}
	if _, err := store.Migrate(testutil.Context(t), db, store.MigrateOptions{Migrations: ms, SnapshotDir: filepath.Join(dir, "snapshots"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
}

// Recorded restores move to their own kind with their messages (none is
// lost to the rebuild), and channels sending backups send restores too.
func TestRestoresGetTheirOwnKind(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261002120000", dir)

	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, s := range []string{
		`INSERT INTO notifications (id, kind, outcome, job_kind, title, created_at) VALUES
			('n-restore', 'backup', 'success', 'restore.run', 'Restore of shop succeeded', '2026-10-01 09:00:00+00:00'),
			('n-backup', 'backup', 'failure', 'backup.run', 'Backup Nightly failed', '2026-10-01 09:00:00+00:00')`,
		`INSERT INTO alert_deliveries (id, notification_id, channel_id, event, kind, severity, outcome, title, state, next_attempt_at,
			created_at, updated_at) VALUES
			('d-restore', 'n-restore', 'c-1', 'notification', 'backup', 'info', 'success', 'Restore of shop succeeded', 'pending',
				'2026-10-01 09:00:30+00:00', '2026-10-01 09:00:00+00:00', '2026-10-01 09:00:00+00:00'),
			('d-backup', 'n-backup', 'c-1', 'notification', 'backup', 'critical', 'failure', 'Backup Nightly failed', 'sent',
				'2026-10-01 09:00:30+00:00', '2026-10-01 09:00:00+00:00', '2026-10-01 09:00:00+00:00')`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	channel := func(name string, subs domain.NotificationSubscriptions) string {
		c := domain.NotificationChannel{ID: name, Name: name, Service: "generic", Enabled: true, Subscriptions: subs, AllEnvironments: true,
			AddressFingerprint: "fp_test", AddressVersion: 1, AddressUpdatedAt: now, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertNotificationChannel(ctx, db, &c, "sealed-"+name); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	backups := channel("backups", domain.NotificationSubscriptions{domain.NotifyBackup: {domain.OutcomeFailure, domain.OutcomeWarning}})
	prunes := channel("prunes", domain.NotificationSubscriptions{domain.NotifyPrune: {domain.OutcomeFailure}})

	migrateTo(t, db, "", dir)
	kinds := func(table string) map[string]string {
		rows, err := db.QueryContext(ctx, `SELECT id, kind FROM `+table)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		out := map[string]string{}
		for rows.Next() {
			var id, kind string
			if err := rows.Scan(&id, &kind); err != nil {
				t.Fatal(err)
			}
			out[id] = kind
		}
		return out
	}
	if got := kinds("notifications"); got["n-restore"] != "restore" || got["n-backup"] != "backup" {
		t.Fatalf("notifications %v", got)
	}
	if got := kinds("alert_deliveries"); len(got) != 2 || got["d-restore"] != "restore" || got["d-backup"] != "backup" {
		t.Fatalf("deliveries %v", got)
	}
	// The deliveries still belong to their notifications.
	if _, err := db.ExecContext(ctx, `DELETE FROM notifications WHERE id = 'n-backup'`); err != nil {
		t.Fatal(err)
	}
	if got := kinds("alert_deliveries"); len(got) != 1 {
		t.Fatalf("deliveries after deleting a notification %v", got)
	}
	c, err := store.GetNotificationChannel(ctx, db, backups)
	if err != nil || !slices.Equal(c.Subscriptions[domain.NotifyRestore], []domain.NotificationOutcome{domain.OutcomeFailure}) {
		t.Fatalf("%+v %v", c.Subscriptions, err)
	}
	if c, err := store.GetNotificationChannel(ctx, db, prunes); err != nil || len(c.Subscriptions[domain.NotifyRestore]) != 0 {
		t.Fatalf("%+v %v", c.Subscriptions, err)
	}

	// Down: restores are backups again.
	if _, err := migrate.NewMigrator(db, Migrations).Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kinds("notifications"); got["n-restore"] != "backup" {
		t.Fatalf("down: %v", got)
	}
	if got := kinds("alert_deliveries"); got["d-restore"] != "backup" {
		t.Fatalf("down: %v", got)
	}
	if c, err := store.GetNotificationChannel(ctx, db, backups); err != nil || len(c.Subscriptions[domain.NotifyRestore]) != 0 {
		t.Fatalf("down: %+v %v", c.Subscriptions, err)
	}
}
