package migrations

func init() {
	// Stored responses of Idempotency-Key requests to non-job operations
	// (#4). Job-starting operations keep their keys on the jobs table (#26).
	// response holds the sealed {header, body} JSON (secrets.Keyring).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE idempotency_keys (
				scope        TEXT    NOT NULL,
				key          TEXT    NOT NULL,
				request_hash TEXT    NOT NULL,
				state        TEXT    NOT NULL CHECK (state IN ('pending', 'completed')),
				status       INTEGER NOT NULL DEFAULT 0,
				response     TEXT    NOT NULL DEFAULT '',
				created_at   TEXT    NOT NULL,
				expires_at   TEXT    NOT NULL,
				PRIMARY KEY (scope, key),
				CHECK ((state = 'completed') = (status BETWEEN 200 AND 299))
			) STRICT`,
			`CREATE INDEX idempotency_keys_expires ON idempotency_keys (expires_at)`,
		)),
		Tx(Exec(`DROP TABLE idempotency_keys`)),
	)
}
