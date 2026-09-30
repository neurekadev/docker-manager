package migration_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/migration"
	"github.com/neurekadev/docker-manager/internal/agent/migration/migrationtest"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

var (
	t0 = time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	t1 = time.Date(2025, 1, 2, 3, 4, 5, 987654321, time.UTC)
)

// seedTree builds a source tree exercising everything a migration must
// preserve: numeric owners, permission and special bits, nanosecond times,
// symlinks (relative, absolute, escaping: copied as text, never followed),
// hard links, FIFOs, empty directories, a multi-chunk file, and the two
// kinds it must skip (a socket and a device node).
func seedTree(h *migrationtest.Host, root string) {
	h.MkdirAll(root)
	h.Put(root, migrationtest.Entry{Type: "dir", Mode: 0o750, UID: 999, GID: 998, MTime: t0, ATime: t1})
	h.Put(root+"/compose.yaml", migrationtest.Entry{Mode: 0o644, UID: 0, GID: 0, MTime: t1, Data: "services: {}\n"})
	h.Put(root+"/data", migrationtest.Entry{Type: "dir", Mode: 0o2775, UID: 1000, GID: 1001, MTime: t0})
	h.Put(root+"/data/db.sqlite", migrationtest.Entry{Mode: 0o600, UID: 1000, GID: 1001, MTime: t0, ATime: t1,
		Data: strings.Repeat("0123456789abcdef", 40000)})
	h.Put(root+"/data/hard-a", migrationtest.Entry{Mode: 0o640, UID: 5, GID: 6, MTime: t1, Data: "shared inode"})
	h.Link(root+"/data/hard-a", root+"/data/hard-b")
	h.Put(root+"/tmp", migrationtest.Entry{Type: "dir", Mode: 0o1777, MTime: t1})
	h.Put(root+"/empty", migrationtest.Entry{Type: "dir", Mode: 0o700, UID: 7, GID: 7, MTime: t0})
	h.Put(root+"/bin", migrationtest.Entry{Type: "dir", Mode: 0o755, MTime: t0})
	h.Put(root+"/bin/tool", migrationtest.Entry{Mode: 0o4755, MTime: t0, Data: "#!/bin/sh\n"})
	h.Put(root+"/rel-link", migrationtest.Entry{Type: "symlink", UID: 1000, GID: 1001, Target: "data/db.sqlite"})
	h.Put(root+"/abs-link", migrationtest.Entry{Type: "symlink", Target: "/etc/passwd"})
	h.Put(root+"/escape-link", migrationtest.Entry{Type: "symlink", Target: "../../outside"})
	h.Put(root+"/pipe", migrationtest.Entry{Type: "fifo", Mode: 0o620, UID: 3, GID: 4, MTime: t1})
	h.Put(root+"/sock", migrationtest.Entry{Type: "socket", Mode: 0o777})
	h.Put(root+"/dev", migrationtest.Entry{Type: "device", Mode: 0o660})
}

// roundTrip archives src, frames and checksums it like a migration stream
// (internal/transfer) and extracts it into dst.
func roundTrip(t *testing.T, h *migrationtest.Host, src, dst string) (migration.ArchiveStats, migration.ExtractStats) {
	t.Helper()
	ctx := testutil.Context(t)
	sfs, err := h.Opener()(src)
	if err != nil {
		t.Fatal(err)
	}
	var framed bytes.Buffer
	fw := transfer.NewWriter(&framed, 0)
	as, err := migration.WriteTree(ctx, sfs, fw)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	h.MkdirAll(dst)
	dfs, err := h.Opener()(dst)
	if err != nil {
		t.Fatal(err)
	}
	r := transfer.NewReader(&framed)
	es, err := migration.ExtractTree(ctx, dfs, r, migration.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, r); err != nil || !r.Done() {
		t.Fatalf("trailer: %v", err)
	}
	if r.Summary() != fw.Summary() {
		t.Fatalf("checksums differ: sent %+v received %+v", fw.Summary(), r.Summary())
	}
	return as, es
}

