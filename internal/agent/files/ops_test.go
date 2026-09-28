package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/fscorpus"
)

// TestHardlinkToOutsideIsRefused runs on every platform (hard links need
// no privilege on NTFS): a regular file sharing its inode with a file
// outside the root is listed but its content is never served.
func TestHardlinkToOutsideIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.Link(f.secret, filepath.Join(f.root, "hl")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "hl"}); code(err) != protocol.CodeUnsupportedFile {
		t.Fatalf("read: %v", err)
	}
	if _, err := f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"hl"}, Format: protocol.FormatRaw}); code(err) != protocol.CodeUnsupportedFile {
		t.Fatalf("raw download: %v", err)
	}
	b, err := f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"."}, Format: protocol.FormatTarGz})
	if err != nil {
		t.Fatal(err)
	}
	if names, contents := untar(t, b); slices.Contains(names, "hl") || strings.Contains(contents, secretContent) {
		t.Fatalf("tar.gz holds the hard link: %v", names)
	}
	res := f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"hl"}, Destination: "."})
	if !f.itemFailed(res, "hl") && res.Outcome == "succeeded" {
		t.Fatalf("copy: %+v", res)
	}
	// chmod would change the shared inode (the outside file): refused.
	before, _ := os.Stat(f.secret)
	res = f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"."}, Recursive: true,
		Chmod: &protocol.ChmodSpec{Mode: 0o400, DirMode: ptr(uint32(0o755))}})
	if !f.itemFailed(res, "hl") {
		t.Fatalf("chmod of a hard-linked file must fail: %+v", res)
	}
	if after, _ := os.Stat(f.secret); after.Mode() != before.Mode() {
		t.Fatalf("outside file mode changed: %v -> %v", before.Mode(), after.Mode())
	}
	f.write("x/a.txt", "a")
	res = f.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"."}, Destination: "x/all.zip", Format: protocol.FormatZip})
	if res.Outcome != "succeeded" {
		t.Fatalf("archive: %+v", res)
	}
	if strings.Contains(f.readFile("x/all.zip"), secretContent) {
		t.Fatal("archive job stored the hard-linked secret")
	}
	// Deleting the link removes the name inside the root only.
	f.runJob(jobspec.FilesDelete, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"hl"}})
	if b, err := os.ReadFile(f.secret); err != nil || string(b) != secretContent {
		t.Fatalf("outside file changed: %v", err)
	}
}

func untar(t *testing.T, b []byte) ([]string, string) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	var all strings.Builder
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, all.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		c, _ := io.ReadAll(tr)
		all.Write(c)
	}
}

func TestListSortFilterAndPaging(t *testing.T) {
	f := newFixture(t)
	f.write("b.txt", "bb")
	f.write("a.txt", "a")
	f.write("c.txt", "ccc")
	f.write(".hidden", "h")
	f.write("sub/x", "x")
	out, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(out.Entries); !slices.Equal(got, []string{"a.txt", "b.txt", "c.txt", "sub"}) || out.Total != 4 || out.Dir.Type != protocol.FileTypeDir {
		t.Fatalf("default listing %v total %d", got, out.Total)
	}
	out, _ = f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: ".", Hidden: true, Sort: protocol.SortSize, Desc: true})
	if got := names(out.Entries); got[0] != "c.txt" || !slices.Contains(got, ".hidden") {
		t.Fatalf("size desc %v", got)
	}
	out, _ = f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: ".", Sort: protocol.SortType})
	if got := names(out.Entries); got[0] != "sub" {
		t.Fatalf("type sort %v", got)
	}
	out, _ = f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: ".", Query: "B."})
	if got := names(out.Entries); !slices.Equal(got, []string{"b.txt"}) {
		t.Fatalf("query %v", got)
	}
	// Paging with a cursor covers every entry exactly once.
	var all []string
	in := protocol.FilesListInput{Scope: f.vol, Path: ".", Hidden: true, Sort: protocol.SortSize, Limit: 2}
	for range 10 {
		out, err := f.svc.List(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, names(out.Entries)...)
		if out.Next == nil {
			break
		}
		in.After = out.Next
	}
	if len(all) != 5 || len(slices.Compact(slices.Sorted(slices.Values(all)))) != 5 {
		t.Fatalf("paged %v", all)
	}
	if _, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: "a.txt"}); code(err) != protocol.CodeNotDirectory {
		t.Fatalf("list a file: %v", err)
	}
	if _, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: "nope"}); code(err) != protocol.CodeNotFound {
		t.Fatalf("list missing: %v", err)
	}
}

