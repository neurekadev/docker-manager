package restictest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/restic"
)

// TestPersistOutlivesTheProcess: a store backed by a file keeps
// repositories, keys and snapshots (with their files) for the next store
// opened on it, like a repository on disk (the #26 crash harness).
func TestPersistOutlivesTheProcess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "restic.json")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC) }
	a := New(now)
	if err := a.Persist(path); err != nil {
		t.Fatal(err)
	}
	repo := a.Open(restic.Location{Repository: "/backups/r"}, "pw")
	if _, err := repo.Init(ctx); err != nil {
		t.Fatal(err)
	}
	sum, err := repo.Backup(ctx, restic.BackupRequest{Paths: []string{src}, Tags: []string{"t1"}})
	if err != nil {
		t.Fatal(err)
	}

	b := New(now)
	if err := b.Persist(path); err != nil {
		t.Fatal(err)
	}
	snaps := b.Snapshots("/backups/r")
	if len(snaps) != 1 || snaps[0].ID != sum.SnapshotID || !snaps[0].HasTag("t1") {
		t.Fatalf("snapshots after reopening %+v", snaps)
	}
	if _, err := b.Open(restic.Location{Repository: "/backups/r"}, "wrong").Config(ctx); err == nil {
		t.Fatal("the key was not persisted (a wrong password opened the repository)")
	}
	var out strings.Builder
	if err := b.Open(restic.Location{Repository: "/backups/r"}, "pw").Dump(ctx, sum.SnapshotID, StoredPath(filepath.Join(src, "a.txt")), &out); err != nil ||
		out.String() != "hello" {
		t.Fatalf("file content after reopening %q %v", out.String(), err)
	}
}
