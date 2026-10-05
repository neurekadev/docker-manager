package migrations

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/uptrace/bun"
)

// The permission catalog drops stack.down: a stack's Stop runs Compose down
// and needs only stack.stop (#274). Rules naming stack.down carry over to
// stack.stop at the same scope where no stack.stop rule exists there.
//
// Nobody gains a down that was denied to them (deny wins, even where that
// costs a plain stop someone had):
//   - a stack.stop allow covered by a stack.down deny of the same group or
//     user (the same scope or a narrower one) becomes a deny, unless the
//     subject allows stack.down at exactly that scope;
//   - a group's stack.down deny also becomes a stack.stop deny at its scope
//     on every member's own rules (a user rule beats every group rule, and a
//     higher group beats a lower one, so an allow elsewhere would otherwise
//     win), with the member's narrower stack.stop allows turned into
//     denies, unless the member allows stack.down at a scope covering it.
//
// The documents that changed get a new permissions revision, so an editor
// open on the old rules refuses to save over them. API token scopes (allow
// only, intersected with the owner's permissions) carry over to stack.stop.
// Nothing to undo: the capability is gone.
func init() {
	Migrations.MustRegister(Tx(stackDownIntoStop), Tx(Exec()))
}

// permScope is a rule's scope: instance, an environment, or one stack.
type permScope struct{ kind, env, typ, id string }

// permRule is a stack.down or stack.stop rule of a group or user.
type permRule struct {
	subject, capability, effect string
	scope                       permScope
}