// TestListShowsSymlinksWithoutFollowing: listings show a symlink's target
// and where it resolves; an escaping one is never followed (Linux; skipped
// where symlinks are unavailable).
func TestListShowsSymlinksWithoutFollowing(t *testing.T) {
	f := newFixture(t)
	f.write("t.txt", "t")
	f.symlink("t.txt", "in")
	f.symlink(f.secret, "out")
	out, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]protocol.FileEntry{}
	for _, e := range out.Entries {
		status[e.Name] = e
	}
	if e := status["in"]; e.Type != protocol.FileTypeSymlink || e.LinkStatus != protocol.LinkInside || e.LinkTarget != "t.txt" {
		t.Fatalf("in: %+v", e)
	}
	if e := status["out"]; e.Type != protocol.FileTypeSymlink || e.LinkStatus != protocol.LinkOutside {
		t.Fatalf("out: %+v", e)
	}
	if _, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "out"}); code(err) != protocol.CodeForbiddenPath {
		t.Fatalf("read through an escaping link: %v", err)
	}
	if r, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "in"}); err != nil || string(r.Data) != "t" {
		t.Fatalf("read through an inside link: %v", err)
	}
}

func names(es []protocol.FileEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

// TestReadWriteETagAndConflicts: reads carry the content ETag; a write
// with that ETag succeeds, a second write with the stale ETag is a
// conflict (the editor's 412), create-only refuses existing files, binary
// detection and bounded reads work, and writes are atomic renames.
func TestReadWriteETagAndConflicts(t *testing.T) {
	f := newFixture(t)
	f.write("conf/app.env", "A=1\n")
	r1, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "conf/app.env"})
	if err != nil || string(r1.Data) != "A=1\n" || r1.Binary || r1.Truncated || !strings.HasPrefix(r1.Entry.ETag, "f1-") {
		t.Fatalf("read %+v %v", r1, err)
	}
	st, _ := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: f.vol, Path: "conf/app.env", ETag: true})
	if st.ETag != r1.Entry.ETag {
		t.Fatalf("stat etag %s != read etag %s", st.ETag, r1.Entry.ETag)
	}
	e, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf/app.env", Data: []byte("A=2\n"), IfMatch: []string{r1.Entry.ETag}})
	if err != nil || e.ETag == "" || e.ETag == r1.Entry.ETag {
		t.Fatalf("write: %+v %v", e, err)
	}
	r2, _ := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "conf/app.env"})
	if string(r2.Data) != "A=2\n" || r2.Entry.ETag != e.ETag {
		t.Fatalf("after write %q etag %s want %s", r2.Data, r2.Entry.ETag, e.ETag)
	}
	// A concurrent editor saving from the old revision is refused.
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf/app.env", Data: []byte("A=3\n"), IfMatch: []string{r1.Entry.ETag}}); code(err) != protocol.CodeConflict {
		t.Fatalf("stale write: %v", err)
	}
	if got := f.readFile("conf/app.env"); got != "A=2\n" {
		t.Fatalf("stale write changed the file: %q", got)
	}
	// An external edit changes the ETag too.
	f.write("conf/app.env", "A=external\n")
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf/app.env", Data: []byte("A=4\n"), IfMatch: []string{e.ETag}}); code(err) != protocol.CodeConflict {
		t.Fatalf("write over external edit: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf/app.env", Data: []byte("x"), CreateOnly: true}); code(err) != protocol.CodeAlreadyExists {
		t.Fatalf("create-only over existing: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf/app.env", Data: []byte("x")}); code(err) != protocol.CodeInvalidFrame {
		t.Fatalf("no precondition: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "conf", Data: []byte("x"), Overwrite: true}); code(err) != protocol.CodeIsDirectory {
		t.Fatalf("overwrite a directory: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "big", Data: make([]byte, protocol.MaxInlineContent+1), CreateOnly: true}); code(err) != protocol.CodeTooLarge {
		t.Fatalf("oversized inline write: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "missing/dir/x", Data: []byte("x"), CreateOnly: true}); code(err) != protocol.CodeNotFound {
		t.Fatalf("write into a missing directory: %v", err)
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(filepath.Join(f.root, "conf"))
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".docker-manager-") {
			t.Fatalf("temporary file left: %s", de.Name())
		}
	}
	// Binary and truncated reads.
	f.write("bin", "a\x00b")
	if r, _ := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "bin"}); !r.Binary {
		t.Fatal("NUL not detected as binary")
	}
	f.write("large.txt", strings.Repeat("é", protocol.MaxInlineContent)) // 2 bytes per rune
	r, _ := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "large.txt"})
	if !r.Truncated || r.Binary || len(r.Data) != protocol.MaxInlineContent || r.Entry.Size != 2*protocol.MaxInlineContent {
		t.Fatalf("large read: truncated %v binary %v len %d", r.Truncated, r.Binary, len(r.Data))
	}
	r, _ = f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "large.txt", Offset: 1, Length: 3})
	if !r.Binary && string(r.Data) != "\xa9é" {
		t.Fatalf("range read %q", r.Data)
	}
	if _, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "conf"}); code(err) != protocol.CodeIsDirectory {
		t.Fatalf("read a directory: %v", err)
	}
	// Mkdir and new files.
	if _, err := f.svc.Mkdir(f.ctx, protocol.FilesMkdirInput{Scope: f.vol, Path: "newdir", Type: protocol.FileTypeDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Mkdir(f.ctx, protocol.FilesMkdirInput{Scope: f.vol, Path: "newdir", Type: protocol.FileTypeDir}); code(err) != protocol.CodeAlreadyExists {
		t.Fatalf("mkdir twice: %v", err)
	}
	if e, err := f.svc.Mkdir(f.ctx, protocol.FilesMkdirInput{Scope: f.vol, Path: "newdir/n.txt", Type: protocol.FileTypeFile, Data: []byte("n")}); err != nil || e.Size != 1 {
		t.Fatalf("new file %+v %v", e, err)
	}
	// Docker Manager's own changes are reported as invalidations (never content).
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for _, p := range f.inval {
		if p.Scope.Kind != protocol.ScopeVolume || p.Scope.ID != "data" {
			t.Fatalf("invalidation scope %+v", p.Scope)
		}
		paths = append(paths, p.Paths...)
	}
	for _, want := range []string{"conf/app.env", "newdir", "newdir/n.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("no invalidation for %s: %v", want, paths)
		}
	}
}

