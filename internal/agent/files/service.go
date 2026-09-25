// Package files is the agent's scoped file service (#15): directory
// listings, metadata, bounded reads and atomic writes, uploads and
// downloads (single files and streamed zip/tar.gz archives), conflict and
// impact previews, and the files.* job executors (archive, extract, copy,
// move, delete, chmod/chown). Every operation works inside one root: a
// stack's project directory (in a verified stack root, #28) or a local
// Docker volume's data directory (storage.Result.AccessFor; non-local
// drivers and DockYard's own volumes are refused).
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
// Residual risks (documented in docs/api/files.md): files are replaced by
// rename, so a concurrent writer outside DockYard can still change a file
// between the precondition check and the rename (the window is a few
// syscalls); bind mounts inside a volume are traversed like directories
// (os.Root does not stop at mount points); os.Root on Linux does not use
// openat2 RESOLVE_BENEATH but an equivalent per-component walk.
//
// File contents and names are never logged.
package files

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

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Engine is the part of the Engine adapter the service needs.
type Engine interface {
	InspectVolume(ctx context.Context, name string) (engine.Volume, error)
	ListContainers(ctx context.Context, f engine.ContainerFilter) ([]engine.Container, error)
}

// RoleLabel marks DockYard's own containers (deploy/*/compose.yaml); the
// volumes they mount are never served by the file manager.
const RoleLabel = "dev.neureka.dockyard.role"

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

// Options configures the service.
type Options struct {
	// Engine returns the connected Engine (nil while disconnected).
	Engine func() Engine
	// Storage returns the #28 layout check (nil before it ran).
	Storage func() *storage.Result
	Clock   clock.Clock
	Logger  *slog.Logger
	Limits  Limits
	// Invalidate is called with the paths DockYard changed in a scope, so
	// other open views refresh (the runtime relays it as fs_invalidation,
	// #23). Must not block.
	Invalidate func(protocol.FSInvalidationPayload)
}

// Service serves scoped file operations.
type Service struct {
	opts   Options
	limits Limits
	log    *slog.Logger

	// locks serializes check-then-replace on the same file (per scope
	// directory and path) within this agent.
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
	return &Service{opts: o, limits: o.Limits.withDefaults(), log: o.Logger.With("component", "files"), locks: map[string]*pathLock{}}
}

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

// fail builds a protocol error.
func fail(code, format string, args ...any) error {
	return &session.HandlerError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// codeOf returns the protocol code of an error from this package.
func codeOf(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
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
	var he *session.HandlerError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
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
	st := (*storage.Result)(nil)
	if s.opts.Storage != nil {
		st = s.opts.Storage()
	}
	if st == nil {
		return nil, fail(protocol.CodeUnsupportedVolume, "the storage layout has not been verified yet")
	}
	var dir string
	switch scope.Kind {
	case protocol.ScopeStack:
		if err := st.Allows(scope.Dir); err != nil {
			return nil, fail(protocol.CodeForbiddenPath, "the stack's project directory is not in a verified stack root")
		}
		dir = scope.Dir
	case protocol.ScopeVolume:
		d, err := s.volumeDir(ctx, st, scope.ID)
		if err != nil {
			return nil, err
		}
		dir = d
	}
	local := filepath.FromSlash(dir)
	resolved, err := filepath.EvalSymlinks(local)
	if err != nil {
		return nil, fail(protocol.CodeNotFound, "the %s's directory does not exist", scope.Kind)
	}
	// The directory itself (not only the given name) must be inside a
	// verified root: a project directory that is a symlink elsewhere is
	// refused.
	switch scope.Kind {
	case protocol.ScopeStack:
		if st.Allows(filepath.ToSlash(resolved)) != nil {
			return nil, fail(protocol.CodeForbiddenPath, "the stack's project directory resolves outside the verified stack roots")
		}
	case protocol.ScopeVolume:
		if !within(filepath.ToSlash(resolved), st.VolumesDir) && !within(filepath.ToSlash(resolved), evalOr(st.VolumesDir)) {
			return nil, fail(protocol.CodeUnsupportedVolume, "the volume's data directory resolves outside Docker's volume directory")
		}
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fail(protocol.CodeNotFound, "cannot open the %s's directory", scope.Kind)
	}
	return &scopeRoot{root: root, scope: scope, key: resolved}, nil
}

func evalOr(p string) string {
	r, err := filepath.EvalSymlinks(filepath.FromSlash(p))
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// within reports whether p is root or below it (slash paths).
func within(p, root string) bool {
	p, root = path.Clean(p), path.Clean(root)
	return root != "." && (p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/"))
}

// volumeDir returns the data directory of a supported local volume.
func (s *Service) volumeDir(ctx context.Context, st *storage.Result, name string) (string, error) {
	var eng Engine
	if s.opts.Engine != nil {
		eng = s.opts.Engine()
	}
	if eng == nil {
		return "", fail(protocol.CodeEngineUnavailable, "the Docker Engine is not connected")
	}
	v, err := eng.InspectVolume(ctx, name)
	if err != nil {
		if engine.IsCode(err, engine.CodeNotFound) {
			return "", fail(protocol.CodeNotFound, "volume %s does not exist", name)
		}
		return "", fail(protocol.CodeEngineUnavailable, "cannot inspect the volume")
	}
	if acc := st.AccessFor(v); !acc.Supported {
		return "", fail(protocol.CodeUnsupportedVolume, "%s", acc.Reason)
	}
	mp := path.Clean(filepath.ToSlash(v.Mountpoint))
	if st.StacksDir != "" && (within(mp, st.StacksDir) || within(st.StacksDir, mp)) {
		return "", fail(protocol.CodeUnsupportedVolume, "the stacks volume is browsed per stack (stack files), not as a volume")
	}
	protected, err := s.protectedVolumes(ctx, eng)
	if err != nil {
		return "", fail(protocol.CodeEngineUnavailable, "cannot list DockYard's own containers")
	}
	if slices.Contains(protected, v.Name) {
		return "", fail(protocol.CodeUnsupportedVolume, "volume %s holds DockYard's own data and is not served by the file manager", name)
	}
	return mp, nil
}

// protectedVolumes lists the volumes mounted by DockYard's own containers
// (manager data, agent state).
func (s *Service) protectedVolumes(ctx context.Context, eng Engine) ([]string, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{RoleLabel}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				out = append(out, m.Name)
			}
		}
	}
	return out, nil
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

// computeETag hashes content, size and modification time.
func computeETag(contentSHA []byte, size int64, mod time.Time) string {
	h := sha256.New()
	h.Write(contentSHA)
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(size))           //nolint:gosec // G115: size is non-negative
	binary.BigEndian.PutUint64(b[8:], uint64(mod.UnixNano())) //nolint:gosec // G115: bit pattern only
	h.Write(b[:])
	return "f1-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// fileETag hashes an opened regular file (read from its start).
func fileETag(ctx context.Context, f *os.File, fi fs.FileInfo) (string, error) {
	if fi.Size() > protocol.MaxETagSize {
		return "", nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx: ctx, r: io.LimitReader(f, protocol.MaxETagSize+1)}); err != nil {
		return "", err
	}
	return computeETag(h.Sum(nil), fi.Size(), fi.ModTime()), nil
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
