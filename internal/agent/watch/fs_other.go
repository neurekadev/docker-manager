//go:build !linux

package watch

// RemoteFilesystem reports whether dir is on a network filesystem. Only
// Linux hosts are supported (#25 Q2); elsewhere (development, tests) every
// directory counts as local.
func RemoteFilesystem(string) bool { return false }

// DefaultMaxWatches is the watch budget where the kernel limit is unknown.
func DefaultMaxWatches() int { return 8192 }
