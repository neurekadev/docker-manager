package fsroot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// List reads one page of a directory listing.
func (s *Service) List(ctx context.Context, in protocol.FilesListInput) (protocol.FilesListOutput, error) {
	rel, err := cleanPath(in.Path)
	if err != nil {
		return protocol.FilesListOutput{}, err
	}
	switch in.Sort {
	case "", protocol.SortName, protocol.SortSize, protocol.SortModified, protocol.SortType:
	default:
		return protocol.FilesListOutput{}, fail(protocol.CodeInvalidFrame, "unknown sort %q", in.Sort)
	}
	limit := in.Limit
	if limit <= 0 || limit > protocol.MaxListPage {
		limit = protocol.MaxListPage
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return protocol.FilesListOutput{}, err
	}
	defer r.Close()
	dirInfo, err := r.root.Lstat(rel)
	if err != nil {
		return protocol.FilesListOutput{}, classify(err, rel)
	}
	if dirInfo.Mode()&fs.ModeSymlink != 0 {
		// A directory symlink inside the root is listed through its target
		// (os.Root keeps it inside); an escaping one is refused.
		if dirInfo, err = r.root.Stat(rel); err != nil {
			return protocol.FilesListOutput{}, classify(err, rel)
		}
	}
	if !dirInfo.IsDir() {
		return protocol.FilesListOutput{}, fail(protocol.CodeNotDirectory, "%s is not a directory", rel)
	}
	// Entries are resolved relative to the opened directory handle.
	sub, err := r.root.OpenRoot(rel)
	if err != nil {
		return protocol.FilesListOutput{}, classify(err, rel)
	}
	defer func() { _ = sub.Close() }()
	d, err := sub.Open(".")
	if err != nil {
		return protocol.FilesListOutput{}, classify(err, rel)
	}
	defer func() { _ = d.Close() }()
	out := protocol.FilesListOutput{Dir: entryOf(rel, dirInfo), Entries: []protocol.FileEntry{}}
	var all []protocol.FileEntry
	query := strings.ToLower(in.Query)
	scanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return protocol.FilesListOutput{}, classify(err, rel)
		}
		batch, err := d.ReadDir(1024)
		for _, de := range batch {
			scanned++
			name := de.Name()
			if !in.Hidden && strings.HasPrefix(name, ".") {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(name), query) {
				continue
			}
			fi, err := sub.Lstat(name)
			if err != nil {
				continue // removed meanwhile
			}
			e := entryOf(join(rel, name), fi)
			if e.Type == protocol.FileTypeSymlink {
				linkInfo(sub, name, &e)
			}
			all = append(all, e)
		}
		if scanned >= protocol.MaxListScan {
			out.Truncated = true
			break
		}
		if errors.Is(err, io.EOF) || len(batch) == 0 {
			break
		}
		if err != nil {
			return protocol.FilesListOutput{}, classify(err, rel)
		}
	}
	less := sorter(in.Sort)
	slices.SortFunc(all, func(a, b protocol.FileEntry) int {
		c := less(a, b)
		if c == 0 {
			c = strings.Compare(a.Name, b.Name)
		}
		if in.Desc {
			c = -c
		}
		return c
	})
	out.Total = len(all)
	start := 0
	if in.After != nil {
		pivot := protocol.FileEntry{Name: in.After.Name}
		switch in.Sort {
		case protocol.SortSize:
			pivot.Size, _ = strconv.ParseInt(in.After.Key, 10, 64)
		case protocol.SortModified:
			n, _ := strconv.ParseInt(in.After.Key, 10, 64)
			pivot.ModTime = unixNano(n)
		case protocol.SortType:
			pivot.Type = in.After.Key
		}
		start = len(all)
		for i, e := range all {
			c := less(e, pivot)
			if c == 0 {
				c = strings.Compare(e.Name, pivot.Name)
			}
			if in.Desc {
				c = -c
			}
			if c > 0 {
				start = i
				break
			}
		}
	}
	end := min(start+limit, len(all))
	out.Entries = append(out.Entries, all[start:end]...)
	if end < len(all) {
		last := all[end-1]
		c := &protocol.FilesListCursor{Name: last.Name}
		switch in.Sort {
		case protocol.SortSize:
			c.Key = strconv.FormatInt(last.Size, 10)
		case protocol.SortModified:
			c.Key = strconv.FormatInt(last.ModTime.UnixNano(), 10)
		case protocol.SortType:
			c.Key = last.Type
		}
		out.Next = c
	}
	return out, nil
}

