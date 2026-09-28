package migrations

func init() {
	// Links of stacks and templates (documentation, website, repository):
	// an ordered JSON list of {label, url}. Registry entries cache the links
	// another instance's registry lists for its templates.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE stacks ADD COLUMN links TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE templates ADD COLUMN links TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE template_registry_entries ADD COLUMN links TEXT NOT NULL DEFAULT '[]'`,
		)),
		Tx(Exec(
			`ALTER TABLE template_registry_entries DROP COLUMN links`,
			`ALTER TABLE templates DROP COLUMN links`,
			`ALTER TABLE stacks DROP COLUMN links`,
		)),
	)
}
