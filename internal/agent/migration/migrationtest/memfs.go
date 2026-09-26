// Package migrationtest provides an in-memory host filesystem for tests of
// environment migration (#35): it keeps what a Windows development host
// cannot (numeric owners, setuid/setgid/sticky bits, nanosecond access and
// modification times, hard links, FIFOs, sockets, device nodes), so the
// archive/extract round trip, the agent's stream handlers and the manager's
// relay run anywhere. Import it from tests only.
package migrationtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
)

// Host is an in-memory host filesystem addressed by absolute slash paths.
type Host struct {
	mu   sync.Mutex
	root *node
	ino  uint64
	now  time.Time
	// FailOpen, when set, makes Open (read) of a name fail (fault
	// injection).
	FailOpen func(name string) error
}

type node struct {
	mode         fs.FileMode
	uid, gid     int
	mtime, atime time.Time
	data         []byte
	target       string
	children     map[string]*node
	ino          uint64
	nlink        int
}

// NewHost returns an empty host with a root directory.
func NewHost() *Host {
	h := &Host{now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	h.root = h.newNode(fs.ModeDir | 0o755)
	return h
}

func (h *Host) newNode(mode fs.FileMode) *node {
	h.ino++
	n := &node{mode: mode, mtime: h.now, atime: h.now, ino: h.ino, nlink: 1}
	if mode.IsDir() {
		n.children = map[string]*node{}
	}
	return n
}

func split(p string) []string {
	p = strings.Trim(path.Clean("/"+p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// walk resolves segs below n without following symlinks.
func walk(n *node, segs []string) (*node, error) {
	for _, s := range segs {
		if n.children == nil {
			return nil, fmt.Errorf("%s: %w", s, fs.ErrInvalid)
		}
		c, ok := n.children[s]
		if !ok {
			return nil, fmt.Errorf("%s: %w", s, fs.ErrNotExist)
		}
		n = c
	}
	return n, nil
}

// MkdirAll creates an absolute directory path (0755, root-owned).
func (h *Host) MkdirAll(p string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.root
	for _, s := range split(p) {
		c, ok := n.children[s]
		if !ok {
			c = h.newNode(fs.ModeDir | 0o755)
			n.children[s] = c
		}
		n = c
	}
}

// Exists reports whether an absolute path exists.
func (h *Host) Exists(p string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := walk(h.root, split(p))
	return err == nil
}

// Opener opens absolute directories of the host as migration.FS views.
func (h *Host) Opener() migration.Opener {
	return func(dir string) (migration.FS, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		n, err := walk(h.root, split(dir))
		if err != nil {
			return nil, err
		}
		if !n.mode.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", dir)
		}
		return &view{h: h, root: n}, nil
	}
}

// Entry describes a file for Put.
type Entry struct {
	// Type: "dir", "file" (default), "symlink", "fifo", "socket", "device".
	Type     string
	Mode     fs.FileMode // permission and special bits
	UID, GID int
	MTime    time.Time
	ATime    time.Time
	Data     string
	Target   string
}

// Put creates (or replaces) an absolute path; parents must exist.
func (h *Host) Put(p string, e Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	segs := split(p)
	parent, err := walk(h.root, segs[:len(segs)-1])
	if err != nil || parent.children == nil {
		panic(fmt.Sprintf("migrationtest: parent of %s: %v", p, err))
	}
	var mode fs.FileMode
	switch e.Type {
	case "dir":
		mode = fs.ModeDir
	case "symlink":
		mode = fs.ModeSymlink
	case "fifo":
		mode = fs.ModeNamedPipe
	case "socket":
		mode = fs.ModeSocket
	case "device":
		mode = fs.ModeDevice
	}
	n := h.newNode(mode | e.Mode)
	n.uid, n.gid, n.data, n.target = e.UID, e.GID, []byte(e.Data), e.Target
	if !e.MTime.IsZero() {
		n.mtime = e.MTime.UTC()
	}
	n.atime = n.mtime
	if !e.ATime.IsZero() {
		n.atime = e.ATime.UTC()
	}
	parent.children[segs[len(segs)-1]] = n
}

// Link adds a hard link newp to the file at oldp (absolute paths).
func (h *Host) Link(oldp, newp string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := walk(h.root, split(oldp))
	if err != nil {
		panic(err)
	}
	segs := split(newp)
	parent, err := walk(h.root, segs[:len(segs)-1])
	if err != nil {
		panic(err)
	}
	n.nlink++
	parent.children[segs[len(segs)-1]] = n
}

// Snapshot is one entry of Tree.
type Snapshot struct {
	Type     string
	Mode     fs.FileMode
	UID, GID int
	MTime    time.Time
	ATime    time.Time
	// SHA256 of a file's content; Target of a symlink; Inode groups hard
	// links (the first path of the inode).
	SHA256 string
	Target string
	Inode  string
}

// Tree returns every entry below an absolute directory (including "."),
// keyed by relative path.
func (h *Host) Tree(dir string) map[string]Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	root, err := walk(h.root, split(dir))
	if err != nil {
		return nil
	}
	out := map[string]Snapshot{}
	first := map[uint64]string{}
	var rec func(name string, n *node)
	rec = func(name string, n *node) {
		s := Snapshot{Mode: n.mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky), UID: n.uid, GID: n.gid,
			MTime: n.mtime, ATime: n.atime, Target: n.target}
		switch {
		case n.mode.IsDir():
			s.Type = "dir"
		case n.mode&fs.ModeSymlink != 0:
			s.Type = "symlink"
		case n.mode&fs.ModeNamedPipe != 0:
			s.Type = "fifo"
		case n.mode&fs.ModeSocket != 0:
			s.Type = "socket"
		case n.mode&fs.ModeDevice != 0:
			s.Type = "device"
		default:
			s.Type = "file"
			sum := sha256.Sum256(n.data)
			s.SHA256 = hex.EncodeToString(sum[:])
			if f, ok := first[n.ino]; ok {
				s.Inode = f
			} else {
				first[n.ino] = name
				s.Inode = name
			}
		}
		out[name] = s
		names := make([]string, 0, len(n.children))
		for k := range n.children {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			child := k
			if name != "." {
				child = name + "/" + k
			}
			rec(child, n.children[k])
		}
	}
	rec(".", root)
	return out
}

// view is a migration.FS rooted at one directory node.
type view struct {
	h    *Host
	root *node
}

func (v *view) resolve(name string) (*node, error) {
	return walk(v.root, split(name))
}

func (v *view) parent(name string) (*node, string, error) {
	segs := split(name)
	if len(segs) == 0 {
		return nil, "", errors.New("the root has no parent")
	}
	p, err := walk(v.root, segs[:len(segs)-1])
	if err != nil {
		return nil, "", err
	}
	if !p.mode.IsDir() {
		return nil, "", fmt.Errorf("%s: parent is not a directory", name)
	}
	return p, segs[len(segs)-1], nil
}

func (v *view) Lstat(name string) (migration.Info, error) {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return migration.Info{}, err
	}
	return migration.Info{Mode: n.mode, Size: int64(len(n.data)), UID: n.uid, GID: n.gid, ModTime: n.mtime, AccessTime: n.atime,
		Nlink: uint64(n.nlink), Dev: 1, Ino: n.ino}, nil //nolint:gosec // small counts
}

