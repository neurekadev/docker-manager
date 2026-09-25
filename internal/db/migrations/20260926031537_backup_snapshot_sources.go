package migrations

func init() {
	// Backup restores (#10): where a snapshot keeps the stack's project
	// directory and each volume's data (snapshot paths), so a restore maps
	// them to the current places exactly.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE backup_snapshots ADD COLUMN project_path TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE backup_snapshots ADD COLUMN volume_paths TEXT NOT NULL DEFAULT '{}'`,
		)),
		Tx(Exec(
			`ALTER TABLE backup_snapshots DROP COLUMN volume_paths`,
			`ALTER TABLE backup_snapshots DROP COLUMN project_path`,
		)),
	)
}
