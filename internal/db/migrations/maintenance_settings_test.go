package migrations

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The maintenance policies become one setup: an all-environments policy as
// it is, else the only policy with every other environment left out, else
// a disabled setup with the default rules. Permission rules that no longer
// mean anything go.
func TestMaintenancePoliciesBecomeOneSetup(t *testing.T) {
	const at = `'2026-10-01 00:00:00.000+00:00'`
	policy := func(id, env string, enabled int) string {
		return `INSERT INTO maintenance_policies (id, environment_id, name, name_key, cron, time_zone, schedule_enabled, rules, last_run,
			revision, created_at, updated_at) VALUES ('` + id + `', '` + env + `', '` + id + `', '` + id + `', '0 4 * * *', 'Europe/Berlin', ` +
			strconv.Itoa(enabled) + `, '[{"category":"build_cache","enabled":true,"minAgeSeconds":3600}]', '', 3, ` + at + `, ` + at + `)`
	}
	for _, c := range []struct {
		name     string
		setup    []string
		id       string
		enabled  bool
		cron     string
		rules    string
		excluded []string
	}{
		{name: "all environments", setup: []string{policy("mp-all", "", 1), policy("mp-a", "env-a", 0)},
			id: "mp-all", enabled: true, cron: "0 4 * * *", rules: "build_cache", excluded: []string{}},
		{name: "the only policy", setup: []string{policy("mp-a", "env-a", 1)},
			id: "mp-a", enabled: true, cron: "0 4 * * *", rules: "build_cache", excluded: []string{"env-b", "env-c"}},
		{name: "several policies", setup: []string{policy("mp-a", "env-a", 1), policy("mp-b", "env-b", 1),
			`UPDATE maintenance_defaults SET rules = '[{"category":"unused_images","enabled":true,"minAgeSeconds":7200}]'`},
			cron: "0 3 * * 0", rules: "unused_images", excluded: []string{}},
		{name: "none", cron: "0 3 * * 0", excluded: []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := testutil.Context(t)
			dir := t.TempDir()
			db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			migrateTo(t, db, "20261002170000", dir)
			stmts := []string{}
			for _, env := range []string{"env-a", "env-b", "env-c"} {
				stmts = append(stmts, `INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at)
					VALUES ('`+env+`', '`+env+`', 'E-`+env+`', 'I-`+env+`', 'active', 1, `+at+`, `+at+`)`)
			}
			stmts = append(stmts, c.setup...)
			for _, s := range stmts {
				if _, err := db.ExecContext(ctx, s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			migrateTo(t, db, "", dir)
			var id, cron, tz, rules, excluded string
			var enabled int
			if err := db.QueryRowContext(ctx, `SELECT id, enabled, cron, time_zone, rules, exclude_environments FROM maintenance_settings`).
				Scan(&id, &enabled, &cron, &tz, &rules, &excluded); err != nil {
				t.Fatal(err)
			}
			var ex []string
			if err := json.Unmarshal([]byte(excluded), &ex); err != nil {
				t.Fatal(err)
			}
			slices.Sort(ex)
			if (c.id != "" && id != c.id) || id == "" || (enabled == 1) != c.enabled || cron != c.cron || !slices.Equal(ex, c.excluded) {
				t.Fatalf("setup %s enabled %d cron %s excluded %v", id, enabled, cron, ex)
			}
			if c.rules == "" && rules != "" || c.rules != "" && !json.Valid([]byte(rules)) {
				t.Fatalf("rules %q", rules)
			}
			if c.rules != "" {
				var rs []struct{ Category string }
				_ = json.Unmarshal([]byte(rules), &rs)
				if len(rs) != 1 || rs[0].Category != c.rules {
					t.Fatalf("rules %s, want %s", rules, c.rules)
				}
			}
			var tables int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name IN ('maintenance_policies', 'maintenance_defaults')`).
				Scan(&tables); err != nil || tables != 0 {
				t.Fatalf("old tables: %d %v", tables, err)
			}
		})
	}
}

func TestMaintenancePermissionRulesWithoutMeaningGo(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261002170000", dir)
	rule := func(capability, kind, env, typ, id string) string {
		return `INSERT INTO group_permission_rules (group_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES ('g1', '` + capability + `', '` + kind + `', '` + env + `', '` + typ + `', '` + id + `', 'allow', 0)`
	}
	for _, s := range []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES ('g1', 'Ops', 0, '2026-10-01', '2026-10-01')`,
		rule("maintenance_policy.read", "instance", "", "", ""),
		rule("maintenance_policy.manage", "environment", "env-a", "", ""),
		rule("maintenance.run", "environment", "env-a", "", ""),
		rule("maintenance.preview", "resource", "", "maintenance_policy", "mp-a"),
		rule("stack.read", "environment", "env-a", "", ""),
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
	if want := []string{"maintenance.run environment", "maintenance_policy.read instance", "stack.read environment"}; !slices.Equal(got, want) {
		t.Fatalf("rules %v, want %v", got, want)
	}
	var rev int
	if err := db.QueryRowContext(ctx, `SELECT permissions_revision FROM groups WHERE id = 'g1'`).Scan(&rev); err != nil || rev != 2 {
		t.Fatalf("permissions revision %d %v", rev, err)
	}
}
