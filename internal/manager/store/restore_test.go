package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestRestoreSnapshotRefusals: unknown names, paths and corrupt snapshots
// are refused and leave the current database untouched.
func TestRestoreSnapshotRefusals(t *testing.T) {
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	inst, err := CreateInstance(ctx, db, testutil.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dbPath, snapDir := filepath.Join(dir, "docker-manager.db"), filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := "docker-manager-20260101T000000Z-00-pre-x.db"
	if err := os.WriteFile(filepath.Join(snapDir, corrupt), []byte("not a database, just bytes that are long enough to look like a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing.db", "../docker-manager.db", filepath.Join(snapDir, corrupt), corrupt} {
		_, err := RestoreSnapshot(ctx, dbPath, snapDir, name, testutil.Epoch)
		if err == nil {
			t.Fatalf("%s restored", name)
		}
		if name != corrupt && !errors.Is(err, ErrSnapshotNotFound) {
			t.Errorf("%s: %v, want ErrSnapshotNotFound", name, err)
		}
	}
	if _, err := os.Stat(dbPath + ".restoring"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary copy left behind: %v", err)
	}
	again, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if got, ok, err := GetInstance(ctx, again); err != nil || !ok || got.ID != inst.ID {
		t.Fatalf("database changed by a refused restore: %+v %v %v", got, ok, err)
	}
}
