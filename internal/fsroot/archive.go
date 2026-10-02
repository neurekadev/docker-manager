package fsroot

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Archive formats detected by content.
const (
	kindZip   = "zip"
	kindTarGz = "tar.gz"
)

// sniff detects a zip or gzip-compressed tar archive by its magic bytes.
func sniff(f io.ReaderAt) (string, error) {
	var b [4]byte
	if _, err := f.ReadAt(b[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	switch {
	case bytes.Equal(b[:], []byte("PK\x03\x04")), bytes.Equal(b[:], []byte("PK\x05\x06")):
		return kindZip, nil
	case b[0] == 0x1f && b[1] == 0x8b:
		return kindTarGz, nil
	}
	return "", fail(protocol.CodeUnsupportedFile, "not a zip or tar.gz archive")
}

// entryName validates an archive member name and returns it as a clean
// root-relative path. Refused: empty names, absolute paths, drive letters,
// backslashes, NUL/control characters, . and .. segments.
func entryName(raw string) (string, bool) {
	n := strings.TrimSuffix(raw, "/")
	n = strings.TrimPrefix(n, "./")
	if n == "" || strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\\x00") {
		return "", false
	}
	first, _, _ := strings.Cut(n, "/")
	if len(first) == 2 && first[1] == ':' {
		return "", false // C:/... (Windows drive)
	}
	rel, ok := protocol.CleanRelativePath(n)
	if !ok || rel == "." || rel != n {
		return "", false
	}
	return rel, true
}

// linkTargetInside reports whether a symlink target stored at the
// root-relative path at resolves inside the root (relative, not climbing
// out). at is where the link lands: for an extraction the destination
// directory joined with the member name, so a link is judged from its
// final location, not from the archive's top.
func linkTargetInside(at, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\\x00") {
		return false
	}
	if first, _, _ := strings.Cut(target, "/"); len(first) == 2 && first[1] == ':' {
		return false
	}
	j := path.Join(path.Dir(at), target)
	return j != ".." && !strings.HasPrefix(j, "../")
}

// archiveEntry is one member of an archive being scanned or extracted.
type archiveEntry struct {
	raw  string
	name string // "" when the name is refused
	typ  string // file, dir, symlink, hardlink, other
	size int64  // declared size (never trusted for limits)
	mode fs.FileMode
	link string // symlink or hardlink target
	open func() (io.Reader, error)
}

const typeHardlink = "hardlink"

