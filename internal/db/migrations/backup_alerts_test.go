package migrations

import (
	"testing"

	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Alerts may be of kind backup (backups paused); the rebuild keeps every
// alert and message, and down drops the backup alerts with their messages.
func TestAlertsGetTheBackupKind(t *testing.T) {
	ctx := testutil.Context(t)
	db := migratedFrom(t, ctx, "20261002210000", "", []string{
		`INSERT INTO alerts (id, dedupe_key, kind, severity, state, title, started_at, updated_at, last_seen_at, revision) VALUES
			('a-disk', 'disk/sda', 'disk_health', 'critical', 'firing', 'Disk failing', '2026-10-02 09:00:00+00:00',
				'2026-10-02 09:00:00+00:00', '2026-10-02 09:00:00+00:00', 1)`,
		`INSERT INTO notifications (id, kind, outcome, title, created_at) VALUES
			('n-prune', 'prune', 'failure', 'Prune failed', '2026-10-02 09:00:00+00:00')`,
		`INSERT INTO alert_deliveries (id, alert_id, notification_id, channel_id, event, kind, severity, title, state,
			next_attempt_at, created_at, updated_at) VALUES
			('d-disk', 'a-disk', NULL, 'c-1', 'firing', 'disk_health', 'critical', 'Disk failing', 'pending',
				'2026-10-02 09:00:30+00:00', '2026-10-02 09:00:00+00:00', '2026-10-02 09:00:00+00:00'),
			('d-prune', NULL, 'n-prune', 'c-1', 'notification', 'prune', 'critical', 'Prune failed', 'sent',
				'2026-10-02 09:00:30+00:00', '2026-10-02 09:00:00+00:00', '2026-10-02 09:00:00+00:00')`,
	})
	expect(t, ctx, db, `SELECT id FROM alerts`, "a-disk")
	expect(t, ctx, db, `SELECT id FROM alert_deliveries ORDER BY id`, "d-disk", "d-prune")
	for _, s := range []string{
		`INSERT INTO alerts (id, dedupe_key, kind, severity, state, title, started_at, updated_at, last_seen_at, revision) VALUES
			('a-backup', 'backup/no_primary', 'backup', 'critical', 'firing', 'Backups paused', '2026-10-02 10:00:00+00:00',
				'2026-10-02 10:00:00+00:00', '2026-10-02 10:00:00+00:00', 1)`,
		`INSERT INTO alert_deliveries (id, alert_id, channel_id, event, kind, severity, title, state, next_attempt_at,
			created_at, updated_at) VALUES
			('d-backup', 'a-backup', 'c-1', 'firing', 'backup', 'critical', 'Backups paused', 'pending',
				'2026-10-02 10:00:30+00:00', '2026-10-02 10:00:00+00:00', '2026-10-02 10:00:00+00:00')`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// The messages still belong to their alerts.
	if _, err := db.ExecContext(ctx, `DELETE FROM alerts WHERE id = 'a-disk'`); err != nil {
		t.Fatal(err)
	}
	expect(t, ctx, db, `SELECT id FROM alert_deliveries ORDER BY id`, "d-backup", "d-prune")

	// Down: the backup alert goes, with its message.
	if _, err := migrate.NewMigrator(db, Migrations).Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, ctx, db, `SELECT id FROM alerts`)
	expect(t, ctx, db, `SELECT id FROM alert_deliveries ORDER BY id`, "d-prune")
	if _, err := db.ExecContext(ctx, `INSERT INTO alerts (id, dedupe_key, kind, severity, state, title, started_at, updated_at,
		last_seen_at, revision) VALUES ('a-backup', 'backup/no_primary', 'backup', 'critical', 'firing', 'Backups paused',
		'2026-10-02 10:00:00+00:00', '2026-10-02 10:00:00+00:00', '2026-10-02 10:00:00+00:00', 1)`); err == nil {
		t.Fatal("down still accepts backup alerts")
	}
}
