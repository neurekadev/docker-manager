package migrations

import (
	"context"
	"errors"
	"slices"

	"github.com/uptrace/bun"
)

// The permission catalog drops stack.down: a stack's Stop runs Compose down
// and needs only stack.stop (#274). Rules naming stack.down carry over to
// stack.stop at the same scope where no stack.stop rule exists there; the
// others go.
//
// Nobody gains a down that was denied to them, and nobody loses one that
// was allowed. stack.stop and stack.down are granted on the instance, an
// environment or one stack, so the decision is checked at each of those
// points, top-down, with the evaluator's precedence (the most specific
// user rule, else the first group in priority order with a matching rule,
// decided by its most specific one): where an explicit stack.down rule
// decided before, the new stack.stop decision must say the same; elsewhere
// it must equal the old stack.stop decision (a plain stop granted without
// any down rule now brings the stack down, on purpose). Each group is
// fixed on its own first, then each member, whose own rules beat every
// group rule; a mismatch gets a stack.stop rule at that point (an existing
// one there takes the right effect).
//
// The documents that changed get a new permissions revision, so an editor
// open on the old rules refuses to save over them. API token scopes (allow
// only, intersected with the owner's permissions) carry over to stack.stop:
// a scope row never changes (trigger api_token_scopes_fixed), so the
// stack.stop rows are inserted and the stack.down rows deleted (#286: an
// UPDATE aborted the migration). Nothing to undo: the capability is gone.
func init() {
	Migrations.MustRegister(Tx(stackDownIntoStop), Tx(Exec()))
}

// permScope is a rule's scope: instance, an environment, or one stack.
type permScope struct{ kind, env, typ, id string }

// specificity orders scopes: a stack beats its environment beats instance.
func (s permScope) specificity() int {
	switch s.kind {
	case "resource":
		return 2
	case "environment":
		return 1
	}
	return 0
}

// permRule is a stack.down or stack.stop rule of a group or user
// (position -1: a rule the migration adds).
type permRule struct {
	capability, effect string
	scope              permScope
	position           int
}

// permSet is one group's or user's stack.down and stack.stop rules.
type permSet struct {
	rules   []permRule
	changed bool
}

