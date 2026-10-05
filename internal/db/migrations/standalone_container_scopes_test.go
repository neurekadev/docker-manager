package migrations

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Rules for Recreate, Edit Settings and Remove on a stack or service go
// (they never allowed anything); the same keys elsewhere and other keys on
// a stack stay, and only the documents that changed get a new revision.
func TestStackScopedStandaloneContainerRulesAreRemoved(t *testing.T) {
	ctx := testutil.Context(t)
	rule := func(table, subject, capability, typ, id string) string {
		return `INSERT INTO ` + table + ` VALUES ('` + subject + `', '` + capability + `', 'resource', '', '` + typ + `', '` + id + `', 'allow', 0)`
	}
	g, u := "group_permission_rules", "user_permission_rules"
	db := migratedFrom(t, ctx, "20261006010000", "", []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES
			('g1', 'Ops', 0, '2026-10-01', '2026-10-01'), ('g2', 'Viewers', 1, '2026-10-01', '2026-10-01')`,
		`INSERT INTO users (id, username, is_owner, status, webauthn_handle, created_at, updated_at)
			VALUES ('u1', 'rita', 0, 'active', x'01', '2026-10-01', '2026-10-01')`,
		`INSERT INTO api_tokens (id, user_id, name, verifier, created_at) VALUES ('t1', 'u1', 'ci', '` + strings.Repeat("a", 64) + `', '2026-10-01')`,
		rule(g, "g1", "container.remove", "stack", "st-1"),
		rule(g, "g1", "container.restart", "stack", "st-1"),
		rule(g, "g2", "container.remove", "container", "web"),
		rule(u, "u1", "container.update", "service", "st-1/web"),
		`INSERT INTO api_token_scopes (token_id, capability, scope_kind, environment_id, resource_type, resource_id, position)
			VALUES ('t1', 'container.recreate', 'resource', '', 'stack', 'st-1', 0),
			('t1', 'container.recreate', 'resource', '', 'container', 'web', 1)`,
	})
	expect(t, ctx, db, `SELECT group_id || ' ' || capability || ' ' || resource_type FROM group_permission_rules ORDER BY 1`,
		"g1 container.restart stack", "g2 container.remove container")
	expect(t, ctx, db, `SELECT capability FROM user_permission_rules`)
	expect(t, ctx, db, `SELECT capability || ' ' || resource_type FROM api_token_scopes`, "container.recreate container")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM groups ORDER BY 1`, "g1 2", "g2 1")
	expect(t, ctx, db, `SELECT CAST(permissions_revision AS TEXT) FROM users`, "2")
}
