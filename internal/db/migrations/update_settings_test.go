package migrations

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The environment update policies become one setup: an all-environments
// policy as it is, else the only policy with every other environment left
// out (its container exclusions gain their environment), else a setup with
// both schedules off. Every target record belongs to the setup afterwards;
// update_policy.manage rules below the instance go.
func TestUpdatePoliciesBecomeOneSetup(t *testing.T) {
	const at = `'2026-10-01 00:00:00.000+00:00'`
	policy := func(id, env, containers string, enabled string) string {
		return `INSERT INTO environment_update_policies (id, environment_id, name, exclude_stacks, exclude_containers, check_cron,
			check_time_zone, check_enabled, run_cron, run_time_zone, run_enabled, run_window, wait_timeout_seconds, revision, created_at,
			updated_at) VALUES ('` + id + `', '` + env + `', '` + id + `', '["st-9"]', '` + containers + `', '0 6 * * *', 'Europe/Berlin', ` +
			enabled + `, '0 7 * * *', 'Europe/Berlin', 0, '', 120, 2, ` + at + `, ` + at + `)`
	}
	record := `INSERT INTO update_policies (id, parent_id, environment_id, name, name_key, target_type, target_id, services, exclude_services,
		check_cron, check_time_zone, check_enabled, run_cron, run_time_zone, run_enabled, wait_timeout_seconds, revision, created_at, updated_at)
		VALUES ('rec-1', 'up-b', 'env-a', 'Automatic updates for shop', 'automatic updates for shop', 'stack', 'st-1', '[]', '[]',
		'0 6 * * *', 'UTC', 0, '0 7 * * *', 'UTC', 0, 0, 1, ` + at + `, ` + at + `)`
	for _, c := range []struct {
		name       string
		setup      []string
		id         string
		checkOn    bool
		cron       string
		excluded   []string
		containers []string
	}{
		{name: "all environments", setup: []string{policy("up-all", "", `["env-b/api"]`, "1"), policy("up-a", "env-a", `[]`, "0")},
			id: "up-all", checkOn: true, cron: "0 6 * * *", excluded: []string{}, containers: []string{"env-b/api"}},
		{name: "the only policy", setup: []string{policy("up-a", "env-a", `["api","db"]`, "1")},
			id: "up-a", checkOn: true, cron: "0 6 * * *", excluded: []string{"env-b", "env-c"}, containers: []string{"env-a/api", "env-a/db"}},
		{name: "several policies", setup: []string{policy("up-a", "env-a", `[]`, "1"), policy("up-b", "env-b", `[]`, "1")},
			cron: "0 3 * * *", excluded: []string{}, containers: []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := testutil.Context(t)
			dir := t.TempDir()
			db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			migrateTo(t, db, "20261002180000", dir)
			var stmts []string
			for _, env := range []string{"env-a", "env-b", "env-c"} {
				stmts = append(stmts, `INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at)
					VALUES ('`+env+`', '`+env+`', 'E-`+env+`', 'I-`+env+`', 'active', 1, `+at+`, `+at+`)`)
			}
			stmts = append(stmts, c.setup...)
			stmts = append(stmts, record)
			for _, s := range stmts {
				if _, err := db.ExecContext(ctx, s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			migrateTo(t, db, "", dir)
			var id, cron, excluded, containers string
			var checkOn int
			if err := db.QueryRowContext(ctx, `SELECT id, check_enabled, check_cron, exclude_environments, exclude_containers FROM update_settings`).
				Scan(&id, &checkOn, &cron, &excluded, &containers); err != nil {
				t.Fatal(err)
			}
			var ex, cs []string
			if err := json.Unmarshal([]byte(excluded), &ex); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(containers), &cs); err != nil {
				t.Fatal(err)
			}
			slices.Sort(ex)
			slices.Sort(cs)
			if (c.id != "" && id != c.id) || id == "" || (checkOn == 1) != c.checkOn || cron != c.cron || !slices.Equal(ex, c.excluded) ||
				!slices.Equal(cs, c.containers) {
				t.Fatalf("setup %s check %d cron %s excluded %v containers %v", id, checkOn, cron, ex, cs)
			}
			var parent string
			if err := db.QueryRowContext(ctx, `SELECT parent_id FROM update_policies WHERE id = 'rec-1'`).Scan(&parent); err != nil || parent != id {
				t.Fatalf("record parent %q %v, want %s", parent, err, id)
			}
			var tables int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name = 'environment_update_policies'`).
				Scan(&tables); err != nil || tables != 0 {
				t.Fatalf("old table: %d %v", tables, err)
			}
		})
	}
}

func TestUpdateManageRulesBelowTheInstanceGo(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261002180000", dir)
	rule := func(capability, kind, env, typ, id string) string {
		return `INSERT INTO group_permission_rules (group_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES ('g1', '` + capability + `', '` + kind + `', '` + env + `', '` + typ + `', '` + id + `', 'allow', 0)`
	}
	for _, s := range []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES ('g1', 'Ops', 0, '2026-10-01', '2026-10-01')`,
		rule("update_policy.manage", "instance", "", "", ""),
		rule("update_policy.manage", "environment", "env-a", "", ""),
		rule("update_policy.read", "environment", "env-a", "", ""),
		rule("update.run", "resource", "", "stack", "st-1"),
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	migrateTo(t, db, "", dir)
	rows, err := db.QueryContext(ctx, `SELECT capability || ' ' || scope_kind FROM group_permission_rules ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"update.run resource", "update_policy.manage instance", "update_policy.read environment"}; !slices.Equal(got, want) {
		t.Fatalf("rules %v, want %v", got, want)
	}
}
