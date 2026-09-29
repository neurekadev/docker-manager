package migrations

func init() {
	// Moving the manager to a new server (docs/internal/architecture/
	// manager-move.md): one row per move. The move code is never stored;
	// code_verifier is the SHA-256 of its secret part. On the new manager
	// the row arrives in the copy (state arrived) and sealed_code keeps the
	// code sealed with the secret key until the old manager confirmed.
	// At most one move is open or in progress (open, draining, handed_off,
	// confirmed); the move service checks it in its transaction.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE manager_moves (
				id               TEXT NOT NULL PRIMARY KEY,
				state            TEXT NOT NULL CHECK (state IN ('open', 'draining', 'handed_off', 'confirmed', 'cancelled', 'expired', 'arrived')),
				code_verifier    TEXT NOT NULL,
				created_by       TEXT NOT NULL DEFAULT '',
				created_at       TEXT NOT NULL,
				expires_at       TEXT NOT NULL,
				draining_at      TEXT,
				handed_off_at    TEXT,
				confirmed_at     TEXT,
				ended_at         TEXT,
				handoff_address  TEXT NOT NULL DEFAULT '',
				source_url       TEXT NOT NULL DEFAULT '',
				arrived_at       TEXT,
				sealed_code      TEXT NOT NULL DEFAULT '',
				confirm_attempts INTEGER NOT NULL DEFAULT 0,
				confirm_error    TEXT NOT NULL DEFAULT '',
				last_confirm_at  TEXT,
				updated_at       TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX manager_moves_state ON manager_moves (state, created_at)`,
		)),
		Tx(Exec(`DROP TABLE manager_moves`)),
	)
}
