package migrations

func init() {
	// Registry connections (#19): manager-owned registry credentials.
	//
	//   - host is normalized (docker.io for every Docker Hub alias);
	//   - secret_sealed is the credential sealed with the secret-protection
	//     key (context "registry_connections/<id>/secret"); it is empty
	//     exactly when the connection is revoked;
	//   - secret_fingerprint is a keyed fingerprint (never the secret);
	//   - environment_id binds a connection to one environment; stack_id to
	//     one stack (stacks arrive with #7, so no foreign key yet).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE registry_connections (
				id                 TEXT    NOT NULL PRIMARY KEY,
				name               TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key           TEXT    NOT NULL UNIQUE,
				host               TEXT    NOT NULL CHECK (length(host) BETWEEN 1 AND 255),
				credential_type    TEXT    NOT NULL CHECK (credential_type IN ('password', 'token')),
				username           TEXT    NOT NULL CHECK (length(username) <= 255),
				repository_pattern TEXT    NOT NULL DEFAULT '',
				environment_id     TEXT    REFERENCES environments (id),
				stack_id           TEXT    NOT NULL DEFAULT '',
				priority           INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN -1000 AND 1000),
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
			`CREATE INDEX registry_connections_host ON registry_connections (host)`,
		)),
		Tx(Exec(`DROP TABLE registry_connections`)),
	)
}
