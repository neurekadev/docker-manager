package migrations

func init() {
	// Backups (#10): manager-state backups were indexed with the scope name
	// ("manager") as their environment. They belong to no environment.
	Migrations.MustRegister(
		Tx(Exec(
			`UPDATE backup_snapshots SET environment_id = '' WHERE scope = 'manager' AND environment_id = 'manager'`,
		)),
		Tx(Exec()),
	)
}