// TestStackScope serves a stack's project directory.
func TestStackScope(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.stack, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.stk, Path: "."})
	if err != nil || !slices.Equal(names(out.Entries), []string{"compose.yaml"}) {
		t.Fatalf("list %+v %v", out, err)
	}
	r, _ := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.stk, Path: "compose.yaml"})
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.stk, Path: "compose.yaml", Data: []byte("services:\n  web: {}\n"), IfMatch: []string{r.Entry.ETag}}); err != nil {
		t.Fatal(err)
	}
}

// TestUploads: bounded uploads with preconditions and conflict policies.
func TestUploads(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.MaxUpload = 1 << 20 })
	body := bytes.Repeat([]byte("0123456789"), 50_000) // 500 000 bytes, several stream windows? no: one window
	sum := sha256.Sum256(body)
	res, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), CreateOnly: true}, body)
	if err != nil || res.Entry.Name != "up.bin" || res.Entry.Size != int64(len(body)) || res.Entry.ETag == "" {
		t.Fatalf("upload %+v %v", res, err)
	}
	if f.readFile("up.bin") != string(body) {
		t.Fatal("content differs")
	}
	// Create-only over an existing file: refused before any byte is kept.
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 3, CreateOnly: true}, []byte("new")); code(err) != protocol.CodeAlreadyExists {
		t.Fatalf("create-only: %v", err)
	}
	// Replace exactly the revision the client has.
	res2, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 3, IfMatch: []string{res.Entry.ETag}}, []byte("new"))
	if err != nil || f.readFile("up.bin") != "new" {
		t.Fatalf("if-match upload %v", err)
	}
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 3, IfMatch: []string{res.Entry.ETag}}, []byte("old")); code(err) != protocol.CodeConflict {
		t.Fatalf("stale if-match upload: %v", err)
	}
	// Conflict policies.
	kb, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 2, Conflict: protocol.ConflictKeepBoth}, []byte("kb"))
	if err != nil || kb.Entry.Name != "up (1).bin" || f.readFile("up (1).bin") != "kb" {
		t.Fatalf("keep_both %+v %v", kb, err)
	}
	sk, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 2, Conflict: protocol.ConflictSkip}, []byte("sk"))
	if err != nil || !sk.Skipped || f.readFile("up.bin") != "new" || sk.Entry.ETag != "" && sk.Entry.ETag != res2.Entry.ETag {
		t.Fatalf("skip %+v %v", sk, err)
	}
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "up.bin", Size: 2, Conflict: protocol.ConflictOverwrite}, []byte("ow")); err != nil || f.readFile("up.bin") != "ow" {
		t.Fatalf("overwrite %v", err)
	}
	// Oversized uploads are refused (declared size, and a sender sending
	// more than it declared).
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "huge", Size: 2 << 20, CreateOnly: true}, nil); code(err) != protocol.CodeTooLarge {
		t.Fatalf("oversized: %v", err)
	}
	s, err := f.pipe().Open(f.ctx, protocol.StreamFilesUpload, protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "liar", Size: 10, CreateOnly: true}, streamOpts(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write(make([]byte, 100))
	_ = s.CloseWrite()
	if _, err := s.Result(f.ctx); code(err) != protocol.CodeTooLarge {
		t.Fatalf("more bytes than declared: %v", err)
	}
	if f.exists("liar") || f.exists("huge") {
		t.Fatal("refused upload left a file")
	}
	// A digest mismatch is refused.
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "d", Size: 1, SHA256: strings.Repeat("0", 64), CreateOnly: true}, []byte("x")); code(err) != protocol.CodeDigestMismatch {
		t.Fatalf("digest: %v", err)
	}
	if _, err := f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "../x", Size: 1, CreateOnly: true}, []byte("x")); code(err) != protocol.CodeForbiddenPath {
		t.Fatalf("bad name: %v", err)
	}
	entries, _ := os.ReadDir(f.root)
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".docker-manager-") {
			t.Fatalf("temporary file left: %s", de.Name())
		}
	}
}

