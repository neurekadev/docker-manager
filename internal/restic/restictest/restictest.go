// Package restictest is an in-memory restic for tests of the backup
// executors (#10): it implements restic.Opener with repositories keyed by
// location, password checks, snapshots of real files (read from and
// restored to disk), tags, listing, dump, forget, keys and injectable
// failures. It never executes anything.
package restictest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
)

// Call records one operation.
type Call struct {
	Op         string
	Repository string
	Password   string
	// Args summarizes non-secret arguments (paths, tags, IDs).
	Args []string
	// Compression is the location's compression mode ("" auto).
	Compression string
}

// Store holds the fake repositories.
type Store struct {
	mu    sync.Mutex
	now   func() time.Time
	repos map[string]*repoState
	seq   int
	// fail maps "op" or "op@repository" to an injected error.
	fail  map[string]error
	calls []Call
	// OnBackup runs at the start of every Backup (tests use it to observe
	// container state while "restic" runs); an error fails the backup.
	OnBackup func(req restic.BackupRequest) error
}

// unlock releases s.mu.
func (s *Store) unlock() { s.mu.Unlock() }

type repoState struct {
	id    string
	keys  []key
	snaps []*snap
	// damaged makes Check fail.
	damaged bool
	// version is the repository format version (0 means 2).
	version int
}

type key struct {
	id       string
	password string
}

type snap struct {
	restic.Snapshot
	files map[string]*file
}

type file struct {
	data []byte
	mode fs.FileMode
	link string
	uid  int
	gid  int
}

// New returns an empty store whose snapshots are timestamped by now
// (time.Now when nil).
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, repos: map[string]*repoState{}, fail: map[string]error{}}
}

// Fail makes op (e.g. "backup", "check") fail with err; with repository
// set only there. A nil err removes the injection.
func (s *Store) Fail(op, repository string, err error) {
	s.mu.Lock()
	defer s.unlock()
	k := op
	if repository != "" {
		k += "@" + repository
	}
	if err == nil {
		delete(s.fail, k)
		return
	}
	s.fail[k] = err
}

// Damage marks a repository as damaged (Check fails with
// restic.CodeRepositoryDamaged), like a truncated pack file.
func (s *Store) Damage(repository string) {
	s.mu.Lock()
	defer s.unlock()
	if r := s.repos[repository]; r != nil {
		r.damaged = true
	}
}

// SetVersion sets a repository's format version (1: a repository created
// by restic before 0.14, which cannot compress).
func (s *Store) SetVersion(repository string, version int) {
	s.mu.Lock()
	defer s.unlock()
	if r := s.repos[repository]; r != nil {
		r.version = version
	}
}

// Delete removes a repository entirely (a lost location).
func (s *Store) Delete(repository string) {
	s.mu.Lock()
	defer s.unlock()
	delete(s.repos, repository)
}

// Move moves a repository to another location (a directory mounted at a
// new path, or copied to another bucket).
func (s *Store) Move(from, to string) {
	s.mu.Lock()
	defer s.unlock()
	if r := s.repos[from]; r != nil {
		s.repos[to] = r
		delete(s.repos, from)
	}
}

// Exists reports whether a repository exists.
func (s *Store) Exists(repository string) bool {
	s.mu.Lock()
	defer s.unlock()
	return s.repos[repository] != nil
}

