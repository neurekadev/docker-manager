package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/uptrace/bun"
)

// Rolling back an upgrade (#34): downgrades are unsupported, so the
// documented rollback is to stop the manager, restore the pre-migration
// snapshot taken by the upgrade and start the previous image again
// (docs/internal/operations/upgrades.md). RestoreSnapshot is that restore; the
// manager must not be running.

// ErrSnapshotNotFound means the named snapshot does not exist.
var ErrSnapshotNotFound = errors.New("store: no such pre-migration snapshot")

// SnapshotRestore reports what RestoreSnapshot did.
type SnapshotRestore struct {
	// Snapshot is the restored snapshot's file name.
	Snapshot string
	// Applied lists the migrations recorded in the restored database (the
	// schema the previous Docker Manager version expects).
	Applied []string
	// ReplacedDir holds the replaced database files (docker-manager.db and its
	// -wal/-shm), kept so the restore itself can be undone.
	ReplacedDir string
}

// RestoreSnapshot replaces the database at dbPath with the snapshot named
// name from snapshotDir. It verifies the snapshot first (SQLite integrity
// check, readable migration table) on a copy, moves the current database
// files to <data>/replaced-<UTC timestamp>/ and then moves the verified
// copy into place, so a failure leaves the current database untouched.
// Stop the manager before calling it.
func RestoreSnapshot(ctx context.Context, dbPath, snapshotDir, name string, now time.Time) (SnapshotRestore, error) {
	res := SnapshotRestore{Snapshot: name}
	names, err := ListSnapshots(snapshotDir)
	if err != nil {
		return res, fmt.Errorf("store: list snapshots: %w", err)
	}
	if !slices.Contains(names, name) { // also refuses paths and traversal
		return res, fmt.Errorf("%w: %q (list them with `docker-manager snapshots list`)", ErrSnapshotNotFound, name)
	}
	tmp := dbPath + ".restoring"
	removeDB(tmp)
	if err := copyFile(filepath.Join(snapshotDir, name), tmp); err != nil {
		removeDB(tmp)
		return res, fmt.Errorf("store: copy snapshot: %w", err)
	}
	if res.Applied, err = checkRestorable(ctx, tmp); err != nil {
		removeDB(tmp)
		return res, err
	}

	res.ReplacedDir = filepath.Join(filepath.Dir(dbPath), "replaced-"+now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(res.ReplacedDir, 0o700); err != nil {
		removeDB(tmp)
		return res, fmt.Errorf("store: create %s: %w", res.ReplacedDir, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(src, filepath.Join(res.ReplacedDir, filepath.Base(src))); err != nil {
			removeDB(tmp)
			return res, fmt.Errorf("store: move the current database aside: %w", err)
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return res, fmt.Errorf("store: move the restored database into place (the replaced files are in %s): %w", res.ReplacedDir, err)
	}
	return res, nil
}

// checkRestorable opens a copied snapshot, runs SQLite's integrity check
// and reads its applied migrations, then closes it (checkpointing its WAL
// so the single file is complete).
func checkRestorable(ctx context.Context, path string) ([]string, error) {
	db, err := Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("store: open snapshot: %w", err)
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: snapshot integrity check: %w", err)
	}
	if result != "ok" {
		_ = db.Close()
		return nil, fmt.Errorf("store: snapshot failed the integrity check: %s", result)
	}
	var applied []string
	if err := db.NewRaw(`SELECT name FROM ? ORDER BY name`, bun.Ident(MigrationsTable)).Scan(ctx, &applied); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: snapshot has no readable migration table: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: checkpoint snapshot: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	return applied, nil
}

func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // src is a snapshot name from ListSnapshots in the data directory
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is next to the database
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
