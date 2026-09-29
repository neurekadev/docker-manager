package migrations

func init() {
	// Backup policies back up the stacks' bind sources outside their
	// project directories only when enabled (and the agent allows them).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_policies ADD COLUMN external_binds INTEGER NOT NULL DEFAULT 0 CHECK (external_binds IN (0, 1))`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_policies DROP COLUMN external_binds`,
		)),
	)
}