func (v *view) ReadDir(name string) ([]string, error) {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return nil, err
	}
	if !n.mode.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", name)
	}
	out := make([]string, 0, len(n.children))
	for k := range n.children {
		out = append(out, k)
	}
	slices.Sort(out)
	return out, nil
}

func (v *view) Open(name string) (io.ReadCloser, error) {
	if f := v.h.FailOpen; f != nil {
		if err := f(name); err != nil {
			return nil, err
		}
	}
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return nil, err
	}
	if !n.mode.IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(n.data))), nil
}

func (v *view) Readlink(name string) (string, error) {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return "", err
	}
	if n.mode&fs.ModeSymlink == 0 {
		return "", fmt.Errorf("%s: not a symlink", name)
	}
	return n.target, nil
}

func (v *view) create(name string, mode fs.FileMode) (*node, error) {
	p, base, err := v.parent(name)
	if err != nil {
		return nil, err
	}
	if _, ok := p.children[base]; ok {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrExist)
	}
	n := v.h.newNode(mode)
	p.children[base] = n
	return n, nil
}

func (v *view) Mkdir(name string, perm fs.FileMode) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	_, err := v.create(name, fs.ModeDir|perm)
	return err
}

func (v *view) MkdirAll(name string, perm fs.FileMode) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n := v.root
	for _, s := range split(name) {
		c, ok := n.children[s]
		if !ok {
			c = v.h.newNode(fs.ModeDir | perm)
			n.children[s] = c
		} else if !c.mode.IsDir() {
			return fmt.Errorf("%s: not a directory", s)
		}
		n = c
	}
	return nil
}

