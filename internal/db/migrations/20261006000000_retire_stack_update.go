package migrations

// The permission catalog drops stack.update (#280): it only allowed a
// stack's Pull (the image pull without a deploy), which stack.deploy now
// covers because a deploy pulls too. Its rules and API token scopes are
// removed, not carried over: an allow carried to stack.deploy would grant
// deploys that were never allowed, and a deny would block deploys that
// were. So no deploy decision changes; holders of stack.deploy gain the
// Pull they could already do as Pull and Deploy, and a pull-only grant
// (an API-only action) loses it. The documents that changed get a new
// permissions revision, so an editor open on the old rules refuses to save
// over them. Nothing to undo: the capability is gone.
func init() {
	Migrations.MustRegister(Tx(Exec(
		`UPDATE groups SET permissions_revision = permissions_revision + 1
		 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE capability = 'stack.update')`,
		`UPDATE users SET permissions_revision = permissions_revision + 1
		 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE capability = 'stack.update')`,
		`DELETE FROM group_permission_rules WHERE capability = 'stack.update'`,
		`DELETE FROM user_permission_rules WHERE capability = 'stack.update'`,
		`DELETE FROM api_token_scopes WHERE capability = 'stack.update'`,
	)), Tx(Exec()))
}
