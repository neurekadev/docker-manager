package migrations

func init() {
	// Environments, agents, agent credentials and enrollment tokens (#3).
	// Secrets are never stored: credentials and enrollment tokens keep only
	// the SHA-256 verifier of their secret part (internal/manager/authsep);
	// a pending credential rotation keeps its credential sealed with the
	// secret-protection key until the agent confirms it.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE environments (
				id                        TEXT    PRIMARY KEY,
				name                      TEXT    NOT NULL,
				service_address           TEXT    NOT NULL DEFAULT '',
				engine_id                 TEXT    NOT NULL,
				install_id                TEXT    NOT NULL,
				agent_id                  TEXT,
				status                    TEXT    NOT NULL CHECK (status IN ('active', 'archived')),
				online                    INTEGER NOT NULL DEFAULT 0 CHECK (online IN (0, 1)),
				connection_changed_at     TEXT,
				last_seen_at              TEXT,
				allow_duplicate_engine_id INTEGER NOT NULL DEFAULT 0 CHECK (allow_duplicate_engine_id IN (0, 1)),
				revision                  INTEGER NOT NULL,
				created_at                TEXT    NOT NULL,
				updated_at                TEXT    NOT NULL,
				archived_at               TEXT
			) STRICT`,
			`CREATE INDEX environments_engine ON environments (engine_id)`,
			`CREATE TABLE agent_enrollments (
				id                        TEXT    PRIMARY KEY,
				verifier                  TEXT    NOT NULL,
				intent                    TEXT    NOT NULL CHECK (intent IN ('new', 'replace', 'reattach')),
				target_id                 TEXT    NOT NULL DEFAULT '',
				environment_name          TEXT    NOT NULL DEFAULT '',
				allow_duplicate_engine_id INTEGER NOT NULL DEFAULT 0 CHECK (allow_duplicate_engine_id IN (0, 1)),
				created_by                TEXT    NOT NULL DEFAULT '',
				created_at                TEXT    NOT NULL,
				expires_at                TEXT    NOT NULL,
				used_at                   TEXT,
				revoked_at                TEXT,
				agent_id                  TEXT    NOT NULL DEFAULT '',
				rejection                 TEXT    NOT NULL DEFAULT ''
			) STRICT`,
			`CREATE INDEX agent_enrollments_expires ON agent_enrollments (expires_at)`,
			`CREATE TABLE agents (
				id               TEXT    PRIMARY KEY,
				environment_id   TEXT    NOT NULL REFERENCES environments (id),
				enrollment_id    TEXT    NOT NULL,
				install_id       TEXT    NOT NULL,
				engine_id        TEXT    NOT NULL,
				hostname         TEXT    NOT NULL DEFAULT '',
				label            TEXT    NOT NULL DEFAULT '',
				version          TEXT    NOT NULL,
				version_status   TEXT    NOT NULL,
				status           TEXT    NOT NULL CHECK (status IN ('active', 'revoked')),
				revoked_reason   TEXT    NOT NULL DEFAULT '',
				capabilities     TEXT    NOT NULL DEFAULT '',
				capabilities_at  TEXT,
				session_id       TEXT    NOT NULL DEFAULT '',
				revision         INTEGER NOT NULL,
				created_at       TEXT    NOT NULL,
				updated_at       TEXT    NOT NULL,
				last_connected_at TEXT,
				last_seen_at     TEXT,
				revoked_at       TEXT
			) STRICT`,
			// One active agent per environment; one per Engine is enforced by
			// the enrollment transaction (duplicate Engine IDs of cloned VMs
			// can be allowed by the owner).
			`CREATE UNIQUE INDEX agents_active_environment ON agents (environment_id) WHERE status = 'active'`,
			`CREATE INDEX agents_active_engine ON agents (engine_id) WHERE status = 'active'`,
			`CREATE TABLE agent_credentials (
				id           TEXT PRIMARY KEY,
				agent_id     TEXT NOT NULL REFERENCES agents (id),
				verifier     TEXT NOT NULL,
				state        TEXT NOT NULL CHECK (state IN ('active', 'pending', 'revoked')),
				sealed       TEXT NOT NULL DEFAULT '',
				created_at   TEXT NOT NULL,
				activated_at TEXT,
				revoked_at   TEXT,
				CHECK (sealed = '' OR state = 'pending')
			) STRICT`,
			`CREATE INDEX agent_credentials_agent ON agent_credentials (agent_id, state)`,
		)),
		Tx(Exec(
			`DROP TABLE agent_credentials`,
			`DROP TABLE agents`,
			`DROP TABLE agent_enrollments`,
			`DROP TABLE environments`,
		)),
	)
}
