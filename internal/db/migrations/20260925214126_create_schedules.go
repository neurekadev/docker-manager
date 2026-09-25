package migrations

import (
	"context"

	"github.com/uptrace/bun"
)

func init() {
	// The shared cron scheduler (#13).
	//
	//   - schedule_settings: the instance's default time zone (revisioned
	//     with the default expressions); schedule_default_crons: edited
	//     default expressions per schedule kind (a kind without a row uses
	//     DockYard's suggestion). Defaults only prefill new policies.
	//   - schedules: durable state of each policy's schedule, synchronized
	//     from the policy owners (#10, #14, #20): the saved expression and
	//     zone, enabled flag, cursor (last processed instant, or the time
	//     the schedule was created/enabled/edited) and the next run.
	//   - schedule_runs: one row per processed instant; UNIQUE
	//     (schedule_id, scheduled_for) and the idempotency key
	//     <kind>:<policy>:<instant> make restarts never enqueue twice.
	//   - schedule_run_jobs: the jobs a run enqueued and their final state
	//     (kept after job retention deletes the job).
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := Exec(
				`CREATE TABLE schedule_settings (
					singleton  INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
					time_zone  TEXT    NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
					revision   INTEGER NOT NULL CHECK (revision >= 1),
					updated_at TEXT    NOT NULL
				) STRICT`,
				`CREATE TABLE schedule_default_crons (
					kind       TEXT NOT NULL PRIMARY KEY,
					cron       TEXT NOT NULL CHECK (length(cron) BETWEEN 1 AND 256),
					updated_at TEXT NOT NULL
				) STRICT`,
				`CREATE TABLE schedules (
					id             TEXT    NOT NULL PRIMARY KEY,
					kind           TEXT    NOT NULL,
					policy_id      TEXT    NOT NULL,
					name           TEXT    NOT NULL DEFAULT '',
					environment_id TEXT    NOT NULL DEFAULT '',
					cron           TEXT    NOT NULL,
					time_zone      TEXT    NOT NULL,
					enabled        INTEGER NOT NULL CHECK (enabled IN (0, 1)),
					invalid_reason TEXT    NOT NULL DEFAULT '',
					cursor_at      TEXT    NOT NULL,
					next_run_at    TEXT,
					created_at     TEXT    NOT NULL,
					updated_at     TEXT    NOT NULL,
					UNIQUE (kind, policy_id)
				) STRICT`,
				`CREATE TABLE schedule_runs (
					id              TEXT    NOT NULL PRIMARY KEY,
					schedule_id     TEXT    NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
					scheduled_for   TEXT    NOT NULL,
					idempotency_key TEXT    NOT NULL UNIQUE,
					outcome         TEXT    NOT NULL CHECK (outcome IN ('pending', 'enqueued', 'missed', 'skipped', 'rejected', 'failed')),
					catch_up        INTEGER NOT NULL DEFAULT 0 CHECK (catch_up IN (0, 1)),
					missed_count    INTEGER NOT NULL DEFAULT 0 CHECK (missed_count >= 0),
					missed_from     TEXT,
					reason          TEXT    NOT NULL DEFAULT '',
					error_class     TEXT    NOT NULL DEFAULT '',
					created_at      TEXT    NOT NULL,
					updated_at      TEXT    NOT NULL,
					UNIQUE (schedule_id, scheduled_for)
				) STRICT`,
				`CREATE INDEX schedule_runs_pending ON schedule_runs (outcome) WHERE outcome = 'pending'`,
				`CREATE TABLE schedule_run_jobs (
					job_id      TEXT    NOT NULL PRIMARY KEY,
					run_id      TEXT    NOT NULL REFERENCES schedule_runs (id) ON DELETE CASCADE,
					position    INTEGER NOT NULL CHECK (position >= 0),
					kind        TEXT    NOT NULL,
					state       TEXT    NOT NULL,
					error_class TEXT    NOT NULL DEFAULT '',
					finished_at TEXT,
					UNIQUE (run_id, position)
				) STRICT`,
			)(ctx, tx); err != nil {
				return err
			}
			// The initial default zone. SQLite's clock only because a
			// migration has no injected clock.
			_, err := tx.ExecContext(ctx, `INSERT INTO schedule_settings (singleton, time_zone, revision, updated_at)
				VALUES (1, 'UTC', 1, strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))`)
			return err
		}),
		Tx(Exec(
			`DROP TABLE schedule_run_jobs`,
			`DROP TABLE schedule_runs`,
			`DROP TABLE schedules`,
			`DROP TABLE schedule_default_crons`,
			`DROP TABLE schedule_settings`,
		)),
	)
}