// TestDownloads: raw files with ranges, and zip/tar.gz archives.
func TestDownloads(t *testing.T) {
	f := newFixture(t)
	big := bytes.Repeat([]byte("abcdefgh"), 400_000) // 3.2 MB: several stream windows
	f.write("dir/big.bin", string(big))
	f.write("dir/sub/small.txt", "small")
	b, err := f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"dir/big.bin"}, Format: protocol.FormatRaw})
	if err != nil || !bytes.Equal(b, big) {
		t.Fatalf("raw: %d %v", len(b), err)
	}
	b, err = f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"dir/big.bin"}, Format: protocol.FormatRaw, Offset: 5, Length: 6})
	if err != nil || string(b) != "fghabc" {
		t.Fatalf("range: %q %v", b, err)
	}
	b, err = f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"dir"}, Format: protocol.FormatZip})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var zn []string
	for _, zf := range zr.File {
		zn = append(zn, zf.Name)
	}
	slices.Sort(zn)
	if !slices.Equal(zn, []string{"dir/", "dir/big.bin", "dir/sub/", "dir/sub/small.txt"}) {
		t.Fatalf("zip members %v", zn)
	}
	b, err = f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"dir/sub", "dir/big.bin"}, Format: protocol.FormatTarGz})
	if err != nil {
		t.Fatal(err)
	}
	tn, _ := untar(t, b)
	slices.Sort(tn)
	if !slices.Equal(tn, []string{"big.bin", "sub/", "sub/small.txt"}) {
		t.Fatalf("tar members %v", tn)
	}
	if _, err := f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"dir"}, Format: protocol.FormatRaw}); code(err) != protocol.CodeIsDirectory {
		t.Fatalf("raw directory: %v", err)
	}
	g := newFixture(t, func(o *Options) { o.Limits.MaxDownload = 1 << 20 })
	noise := make([]byte, 3<<19) // incompressible 1.5 MiB
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	g.write("big.bin", string(noise))
	if _, err := g.download(protocol.FilesDownloadInput{Scope: g.vol, Paths: []string{"."}, Format: protocol.FormatZip}); code(err) != protocol.CodeTooLarge {
		t.Fatalf("archive download limit: %v", err)
	}
	if _, err := g.download(protocol.FilesDownloadInput{Scope: g.vol, Paths: []string{"big.bin"}, Format: protocol.FormatRaw}); code(err) != protocol.CodeTooLarge {
		t.Fatalf("raw download limit: %v", err)
	}
}