// scanArchive calls fn for every member of the archive at rel, in order.
func (s *Service) scanArchive(ctx context.Context, r *scopeRoot, lim Limits, rel string, fn func(archiveEntry) error) error {
	f, fi, err := openRegular(r, rel)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	kind, err := sniff(f)
	if err != nil {
		return err
	}
	count := 0
	next := func(e archiveEntry) error {
		if err := ctx.Err(); err != nil {
			return classify(err, rel)
		}
		if count++; count > lim.MaxArchiveEntries {
			return fail(protocol.CodeTooLarge, "the archive has more than %d entries", lim.MaxArchiveEntries)
		}
		e.name, _ = entryName(e.raw)
		return fn(e)
	}
	switch kind {
	case kindZip:
		zr, err := zip.NewReader(f, fi.Size())
		if err != nil {
			return fail(protocol.CodeUnsupportedFile, "the zip archive is damaged")
		}
		if len(zr.File) > lim.MaxArchiveEntries {
			return fail(protocol.CodeTooLarge, "the archive has more than %d entries", lim.MaxArchiveEntries)
		}
		for _, zf := range zr.File {
			e := archiveEntry{raw: zf.Name, size: int64(zf.UncompressedSize64), mode: zf.Mode()} //nolint:gosec // G115: declared size, display only
			switch {
			case zf.Mode()&fs.ModeSymlink != 0:
				e.typ = protocol.FileTypeSymlink
				rc, err := zf.Open()
				if err != nil {
					return fail(protocol.CodeUnsupportedFile, "the zip archive is damaged")
				}
				b, err := io.ReadAll(io.LimitReader(rc, 4097))
				_ = rc.Close()
				if err != nil || len(b) > 4096 {
					e.typ = protocol.FileTypeOther
				}
				e.link = string(b)
			case zf.Mode().IsDir() || strings.HasSuffix(zf.Name, "/"):
				e.typ = protocol.FileTypeDir
			case zf.Mode().IsRegular():
				e.typ = protocol.FileTypeFile
				e.open = func() (io.Reader, error) { return zf.Open() }
			default:
				e.typ = protocol.FileTypeOther
			}
			if err := next(e); err != nil {
				return err
			}
		}
	case kindTarGz:
		gz, err := gzip.NewReader(bufio.NewReader(f))
		if err != nil {
			return fail(protocol.CodeUnsupportedFile, "the gzip stream is damaged")
		}
		gz.Multistream(false)
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fail(protocol.CodeUnsupportedFile, "the tar archive is damaged")
			}
			e := archiveEntry{raw: h.Name, size: h.Size, mode: fs.FileMode(h.Mode & 0o777), link: h.Linkname} //nolint:gosec // G115: masked
			switch h.Typeflag {
			case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA is still found in old archives
				e.typ = protocol.FileTypeFile
				e.open = func() (io.Reader, error) { return tr, nil }
			case tar.TypeDir:
				e.typ = protocol.FileTypeDir
			case tar.TypeSymlink:
				e.typ = protocol.FileTypeSymlink
			case tar.TypeLink:
				e.typ = typeHardlink
			case tar.TypeXGlobalHeader:
				continue
			default:
				e.typ = protocol.FileTypeOther
			}
			if err := next(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// bound shortens a (refused) archive member name for a job item.
func bound(s string) string {
	s = strings.ToValidUTF8(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s), "?")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// budget bounds the bytes an extraction writes.
type budget struct {
	left int64
	max  int64
}

func (b *budget) Write(p []byte) (int, error) {
	if int64(len(p)) > b.left {
		return 0, fail(protocol.CodeTooLarge, "the archive expands beyond %s (decompression bomb?); extraction stopped", humanize.Bytes(b.max))
	}
	b.left -= int64(len(p))
	return len(p), nil
}

// extractReport receives per-entry outcomes (names only).
type extractReport func(name, status, message string)

// extract unpacks the archive at archiveRel into destRel. Each entry is
// validated; refused entries are reported and skipped. Limits (entries,
// bytes written, expansion ratio) stop the whole extraction with
// too_large. The conflict policy applies per entry.
func (s *Service) extract(ctx context.Context, r *scopeRoot, lim Limits, archiveRel, destRel, policy string, report extractReport) ([]string, error) {
	if err := r.checkPath(archiveRel, true); err != nil {
		return nil, err
	}
	st, err := r.root.Lstat(archiveRel)
	if err != nil {
		return nil, classify(err, archiveRel)
	}
	limit := max(lim.ExtractRatioFloor, st.Size()*lim.MaxExtractRatio)
	limit = min(limit, lim.MaxExtractBytes)
	b := &budget{left: limit, max: limit}
	if err := r.mkdirAll(destRel); err != nil {
		return nil, err
	}
	// Regular files this extraction wrote (hard link targets).
	written := map[string]string{}
	// Refused links: nothing is written "through" their names either.
	refused := map[string]bool{}
	underRefused := func(name string) bool {
		for p := name; p != "." && p != "/"; p = path.Dir(p) {
			if refused[p] {
				return true
			}
		}
		return false
	}
	var changed []string
	err = s.scanArchive(ctx, r, lim, archiveRel, func(e archiveEntry) error {
		if e.name == "" {
			report(bound(e.raw), "skipped", "refused: unsafe entry name")
			return nil
		}
		if underRefused(e.name) {
			report(e.name, "skipped", "refused: below a refused link")
			return nil
		}
		dest := join(destRel, e.name)
		switch e.typ {
		case protocol.FileTypeDir:
			if err := r.mkdirAll(dest); err != nil {
				report(e.name, "failed", codeOf(err))
			}
			return nil
		case protocol.FileTypeSymlink:
			if !linkTargetInside(dest, e.link) {
				refused[e.name] = true
				report(e.name, "skipped", "refused: symlink target leaves the root")
				return nil
			}
		case typeHardlink:
			tn, ok := entryName(e.link)
			if !ok || written[tn] == "" {
				refused[e.name] = true
				report(e.name, "skipped", "refused: hard link to a file outside this archive")
				return nil
			}
		case protocol.FileTypeFile:
		default:
			refused[e.name] = true
			report(e.name, "skipped", "refused: special file")
			return nil
		}
		if err := r.mkdirAll(path.Dir(dest)); err != nil {
			report(e.name, "failed", codeOf(err))
			return nil
		}
		t, err := openTarget(r, dest)
		if err != nil {
			report(e.name, "failed", codeOf(err))
			return nil
		}
		defer t.Close()
		name, cur, skip, err := s.resolveConflict(ctx, t, policy)
		if err != nil {
			report(e.name, "failed", codeOf(err))
			return nil
		}
		if skip {
			report(e.name, "skipped", "exists")
			return nil
		}
		if e.typ == protocol.FileTypeSymlink {
			if cur.info != nil {
				if err := t.dir.Remove(name); err != nil {
					report(e.name, "failed", codeOf(classify(err, e.name)))
					return nil
				}
			}
			if err := t.dir.Symlink(e.link, name); err != nil {
				report(e.name, "failed", codeOf(classify(err, e.name)))
				return nil
			}
			changed = append(changed, join(path.Dir(dest), name))
			return nil
		}
		var src io.Reader
		switch e.typ {
		case typeHardlink:
			// Extracted as a copy of the earlier member (a hard link would
			// give both names two links, and their content is not served).
			tn, _ := entryName(e.link)
			hf, _, err := openRegular(r, written[tn])
			if err != nil {
				report(e.name, "failed", codeOf(err))
				return nil
			}
			defer func() { _ = hf.Close() }()
			src = hf
		default:
			rd, err := e.open()
			if err != nil {
				return fail(protocol.CodeUnsupportedFile, "the archive is damaged")
			}
			src = rd
		}
		tt := &target{dir: t.dir, name: name, rel: join(path.Dir(dest), name)}
		tmp, _, _, err := s.writeTemp(ctx, tt, io.TeeReader(src, b), lim.MaxExtractBytes, nil)
		if err != nil {
			if codeOf(err) == protocol.CodeTooLarge {
				return err // the budget: stop everything
			}
			if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fail(protocol.CodeUnsupportedFile, "the archive is damaged")
			}
			report(e.name, "failed", codeOf(err))
			return nil
		}
		// Permission bits only: no setuid/setgid/sticky from archives.
		if e.mode.Perm() != 0 {
			_ = t.dir.Chmod(tmp, e.mode.Perm())
		}
		if err := s.commit(tt, tmp, cur, cur.info == nil); err != nil {
			_ = t.dir.Remove(tmp)
			report(e.name, "failed", codeOf(err))
			return nil
		}
		written[e.name] = tt.rel
		changed = append(changed, tt.rel)
		return nil
	})
	return changed, err
}

// resolveConflict applies a conflict policy to a target: the name to write
// (keep_both picks "name (n).ext"), the entry being replaced, or skip.
func (s *Service) resolveConflict(ctx context.Context, t *target, policy string) (string, *current, bool, error) {
	fi, err := t.dir.Lstat(t.name)
	if errors.Is(err, fs.ErrNotExist) {
		return t.name, &current{}, false, nil
	}
	if err != nil {
		return "", nil, false, classify(err, t.rel)
	}
	switch policy {
	case protocol.ConflictSkip:
		return "", nil, true, nil
	case protocol.ConflictKeepBoth:
		n, err := freeName(t.dir, t.name)
		return n, &current{}, false, err
	case protocol.ConflictOverwrite:
		if fi.IsDir() {
			return "", nil, false, fail(protocol.CodeIsDirectory, "%s is a directory", t.rel)
		}
		if !fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
			return "", nil, false, fail(protocol.CodeUnsupportedFile, "%s is not a regular file", t.rel)
		}
		return t.name, &current{info: fi}, false, nil
	}
	_ = ctx
	return "", nil, false, fail(protocol.CodeAlreadyExists, "%s already exists", t.rel)
}