// Repositories lists the repositories.
func (s *Store) Repositories() []string {
	s.mu.Lock()
	defer s.unlock()
	var out []string
	for k := range s.repos {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Snapshots returns a repository's snapshots (no password needed).
func (s *Store) Snapshots(repository string) []restic.Snapshot {
	s.mu.Lock()
	defer s.unlock()
	r := s.repos[repository]
	if r == nil {
		return nil
	}
	out := make([]restic.Snapshot, 0, len(r.snaps))
	for _, sn := range r.snaps {
		out = append(out, sn.Snapshot)
	}
	return out
}

// Passwords returns a repository's key passwords (tests of rotation).
func (s *Store) Passwords(repository string) []string {
	s.mu.Lock()
	defer s.unlock()
	r := s.repos[repository]
	if r == nil {
		return nil
	}
	var out []string
	for _, k := range r.keys {
		out = append(out, k.password)
	}
	return out
}

// Calls returns the recorded calls.
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.unlock()
	return slices.Clone(s.calls)
}

// Open implements restic.Opener.
func (s *Store) Open(loc restic.Location, password string) restic.Repo {
	return &repo{s: s, loc: loc, password: password}
}

type repo struct {
	s        *Store
	loc      restic.Location
	password string
}

func (s *Store) nextID() string {
	s.seq++
	sum := sha256.Sum256(fmt.Appendf(nil, "restictest-%d", s.seq))
	return hex.EncodeToString(sum[:])
}

// begin records the call and returns the repository (locked store).
func (p *repo) begin(ctx context.Context, op string, needRepo bool, args ...string) (*repoState, error) {
	if err := ctx.Err(); err != nil {
		return nil, &restic.Error{Op: op, Code: restic.CodeCancelled, Message: "cancelled"}
	}
	s := p.s
	s.calls = append(s.calls, Call{Op: op, Repository: p.loc.Repository, Password: p.password, Args: args, Compression: p.loc.Compression})
	if err := s.fail[op+"@"+p.loc.Repository]; err != nil {
		return nil, err
	}
	if err := s.fail[op]; err != nil {
		return nil, err
	}
	r := s.repos[p.loc.Repository]
	if !needRepo {
		return r, nil
	}
	if r == nil {
		return nil, &restic.Error{Op: op, Code: restic.CodeRepositoryNotFound, ExitCode: 10, Message: "repository does not exist"}
	}
	if !slices.ContainsFunc(r.keys, func(k key) bool { return k.password == p.password }) {
		return nil, &restic.Error{Op: op, Code: restic.CodeKeyRejected, ExitCode: 12, Message: "wrong password or no key found"}
	}
	return r, nil
}

func (p *repo) Init(ctx context.Context) (string, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "init", false)
	if err != nil {
		return "", err
	}
	if r != nil {
		return "", &restic.Error{Op: "init", Code: restic.CodeRepositoryExists, ExitCode: 1, Message: "config file already exists"}
	}
	r = &repoState{id: p.s.nextID(), keys: []key{{id: p.s.nextID(), password: p.password}}}
	p.s.repos[p.loc.Repository] = r
	return r.id, nil
}

func (p *repo) Config(ctx context.Context) (restic.Config, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "config", true)
	if err != nil {
		return restic.Config{}, err
	}
	version := r.version
	if version == 0 {
		version = 2
	}
	return restic.Config{ID: r.id, Version: version}, nil
}

// storedPath maps an absolute OS path to restic's slash form (Windows
// drive letters become a first element, like restic on Windows).
func storedPath(p string) string {
	p = filepath.Clean(p)
	if v := filepath.VolumeName(p); v != "" {
		p = "/" + strings.TrimSuffix(v, ":") + filepath.ToSlash(strings.TrimPrefix(p, v))
		return p
	}
	return filepath.ToSlash(p)
}

// osPath maps a stored path below target.
func osPath(target, stored string) string {
	return filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(stored, "/")))
}

func excluded(stored string, patterns []string) bool {
	for _, pat := range patterns {
		sp := storedPath(pat)
		if stored == sp || strings.HasPrefix(stored, sp+"/") {
			return true
		}
		if ok, _ := path.Match(sp, stored); ok {
			return true
		}
		if !strings.Contains(pat, "/") && !strings.Contains(pat, "\\") {
			if ok, _ := path.Match(pat, path.Base(stored)); ok {
				return true
			}
		}
	}
	return false
}

