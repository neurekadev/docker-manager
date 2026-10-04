package migrations

func init() {
	// Whether a stack's definition on disk has a build section (#33), and
	// the definition hash that answer was validated for: the Build actions
	// follow the files, while services follow the last deploy. Existing
	// stacks start with their services' answer (stored JSON without
	// spaces; "build" is omitted when false) and no hash, so the next read
	// of their files (a save, or the reconciliation when the agent
	// reconnects) validates them.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE stacks ADD COLUMN source_build INTEGER NOT NULL DEFAULT 0 CHECK (source_build IN (0, 1))`,
			`ALTER TABLE stacks ADD COLUMN source_build_hash TEXT NOT NULL DEFAULT ''`,
			`UPDATE stacks SET source_build = 1 WHERE services LIKE '%"build":true%'`,
		)),
		Tx(Exec(
			`ALTER TABLE stacks DROP COLUMN source_build_hash`,
			`ALTER TABLE stacks DROP COLUMN source_build`,
		)),
	)
}
