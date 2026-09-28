package migrations

func init() {
	// Backup policies back up buildx builder volumes (rebuildable build
	// cache) only when enabled.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_policies ADD COLUMN buildx_volumes INTEGER NOT NULL DEFAULT 0 CHECK (buildx_volumes IN (0, 1))`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_policies DROP COLUMN buildx_volumes`,
		)),
	)
}
