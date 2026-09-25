//go:build linux

package migration_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/migration"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/transfer"
)

type osEntry struct {
	mode         fs.FileMode
	uid, gid     uint32
	mtime, atime time.Time
	target       string
	content      string
	ino          uint64
}

func osTree(t *testing.T, root string) map[string]osEntry {
	t.Helper()
	out := map[string]osEntry{}
	first := map[uint64]uint64{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := fi.Sys().(*syscall.Stat_t)
		e := osEntry{mode: fi.Mode(), uid: st.Uid, gid: st.Gid, mtime: fi.ModTime(), atime: time.Unix(st.Atim.Sec, st.Atim.Nsec)}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			e.target, _ = os.Readlink(p)
			e.mtime, e.atime = time.Time{}, time.Time{}
			e.mode = fs.ModeSymlink
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			e.content = string(b)
			if f, ok := first[st.Ino]; ok {
				e.ino = f
			} else {
				first[st.Ino] = uint64(len(first) + 1)
				e.ino = first[st.Ino]
			}
		}
		// Reading files and directories updates access times (relatime),
		// on the source too: access times are compared by the in-memory
		// variant only.
		e.atime = time.Time{}
		out[rel] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOSRoundTrip (Linux): the same round trip on a real filesystem
// through os.Root: modes including setgid/sticky, nanosecond modification times,
// symlinks (never followed), hard links and FIFOs; numeric owners only when
// the test runs as root (as the agent does). Needs Linux: Windows cannot
// represent owners, FIFOs or special bits (the in-memory variant covers
// them there).
func TestOSRoundTrip(t *testing.T) {
	ctx := testutil.Context(t)
	base := t.TempDir()
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "data", "empty"), 0o755))
	must(os.WriteFile(filepath.Join(src, "compose.yaml"), []byte("services: {}\n"), 0o644))
	must(os.WriteFile(filepath.Join(src, "data", "a"), bytes.Repeat([]byte("x"), 300000), 0o600))
	must(os.Link(filepath.Join(src, "data", "a"), filepath.Join(src, "data", "b")))
	must(os.Symlink("../compose.yaml", filepath.Join(src, "data", "rel")))
	must(os.Symlink("/etc/passwd", filepath.Join(src, "abs")))
	must(syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600))
	must(os.Chmod(filepath.Join(src, "data"), 0o2775|fs.ModeSetgid))
	must(os.Chmod(filepath.Join(src, "data", "empty"), 0o1777|fs.ModeSticky))
	asRoot := os.Geteuid() == 0
	if asRoot {
		must(os.Lchown(filepath.Join(src, "data", "a"), 1000, 1001))
		must(os.Lchown(filepath.Join(src, "data", "rel"), 1002, 1003))
		must(os.Chown(src, 999, 999))
	}
	stamp := time.Date(2025, 5, 6, 7, 8, 9, 123456789, time.UTC)
	for _, p := range []string{"compose.yaml", "data/a", "data/empty", "data", "pipe", "."} {
		must(os.Chtimes(filepath.Join(src, p), stamp.Add(time.Hour), stamp))
	}
	sfs, err := migration.OSOpener(src)
	must(err)
	defer func() { _ = sfs.Close() }()
	var framed bytes.Buffer
	fw := transfer.NewWriter(&framed, 4096)
	if _, err := migration.WriteTree(ctx, sfs, fw); err != nil {
		t.Fatal(err)
	}
	must(fw.Close())
	must(os.Mkdir(dst, 0o700))
	dfs, err := migration.OSOpener(dst)
	must(err)
	defer func() { _ = dfs.Close() }()
	if _, err := migration.ExtractTree(ctx, dfs, transfer.NewReader(&framed), migration.ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	want, got := osTree(t, src), osTree(t, dst)
	if !asRoot {
		t.Log("not root: numeric owners are not compared")
		for k, e := range want {
			e.uid, e.gid = 0, 0
			want[k] = e
		}
		for k, e := range got {
			e.uid, e.gid = 0, 0
			got[k] = e
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for k, w := range want {
		if g := got[k]; g != w {
			t.Errorf("%s:\n got %+v\nwant %+v", k, g, w)
		}
	}
}
