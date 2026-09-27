// Package fsroot is the scoped file service shared by the agent (stack
// project directories and local volumes, #15) and the manager (template
// drafts): directory listings, metadata, bounded reads and atomic writes,
// uploads and downloads (single files and zip/tar.gz archives), conflict
// and impact previews, and the files.* job executors (archive, extract,
// copy, move, delete, chmod/chown). Every operation works inside one root,
// the directory Options.Resolve returns for a scope; the caller decides
// which directories it serves (internal/agent/files for stacks and
// volumes).
//
// Containment: every file access goes through an *os.Root opened on the
// scope directory. os.Root resolves each path component relative to an
// open directory handle (openat with O_NOFOLLOW per component on Linux),
// follows symlinks only while they stay beneath the root and refuses
// absolute or escaping targets, so a path, a symlink or a directory
// swapped for a symlink between a check and its use (TOCTOU) can never
// reach outside the root. On top of that the service
//
//   - validates every path lexically (protocol.CleanRelativePath) and
//     every new name (protocol.ValidFileName);
//   - never follows symlinks in recursive operations (entries are
//     Lstat'ed; a directory is descended only after the opened handle is
//     confirmed to be the same directory that was checked, os.SameFile);
//   - refuses content access (read, download, archive, copy) to regular
//     files with more than one hard link, whose other names may lie
//     outside the root, and to device files, FIFOs and sockets;
//   - re-checks the target right before every mutation (write/upload
//     compare the current entry, then rename a fully written temporary
//     file into place; chmod/chown use the opened handle, fchmod/fchown);
//   - validates every archive entry (zip-slip, tar-slip, symlink and
//     hardlink targets, special files, setuid bits) and counts the bytes it
//     actually writes against size, ratio and entry limits.
//
// Residual risks (documented in docs/internal/api/files.md): files are replaced by
// rename, so a concurrent writer outside Docker Manager can still change a file
// between the precondition check and the rename (the window is a few
// syscalls); bind mounts inside a volume are traversed like directories
// (os.Root does not stop at mount points); os.Root on Linux does not use
// openat2 RESOLVE_BENEATH but an equivalent per-component walk.
//
// Errors are *protocol.Error values that name root-relative paths only.
// File contents and names are never logged.
package fsroot

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Limits bound the service's work.
type Limits struct {
	// MaxUpload bounds one uploaded file (default 2 GiB).
	MaxUpload int64
	// MaxDownload bounds one download or created archive (default 10 GiB).
	MaxDownload int64
	// MaxArchiveEntries bounds the entries of an archive read or written
	// (default 100 000).
	MaxArchiveEntries int
	// MaxExtractBytes bounds the bytes one extraction writes (default
	// 10 GiB); MaxExtractRatio bounds written bytes per archive byte
	// (default 100; decompression bombs), with ExtractRatioFloor bytes
	// always allowed (default 1 MiB).
	MaxExtractBytes   int64
	MaxExtractRatio   int64
	ExtractRatioFloor int64
	// MaxWalk bounds the entries of one recursive operation (default
	// 1 000 000).
	MaxWalk int
}

func (l Limits) withDefaults() Limits {
	if l.MaxUpload <= 0 {
		l.MaxUpload = 2 << 30
	}
	if l.MaxDownload <= 0 {
		l.MaxDownload = 10 << 30
	}
	if l.MaxArchiveEntries <= 0 {
		l.MaxArchiveEntries = 100_000
	}
	if l.MaxExtractBytes <= 0 {
		l.MaxExtractBytes = 10 << 30
	}
	if l.MaxExtractRatio <= 0 {
		l.MaxExtractRatio = 100
	}
	if l.ExtractRatioFloor <= 0 {
		l.ExtractRatioFloor = 1 << 20
	}
	if l.MaxWalk <= 0 {
		l.MaxWalk = 1_000_000
	}
	return l
}

// Resolver returns the root directory of a scope: an absolute OS path
// with every symlink resolved, already checked to be a directory the
// caller serves. Failures are *protocol.Error values.
type Resolver func(ctx context.Context, scope protocol.FileScope) (string, error)

