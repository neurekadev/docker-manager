package files

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/fsroot"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil/fscorpus"
)

// every runs every operation of the service on path p (as source,
// destination and name where it fits) and fails the test when any of them
// returns the outside secret.
func (f *fixture) every(scope protocol.FileScope, p string) {
	f.t.Helper()
	leak := func(what string, b []byte) {
		if bytes.Contains(b, []byte(secretContent)) {
			f.t.Errorf("%s %q returned the outside secret", what, p)
		}
	}
	if e, err := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: scope, Path: p, ETag: true}); err == nil && !protocol.ValidRelativePath(e.Path) {
		f.t.Errorf("stat %q answered path %q", p, e.Path)
	}
	if out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: scope, Path: p}); err == nil {
		leak("read", out.Data)
	}
	if out, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: scope, Path: p, Hidden: true}); err == nil {
		for _, e := range out.Entries {
			if !protocol.ValidRelativePath(e.Path) {
				f.t.Errorf("list %q answered path %q", p, e.Path)
			}
		}
	}
	_, _ = f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: p, Data: []byte("w"), CreateOnly: true})
	_, _ = f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: p, Data: []byte("w"), Overwrite: true})
	_, _ = f.svc.Mkdir(f.ctx, protocol.FilesMkdirInput{Scope: scope, Path: p, Type: protocol.FileTypeDir})
	_, _ = f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: scope, Operation: protocol.FileOpCopy, Paths: []string{p}, Destination: p})
	_, _ = f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: scope, Operation: protocol.FileOpExtract, Paths: []string{p}, Destination: "."})
	for _, format := range []string{protocol.FormatRaw, protocol.FormatZip, protocol.FormatTarGz} {
		if b, err := f.download(protocol.FilesDownloadInput{Scope: scope, Paths: []string{p}, Format: format}); err == nil {
			leak("download "+format, b)
			if format == protocol.FormatZip {
				f.leakInZip(b)
			}
		}
	}
	_, _ = f.upload(protocol.FilesUploadInput{Scope: scope, Dir: p, Name: "up.txt", Size: 2, Conflict: protocol.ConflictOverwrite}, []byte("up"))
	chmod := &protocol.ChmodSpec{Mode: 0o700}
	for _, job := range []struct {
		kind domain.JobKind
		in   protocol.FilesJobInput
	}{
		{jobspec.FilesCopy, protocol.FilesJobInput{Paths: []string{p}, Destination: ".", Conflict: protocol.ConflictKeepBoth}},
		{jobspec.FilesCopy, protocol.FilesJobInput{Paths: []string{"."}, Destination: p}},
		{jobspec.FilesMove, protocol.FilesJobInput{Paths: []string{p}, Destination: "moved", Conflict: protocol.ConflictKeepBoth}},
		{jobspec.FilesArchive, protocol.FilesJobInput{Paths: []string{p}, Destination: "a.zip", Format: protocol.FormatZip, Conflict: protocol.ConflictKeepBoth}},
		{jobspec.FilesArchive, protocol.FilesJobInput{Paths: []string{"."}, Destination: p, Format: protocol.FormatZip}},
		{jobspec.FilesExtract, protocol.FilesJobInput{Paths: []string{p}, Destination: "x", Conflict: protocol.ConflictKeepBoth}},
		{jobspec.FilesMetadata, protocol.FilesJobInput{Paths: []string{p}, Recursive: true, Chmod: chmod}},
		{jobspec.FilesDelete, protocol.FilesJobInput{Paths: []string{p}}},
	} {
		job.in.Scope = scope
		_ = os.MkdirAll(filepath.Join(f.root, "moved"), 0o755)
		f.runJob(job.kind, job.in)
	}
}

// leakInZip fails when any member of a zip holds the secret.
func (f *fixture) leakInZip(b []byte) {
	f.t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		f.t.Errorf("download produced an invalid zip: %v", err)
		return
	}
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		c, _ := io.ReadAll(rc)
		_ = rc.Close()
		if bytes.Contains(c, []byte(secretContent)) {
			f.t.Errorf("zip member %q holds the outside secret", zf.Name)
		}
	}
}