func (p *repo) Backup(ctx context.Context, req restic.BackupRequest) (restic.BackupSummary, error) {
	if p.s.OnBackup != nil {
		if err := p.s.OnBackup(req); err != nil {
			return restic.BackupSummary{}, err
		}
	}
	files := map[string]*file{}
	var sum restic.BackupSummary
	var paths []string
	var first string // the first regular file, reported as being read
	if req.Stdin != nil {
		b, err := io.ReadAll(req.Stdin)
		if err != nil {
			return sum, err
		}
		name := "/" + req.StdinFilename
		files[name] = &file{data: b, mode: 0o644}
		paths = []string{name}
		sum.TotalFilesProcessed, sum.TotalBytesProcessed = 1, int64(len(b))
	} else {
		for _, root := range req.Paths {
			if req.Dir != "" && !filepath.IsAbs(root) {
				root = filepath.Join(req.Dir, root)
			}
			paths = append(paths, storedPath(root))
			err := filepath.WalkDir(root, func(pth string, d fs.DirEntry, err error) error {
				if err != nil {
					if len(sum.Errors) < 20 {
						sum.Errors = append(sum.Errors, pth+": "+err.Error())
					}
					sum.Incomplete = true
					return nil
				}
				sp := storedPath(pth)
				if excluded(sp, req.Excludes) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				f := &file{mode: info.Mode()}
				switch {
				case info.Mode()&fs.ModeSymlink != 0:
					f.link, _ = os.Readlink(pth)
				case info.Mode().IsRegular():
					b, err := os.ReadFile(pth) //nolint:gosec // test fake reading the test's own files
					if err != nil {
						sum.Incomplete = true
						sum.Errors = append(sum.Errors, pth+": "+err.Error())
						return nil
					}
					f.data = b
					if first == "" {
						first = sp
					}
					sum.TotalBytesProcessed += int64(len(b))
					sum.TotalFilesProcessed++
				}
				files[sp] = f
				return nil
			})
			if err != nil {
				return sum, err
			}
		}
	}
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "backup", true, append(append([]string{}, req.Tags...), paths...)...)
	if err != nil {
		return restic.BackupSummary{}, err
	}
	at := p.s.now()
	if !req.Time.IsZero() {
		at = req.Time
	}
	id := p.s.nextID()
	sn := &snap{Snapshot: restic.Snapshot{ID: id, ShortID: id[:8], Time: at.UTC(), Hostname: req.Host, Paths: paths,
		Tags: slices.Clone(req.Tags)}, files: files}
	r.snaps = append(r.snaps, sn)
	sum.SnapshotID = id
	sum.FilesNew = int64(len(files))
	sum.DataAdded = sum.TotalBytesProcessed
	if req.Progress != nil {
		if first != "" {
			req.Progress(restic.Progress{Percent: 50, FilesTotal: sum.TotalFilesProcessed, BytesTotal: sum.TotalBytesProcessed,
				SecondsRemaining: 1, CurrentFile: first})
		}
		req.Progress(restic.Progress{Percent: 100, FilesDone: sum.TotalFilesProcessed, FilesTotal: sum.TotalFilesProcessed,
			BytesDone: sum.TotalBytesProcessed, BytesTotal: sum.TotalBytesProcessed})
	}
	return sum, nil
}

func (p *repo) find(r *repoState, op, id string) (*snap, error) {
	for _, sn := range r.snaps {
		if sn.ID == id || (len(id) >= 8 && strings.HasPrefix(sn.ID, id)) {
			return sn, nil
		}
	}
	return nil, &restic.Error{Op: op, Code: restic.CodeSnapshotNotFound, ExitCode: 1, Message: "no matching ID found"}
}

func (p *repo) Snapshots(ctx context.Context, f restic.SnapshotFilter) ([]restic.Snapshot, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "snapshots", true, f.Tags...)
	if err != nil {
		return nil, err
	}
	var out []restic.Snapshot
	for _, sn := range r.snaps {
		if f.Host != "" && sn.Hostname != f.Host {
			continue
		}
		ok := true
		for _, t := range f.Tags {
			if !sn.HasTag(t) {
				ok = false
			}
		}
		if ok {
			out = append(out, sn.Snapshot)
		}
	}
	return out, nil
}

func (p *repo) Ls(ctx context.Context, snapshotID, dir string, recursive bool, limit int) (restic.Listing, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "ls", true, snapshotID, dir)
	if err != nil {
		return restic.Listing{}, err
	}
	sn, err := p.find(r, "ls", snapshotID)
	if err != nil {
		return restic.Listing{}, err
	}
	if limit <= 0 {
		limit = restic.DefaultMaxNodes
	}
	dir = strings.TrimSuffix(dir, "/")
	keys := make([]string, 0, len(sn.files))
	for k := range sn.files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var l restic.Listing
	for _, k := range keys {
		switch {
		case dir == "": // restic lists everything without a directory argument
		case k == dir:
		case strings.HasPrefix(k, dir+"/"):
			if !recursive && strings.Contains(strings.TrimPrefix(k, dir+"/"), "/") {
				continue
			}
		default:
			continue
		}
		if len(l.Nodes) >= limit {
			l.Truncated = true
			break
		}
		f := sn.files[k]
		typ := "file"
		switch {
		case f.mode.IsDir():
			typ = "dir"
		case f.link != "":
			typ = "symlink"
		}
		l.Nodes = append(l.Nodes, restic.Node{Name: path.Base(k), Type: typ, Path: k, Size: int64(len(f.data)), Mode: uint32(f.mode.Perm()),
			UID: f.uid, GID: f.gid, MTime: sn.Time})
	}
	if dir != "" && len(l.Nodes) == 0 {
		return l, &restic.Error{Op: "ls", Code: restic.CodeSnapshotNotFound, Message: "path not found in snapshot"}
	}
	return l, nil
}

