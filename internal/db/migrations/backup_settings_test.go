package migrations

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

// The backup policies become one setup (#246): an all-environments policy
// as it is, else the only policy with every other environment left out
// (its volume exclusions keyed by environment, its repository there the
// Primary), else a disabled setup whose Primary is the oldest repository.
// Permission rules that no longer mean anything go.
func TestBackupPoliciesBecomeOneSetup(t *testing.T) {
	const at = `'2026-10-01 00:00:00.000+00:00'`
	repo := func(id, created string) string {
		return `INSERT INTO backup_repositories (id, name, name_key, kind, endpoint, bucket, state, verify_cron, verify_time_zone, revision,
			created_at, updated_at) VALUES ('` + id + `', '` + id + `', '` + id + `', 's3', 'https://s3.example.com', 'b-` + id +
			`', 'ready', '0 5 * * 0', 'UTC', 1, '` + created + `', ` + at + `)`
	}
	policy := func(id, env, repo, envRepos, excludeVolumes string, enabled int) string {
		return `INSERT INTO backup_policies (id, name, name_key, environment_id, repository_id, environment_repos, exclude_volumes, cron,
			time_zone, enabled, retention, revision, created_at, updated_at) VALUES ('` + id + `', '` + id + `', '` + id + `', '` + env +
			`', '` + repo + `', '` + envRepos + `', '` + excludeVolumes + `', '0 4 * * *', 'Europe/Berlin', ` + strconv.Itoa(enabled) +
			`, '{"Daily":7}', 3, ` + at + `, ` + at + `)`
	}
	repos := []string{repo("r-new", "2026-03-01"), repo("r-old", "2026-02-01"), repo("r-env", "2026-04-01")}
	for _, c := range []struct {
		name           string
		setup          []string
		id             string
		enabled        bool
		primary        string
		cron           string
		excluded       []string
		excludeVolumes []string
	}{
		{name: "all environments", setup: []string{policy("bp-all", "", "r-new", `{"env-b":"r-env"}`, `["env-a/cache"]`, 1),
			policy("bp-a", "env-a", "r-old", `{}`, `[]`, 0)},
			id: "bp-all", enabled: true, primary: "r-new", cron: "0 4 * * *", excluded: []string{}, excludeVolumes: []string{"env-a/cache"}},
		{name: "the only policy", setup: []string{policy("bp-a", "env-a", "r-new", `{"env-a":"r-env"}`, `["cache","tmp"]`, 1)},
			id: "bp-a", enabled: true, primary: "r-env", cron: "0 4 * * *", excluded: []string{"env-b", "env-c"},
			excludeVolumes: []string{"env-a/cache", "env-a/tmp"}},
		{name: "several policies", setup: []string{policy("bp-a", "env-a", "r-new", `{}`, `[]`, 1), policy("bp-b", "env-b", "r-new", `{}`, `[]`, 1)},
			primary: "r-old", cron: "0 * * * *", excluded: []string{}, excludeVolumes: []string{}},
		{name: "none", primary: "r-old", cron: "0 * * * *", excluded: []string{}, excludeVolumes: []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := testutil.Context(t)
			stmts := []string{}
			for _, env := range []string{"env-a", "env-b", "env-c"} {
				stmts = append(stmts, `INSERT INTO environments (id, name, engine_id, install_id, status, revision, created_at, updated_at)
					VALUES ('`+env+`', '`+env+`', 'E-`+env+`', 'I-`+env+`', 'active', 1, `+at+`, `+at+`)`)
			}
			stmts = append(append(stmts, repos...), c.setup...)
			db := migratedFrom(t, ctx, "20261002200000", "", stmts)
			var id, primary, secondary, cron, excluded, excludeVolumes, retention string
			var enabled int
			if err := db.QueryRowContext(ctx, `SELECT id, enabled, primary_repository_id, secondary_repository_id, cron, exclude_environments,
				exclude_volumes, retention FROM backup_settings`).Scan(&id, &enabled, &primary, &secondary, &cron, &excluded, &excludeVolumes,
				&retention); err != nil {
				t.Fatal(err)
			}
			var ex, vols []string
			if err := json.Unmarshal([]byte(excluded), &ex); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(excludeVolumes), &vols); err != nil {
				t.Fatal(err)
			}
			slices.Sort(ex)
			slices.Sort(vols)
			if (c.id != "" && id != c.id) || id == "" || (enabled == 1) != c.enabled || primary != c.primary || secondary != "" ||
				cron != c.cron || !slices.Equal(ex, c.excluded) || !slices.Equal(vols, c.excludeVolumes) {
				t.Fatalf("setup %s enabled %d primary %s secondary %q cron %s excluded %v volumes %v", id, enabled, primary, secondary, cron, ex, vols)
			}
			if c.id != "" && retention != `{"Daily":7}` {
				t.Errorf("retention %s", retention)
			}
			expect(t, ctx, db, `SELECT name FROM sqlite_master WHERE name = 'backup_policies'`)
		})
	}
}

// Rules on one environment or one backup policy go; instance rules stay.
func TestBackupPolicyRulesBecomeInstanceOnly(t *testing.T) {
	ctx := testutil.Context(t)
	rule := func(capability, kind, env, typ, id string) string {
		return `INSERT INTO group_permission_rules (group_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES ('g1', '` + capability + `', '` + kind + `', '` + env + `', '` + typ + `', '` + id + `', 'allow', 0)`
	}
	db := migratedFrom(t, ctx, "20261002200000", "", []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES ('g1', 'Ops', 0, '2026-10-01', '2026-10-01')`,
		rule("backup_policy.read", "instance", "", "", ""),
		rule("backup_policy.manage", "resource", "", "backup_policy", "bp-1"),
		rule("backup.run", "resource", "", "backup_policy", "bp-1"),
		rule("backup.run", "resource", "", "stack", "st-1"),
	})
	expect(t, ctx, db, `SELECT capability || ' ' || resource_type FROM group_permission_rules ORDER BY 1`,
		"backup.run stack", "backup_policy.read ")
}
