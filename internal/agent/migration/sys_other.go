//go:build !linux

package migration

import (
	"errors"
	"io/fs"
	"os"
)

// The agent runs on Linux only (#25 Q2). Other platforms build the package
// for tests: numeric owners, FIFOs and free space are not available there
// (the Linux-only tests and the in-memory FS of migrationtest cover them).

func infoOf(fi fs.FileInfo) Info {
	return Info{Mode: fi.Mode(), Size: fi.Size(), ModTime: fi.ModTime(), AccessTime: fi.ModTime(), Nlink: 1}
}

func mkfifoAt(*os.Root, string, fs.FileMode) error {
	return errors.New("FIFOs are not supported on this platform")
}

// lchown is a no-op: the platform has no numeric owners.
func lchown(*os.Root, string, int, int) error { return nil }

func freeBytes(string) int64 { return -1 }