func stackDownIntoStop(ctx context.Context, tx bun.Tx) error {
	stackEnv := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT id, environment_id FROM stacks`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, env string
		if err := rows.Scan(&id, &env); err != nil {
			_ = rows.Close()
			return err
		}
		stackEnv[id] = env
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	// covers reports whether rules at outer apply wherever rules at inner do.
	covers := func(outer, inner permScope) bool {
		switch outer.kind {
		case "instance":
			return true
		case "environment":
			return (inner.kind == "environment" && inner.env == outer.env) ||
				(inner.kind == "resource" && inner.typ == "stack" && stackEnv[inner.id] == outer.env)
		default:
			return inner.kind == "resource" && inner.typ == outer.typ && inner.id == outer.id
		}
	}
	type table struct{ name, subject, parent string }
	groups := table{"group_permission_rules", "group_id", "groups"}
	users := table{"user_permission_rules", "user_id", "users"}
	load := func(t table) ([]permRule, error) {
		rows, err := tx.QueryContext(ctx, `SELECT `+t.subject+`, capability, effect, scope_kind, environment_id, resource_type, resource_id
			FROM `+t.name+` WHERE capability IN ('stack.down', 'stack.stop')`)
		if err != nil {
			return nil, err
		}
		var out []permRule
		for rows.Next() {
			var r permRule
			if err := rows.Scan(&r.subject, &r.capability, &r.effect, &r.scope.kind, &r.scope.env, &r.scope.typ, &r.scope.id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		return out, errors.Join(rows.Err(), rows.Close())
	}
	groupRules, err := load(groups)
	if err != nil {
		return err
	}
	userRules, err := load(users)
	if err != nil {
		return err
	}
	members := map[string][]string{}
	rows, err = tx.QueryContext(ctx, `SELECT group_id, user_id FROM user_groups`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var g, u string
		if err := rows.Scan(&g, &u); err != nil {
			_ = rows.Close()
			return err
		}
		members[g] = append(members[g], u)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}

	changed := map[table]map[string]bool{groups: {}, users: {}}
	where := func(r permRule) (string, []any) {
		return ` WHERE capability = 'stack.stop' AND scope_kind = ? AND environment_id = ? AND resource_type = ? AND resource_id = ?`,
			[]any{r.scope.kind, r.scope.env, r.scope.typ, r.scope.id}
	}
	deny := func(t table, r permRule) error {
		w, args := where(r)
		_, err := tx.ExecContext(ctx, `UPDATE `+t.name+` SET effect = 'deny'`+w+` AND `+t.subject+` = ?`, append(args, r.subject)...)
		changed[t][r.subject] = true
		return err
	}
	allowsDownAt := func(rules []permRule, subject string, s permScope) bool {
		for _, r := range rules {
			if r.subject == subject && r.capability == "stack.down" && r.effect == "allow" && r.scope == s {
				return true
			}
		}
		return false
	}
	// Same subject: a stack.stop allow under a stack.down deny becomes a deny.
	for _, set := range []struct {
		t     table
		rules []permRule
	}{{groups, groupRules}, {users, userRules}} {
		for _, d := range set.rules {
			if d.capability != "stack.down" || d.effect != "deny" {
				continue
			}
			for _, a := range set.rules {
				if a.subject == d.subject && a.capability == "stack.stop" && a.effect == "allow" && covers(d.scope, a.scope) &&
					!allowsDownAt(set.rules, a.subject, a.scope) {
					if err := deny(set.t, a); err != nil {
						return err
					}
				}
			}
		}
	}
	// A group's stack.down deny on its members' own rules.
	for _, d := range groupRules {
		if d.capability != "stack.down" || d.effect != "deny" {
			continue
		}
		for _, u := range members[d.subject] {
			if slices.ContainsFunc(userRules, func(r permRule) bool {
				return r.subject == u && r.capability == "stack.down" && r.effect == "allow" && covers(r.scope, d.scope)
			}) {
				continue
			}
			for _, a := range userRules {
				if a.subject == u && a.capability == "stack.stop" && a.effect == "allow" && covers(d.scope, a.scope) &&
					!allowsDownAt(userRules, u, a.scope) {
					if err := deny(users, a); err != nil {
						return err
					}
				}
			}
			has := slices.ContainsFunc(userRules, func(r permRule) bool {
				return r.subject == u && r.capability == "stack.stop" && r.scope == d.scope
			})
			if has {
				continue
			}
			added := permRule{subject: u, capability: "stack.stop", effect: "deny", scope: d.scope}
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_permission_rules
				(user_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
				VALUES (?, 'stack.stop', ?, ?, ?, ?, 'deny',
				  (SELECT COALESCE(MAX(position), -1) + 1 FROM user_permission_rules WHERE user_id = ?))`,
				u, d.scope.kind, d.scope.env, d.scope.typ, d.scope.id, u); err != nil {
				return fmt.Errorf("deny stack.stop for a member of a group denying stack.down: %w", err)
			}
			userRules = append(userRules, added)
			changed[users][u] = true
		}
	}
	// Every subject with a stack.down rule changes too.
	for _, r := range groupRules {
		if r.capability == "stack.down" {
			changed[groups][r.subject] = true
		}
	}
	for _, r := range userRules {
		if r.capability == "stack.down" {
			changed[users][r.subject] = true
		}
	}
	for _, t := range []table{groups, users} {
		for subject := range changed[t] {
			if _, err := tx.ExecContext(ctx, `UPDATE `+t.parent+` SET permissions_revision = permissions_revision + 1 WHERE id = ?`, subject); err != nil {
				return err
			}
		}
		same := `o.` + t.subject + ` = r.` + t.subject + ` AND o.scope_kind = r.scope_kind AND o.environment_id = r.environment_id
			AND o.resource_type = r.resource_type AND o.resource_id = r.resource_id`
		if err := Exec(
			`UPDATE `+t.name+` AS r SET capability = 'stack.stop'
			 WHERE r.capability = 'stack.down' AND NOT EXISTS (
			   SELECT 1 FROM `+t.name+` AS o WHERE o.capability = 'stack.stop' AND `+same+`)`,
			`DELETE FROM `+t.name+` WHERE capability = 'stack.down'`,
		)(ctx, tx); err != nil {
			return err
		}
	}
	return Exec(
		`UPDATE api_token_scopes AS r SET capability = 'stack.stop'
		 WHERE r.capability = 'stack.down' AND NOT EXISTS (
		   SELECT 1 FROM api_token_scopes AS o WHERE o.capability = 'stack.stop' AND o.token_id = r.token_id
		     AND o.scope_kind = r.scope_kind AND o.environment_id = r.environment_id
		     AND o.resource_type = r.resource_type AND o.resource_id = r.resource_id)`,
		`DELETE FROM api_token_scopes WHERE capability = 'stack.down'`,
	)(ctx, tx)
}
