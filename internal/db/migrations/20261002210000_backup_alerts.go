package migrations

import "strings"

const (
	alertKindsWithoutBackup = `'disk_health', 'raid', 'temperature', 'disk_space', 'memory', 'environment_offline', 'updates', 'job_failed'`
	alertKindsWithBackup    = alertKindsWithoutBackup + `, 'backup'`
)

// rebuildAlerts replaces alerts (allowing kinds, keeping the rows keep
// selects) and alert_deliveries, which references it: SQLite cannot change
// a CHECK in place, and dropping alerts alone would delete its deliveries
// (ON DELETE CASCADE).
func rebuildAlerts(kinds, keep string) []string {
	deliveries := strings.Replace(deliveriesTable("alert_deliveries_new", "notifications"),
		"REFERENCES alerts (id)", "REFERENCES alerts_new (id)", 1)
	return []string{
		alertsTable("alerts_new", kinds),
		`INSERT INTO alerts_new (` + alertColumns + `) SELECT ` + alertColumns + ` FROM alerts WHERE ` + keep,
		deliveries,
		`INSERT INTO alert_deliveries_new (` + deliveryColumns + `) SELECT ` + deliveryColumns + ` FROM alert_deliveries
		 WHERE alert_id IS NULL OR alert_id IN (SELECT id FROM alerts_new)`,
		`DROP TABLE alert_deliveries`,
		`DROP TABLE alerts`,
		// Renaming alerts_new also renames the reference of
		// alert_deliveries_new.
		`ALTER TABLE alerts_new RENAME TO alerts`,
		`ALTER TABLE alert_deliveries_new RENAME TO alert_deliveries`,
		alertIndexes[0], alertIndexes[1], alertIndexes[2],
		deliveryIndexes[0], deliveryIndexes[1], deliveryIndexes[2], deliveryIndexes[3], deliveryIndexes[4],
		`CREATE INDEX alert_deliveries_notification ON alert_deliveries (notification_id) WHERE notification_id IS NOT NULL`,
	}
}

func init() {
	// Backups paused (#246): the backup setup without a Primary repository
	// raises an alert of kind backup. Down removes such alerts (and their
	// messages).
	Migrations.MustRegister(
		Tx(Exec(rebuildAlerts(alertKindsWithBackup, "1")...)),
		Tx(Exec(rebuildAlerts(alertKindsWithoutBackup, "kind <> 'backup'")...)),
	)
}
