package migrations

func init() {
	// Authorization (#17): group rules (allow/deny per capability and
	// scope) and user override rules (absence = inherit), with optimistic
	// revisions of each permission document.
	//
	// Invariants enforced by the schema:
	//   - one rule per (subject, capability, scope): the primary key
	//     rejects ambiguous duplicates;
	//   - rules die with their group or user (ON DELETE CASCADE); a group
	//     with members cannot be deleted (users.group_id RESTRICT) and the
	//     default group cannot be deleted (default_group RESTRICT, #16).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE groups ADD COLUMN permissions_revision INTEGER NOT NULL DEFAULT 1 CHECK (permissions_revision >= 1)`,
			`ALTER TABLE users ADD COLUMN permissions_revision INTEGER NOT NULL DEFAULT 1 CHECK (permissions_revision >= 1)`,
			`CREATE TABLE group_permission_rules (
				group_id       TEXT    NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
				capability     TEXT    NOT NULL CHECK (length(capability) BETWEEN 3 AND 128),
				scope_kind     TEXT    NOT NULL CHECK (scope_kind IN ('instance', 'environment', 'resource')),
				environment_id TEXT    NOT NULL DEFAULT '',
				resource_type  TEXT    NOT NULL DEFAULT '',
				resource_id    TEXT    NOT NULL DEFAULT '',
				effect         TEXT    NOT NULL CHECK (effect IN ('allow', 'deny')),
				position       INTEGER NOT NULL,
				PRIMARY KEY (group_id, capability, scope_kind, environment_id, resource_type, resource_id)
			) STRICT`,
			`CREATE INDEX group_permission_rules_resource ON group_permission_rules (resource_type, resource_id)`,
			`CREATE TABLE user_permission_rules (
				user_id        TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
				capability     TEXT    NOT NULL CHECK (length(capability) BETWEEN 3 AND 128),
				scope_kind     TEXT    NOT NULL CHECK (scope_kind IN ('instance', 'environment', 'resource')),
				environment_id TEXT    NOT NULL DEFAULT '',
				resource_type  TEXT    NOT NULL DEFAULT '',
				resource_id    TEXT    NOT NULL DEFAULT '',
				effect         TEXT    NOT NULL CHECK (effect IN ('allow', 'deny')),
				position       INTEGER NOT NULL,
				PRIMARY KEY (user_id, capability, scope_kind, environment_id, resource_type, resource_id)
			) STRICT`,
			`CREATE INDEX user_permission_rules_resource ON user_permission_rules (resource_type, resource_id)`,
		)),
		Tx(Exec(
			`DROP TABLE user_permission_rules`,
			`DROP TABLE group_permission_rules`,
			`ALTER TABLE users DROP COLUMN permissions_revision`,
			`ALTER TABLE groups DROP COLUMN permissions_revision`,
		)),
	)
}