// TestTraversalCorpusStaysInsideRoot runs every operation with every path
// of the traversal corpus (#29) against both scopes: each either fails or
// acts inside the root; nothing outside the roots changes and the outside
// secret is never returned. Runs on every platform.
//
// Each case starts from the same roots: a corpus path that is a legal name
// inside the root (e.g. "..／secret.txt") receives a copy and an archive
// of the whole root, so carrying the roots over would double them per case
// (exponential work, 30 s and more under -race).
func TestTraversalCorpusStaysInsideRoot(t *testing.T) {
	f := newFixture(t)
	f.write("inside.txt", "inside\n")
	before := f.snapshot()
	for _, c := range fscorpus.TraversalCases() {
		for _, scope := range []protocol.FileScope{f.vol, f.stk} {
			f.every(scope, c.Path)
		}
		if after := f.snapshot(); after != before {
			t.Fatalf("case %s (%q): something outside the roots changed:\nbefore:\n%s\nafter:\n%s", c.Name, c.Path, before, after)
		}
		f.resetRoots()
		f.write("inside.txt", "inside\n")
	}
	// Traversal paths are refused outright, before touching the disk.
	for _, p := range []string{"..", "../secret.txt", "a/../../x", "/etc/passwd", "..\\x", "a\x00b", "./a", "a//b"} {
		if _, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: p}); code(err) != protocol.CodeForbiddenPath {
			t.Errorf("read %q: %v, want forbidden_path", p, err)
		}
	}
}

