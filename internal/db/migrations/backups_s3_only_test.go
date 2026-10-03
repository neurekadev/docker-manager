package migrations

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Local backup repositories go with their part of the index and the rules
// on them; a policy writing to one moves to the oldest S3 repository,
// disabled, or is deleted when there is none.
func TestLocalBackupRepositoriesGo(t *testing.T) {
	const at = `'2026-10-01 00:00:00.000+00:00'`
	repo := func(id, kind, created string) string {
		executor, path, bucket := "", "", ""
		if kind == "local" {
			executor, path = "env-a", "/backups/"+id
		} else {
			bucket = "backups"
		}
		return `INSERT INTO backup_repositories (id, name, name_key, kind, executor, path, endpoint, bucket, state, verify_cron,
			verify_time_zone, revision, created_at, updated_at) VALUES ('` + id + `', '` + id + `', '` + id + `', '` + kind + `', '` +
			executor + `', '` + path + `', '', '` + bucket + `', 'ready', '0 5 * * 0', 'UTC', 1, '` + created + `', ` + at + `)`
	}
	policy := func(id, env, repo, envRepos string) string {
		return `INSERT INTO backup_policies (id, name, name_key, environment_id, repository_id, environment_repos, cron, time_zone, enabled,
			revision, created_at, updated_at) VALUES ('` + id + `', '` + id + `', '` + id + `', '` + env + `', '` + repo + `', '` + envRepos +
			`', '0 2 * * *', 'UTC', 1, 2, ` + at + `, ` + at + `)`
	}
	snapshot := func(id, repo string) string {
		return `INSERT INTO backup_snapshots (id, repository_id, scope, kind, item, restic_snapshot_id, snapshot_time, state, created_at)
			VALUES ('` + id + `', '` + repo + `', 'env:env-a', 'volume', 'data', 'r-` + id + `', ` + at + `, 'complete', ` + at + `)`
	}
	set := func(id string, repos ...string) string {
		members := "["
		for i, r := range repos {
			if i > 0 {
				members += ","
			}
			members += `{"RepositoryID":"` + r + `"}`
		}
		return `INSERT INTO backup_sets (id, origin, state, started_at, members, updated_at)
			VALUES ('` + id + `', 'schedule', 'complete', ` + at + `, '` + members + "]', " + at + `)`
	}
	rule := func(typ, id string) string {
		return `INSERT INTO group_permission_rules (group_id, capability, scope_kind, environment_id, resource_type, resource_id, effect, position)
			VALUES ('g1', '` + typ + `.read', 'resource', '', '` + typ + `', '` + id + `', 'allow', 0)`
	}
	common := []string{
		`INSERT INTO groups (id, name, position, created_at, updated_at) VALUES ('g1', 'Ops', 0, '2026-10-01', '2026-10-01')`,
		repo("disk", "local", "2026-01-01"),
		snapshot("sn-disk", "disk"),
		set("set-disk", "disk"),
		`INSERT INTO backup_locations (repository_id, scope, updated_at) VALUES ('disk', 'env:env-a', ` + at + `)`,
		`INSERT INTO backup_storage_samples (repository_id, scope, hour, at, size_bytes, uncompressed_bytes)
			VALUES ('disk', 'env:env-a', 1, '2026-01-01 00:00:00.000+00:00', 100, 200)`,
		rule("backup_repository", "disk"),
		rule("backup", "sn-disk"),
	}

	t.Run("with an S3 repository", func(t *testing.T) {
		ctx := testutil.Context(t)
		db := backupsDB(t, ctx, append(common,
			repo("s3-new", "s3", "2026-03-01"),
			repo("s3-old", "s3", "2026-02-01"),
			snapshot("sn-s3", "s3-old"),
			set("set-mixed", "disk", "s3-old"),
			policy("p-disk", "", "disk", `{"env-b":"disk","env-c":"s3-new"}`),
			policy("p-s3", "env-a", "s3-new", `{}`),
			rule("backup_policy", "p-disk"),
			rule("backup_repository", "s3-old"),
		))
		var repoID, envRepos string
		var enabled, revision int
		if err := db.QueryRowContext(ctx, `SELECT repository_id, environment_repos, enabled, revision FROM backup_policies WHERE id = 'p-disk'`).
			Scan(&repoID, &envRepos, &enabled, &revision); err != nil {
			t.Fatal(err)
		}
		if repoID != "s3-old" || envRepos != `{"env-c":"s3-new"}` || enabled != 0 || revision != 3 {
			t.Fatalf("moved policy: repository %s, environments %s, enabled %d, revision %d", repoID, envRepos, enabled, revision)
		}
		if err := db.QueryRowContext(ctx, `SELECT enabled FROM backup_policies WHERE id = 'p-s3'`).Scan(&enabled); err != nil || enabled != 1 {
			t.Fatalf("the S3 policy changed: enabled %d %v", enabled, err)
		}
		expect(t, ctx, db, `SELECT id FROM backup_repositories ORDER BY id`, "s3-new", "s3-old")
		expect(t, ctx, db, `SELECT id FROM backup_snapshots ORDER BY id`, "sn-s3")
		expect(t, ctx, db, `SELECT id FROM backup_sets ORDER BY id`, "set-mixed")
		expect(t, ctx, db, `SELECT resource_id FROM group_permission_rules ORDER BY resource_id`, "p-disk", "s3-old")
		expect(t, ctx, db, `SELECT repository_id FROM backup_locations`)
		expect(t, ctx, db, `SELECT size_bytes FROM backup_storage_samples ORDER BY hour`, "100", "0")
	})

	t.Run("without one", func(t *testing.T) {
		ctx := testutil.Context(t)
		db := backupsDB(t, ctx, append(common, policy("p-disk", "", "disk", `{}`), rule("backup_policy", "p-disk")))
		expect(t, ctx, db, `SELECT id FROM backup_policies`)
		expect(t, ctx, db, `SELECT id FROM backup_repositories`)
		expect(t, ctx, db, `SELECT resource_id FROM group_permission_rules`)
		expect(t, ctx, db, `SELECT id FROM backup_sets`)
	})
}

// backupsDB is a database just before the S3-only migration with stmts
// applied, migrated to the latest version.
func backupsDB(t *testing.T, ctx context.Context, stmts []string) *bun.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrateTo(t, db, "20261002190000", dir)
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	migrateTo(t, db, "", dir)
	return db
}

// expect requires query to return want (one text column), in order.
func expect(t *testing.T, ctx context.Context, db *bun.DB, query string, want ...string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := []string{}
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
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", query, got, want)
	}
}
