package migrations

func init() {
	// No default group (#233): new accounts join no group and are denied
	// everything until the owner adds them to a group or grants them
	// overrides; a new instance starts without groups.
	//
	//   - default_group and its permanence trigger go;
	//   - the initial Restricted group goes when it is still as created: no
	//     rules, no members, never renamed or edited (a new instance's, or
	//     one nobody used). It granted nothing, so nobody's access changes.
	//     A Restricted group in use stays an ordinary group.
	//
	// Down makes the highest-priority group the default again, creating an
	// empty Restricted group when there is none.
	Migrations.MustRegister(
		Tx(Exec(
			`DROP TRIGGER default_group_permanent`,
			`DROP TABLE default_group`,
			`DELETE FROM groups WHERE name = 'Restricted' AND revision = 1 AND permissions_revision = 1
				AND NOT EXISTS (SELECT 1 FROM group_permission_rules r WHERE r.group_id = groups.id)
				AND NOT EXISTS (SELECT 1 FROM user_groups m WHERE m.group_id = groups.id)`,
		)),
		Tx(Exec(
			`INSERT INTO groups (id, name, position, created_at, updated_at)
			 SELECT lower(hex(randomblob(16))), 'Restricted', 0, strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'),
				strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
			 WHERE NOT EXISTS (SELECT 1 FROM groups)`,
			`CREATE TABLE default_group (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
				group_id  TEXT    NOT NULL REFERENCES groups (id) ON DELETE RESTRICT
			) STRICT`,
			`INSERT INTO default_group (singleton, group_id) SELECT 1, id FROM groups ORDER BY position, id LIMIT 1`,
			`CREATE TRIGGER default_group_permanent BEFORE DELETE ON default_group
			BEGIN SELECT RAISE(ABORT, 'there must always be a default group'); END`,
		)),
	)
}
