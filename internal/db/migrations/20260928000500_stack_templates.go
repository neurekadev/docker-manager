package migrations

func init() {
	// Stacks created from a template (template registry) remember the
	// template version: template_instance_id identifies the registry (the
	// manager instance that owns the template), so a removed and re-added
	// registry finds its stacks again. Empty for other stacks.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE stacks ADD COLUMN template_instance_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE stacks ADD COLUMN template_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE stacks ADD COLUMN template_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE stacks ADD COLUMN template_version INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE stacks ADD COLUMN template_version_label TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX stacks_template ON stacks (template_instance_id, template_id)`,
		)),
		Tx(Exec(
			`DROP INDEX stacks_template`,
			`ALTER TABLE stacks DROP COLUMN template_version_label`,
			`ALTER TABLE stacks DROP COLUMN template_version`,
			`ALTER TABLE stacks DROP COLUMN template_name`,
			`ALTER TABLE stacks DROP COLUMN template_id`,
			`ALTER TABLE stacks DROP COLUMN template_instance_id`,
		)),
	)
}
