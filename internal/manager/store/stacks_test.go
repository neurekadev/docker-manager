package store

import (
	"strings"
	"testing"

	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestMigrationClearsStackIcons: stacks and services no longer have an
// icon of their own. The upgrade clears the stored stack icons, drops the
// icon from the per-service metadata (removing entries left without any)
// and keeps descriptions and revisions.
func TestMigrationClearsStackIcons(t *testing.T) {
	const cleanup = "20260928192810"
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < cleanup {
			before.Add(m)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
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
		st := domain.Stack{ID: id, EnvironmentID: "env-1", Name: id, Root: domain.StackRootStacks, Dir: id, Origin: domain.StackOriginCreated,
			Status: domain.StackUndeployed, Revision: 3, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
		if err := InsertStack(ctx, db, &st); err != nil {
			t.Fatal(err)
		}
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