// escapeFixture builds the fscorpus escape tree and serves its root as the
// volume "tree" (skipped where symlinks are unavailable).
func escapeFixture(t *testing.T) (*fixture, *fscorpus.EscapeTree, protocol.FileScope) {
	t.Helper()
	f := newFixture(t)
	et, err := fscorpus.NewEscapeTree(filepath.Join(f.base, "tree"))
	if errors.Is(err, fscorpus.ErrSymlinksUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	f.eng.volumes["tree"] = volume("tree", et.Root)
	return f, et, protocol.FileScope{Kind: protocol.ScopeVolume, ID: "tree"}
}

// TestEscapeTreeIsRefused: every escaping symlink and the hard link to the
// outside secret are refused for content access, downloads and archives
// skip them, recursive operations never follow them, and legitimate links
// inside the root keep working.
func TestEscapeTreeIsRefused(t *testing.T) {
	f, et, scope := escapeFixture(t)
	secretPath := filepath.Join(et.Outside, "secret.txt")
	secretBefore, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range et.Escaping() {
		out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: scope, Path: c.Path})
		if err == nil || strings.Contains(string(out.Data), et.Secret) {
			t.Errorf("read %s (%s): %v %q", c.Path, c.Note, err, out.Data)
			continue
		}
		want := protocol.CodeForbiddenPath
		if c.Kind == fscorpus.KindHardlink {
			want = protocol.CodeUnsupportedFile
		}
		if code(err) != want {
			t.Errorf("read %s: code %s, want %s", c.Path, code(err), want)
		}
		if b, err := f.download(protocol.FilesDownloadInput{Scope: scope, Paths: []string{c.Path}, Format: protocol.FormatRaw}); err == nil || bytes.Contains(b, []byte(et.Secret)) {
			t.Errorf("raw download %s: %v", c.Path, err)
		}
		// Writing through an escaping link never creates anything outside.
		_, _ = f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: c.Path + "/new.txt", Data: []byte("x"), CreateOnly: true})
		if c.Kind == fscorpus.KindHardlink {
			continue // replacing the hard-linked name is checked below, after the archive and copy checks
		}
		if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: c.Path, Data: []byte("x"), Overwrite: true}); err == nil {
			t.Errorf("overwriting symlink %s succeeded", c.Path)
		}
	}
	// The hard-linked file gets no ETag (it would be derived from the
	// outside content), so If-Match with a guessed tag cannot confirm it.
	hl, err := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: scope, Path: "hardlink-secret", ETag: true})
	if err != nil || hl.Type != protocol.FileTypeFile || hl.ETag != "" || (runtime.GOOS != "windows" && hl.Links != 2) {
		t.Errorf("stat hardlink-secret: %+v %v", hl, err)
	}
	secretInfo, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(et.Secret))
	guess := fsroot.ComputeETag(sum[:], secretInfo.Size(), secretInfo.ModTime())
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: "hardlink-secret", Data: []byte("x"), IfMatch: []string{guess}}); code(err) != protocol.CodeConflict {
		t.Errorf("If-Match with the outside file's tag: %v, want conflict", err)
	}
	if _, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: scope, Path: "escape-dir"}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("list escape-dir: %v", err)
	}
	// Stat shows symlinks without following them.
	e, err := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: scope, Path: "escape-rel"})
	if err != nil || e.Type != protocol.FileTypeSymlink || e.LinkStatus != protocol.LinkOutside || e.LinkTarget != "../outside/secret.txt" {
		t.Errorf("stat escape-rel: %+v %v", e, err)
	}
	if e, _ := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: scope, Path: "link-inside"}); e.LinkStatus != protocol.LinkInside {
		t.Errorf("link-inside status %q", e.LinkStatus)
	}
	if e, _ := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: scope, Path: "loop-a"}); e.LinkStatus != protocol.LinkLoop && e.LinkStatus != protocol.LinkDangling {
		t.Errorf("loop-a status %q", e.LinkStatus)
	}
	// Legitimate entries still work.
	for p, want := range map[string]string{"inside.txt": "inside\n", "link-inside": "inside\n", "link-dir-inside/nested.txt": "nested\n"} {
		out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: scope, Path: p})
		if err != nil || string(out.Data) != want {
			t.Errorf("read %s: %q %v", p, out.Data, err)
		}
	}
	// An archive of the whole root skips the escapes and lists them.
	b, err := f.download(protocol.FilesDownloadInput{Scope: scope, Paths: []string{"."}, Format: protocol.FormatZip})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(et.Secret)) {
		t.Fatal("zip download contains the secret")
	}
	f.leakInZip(b)
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	var names []string
	var skippedList string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
		if zf.Name == protocol.SkippedListName {
			rc, _ := zf.Open()
			sb, _ := io.ReadAll(rc)
			skippedList = string(sb)
		}
	}
	for _, want := range []string{"escape-abs", "escape-rel", "escape-dir", "hardlink-secret"} {
		if !strings.Contains(skippedList, want) {
			t.Errorf("skipped list lacks %s: %q (members %v)", want, skippedList, names)
		}
	}
	// Recursive operations never follow links out of the root.
	res := f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: scope, Paths: []string{"."}, Recursive: true, Chmod: &protocol.ChmodSpec{Mode: 0o700}})
	if res.Outcome == "failed" {
		t.Errorf("chmod job: %+v", res)
	}
	res = f.runJob(jobspec.FilesCopy, protocol.FilesJobInput{Scope: scope, Paths: []string{"escape-dir", "hardlink-secret"}, Destination: "dir"})
	if !f.itemFailed(res, "hardlink-secret") {
		t.Errorf("copying the hard-linked file must fail: %+v", res.Items)
	}
	if fi, err := os.Lstat(filepath.Join(et.Root, "dir", "escape-dir")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("a copied symlink must stay a symlink: %v", err)
	}
	f.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: scope, Paths: []string{"."}, Destination: "all.tar.gz", Format: protocol.FormatTarGz})
	if tgz, err := os.ReadFile(filepath.Join(et.Root, "all.tar.gz")); err != nil {
		t.Errorf("archive job: %v", err)
	} else if zr, err := gzip.NewReader(bytes.NewReader(tgz)); err != nil {
		t.Errorf("archive job wrote an invalid gzip stream: %v", err)
	} else if tb, _ := io.ReadAll(zr); !bytes.Contains(tb, []byte("hardlink-secret  (several hard links)")) || bytes.Contains(tb, []byte(et.Secret)) {
		t.Error("the archive job must skip the hard-linked file and list it")
	}
	// Replacing the hard-linked name renames a new file over it: the
	// outside name keeps its inode and content (checked below).
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: "hardlink-secret", Data: []byte("x"), Overwrite: true}); err != nil {
		t.Errorf("overwrite hardlink-secret: %v", err)
	}
	if a, err1 := os.Stat(filepath.Join(et.Root, "hardlink-secret")); err1 != nil || os.SameFile(a, secretBefore) {
		t.Errorf("the overwrite must replace the name, not write the shared inode: %v", err1)
	}
	res = f.runJob(jobspec.FilesDelete, protocol.FilesJobInput{Scope: scope, Paths: []string{"escape-dir", "escape-abs", "hardlink-secret"}})
	if res.Outcome != "succeeded" {
		t.Errorf("delete: %+v", res)
	}
	secretAfter, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("the outside secret was removed: %v", err)
	}
	b2, _ := os.ReadFile(secretPath)
	if string(b2) != et.Secret || secretAfter.Mode() != secretBefore.Mode() || !secretAfter.ModTime().Equal(secretBefore.ModTime()) {
		t.Fatalf("the outside secret changed: %v -> %v", secretBefore.Mode(), secretAfter.Mode())
	}
	if strings.Contains(f.logs.String(), et.Secret) {
		t.Fatal("the secret reached the agent log")
	}
}

