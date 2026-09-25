package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/migrationtest"
)

// Upgrade tests (#34). The "previous release" is the schema without the
// newest migrations. Its code cannot run in this process (the current
// code expects the current schema), so the tests play its startup at the
// store level: open the database, apply its migration set (the first thing
// every manager does, refusing unknown migrations) and read its data.

// previousRelease is every migration but the newest two, so an upgrade has
// pending migrations.
func previousRelease() *migrate.Migrations { return migrationtest.Previous(migrations.Migrations, 2) }

func names(set *migrate.Migrations) []string {
	var out []string
	for _, m := range set.Sorted() {
		out = append(out, m.Name)
	}
	return out
}

// startPrevious runs the previous release's database startup on dataDir:
// migrate with its set (creating the instance and key on a fresh
// directory) and return the instance ID. It returns the migration error
// (store.ErrUnknownMigrations for a database of a newer release).
func startPrevious(t *testing.T, dataDir string, set *migrate.Migrations) (string, []string, error) {
	t.Helper()
	ctx := testutil.Context(t)
	cfg := testConfig(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	res, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: set, SnapshotDir: cfg.SnapshotDir(), Clock: testutil.FakeClock(),
		Logger: testutil.Logger(t)})
	if err != nil {
		return "", nil, err
	}
	inst, ok, err := store.GetInstance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		if inst, err = store.CreateInstance(ctx, db, testutil.Epoch); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.CreateKeyFile(cfg.SecretKeyFile, nil); err != nil {
			t.Fatal(err)
		}
	}
	return inst.ID, res.Applied, nil
}

// startCurrent starts and stops the current manager on dataDir with set
// and returns its instance ID.
func startCurrent(t *testing.T, dataDir string, set *migrate.Migrations) string {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	addrCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	var id string
	go func() {
		errCh <- Run(ctx, Options{Config: testConfig(dataDir), Logger: testutil.Logger(t), UI: testUI, Clock: testutil.FakeClock(),
			Migrations: set, OnStarted: func(m *Manager) { id = m.Instance().ID }, OnListening: func(a net.Addr) { addrCh <- a }})
	}()
	select {
	case a := <-addrCh:
		if code, body := get(t, "http://"+a.String()+"/api/v1/health/ready"); code != http.StatusOK {
			t.Fatalf("ready: %d %s", code, body)
		}
		cancel()
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	case err := <-errCh:
		t.Fatalf("manager did not start: %v", err)
	}
	return id
}

// TestUpgradeWithPendingMigrationsLeavesSnapshot (#34, #2): the current
// manager started on the previous release's database applies the pending
// migrations after writing a pre-migration snapshot; the data is kept and
// the snapshot holds exactly the previous release's schema.
func TestUpgradeWithPendingMigrationsLeavesSnapshot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	prev := previousRelease()
	before, _, err := startPrevious(t, dataDir, prev)
	if err != nil {
		t.Fatal(err)
	}
	if got := appliedMigrations(t, dataDir); !slices.Equal(got, names(prev)) {
		t.Fatalf("previous release applied %v", got)
	}

	if after := startCurrent(t, dataDir, nil); after != before {
		t.Fatalf("instance changed across the upgrade: %s -> %s", before, after)
	}
	if got := appliedMigrations(t, dataDir); !slices.Equal(got, names(migrations.Migrations)) {
		t.Fatalf("upgrade applied %v", got)
	}
	snaps, _ := store.ListSnapshots(filepath.Join(dataDir, config.SnapshotDirName))
	pending := names(migrations.Migrations)[len(names(prev))]
	if len(snaps) != 1 || !strings.Contains(snaps[0], "pre-"+pending) {
		t.Fatalf("snapshots %v, want one before %s", snaps, pending)
	}
	db, err := store.Open(testutil.Context(t), filepath.Join(dataDir, config.SnapshotDirName, snaps[0]))
	if err != nil {
		t.Fatal(err)
	}
	applied, pend, err := store.Status(testutil.Context(t), db, prev)
	_ = db.Close()
	if err != nil || !slices.Equal(applied, names(prev)) || len(pend) != 0 {
		t.Fatalf("snapshot schema %v pending %v: %v", applied, pend, err)
	}
}

// TestFailedUpgradeRollsBackWithSnapshot (#34): an upgrade whose
// migration fails aborts startup (nothing listens). The previous release
// cannot open the partly migrated database (downgrades are unsupported),
// but after restoring the pre-migration snapshot it starts with its data
// and schema: the documented rollback. A later upgrade works again.
func TestFailedUpgradeRollsBackWithSnapshot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	prev := previousRelease()
	before, _, err := startPrevious(t, dataDir, prev)
	if err != nil {
		t.Fatal(err)
	}

	// The new release's last migration fails.
	err = Run(testutil.Context(t), Options{Config: testConfig(dataDir), Logger: testutil.Logger(t), UI: testUI, Clock: testutil.FakeClock(),
		Migrations: migrationtest.WithFailing(migrations.Migrations),
		Listen: func(string, string) (net.Listener, error) {
			t.Error("listener bound despite the failed migration")
			return nil, errors.New("must not listen")
		}})
	if err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("upgrade = %v, want the migration failure", err)
	}
	snapDir := filepath.Join(dataDir, config.SnapshotDirName)
	snaps, _ := store.ListSnapshots(snapDir)
	if len(snaps) != 1 {
		t.Fatalf("snapshots %v", snaps)
	}

	// The migrations before the failing one were applied: the previous
	// release refuses the database.
	if _, _, err := startPrevious(t, dataDir, prev); !errors.Is(err, store.ErrUnknownMigrations) {
		t.Fatalf("previous release on the upgraded database = %v, want ErrUnknownMigrations", err)
	}

	// Rollback: restore the snapshot, start the previous release.
	res, err := store.RestoreSnapshot(testutil.Context(t), filepath.Join(dataDir, config.DatabaseFileName), snapDir, snaps[0], testutil.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Applied, names(prev)) || res.ReplacedDir == "" {
		t.Fatalf("restore %+v", res)
	}
	got, applied, err := startPrevious(t, dataDir, prev)
	if err != nil || got != before || len(applied) != 0 {
		t.Fatalf("previous release after the rollback: instance %s (want %s), applied %v, %v", got, before, applied, err)
	}
	if got := appliedMigrations(t, dataDir); !slices.Equal(got, names(prev)) {
		t.Fatalf("schema after rollback %v", got)
	}
	// The replaced (partly upgraded) database is kept for inspection.
	replaced, err := store.Open(testutil.Context(t), filepath.Join(res.ReplacedDir, config.DatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Status(testutil.Context(t), replaced, prev); !errors.Is(err, store.ErrUnknownMigrations) {
		t.Fatalf("replaced database status %v", err)
	}
	_ = replaced.Close()
	// Once the failing migration is fixed, the upgrade succeeds.
	if after := startCurrent(t, dataDir, nil); after != before {
		t.Fatalf("instance after the retried upgrade %s, want %s", after, before)
	}
}
