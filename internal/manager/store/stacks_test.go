package store

import (
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// insertStackRow stores an undeployed stack of environment env-1 with raw
// SQL, as a schema before the newest migrations has it (the model names
// columns they add).
func insertStackRow(t *testing.T, db *bun.DB, id, services string) {
	t.Helper()
	if _, err := db.ExecContext(testutil.Context(t), `INSERT INTO stacks (id, environment_id, name, root, dir, origin, status, services,
		revision, created_at, updated_at) VALUES (?, 'env-1', ?, 'stacks', ?, 'created', 'undeployed', ?, 3, ?, ?)`,
		id, id, id, services, testutil.Epoch, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
}

// migrateBefore applies the migrations older than name.
func migrateBefore(t *testing.T, db *bun.DB, dir, name string) {
	t.Helper()
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < name {
			before.Add(m)
		}
	}
	if _, err := Migrate(testutil.Context(t), db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationClearsStackIcons: stacks and services no longer have an
// icon of their own. The upgrade clears the stored stack icons, drops the
// icon from the per-service metadata (removing entries left without any)
// and keeps descriptions and revisions.
func TestMigrationClearsStackIcons(t *testing.T) {
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	migrateBefore(t, db, dir, "20260928192810")
	env := domain.Environment{ID: "env-1", Name: "NAS", EngineID: "ENGINE-1", InstallID: "i-1", Status: domain.EnvironmentActive,
		Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	if err := InsertEnvironment(ctx, db, &env); err != nil {
		t.Fatal(err)
	}
	stored := map[string]struct{ icon, meta string }{
		"with-icons": {"globe", `{"web":{"description":"Front","icon":"globe"},"db":{"icon":"database"}}`},
		"no-icons":   {"", `{"web":{"description":"Front"}}`},
		"empty":      {"cog", `{}`},
	}
	for id, s := range stored {
		insertStackRow(t, db, id, "[]")
		if _, err := db.ExecContext(ctx, "UPDATE stacks SET icon = ?, service_meta = ? WHERE id = ?", s.icon, s.meta, id); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	var icons int
	if err := db.NewRaw("SELECT COUNT(*) FROM stacks WHERE icon != ''").Scan(ctx, &icons); err != nil || icons != 0 {
		t.Errorf("%d stacks keep an icon (%v)", icons, err)
	}
	want := map[string]map[string]domain.DisplayMeta{
		"with-icons": {"web": {Description: "Front"}},
		"no-icons":   {"web": {Description: "Front"}},
		"empty":      {},
	}
	for id, meta := range want {
		st, err := GetStack(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.ServiceMeta) != len(meta) || st.Revision != 3 {
			t.Errorf("%s: service metadata %+v revision %d", id, st.ServiceMeta, st.Revision)
		}
		for name, m := range meta {
			if st.ServiceMeta[name] != m {
				t.Errorf("%s/%s: %+v, want %+v", id, name, st.ServiceMeta[name], m)
			}
		}
		var raw string
		if err := db.NewRaw("SELECT service_meta FROM stacks WHERE id = ?", id).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "icon") {
			t.Errorf("%s still stores a service icon: %s", id, raw)
		}
	}
}

// TestMigrationDerivesSourceBuild: existing stacks start with whether
// their services have a build section and no validated hash (their next
// read validates them), and both round-trip.
func TestMigrationDerivesSourceBuild(t *testing.T) {
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	migrateBefore(t, db, dir, "20261004231043")
	env := domain.Environment{ID: "env-1", Name: "NAS", EngineID: "ENGINE-1", InstallID: "i-1", Status: domain.EnvironmentActive,
		Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	if err := InsertEnvironment(ctx, db, &env); err != nil {
		t.Fatal(err)
	}
	insertStackRow(t, db, "built", `[{"name":"db","image":"postgres:16"},{"name":"web","image":"shop-web","build":true}]`)
	insertStackRow(t, db, "plain", `[{"name":"db","image":"postgres:16"}]`)

	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	built, err := GetStack(ctx, db, "built")
	if err != nil || !built.SourceBuild || built.SourceBuildHash != "" {
		t.Errorf("built: %+v %v", built, err)
	}
	plain, err := GetStack(ctx, db, "plain")
	if err != nil || plain.SourceBuild {
		t.Fatalf("plain: %+v %v", plain, err)
	}
	plain.SourceBuild, plain.SourceBuildHash = true, "sha256:def"
	if err := UpdateStack(ctx, db, &plain); err != nil {
		t.Fatal(err)
	}
	if got, err := GetStack(ctx, db, "plain"); err != nil || !got.SourceBuild || got.SourceBuildHash != "sha256:def" {
		t.Errorf("after update: %+v %v", got, err)
	}
}
