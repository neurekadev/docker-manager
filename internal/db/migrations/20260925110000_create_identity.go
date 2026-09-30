package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/ids"
)

func init() {
	// Identity (#16): groups with the permanent default-group invariant and
	// the initial "Restricted" group (no grants; #17 adds rules), users with
	// the protected owner, passkeys, recovery codes, invitations, account
	// resets and the instance sign-in policy.
	//
	// Invariants enforced by the schema, not only by code:
	//   - exactly one default group: default_group is a permanent singleton
	//     row (it cannot be deleted) whose NOT NULL foreign key cannot point
	//     at a missing group, so the default group cannot be deleted either;
	//   - at most one owner (partial unique index, which also makes
	//     concurrent first-run setup race-safe), who is always active and
	//     can neither be deleted nor demoted (CHECK and triggers);
	//   - one-time codes are stored only as verifiers (SHA-256 of 256-bit
	//     secrets) and are unique.
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := Exec(
				`CREATE TABLE groups (
					id         TEXT    PRIMARY KEY,
					name       TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(name)) BETWEEN 1 AND 64),
					revision   INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
					created_at TEXT    NOT NULL,
					updated_at TEXT    NOT NULL
				) STRICT`,
				`CREATE TABLE default_group (
					singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
					group_id  TEXT    NOT NULL REFERENCES groups (id) ON DELETE RESTRICT
				) STRICT`,
				`CREATE TRIGGER default_group_permanent BEFORE DELETE ON default_group
				BEGIN SELECT RAISE(ABORT, 'there must always be a default group'); END`,
				`CREATE TABLE users (
					id                      TEXT    PRIMARY KEY,
					username                TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
					display_name            TEXT    NOT NULL DEFAULT '',
					email                   TEXT    COLLATE NOCASE,
					is_owner                INTEGER NOT NULL DEFAULT 0 CHECK (is_owner IN (0, 1)),
					group_id                TEXT    NOT NULL REFERENCES groups (id) ON DELETE RESTRICT,
					status                  TEXT    NOT NULL CHECK (status IN ('active', 'disabled')),
					password_hash           TEXT,
					password_changed_at     TEXT,
					totp_seed               TEXT,
					totp_enabled_at         TEXT,
					totp_last_step          INTEGER NOT NULL DEFAULT 0,
					totp_pending_seed       TEXT,
					totp_pending_expires_at TEXT,
					webauthn_handle         BLOB    NOT NULL UNIQUE,
					session_epoch           INTEGER NOT NULL DEFAULT 1,
					enrollment_deadline     TEXT,
					invitation_id           TEXT,
					revision                INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
					created_at              TEXT    NOT NULL,
					updated_at              TEXT    NOT NULL,
					disabled_at             TEXT,
					last_sign_in_at         TEXT,
					CHECK (is_owner = 0 OR status = 'active'),
					CHECK ((totp_seed IS NULL) = (totp_enabled_at IS NULL))
				) STRICT`,
				`CREATE UNIQUE INDEX users_one_owner ON users (is_owner) WHERE is_owner = 1`,
				`CREATE INDEX users_group ON users (group_id)`,
				`CREATE TRIGGER users_owner_undeletable BEFORE DELETE ON users WHEN OLD.is_owner = 1
				BEGIN SELECT RAISE(ABORT, 'the instance owner cannot be deleted'); END`,
				`CREATE TRIGGER users_owner_permanent BEFORE UPDATE OF is_owner ON users WHEN OLD.is_owner = 1 AND NEW.is_owner = 0
				BEGIN SELECT RAISE(ABORT, 'ownership cannot be removed'); END`,
				`CREATE TABLE passkeys (
					id              TEXT    PRIMARY KEY,
					user_id         TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
					credential_id   BLOB    NOT NULL UNIQUE,
					name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
					credential      TEXT    NOT NULL,
					sign_count      INTEGER NOT NULL DEFAULT 0,
					backup_eligible INTEGER NOT NULL CHECK (backup_eligible IN (0, 1)),
					backup_state    INTEGER NOT NULL CHECK (backup_state IN (0, 1)),
					aaguid          TEXT    NOT NULL DEFAULT '',
					created_at      TEXT    NOT NULL,
					last_used_at    TEXT
				) STRICT`,
				`CREATE INDEX passkeys_user ON passkeys (user_id)`,
				`CREATE TABLE recovery_codes (
					id         TEXT PRIMARY KEY,
					user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
					code_hash  TEXT NOT NULL UNIQUE,
					created_at TEXT NOT NULL,
					used_at    TEXT
				) STRICT`,
				`CREATE INDEX recovery_codes_user ON recovery_codes (user_id)`,
				`CREATE TABLE invitations (
					id               TEXT PRIMARY KEY,
					verifier         TEXT NOT NULL UNIQUE,
					email            TEXT COLLATE NOCASE,
					created_by       TEXT REFERENCES users (id) ON DELETE SET NULL,
					created_at       TEXT NOT NULL,
					expires_at       TEXT NOT NULL,
					redeemed_at      TEXT,
					redeemed_user_id TEXT REFERENCES users (id) ON DELETE SET NULL,
					revoked_at       TEXT,
					CHECK (redeemed_at IS NULL OR revoked_at IS NULL)
				) STRICT`,
				`CREATE TABLE account_resets (
					id         TEXT PRIMARY KEY,
					user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
					kind       TEXT NOT NULL CHECK (kind IN ('password_reset', 'owner_recovery')),
					verifier   TEXT NOT NULL UNIQUE,
					created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
					created_at TEXT NOT NULL,
					expires_at TEXT NOT NULL,
					used_at    TEXT
				) STRICT`,
				`CREATE INDEX account_resets_user ON account_resets (user_id)`,
				`CREATE TABLE security_settings (
					singleton                INTEGER PRIMARY KEY CHECK (singleton = 1),
					strict_passwords         INTEGER NOT NULL CHECK (strict_passwords IN (0, 1)),
					min_password_length      INTEGER NOT NULL CHECK (min_password_length BETWEEN 8 AND 64),
					required_factors         TEXT    NOT NULL CHECK (required_factors IN ('none', 'totp', 'passkey', 'either', 'both')),
					enrollment_grace_hours   INTEGER NOT NULL CHECK (enrollment_grace_hours BETWEEN 1 AND 720),
					invitation_ttl_hours     INTEGER NOT NULL CHECK (invitation_ttl_hours BETWEEN 1 AND 720),
					password_reset_ttl_hours INTEGER NOT NULL CHECK (password_reset_ttl_hours BETWEEN 1 AND 168),
					revision                 INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
					updated_at               TEXT    NOT NULL
				) STRICT`,
			)(ctx, tx); err != nil {
				return err
			}
			// The initial default group and policy. Timestamps use SQLite's
			// clock here only because a migration has no injected clock.
			now := "strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')"
			restricted := ids.New()
			if _, err := tx.ExecContext(ctx, `INSERT INTO groups (id, name, created_at, updated_at) VALUES (?, 'Restricted', `+now+`, `+now+`)`, restricted); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO default_group (singleton, group_id) VALUES (1, ?)`, restricted); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO security_settings (singleton, strict_passwords, min_password_length, required_factors,
				enrollment_grace_hours, invitation_ttl_hours, password_reset_ttl_hours, updated_at)
				VALUES (1, 1, 15, 'none', 72, 72, 24, `+now+`)`)
			return err
		}),
		Tx(Exec(
			`DROP TABLE security_settings`,
			`DROP TABLE account_resets`,
			`DROP TABLE invitations`,
			`DROP TABLE recovery_codes`,
			`DROP TABLE passkeys`,
			`DROP TRIGGER users_owner_permanent`,
			`DROP TRIGGER users_owner_undeletable`,
			`DROP TABLE users`,
			`DROP TRIGGER default_group_permanent`,
			`DROP TABLE default_group`,
			`DROP TABLE groups`,
		)),
	)
}