// TestFileJobs: copy, move, delete, archive, extract and chmod with
// conflict policies and per-item results.
func TestFileJobs(t *testing.T) {
	f := newFixture(t)
	f.write("src/a.txt", "a")
	f.write("src/deep/b.txt", "b")
	f.write("dst/a.txt", "old")
	if err := os.MkdirAll(filepath.Join(f.root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt", "src/deep"}, Destination: "dst"})
	if res.Outcome != "partial" || !f.itemFailed(res, "src/a.txt") || f.readFile("dst/deep/b.txt") != "b" || f.readFile("dst/a.txt") != "old" {
		t.Fatalf("copy with a conflict: %+v", res)
	}
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt"}, Destination: "dst", Conflict: protocol.ConflictKeepBoth})
	if res.Outcome != "succeeded" || f.readFile("dst/a (1).txt") != "a" {
		t.Fatalf("keep_both: %+v", res)
	}
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt"}, Destination: "dst", Conflict: protocol.ConflictOverwrite})
	if res.Outcome != "succeeded" || f.readFile("dst/a.txt") != "a" {
		t.Fatalf("overwrite: %+v", res)
	}
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src"}, Destination: "src/deep"})
	if res.Outcome != "partial" {
		t.Fatalf("copy into itself: %+v", res)
	}
	// Copy into the entry's own directory (the file manager's paste into the
	// same folder): only keep_both duplicates it; anything else fails.
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt"}, Destination: "src", Conflict: protocol.ConflictOverwrite})
	if res.Outcome != "partial" || !f.itemFailed(res, "src/a.txt") || f.readFile("src/a.txt") != "a" {
		t.Fatalf("overwrite onto itself: %+v", res)
	}
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt", "src/deep"}, Destination: "src", Conflict: protocol.ConflictKeepBoth})
	if res.Outcome != "succeeded" || f.readFile("src/a (1).txt") != "a" || f.readFile("src/deep (1)/b.txt") != "b" || f.readFile("src/a.txt") != "a" {
		t.Fatalf("duplicate with keep_both: %+v", res)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a (1).txt"}, Destination: "src", Conflict: protocol.ConflictKeepBoth})
	if res.Outcome != "partial" || !f.exists("src/a (1).txt") {
		t.Fatalf("move onto itself: %+v", res)
	}
	// Preview before a move: one conflict, impact counted recursively.
	pv, err := f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: f.vol, Operation: protocol.FileOpMove, Paths: []string{"src/deep", "src/a.txt"}, Destination: "dst"})
	if err != nil || len(pv.Conflicts) != 2 || pv.Impact.Files != 2 || pv.Impact.Dirs != 1 {
		t.Fatalf("preview %+v %v", pv, err)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt"}, Destination: "dst", Conflict: protocol.ConflictSkip})
	if res.Outcome != "succeeded" || !f.exists("src/a.txt") {
		t.Fatalf("move skip: %+v", res)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"src/a.txt"}, Destination: "."})
	if res.Outcome != "succeeded" || f.exists("src/a.txt") || f.readFile("a.txt") != "a" {
		t.Fatalf("move: %+v", res)
	}
	// A rename is a move into the same directory under a new name.
	pv, _ = f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: f.vol, Operation: protocol.FileOpMove, Paths: []string{"a.txt"},
		Destination: ".", Names: []string{"dst"}})
	if len(pv.Conflicts) != 1 || pv.Conflicts[0].Destination != "dst" {
		t.Fatalf("rename preview %+v", pv)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"a.txt"}, Destination: ".", Name: "r.txt"})
	if res.Outcome != "succeeded" || f.exists("a.txt") || f.readFile("r.txt") != "a" {
		t.Fatalf("rename: %+v", res)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"r.txt"}, Destination: ".", Name: "r.txt"})
	if res.Outcome != "partial" {
		t.Fatalf("rename onto itself: %+v", res)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"r.txt"}, Destination: ".", Name: "../x"})
	if res.Outcome != "failed" {
		t.Fatalf("rename to an escaping name: %+v", res)
	}
	res = f.runJob(jobspec.FilesMove, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"r.txt"}, Destination: ".", Name: "a.txt"})
	if res.Outcome != "succeeded" {
		t.Fatalf("rename back: %+v", res)
	}
	res = f.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"dst"}, Destination: "dst.tar.gz", Format: protocol.FormatTarGz})
	if res.Outcome != "succeeded" {
		t.Fatalf("archive: %+v", res)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"dst.tar.gz"}, Destination: "restored"})
	if res.Outcome != "succeeded" || f.readFile("restored/dst/deep/b.txt") != "b" {
		t.Fatalf("extract: %+v", res)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"dst.tar.gz"}, Destination: "restored"})
	if res.Outcome != "partial" {
		t.Fatalf("extract onto existing files must report conflicts: %+v", res)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"dst.tar.gz"}, Destination: "restored", Conflict: protocol.ConflictOverwrite})
	if res.Outcome != "succeeded" {
		t.Fatalf("extract overwrite: %+v", res)
	}
	res = f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"restored"}, Recursive: true,
		Chmod: &protocol.ChmodSpec{Mode: 0o640, DirMode: ptr(uint32(0o750))}})
	if res.Outcome != "succeeded" {
		t.Fatalf("chmod: %+v", res)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(f.root, "restored", "dst", "deep", "b.txt"))
		di, _ := os.Stat(filepath.Join(f.root, "restored", "dst", "deep"))
		if fi.Mode().Perm() != 0o640 || di.Mode().Perm() != 0o750 {
			t.Fatalf("modes %v %v", fi.Mode(), di.Mode())
		}
		uid, gid := uint32(os.Getuid()), uint32(os.Getgid()) //nolint:gosec // G115: test IDs
		res = f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"restored"}, Recursive: true,
			Chown: &protocol.ChownSpec{UID: &uid, GID: &gid}})
		if res.Outcome != "succeeded" {
			t.Fatalf("chown: %+v", res)
		}
	}
	res = f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"a.txt"}, Chmod: &protocol.ChmodSpec{Mode: 0o4755}})
	if res.Outcome != "failed" {
		t.Fatalf("setuid must be refused: %+v", res)
	}
	pv, _ = f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: f.vol, Operation: protocol.FileOpDelete, Paths: []string{"restored", "dst"}})
	// restored/dst/{a.txt, a (1).txt, deep/b.txt} and dst/{same}.
	if pv.Impact.Files != 6 || pv.Impact.Dirs != 5 {
		t.Fatalf("delete preview %+v", pv.Impact)
	}
	res = f.runJob(jobspec.FilesDelete, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"restored", "dst", "nope", "."}})
	if res.Outcome != "partial" || f.exists("restored") || f.exists("dst") {
		t.Fatalf("delete: %+v", res)
	}
}

