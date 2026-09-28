package migrations

func init() {
	// Backups (#10): each repository's compression mode (restic
	// --compression) for the data written to it: auto (restic's default),
	// max or off.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_repositories ADD COLUMN compression TEXT NOT NULL DEFAULT 'auto' CHECK (compression IN ('auto', 'max', 'off'))`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_repositories DROP COLUMN compression`,
		)),
	)
}
