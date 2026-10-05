package migrations

// container.recreate, container.update and container.remove take no stack
// or service rules any more (#281): they act on standalone containers only
// (the server refuses them on a managed stack's containers), so such a
// rule never allowed anything. The catalog drops those scopes, and the
// rules and API token scopes using them go, so no decision changes and the
// documents still validate. The documents that changed get a new
// permissions revision. Nothing to undo.
func init() {
	const match = `capability IN ('container.recreate', 'container.update', 'container.remove')
		AND scope_kind = 'resource' AND resource_type IN ('stack', 'service')`
	Migrations.MustRegister(Tx(Exec(
		`UPDATE groups SET permissions_revision = permissions_revision + 1
		 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE `+match+`)`,
		`UPDATE users SET permissions_revision = permissions_revision + 1
		 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE `+match+`)`,
		`DELETE FROM group_permission_rules WHERE `+match,
		`DELETE FROM user_permission_rules WHERE `+match,
		`DELETE FROM api_token_scopes WHERE `+match,
	)), Tx(Exec()))
}
