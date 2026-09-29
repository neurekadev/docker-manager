package migrations

func init() {
	// Environment migrations (#35): one record per environment.migrate job
	// (id = job ID). detail holds the groups in their order, each stack's
	// state and stack migration, and the networks created on the
	// destination. The stacks' own migrations stay in migrations.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE environment_migrations (
				id                    TEXT NOT NULL PRIMARY KEY,
				source_environment_id TEXT NOT NULL,
				target_environment_id TEXT NOT NULL,
				state                 TEXT NOT NULL CHECK (state IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
				detail                TEXT NOT NULL DEFAULT '{}',
				created_at            TEXT NOT NULL,
				updated_at            TEXT NOT NULL,
				finished_at           TEXT,
				CHECK (source_environment_id <> target_environment_id)
			) STRICT`,
			`CREATE INDEX environment_migrations_source ON environment_migrations (source_environment_id, created_at)`,
		)),
		Tx(Exec(`DROP TABLE environment_migrations`)),
	)
}
