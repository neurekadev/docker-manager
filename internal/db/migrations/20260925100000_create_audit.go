package migrations

func init() {
	// Append-only, hash-chained audit trail (#30).
	//
	// audit_events: one row per record. seq is the chain position (assigned
	// by the writer as head_seq + 1, never reused); hash = SHA-256 over
	// prev_hash and the canonical record (internal/manager/audit). at is a
	// fixed-width UTC text timestamp (YYYY-MM-DDTHH:MM:SS.ffffffZ) so it
	// sorts and compares lexicographically. targets and details are
	// canonical JSON; size is the canonical record size in bytes (size cap).
	//
	// audit_chain: the single chain state row: the purge anchor (last
	// deleted seq/hash; 0/'' before the first purge), the head (last seq and
	// hash, so truncating the newest records is detected), running totals
	// and the purging guard.
	//
	// Triggers make the table append-only for every code path: rows can
	// never be updated, and deleted only by the retention purge while it
	// holds the purging guard in its transaction.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE audit_events (
				seq            INTEGER PRIMARY KEY,
				id             TEXT    NOT NULL UNIQUE,
				at             TEXT    NOT NULL,
				category       TEXT    NOT NULL CHECK (category IN ('identity', 'authorization', 'credentials', 'operations', 'system')),
				action         TEXT    NOT NULL,
				operation_id   TEXT    NOT NULL DEFAULT '',
				actor_kind     TEXT    NOT NULL CHECK (actor_kind IN ('user', 'api_token', 'service', 'agent', 'anonymous')),
				actor_user_id  TEXT    NOT NULL DEFAULT '',
				actor_token_id TEXT    NOT NULL DEFAULT '',
				actor_agent_id TEXT    NOT NULL DEFAULT '',
				client_ip      TEXT    NOT NULL DEFAULT '',
				user_agent     TEXT    NOT NULL DEFAULT '',
				environment_id TEXT    NOT NULL DEFAULT '',
				targets        TEXT    NOT NULL DEFAULT '[]',
				outcome        TEXT    NOT NULL CHECK (outcome IN ('success', 'partial', 'failure', 'denied', 'error')),
				error_class    TEXT    NOT NULL DEFAULT '',
				job_id         TEXT    NOT NULL DEFAULT '',
				request_id     TEXT    NOT NULL DEFAULT '',
				details        TEXT    NOT NULL DEFAULT '{}',
				size           INTEGER NOT NULL,
				prev_hash      TEXT    NOT NULL,
				hash           TEXT    NOT NULL
			) STRICT`,
			`CREATE INDEX audit_events_at ON audit_events (at)`,
			`CREATE INDEX audit_events_action ON audit_events (action, seq)`,
			`CREATE INDEX audit_events_actor_user ON audit_events (actor_user_id, seq)`,
			`CREATE INDEX audit_events_environment ON audit_events (environment_id, seq)`,
			`CREATE INDEX audit_events_job ON audit_events (job_id, seq)`,
			`CREATE TABLE audit_chain (
				id           INTEGER PRIMARY KEY CHECK (id = 1),
				anchor_seq   INTEGER NOT NULL,
				anchor_hash  TEXT    NOT NULL,
				head_seq     INTEGER NOT NULL,
				head_hash    TEXT    NOT NULL,
				record_count INTEGER NOT NULL,
				total_bytes  INTEGER NOT NULL,
				purging      INTEGER NOT NULL DEFAULT 0 CHECK (purging IN (0, 1))
			) STRICT`,
			`INSERT INTO audit_chain (id, anchor_seq, anchor_hash, head_seq, head_hash, record_count, total_bytes, purging)
				VALUES (1, 0, '', 0, '', 0, 0, 0)`,
			`CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
				BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END`,
			`CREATE TRIGGER audit_events_purge_only BEFORE DELETE ON audit_events
				WHEN (SELECT purging FROM audit_chain WHERE id = 1) IS NOT 1
				BEGIN SELECT RAISE(ABORT, 'audit_events rows are deleted only by the retention purge'); END`,
		)),
		Tx(Exec(
			`DROP TRIGGER audit_events_purge_only`,
			`DROP TRIGGER audit_events_no_update`,
			`DROP TABLE audit_chain`,
			`DROP TABLE audit_events`,
		)),
	)
}
