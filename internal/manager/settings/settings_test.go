package settings

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{"Homelab": "Homelab", "  Lab 2 ": "Lab 2", "Ünïcødé ⚓": "Ünïcødé ⚓", strings.Repeat("é", 64): strings.Repeat("é", 64)} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "a\nb", "tab\there", "\x7f", strings.Repeat("x", 65), "bad\xffutf8"} {
		if _, err := NormalizeName(in); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestUpdateIsRevisioned(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	svc := New(db, clock.NewFake(at))
	s, err := svc.Get(ctx)
	if err != nil || s.Name != "Docker Manager" || s.Revision != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	name := " Homelab "
	before, after, err := svc.Update(ctx, 1, domain.InstanceSettingsPatch{Name: &name})
	if err != nil || before.Name != "Docker Manager" || after.Name != "Homelab" || after.Revision != 2 || !after.UpdatedAt.Equal(at) {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	if _, _, err := svc.Update(ctx, 1, domain.InstanceSettingsPatch{Name: &name}); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	bad := "\x00"
	if _, _, err := svc.Update(ctx, 2, domain.InstanceSettingsPatch{Name: &bad}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad name: %v", err)
	}
	if s, _ := svc.Get(ctx); s.Name != "Homelab" || s.Revision != 2 {
		t.Fatalf("%+v", s)
	}
}
