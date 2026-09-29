package migrations

func init() {
	// Signed-in devices (#16): one row per signed-in browser session, so a
	// user sees their devices and can sign out one of them. The SCS session
	// (table sessions) carries the row ID; a session whose row is gone is
	// signed out on its next request. Rows hold no token: the ID is public.
	//
	//   - a row belongs to one user and dies with the account (ON DELETE
	//     CASCADE); its owner and creation time never change;
	//   - epoch is the user's session epoch the session was made in: rows of
	//     an older epoch are ended sessions (password change, "Sign out
	//     everywhere") and are no longer listed;
	//   - stay_signed_in selects the longer limits ("Stay signed in").
	//
	// The security policy gains allow_stay_signed_in (on by default).
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE user_sessions (
				id             TEXT    PRIMARY KEY,
				user_id        TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
				epoch          INTEGER NOT NULL,
				stay_signed_in INTEGER NOT NULL CHECK (stay_signed_in IN (0, 1)),
				created_at     TEXT    NOT NULL,
				last_seen_at   TEXT    NOT NULL,
				ip             TEXT    NOT NULL DEFAULT '',
				user_agent     TEXT    NOT NULL DEFAULT '' CHECK (length(user_agent) <= 256)
			) STRICT`,
			`CREATE INDEX user_sessions_user ON user_sessions (user_id)`,
			`CREATE TRIGGER user_sessions_identity_fixed BEFORE UPDATE OF id, user_id, created_at ON user_sessions
			BEGIN SELECT RAISE(ABORT, 'the owner and creation time of a session never change'); END`,
			`ALTER TABLE security_settings ADD COLUMN allow_stay_signed_in INTEGER NOT NULL DEFAULT 1 CHECK (allow_stay_signed_in IN (0, 1))`,
		)),
		Tx(Exec(
			`ALTER TABLE security_settings DROP COLUMN allow_stay_signed_in`,
			`DROP TRIGGER user_sessions_identity_fixed`,
			`DROP TABLE user_sessions`,
		)),
	)
}
