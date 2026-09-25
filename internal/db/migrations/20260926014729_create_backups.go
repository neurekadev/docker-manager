package migrations

func init() {
	// Backups (#10, #24): instance-owned repositories, the Recovery Key
	// state, policies, backup sets and the snapshot index.
	//
	//   - backup_key_state holds the instance Recovery Key sealed with the
	//     secret-protection key (context "backup_key_state/<slot>"): the
	//     current key, a generated key awaiting confirmation (pending) and,
	//     during a rotation, the previous key. Only fingerprints are
	//     readable through the API.
	//   - backup_repositories: destinations (local on one executor, or S3
	//     with sealed credentials "backup_repositories/<id>/access_key" and
	//     ".../secret_key").
	//   - backup_locations: the physical restic repository of each scope
	//     (manager, env:<id>) below a destination, with the key generation
	//     it is known to use.
	//   - backup_policies: selections, schedule (#13) and retention as JSON.
	//   - backup_sets / backup_snapshots: runs and the snapshot index. Sets
	//     and snapshots outlive their policy (history).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE backup_key_state (
				id                   INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
				generation           INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
				current_sealed       TEXT    NOT NULL DEFAULT '',
				current_fingerprint  TEXT    NOT NULL DEFAULT '',
				created_at           TEXT,
				confirmed_at         TEXT,
				pending_sealed       TEXT    NOT NULL DEFAULT '',
				pending_fingerprint  TEXT    NOT NULL DEFAULT '',
				pending_created_at   TEXT,
				previous_sealed      TEXT    NOT NULL DEFAULT '',
				previous_fingerprint TEXT    NOT NULL DEFAULT '',
				rotation_started_at  TEXT,
				revision             INTEGER NOT NULL CHECK (revision >= 1),
				updated_at           TEXT    NOT NULL,
				CHECK ((generation = 0) = (current_sealed = '')),
				CHECK ((pending_sealed = '') = (pending_fingerprint = '')),
				CHECK ((previous_sealed = '') = (previous_fingerprint = ''))
			) STRICT`,
			`CREATE TABLE backup_repositories (
				id                     TEXT    NOT NULL PRIMARY KEY,
				name                   TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key               TEXT    NOT NULL UNIQUE,
				kind                   TEXT    NOT NULL CHECK (kind IN ('local', 's3')),
				executor               TEXT    NOT NULL DEFAULT '',
				path                   TEXT    NOT NULL DEFAULT '',
				endpoint               TEXT    NOT NULL DEFAULT '',
				bucket                 TEXT    NOT NULL DEFAULT '',
				prefix                 TEXT    NOT NULL DEFAULT '',
				region                 TEXT    NOT NULL DEFAULT '',
				path_style             INTEGER NOT NULL DEFAULT 0 CHECK (path_style IN (0, 1)),
				access_key_sealed      TEXT    NOT NULL DEFAULT '',
				secret_key_sealed      TEXT    NOT NULL DEFAULT '',
				credential_fingerprint TEXT    NOT NULL DEFAULT '',
				state                  TEXT    NOT NULL CHECK (state IN ('awaiting_confirmation', 'ready')),
				confirmed_at           TEXT,
				verify_cron            TEXT    NOT NULL,
				verify_time_zone       TEXT    NOT NULL,
				verify_enabled         INTEGER NOT NULL DEFAULT 0 CHECK (verify_enabled IN (0, 1)),
				verify_read_data       TEXT    NOT NULL DEFAULT '',
				last_test_at           TEXT,
				last_test_result       TEXT    NOT NULL DEFAULT '',
				last_test              TEXT    NOT NULL DEFAULT '',
				revision               INTEGER NOT NULL CHECK (revision >= 1),
				created_at             TEXT    NOT NULL,
				updated_at             TEXT    NOT NULL,
				CHECK ((kind = 'local') = (path <> '')),
				CHECK ((kind = 'local') = (executor <> '')),
				CHECK ((kind = 's3') = (bucket <> ''))
			) STRICT`,
			`CREATE TABLE backup_locations (
				repository_id        TEXT    NOT NULL REFERENCES backup_repositories (id) ON DELETE CASCADE,
				scope                TEXT    NOT NULL,
				restic_repository_id TEXT    NOT NULL DEFAULT '',
				key_generation       INTEGER NOT NULL DEFAULT 0,
				initialized_at       TEXT,
				last_backup_at       TEXT,
				last_verified_at     TEXT,
				last_verify_result   TEXT    NOT NULL DEFAULT '',
				last_verify_job_id   TEXT    NOT NULL DEFAULT '',
				size_bytes           INTEGER NOT NULL DEFAULT 0,
				updated_at           TEXT    NOT NULL,
				PRIMARY KEY (repository_id, scope)
			) STRICT`,
			`CREATE TABLE backup_policies (
				id                TEXT    NOT NULL PRIMARY KEY,
				name              TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key          TEXT    NOT NULL UNIQUE,
				repository_id     TEXT    NOT NULL REFERENCES backup_repositories (id),
				environment_repos TEXT    NOT NULL DEFAULT '{}',
				include_manager   INTEGER NOT NULL DEFAULT 0 CHECK (include_manager IN (0, 1)),
				include_metrics   INTEGER NOT NULL DEFAULT 0 CHECK (include_metrics IN (0, 1)),
				stacks            TEXT    NOT NULL DEFAULT '[]',
				volumes           TEXT    NOT NULL DEFAULT '[]',
				shutdown          INTEGER NOT NULL DEFAULT 0 CHECK (shutdown IN (0, 1)),
				cron              TEXT    NOT NULL,
				time_zone         TEXT    NOT NULL,
				enabled           INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
				retention         TEXT    NOT NULL DEFAULT '{}',
				revision          INTEGER NOT NULL CHECK (revision >= 1),
				created_at        TEXT    NOT NULL,
				updated_at        TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE backup_sets (
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
			`CREATE INDEX backup_sets_policy ON backup_sets (policy_id, started_at)`,
			`CREATE INDEX backup_sets_follow_up ON backup_sets (follow_up)`,
			`CREATE TABLE backup_snapshots (
				id                 TEXT    NOT NULL PRIMARY KEY,
				set_id             TEXT    NOT NULL DEFAULT '',
				policy_id          TEXT    NOT NULL DEFAULT '',
				repository_id      TEXT    NOT NULL,
				scope              TEXT    NOT NULL,
				environment_id     TEXT    NOT NULL DEFAULT '',
				kind               TEXT    NOT NULL CHECK (kind IN ('manager_state', 'stack', 'volume')),
				item               TEXT    NOT NULL,
				stack_id           TEXT    NOT NULL DEFAULT '',
				stack_name         TEXT    NOT NULL DEFAULT '',
				volume             TEXT    NOT NULL DEFAULT '',
				restic_snapshot_id TEXT    NOT NULL,
				snapshot_time      TEXT    NOT NULL,
				paths              TEXT    NOT NULL DEFAULT '[]',
				volumes            TEXT    NOT NULL DEFAULT '[]',
				consistency        TEXT    NOT NULL DEFAULT '',
				state              TEXT    NOT NULL CHECK (state IN ('complete', 'partial')),
				error_class        TEXT    NOT NULL DEFAULT '',
				bytes_added        INTEGER NOT NULL DEFAULT 0,
				bytes_total        INTEGER NOT NULL DEFAULT 0,
				files              INTEGER NOT NULL DEFAULT 0,
				job_id             TEXT    NOT NULL DEFAULT '',
				verified_at        TEXT,
				forgotten_at       TEXT,
				created_at         TEXT    NOT NULL,
				UNIQUE (repository_id, scope, restic_snapshot_id)
			) STRICT`,
			`CREATE INDEX backup_snapshots_set ON backup_snapshots (set_id)`,
			`CREATE INDEX backup_snapshots_item ON backup_snapshots (repository_id, scope, item, snapshot_time)`,
			`CREATE INDEX backup_snapshots_stack ON backup_snapshots (stack_id)`,
		)),
		Tx(Exec(
			`DROP TABLE backup_snapshots`,
			`DROP TABLE backup_sets`,
			`DROP TABLE backup_policies`,
			`DROP TABLE backup_locations`,
			`DROP TABLE backup_repositories`,
			`DROP TABLE backup_key_state`,
		)),
	)
}
