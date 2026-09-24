package migrations

func init() {
	// Browser sessions (#16, #18): the alexedwards/scs session store on
	// DockYard's Bun/SQLite connection (internal/manager/auth/sessions).
	// token is the SHA-256 of the cookie value (SCS HashTokenInStore), never
	// the cookie itself; data is the SCS-encoded session map; expiry is the
	// idle/absolute deadline after which the row is dead and swept.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE sessions (
				token  TEXT NOT NULL PRIMARY KEY,
				data   BLOB NOT NULL,
				expiry TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX sessions_expiry ON sessions (expiry)`,
		)),
		Tx(Exec(`DROP TABLE sessions`)),
	)
}
