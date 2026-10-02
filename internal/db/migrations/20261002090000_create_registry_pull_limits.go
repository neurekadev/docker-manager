package migrations

func init() {
	// Registry pull limits (#217): the last pull limit a registry reported
	// to the manager's checks, per registry host and credential.
	//
	//   - host is the normalized registry host (docker.io for Docker Hub);
	//     registry_connection_id is the connection whose credential was used,
	//     '' for anonymous access (no foreign key: '' names no connection;
	//     the rows of a connection are deleted with it);
	//   - pull_limit, remaining, window_seconds and reset_at are what the
	//     last response with limit headers said, observed_at when; they are
	//     NULL while the registry reported none (GHCR);
	//   - checked_at is the registry's last answer to a check;
	//   - last_limited_at is the last 429 answer, limited_until when that
	//     limit resets (NULL when the registry gave no time or a later answer
	//     was not a 429).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE registry_pull_limits (
				host                   TEXT    NOT NULL CHECK (length(host) BETWEEN 1 AND 255),
				registry_connection_id TEXT    NOT NULL DEFAULT '',
				pull_limit             INTEGER CHECK (pull_limit > 0),
				remaining              INTEGER CHECK (remaining >= 0),
				window_seconds         INTEGER CHECK (window_seconds > 0),
				reset_at               TEXT,
				observed_at            TEXT,
				checked_at             TEXT    NOT NULL,
				limited_until          TEXT,
				last_limited_at        TEXT,
				PRIMARY KEY (host, registry_connection_id)
			) STRICT`,
			`CREATE INDEX registry_pull_limits_connection ON registry_pull_limits (registry_connection_id)`,
		)),
		Tx(Exec(`DROP TABLE registry_pull_limits`)),
	)
}
