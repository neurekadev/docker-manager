//go:build linux

package migration

import (
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

// Sync flushes every filesystem's dirty data to disk (a copy is durable
// before anything starts using it).
func Sync() { syscall.Sync() }
