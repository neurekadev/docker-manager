package migrations

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// stack.update rules and token scopes go without a carry-over, so no
// stack.deploy decision changes; only the documents that held one get a
// new revision.
func TestStackUpdateRulesAreRemoved(t *testing.T) {
	ctx := testutil.Context(t)
	rule := func(table, subject, capability, stack, effect string) string {
		return `INSERT INTO ` + table + ` VALUES ('` + subject + `', '` + capability + `', 'resource', '', 'stack', '` + stack + `', '` + effect + `', 0)`
	}
	scope := func(token, capability, stack string) string {
		return `INSERT INTO api_token_scopes (token_id, capability, scope_kind, environment_id, resource_type, resource_id, position)
			VALUES ('` + token + `', '` + capability + `', 'resource', '', 'stack', '` + stack + `', 0)`
	}
	db := migratedFrom(t, ctx, "20261006000000", "", []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES
			('g1', 'Ops', 0, '2026-10-01', '2026-10-01'), ('g2', 'Viewers', 1, '2026-10-01', '2026-10-01')`,
		`INSERT INTO users (id, username, is_owner, status, webauthn_handle, created_at, updated_at)
			VALUES ('u1', 'rita', 0, 'active', x'01', '2026-10-01', '2026-10-01'),
			('u2', 'sam', 0, 'active', x'02', '2026-10-01', '2026-10-01')`,
		`INSERT INTO api_tokens (id, user_id, name, verifier, created_at) VALUES ('t1', 'u1', 'ci', '` + strings.Repeat("a", 64) + `', '2026-10-01')`,
		rule("group_permission_rules", "g1", "stack.update", "st-1", "allow"),
		rule("group_permission_rules", "g1", "stack.deploy", "st-2", "deny"),
		rule("group_permission_rules", "g2", "stack.read", "st-1", "allow"),
		rule("user_permission_rules", "u1", "stack.update", "st-2", "deny"),
		rule("user_permission_rules", "u2", "stack.deploy", "st-1", "allow"),
		scope("t1", "stack.update", "st-1"),
		scope("t1", "stack.deploy", "st-2"),
	})
	expect(t, ctx, db, `SELECT group_id || ' ' || capability || ' ' || resource_id || ' ' || effect FROM group_permission_rules ORDER BY 1`,
		"g1 stack.deploy st-2 deny", "g2 stack.read st-1 allow")
	expect(t, ctx, db, `SELECT user_id || ' ' || capability || ' ' || resource_id FROM user_permission_rules`, "u2 stack.deploy st-1")
	expect(t, ctx, db, `SELECT capability || ' ' || resource_id FROM api_token_scopes`, "stack.deploy st-2")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM groups ORDER BY 1`, "g1 2", "g2 1")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM users ORDER BY 1`, "u1 2", "u2 1")
}
