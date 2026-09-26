package migrations

func init() {
	// Old explicit-source backup and multi-policy maintenance schedules cannot
	// be mapped unambiguously to the new one-policy-per-scope model. As with
	// legacy update policies, their configuration is removed; backup sets and
	// snapshots remain as recovery history.
	Migrations.MustRegister(
		Tx(Exec(
			`DELETE FROM backup_policies`,
			`ALTER TABLE backup_policies ADD COLUMN environment_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE backup_policies ADD COLUMN exclude_stacks TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE backup_policies ADD COLUMN exclude_volumes TEXT NOT NULL DEFAULT '[]'`,
			`CREATE UNIQUE INDEX backup_policy_scope ON backup_policies (environment_id)`,
			`DELETE FROM maintenance_policies`,
			`CREATE TABLE maintenance_policies_new (
				id TEXT NOT NULL PRIMARY KEY,
				environment_id TEXT NOT NULL DEFAULT '',
				name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				cron TEXT NOT NULL,
				time_zone TEXT NOT NULL,
				schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1)),
				rules TEXT NOT NULL,
				last_run TEXT NOT NULL DEFAULT '',
				revision INTEGER NOT NULL CHECK (revision >= 1),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT`,
			`DROP TABLE maintenance_policies`,
			`ALTER TABLE maintenance_policies_new RENAME TO maintenance_policies`,
			`CREATE UNIQUE INDEX maintenance_policy_scope ON maintenance_policies (environment_id)`,
		)),
		Tx(Exec(
			`DROP INDEX maintenance_policy_scope`,
			`CREATE TABLE maintenance_policies_old (
				id TEXT NOT NULL PRIMARY KEY,
				environment_id TEXT NOT NULL REFERENCES environments (id),
				name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				cron TEXT NOT NULL,
				time_zone TEXT NOT NULL,
				schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1)),
				rules TEXT NOT NULL,
				last_run TEXT NOT NULL DEFAULT '',
				revision INTEGER NOT NULL CHECK (revision >= 1),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				UNIQUE (environment_id, name_key)
			) STRICT`,
			`DROP TABLE maintenance_policies`,
			`ALTER TABLE maintenance_policies_old RENAME TO maintenance_policies`,
			`DROP INDEX backup_policy_scope`,
			`ALTER TABLE backup_policies DROP COLUMN exclude_volumes`,
			`ALTER TABLE backup_policies DROP COLUMN exclude_stacks`,
			`ALTER TABLE backup_policies DROP COLUMN environment_id`,
		)),
	)
}
