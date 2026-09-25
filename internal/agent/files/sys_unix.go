//go:build unix

package files

import (
	"io/fs"
	"os"
	"syscall"
)

// ownerOf returns the numeric owner, group and hard link count of fi.
func ownerOf(fi fs.FileInfo) (uid, gid uint32, links uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid, st.Gid, uint64(st.Nlink) //nolint:unconvert // Nlink is uint32 on some architectures
	}
	return 0, 0, 0
}

// openLinks returns the hard link count of an opened file (from fstat).
func openLinks(_ *os.File, fi fs.FileInfo) uint64 {
	_, _, n := ownerOf(fi)
	return n
}

// openNonBlocking are extra open flags for metadata-only opens (a FIFO
// must not block the open).
const openNonBlocking = syscall.O_NONBLOCK
