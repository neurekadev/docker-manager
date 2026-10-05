package migrations

// The permission catalog drops stack.down: a stack's Stop runs Compose down
// and needs only stack.stop (#274). Rules naming stack.down carry over to
// stack.stop at the same scope where no stack.stop rule exists there; where
// both exist and one denies, the deny wins (nobody gains a down that was
// denied to them). The documents that changed get a new permissions
// revision, so an editor open on the old rules refuses to save over them.
// API token scopes (allow only) carry over the same way. Nothing to undo:
// the capability is gone.
func init() {
	var stmts []string
	for _, t := range []struct{ table, subject, parent string }{
		{"group_permission_rules", "group_id", "groups"},
		{"user_permission_rules", "user_id", "users"},
	} {
		same := `o.` + t.subject + ` = r.` + t.subject + ` AND o.scope_kind = r.scope_kind AND o.environment_id = r.environment_id
			AND o.resource_type = r.resource_type AND o.resource_id = r.resource_id`
		stmts = append(stmts,
			`UPDATE `+t.parent+` SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT `+t.subject+` FROM `+t.table+` WHERE capability = 'stack.down')`,
			`UPDATE `+t.table+` AS r SET effect = 'deny'
			 WHERE r.capability = 'stack.stop' AND r.effect = 'allow' AND EXISTS (
			   SELECT 1 FROM `+t.table+` AS o WHERE o.capability = 'stack.down' AND o.effect = 'deny' AND `+same+`)`,
			`UPDATE `+t.table+` AS r SET capability = 'stack.stop'
			 WHERE r.capability = 'stack.down' AND NOT EXISTS (
			   SELECT 1 FROM `+t.table+` AS o WHERE o.capability = 'stack.stop' AND `+same+`)`,
			`DELETE FROM `+t.table+` WHERE capability = 'stack.down'`,
		)
	}
	stmts = append(stmts,
		`UPDATE api_token_scopes AS r SET capability = 'stack.stop'
		 WHERE r.capability = 'stack.down' AND NOT EXISTS (
		   SELECT 1 FROM api_token_scopes AS o WHERE o.capability = 'stack.stop' AND o.token_id = r.token_id
		     AND o.scope_kind = r.scope_kind AND o.environment_id = r.environment_id
		     AND o.resource_type = r.resource_type AND o.resource_id = r.resource_id)`,
		`DELETE FROM api_token_scopes WHERE capability = 'stack.down'`,
	)
	Migrations.MustRegister(Tx(Exec(stmts...)), Tx(Exec()))
}