// freeName returns "name (n).ext" not present in dir.
func freeName(dir *os.Root, name string) (string, error) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == name { // ".env": the whole name is the stem
		stem, ext = name, ""
	}
	for i := 1; i <= 999; i++ {
		n := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if len(n) > 255 {
			break
		}
		if _, err := dir.Lstat(n); errors.Is(err, fs.ErrNotExist) {
			return n, nil
		}
	}
	return "", fail(protocol.CodeAlreadyExists, "no free name for %s", name)
}

// archiveWriter writes zip or tar.gz members.
type archiveWriter interface {
	dir(name string, fi fs.FileInfo) error
	file(name string, fi fs.FileInfo, r io.Reader) error
	symlink(name, target string, fi fs.FileInfo) error
	Close() error
}

type zipWriter struct{ w *zip.Writer }

func (z zipWriter) header(name string, fi fs.FileInfo) *zip.FileHeader {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fi.ModTime().UTC()}
	h.SetMode(fi.Mode())
	return h
}

func (z zipWriter) dir(name string, fi fs.FileInfo) error {
	h := z.header(name+"/", fi)
	h.Method = zip.Store
	_, err := z.w.CreateHeader(h)
	return err
}

func (z zipWriter) file(name string, fi fs.FileInfo, r io.Reader) error {
	w, err := z.w.CreateHeader(z.header(name, fi))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

func (z zipWriter) symlink(name, target string, fi fs.FileInfo) error {
	h := z.header(name, fi)
	h.Method = zip.Store
	w, err := z.w.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, target)
	return err
}