func sorter(key string) func(a, b protocol.FileEntry) int {
	switch key {
	case protocol.SortSize:
		return func(a, b protocol.FileEntry) int { return cmpInt(a.Size, b.Size) }
	case protocol.SortModified:
		return func(a, b protocol.FileEntry) int { return a.ModTime.Compare(b.ModTime) }
	case protocol.SortType:
		// Directories first, then files, symlinks, others.
		rank := map[string]int{protocol.FileTypeDir: 0, protocol.FileTypeFile: 1, protocol.FileTypeSymlink: 2, protocol.FileTypeOther: 3}
		return func(a, b protocol.FileEntry) int { return cmpInt(int64(rank[a.Type]), int64(rank[b.Type])) }
	}
	return func(protocol.FileEntry, protocol.FileEntry) int { return 0 }
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// join joins a root-relative directory and a name.
func join(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// Read returns up to MaxInlineContent bytes of a regular file.
func (s *Service) Read(ctx context.Context, in protocol.FilesReadInput) (protocol.FilesReadOutput, error) {
	rel, err := cleanPath(in.Path)
	if err != nil {
		return protocol.FilesReadOutput{}, err
	}
	if in.Offset < 0 || in.Length < 0 {
		return protocol.FilesReadOutput{}, fail(protocol.CodeInvalidFrame, "offset and length must not be negative")
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return protocol.FilesReadOutput{}, err
	}
	defer r.Close()
	f, fi, err := openRegular(r.root, rel)
	if err != nil {
		return protocol.FilesReadOutput{}, err
	}
	defer func() { _ = f.Close() }()
	e := entryOf(rel, fi)
	e.Links = max(e.Links, openLinks(f, fi))
	if e.ETag, err = fileETag(ctx, f, fi); err != nil {
		return protocol.FilesReadOutput{}, classify(err, rel)
	}
	n := in.Length
	if n == 0 || n > protocol.MaxInlineContent {
		n = protocol.MaxInlineContent
	}
	if _, err := f.Seek(in.Offset, io.SeekStart); err != nil {
		return protocol.FilesReadOutput{}, classify(err, rel)
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return protocol.FilesReadOutput{}, classify(err, rel)
	}
	buf = buf[:got]
	truncated := in.Offset+int64(got) < fi.Size()
	return protocol.FilesReadOutput{Entry: e, Data: buf, Binary: isBinary(buf, truncated), Truncated: truncated}, nil
}

// isBinary reports whether b is not UTF-8 text: a NUL byte or invalid
// UTF-8 (an incomplete sequence cut off at the end of a truncated read is
// tolerated).
func isBinary(b []byte, truncated bool) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return true
	}
	if utf8.Valid(b) {
		return false
	}
	if truncated {
		for cut := 1; cut <= 3 && cut <= len(b); cut++ {
			if utf8.Valid(b[:len(b)-cut]) {
				return false
			}
		}
	}
	return true
}

// target is the parent directory handle and name of a file to create or
// replace.
type target struct {
	dir  *os.Root
	name string
	rel  string
}

func (t *target) Close() { _ = t.dir.Close() }

// openTarget opens the parent directory of rel (which must exist).
func openTarget(r *scopeRoot, rel string) (*target, error) {
	if rel == "." {
		return nil, fail(protocol.CodeIsDirectory, "the scope root cannot be replaced")
	}
	name := path.Base(rel)
	if !protocol.ValidFileName(name) {
		return nil, fail(protocol.CodeForbiddenPath, "invalid file name")
	}
	dir, err := r.root.OpenRoot(path.Dir(rel))
	if err != nil {
		return nil, classify(err, path.Dir(rel))
	}
	return &target{dir: dir, name: name, rel: rel}, nil
}

