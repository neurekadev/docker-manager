package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// A prune that did not finish (cancelled, failed, or the process died)
// leaves unreferenced data in the repository until the next prune. Retention
// prunes only after it forgot snapshots (a prune downloads and rewrites pack
// data), so the executor marks a location's prune as pending before it
// starts and clears the mark once it succeeded; the next retention of that
// location prunes even when it forgets nothing. The marks are files under
// <dir>/prune-pending (the agent's state directory, the manager's data
// directory); dir "" keeps none.

const prunePendingDir = "prune-pending"

func prunePendingPath(dir, repositoryID, scope string) string {
	sum := sha256.Sum256([]byte(repositoryID + "\x00" + scope))
	return filepath.Join(dir, prunePendingDir, hex.EncodeToString(sum[:12]))
}

// PrunePending reports whether the last prune of a location did not finish.
func PrunePending(dir, repositoryID, scope string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(prunePendingPath(dir, repositoryID, scope))
	return err == nil
}

// MarkPrunePending sets (before a prune) or clears (after it succeeded) a
// location's pending-prune mark.
func MarkPrunePending(dir, repositoryID, scope string, pending bool) error {
	if dir == "" {
		return nil
	}
	p := prunePendingPath(dir, repositoryID, scope)
	if !pending {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o600)
}
