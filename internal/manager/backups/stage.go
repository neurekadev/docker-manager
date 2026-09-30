package backups

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Staging a manager-state restore prepared outside this package: the copy
// a moving manager handed over (internal/manager/managermove) is applied
// by the same startup path as a backup import (ApplyPendingRestore), so it
// shares PendingRestoreDir, the marker and the file names.

// StageRestore stages a prepared manager state for the next start: dir
// (below dataDir, on the same file system) receives the database file db,
// the template drafts archive drafts ("" none), the secret key and the
// marker mk, then becomes PendingRestoreDir (replacing a stale one). The
// next start applies it (ApplyPendingRestore).
func StageRestore(dataDir, dir, db, drafts string, key secrets.Key, mk RestoreMarker) error {
	if mk.Format == "" {
		mk.Format, mk.Version = RestoreMarkerFormat, 1
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := moveInto(db, filepath.Join(dir, restoredDBFile)); err != nil {
		return fmt.Errorf("stage the database: %w", err)
	}
	if drafts != "" {
		if err := moveInto(drafts, filepath.Join(dir, restoredDraftsFile)); err != nil {
			return fmt.Errorf("stage the template drafts: %w", err)
		}
	}
	if err := secrets.ReplaceKeyFile(filepath.Join(dir, restoredKeyFile), key); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, restoreMarkerFile), mk); err != nil {
		return err
	}
	pending := filepath.Join(dataDir, PendingRestoreDir)
	if err := os.RemoveAll(pending); err != nil {
		return err
	}
	if err := os.Rename(dir, pending); err != nil {
		return fmt.Errorf("stage the restore: %w", err)
	}
	return nil
}

// RestorePending reports whether a staged restore waits for the next start.
func RestorePending(dataDir string) bool {
	return exists(filepath.Join(dataDir, PendingRestoreDir, restoreMarkerFile))
}

func moveInto(src, dst string) error {
	if src == dst {
		return nil
	}
	if !exists(src) {
		return fmt.Errorf("%s is missing", filepath.Base(src))
	}
	return os.Rename(src, dst)
}

// ErrSealedSettings means a database's sealed settings do not open with
// the key that should protect them.
var ErrSealedSettings = errors.New("the secret key does not decrypt the database's sealed settings")

// CheckSealedSettings proves that key protects db's sealed settings: when
// the database holds a Recovery Key record, key must open it
// (ErrSealedSettings otherwise). A database without one passes.
func CheckSealedSettings(ctx context.Context, db bun.IDB, key secrets.Key) error {
	rec, found, err := store.GetBackupKey(ctx, db)
	if err != nil {
		return err
	}
	if !found || rec.State.Generation == 0 || rec.Sealed.Current == "" {
		return nil
	}
	b, err := secrets.NewKeyring(key).Open(rec.Sealed.Current, sealCurrent)
	if err != nil {
		return ErrSealedSettings
	}
	if _, err := ParseRecoveryKey(string(b)); err != nil {
		return ErrSealedSettings
	}
	return nil
}
