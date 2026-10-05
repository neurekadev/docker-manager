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

// A stack.down deny is never bypassed once Stop runs Compose down: a
// narrower stack.stop allow of the same group turns into a deny (within an
// environment too, by the stack's environment), and a group's deny reaches
// its members' own rules, which beat every group rule; a member allowing
// stack.down over the deny's scope keeps it, and a stop allow beside a
// down allow at the same scope stays.
func TestStackDownDeniesAreNeverBypassed(t *testing.T) {
	ctx := testutil.Context(t)
	at := `'2026-10-01'`
	rule := func(table, subject, capability, kind, env, stack, effect string) string {
		typ := ""
		if stack != "" {
			typ = "stack"
		}
		return `INSERT INTO ` + table + ` VALUES ('` + subject + `', '` + capability + `', '` + kind + `', '` + env + `', '` + typ + `', '` + stack + `', '` + effect + `', 0)`
	}
	g, u := "group_permission_rules", "user_permission_rules"
	stack := func(id, env string) string {
		return `INSERT INTO stacks (id, environment_id, name, root, dir, origin, status, created_at, updated_at)
			VALUES ('` + id + `', '` + env + `', '` + id + `', 'stacks', '` + id + `', 'created', 'deployed', ` + at + `, ` + at + `)`
	}
	db := migratedFrom(t, ctx, "20261005000000", "", []string{
		`INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at) VALUES
			('env-1', 'one', 'E-1', 'I-1', 'active', 1, ` + at + `, ` + at + `),
			('env-2', 'two', 'E-2', 'I-2', 'active', 1, ` + at + `, ` + at + `)`,
		stack("st-a", "env-1"), stack("st-b", "env-2"),
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES
			('ops', 'Ops', 0, ` + at + `, ` + at + `), ('lead', 'Lead', 1, ` + at + `, ` + at + `), ('dev', 'Dev', 2, ` + at + `, ` + at + `)`,
		`INSERT INTO users (id, username, is_owner, status, webauthn_handle, created_at, updated_at) VALUES
			('rita', 'rita', 0, 'active', x'01', ` + at + `, ` + at + `),
			('sam', 'sam', 0, 'active', x'02', ` + at + `, ` + at + `),
			('kim', 'kim', 0, 'active', x'03', ` + at + `, ` + at + `),
			('lea', 'lea', 0, 'active', x'04', ` + at + `, ` + at + `)`,
		`INSERT INTO user_groups (user_id, group_id) VALUES ('rita', 'ops'), ('sam', 'ops'), ('kim', 'ops'), ('lea', 'lead')`,
		// ops: no down anywhere, but stop on one stack.
		rule(g, "ops", "stack.down", "instance", "", "", "deny"),
		rule(g, "ops", "stack.stop", "resource", "", "st-a", "allow"),
		// dev: no down in env-1; stop on a stack there and one in env-2.
		rule(g, "dev", "stack.down", "environment", "env-1", "", "deny"),
		rule(g, "dev", "stack.stop", "resource", "", "st-a", "allow"),
		rule(g, "dev", "stack.stop", "resource", "", "st-b", "allow"),
		// lead: stop and down allowed at the same scope.
		rule(g, "lead", "stack.stop", "instance", "", "", "allow"),
		rule(g, "lead", "stack.down", "instance", "", "", "allow"),
		// rita (ops) allows herself stop everywhere: her rule beat the group.
		rule(u, "rita", "stack.stop", "instance", "", "", "allow"),
		// kim (ops) allows herself down everywhere: the group's deny never applied.
		rule(u, "kim", "stack.down", "instance", "", "", "allow"),
		rule(u, "kim", "stack.stop", "resource", "", "st-a", "allow"),
	})
	expect(t, ctx, db, `SELECT group_id || ' ' || capability || ' ' || scope_kind || ':' || environment_id || resource_id || ' ' || effect
		FROM group_permission_rules ORDER BY 1`,
		"dev stack.stop environment:env-1 deny", "dev stack.stop resource:st-a deny", "dev stack.stop resource:st-b allow",
		"lead stack.stop instance: allow",
		"ops stack.stop instance: deny", "ops stack.stop resource:st-a deny")
	// rita's allow turns into a deny; sam (no rules of his own) gets the
	// group's deny; kim allowed down over the whole scope and keeps it all;
	// lea's group allows down: untouched.
	expect(t, ctx, db, `SELECT user_id || ' ' || capability || ' ' || scope_kind || ':' || resource_id || ' ' || effect
		FROM user_permission_rules ORDER BY 1`,
		"kim stack.stop instance: allow", "kim stack.stop resource:st-a allow",
		"rita stack.stop instance: deny", "sam stack.stop instance: deny")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM users ORDER BY 1`, "kim 2", "lea 1", "rita 2", "sam 2")
}
