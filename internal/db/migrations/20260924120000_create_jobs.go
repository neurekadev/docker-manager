package migrations

func init() {
	// Job engine (#26): durable jobs, their targets, held locks, the bounded
	// progress/event log and per-environment fencing counters.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE jobs (
				id                 TEXT    PRIMARY KEY,
				kind               TEXT    NOT NULL,
				executor           TEXT    NOT NULL CHECK (executor IN ('agent', 'manager')),
				origin             TEXT    NOT NULL CHECK (origin IN ('manual', 'scheduled', 'api_token')),
				initiator_user_id  TEXT,
				initiator_token_id TEXT,
				policy_id          TEXT,
				environment_id     TEXT,
				targets            TEXT    NOT NULL DEFAULT '[]',
				input              TEXT    NOT NULL DEFAULT '{}',
				input_hash         TEXT    NOT NULL,
				idempotency_scope  TEXT,
				idempotency_key    TEXT,
				attempt            INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1),
				state              TEXT    NOT NULL CHECK (state IN ('queued', 'blocked', 'dispatched', 'running', 'cancelling',
				                                                     'succeeded', 'failed', 'partial', 'cancelled', 'interrupted')),
				progress_percent   INTEGER NOT NULL DEFAULT -1 CHECK (progress_percent BETWEEN -1 AND 100),
				progress_step      TEXT    NOT NULL DEFAULT '',
				progress_message   TEXT    NOT NULL DEFAULT '',
				items              TEXT    NOT NULL DEFAULT '[]',
				error_class        TEXT    NOT NULL DEFAULT '',
				error_message      TEXT    NOT NULL DEFAULT '',
				recovery           TEXT    NOT NULL DEFAULT '',
				blocked_by         TEXT    NOT NULL DEFAULT '',
				blocked_reason     TEXT    NOT NULL DEFAULT '',
				locks              TEXT    NOT NULL DEFAULT '[]',
				fencing_token      INTEGER NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
				cancel_requested   INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
				current_step       TEXT    NOT NULL DEFAULT '',
				step_in_flight     INTEGER NOT NULL DEFAULT 0 CHECK (step_in_flight IN (0, 1)),
				completed_steps    TEXT    NOT NULL DEFAULT '[]',
				compensations      TEXT    NOT NULL DEFAULT '[]',
				resumes            INTEGER NOT NULL DEFAULT 0,
				last_event_seq     INTEGER NOT NULL DEFAULT 0,
				created_at         TEXT    NOT NULL,
				updated_at         TEXT    NOT NULL,
				dispatched_at      TEXT,
				started_at         TEXT,
				finished_at        TEXT,
				CHECK ((idempotency_key IS NULL) = (idempotency_scope IS NULL))
			) STRICT`,
			`CREATE UNIQUE INDEX jobs_idempotency ON jobs (idempotency_scope, idempotency_key) WHERE idempotency_key IS NOT NULL`,
			`CREATE INDEX jobs_state ON jobs (state, id)`,
			`CREATE INDEX jobs_environment ON jobs (environment_id, id)`,
			`CREATE INDEX jobs_kind ON jobs (kind, id)`,
			`CREATE INDEX jobs_finished ON jobs (finished_at) WHERE finished_at IS NOT NULL`,
			`CREATE TABLE job_targets (
				job_id         TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
				type           TEXT NOT NULL,
				environment_id TEXT NOT NULL,
				target_id      TEXT NOT NULL,
				PRIMARY KEY (job_id, type, environment_id, target_id)
			) STRICT, WITHOUT ROWID`,
			`CREATE INDEX job_targets_lookup ON job_targets (type, target_id, job_id)`,
			`CREATE TABLE job_locks (
				job_id         TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
				scope          TEXT NOT NULL,
				environment_id TEXT NOT NULL,
				name           TEXT NOT NULL,
				mode           TEXT NOT NULL CHECK (mode IN ('shared', 'exclusive')),
				acquired_at    TEXT NOT NULL,
				PRIMARY KEY (job_id, scope, environment_id, name)
			) STRICT, WITHOUT ROWID`,
			`CREATE INDEX job_locks_resource ON job_locks (scope, environment_id, name)`,
			`CREATE TABLE job_events (
				job_id  TEXT    NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
				seq     INTEGER NOT NULL CHECK (seq >= 1),
				at      TEXT    NOT NULL,
				type    TEXT    NOT NULL,
				state   TEXT    NOT NULL DEFAULT '',
				message TEXT    NOT NULL DEFAULT '',
				percent INTEGER NOT NULL DEFAULT -1,
				step    TEXT    NOT NULL DEFAULT '',
				item    TEXT,
				PRIMARY KEY (job_id, seq)
			) STRICT, WITHOUT ROWID`,
			`CREATE TABLE job_fencing (
				environment_id TEXT    PRIMARY KEY,
				last_token     INTEGER NOT NULL CHECK (last_token >= 0)
			) STRICT`,
		)),
		Tx(Exec(
			`DROP TABLE job_fencing`,
			`DROP TABLE job_events`,
			`DROP TABLE job_locks`,
			`DROP TABLE job_targets`,
			`DROP TABLE jobs`,
		)),
	)
}
