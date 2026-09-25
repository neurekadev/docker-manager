package migrations

func init() {
	// Docker maintenance (#14).
	//
	//   - maintenance_policies: prune policies per environment with their
	//     own schedule (cron, zone, enabled; #13), the rules of every
	//     category as a JSON document and the summary of the latest
	//     finished run (JSON, empty before the first one);
	//   - maintenance_defaults: the instance's suggested rules for new
	//     policies (one row; empty rules = DockYard's shipped suggestions).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE maintenance_policies (
				id               TEXT    NOT NULL PRIMARY KEY,
				environment_id   TEXT    NOT NULL REFERENCES environments (id),
				name             TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key         TEXT    NOT NULL,
				description      TEXT    NOT NULL DEFAULT '',
				cron             TEXT    NOT NULL,
				time_zone        TEXT    NOT NULL,
				schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1)),
				rules            TEXT    NOT NULL,
				last_run         TEXT    NOT NULL DEFAULT '',
				revision         INTEGER NOT NULL CHECK (revision >= 1),
				created_at       TEXT    NOT NULL,
				updated_at       TEXT    NOT NULL,
				UNIQUE (environment_id, name_key)
			) STRICT`,
			`CREATE TABLE maintenance_defaults (
				singleton  INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
				rules      TEXT    NOT NULL DEFAULT '',
				revision   INTEGER NOT NULL CHECK (revision >= 1),
				updated_at TEXT    NOT NULL
			) STRICT`,
			// SQLite's clock only because a migration has no injected clock.
			`INSERT INTO maintenance_defaults (singleton, rules, revision, updated_at)
				VALUES (1, '', 1, strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))`,
		)),
		Tx(Exec(
			`DROP TABLE maintenance_defaults`,
			`DROP TABLE maintenance_policies`,
		)),
	)
}
