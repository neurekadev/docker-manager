package migrations

import (
	"context"

	"github.com/uptrace/bun"
)

func init() {
	// Digest-driven automatic updates (#20).
	//
	//   - update_policies: one per target (a DockYard stack or managed
	//     standalone container); opt-in service list and exclusions, the
	//     check and run schedules (#13: own cron, zone, enabled flag; both
	//     start disabled) and the run window.
	//   - update_candidates: the digest model per service (or the
	//     container): reference, platform, registry connection, applied,
	//     previous and candidate digests, eligibility, check time and the
	//     source hashes read around the check.
	//   - update_quarantine: candidate digests that failed an update (never
	//     retried automatically).
	//   - update_history: applied digest history of the policy's runs.
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			return Exec(
				`CREATE TABLE update_policies (
					id                   TEXT    NOT NULL PRIMARY KEY,
					environment_id       TEXT    NOT NULL,
					name                 TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
					name_key             TEXT    NOT NULL,
					target_type          TEXT    NOT NULL CHECK (target_type IN ('stack', 'container')),
					target_id            TEXT    NOT NULL CHECK (length(target_id) BETWEEN 1 AND 128),
					services             TEXT    NOT NULL DEFAULT '[]',
					exclude_services     TEXT    NOT NULL DEFAULT '[]',
					check_cron           TEXT    NOT NULL,
					check_time_zone      TEXT    NOT NULL,
					check_enabled        INTEGER NOT NULL CHECK (check_enabled IN (0, 1)),
					run_cron             TEXT    NOT NULL,
					run_time_zone        TEXT    NOT NULL,
					run_enabled          INTEGER NOT NULL CHECK (run_enabled IN (0, 1)),
					run_window           TEXT    NOT NULL DEFAULT '',
					wait_timeout_seconds INTEGER NOT NULL DEFAULT 0 CHECK (wait_timeout_seconds BETWEEN 0 AND 3600),
					revision             INTEGER NOT NULL CHECK (revision >= 1),
					created_at           TEXT    NOT NULL,
					updated_at           TEXT    NOT NULL,
					UNIQUE (environment_id, target_type, target_id),
					UNIQUE (environment_id, name_key)
				) STRICT`,
				`CREATE TABLE update_candidates (
					id                     TEXT    NOT NULL PRIMARY KEY,
					policy_id              TEXT    NOT NULL REFERENCES update_policies (id) ON DELETE CASCADE,
					service                TEXT    NOT NULL,
					reference              TEXT    NOT NULL DEFAULT '',
					registry               TEXT    NOT NULL DEFAULT '',
					repository             TEXT    NOT NULL DEFAULT '',
					tag                    TEXT    NOT NULL DEFAULT '',
					platform               TEXT    NOT NULL DEFAULT '',
					registry_connection_id TEXT    NOT NULL DEFAULT '',
					eligible               INTEGER NOT NULL CHECK (eligible IN (0, 1)),
					reason                 TEXT    NOT NULL DEFAULT '',
					reason_message         TEXT    NOT NULL DEFAULT '',
					non_version_tag        INTEGER NOT NULL DEFAULT 0 CHECK (non_version_tag IN (0, 1)),
					status                 TEXT    NOT NULL CHECK (status IN ('ineligible', 'unchecked', 'up_to_date',
						'update_available', 'quarantined', 'check_failed', 'run_failed')),
					applied_digest         TEXT    NOT NULL DEFAULT '',
					applied_image_id       TEXT    NOT NULL DEFAULT '',
					previous_digest        TEXT    NOT NULL DEFAULT '',
					candidate_digest       TEXT    NOT NULL DEFAULT '',
					candidate_index_digest TEXT    NOT NULL DEFAULT '',
					error_class            TEXT    NOT NULL DEFAULT '',
					error_message          TEXT    NOT NULL DEFAULT '',
					retry_after_seconds    INTEGER NOT NULL DEFAULT 0,
					checked_at             TEXT,
					check_job_id           TEXT    NOT NULL DEFAULT '',
					source_hash_before     TEXT    NOT NULL DEFAULT '',
					source_hash_after      TEXT    NOT NULL DEFAULT '',
					updated_at             TEXT    NOT NULL,
					UNIQUE (policy_id, service)
				) STRICT`,
				`CREATE TABLE update_quarantine (
					policy_id   TEXT NOT NULL REFERENCES update_policies (id) ON DELETE CASCADE,
					service     TEXT NOT NULL,
					digest      TEXT NOT NULL,
					job_id      TEXT NOT NULL DEFAULT '',
					error_class TEXT NOT NULL DEFAULT '',
					created_at  TEXT NOT NULL,
					PRIMARY KEY (policy_id, service, digest)
				) STRICT`,
				`CREATE TABLE update_history (
					id                     TEXT NOT NULL PRIMARY KEY,
					policy_id              TEXT NOT NULL REFERENCES update_policies (id) ON DELETE CASCADE,
					environment_id         TEXT NOT NULL,
					target_type            TEXT NOT NULL,
					target_id              TEXT NOT NULL,
					service                TEXT NOT NULL,
					reference              TEXT NOT NULL DEFAULT '',
					registry_connection_id TEXT NOT NULL DEFAULT '',
					from_digest            TEXT NOT NULL DEFAULT '',
					to_digest              TEXT NOT NULL DEFAULT '',
					from_image_id          TEXT NOT NULL DEFAULT '',
					to_image_id            TEXT NOT NULL DEFAULT '',
					job_id                 TEXT NOT NULL DEFAULT '',
					outcome                TEXT NOT NULL CHECK (outcome IN ('updated', 'unchanged', 'kept_stopped', 'failed')),
					error_class            TEXT NOT NULL DEFAULT '',
					source_hash_before     TEXT NOT NULL DEFAULT '',
					source_hash_after      TEXT NOT NULL DEFAULT '',
					at                     TEXT NOT NULL
				) STRICT`,
				`CREATE INDEX update_history_policy ON update_history (policy_id, at)`,
			)(ctx, tx)
		}),
		Tx(func(ctx context.Context, tx bun.Tx) error {
			return Exec(
				`DROP TABLE update_history`,
				`DROP TABLE update_quarantine`,
				`DROP TABLE update_candidates`,
				`DROP TABLE update_policies`,
			)(ctx, tx)
		}),
	)
}
