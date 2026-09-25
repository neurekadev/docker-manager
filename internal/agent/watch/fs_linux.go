//go:build linux

package watch

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// remoteMagic are filesystem types whose changes made elsewhere (another
// client, the server) never reach local inotify, or whose inotify support
// is incomplete (FUSE): such scopes are polled (#23, #25 Q5).
var remoteMagic = map[int64]string{
	0x6969:     "nfs",
	0x517b:     "smb",
	0xff534d42: "cifs",
	0xfe534d42: "smb2",
	0x65735546: "fuse",
	0x564c:     "ncp",
	0x5346414f: "afs",
	0x01021997: "v9fs",
	0x47504653: "gpfs",
	0x0bd00bd0: "lustre", //nolint:misspell // the Lustre filesystem
	0x00c36400: "ceph",
	0x19830326: "fhgfs",
}

// RemoteFilesystem reports whether dir is on a network or FUSE filesystem.
func RemoteFilesystem(dir string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false
	}
	_, ok := remoteMagic[int64(st.Type)] //nolint:unconvert // Type is int32 on some architectures
	return ok
}

// DefaultMaxWatches is half the kernel's fs.inotify.max_user_watches (the
// limit is per user and shared with every other root process on the host,
// including other containers), at least 1 024 and at most 524 288; 8 192
// when it cannot be read.
func DefaultMaxWatches() int {
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return 8192
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 {
		return 8192
	}
	return min(max(n/2, 1024), 524288)
}
