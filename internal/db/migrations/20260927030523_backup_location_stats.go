package migrations

func init() {
	// Backups (#10): the measured size of each location (restic stats
	// --mode raw-data after every backup and prune): stored and
	// uncompressed bytes, compression ratio and progress, the restic
	// snapshot count and when it was measured (size_bytes already exists).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_locations ADD COLUMN uncompressed_bytes INTEGER NOT NULL DEFAULT 0 CHECK (uncompressed_bytes >= 0)`,
			`ALTER TABLE backup_locations ADD COLUMN compression_ratio REAL NOT NULL DEFAULT 0`,
			`ALTER TABLE backup_locations ADD COLUMN compression_progress REAL NOT NULL DEFAULT 0`,
			`ALTER TABLE backup_locations ADD COLUMN stats_snapshots INTEGER NOT NULL DEFAULT 0 CHECK (stats_snapshots >= 0)`,
			`ALTER TABLE backup_locations ADD COLUMN stats_at TEXT`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_locations DROP COLUMN stats_at`,
			`ALTER TABLE backup_locations DROP COLUMN stats_snapshots`,
			`ALTER TABLE backup_locations DROP COLUMN compression_progress`,
			`ALTER TABLE backup_locations DROP COLUMN compression_ratio`,
			`ALTER TABLE backup_locations DROP COLUMN uncompressed_bytes`,
		)),
	)
}
