package migrations

func init() {
	// Job engine (#26): the result output an interrupted attempt reported
	// (or a manager-local attempt journaled) is kept with the job, so a
	// resumed attempt continues from it even when its command is re-sent
	// after a manager restart ("" = none).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE jobs ADD COLUMN resume_output TEXT NOT NULL DEFAULT ''`,
		)),
		Tx(Exec(
			`ALTER TABLE jobs DROP COLUMN resume_output`,
		)),
	)
}
