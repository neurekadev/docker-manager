//go:build linux

package observe

import (
	"strconv"
	"syscall"
)

// statfs reports a filesystem's usage like df: used = total - free, where
// free counts all free blocks (also those reserved for root). Device
// identifies the filesystem so several roots on one filesystem are reported
// once.
func statfs(path string) (DiskStat, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return DiskStat{}, err
	}
	bs := uint64(s.Bsize) //nolint:gosec // G115: the block size is positive
	total, free := s.Blocks*bs, s.Bfree*bs
	dev := ""
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err == nil {
		dev = strconv.FormatUint(uint64(st.Dev), 10) //nolint:unconvert // Dev is not uint64 on every architecture
	}
	return DiskStat{TotalBytes: clampInt64(total), UsedBytes: clampInt64(total - free), Device: dev}, nil
}