func (z zipWriter) Close() error { return z.w.Close() }

type tarWriter struct {
	gz *gzip.Writer
	w  *tar.Writer
}

func (t tarWriter) header(name string, fi fs.FileInfo) *tar.Header {
	uid, gid, _ := ownerOf(fi)
	return &tar.Header{Name: name, Mode: int64(modeBits(fi.Mode()) & 0o777), ModTime: fi.ModTime().UTC().Truncate(time.Second),
		Uid: int(uid), Gid: int(gid), Format: tar.FormatPAX}
}

func (t tarWriter) dir(name string, fi fs.FileInfo) error {
	h := t.header(name+"/", fi)
	h.Typeflag = tar.TypeDir
	return t.w.WriteHeader(h)
}

func (t tarWriter) file(name string, fi fs.FileInfo, r io.Reader) error {
	h := t.header(name, fi)
	h.Typeflag, h.Size = tar.TypeReg, fi.Size()
	if err := t.w.WriteHeader(h); err != nil {
		return err
	}
	// Exactly Size bytes (the file may have grown or shrunk meanwhile).
	n, err := io.Copy(t.w, io.LimitReader(r, fi.Size()))
	if err == nil && n < fi.Size() {
		err = fmt.Errorf("file shrank while archiving")
	}
	return err
}

func (t tarWriter) symlink(name, target string, fi fs.FileInfo) error {
	h := t.header(name, fi)
	h.Typeflag, h.Linkname = tar.TypeSymlink, target
	return t.w.WriteHeader(h)
}

func (t tarWriter) Close() error {
	return errors.Join(t.w.Close(), t.gz.Close())
}

func newArchiveWriter(format string, w io.Writer) (archiveWriter, error) {
	switch format {
	case protocol.FormatZip:
		return zipWriter{w: zip.NewWriter(w)}, nil
	case protocol.FormatTarGz:
		gz := gzip.NewWriter(w)
		return tarWriter{gz: gz, w: tar.NewWriter(gz)}, nil
	}
	return nil, fail(protocol.CodeInvalidFrame, "archive format must be zip or tar.gz")
}