func ptr[T any](v T) *T { return &v }

func streamOpts(maxBytes int64) streammux.OpenOptions {
	return streammux.OpenOptions{MaxBytes: maxBytes}
}

// TestContentNeverLogged: file contents written, read, uploaded and
// downloaded never reach the agent log.
func TestContentNeverLogged(t *testing.T) {
	f := newFixture(t)
	const canary = "CANARY-FILE-CONTENT-9c1e77aa"
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: f.vol, Path: "c.env", Data: []byte("TOKEN=" + canary), CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "c.env"})
	_, _ = f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "u.env", Size: int64(len(canary)), CreateOnly: true}, []byte(canary))
	_, _ = f.upload(protocol.FilesUploadInput{Scope: f.vol, Dir: ".", Name: "u.env", Size: int64(len(canary)), SHA256: strings.Repeat("1", 64), Conflict: protocol.ConflictOverwrite}, []byte(canary))
	_, _ = f.download(protocol.FilesDownloadInput{Scope: f.vol, Paths: []string{"."}, Format: protocol.FormatZip})
	f.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"."}, Destination: "all.zip", Format: protocol.FormatZip})
	if strings.Contains(f.logs.String(), canary) {
		t.Fatalf("file content reached the log:\n%s", f.logs.String())
	}
}

