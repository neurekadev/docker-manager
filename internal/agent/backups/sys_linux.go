//go:build linux

package backups

import (
	"io/fs"
	"os"
	"syscall"
)

// freeBytes returns the bytes available to root on the filesystem of dir
// (-1: unknown).
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bfree) * int64(st.Bsize) //nolint:gosec // block counts fit
}

// copyOwner gives target the owner of fi (a restored directory).
func copyOwner(fi fs.FileInfo, target string) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(target, int(st.Uid), int(st.Gid))
	}
}