// tempName returns a fresh hidden temporary name in a directory.
func tempName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return ".docker-manager-" + hex.EncodeToString(b[:]) + ".tmp"
}

// precondition says which existing file a replacement may overwrite.
type precondition struct {
	ifMatch    []string
	createOnly bool
	overwrite  bool
}

// current is the entry a replacement found and checked.
type current struct {
	info fs.FileInfo
}

// check evaluates the precondition against the target's current entry.
func (s *Service) check(ctx context.Context, t *target, p precondition) (*current, error) {
	fi, err := t.dir.Lstat(t.name)
	if errors.Is(err, fs.ErrNotExist) {
		if len(p.ifMatch) > 0 {
			return nil, fail(protocol.CodeConflict, "%s no longer exists", t.rel)
		}
		return &current{}, nil
	}
	if err != nil {
		return nil, classify(err, t.rel)
	}
	switch {
	case p.createOnly:
		return nil, fail(protocol.CodeAlreadyExists, "%s already exists", t.rel)
	case fi.IsDir():
		return nil, fail(protocol.CodeIsDirectory, "%s is a directory", t.rel)
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fail(protocol.CodeUnsupportedFile, "%s is a symbolic link; edit its target instead", t.rel)
	case !fi.Mode().IsRegular():
		return nil, fail(protocol.CodeUnsupportedFile, "%s is not a regular file", t.rel)
	}
	if len(p.ifMatch) > 0 && !slices.Contains(p.ifMatch, "*") {
		f, ofi, err := openChecked(t.dir, t.name, fi, os.O_RDONLY)
		if err != nil {
			return nil, err
		}
		tag, err := fileETag(ctx, f, ofi)
		_ = f.Close()
		if err != nil {
			return nil, classify(err, t.rel)
		}
		if tag == "" || !slices.Contains(p.ifMatch, tag) {
			return nil, fail(protocol.CodeConflict, "%s was changed since it was read", t.rel)
		}
	} else if len(p.ifMatch) == 0 && !p.overwrite {
		return nil, fail(protocol.CodeAlreadyExists, "%s already exists", t.rel)
	}
	return &current{info: fi}, nil
}

// unchanged re-checks, right before the rename, that the entry is still
// the one check saw.
func (c *current) unchanged(t *target) bool {
	fi, err := t.dir.Lstat(t.name)
	if c.info == nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return err == nil && os.SameFile(fi, c.info) && fi.Size() == c.info.Size() && fi.ModTime().Equal(c.info.ModTime())
}

// commit moves a fully written temporary file into place: no-clobber via
// a hard link when the target must not exist (keep_both, create-only),
// otherwise a rename after re-checking the target.
func (s *Service) commit(t *target, tmp string, cur *current, noClobber bool) error {
	if noClobber {
		err := t.dir.Link(tmp, t.name)
		if err == nil {
			_ = t.dir.Remove(tmp)
			return nil
		}
		if errors.Is(err, fs.ErrExist) {
			return fail(protocol.CodeAlreadyExists, "%s already exists", t.rel)
		}
		// Filesystems without hard links: check, then rename.
		if _, lerr := t.dir.Lstat(t.name); lerr == nil {
			return fail(protocol.CodeAlreadyExists, "%s already exists", t.rel)
		}
	} else if !cur.unchanged(t) {
		return fail(protocol.CodeConflict, "%s was changed during the write", t.rel)
	}
	if err := t.dir.Rename(tmp, t.name); err != nil {
		return classify(err, t.rel)
	}
	return nil
}

