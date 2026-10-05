package migrations

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// stack.down rules carry over to stack.stop at their scope; an existing
// stack.stop rule at the same scope stays, turned into a deny when the
// down rule denied; the documents that changed get a new revision, and
// token scopes carry over the same way.
func TestStackDownRulesBecomeStackStop(t *testing.T) {
	ctx := testutil.Context(t)
	rule := func(table, subject, capability, stack, effect string) string {
		return `INSERT INTO ` + table + ` VALUES ('` + subject + `', '` + capability + `', 'resource', '', 'stack', '` + stack + `', '` + effect + `', 0)`
	}
	scope := func(token, capability, stack string) string {
		return `INSERT INTO api_token_scopes (token_id, capability, scope_kind, environment_id, resource_type, resource_id, position)
			VALUES ('` + token + `', '` + capability + `', 'resource', '', 'stack', '` + stack + `', 0)`
	}
	db := migratedFrom(t, ctx, "20261005000000", "", []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES
			('g1', 'Ops', 0, '2026-10-01', '2026-10-01'), ('g2', 'Viewers', 1, '2026-10-01', '2026-10-01')`,
		`INSERT INTO users (id, username, is_owner, status, webauthn_handle, created_at, updated_at)
			VALUES ('u1', 'rita', 0, 'active', x'01', '2026-10-01', '2026-10-01')`,
		`INSERT INTO api_tokens (id, user_id, name, verifier, created_at) VALUES ('t1', 'u1', 'ci', '` + strings.Repeat("a", 64) + `', '2026-10-01')`,
		// Only a down rule: it becomes a stop rule.
		rule("group_permission_rules", "g1", "stack.down", "st-1", "allow"),
		// Both, the down rule denies: the stop rule turns into a deny.
		rule("group_permission_rules", "g1", "stack.stop", "st-2", "allow"),
		rule("group_permission_rules", "g1", "stack.down", "st-2", "deny"),
		// Both, the down rule allows: the stop rule stays as it is.
		rule("group_permission_rules", "g1", "stack.stop", "st-3", "deny"),
		rule("group_permission_rules", "g1", "stack.down", "st-3", "allow"),
		// Untouched.
		rule("group_permission_rules", "g2", "stack.read", "st-1", "allow"),
		rule("user_permission_rules", "u1", "stack.down", "st-4", "deny"),
		scope("t1", "stack.down", "st-1"),
		scope("t1", "stack.stop", "st-2"),
		scope("t1", "stack.down", "st-2"),
	})
	expect(t, ctx, db, `SELECT group_id || ' ' || capability || ' ' || resource_id || ' ' || effect FROM group_permission_rules ORDER BY 1`,
		"g1 stack.stop st-1 allow", "g1 stack.stop st-2 deny", "g1 stack.stop st-3 deny", "g2 stack.read st-1 allow")
	expect(t, ctx, db, `SELECT capability || ' ' || resource_id || ' ' || effect FROM user_permission_rules`, "stack.stop st-4 deny")
	expect(t, ctx, db, `SELECT capability || ' ' || resource_id FROM api_token_scopes ORDER BY 1`, "stack.stop st-1", "stack.stop st-2")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM groups ORDER BY 1`, "g1 2", "g2 1")
	expect(t, ctx, db, `SELECT CAST(permissions_revision AS TEXT) FROM users`, "2")
}