// Kinds names the job kinds the executors serve.
type Kinds struct {
	Delete, Copy, Move, Archive, Extract, Metadata domain.JobKind
}

// AgentKinds are the agent's files.* job kinds (the default).
var AgentKinds = Kinds{
	Delete: jobspec.FilesDelete, Copy: jobspec.FilesCopy, Move: jobspec.FilesMove,
	Archive: jobspec.FilesArchive, Extract: jobspec.FilesExtract, Metadata: jobspec.FilesMetadata,
}

// Options configures the service.
type Options struct {
	// Resolve maps a scope to its root directory (required).
	Resolve Resolver
	Clock   clock.Clock
	Logger  *slog.Logger
	Limits  Limits
	// Kinds are the job kinds of Executors (default AgentKinds).
	Kinds Kinds
	// Invalidate is called with the paths Docker Manager changed in a scope, so
	// other open views refresh (the agent relays it as fs_invalidation,
	// #23). Must not block.
	Invalidate func(protocol.FSInvalidationPayload)
}

// Service serves scoped file operations.
type Service struct {
	opts   Options
	limits Limits
	log    *slog.Logger

	// locks serializes check-then-replace on the same file (per scope
	// directory and path) within this process.
	lockMu sync.Mutex
	locks  map[string]*pathLock
}

type pathLock struct {
	mu   sync.Mutex
	refs int
}

// New returns the service.
func New(o Options) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Kinds == (Kinds{}) {
		o.Kinds = AgentKinds
	}
	return &Service{opts: o, limits: o.Limits.withDefaults(), log: o.Logger.With("component", "files"), locks: map[string]*pathLock{}}
}

// Limits returns the effective limits.
func (s *Service) Limits() Limits { return s.limits }

// lock serializes mutations of one file of a root.
func (s *Service) lock(key string) func() {
	s.lockMu.Lock()
	l := s.locks[key]
	if l == nil {
		l = &pathLock{}
		s.locks[key] = l
	}
	l.refs++
	s.lockMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.lockMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, key)
		}
		s.lockMu.Unlock()
	}
}

// Fail builds a protocol error (for resolvers).
func Fail(code, format string, args ...any) error {
	return &protocol.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func fail(code, format string, args ...any) error { return Fail(code, format, args...) }

// codeOf returns the protocol code of an error from this package.
func codeOf(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return protocol.CodeInternal
}

// isEscape reports whether err is os.Root refusing a path or symlink that
// leaves the root (the error value is not exported).
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path escapes from parent")
}

// classify maps a filesystem error on rel to a protocol error. Messages
// name the root-relative path only, never host paths.
func classify(err error, rel string) error {
	var pe *protocol.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return err
	case isEscape(err):
		return fail(protocol.CodeForbiddenPath, "%s leaves the scope root (a symlink or path outside it)", rel)
	case errors.Is(err, syscall.ELOOP):
		return fail(protocol.CodeForbiddenPath, "%s: too many levels of symbolic links", rel)
	case errors.Is(err, fs.ErrNotExist):
		return fail(protocol.CodeNotFound, "%s does not exist", rel)
	case errors.Is(err, fs.ErrExist):
		return fail(protocol.CodeAlreadyExists, "%s already exists", rel)
	case errors.Is(err, syscall.ENOTDIR):
		return fail(protocol.CodeNotDirectory, "%s: a path component is not a directory", rel)
	case errors.Is(err, syscall.EISDIR):
		return fail(protocol.CodeIsDirectory, "%s is a directory", rel)
	case errors.Is(err, syscall.ENOTEMPTY):
		return fail(protocol.CodeConflict, "%s is not empty", rel)
	case errors.Is(err, fs.ErrPermission):
		return fail(protocol.CodeForbiddenPath, "%s: permission denied", rel)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fail(protocol.CodeCancelled, "cancelled")
	}
	return err
}