func (p *repo) Dump(ctx context.Context, snapshotID, name string, w io.Writer) error {
	p.s.mu.Lock()
	r, err := p.begin(ctx, "dump", true, snapshotID, name)
	if err != nil {
		p.s.mu.Unlock()
		return err
	}
	sn, err := p.find(r, "dump", snapshotID)
	if err != nil {
		p.s.mu.Unlock()
		return err
	}
	f := sn.files[name]
	p.s.mu.Unlock()
	if f == nil || !f.mode.IsRegular() {
		return &restic.Error{Op: "dump", Code: restic.CodeSnapshotNotFound, Message: "cannot dump file: not found in snapshot"}
	}
	_, err = io.Copy(w, bytes.NewReader(f.data))
	return err
}

// patternPath maps a restore pattern (an OS path, or restic's slash form)
// to the stored form.
func patternPath(pat string) string {
	if strings.HasPrefix(pat, "/") && !filepath.IsAbs(pat) {
		return pat // already slash form (Windows)
	}
	return storedPath(pat)
}

// includes reports whether stored is selected by an include pattern (the
// pattern itself, below it, or one of its parent directories).
func includes(stored string, patterns []string) bool {
	for _, pat := range patterns {
		sp := patternPath(pat)
		if stored == sp || strings.HasPrefix(stored, sp+"/") || strings.HasPrefix(sp, stored+"/") {
			return true
		}
	}
	return false
}

// excludes reports whether stored is the pattern or below it.
func excludes(stored string, patterns []string) bool {
	for _, pat := range patterns {
		sp := patternPath(pat)
		if stored == sp || strings.HasPrefix(stored, sp+"/") {
			return true
		}
	}
	return false
}

func (p *repo) Restore(ctx context.Context, req restic.RestoreRequest) (restic.RestoreSummary, error) {
	p.s.mu.Lock()
	r, err := p.begin(ctx, "restore", true, req.SnapshotID, req.Target)
	if err != nil {
		p.s.mu.Unlock()
		return restic.RestoreSummary{}, err
	}
	sn, err := p.find(r, "restore", req.SnapshotID)
	if err != nil {
		p.s.mu.Unlock()
		return restic.RestoreSummary{}, err
	}
	keys := make([]string, 0, len(sn.files))
	for k := range sn.files {
		keys = append(keys, k)
	}
	files := sn.files
	paths := slices.Clone(sn.Paths)
	p.s.mu.Unlock()
	slices.Sort(keys)
	var sum restic.RestoreSummary
	restored := map[string]bool{}
	for _, k := range keys {
		if len(req.Include) > 0 && !includes(k, req.Include) {
			continue
		}
		if excludes(k, req.Exclude) {
			continue
		}
		f := files[k]
		dst := osPath(req.Target, k)
		restored[dst] = true
		sum.TotalFiles++
		switch {
		case f.mode.IsDir():
			if err := os.MkdirAll(dst, 0o750); err != nil {
				return sum, err
			}
		case f.link != "":
			_ = os.Remove(dst)
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return sum, err
			}
			if err := os.Symlink(f.link, dst); err != nil {
				return sum, err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return sum, err
			}
			if req.Overwrite == "never" {
				if _, err := os.Lstat(dst); err == nil {
					sum.FilesSkipped++
					continue
				}
			}
			if err := os.WriteFile(dst, f.data, f.mode.Perm()|0o200); err != nil {
				return sum, err
			}
			sum.FilesRestored++
			sum.BytesRestored += int64(len(f.data))
			sum.TotalBytes += int64(len(f.data))
		}
	}
	if req.Delete {
		// Remove what the snapshot does not have below the restored roots
		// (the snapshot's paths, or the include patterns).
		roots := paths
		if len(req.Include) > 0 {
			roots = nil
			for _, i := range req.Include {
				roots = append(roots, patternPath(i))
			}
		}
		for _, root := range roots {
			base := osPath(req.Target, root)
			var extra []string
			_ = filepath.WalkDir(base, func(pth string, d fs.DirEntry, err error) error {
				if err != nil || pth == base {
					return nil
				}
				if !restored[pth] {
					extra = append(extra, pth)
					if d.IsDir() {
						return filepath.SkipDir
					}
				}
				return nil
			})
			for _, e := range extra {
				_ = os.RemoveAll(e)
			}
		}
	}
	if req.Progress != nil {
		req.Progress(restic.Progress{Percent: 100, FilesDone: sum.FilesRestored, FilesTotal: sum.TotalFiles})
	}
	return sum, nil
}

