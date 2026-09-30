//go:build linux

package migration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/migration"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestCopyTreeKeepsTheTreeAndVerifies(t *testing.T) {
	ctx := testutil.Context(t)
	src, dst := t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(src, "compose.yaml"), []byte("services: {}\n"), 0o644))
	must(os.MkdirAll(filepath.Join(src, "pgdata", "base"), 0o700))
	must(os.WriteFile(filepath.Join(src, "pgdata", "base", "1"), []byte("rows"), 0o600))
	must(os.Chmod(filepath.Join(src, "pgdata", "base", "1"), 0o600|os.ModeSetgid))
	must(os.Symlink("pgdata/base/1", filepath.Join(src, "latest")))
	must(os.Link(filepath.Join(src, "compose.yaml"), filepath.Join(src, "compose.link")))
	old := time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC)
	must(os.Chtimes(filepath.Join(src, "pgdata", "base", "1"), old, old))
	must(os.Mkdir(filepath.Join(dst, "copy"), 0o700))

	sfs, err := migration.OSOpener(filepath.ToSlash(src))
	must(err)
	defer func() { _ = sfs.Close() }()
	root, err := migration.OSOpener(filepath.ToSlash(dst))
	must(err)
	defer func() { _ = root.Close() }()
	dfs, err := root.Sub("copy")
	must(err)
	defer func() { _ = dfs.Close() }()

	st, err := migration.CopyTree(ctx, sfs, dfs, 0)
	must(err)
	if st.Extracted.Bytes != st.Bytes || st.Bytes == 0 {
		t.Errorf("stats %+v", st)
	}
	must(migration.VerifyTree(ctx, sfs, dfs))
	fi, err := os.Lstat(filepath.Join(dst, "copy", "pgdata", "base", "1"))
	must(err)
	if fi.Mode()&os.ModeSetgid == 0 || fi.Mode().Perm() != 0o600 || !fi.ModTime().Equal(old) {
		t.Errorf("file metadata %v %v", fi.Mode(), fi.ModTime())
	}
	if target, err := os.Readlink(filepath.Join(dst, "copy", "latest")); err != nil || target != "pgdata/base/1" {
		t.Errorf("symlink %q %v", target, err)
	}

	// A difference is found.
	must(os.Chmod(filepath.Join(dst, "copy", "compose.yaml"), 0o600))
	if err := migration.VerifyTree(ctx, sfs, dfs); err == nil {
		t.Error("a changed mode was not detected")
	}
}

// TestSyncTreeFlushesOnlyTheTree: SyncTree flushes the files and
// directories of one tree (never a host-wide sync(2), which waits for every
// filesystem and cannot be cancelled), never opens a FIFO (that open would
// block) and stops when its context ends.
func TestSyncTreeFlushesOnlyTheTree(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "pgdata", "base"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "pgdata", "base", "1"), []byte("rows"), 0o600))
	must(os.Symlink("/nonexistent", filepath.Join(dir, "dangling")))
	must(os.Link(filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "compose.link")))
	must(syscall.Mkfifo(filepath.Join(dir, "pgdata", "fifo"), 0o600))

	must(migration.SyncTree(ctx, filepath.ToSlash(dir)))
	must(migration.SyncDir(filepath.ToSlash(dir)))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := migration.SyncTree(cancelled, filepath.ToSlash(dir)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled SyncTree: %v", err)
	}
	if err := migration.SyncTree(ctx, filepath.ToSlash(filepath.Join(dir, "missing"))); err == nil {
		t.Error("a missing tree was flushed")
	}
}