// cleanPath validates a root-relative path.
func cleanPath(p string) (string, error) {
	rel, ok := protocol.CleanRelativePath(p)
	if !ok {
		return "", fail(protocol.CodeForbiddenPath, "invalid path: use a root-relative path without . or .. segments")
	}
	return rel, nil
}

// scopeRoot is an opened scope.
type scopeRoot struct {
	root  *os.Root
	scope protocol.FileScope
	// key identifies the root directory for per-file locks.
	key string
}

func (r *scopeRoot) Close() { _ = r.root.Close() }

// open resolves and opens a scope's root directory.
func (s *Service) open(ctx context.Context, scope protocol.FileScope) (*scopeRoot, error) {
	if err := scope.Validate(); err != nil {
		return nil, fail(protocol.CodeInvalidFrame, "invalid scope")
	}
	if s.opts.Resolve == nil {
		return nil, fail(protocol.CodeInternal, "no scope resolver")
	}
	resolved, err := s.opts.Resolve(ctx, scope)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fail(protocol.CodeNotFound, "cannot open the %s's directory", scope.Kind)
	}
	return &scopeRoot{root: root, scope: scope, key: resolved}, nil
}

// ScopeDir resolves a scope to its verified, symlink-free root directory
// (absolute OS path) with the same checks as every file operation: the
// file watcher (#23) watches exactly this directory.
func (s *Service) ScopeDir(ctx context.Context, scope protocol.FileScope) (string, error) {
	r, err := s.open(ctx, scope)
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.key, nil
}

// invalidate reports changed paths of a scope (never contents).
func (s *Service) invalidate(scope protocol.FileScope, paths ...string) {
	if s.opts.Invalidate == nil || len(paths) == 0 {
		return
	}
	p := protocol.FSInvalidationPayload{Scope: protocol.ScopeRef{Kind: scope.Kind, ID: scope.ID}, At: s.opts.Clock.Now().UTC()}
	for _, rp := range paths {
		if protocol.ValidRelativePath(rp) && !slices.Contains(p.Paths, rp) {
			p.Paths = append(p.Paths, rp)
		}
	}
	if len(p.Paths) > protocol.MaxPaths {
		p.Paths, p.Overflow = nil, true
	}
	if len(p.Paths) == 0 {
		p.Overflow = true
	}
	s.opts.Invalidate(p)
}

// Unix mode bits of a FileMode.
func modeBits(m fs.FileMode) uint32 {
	b := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		b |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		b |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		b |= 0o1000
	}
	return b
}

func typeOf(m fs.FileMode) string {
	switch {
	case m.IsRegular():
		return protocol.FileTypeFile
	case m.IsDir():
		return protocol.FileTypeDir
	case m&fs.ModeSymlink != 0:
		return protocol.FileTypeSymlink
	}
	return protocol.FileTypeOther
}

// entryOf builds the metadata of rel from an Lstat result.
func entryOf(rel string, fi fs.FileInfo) protocol.FileEntry {
	uid, gid, links := ownerOf(fi)
	name := path.Base(rel)
	if rel == "." {
		name = "."
	}
	e := protocol.FileEntry{Name: name, Path: rel, Type: typeOf(fi.Mode()), Mode: modeBits(fi.Mode()), UID: uid, GID: gid,
		ModTime: fi.ModTime().UTC(), Links: links}
	if e.Type == protocol.FileTypeFile {
		e.Size = fi.Size()
	}
	return e
}

// linkInfo fills a symlink's target and where it resolves.
func linkInfo(r *os.Root, rel string, e *protocol.FileEntry) {
	if t, err := r.Readlink(rel); err == nil {
		e.LinkTarget = filepath.ToSlash(t)
	}
	_, err := r.Stat(rel)
	switch {
	case err == nil:
		e.LinkStatus = protocol.LinkInside
	case isEscape(err):
		e.LinkStatus = protocol.LinkOutside
	case errors.Is(err, syscall.ELOOP):
		e.LinkStatus = protocol.LinkLoop
	default:
		e.LinkStatus = protocol.LinkDangling
	}
}

