package store

import (
	"testing"

	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestLinksMigrationAndRoundTrip: templates stored before links existed
// read back without links; links survive an update and a registry sync in
// their order.
func TestLinksMigrationAndRoundTrip(t *testing.T) {
	const links = "20260928180542"
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < links {
			before.Add(m)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO templates (id, name, name_key, description, tags, visibility, revision, created_at, updated_at)
		VALUES ('t-old', 'Old', 'old', '', '[]', 'private', 1, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}

	tm, err := GetTemplate(ctx, db, "t-old")
	if err != nil {
		t.Fatal(err)
	}
	if tm.Links == nil || len(tm.Links) != 0 {
		t.Fatalf("links of an old template %#v", tm.Links)
	}
	tm.Links = []domain.Link{{Label: "Docs", URL: "https://docs.example.com"}, {URL: "https://git.example.com/app"}}
	tm.Revision = 2
	if err := UpdateTemplate(ctx, db, &tm, 1); err != nil {
		t.Fatal(err)
	}
	got, err := GetTemplate(ctx, db, "t-old")
	if err != nil || len(got.Links) != 2 || got.Links[0] != tm.Links[0] || got.Links[1] != tm.Links[1] {
		t.Fatalf("round trip %+v, %v", got.Links, err)
	}

	reg := domain.TemplateRegistry{InstanceID: "remote-1", URL: "https://friend.example", Name: "Friend", Status: domain.TemplateRegistryOK,
		CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	if err := UpsertTemplateRegistry(ctx, db, &reg); err != nil {
		t.Fatal(err)
	}
	entry := domain.RegistryTemplate{TemplateID: "rt-1", Name: "Cloud", Links: []domain.Link{{Label: "Guide", URL: "https://friend.example/guide"}},
		Versions: []domain.RegistryTemplateVersion{{Number: 1, Label: "1.0.0"}}, UpdatedAt: testutil.Epoch}
	if err := ReplaceRegistryTemplates(ctx, db, "remote-1", []domain.RegistryTemplate{entry}); err != nil {
		t.Fatal(err)
	}
	rt, err := GetRegistryTemplate(ctx, db, "remote-1", "rt-1")
	if err != nil || len(rt.Links) != 1 || rt.Links[0] != entry.Links[0] {
		t.Fatalf("registry template links %+v, %v", rt.Links, err)
	}
	// The next sync replaces them.
	entry.Links = nil
	if err := ReplaceRegistryTemplates(ctx, db, "remote-1", []domain.RegistryTemplate{entry}); err != nil {
		t.Fatal(err)
	}
	if rt, err = GetRegistryTemplate(ctx, db, "remote-1", "rt-1"); err != nil || len(rt.Links) != 0 {
		t.Fatalf("links after a sync without them %+v, %v", rt.Links, err)
	}
}
