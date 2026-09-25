package migration

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrUnsafeArchive is returned for a member the destination refuses: a
// name escaping the root, a member below a symlink or file, a duplicate,
// a hard link to anything but an earlier regular file, a device node.
var ErrUnsafeArchive = errors.New("unsafe archive member")

// ExtractStats describes an extracted tree.
type ExtractStats struct {
	Entries int64
	Bytes   int64
}

// ExtractOptions tunes ExtractTree.
type ExtractOptions struct {
	// MaxBytes bounds the regular files' content (0: unbounded), e.g. the
	// destination's free space.
	MaxBytes int64
}

// ExtractTree writes the archive read from r into fsys, whose root must be
// a new, empty directory (a staging directory or a new volume): it
// recreates directories, regular files, symlinks (never followed: their
// target is only text), hard links to earlier members and FIFOs, then
// applies numeric owners, modes (special bits included, after chown, which
// would clear them) and times (directories last, deepest first, so
// writing their children does not change them). Members are validated
// before anything is written for them; see ErrUnsafeArchive.
func ExtractTree(ctx context.Context, fsys FS, r io.Reader, o ExtractOptions) (ExtractStats, error) {
	tr := tar.NewReader(r)
	var st ExtractStats
	kind := map[string]byte{".": tar.TypeDir} // name -> type of every member so far
	var dirs []*tar.Header
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, err
		}
		name, ok := cleanName(hdr.Name)
		if !ok {
			return st, fmt.Errorf("%w: invalid name %q", ErrUnsafeArchive, hdr.Name)
		}
		if st.Entries++; st.Entries > MaxEntries {
			return st, fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, MaxEntries)
		}
		if (name == ".") != (st.Entries == 1) || (name == "." && hdr.Typeflag != tar.TypeDir) {
			return st, fmt.Errorf("%w: the root directory must be the first member (and only that)", ErrUnsafeArchive)
		}
		if name == "." {
			dirs = append(dirs, hdr)
			continue
		}
		if _, dup := kind[name]; dup {
			return st, fmt.Errorf("%w: duplicate member %s", ErrUnsafeArchive, name)
		}
		// Every parent must be a directory created by this archive: nothing
		// is ever written below a symlink or a file.
		if kind[parentOf(name)] != tar.TypeDir {
			return st, fmt.Errorf("%w: %s is not inside an archived directory", ErrUnsafeArchive, name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := fsys.Mkdir(name, 0o700); err != nil {
				return st, fmt.Errorf("%s: %w", name, err)
			}
			dirs = append(dirs, hdr)
		case tar.TypeReg:
			if o.MaxBytes > 0 && st.Bytes+hdr.Size > o.MaxBytes {
				return st, fmt.Errorf("%w: the data exceeds %d bytes", ErrUnsafeArchive, o.MaxBytes)
			}
			if err := writeFile(ctx, fsys, name, tr, hdr.Size); err != nil {
				return st, err
			}
			st.Bytes += hdr.Size
		case tar.TypeSymlink:
			if hdr.Linkname == "" || strings.ContainsRune(hdr.Linkname, 0) {
				return st, fmt.Errorf("%w: symlink %s without a target", ErrUnsafeArchive, name)
			}
			if err := fsys.Symlink(hdr.Linkname, name); err != nil {
				return st, fmt.Errorf("%s: %w", name, err)
			}
		case tar.TypeLink:
			target, ok := cleanName(hdr.Linkname)
			if !ok || kind[target] != tar.TypeReg {
				return st, fmt.Errorf("%w: hard link %s must point to an earlier regular file", ErrUnsafeArchive, name)
			}
			if err := fsys.Link(target, name); err != nil {
				return st, fmt.Errorf("%s: %w", name, err)
			}
		case tar.TypeFifo:
			if err := fsys.Mkfifo(name, 0o600); err != nil {
				return st, fmt.Errorf("%s: %w", name, err)
			}
		default:
			return st, fmt.Errorf("%w: %s has unsupported type %q", ErrUnsafeArchive, name, hdr.Typeflag)
		}
		kind[name] = hdr.Typeflag
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeLink {
			if err := applyMeta(fsys, name, hdr); err != nil {
				return st, err
			}
		}
	}
	if len(dirs) == 0 {
		return st, fmt.Errorf("%w: the archive has no root directory", ErrUnsafeArchive)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		name, _ := cleanName(dirs[i].Name)
		if err := applyMeta(fsys, name, dirs[i]); err != nil {
			return st, err
		}
	}
	return st, nil
}

func writeFile(ctx context.Context, fsys FS, name string, r io.Reader, size int64) error {
	f, err := fsys.Create(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	n, err := io.Copy(f, ctxReader{ctx, io.LimitReader(r, size)})
	cerr := f.Close()
	if err == nil && n != size {
		err = fmt.Errorf("%s: short content (%d of %d bytes)", name, n, size)
	}
	if err == nil {
		err = cerr
	}
	return err
}

// applyMeta sets owner, mode and times (chown first: it clears setuid and
// setgid bits on Linux). Symlinks get their owner only.
func applyMeta(fsys FS, name string, hdr *tar.Header) error {
	if err := fsys.Lchown(name, hdr.Uid, hdr.Gid); err != nil {
		return fmt.Errorf("%s: chown: %w", name, err)
	}
	if hdr.Typeflag == tar.TypeSymlink {
		return nil
	}
	if err := fsys.Chmod(name, fileMode(hdr.Mode)); err != nil {
		return fmt.Errorf("%s: chmod: %w", name, err)
	}
	atime := hdr.AccessTime
	if atime.IsZero() {
		atime = hdr.ModTime
	}
	if err := fsys.Chtimes(name, atime, hdr.ModTime); err != nil {
		return fmt.Errorf("%s: chtimes: %w", name, err)
	}
	return nil
}

// Stat helpers for tests and previews.

// IsEmptyDir reports whether name is an empty directory.
func IsEmptyDir(fsys FS, name string) bool {
	info, err := fsys.Lstat(name)
	if err != nil || !info.Mode.IsDir() {
		return false
	}
	names, err := fsys.ReadDir(name)
	return err == nil && len(names) == 0
}
