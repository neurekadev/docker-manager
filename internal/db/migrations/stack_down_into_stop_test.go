package migrations

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// stack.down rules carry over to stack.stop at their scope; where both
// exist at one scope, the stop rule takes the down rule's effect (Stop is
// a down now); the documents that changed get a new revision, and token
// scopes carry over the same way.
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
		// Both, the down rule allows: down was allowed, so Stop is.
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
		"g1 stack.stop st-1 allow", "g1 stack.stop st-2 deny", "g1 stack.stop st-3 allow", "g2 stack.read st-1 allow")
	expect(t, ctx, db, `SELECT capability || ' ' || resource_id || ' ' || effect FROM user_permission_rules`, "stack.stop st-4 deny")
	expect(t, ctx, db, `SELECT capability || ' ' || resource_id FROM api_token_scopes ORDER BY 1`, "stack.stop st-1", "stack.stop st-2")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM groups ORDER BY 1`, "g1 2", "g2 1")
	expect(t, ctx, db, `SELECT CAST(permissions_revision AS TEXT) FROM users`, "2")
}

// Once Stop runs Compose down, nobody gains a down that was denied and
// nobody loses one that was allowed, at the instance, each environment
// and each stack, with the evaluator's precedence: a group's narrower stop
// allow under its down deny turns into a deny (environments by their
// stacks); a member's own stop allow, which beats every group rule, turns
// into a deny under a group's down deny, and a member granted stop by a
// higher group but denied down by a lower one gets a deny of their own. A
// more specific down allow (in the group, the member's own, or a group
// asked first, also over part of the deny's scope) keeps the stop allowed.
func TestStackDownDecisionsSurviveTheMigration(t *testing.T) {
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
	group := func(id string, pos int) string {
		return `INSERT INTO groups (id, name, position, created_at, updated_at) VALUES ('` + id + `', '` + id + `', ` +
			string(rune('0'+pos)) + `, ` + at + `, ` + at + `)`
	}
	user := func(id string, handle byte) string {
		return `INSERT INTO users (id, username, is_owner, status, webauthn_handle, created_at, updated_at)
			VALUES ('` + id + `', '` + id + `', 0, 'active', x'0` + string(rune(handle)) + `', ` + at + `, ` + at + `)`
	}
	member := func(user, group string) string {
		return `INSERT INTO user_groups (user_id, group_id) VALUES ('` + user + `', '` + group + `')`
	}
	db := migratedFrom(t, ctx, "20261005000000", "", []string{
		`INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at) VALUES
			('env-1', 'one', 'E-1', 'I-1', 'active', 1, ` + at + `, ` + at + `),
			('env-2', 'two', 'E-2', 'I-2', 'active', 1, ` + at + `, ` + at + `)`,
		stack("st-a", "env-1"), stack("st-b", "env-2"),
		group("lead", 0), group("ops", 1), group("dev", 2), group("qa", 3),
		group("alpha", 4), group("beta", 5), group("open", 6), group("closed", 7),
		user("rita", '1'), user("sam", '2'), user("kim", '3'), user("lea", '4'), user("max", '5'),
		user("zoe", '6'), user("ivy", '7'), user("gus", '8'),
		member("rita", "ops"), member("sam", "ops"), member("kim", "ops"), member("lea", "lead"),
		member("max", "ops"), member("max", "lead"), member("zoe", "qa"),
		member("ivy", "alpha"), member("ivy", "beta"), member("gus", "open"), member("gus", "closed"),
		// ops: no down anywhere, but stop on one stack.
		rule(g, "ops", "stack.down", "instance", "", "", "deny"),
		rule(g, "ops", "stack.stop", "resource", "", "st-a", "allow"),
		// dev: no down in env-1; stop on a stack there and one in env-2.
		rule(g, "dev", "stack.down", "environment", "env-1", "", "deny"),
		rule(g, "dev", "stack.stop", "resource", "", "st-a", "allow"),
		rule(g, "dev", "stack.stop", "resource", "", "st-b", "allow"),
		// qa: no down, except in env-1 (more specific); stop on a stack there.
		rule(g, "qa", "stack.down", "instance", "", "", "deny"),
		rule(g, "qa", "stack.down", "environment", "env-1", "", "allow"),
		rule(g, "qa", "stack.stop", "resource", "", "st-a", "allow"),
		// lead (asked before ops): stop and down allowed at the same scope.
		rule(g, "lead", "stack.stop", "instance", "", "", "allow"),
		rule(g, "lead", "stack.down", "instance", "", "", "allow"),
		// alpha (asked before beta) allows down in env-1 only; beta denies it.
		rule(g, "alpha", "stack.down", "environment", "env-1", "", "allow"),
		rule(g, "beta", "stack.down", "instance", "", "", "deny"),
		// open (asked before closed) allows stop; closed denies down.
		rule(g, "open", "stack.stop", "instance", "", "", "allow"),
		rule(g, "closed", "stack.down", "instance", "", "", "deny"),
		// rita (ops) allows herself stop everywhere: her rule beat the group.
		rule(u, "rita", "stack.stop", "instance", "", "", "allow"),
		// kim (ops) allows herself down everywhere: the group's deny never applied.
		rule(u, "kim", "stack.down", "instance", "", "", "allow"),
		rule(u, "kim", "stack.stop", "resource", "", "st-a", "allow"),
		// ivy (alpha, beta) allows herself stop on a stack in env-1.
		rule(u, "ivy", "stack.stop", "resource", "", "st-a", "allow"),
	})
	expect(t, ctx, db, `SELECT group_id || ' ' || capability || ' ' || scope_kind || ':' || environment_id || resource_id || ' ' || effect
		FROM group_permission_rules ORDER BY 1`,
		"alpha stack.stop environment:env-1 allow",
		"beta stack.stop instance: deny",
		"closed stack.stop instance: deny",
		"dev stack.stop environment:env-1 deny", "dev stack.stop resource:st-a deny", "dev stack.stop resource:st-b allow",
		"lead stack.stop instance: allow",
		"open stack.stop instance: allow",
		"ops stack.stop instance: deny", "ops stack.stop resource:st-a deny",
		"qa stack.stop environment:env-1 allow", "qa stack.stop instance: deny", "qa stack.stop resource:st-a allow")
	// gus: open's stop allow would decide, but closed denied down: his own
	// deny. ivy: alpha decided down in env-1, beta elsewhere: her stop on
	// st-a stays. kim allowed down everywhere. rita's allow turns into a
	// deny. sam, lea, max (lead decided before ops) and zoe (qa's env-1
	// allow) need no rule of their own.
	expect(t, ctx, db, `SELECT user_id || ' ' || capability || ' ' || scope_kind || ':' || resource_id || ' ' || effect
		FROM user_permission_rules ORDER BY 1`,
		"gus stack.stop instance: deny",
		"ivy stack.stop resource:st-a allow",
		"kim stack.stop instance: allow", "kim stack.stop resource:st-a allow",
		"rita stack.stop instance: deny")
	expect(t, ctx, db, `SELECT id || ' ' || permissions_revision FROM users ORDER BY 1`,
		"gus 2", "ivy 1", "kim 2", "lea 1", "max 1", "rita 2", "sam 1", "zoe 1")
}