func (f *fixture) itemFailed(res protocol.ResultPayload, name string) bool {
	for _, it := range res.Items {
		if it.Name == name && it.Status == domain.ItemFailed {
			return true
		}
	}
	return false
}

// TestTOCTOUDirectorySwap races reads, listings, writes and chmod against
// a directory that is flipped to a symlink pointing outside the root
// (fscorpus.RaceWhile): no operation ever sees or changes the outside.
func TestTOCTOUDirectorySwap(t *testing.T) {
	f, et, scope := escapeFixture(t)
	opts := fscorpus.RaceOptions{MinAttempts: 200, MinSuccesses: 20, MinFlips: 50}
	dir := filepath.Join(et.Root, "dir")
	res, err := fscorpus.RaceWhile(dir, et.Outside, opts, func() error {
		out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: scope, Path: "dir/nested.txt"})
		if err == nil && strings.Contains(string(out.Data), et.Secret) {
			return fmt.Errorf("read outside: %w", fscorpus.ErrEscaped)
		}
		return err
	})
	if err != nil {
		t.Fatalf("read race: %v (%+v)", err, res)
	}
	res, err = fscorpus.RaceWhile(dir, et.Outside, opts, func() error {
		out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: scope, Path: "dir/secret.txt"})
		if err == nil && strings.Contains(string(out.Data), et.Secret) {
			return fmt.Errorf("read outside: %w", fscorpus.ErrEscaped)
		}
		// dir/secret.txt exists only through the swapped link: count the
		// in-root listing as the operation's success.
		if _, lerr := f.svc.List(f.ctx, protocol.FilesListInput{Scope: scope, Path: "dir"}); lerr == nil {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatalf("list race: %v (%+v)", err, res)
	}
	res, err = fscorpus.RaceWhile(dir, et.Outside, opts, func() error {
		_, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: scope, Path: "dir/written.txt", Data: []byte("x"), Overwrite: true})
		if _, serr := os.Stat(filepath.Join(et.Outside, "written.txt")); serr == nil {
			return fmt.Errorf("write outside: %w", fscorpus.ErrEscaped)
		}
		return err
	})
	if err != nil {
		t.Fatalf("write race: %v (%+v)", err, res)
	}
	secretBefore, _ := os.Stat(filepath.Join(et.Outside, "secret.txt"))
	res, err = fscorpus.RaceWhile(dir, et.Outside, opts, func() error {
		r := f.runJob(jobspec.FilesMetadata, protocol.FilesJobInput{Scope: scope, Paths: []string{"dir"}, Recursive: true,
			Chmod: &protocol.ChmodSpec{Mode: 0o600, DirMode: ptr(uint32(0o755))}})
		if st, _ := os.Stat(filepath.Join(et.Outside, "secret.txt")); st.Mode() != secretBefore.Mode() {
			return fmt.Errorf("chmod outside: %w", fscorpus.ErrEscaped)
		}
		if r.Outcome != "succeeded" {
			return errors.New(r.Outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chmod race: %v (%+v)", err, res)
	}
}

// TestArchiveSlipAndSpecialEntries extracts the zip-slip and tar-slip
// corpora: only the safe entries are written, nothing lands outside the
// destination root, escaping symlinks and hardlinks and the device node
// are refused, and the setuid bit is dropped.
func TestArchiveSlipAndSpecialEntries(t *testing.T) {
	f := newFixture(t)
	f.write("slip.zip", string(fscorpus.ZipSlip()))
	// tar-slip.tar is plain tar; the service extracts tar.gz.
	f.write("slip.tar.gz", string(gzipBytes(t, fscorpus.TarSlip())))
	before := f.snapshot()
	res := f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"slip.zip"}, Destination: "z"})
	if res.Outcome != "succeeded" && res.Outcome != "partial" {
		t.Fatalf("zip extract: %+v", res)
	}
	if got := f.readFile("z/safe.txt"); !strings.HasPrefix(got, "zip-slip payload for safe.txt") {
		t.Fatalf("safe.txt: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(f.root, "z"))
	if len(entries) != 1 {
		t.Errorf("zip extraction wrote %d entries, want only safe.txt: %v", len(entries), entries)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"slip.tar.gz"}, Destination: "t"})
	if res.Outcome == "failed" {
		t.Fatalf("tar extract: %+v", res)
	}
	for _, refused := range []string{"escape-abs", "escape-up", "hardlink-abs", "hardlink-up", "dev-null", "escape-abs/evil-through-abs-symlink.txt",
		"escape-up/evil-through-rel-symlink.txt"} {
		if f.exists("t/" + refused) {
			t.Errorf("%s was extracted", refused)
		}
	}
	if !f.exists("t/safe.txt") || !f.exists("t/setuid-shell") {
		t.Fatal("safe entries missing")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(f.root, "t", "setuid-shell"))
		if fi.Mode()&os.ModeSetuid != 0 {
			t.Error("setuid bit kept")
		}
	}
	if after := f.snapshot(); after != before {
		t.Fatalf("extraction wrote outside the root:\n%s\n---\n%s", before, after)
	}
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := newGzip(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDecompressionBombs: ratio, lying-size and gzip bombs stop with
// too_large after writing at most the budget; nested archives are not
// extracted recursively; too many entries are refused.
func TestDecompressionBombs(t *testing.T) {
	const bomb = 32 << 20
	f := newFixture(t, func(o *Options) { o.Limits.MaxArchiveEntries = 1000 })
	cases := map[string][]byte{
		"ratio.zip": fscorpus.ZipBomb(bomb),
		"lying.zip": fscorpus.ZipLyingSize(bomb),
		"bomb.tgz":  fscorpus.TarGzBomb(bomb),
	}
	for name, data := range cases {
		f.write(name, string(data))
		res := f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{name}, Destination: "out-" + name})
		// The lying-size member is cut off by the zip reader when it
		// inflates past its declared size (damaged archive); the others hit
		// the write budget. Either way the job fails.
		stopped := strings.Contains(res.Message, protocol.CodeTooLarge) ||
			(name == "lying.zip" && strings.Contains(res.Message, protocol.CodeUnsupportedFile))
		if res.Outcome != "failed" || !stopped {
			t.Errorf("%s: %+v, want failed too_large", name, res)
		}
		limit := max(f.svc.Limits().ExtractRatioFloor, int64(len(data))*f.svc.Limits().MaxExtractRatio)
		if n := dirSize(t, filepath.Join(f.root, "out-"+name)); n > limit {
			t.Errorf("%s: %d bytes written, budget %d", name, n, limit)
		}
	}
	f.write("nested.zip", string(fscorpus.ZipNested(fscorpus.ZipBomb(bomb/4), 4)))
	res := f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"nested.zip"}, Destination: "nested"})
	if res.Outcome != "succeeded" {
		t.Fatalf("nested: %+v", res)
	}
	if n := dirSize(t, filepath.Join(f.root, "nested")); n > 1<<20 {
		t.Errorf("nested archive expanded to %d bytes: inner archives must not be extracted", n)
	}
	f.write("many.zip", string(fscorpus.ZipManyEntries(1001)))
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: f.vol, Paths: []string{"many.zip"}, Destination: "many"})
	if res.Outcome != "failed" || !strings.Contains(res.Message, protocol.CodeTooLarge) {
		t.Errorf("many entries: %+v", res)
	}
	out, err := f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: f.vol, Operation: protocol.FileOpExtract, Paths: []string{"many.zip"}, Destination: "."})
	if code(err) != protocol.CodeTooLarge {
		t.Errorf("preview of too many entries: %+v %v", out.Impact, err)
	}
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// TestScopeResolutionRefusals: non-local and remote-backed volumes,
// Docker Manager's own volumes, the stacks volume, volumes outside the volume
// directory and stack directories outside the verified roots are refused.
func TestScopeResolutionRefusals(t *testing.T) {
	f := newFixture(t)
	for name, want := range map[string]string{
		"plugin": protocol.CodeUnsupportedVolume, "nfs": protocol.CodeUnsupportedVolume, "agentstate": protocol.CodeUnsupportedVolume,
		"managerdata": protocol.CodeUnsupportedVolume, "docker-manager_stacks": protocol.CodeUnsupportedVolume, "missing": protocol.CodeNotFound,
	} {
		_, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: protocol.FileScope{Kind: protocol.ScopeVolume, ID: name}, Path: "."})
		if code(err) != want {
			t.Errorf("volume %s: %v, want %s", name, err, want)
		}
	}
	for _, dir := range []string{slash(f.outside), slash(f.base), slash(filepath.Join(f.root))} {
		_, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: protocol.FileScope{Kind: protocol.ScopeStack, ID: "s", Dir: dir}, Path: "."})
		if code(err) != protocol.CodeForbiddenPath {
			t.Errorf("stack dir %s: %v, want forbidden_path", dir, err)
		}
	}
	// A project directory that is a symlink to elsewhere is refused.
	link := filepath.Join(filepath.Dir(f.stack), "linked")
	if err := os.Symlink(f.outside, link); err == nil {
		_, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: protocol.FileScope{Kind: protocol.ScopeStack, ID: "s", Dir: slash(link)}, Path: "."})
		if code(err) != protocol.CodeForbiddenPath {
			t.Errorf("symlinked stack dir: %v", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	// Before the storage check ran nothing is served.
	g := newFixture(t, func(o *Options) { o.Storage = nil })
	if _, err := g.svc.List(g.ctx, protocol.FilesListInput{Scope: g.vol, Path: "."}); code(err) != protocol.CodeUnsupportedVolume {
		t.Errorf("unverified storage: %v", err)
	}
}

// stackSymlink creates a symlink inside the stack's project directory,
// skipping the test where the platform cannot create symlinks.
func (f *fixture) stackSymlink(target, rel string) {
	f.t.Helper()
	if err := os.Symlink(filepath.FromSlash(target), filepath.Join(f.stack, filepath.FromSlash(rel))); err != nil {
		if runtime.GOOS == "windows" {
			f.t.Skipf("symlinks unavailable on this Windows host: %v", err)
		}
		f.t.Fatal(err)
	}
}

// zipMember is one member of zipOf: a symlink (link: content is the
// target) or a regular file.
type zipMember struct {
	name, content string
	link          bool
}

// zipOf builds a zip of the members, in order.
func zipOf(t *testing.T, members ...zipMember) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Store}
		mode := os.FileMode(0o644)
		if m.link {
			mode = os.ModeSymlink | 0o777
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestStackScopesFollowNoSymlinks (#180): in a stack's project directory
// no symlink is followed, even inside it, so a link cannot give a
// definition file a second name (d -> . makes d/.env the stack's .env,
// x -> .env makes x one): every operation through a symlinked directory
// is refused, a final symlink is never opened for content, and the link
// itself can still be listed, inspected and deleted.
func TestStackScopesFollowNoSymlinks(t *testing.T) {
	f := newFixture(t)
	const secret = "TOKEN=stack-definition-secret\n"
	for rel, content := range map[string]string{".env": secret, "compose.yaml": "services: {}\n", "sub/ok.txt": "ok\n"} {
		p := filepath.Join(f.stack, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.stackSymlink(".", "d")
	f.stackSymlink(".env", "x")
	f.stackSymlink("..", "sub/up")
	s := f.stk
	stackFile := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(f.stack, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	stackHas := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(f.stack, filepath.FromSlash(rel)))
		return err == nil
	}

	// Content reads through a symlinked directory or of a final symlink.
	for _, p := range []string{"d/.env", "x", "sub/up/.env", "d/sub/ok.txt"} {
		if out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: s, Path: p}); code(err) != protocol.CodeForbiddenPath {
			t.Errorf("read %s: %q %v, want forbidden_path", p, out.Data, err)
		}
		if b, err := f.download(protocol.FilesDownloadInput{Scope: s, Paths: []string{p}, Format: protocol.FormatRaw}); err == nil {
			t.Errorf("raw download %s: %q", p, b)
		}
	}
	if out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: s, Path: "sub/ok.txt"}); err != nil || string(out.Data) != "ok\n" {
		t.Errorf("read sub/ok.txt: %q %v", out.Data, err)
	}
	// Metadata (and an ETag derived from the content) never comes through
	// a symlinked directory; the link itself is shown as a link.
	if e, err := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: s, Path: "d/.env", ETag: true}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("stat d/.env: %+v %v", e, err)
	}
	if e, err := f.svc.Stat(f.ctx, protocol.FilesStatInput{Scope: s, Path: "x", ETag: true}); err != nil || e.Type != protocol.FileTypeSymlink || e.ETag != "" {
		t.Errorf("stat x: %+v %v", e, err)
	}
	if _, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: s, Path: "d"}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("list d: %v", err)
	}
	out, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: s, Path: ".", Hidden: true})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, e := range out.Entries {
		listed[e.Name] = e.Type
	}
	if listed["d"] != protocol.FileTypeSymlink || listed["x"] != protocol.FileTypeSymlink {
		t.Errorf("listing %v: the links must be listed as links", listed)
	}
	// Archives never hold a definition file's content under another name.
	for _, paths := range [][]string{{"d"}, {"x"}, {"sub"}, {"d/.env"}} {
		if b, err := f.download(protocol.FilesDownloadInput{Scope: s, Paths: paths, Format: protocol.FormatZip}); err == nil && bytes.Contains(b, []byte(secret)) {
			t.Errorf("zip of %v holds .env", paths)
		}
	}
	if _, err := f.svc.Preview(f.ctx, protocol.FilesPreviewInput{Scope: s, Operation: protocol.FileOpDelete, Paths: []string{"d/compose.yaml"}}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("delete preview through d: %v", err)
	}

	// Writes through a symlinked directory or onto a final symlink.
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: s, Path: "d/.env", Data: []byte("EVIL=1\n"), Overwrite: true}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("write d/.env: %v", err)
	}
	if _, err := f.svc.Write(f.ctx, protocol.FilesWriteInput{Scope: s, Path: "x", Data: []byte("EVIL=1\n"), Overwrite: true}); err == nil {
		t.Error("write x replaced or wrote through the link")
	}
	if _, err := f.svc.Mkdir(f.ctx, protocol.FilesMkdirInput{Scope: s, Path: "d/new", Type: protocol.FileTypeDir}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("mkdir d/new: %v", err)
	}
	if _, err := f.upload(protocol.FilesUploadInput{Scope: s, Dir: "d", Name: "compose.yaml", Size: 4, Conflict: protocol.ConflictOverwrite}, []byte("evil")); err == nil {
		t.Error("upload into d succeeded")
	}
	// Jobs: nothing is read, moved, deleted or changed through d.
	for _, job := range []struct {
		kind domain.JobKind
		in   protocol.FilesJobInput
		item string
	}{
		{jobspec.FilesCopy, protocol.FilesJobInput{Paths: []string{"d/.env"}, Destination: "sub"}, "d/.env"},
		{jobspec.FilesCopy, protocol.FilesJobInput{Paths: []string{"sub/ok.txt"}, Destination: "d", Name: "compose.yaml", Conflict: protocol.ConflictOverwrite}, "sub/ok.txt"},
		{jobspec.FilesMove, protocol.FilesJobInput{Paths: []string{"d/.env"}, Destination: "sub"}, "d/.env"},
		{jobspec.FilesMove, protocol.FilesJobInput{Paths: []string{"sub/ok.txt"}, Destination: "d", Name: ".env", Conflict: protocol.ConflictOverwrite}, "sub/ok.txt"},
		{jobspec.FilesDelete, protocol.FilesJobInput{Paths: []string{"d/compose.yaml"}}, "d/compose.yaml"},
		{jobspec.FilesMetadata, protocol.FilesJobInput{Paths: []string{"d/.env"}, Chmod: &protocol.ChmodSpec{Mode: 0o600}}, "d/.env"},
	} {
		job.in.Scope = s
		res := f.runJob(job.kind, job.in)
		if !f.itemFailed(res, job.item) && res.Outcome != "failed" {
			t.Errorf("%s %+v: %+v, want it refused", job.kind, job.in, res)
		}
	}
	res := f.runJob(jobspec.FilesArchive, protocol.FilesJobInput{Scope: s, Paths: []string{"d/.env"}, Destination: "a.zip", Format: protocol.FormatZip})
	if res.Outcome != "failed" || stackHas("a.zip") {
		t.Errorf("archive of d/.env: %+v", res)
	}
	if stackFile(".env") != secret || stackFile("compose.yaml") != "services: {}\n" || stackHas("sub/.env") || stackHas("new") {
		t.Fatal("an operation through a symlink reached the stack's definition")
	}
	// Deleting a link removes the link, never its target.
	res = f.runJob(jobspec.FilesDelete, protocol.FilesJobInput{Scope: s, Paths: []string{"x", "d"}})
	if res.Outcome != "succeeded" || stackHas("x") || stackHas("d") || stackFile(".env") != secret {
		t.Errorf("delete the links: %+v", res)
	}

	// Extraction: a link the archive creates is never followed by later
	// members, and a link target is judged from where the link lands.
	f.stackSymlink(".", "d")
	arc := zipOf(t,
		zipMember{name: "l", content: ".", link: true},
		zipMember{name: "l/compose.yaml", content: "services: {evil: {}}\n"},
		zipMember{name: "up", content: "../.env", link: true},
		zipMember{name: "out", content: "../../x", link: true},
	)
	if err := os.WriteFile(filepath.Join(f.stack, "a.zip"), arc, 0o644); err != nil {
		t.Fatal(err)
	}
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: s, Paths: []string{"a.zip"}, Destination: "tmp"})
	if !f.itemFailed(res, "l/compose.yaml") || stackHas("tmp/compose.yaml") || stackFile("compose.yaml") != "services: {}\n" {
		t.Errorf("extract through an extracted link: %+v", res)
	}
	if fi, err := os.Lstat(filepath.Join(f.stack, "tmp", "up")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("tmp/up -> ../.env stays inside the root and is extracted as a link: %v", err)
	}
	if stackHas("tmp/out") {
		t.Error("tmp/out -> ../../x leaves the root and must be refused")
	}
	if _, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: s, Path: "tmp/up"}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("read tmp/up: %v", err)
	}
	// Extracting into a symlinked directory is refused before anything is
	// written.
	res = f.runJob(jobspec.FilesExtract, protocol.FilesJobInput{Scope: s, Paths: []string{"a.zip"}, Destination: "d/tmp2"})
	if res.Outcome != "failed" || stackHas("tmp2") {
		t.Errorf("extract into d/tmp2: %+v", res)
	}
	if strings.Contains(f.logs.String(), "stack-definition-secret") {
		t.Fatal("the definition's content reached the agent log")
	}
}

// TestVolumeScopesStillFollowInsideLinks: volumes keep following symlinks
// that stay inside the root (only stack scopes refuse them).
func TestVolumeScopesStillFollowInsideLinks(t *testing.T) {
	f := newFixture(t)
	f.write("sub/a.txt", "a")
	f.symlink("sub", "d")
	if out, err := f.svc.Read(f.ctx, protocol.FilesReadInput{Scope: f.vol, Path: "d/a.txt"}); err != nil || string(out.Data) != "a" {
		t.Fatalf("read d/a.txt in a volume: %q %v", out.Data, err)
	}
	if _, err := f.svc.List(f.ctx, protocol.FilesListInput{Scope: f.vol, Path: "d"}); err != nil {
		t.Fatalf("list d in a volume: %v", err)
	}
}
