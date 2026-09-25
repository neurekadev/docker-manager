package migrations

func init() {
	// Compose stacks (#7): managed projects with their deployment intent
	// (last applied revision, applied images) and last observed Engine
	// state, and immutable revisions of their on-disk definition.
	//
	// Invariants enforced by the schema:
	//   - one stack per (environment, project name) and per project
	//     directory: importing or creating never adopts a project twice;
	//   - revisions are immutable (no UPDATE) and numbered per stack; they
	//     are deleted only with their stack;
	//   - revision contents are sealed with the secret-protection key
	//     (they hold .env values, #25: no separate secret store).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE stacks (
				id                   TEXT    PRIMARY KEY,
				environment_id       TEXT    NOT NULL REFERENCES environments (id) ON DELETE RESTRICT,
				name                 TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 63),
				display_name         TEXT    NOT NULL DEFAULT '',
				description          TEXT    NOT NULL DEFAULT '',
				icon                 TEXT    NOT NULL DEFAULT '',
				service_meta         TEXT    NOT NULL DEFAULT '{}',
				root                 TEXT    NOT NULL CHECK (root IN ('stacks', 'bind')),
				root_path            TEXT    NOT NULL DEFAULT '',
				dir                  TEXT    NOT NULL CHECK (length(dir) >= 1),
				config_files         TEXT    NOT NULL DEFAULT '[]',
				env_files            TEXT    NOT NULL DEFAULT '[]',
				origin               TEXT    NOT NULL CHECK (origin IN ('created', 'imported')),
				status               TEXT    NOT NULL CHECK (status IN ('undeployed', 'deployed', 'stopped', 'down', 'failed')),
				applied_revision_id  TEXT    NOT NULL DEFAULT '',
				applied_seq          INTEGER NOT NULL DEFAULT 0,
				applied_hash         TEXT    NOT NULL DEFAULT '',
				applied_at           TEXT,
				observed_revision_id TEXT    NOT NULL DEFAULT '',
				observed_seq         INTEGER NOT NULL DEFAULT 0,
				observed_hash        TEXT    NOT NULL DEFAULT '',
				observed_at          TEXT,
				failed_revision_id   TEXT    NOT NULL DEFAULT '',
				failed_seq           INTEGER NOT NULL DEFAULT 0,
				failed_hash          TEXT    NOT NULL DEFAULT '',
				images               TEXT    NOT NULL DEFAULT '[]',
				services             TEXT    NOT NULL DEFAULT '[]',
				binds                TEXT    NOT NULL DEFAULT '[]',
				previous_state       TEXT    NOT NULL DEFAULT '[]',
				engine_state         TEXT    NOT NULL DEFAULT 'unknown'
				                             CHECK (engine_state IN ('unknown', 'running', 'partial', 'stopped', 'missing')),
				engine_services      TEXT    NOT NULL DEFAULT '[]',
				engine_observed_at   TEXT,
				last_job_id          TEXT    NOT NULL DEFAULT '',
				last_job_kind        TEXT    NOT NULL DEFAULT '',
				revision             INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
				created_at           TEXT    NOT NULL,
				updated_at           TEXT    NOT NULL
			) STRICT`,
			`CREATE UNIQUE INDEX stacks_environment_name ON stacks (environment_id, name)`,
			`CREATE UNIQUE INDEX stacks_location ON stacks (environment_id, root, root_path, dir)`,
			`CREATE TABLE stack_revisions (
				id              TEXT    PRIMARY KEY,
				stack_id        TEXT    NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
				seq             INTEGER NOT NULL CHECK (seq >= 1),
				hash            TEXT    NOT NULL CHECK (length(hash) = 64),
				source          TEXT    NOT NULL CHECK (source IN ('deploy', 'editor', 'file_manager', 'external', 'restore')),
				author_user_id  TEXT    NOT NULL DEFAULT '',
				author_token_id TEXT    NOT NULL DEFAULT '',
				job_id          TEXT    NOT NULL DEFAULT '',
				restored_from   TEXT    NOT NULL DEFAULT '',
				files           TEXT    NOT NULL,
				content         TEXT    NOT NULL DEFAULT '',
				content_omitted INTEGER NOT NULL DEFAULT 0 CHECK (content_omitted IN (0, 1)),
				created_at      TEXT    NOT NULL,
				UNIQUE (stack_id, seq)
			) STRICT`,
			`CREATE INDEX stack_revisions_hash ON stack_revisions (stack_id, hash)`,
			`CREATE TRIGGER stack_revisions_immutable BEFORE UPDATE ON stack_revisions
			BEGIN
				SELECT RAISE(ABORT, 'stack revisions are immutable');
			END`,
		)),
		Tx(Exec(
			`DROP TABLE stack_revisions`,
			`DROP TABLE stacks`,
		)),
	)
}
