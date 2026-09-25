package migrations

func init() {
	// Diagnostics (#34): the public API request that created a job, so the
	// agent command carries it (frame requestId) and agent logs correlate
	// with the manager's. Empty for scheduled work.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE jobs ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`,
		)),
		Tx(Exec(
			`ALTER TABLE jobs DROP COLUMN request_id`,
		)),
	)
}
