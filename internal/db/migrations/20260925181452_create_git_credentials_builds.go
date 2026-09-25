package migrations

func init() {
	// Image builds (#33).
	//
	//   - git_credentials: owner-administered HTTPS Git credentials, sealed
	//     like registry connections (context "git_credentials/<id>/secret";
	//     empty exactly when revoked; keyed fingerprint only);
	//   - build_definitions: saved builds per environment (the source is a
	//     JSON document without credentials);
	//   - image_builds: one record per build with the requested source
	//     (build argument names only) and the outcome copied from its job.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE git_credentials (
				id                 TEXT    NOT NULL PRIMARY KEY,
				name               TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key           TEXT    NOT NULL UNIQUE,
				host               TEXT    NOT NULL CHECK (length(host) BETWEEN 1 AND 255),
				path_prefix        TEXT    NOT NULL DEFAULT '',
				username           TEXT    NOT NULL CHECK (length(username) BETWEEN 1 AND 255),
				plain_http         INTEGER NOT NULL DEFAULT 0 CHECK (plain_http IN (0, 1)),
				status             TEXT    NOT NULL CHECK (status IN ('active', 'revoked')),
				secret_sealed      TEXT    NOT NULL,
				secret_fingerprint TEXT    NOT NULL,
				secret_version     INTEGER NOT NULL CHECK (secret_version >= 1),
				secret_updated_at  TEXT    NOT NULL,
				last_used_at       TEXT,
				last_check_at      TEXT,
				last_check_result  TEXT    NOT NULL DEFAULT '',
				revoked_at         TEXT,
				revision           INTEGER NOT NULL CHECK (revision >= 1),
				created_at         TEXT    NOT NULL,
				updated_at         TEXT    NOT NULL,
				CHECK ((status = 'active') = (secret_sealed <> ''))
			) STRICT`,
			`CREATE INDEX git_credentials_host ON git_credentials (host)`,
			`CREATE TABLE build_definitions (
				id             TEXT    NOT NULL PRIMARY KEY,
				environment_id TEXT    NOT NULL REFERENCES environments (id),
				name           TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key       TEXT    NOT NULL,
				description    TEXT    NOT NULL DEFAULT '',
				source         TEXT    NOT NULL,
				last_build_id  TEXT    NOT NULL DEFAULT '',
				revision       INTEGER NOT NULL CHECK (revision >= 1),
				created_at     TEXT    NOT NULL,
				updated_at     TEXT    NOT NULL,
				UNIQUE (environment_id, name_key)
			) STRICT`,
			`CREATE TABLE image_builds (
				id                 TEXT    NOT NULL PRIMARY KEY,
				environment_id     TEXT    NOT NULL REFERENCES environments (id),
				job_id             TEXT    NOT NULL UNIQUE,
				definition_id      TEXT    NOT NULL DEFAULT '',
				git_url            TEXT    NOT NULL,
				ref                TEXT    NOT NULL DEFAULT '',
				context_path       TEXT    NOT NULL DEFAULT '',
				dockerfile         TEXT    NOT NULL DEFAULT '',
				target             TEXT    NOT NULL DEFAULT '',
				tags               TEXT    NOT NULL,
				platform           TEXT    NOT NULL DEFAULT '',
				no_cache           INTEGER NOT NULL DEFAULT 0 CHECK (no_cache IN (0, 1)),
				pull               INTEGER NOT NULL DEFAULT 0 CHECK (pull IN (0, 1)),
				build_arg_keys     TEXT    NOT NULL DEFAULT '[]',
				git_credential_id  TEXT    NOT NULL DEFAULT '',
				registry_ids       TEXT    NOT NULL DEFAULT '[]',
				status             TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'interrupted')),
				resolved_commit    TEXT    NOT NULL DEFAULT '',
				resolved_ref       TEXT    NOT NULL DEFAULT '',
				image_id           TEXT    NOT NULL DEFAULT '',
				error_class        TEXT    NOT NULL DEFAULT '',
				error_message      TEXT    NOT NULL DEFAULT '',
				initiator_user_id  TEXT    NOT NULL DEFAULT '',
				created_at         TEXT    NOT NULL,
				started_at         TEXT,
				finished_at        TEXT
			) STRICT`,
			`CREATE INDEX image_builds_environment ON image_builds (environment_id, id)`,
			`CREATE INDEX image_builds_definition ON image_builds (definition_id, id)`,
		)),
		Tx(Exec(
			`DROP TABLE image_builds`,
			`DROP TABLE build_definitions`,
			`DROP TABLE git_credentials`,
		)),
	)
}
