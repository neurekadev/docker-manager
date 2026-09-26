package migrations

func init() {
	// The project is now Docker Manager: an instance still carrying the
	// former default display name (DockYard) takes the new default. A name
	// the owner chose is kept.
	Migrations.MustRegister(
		Tx(Exec(
			`UPDATE instance_settings SET name = 'Docker Manager' WHERE singleton = 1 AND name = 'DockYard'`,
		)),
		Tx(Exec(
			`UPDATE instance_settings SET name = 'DockYard' WHERE singleton = 1 AND name = 'Docker Manager'`,
		)),
	)
}