// TestManagerLimitsInJobInputs: the limits the manager sends in a job's
// input (FeatureFileLimits) replace the agent's defaults for that job, in
// both directions.
func TestManagerLimitsInJobInputs(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Limits.MaxArchiveEntries = 10 })
	f.write("many.zip", string(fscorpus.ZipManyEntries(50)))
	res := f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"many.zip"}, Destination: "a"})
	if res.Outcome != "failed" || !strings.Contains(res.Message, protocol.CodeTooLarge) {
		t.Fatalf("extract over the agent's entry limit: %+v", res)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"many.zip"}, Destination: "b",
		Limits: &protocol.FileLimits{MaxArchiveEntries: 100}})
	if res.Outcome != "succeeded" {
		t.Fatalf("extract within the manager's raised entry limit: %+v", res)
	}
	out, err := f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: f.vol, Operation: protocol.FileOpExtract, Paths: []string{"many.zip"},
		Destination: "c", Limits: &protocol.FileLimits{MaxArchiveEntries: 100}})
	if err != nil || out.Impact.Entries != 50 {
		t.Fatalf("preview within the raised limit: %+v %v", out.Impact, err)
	}

	g := newFixture(t)
	g.write("text.txt", strings.Repeat("a", 64<<10))
	res = g.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: g.vol, Paths: []string{"text.txt"}, Destination: "text.zip", Format: protocol.FormatZip})
	if res.Outcome != "succeeded" {
		t.Fatalf("archive: %+v", res)
	}
	// 64 KiB of text compress well below the agent's 1 MiB ratio floor; a
	// lower extraction budget from the manager stops the job.
	res = g.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: g.vol, Paths: []string{"text.zip"}, Destination: "small",
		Limits: &protocol.FileLimits{MaxExtractBytes: 1024}})
	if res.Outcome != "failed" || !strings.Contains(res.Message, protocol.CodeTooLarge) {
		t.Fatalf("extract over the manager's byte limit: %+v", res)
	}
	res = g.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: g.vol, Paths: []string{"text.txt"}, Destination: "tiny.zip", Format: protocol.FormatZip,
		Limits: &protocol.FileLimits{MaxDownload: 100}})
	if res.Outcome != "failed" || !strings.Contains(res.Message, protocol.CodeTooLarge) || g.exists("tiny.zip") {
		t.Fatalf("archive over the manager's size limit: %+v", res)
	}
}
