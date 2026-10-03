package migrations

import (
	"context"
	"errors"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// usersTable is the users table, with a group_id column when withGroup.
func usersTable(name string, withGroup bool) string {
	group := ""
	if withGroup {
		group = "group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE RESTRICT,"
	}
	return `CREATE TABLE ` + name + ` (
		id                      TEXT    PRIMARY KEY,
		username                TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
		display_name            TEXT    NOT NULL DEFAULT '',
		email                   TEXT    COLLATE NOCASE,
		is_owner                INTEGER NOT NULL DEFAULT 0 CHECK (is_owner IN (0, 1)),
		` + group + `
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
		permissions_revision    INTEGER NOT NULL DEFAULT 1 CHECK (permissions_revision >= 1),
		CHECK (is_owner = 0 OR status = 'active'),
		CHECK ((totp_seed IS NULL) = (totp_enabled_at IS NULL))
	) STRICT`
}

const userColumns = `id, username, display_name, email, is_owner, status, password_hash, password_changed_at, totp_seed,
	totp_enabled_at, totp_last_step, totp_pending_seed, totp_pending_expires_at, webauthn_handle, session_epoch,
	enrollment_deadline, invitation_id, revision, created_at, updated_at, disabled_at, last_sign_in_at, permissions_revision`

// usersIndexes are the users table's indexes and triggers (without the
// group index).
var usersIndexes = []string{
	`CREATE UNIQUE INDEX users_one_owner ON users (is_owner) WHERE is_owner = 1`,
	`CREATE TRIGGER users_owner_undeletable BEFORE DELETE ON users WHEN OLD.is_owner = 1
	BEGIN SELECT RAISE(ABORT, 'the instance owner cannot be deleted'); END`,
	`CREATE TRIGGER users_owner_permanent BEFORE UPDATE OF is_owner ON users WHEN OLD.is_owner = 1 AND NEW.is_owner = 0
	BEGIN SELECT RAISE(ABORT, 'ownership cannot be removed'); END`,
}

// withoutForeignKeys runs fn in a transaction on one connection with
// foreign key enforcement off: SQLite's procedure for rebuilding a table
// that other tables reference (dropping users with enforcement on would
// delete every session, passkey and rule of its accounts through ON DELETE
// CASCADE). The transaction fails when it leaves a dangling reference.
func withoutForeignKeys(fn func(ctx context.Context, tx bun.Tx) error) migrate.MigrationFunc {
	return func(ctx context.Context, db *bun.DB) error {
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		// The pragma is a no-op inside a transaction: set it before.
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`) }()
		return conn.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := fn(ctx, tx); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			if rows.Next() {
				return errors.New("migration left a dangling foreign key")
			}
			return rows.Err()
		})
	}
}

func init() {
	// Users in several groups, groups in a priority order (#233):
	//
	//   - groups.position orders the groups, 0 first (the highest priority:
	//     the first group with a rule matching a capability decides). The
	//     existing groups keep their creation order; with one group per user
	//     so far, the order changes nobody's access;
	//   - user_groups holds the memberships. A group with members still
	//     cannot be deleted (ON DELETE RESTRICT); a deleted user leaves its
	//     groups (CASCADE). Every account except the owner keeps its group;
	//     the owner, whom group rules never govern, is in none;
	//   - users loses group_id (a rebuild: SQLite cannot drop a column with
	//     a foreign key).
	//
	// Down puts every account back in its highest-priority group, or the
	// default group when it has none.
	Migrations.MustRegister(
		withoutForeignKeys(Exec(
			`ALTER TABLE groups ADD COLUMN position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0)`,
			`UPDATE groups SET position = (SELECT count(*) FROM groups g WHERE g.id < groups.id)`,
			`CREATE TABLE user_groups (
				user_id  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
				group_id TEXT NOT NULL REFERENCES groups (id) ON DELETE RESTRICT,
				PRIMARY KEY (user_id, group_id)
			) STRICT`,
			`CREATE INDEX user_groups_group ON user_groups (group_id)`,
			`INSERT INTO user_groups (user_id, group_id) SELECT id, group_id FROM users WHERE is_owner = 0`,
			usersTable("users_new", false),
			`INSERT INTO users_new (`+userColumns+`) SELECT `+userColumns+` FROM users`,
			`DROP TABLE users`,
			`ALTER TABLE users_new RENAME TO users`,
			usersIndexes[0], usersIndexes[1], usersIndexes[2],
		)),
		withoutForeignKeys(Exec(
			usersTable("users_old", true),
			`INSERT INTO users_old (group_id, `+userColumns+`)
			 SELECT COALESCE(
				(SELECT ug.group_id FROM user_groups ug JOIN groups g ON g.id = ug.group_id WHERE ug.user_id = users.id
				 ORDER BY g.position, g.id LIMIT 1),
				(SELECT group_id FROM default_group)), `+userColumns+` FROM users`,
			`DROP TABLE users`,
			`ALTER TABLE users_old RENAME TO users`,
			usersIndexes[0], usersIndexes[1], usersIndexes[2],
			`CREATE INDEX users_group ON users (group_id)`,
			`DROP TABLE user_groups`,
			`ALTER TABLE groups DROP COLUMN position`,
		)),
	)
}