// TestTreeRoundTripPreservesMetadata (#35 Done-when 4): owners, modes
// (setuid/setgid/sticky included), nanosecond times, symlinks, hard links
// and FIFOs survive the archive -> framed stream -> extract round trip
// exactly; sockets and device nodes are skipped and listed. Runs on every
// platform through the in-memory host filesystem (ownership and special
// bits cannot be observed on a Windows filesystem; the real-filesystem
// variant, TestOSRoundTrip, needs Linux and root for owners).
func TestTreeRoundTripPreservesMetadata(t *testing.T) {
	h := migrationtest.NewHost()
	seedTree(h, "/src/app")
	as, es := roundTrip(t, h, "/src/app", "/dst/app")
	if as.SkippedCount != 2 || len(as.Skipped) != 2 {
		t.Fatalf("skipped %+v", as.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range as.Skipped {
		reasons[s.Path] = s.Reason
	}
	if reasons["sock"] != "socket" || reasons["dev"] != "device node" {
		t.Errorf("skip reasons %v", reasons)
	}
	src, dst := h.Tree("/src/app"), h.Tree("/dst/app")
	delete(src, "sock")
	delete(src, "dev")
	if len(src) != len(dst) {
		t.Fatalf("%d entries copied, want %d:\n%v", len(dst), len(src), dst)
	}
	for name, want := range src {
		got, ok := dst[name]
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if want.Type == "symlink" {
			// A symlink's own timestamps are not preserved (os.Root has
			// no lutimes); everything else is.
			want.MTime, want.ATime, got.MTime, got.ATime = time.Time{}, time.Time{}, time.Time{}, time.Time{}
			want.Mode, got.Mode = 0, 0
		}
		if got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	if dst["data/hard-b"].Inode != "data/hard-a" {
		t.Errorf("hard link not preserved: %+v", dst["data/hard-b"])
	}
	if es.Bytes != as.Bytes || as.Entries != int64(len(src))+2 || es.Entries != int64(len(src)) {
		t.Errorf("stats archive %+v extract %+v", as, es)
	}
}

func member(name string, typ byte, extra func(*tar.Header)) func(*tar.Writer) {
	return func(tw *tar.Writer) {
		hdr := &tar.Header{Name: name, Typeflag: typ, Mode: 0o644, Format: tar.FormatPAX}
		if extra != nil {
			extra(hdr)
		}
		_ = tw.WriteHeader(hdr)
		if typ == tar.TypeReg {
			_, _ = tw.Write(make([]byte, hdr.Size))
		}
	}
}

func root() func(*tar.Writer) { return member("./", tar.TypeDir, nil) }

// TestExtractRefusesUnsafeMembers: the destination never writes outside
// its new root, below a symlink or a file, through a hard link to
// anything but an earlier regular file, or a device node.
func TestExtractRefusesUnsafeMembers(t *testing.T) {
	link := func(target string) func(*tar.Header) { return func(h *tar.Header) { h.Linkname = target } }
	cases := map[string][]func(*tar.Writer){
		"parent escape":       {root(), member("../evil", tar.TypeReg, nil)},
		"nested escape":       {root(), member("a/../../evil", tar.TypeReg, nil)},
		"absolute":            {root(), member("/etc/evil", tar.TypeReg, nil)},
		"backslash":           {root(), member("a\\..\\evil", tar.TypeReg, nil)},
		"below a symlink":     {root(), member("l", tar.TypeSymlink, link("/etc")), member("l/passwd", tar.TypeReg, nil)},
		"below a file":        {root(), member("f", tar.TypeReg, nil), member("f/x", tar.TypeReg, nil)},
		"missing parent":      {root(), member("nodir/x", tar.TypeReg, nil)},
		"hard link ahead":     {root(), member("h", tar.TypeLink, link("later")), member("later", tar.TypeReg, nil)},
		"hard link symlink":   {root(), member("l", tar.TypeSymlink, link("x")), member("h", tar.TypeLink, link("l"))},
		"hard link escape":    {root(), member("h", tar.TypeLink, link("../outside"))},
		"hard link directory": {root(), member("d/", tar.TypeDir, nil), member("h", tar.TypeLink, link("d"))},
		"device node":         {root(), member("sda", tar.TypeBlock, nil)},
		"char device":         {root(), member("tty", tar.TypeChar, nil)},
		"duplicate":           {root(), member("x", tar.TypeReg, nil), member("x", tar.TypeReg, nil)},
		"root not first":      {member("x", tar.TypeReg, nil), root()},
		"no root":             {},
		"empty symlink":       {root(), member("l", tar.TypeSymlink, nil)},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, m := range members {
				m(tw)
			}
			_ = tw.Close()
			h := migrationtest.NewHost()
			h.MkdirAll("/dst/x")
			h.MkdirAll("/etc")
			dfs, _ := h.Opener()("/dst/x")
			_, err := migration.ExtractTree(testutil.Context(t), dfs, &buf, migration.ExtractOptions{})
			if !errors.Is(err, migration.ErrUnsafeArchive) {
				t.Fatalf("error %v, want ErrUnsafeArchive", err)
			}
			if h.Exists("/dst/evil") || h.Exists("/evil") || h.Exists("/etc/passwd") || h.Exists("/outside") {
				t.Fatal("wrote outside the root")
			}
		})
	}
}

// TestExtractHonorsMaxBytes: the destination stops before its free space.
func TestExtractHonorsMaxBytes(t *testing.T) {
	h := migrationtest.NewHost()
	seedTree(h, "/src/app")
	sfs, _ := h.Opener()("/src/app")
	var buf bytes.Buffer
	if _, err := migration.WriteTree(testutil.Context(t), sfs, &buf); err != nil {
		t.Fatal(err)
	}
	h.MkdirAll("/dst/app")
	dfs, _ := h.Opener()("/dst/app")
	if _, err := migration.ExtractTree(testutil.Context(t), dfs, &buf, migration.ExtractOptions{MaxBytes: 1000}); !errors.Is(err, migration.ErrUnsafeArchive) {
		t.Fatalf("error %v", err)
	}
}

// TestWriteTreeDetectsShrinkingFile: a file that shrinks while it is
// archived fails the part (it is retried from the start), never produces a
// corrupt archive.
func TestWriteTreeDetectsShrinkingFile(t *testing.T) {
	h := migrationtest.NewHost()
	h.MkdirAll("/src")
	h.Put("/src/f", migrationtest.Entry{Data: "0123456789"})
	sfs, _ := h.Opener()("/src")
	shrunk := &shrinkFS{FS: sfs}
	if _, err := migration.WriteTree(testutil.Context(t), shrunk, io.Discard); !errors.Is(err, migration.ErrChanged) {
		t.Fatalf("error %v", err)
	}
}

type shrinkFS struct{ migration.FS }

func (s *shrinkFS) Open(name string) (io.ReadCloser, error) {
	rc, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(io.LimitReader(rc, 3)), nil
}

func TestMeasureTree(t *testing.T) {
	h := migrationtest.NewHost()
	seedTree(h, "/src/app")
	sfs, _ := h.Opener()("/src/app")
	entries, bytes, skipped, truncated := migration.MeasureTree(testutil.Context(t), sfs, 1000)
	if entries != 16 || skipped != 2 || truncated || bytes < 640000 {
		t.Fatalf("entries %d bytes %d skipped %d truncated %v", entries, bytes, skipped, truncated)
	}
	if e, _, _, tr := migration.MeasureTree(testutil.Context(t), sfs, 5); !tr || e != 5 {
		t.Fatalf("budget: %d %v", e, tr)
	}
}
