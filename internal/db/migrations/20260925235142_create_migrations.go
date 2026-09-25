package migrations

func init() {
	// Environment migrations (#35): one record per stack.migrate /
	// volume.migrate job (id = job ID). detail holds the source location,
	// volume mapping, transferred parts with their checksums and the
	// services that ran on the source before it stopped.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE migrations (
				id                    TEXT    NOT NULL PRIMARY KEY,
				kind                  TEXT    NOT NULL CHECK (kind IN ('stack', 'volume')),
				stack_id              TEXT    NOT NULL DEFAULT '',
				volume                TEXT    NOT NULL DEFAULT '',
				source_environment_id TEXT    NOT NULL,
				target_environment_id TEXT    NOT NULL,
				state                 TEXT    NOT NULL CHECK (state IN ('running', 'completed', 'source_removed', 'failed', 'cancelled', 'interrupted')),
				cut_over              INTEGER NOT NULL DEFAULT 0 CHECK (cut_over IN (0, 1)),
				target_partial        INTEGER NOT NULL DEFAULT 0 CHECK (target_partial IN (0, 1)),
				bytes                 INTEGER NOT NULL DEFAULT 0,
				detail                TEXT    NOT NULL DEFAULT '{}',
				deploy_job_id         TEXT    NOT NULL DEFAULT '',
				removal_job_id        TEXT    NOT NULL DEFAULT '',
				created_at            TEXT    NOT NULL,
				updated_at            TEXT    NOT NULL,
				finished_at           TEXT,
				CHECK (source_environment_id <> target_environment_id)
			) STRICT`,
			`CREATE INDEX migrations_stack ON migrations (stack_id, created_at)`,
			`CREATE INDEX migrations_target ON migrations (target_environment_id, state)`,
		)),
		Tx(Exec(`DROP TABLE migrations`)),
	)
}
