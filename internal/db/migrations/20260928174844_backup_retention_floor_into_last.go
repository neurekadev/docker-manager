package migrations

func init() {
	// The minimum recovery floor kept the newest N backups of each stack
	// and volume on top of the rules: exactly what the "last" rule does. It
	// is removed; a policy with rules and a floor above its "last" keeps the
	// same backups with "last" raised to the floor (a policy without rules
	// keeps everything anyway), and changes revision.
	Migrations.MustRegister(
		Tx(Exec(
			`UPDATE backup_policies
			SET retention = json_set(retention, '$.Last', json_extract(retention, '$.MinKeep')), revision = revision + 1
			WHERE COALESCE(json_extract(retention, '$.MinKeep'), 0) > COALESCE(json_extract(retention, '$.Last'), 0)
			AND COALESCE(json_extract(retention, '$.Last'), 0) + COALESCE(json_extract(retention, '$.Hourly'), 0) + COALESCE(json_extract(retention, '$.Daily'), 0) + COALESCE(json_extract(retention, '$.Weekly'), 0) + COALESCE(json_extract(retention, '$.Monthly'), 0) + COALESCE(json_extract(retention, '$.Yearly'), 0) + COALESCE(json_extract(retention, '$.WithinDays'), 0) > 0`,
			`UPDATE backup_policies SET retention = json_remove(retention, '$.MinKeep')
			WHERE json_type(retention, '$.MinKeep') IS NOT NULL`,
		)),
		// The floor cannot be told apart from "last" afterwards: down only
		// restores the key, as 0 (off).
		Tx(Exec(
			`UPDATE backup_policies SET retention = json_set(retention, '$.MinKeep', 0)`,
		)),
	)
}
