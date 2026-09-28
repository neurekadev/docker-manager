package migrations

func init() {
	// Backups (#10): a set whose members were all removed before their
	// turn (skipped: nothing was backed up and nothing failed) is
	// "skipped". SQLite cannot change a CHECK constraint in place, so the
	// table is rebuilt. Down turns such sets into failed ones (what they
	// were before skipped members existed).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE backup_sets_new (
				id                   TEXT NOT NULL PRIMARY KEY,
				policy_id            TEXT NOT NULL DEFAULT '',
				policy_name          TEXT NOT NULL DEFAULT '',
				origin               TEXT NOT NULL,
				state                TEXT NOT NULL CHECK (state IN ('pending', 'complete', 'partial', 'failed', 'skipped')),
				started_at           TEXT NOT NULL,
				finished_at          TEXT,
				members              TEXT NOT NULL DEFAULT '[]',
				manifest_snapshot_id TEXT NOT NULL DEFAULT '',
				follow_up            TEXT NOT NULL DEFAULT '' CHECK (follow_up IN ('', 'retention', 'done')),
				updated_at           TEXT NOT NULL
			) STRICT`,
			`INSERT INTO backup_sets_new (id, policy_id, policy_name, origin, state, started_at, finished_at, members,
				manifest_snapshot_id, follow_up, updated_at)
			 SELECT id, policy_id, policy_name, origin, state, started_at, finished_at, members,
				manifest_snapshot_id, follow_up, updated_at FROM backup_sets`,
			`DROP TABLE backup_sets`,
			`ALTER TABLE backup_sets_new RENAME TO backup_sets`,
			`CREATE INDEX backup_sets_policy ON backup_sets (policy_id, started_at)`,
			`CREATE INDEX backup_sets_follow_up ON backup_sets (follow_up)`,
		)),
		Tx(Exec(
			`UPDATE backup_sets SET state = 'failed' WHERE state = 'skipped'`,
			`CREATE TABLE backup_sets_old (
				id                   TEXT NOT NULL PRIMARY KEY,
				policy_id            TEXT NOT NULL DEFAULT '',
				policy_name          TEXT NOT NULL DEFAULT '',
				origin               TEXT NOT NULL,
				state                TEXT NOT NULL CHECK (state IN ('pending', 'complete', 'partial', 'failed')),
				started_at           TEXT NOT NULL,
				finished_at          TEXT,
				members              TEXT NOT NULL DEFAULT '[]',
				manifest_snapshot_id TEXT NOT NULL DEFAULT '',
				follow_up            TEXT NOT NULL DEFAULT '' CHECK (follow_up IN ('', 'retention', 'done')),
				updated_at           TEXT NOT NULL
			) STRICT`,
			`INSERT INTO backup_sets_old (id, policy_id, policy_name, origin, state, started_at, finished_at, members,
				manifest_snapshot_id, follow_up, updated_at)
			 SELECT id, policy_id, policy_name, origin, state, started_at, finished_at, members,
				manifest_snapshot_id, follow_up, updated_at FROM backup_sets`,
			`DROP TABLE backup_sets`,
			`ALTER TABLE backup_sets_old RENAME TO backup_sets`,
			`CREATE INDEX backup_sets_policy ON backup_sets (policy_id, started_at)`,
			`CREATE INDEX backup_sets_follow_up ON backup_sets (follow_up)`,
		)),
	)
}
