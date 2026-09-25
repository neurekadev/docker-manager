//go:build !linux

package observe

import "errors"

// statfs is implemented on Linux only, the only supported agent platform
// (#25 Q2); other builds (developer machines) report no disks.
func statfs(string) (DiskStat, error) {
	return DiskStat{}, errors.New("filesystem statistics are only available on Linux")
}
