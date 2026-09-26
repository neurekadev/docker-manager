package migrations

func init() {
	// Backup policies (#10) back up anonymous volumes only when enabled.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_policies ADD COLUMN anonymous_volumes INTEGER NOT NULL DEFAULT 0 CHECK (anonymous_volumes IN (0, 1))`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_policies DROP COLUMN anonymous_volumes`,
		)),
	)
}