type memFile struct {
	v   *view
	n   *node
	buf bytes.Buffer
}

func (f *memFile) Write(p []byte) (int, error) { return f.buf.Write(p) }

func (f *memFile) Close() error {
	f.v.h.mu.Lock()
	defer f.v.h.mu.Unlock()
	f.n.data = bytes.Clone(f.buf.Bytes())
	return nil
}

func (v *view) Create(name string) (io.WriteCloser, error) {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.create(name, 0o600)
	if err != nil {
		return nil, err
	}
	return &memFile{v: v, n: n}, nil
}

func (v *view) Symlink(target, name string) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.create(name, fs.ModeSymlink|0o777)
	if err == nil {
		n.target = target
	}
	return err
}

func (v *view) Link(oldname, newname string) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(oldname)
	if err != nil {
		return err
	}
	p, base, err := v.parent(newname)
	if err != nil {
		return err
	}
	if _, ok := p.children[base]; ok {
		return fs.ErrExist
	}
	n.nlink++
	p.children[base] = n
	return nil
}

func (v *view) Mkfifo(name string, perm fs.FileMode) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	_, err := v.create(name, fs.ModeNamedPipe|perm)
	return err
}

func (v *view) Lchown(name string, uid, gid int) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return err
	}
	n.uid, n.gid = uid, gid
	return nil
}

func (v *view) Chmod(name string, mode fs.FileMode) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return err
	}
	if n.mode&fs.ModeSymlink != 0 {
		return errors.New("chmod on a symlink")
	}
	n.mode = n.mode&fs.ModeType | mode&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)
	return nil
}

func (v *view) Chtimes(name string, atime, mtime time.Time) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return err
	}
	if n.mode&fs.ModeSymlink != 0 {
		return errors.New("chtimes on a symlink")
	}
	n.atime, n.mtime = atime.UTC(), mtime.UTC()
	return nil
}

func (v *view) Rename(oldname, newname string) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	op, ob, err := v.parent(oldname)
	if err != nil {
		return err
	}
	n, ok := op.children[ob]
	if !ok {
		return fs.ErrNotExist
	}
	np, nb, err := v.parent(newname)
	if err != nil {
		return err
	}
	if _, exists := np.children[nb]; exists {
		return fs.ErrExist
	}
	delete(op.children, ob)
	np.children[nb] = n
	return nil
}

func (v *view) RemoveAll(name string) error {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	p, base, err := v.parent(name)
	if err != nil {
		return err
	}
	delete(p.children, base)
	return nil
}

func (v *view) Sub(name string) (migration.FS, error) {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	n, err := v.resolve(name)
	if err != nil {
		return nil, err
	}
	if !n.mode.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", name)
	}
	return &view{h: v.h, root: n}, nil
}

func (v *view) Close() error { return nil }
