package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
)

// MigrationsTable is Bun's bookkeeping table (the migrate package default).
const MigrationsTable = "bun_migrations"

// DefaultKeepSnapshots is how many pre-migration snapshots are retained.
const DefaultKeepSnapshots = 3

// snapshotPrefix/snapshotSuffix frame snapshot file names:
// docker-manager-<UTC timestamp>-<seq>-pre-<first pending migration>.db
const (
	snapshotPrefix = "docker-manager-"
	snapshotSuffix = ".db"
)

// MigrateOptions configures Migrate.
type MigrateOptions struct {
	Migrations    *migrate.Migrations
	SnapshotDir   string
	KeepSnapshots int // DefaultKeepSnapshots when <= 0
	Clock         clock.Clock
	Logger        *slog.Logger
	// NoSnapshot skips the pre-migration snapshot (the metrics database,
	// #5: its data is expendable and can be large).
	NoSnapshot bool
}

// MigrateResult reports what Migrate did.
type MigrateResult struct {
	// Applied lists the migrations applied by this run, in order.
	Applied []string
	// Snapshot is the pre-migration snapshot path, or "" if none was taken.
	Snapshot string
}

// ErrUnknownMigrations means the database was migrated by a newer build.
var ErrUnknownMigrations = errors.New("store: database contains migrations unknown to this build (was it migrated by a newer Docker Manager version?)")

// Status reports applied and pending migration names without modifying the
// database (it does not create Bun's tables).
func Status(ctx context.Context, db bun.IDB, set *migrate.Migrations) (applied, pending []string, err error) {
	var exists int
	if err := db.NewRaw(`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, MigrationsTable).Scan(ctx, &exists); err != nil {
		return nil, nil, fmt.Errorf("store: inspect schema: %w", err)
	}
	if exists > 0 {
		if err := db.NewRaw(`SELECT name FROM ? ORDER BY name`, bun.Ident(MigrationsTable)).Scan(ctx, &applied); err != nil {
			return nil, nil, fmt.Errorf("store: read applied migrations: %w", err)
		}
	}
	known := map[string]bool{}
	for _, m := range set.Sorted() {
		known[m.Name] = true
		if !slices.Contains(applied, m.Name) {
			pending = append(pending, m.Name)
		}
	}
	for _, name := range applied {
		if !known[name] {
			return applied, pending, fmt.Errorf("%w: %s", ErrUnknownMigrations, name)
		}
	}
	return applied, pending, nil
}

// Migrate applies pending migrations. When the database already holds data
// it first writes a consistent VACUUM INTO snapshot to SnapshotDir (keeping
// the newest KeepSnapshots). Each migration runs in its own transaction and
// is only recorded as applied on success, so a failing migration leaves the
// database at its previous state and returns an error; callers must abort
// startup.
func Migrate(ctx context.Context, db *bun.DB, opts MigrateOptions) (MigrateResult, error) {
	var res MigrateResult
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.KeepSnapshots <= 0 {
		opts.KeepSnapshots = DefaultKeepSnapshots
	}

	_, pending, err := Status(ctx, db, opts.Migrations)
	if err != nil {
		return res, err
	}
	if len(pending) == 0 {
		opts.Logger.Info("database schema is up to date")
		return res, nil
	}

	tables, err := UserTables(ctx, db)
	if err != nil {
		return res, fmt.Errorf("store: list tables: %w", err)
	}
	if len(tables) > 0 && !opts.NoSnapshot {
		res.Snapshot, err = Snapshot(ctx, db, opts.SnapshotDir, "pre-"+pending[0], opts.Clock)
		if err != nil {
			return res, err
		}
		opts.Logger.Info("pre-migration snapshot written", "path", res.Snapshot, "pending", len(pending))
		if err := PruneSnapshots(opts.SnapshotDir, opts.KeepSnapshots); err != nil {
			opts.Logger.Warn("could not prune old snapshots", "error", err)
		}
	}

	migrator := migrate.NewMigrator(db, opts.Migrations, migrate.WithMarkAppliedOnSuccess(true))
	if err := migrator.Init(ctx); err != nil {
		return res, fmt.Errorf("store: init migrations: %w", err)
	}
	group, err := migrator.Migrate(ctx)
	if group != nil {
		for _, m := range group.Migrations {
			if m.Name != "" {
				res.Applied = append(res.Applied, m.String())
			}
		}
	}
	if err != nil {
		if len(res.Applied) > 0 {
			// The last entry is the migration that failed and was not recorded.
			res.Applied = res.Applied[:len(res.Applied)-1]
		}
		return res, fmt.Errorf("store: migration failed (pre-migration snapshot: %q): %w", res.Snapshot, err)
	}
	opts.Logger.Info("database migrated", "applied", res.Applied)
	return res, nil
}

// Snapshot writes a consistent copy of the database to dir using VACUUM INTO
// and returns its path. The file is created with mode 0600.
func Snapshot(ctx context.Context, db bun.IDB, dir, label string, clk clock.Clock) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("store: create snapshot dir: %w", err)
	}
	existing, err := ListSnapshots(dir)
	if err != nil {
		return "", fmt.Errorf("store: list snapshots: %w", err)
	}
	// docker-manager-<UTC timestamp>-<2-digit sequence>-<label>.db sorts chronologically.
	stamp := snapshotPrefix + clk.Now().UTC().Format("20060102T150405Z")
	seq := 0
	for _, n := range existing {
		if strings.HasPrefix(n, stamp+"-") {
			seq++
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%02d-%s%s", stamp, seq, sanitizeLabel(label), snapshotSuffix))
	if _, err := db.NewRaw("VACUUM INTO ?", path).Exec(ctx); err != nil {
		return "", fmt.Errorf("store: snapshot: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("store: snapshot permissions: %w", err)
	}
	return path, nil
}

// ListSnapshots returns snapshot file names in dir, oldest first.
func ListSnapshots(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, snapshotPrefix) && strings.HasSuffix(n, snapshotSuffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names) // timestamp prefix sorts chronologically
	return names, nil
}

// PruneSnapshots deletes all but the newest keep snapshots in dir.
func PruneSnapshots(dir string, keep int) error {
	names, err := ListSnapshots(dir)
	if err != nil {
		return err
	}
	var errs []error
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			errs = append(errs, err)
		}
		names = names[1:]
	}
	return errors.Join(errs...)
}

func sanitizeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