// countingWriter bounds the bytes written to w.
type countingWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, fail(protocol.CodeTooLarge, "the archive exceeds %s", humanize.Bytes(c.limit))
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeArchive writes the paths (recursively, never following symlinks)
// as a zip or tar.gz archive to w. Symlinks are stored only when they
// resolve inside the root; escaping symlinks, hard-linked and special
// files are skipped and listed in a final DOCKER-MANAGER-SKIPPED.txt member.
// skip, when set, excludes one root-relative path (the archive being
// written into the root).
func (s *Service) writeArchive(ctx context.Context, r *scopeRoot, lim Limits, paths []string, format string, w io.Writer, skip string) (int, error) {
	cw := &countingWriter{w: w, limit: lim.MaxDownload}
	aw, err := newArchiveWriter(format, cw)
	if err != nil {
		return 0, err
	}
	var skipped []string
	entries := 0
	for _, p := range paths {
		prefix := ""
		if p != "." {
			prefix = path.Base(p)
		}
		err := walk(ctx, r, p, true, s.limits.MaxWalk, func(rel string, fi fs.FileInfo) error {
			if rel == skip {
				return nil
			}
			name := prefix
			if rel != p {
				name = join(prefix, strings.TrimPrefix(rel, p+"/"))
				if p == "." {
					name = rel
				}
			}
			if name == "" || name == "." {
				return nil // the root itself
			}
			if entries++; entries > lim.MaxArchiveEntries {
				return fail(protocol.CodeTooLarge, "more than %d entries", lim.MaxArchiveEntries)
			}
			switch typeOf(fi.Mode()) {
			case protocol.FileTypeDir:
				return aw.dir(name, fi)
			case protocol.FileTypeSymlink:
				var e protocol.FileEntry
				linkInfo(r.root, rel, &e)
				if e.LinkStatus != protocol.LinkInside || !linkTargetInside(name, e.LinkTarget) {
					skipped = append(skipped, name+"  (symlink leaves the root or the archive)")
					return nil
				}
				return aw.symlink(name, e.LinkTarget, fi)
			case protocol.FileTypeFile:
				f, ofi, err := openChecked(r.root, rel, fi, os.O_RDONLY)
				if err != nil {
					skipped = append(skipped, name+"  (changed during the download)")
					return nil
				}
				defer func() { _ = f.Close() }()
				if openLinks(f, ofi) > 1 {
					skipped = append(skipped, name+"  (several hard links)")
					return nil
				}
				return aw.file(name, ofi, ctxReader{ctx: ctx, r: f})
			}
			skipped = append(skipped, name+"  (special file)")
			return nil
		})
		if errors.Is(err, errWalkLimit) {
			return entries, fail(protocol.CodeTooLarge, "more than %d entries", s.limits.MaxWalk)
		}
		if err != nil {
			return entries, err
		}
	}
	if len(skipped) > 0 {
		list := []byte("Docker Manager left out these entries:\n" + strings.Join(skipped, "\n") + "\n")
		fi := memInfo{name: protocol.SkippedListName, size: int64(len(list)), mod: s.opts.Clock.Now()}
		if err := aw.file(protocol.SkippedListName, fi, bytes.NewReader(list)); err != nil {
			return entries, err
		}
	}
	return entries, aw.Close()
}

// memInfo is the FileInfo of a generated archive member.
type memInfo struct {
	name string
	size int64
	mod  time.Time
}

func (m memInfo) Name() string       { return m.name }
func (m memInfo) Size() int64        { return m.size }
func (m memInfo) Mode() fs.FileMode  { return 0o644 }
func (m memInfo) ModTime() time.Time { return m.mod }
func (m memInfo) IsDir() bool        { return false }
func (m memInfo) Sys() any           { return nil }
