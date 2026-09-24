package migrations

func init() {
	// instance holds exactly one row describing this DockYard installation.
	Migrations.MustRegister(
		Tx(Exec(`CREATE TABLE instance (
			singleton  INTEGER PRIMARY KEY CHECK (singleton = 1),
			id         TEXT    NOT NULL UNIQUE,
			created_at TEXT    NOT NULL
		) STRICT`)),
		Tx(Exec(`DROP TABLE instance`)),
	)
}