func (p *repo) Forget(ctx context.Context, ids []string) error {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "forget", true, ids...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := p.find(r, "forget", id); err != nil {
			return err
		}
	}
	r.snaps = slices.DeleteFunc(r.snaps, func(sn *snap) bool { return slices.Contains(ids, sn.ID) })
	return nil
}

func (p *repo) Prune(ctx context.Context) error {
	p.s.mu.Lock()
	defer p.s.unlock()
	_, err := p.begin(ctx, "prune", true)
	return err
}

func (p *repo) Stats(ctx context.Context) (restic.Stats, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "stats", true)
	if err != nil {
		return restic.Stats{}, err
	}
	// Like a compressed repository: every blob stored at half its size.
	var st restic.Stats
	for _, sn := range r.snaps {
		st.SnapshotsCount++
		for _, f := range sn.files {
			st.TotalUncompSize += int64(len(f.data))
		}
	}
	st.TotalSize = st.TotalUncompSize / 2
	if st.TotalUncompSize > 0 {
		st.CompressionRatio, st.CompressionProgress, st.CompressionSpaceSaving = 2, 100, 50
	}
	return st, nil
}

func (p *repo) Check(ctx context.Context, req restic.CheckRequest) (restic.CheckResult, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "check", true, req.ReadDataSubset)
	if err != nil {
		return restic.CheckResult{}, err
	}
	if r.damaged {
		return restic.CheckResult{}, &restic.Error{Op: "check", Code: restic.CodeRepositoryDamaged, ExitCode: 1,
			Message: "Fatal: repository contains errors"}
	}
	return restic.CheckResult{ReadData: req.ReadDataSubset != ""}, nil
}

func (p *repo) Keys(ctx context.Context) ([]restic.Key, error) {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "key list", true)
	if err != nil {
		return nil, err
	}
	var out []restic.Key
	for _, k := range r.keys {
		out = append(out, restic.Key{ID: k.id, Current: k.password == p.password, UserName: "docker-manager", HostName: "docker-manager"})
	}
	return out, nil
}

func (p *repo) AddKey(ctx context.Context, newPassword string) error {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "key add", true)
	if err != nil {
		return err
	}
	if newPassword == "" {
		return errors.New("empty key")
	}
	r.keys = append(r.keys, key{id: p.s.nextID(), password: newPassword})
	return nil
}

func (p *repo) RemoveKey(ctx context.Context, id string) error {
	p.s.mu.Lock()
	defer p.s.unlock()
	r, err := p.begin(ctx, "key remove", true, id)
	if err != nil {
		return err
	}
	for i, k := range r.keys {
		if k.id == id {
			if k.password == p.password {
				return &restic.Error{Op: "key remove", Code: restic.CodeFailed, Message: "refusing to remove key currently used to access repository"}
			}
			r.keys = slices.Delete(r.keys, i, i+1)
			return nil
		}
	}
	return &restic.Error{Op: "key remove", Code: restic.CodeFailed, Message: "key not found"}
}

func (p *repo) Unlock(ctx context.Context) error {
	p.s.mu.Lock()
	defer p.s.unlock()
	_, err := p.begin(ctx, "unlock", true)
	return err
}

// SnapshotFiles returns the stored paths of a snapshot (tests).
func (s *Store) SnapshotFiles(repository, id string) []string {
	s.mu.Lock()
	defer s.unlock()
	r := s.repos[repository]
	if r == nil {
		return nil
	}
	for _, sn := range r.snaps {
		if sn.ID == id {
			var out []string
			for k := range sn.files {
				out = append(out, k)
			}
			slices.Sort(out)
			return out
		}
	}
	return nil
}

// StoredPath exposes the path mapping (tests compare listings).
func StoredPath(p string) string { return storedPath(p) }
