package migrations

func init() {
	// Moving the manager to a new server (docs/internal/architecture/
	// manager-move.md): one row per move. The move code is never stored in
	// clear: sealed_code keeps it sealed with the secret key (the old
	// manager authenticates the new one and encrypts the handoff with it;
	// on the new manager the row arrives in the copy, state arrived, and
	// keeps it until the old manager confirmed). redirects is a JSON list
	// of the manager.redirect requests sent before the handoff;
	// confirm_acknowledged_at records the owner stating the old manager no
	// longer runs the instance when it never confirmed. At most one move
	// is open or in progress (open, moving, ready, draining, handed_off,
	// confirmed); the move service checks it in its transaction.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE manager_moves (
				id                      TEXT NOT NULL PRIMARY KEY,
				state                   TEXT NOT NULL CHECK (state IN ('open', 'moving', 'ready', 'draining', 'handed_off', 'confirmed', 'cancelled', 'expired', 'arrived')),
				created_by              TEXT NOT NULL DEFAULT '',
				created_at              TEXT NOT NULL,
				expires_at              TEXT NOT NULL,
				this_server_address     TEXT NOT NULL DEFAULT '',
				new_server_address      TEXT NOT NULL DEFAULT '',
				enrollment_id           TEXT NOT NULL DEFAULT '',
				source_environment_id   TEXT NOT NULL DEFAULT '',
				target_environment_id   TEXT NOT NULL DEFAULT '',
				checked_in_at           TEXT,
				move_job_id             TEXT NOT NULL DEFAULT '',
				migration_id            TEXT NOT NULL DEFAULT '',
				ready_at                TEXT,
				draining_at             TEXT,
				handed_off_at           TEXT,
				confirmed_at            TEXT,
				ended_at                TEXT,
				handoff_address         TEXT NOT NULL DEFAULT '',
				redirects               TEXT NOT NULL DEFAULT '[]',
				source_url              TEXT NOT NULL DEFAULT '',
				arrived_at              TEXT,
				sealed_code             TEXT NOT NULL DEFAULT '',
				confirm_attempts        INTEGER NOT NULL DEFAULT 0,
				confirm_error           TEXT NOT NULL DEFAULT '',
				last_confirm_at         TEXT,
				confirm_acknowledged_at TEXT,
				updated_at              TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX manager_moves_state ON manager_moves (state, created_at)`,
		)),
		Tx(Exec(`DROP TABLE manager_moves`)),
	)
}
