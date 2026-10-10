//go:build !linux

package stackarchives

// freeBytes is unknown outside Linux (the manager runs on Linux only).
func freeBytes(string) int64 { return -1 }
