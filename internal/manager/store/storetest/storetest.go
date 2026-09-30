// Package storetest opens migrated manager databases for tests. Import it
// only from _test.go files.
package storetest

import (
	"path/filepath"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Migrated opens a fresh database in a temporary directory with every
// migration applied. It is closed when the test ends.
func Migrated(t testing.TB) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{
		Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snapshots"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t),
	}); err != nil {
		t.Fatal(err)
	}
	return db
}
