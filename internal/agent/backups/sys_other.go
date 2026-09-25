//go:build !linux

package backups

import "io/fs"

// freeBytes is unknown outside Linux (the agent is Linux only; tests).
func freeBytes(string) int64 { return -1 }

// copyOwner is a no-op outside Linux.
func copyOwner(fs.FileInfo, string) {}
