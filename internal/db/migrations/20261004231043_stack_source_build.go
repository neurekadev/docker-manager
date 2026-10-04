package migrations

func init() {
	// Whether a stack's definition on disk has a build section (#33): the
	// Build actions follow the files, while services follow the last
	// deploy. Existing stacks start with their services' answer (stored
	// JSON without spaces; "build" is omitted when false) until the next
	// change of their files or deploy re-validates them.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE stacks ADD COLUMN source_build INTEGER NOT NULL DEFAULT 0 CHECK (source_build IN (0, 1))`,
			`UPDATE stacks SET source_build = 1 WHERE services LIKE '%"build":true%'`,
		)),
		Tx(Exec(
			`ALTER TABLE stacks DROP COLUMN source_build`,
		)),
	)
}