func stackDownIntoStop(ctx context.Context, tx bun.Tx) error {
	query := func(q string, scan func(scan func(...any) error) error) error {
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := scan(rows.Scan); err != nil {
				_ = rows.Close()
				return err
			}
		}
		return errors.Join(rows.Err(), rows.Close())
	}
	stackEnv := map[string]string{}
	if err := query(`SELECT id, environment_id FROM stacks`, func(scan func(...any) error) error {
		var id, env string
		err := scan(&id, &env)
		stackEnv[id] = env
		return err
	}); err != nil {
		return err
	}
	var envs []string
	if err := query(`SELECT id FROM environments`, func(scan func(...any) error) error {
		var id string
		err := scan(&id)
		envs = append(envs, id)
		return err
	}); err != nil {
		return err
	}
	type rank struct {
		pos int
		id  string
	}
	var groupOrder []rank
	if err := query(`SELECT id, position FROM groups`, func(scan func(...any) error) error {
		var r rank
		err := scan(&r.id, &r.pos)
		groupOrder = append(groupOrder, r)
		return err
	}); err != nil {
		return err
	}
	// Groups are asked highest first: by position, then ID.
	slices.SortFunc(groupOrder, func(a, b rank) int {
		if a.pos != b.pos {
			return a.pos - b.pos
		}
		if a.id < b.id {
			return -1
		}
		return 1
	})
	memberOf := map[string][]string{}
	if err := query(`SELECT user_id, group_id FROM user_groups`, func(scan func(...any) error) error {
		var u, g string
		err := scan(&u, &g)
		memberOf[u] = append(memberOf[u], g)
		return err
	}); err != nil {
		return err
	}
	owners := map[string]bool{}
	if err := query(`SELECT id FROM users WHERE is_owner = 1`, func(scan func(...any) error) error {
		var id string
		err := scan(&id)
		owners[id] = true
		return err
	}); err != nil {
		return err
	}
	type table struct{ name, subject, parent string }
	groupsT := table{"group_permission_rules", "group_id", "groups"}
	usersT := table{"user_permission_rules", "user_id", "users"}
	load := func(t table) (map[string]*permSet, error) {
		out := map[string]*permSet{}
		err := query(`SELECT `+t.subject+`, capability, effect, scope_kind, environment_id, resource_type, resource_id, position
			FROM `+t.name+` WHERE capability IN ('stack.down', 'stack.stop')`, func(scan func(...any) error) error {
			var subject string
			var r permRule
			if err := scan(&subject, &r.capability, &r.effect, &r.scope.kind, &r.scope.env, &r.scope.typ, &r.scope.id, &r.position); err != nil {
				return err
			}
			if out[subject] == nil {
				out[subject] = &permSet{}
			}
			out[subject].rules = append(out[subject].rules, r)
			return nil
		})
		return out, err
	}
	groupRules, err := load(groupsT)
	if err != nil {
		return err
	}
	userRules, err := load(usersT)
	if err != nil {
		return err
	}

	// The points the decision is checked at, top-down: instance, every
	// environment, every stack (also those only rules still name).
	stacks := map[string]bool{}
	for id := range stackEnv {
		stacks[id] = true
	}
	for _, sets := range []map[string]*permSet{groupRules, userRules} {
		for _, ps := range sets {
			for _, r := range ps.rules {
				if r.scope.kind == "environment" && !slices.Contains(envs, r.scope.env) {
					envs = append(envs, r.scope.env)
				}
				if r.scope.kind == "resource" {
					stacks[r.scope.id] = true
				}
			}
		}
	}
	slices.Sort(envs)
	points := []permScope{{kind: "instance"}}
	for _, e := range envs {
		points = append(points, permScope{kind: "environment", env: e})
	}
	stackIDs := make([]string, 0, len(stacks))
	for id := range stacks {
		stackIDs = append(stackIDs, id)
	}
	slices.Sort(stackIDs)
	for _, id := range stackIDs {
		points = append(points, permScope{kind: "resource", typ: "stack", id: id})
	}
	// matches reports whether a rule at s applies at the point p.
	matches := func(s, p permScope) bool {
		switch s.kind {
		case "instance":
			return true
		case "environment":
			return (p.kind == "environment" && p.env == s.env) || (p.kind == "resource" && s.env != "" && stackEnv[p.id] == s.env)
		default:
			return p.kind == "resource" && p.typ == s.typ && p.id == s.id
		}
	}
	// decide is the most specific matching rule's effect ("" when none).
	decide := func(rules []permRule, capability string, p permScope) string {
		effect, best := "", -1
		for _, r := range rules {
			if r.capability == capability && matches(r.scope, p) && r.scope.specificity() > best {
				effect, best = r.effect, r.scope.specificity()
			}
		}
		return effect
	}
	// evaluate is the evaluator's decision for a member: its own rules, else
	// the first of its groups (highest first) with a matching rule.
	evaluate := func(own []permRule, groups []string, sets map[string]*permSet, capability string, p permScope) string {
		if e := decide(own, capability, p); e != "" {
			return e
		}
		for _, g := range groups {
			if ps := sets[g]; ps != nil {
				if e := decide(ps.rules, capability, p); e != "" {
					return e
				}
			}
		}
		return ""
	}
	// want is the stack.stop decision that keeps the old one: an explicit
	// stack.down decision, else the old stack.stop decision.
	want := func(down, stop string) bool {
		if down != "" {
			return down == "allow"
		}
		return stop == "allow"
	}
	// carry renames stack.down to stack.stop where no stack.stop rule exists
	// at the same scope and drops the other stack.down rules.
	carry := func(rules []permRule) []permRule {
		var out []permRule
		for _, r := range rules {
			if r.capability == "stack.stop" {
				out = append(out, r)
			}
		}
		for _, r := range rules {
			if r.capability == "stack.down" && !slices.ContainsFunc(out, func(o permRule) bool { return o.scope == r.scope }) {
				r.capability = "stack.stop"
				out = append(out, r)
			}
		}
		return out
	}
	// fix gives the subject's stack.stop rule at p the effect allow.
	fix := func(ps *permSet, p permScope, allow bool) {
		effect := map[bool]string{true: "allow", false: "deny"}[allow]
		ps.changed = true
		for i, r := range ps.rules {
			if r.scope == p {
				ps.rules[i].effect = effect
				return
			}
		}
		ps.rules = append(ps.rules, permRule{capability: "stack.stop", effect: effect, scope: p, position: -1})
	}

	// Groups on their own.
	oldGroups := map[string]*permSet{}
	for id, ps := range groupRules {
		old := slices.Clone(ps.rules)
		oldGroups[id] = &permSet{rules: old}
		ps.changed = slices.ContainsFunc(old, func(r permRule) bool { return r.capability == "stack.down" })
		ps.rules = carry(old)
		for _, p := range points {
			w := want(decide(old, "stack.down", p), decide(old, "stack.stop", p))
			if decide(ps.rules, "stack.stop", p) == "allow" != w {
				fix(ps, p, w)
			}
		}
	}
	// Members, with their groups in priority order.
	ordered := func(gs []string) []string {
		var out []string
		for _, r := range groupOrder {
			if slices.Contains(gs, r.id) {
				out = append(out, r.id)
			}
		}
		return out
	}
	var users []string
	for u := range userRules {
		users = append(users, u)
	}
	for u, gs := range memberOf {
		if userRules[u] == nil && slices.ContainsFunc(gs, func(g string) bool { return groupRules[g] != nil }) {
			users = append(users, u)
		}
	}
	for _, u := range users {
		if owners[u] {
			continue
		}
		ps := userRules[u]
		if ps == nil {
			ps = &permSet{}
			userRules[u] = ps
		}
		old := slices.Clone(ps.rules)
		ps.changed = slices.ContainsFunc(old, func(r permRule) bool { return r.capability == "stack.down" })
		ps.rules = carry(old)
		groups := ordered(memberOf[u])
		for _, p := range points {
			w := want(evaluate(old, groups, oldGroups, "stack.down", p), evaluate(old, groups, oldGroups, "stack.stop", p))
			if evaluate(ps.rules, groups, groupRules, "stack.stop", p) == "allow" != w {
				fix(ps, p, w)
			}
		}
	}

	// Write the changed documents back.
	for _, w := range []struct {
		t    table
		sets map[string]*permSet
	}{{groupsT, groupRules}, {usersT, userRules}} {
		for subject, ps := range w.sets {
			if !ps.changed {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+w.t.name+` WHERE `+w.t.subject+` = ? AND capability IN ('stack.down', 'stack.stop')`,
				subject); err != nil {
				return err
			}
			for _, r := range ps.rules {
				if _, err := tx.ExecContext(ctx, `INSERT INTO `+w.t.name+`
					(`+w.t.subject+`, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
					VALUES (?, 'stack.stop', ?, ?, ?, ?, ?,
					  CASE WHEN ? >= 0 THEN ? ELSE (SELECT COALESCE(MAX(position), -1) + 1 FROM `+w.t.name+` WHERE `+w.t.subject+` = ?) END)`,
					subject, r.scope.kind, r.scope.env, r.scope.typ, r.scope.id, r.effect, r.position, r.position, subject); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+w.t.parent+` SET permissions_revision = permissions_revision + 1 WHERE id = ?`, subject); err != nil {
				return err
			}
		}
	}
	return Exec(
		`INSERT INTO api_token_scopes (token_id, capability, scope_kind, environment_id, resource_type, resource_id, position)
		 SELECT r.token_id, 'stack.stop', r.scope_kind, r.environment_id, r.resource_type, r.resource_id, r.position
		 FROM api_token_scopes AS r
		 WHERE r.capability = 'stack.down' AND NOT EXISTS (
		   SELECT 1 FROM api_token_scopes AS o WHERE o.capability = 'stack.stop' AND o.token_id = r.token_id
		     AND o.scope_kind = r.scope_kind AND o.environment_id = r.environment_id
		     AND o.resource_type = r.resource_type AND o.resource_id = r.resource_id)`,
		`DELETE FROM api_token_scopes WHERE capability = 'stack.down'`,
	)(ctx, tx)
}
