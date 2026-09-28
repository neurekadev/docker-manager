//go:build linux

package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"
)

func infoOf(fi fs.FileInfo) Info {
	in := Info{Mode: fi.Mode(), Size: fi.Size(), ModTime: fi.ModTime(), AccessTime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		in.UID, in.GID = int(st.Uid), int(st.Gid)
		in.Nlink = uint64(st.Nlink)             //nolint:unconvert // Nlink is uint32 on some architectures
		in.Dev, in.Ino = uint64(st.Dev), st.Ino //nolint:unconvert,gosec // Dev is uint32 on some architectures
		in.AccessTime = time.Unix(st.Atim.Sec, st.Atim.Nsec)
	}
	return in
}

// mkfifoAt creates a FIFO relative to its parent directory opened through
// the root (never through a path that could leave it).
func mkfifoAt(root *os.Root, name string, perm fs.FileMode) error {
	dir, err := root.Open(filepath.FromSlash(parentOf(name)))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return syscall.Mknodat(int(dir.Fd()), path.Base(name), syscall.S_IFIFO|uint32(perm.Perm()), 0) //nolint:gosec // fd fits an int
}

func lchown(root *os.Root, name string, uid, gid int) error { return root.Lchown(name, uid, gid) }

// freeBytes returns the bytes available to root on the filesystem of dir
// (-1: unknown).
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.FromSlash(dir), &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize) //nolint:gosec // block counts fit
}

// SyncTree makes the tree at dir durable before anything uses it: it
// flushes (fsync) every regular file and directory below dir, each
// directory after its entries, and dir last. Only this tree is flushed: a
// host-wide sync(2) waits for the dirty data of every filesystem of the
// host (minutes on a busy one) and cannot be cancelled. Symlinks, FIFOs and
// other special files are never opened; their entries are flushed with
// their directory. It stops when ctx ends. A filesystem that cannot flush
// a directory (EINVAL, ENOTSUP) is not an error.
func SyncTree(ctx context.Context, dir string) error {
	r, err := os.OpenRoot(filepath.FromSlash(dir))
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return syncEntry(ctx, r, ".")
}

// SyncDir flushes the entries of dir (e.g. after a rename into it).
func SyncDir(dir string) error {
	f, err := os.Open(filepath.FromSlash(dir))
	if err != nil {
		return err
	}
	return syncDirFile(f)
}

func syncEntry(ctx context.Context, r *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := r.Lstat(name)
	if err != nil {
		return err
	}
	switch {
	case fi.IsDir():
		f, err := r.Open(name)
		if err != nil {
			return err
		}
		names, err := f.Readdirnames(-1)
		if err != nil {
			_ = f.Close()
			return err
		}
		for _, n := range names {
			if err := syncEntry(ctx, r, filepath.Join(name, n)); err != nil {
				_ = f.Close()
				return err
			}
		}
		return syncDirFile(f)
	case fi.Mode().IsRegular():
		// O_NONBLOCK: an entry swapped for a FIFO meanwhile never blocks
		// the open (it fails the same-file check instead).
		f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		if ofi, err := f.Stat(); err != nil || !os.SameFile(fi, ofi) {
			_ = f.Close()
			return fmt.Errorf("%s changed while it was flushed", name)
		}
		return errors.Join(f.Sync(), f.Close())
	}
	return nil
}

// syncDirFile flushes and closes an open directory.
func syncDirFile(f *os.File) error {
	err := f.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		err = nil // the filesystem does not flush directories
	}
	return errors.Join(err, f.Close())
}
