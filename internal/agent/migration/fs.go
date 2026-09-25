package migration

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Info is the metadata of one filesystem entry, read without following a
// symlink in the final component.
type Info struct {
	// Mode carries the type bits and the permission bits including
	// setuid, setgid and sticky.
	Mode       fs.FileMode
	Size       int64
	UID, GID   int
	ModTime    time.Time
	AccessTime time.Time
	// Nlink, Dev and Ino identify hard links.
	Nlink    uint64
	Dev, Ino uint64
}

// FS is a directory tree rooted at one directory (a project directory, a
// volume's data directory, the stacks volume). Every name is relative to
// the root ("." is the root), slash-separated, and never resolves outside
// it: the production implementation is an os.Root, so symlinks are never
// followed out of the root, and the migration code never follows them at
// all (Lstat, Readlink, no-follow opens). Implementations for tests live in
// migrationtest.
type FS interface {
	Lstat(name string) (Info, error)
	// ReadDir returns the directory's entry names, sorted.
	ReadDir(name string) ([]string, error)
	// Open opens a regular file for reading; it fails when name is not a
	// regular file (a symlink is not followed).
	Open(name string) (io.ReadCloser, error)
	Readlink(name string) (string, error)
	Mkdir(name string, perm fs.FileMode) error
	// MkdirAll creates name and its missing parents.
	MkdirAll(name string, perm fs.FileMode) error
	// Create creates a new regular file (it must not exist).
	Create(name string) (io.WriteCloser, error)
	Symlink(target, name string) error
	Link(oldname, newname string) error
	Mkfifo(name string, perm fs.FileMode) error
	// Lchown changes a file's numeric owner (a symlink itself, not its
	// target).
	Lchown(name string, uid, gid int) error
	// Chmod and Chtimes are never called on symlinks.
	Chmod(name string, mode fs.FileMode) error
	Chtimes(name string, atime, mtime time.Time) error
	Rename(oldname, newname string) error
	RemoveAll(name string) error
	// Sub opens the directory name as an FS of its own (it stays inside
	// this root).
	Sub(name string) (FS, error)
	Close() error
}

// Opener opens the directory at a host path (identical inside the agent,
// #28) as an FS.
type Opener func(dir string) (FS, error)

// OSOpener opens real directories through os.Root. The directory itself
// must not be a symlink.
func OSOpener(dir string) (FS, error) {
	local := filepath.FromSlash(dir)
	fi, err := os.Lstat(local)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	r, err := os.OpenRoot(local)
	if err != nil {
		return nil, err
	}
	return &osFS{root: r}, nil
}

type osFS struct{ root *os.Root }

func osName(name string) string {
	if name == "" {
		return "."
	}
	return filepath.FromSlash(name)
}

func (o *osFS) Lstat(name string) (Info, error) {
	fi, err := o.root.Lstat(osName(name))
	if err != nil {
		return Info{}, err
	}
	return infoOf(fi), nil
}

func (o *osFS) ReadDir(name string) ([]string, error) {
	fi, err := o.root.Lstat(osName(name))
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", name)
	}
	f, err := o.root.Open(osName(name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// The directory must still be the one Lstat saw (not a symlink swapped
	// in meanwhile).
	if ofi, err := f.Stat(); err != nil || !os.SameFile(fi, ofi) {
		return nil, fmt.Errorf("%s changed while it was read", name)
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

func (o *osFS) Open(name string) (io.ReadCloser, error) {
	lfi, err := o.root.Lstat(osName(name))
	if err != nil {
		return nil, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	f, err := o.root.OpenFile(osName(name), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !os.SameFile(lfi, fi) || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s changed while it was opened", name)
	}
	return f, nil
}

func (o *osFS) Readlink(name string) (string, error) { return o.root.Readlink(osName(name)) }

func (o *osFS) Mkdir(name string, perm fs.FileMode) error { return o.root.Mkdir(osName(name), perm) }

func (o *osFS) MkdirAll(name string, perm fs.FileMode) error {
	return o.root.MkdirAll(osName(name), perm)
}

func (o *osFS) Create(name string) (io.WriteCloser, error) {
	return o.root.OpenFile(osName(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func (o *osFS) Symlink(target, name string) error { return o.root.Symlink(target, osName(name)) }

func (o *osFS) Link(oldname, newname string) error {
	return o.root.Link(osName(oldname), osName(newname))
}

func (o *osFS) Mkfifo(name string, perm fs.FileMode) error { return mkfifoAt(o.root, name, perm) }

func (o *osFS) Lchown(name string, uid, gid int) error {
	return lchown(o.root, osName(name), uid, gid)
}

func (o *osFS) Chmod(name string, mode fs.FileMode) error { return o.root.Chmod(osName(name), mode) }

func (o *osFS) Chtimes(name string, atime, mtime time.Time) error {
	return o.root.Chtimes(osName(name), atime, mtime)
}

func (o *osFS) Rename(oldname, newname string) error {
	return o.root.Rename(osName(oldname), osName(newname))
}

func (o *osFS) RemoveAll(name string) error {
	if name == "." || name == "" {
		return errors.New("refusing to remove the root")
	}
	return o.root.RemoveAll(osName(name))
}

func (o *osFS) Sub(name string) (FS, error) {
	r, err := o.root.OpenRoot(osName(name))
	if err != nil {
		return nil, err
	}
	return &osFS{root: r}, nil
}

func (o *osFS) Close() error { return o.root.Close() }

// cleanName validates an archive member name: relative, slash-separated,
// no "." or ".." segments, no backslashes or control characters. "./" and
// "" are the root ".".
func cleanName(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return ".", true
	}
	if len(name) > 4096 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", false
			}
		}
	}
	return path.Clean(name), true
}

// parentOf returns the parent of a clean member name ("." for top-level).
func parentOf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return "."
}
