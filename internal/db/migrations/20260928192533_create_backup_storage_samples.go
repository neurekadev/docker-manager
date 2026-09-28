package migrations

func init() {
	// Backups (#10): the storage history of each location, one sample
	// (stored and uncompressed bytes) every time its size is measured (after
	// every backup and prune), at most one per location and UTC hour (hour =
	// Unix seconds / 3600; a later measurement in the same hour replaces
	// it). No foreign key: removing a repository appends a zero sample
	// instead, so its storage stops counting from then on while the time
	// before keeps it. Samples older than two years are pruned (the newest
	// older one of each location stays as the value carried into the
	// range). Every location measured before this migration starts with
	// its current size as the first sample.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE backup_storage_samples (
				repository_id      TEXT    NOT NULL,
				scope              TEXT    NOT NULL,
				hour               INTEGER NOT NULL,
				at                 TEXT    NOT NULL,
				size_bytes         INTEGER NOT NULL CHECK (size_bytes >= 0),
				uncompressed_bytes INTEGER NOT NULL CHECK (uncompressed_bytes >= 0),
				PRIMARY KEY (repository_id, scope, hour)
			) STRICT`,
			`CREATE INDEX backup_storage_samples_at ON backup_storage_samples (at)`,
			`INSERT INTO backup_storage_samples (repository_id, scope, hour, at, size_bytes, uncompressed_bytes)
				SELECT repository_id, scope, CAST(strftime('%s', stats_at) AS INTEGER) / 3600, stats_at,
					max(size_bytes, 0), max(uncompressed_bytes, 0)
				FROM backup_locations WHERE stats_at IS NOT NULL AND strftime('%s', stats_at) IS NOT NULL`,
		)),
		Tx(Exec(
			`DROP TABLE backup_storage_samples`,
		)),
	)
}