// ComputeETag hashes content, size and modification time (the ETag of a
// file whose content hashes to contentSHA).
func ComputeETag(contentSHA []byte, size int64, mod time.Time) string {
	h := sha256.New()
	h.Write(contentSHA)
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(size))           //nolint:gosec // G115: size is non-negative
	binary.BigEndian.PutUint64(b[8:], uint64(mod.UnixNano())) //nolint:gosec // G115: bit pattern only
	h.Write(b[:])
	return "f1-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// fileETag hashes an opened regular file (read from its start). Files
// with several hard links get none: the tag is derived from the content,
// which is not served for them (another name may lie outside the root), so
// a tag would confirm guesses of that content. If-Match on them conflicts.
func fileETag(ctx context.Context, f *os.File, fi fs.FileInfo) (string, error) {
	if fi.Size() > protocol.MaxETagSize || openLinks(f, fi) > 1 {
		return "", nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx: ctx, r: io.LimitReader(f, protocol.MaxETagSize+1)}); err != nil {
		return "", err
	}
	return ComputeETag(h.Sum(nil), fi.Size(), fi.ModTime()), nil
}

// ctxReader stops reading when ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// openRegular opens rel for reading content: it must be a regular file
// with a single hard link, and the opened handle must be the entry that
// was checked (no swap in between).
func openRegular(r *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	f, err := r.Open(rel)
	if err != nil {
		return nil, nil, classify(err, rel)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, classify(err, rel)
	}
	switch {
	case fi.IsDir():
		_ = f.Close()
		return nil, nil, fail(protocol.CodeIsDirectory, "%s is a directory", rel)
	case !fi.Mode().IsRegular():
		_ = f.Close()
		return nil, nil, fail(protocol.CodeUnsupportedFile, "%s is not a regular file", rel)
	case openLinks(f, fi) > 1:
		_ = f.Close()
		return nil, nil, fail(protocol.CodeUnsupportedFile, "%s has several hard links (another name may lie outside the root); its content is not served", rel)
	}
	return f, fi, nil
}

// openChecked opens an entry that was Lstat'ed as lfi and confirms the
// handle refers to the same file (a swap in between is refused).
func openChecked(r *os.Root, rel string, lfi fs.FileInfo, flag int) (*os.File, fs.FileInfo, error) {
	f, err := r.OpenFile(rel, flag, 0)
	if err != nil {
		return nil, nil, classify(err, rel)
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(fi, lfi) {
		_ = f.Close()
		return nil, nil, fail(protocol.CodeConflict, "%s changed during the operation", rel)
	}
	return f, fi, nil
}

// Stat returns the metadata of one entry (not following a final symlink).
func (s *Service) Stat(ctx context.Context, in protocol.FilesStatInput) (protocol.FileEntry, error) {
	rel, err := cleanPath(in.Path)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	r, err := s.open(ctx, in.Scope)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	defer r.Close()
	return s.stat(ctx, r, rel, in.ETag)
}

func (s *Service) stat(ctx context.Context, r *scopeRoot, rel string, etag bool) (protocol.FileEntry, error) {
	fi, err := r.root.Lstat(rel)
	if err != nil {
		return protocol.FileEntry{}, classify(err, rel)
	}
	e := entryOf(rel, fi)
	switch e.Type {
	case protocol.FileTypeSymlink:
		linkInfo(r.root, rel, &e)
	case protocol.FileTypeFile:
		if etag && fi.Size() <= protocol.MaxETagSize {
			f, ofi, err := openChecked(r.root, rel, fi, os.O_RDONLY)
			if err != nil {
				return protocol.FileEntry{}, err
			}
			defer func() { _ = f.Close() }()
			e.Links = max(e.Links, openLinks(f, ofi))
			if e.ETag, err = fileETag(ctx, f, ofi); err != nil {
				return protocol.FileEntry{}, classify(err, rel)
			}
		}
	}
	return e, nil
}

// OpenLinks returns the hard link count of an opened file (0 when the
// platform cannot tell).
func OpenLinks(f *os.File, fi fs.FileInfo) uint64 { return openLinks(f, fi) }