// writeTemp writes r (at most limit bytes) to a new temporary file in t's
// directory, applying the replaced file's mode and owner (or 0644), and
// returns its name, size and SHA-256.
func (s *Service) writeTemp(ctx context.Context, t *target, r io.Reader, limit int64, cur *current) (string, int64, []byte, error) {
	tmp := tempName()
	f, err := t.dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, nil, classify(err, t.rel)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = t.dir.Remove(tmp)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), ctxReader{ctx: ctx, r: io.LimitReader(r, limit+1)})
	if err != nil {
		return "", 0, nil, classify(err, t.rel)
	}
	if n > limit {
		return "", 0, nil, fail(protocol.CodeTooLarge, "the content exceeds %d bytes", limit)
	}
	mode := os.FileMode(0o644)
	if cur != nil && cur.info != nil {
		mode = cur.info.Mode().Perm()
		if uid, gid, _ := ownerOf(cur.info); uid != 0 || gid != 0 {
			_ = f.Chown(int(uid), int(gid))
		}
	}
	if err := f.Chmod(mode); err != nil {
		return "", 0, nil, classify(err, t.rel)
	}
	if err := f.Sync(); err != nil {
		return "", 0, nil, classify(err, t.rel)
	}
	if err := f.Close(); err != nil {
		return "", 0, nil, classify(err, t.rel)
	}
	ok = true
	return tmp, n, h.Sum(nil), nil
}

// Write replaces (or creates) a regular file atomically.
func (s *Service) Write(ctx context.Context, in protocol.FilesWriteInput) (protocol.FileEntry, error) {
	rel, err := cleanPath(in.Path)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	if n := boolCount(len(in.IfMatch) > 0, in.CreateOnly, in.Overwrite); n != 1 {
		return protocol.FileEntry{}, fail(protocol.CodeInvalidFrame, "exactly one of ifMatch, createOnly and overwrite is required")
	}
	if len(in.Data) > protocol.MaxInlineContent {
		return protocol.FileEntry{}, fail(protocol.CodeTooLarge, "inline content exceeds %d bytes; upload instead", protocol.MaxInlineContent)
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	defer r.Close()
	t, err := openTarget(r, rel)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	defer t.Close()
	unlock := s.lock(r.key + "\x00" + rel)
	defer unlock()
	cur, err := s.check(ctx, t, precondition{ifMatch: in.IfMatch, createOnly: in.CreateOnly, overwrite: in.Overwrite})
	if err != nil {
		return protocol.FileEntry{}, err
	}
	tmp, _, sum, err := s.writeTemp(ctx, t, bytes.NewReader(in.Data), protocol.MaxInlineContent, cur)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	if err := s.commit(t, tmp, cur, in.CreateOnly); err != nil {
		_ = t.dir.Remove(tmp)
		return protocol.FileEntry{}, err
	}
	s.invalidate(in.Scope, rel)
	return s.entryWithSum(t, rel, sum)
}

// entryWithSum stats a just-written file and derives its ETag from the
// content hash computed while writing.
func (s *Service) entryWithSum(t *target, rel string, sum []byte) (protocol.FileEntry, error) {
	fi, err := t.dir.Lstat(t.name)
	if err != nil {
		return protocol.FileEntry{}, classify(err, rel)
	}
	e := entryOf(rel, fi)
	if fi.Mode().IsRegular() && fi.Size() <= protocol.MaxETagSize {
		e.ETag = ComputeETag(sum, fi.Size(), fi.ModTime())
	}
	return e, nil
}

func boolCount(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// Mkdir creates a directory or a new regular file.
func (s *Service) Mkdir(ctx context.Context, in protocol.FilesMkdirInput) (protocol.FileEntry, error) {
	rel, err := cleanPath(in.Path)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	defer r.Close()
	t, err := openTarget(r, rel)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	defer t.Close()
	switch in.Type {
	case protocol.FileTypeDir:
		if len(in.Data) > 0 {
			return protocol.FileEntry{}, fail(protocol.CodeInvalidFrame, "a directory has no content")
		}
		if err := t.dir.Mkdir(t.name, 0o755); err != nil {
			return protocol.FileEntry{}, classify(err, rel)
		}
		s.invalidate(in.Scope, rel)
		fi, err := t.dir.Lstat(t.name)
		if err != nil {
			return protocol.FileEntry{}, classify(err, rel)
		}
		return entryOf(rel, fi), nil
	case protocol.FileTypeFile:
		return s.Write(ctx, protocol.FilesWriteInput{Scope: in.Scope, Path: rel, Data: in.Data, CreateOnly: true})
	}
	return protocol.FileEntry{}, fail(protocol.CodeInvalidFrame, "type must be dir or file")
}
