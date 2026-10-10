package stackarchives

import (
	"path/filepath"
	"syscall"
)

// freeBytes reports the bytes available to the manager on dir's
// filesystem (-1 when unknown).
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.FromSlash(dir), &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize) //nolint:gosec // block counts fit
}
