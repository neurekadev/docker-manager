//go:build windows

package files

import (
	"io/fs"
	"os"
	"syscall"
)

// ownerOf: Windows has no numeric owners; the link count needs a handle
// (openLinks). The agent runs on Linux; this keeps tests runnable.
func ownerOf(fs.FileInfo) (uid, gid uint32, links uint64) { return 0, 0, 0 }

// openLinks returns the hard link count of an opened file.
func openLinks(f *os.File, _ fs.FileInfo) uint64 {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0
	}
	return uint64(d.NumberOfLinks)
}

const openNonBlocking = 0
