package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/sqliteshim"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/migrationtest"
)

func TestDriverIsPureGo(t *testing.T) {
	// sqliteshim selects modernc.org/sqlite ("sqlite") on linux/amd64,
	// linux/arm64 and windows/amd64 regardless of cgo, unless the cgosqlite
	// build tag is set. Docker Manager must never link the cgo driver.
	if got := sqliteshim.DriverName(); got != "sqlite" {
		t.Fatalf("sqliteshim driver = %q, want modernc %q", got, "sqlite")
	}
}

func TestDSN(t *testing.T) {
	dsn := DSN("/data/docker-manager.db")
	for _, want := range []string{"/data/docker-manager.db?", "_txlock=immediate", "_pragma=busy_timeout(5000)", "_pragma=journal_mode(WAL)", "_pragma=foreign_keys(1)", "_pragma=synchronous(NORMAL)"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q lacks %q", dsn, want)
		}
	}
}

func openTemp(t *testing.T) (*bun.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(testutil.Context(t), filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dir
}

func TestOpenAppliesPragmas(t *testing.T) {
	db, _ := openTemp(t)
	ctx := testutil.Context(t)
	checks := map[string]string{"journal_mode": "wal", "foreign_keys": "1", "synchronous": "1", "busy_timeout": "5000"}
	for pragma, want := range checks {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "bad?.db")); err == nil {
		t.Error("expected error for path with '?'")
	}
}

func migrateOpts(t *testing.T, dir string, set *migrate.Migrations) MigrateOptions {
	return MigrateOptions{
		Migrations:  set,
		SnapshotDir: filepath.Join(dir, "snapshots"),
		Clock:       testutil.FakeClock(),
		Logger:      testutil.Logger(t),
	}
}

func TestMigrateFreshThenNoReplay(t *testing.T) {
	db, dir := openTemp(t)
	ctx := testutil.Context(t)

	res, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != len(migrations.Migrations.Sorted()) {
		t.Fatalf("applied %v", res.Applied)
	}
	if res.Snapshot != "" {
		t.Fatalf("snapshot taken for an empty database: %s", res.Snapshot)
	}
	applied, pending, err := Status(ctx, db, migrations.Migrations)
	if err != nil || len(pending) != 0 || len(applied) == 0 {
		t.Fatalf("status applied=%v pending=%v err=%v", applied, pending, err)
	}

	res, err = Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || res.Snapshot != "" {
		t.Fatalf("second run replayed: %+v", res)
	}
	again, _, _ := Status(ctx, db, migrations.Migrations)
	if !slices.Equal(applied, again) {
		t.Fatalf("applied set changed: %v -> %v", applied, again)
	}
	if snaps, _ := ListSnapshots(filepath.Join(dir, "snapshots")); len(snaps) != 0 {
		t.Fatalf("unexpected snapshots %v", snaps)
	}
}

func TestFailingMigrationLeavesDatabaseUnchanged(t *testing.T) {
	db, dir := openTemp(t)
	ctx := testutil.Context(t)
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateInstance(ctx, db, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	tablesBefore, _ := UserTables(ctx, db)
	appliedBefore, _, _ := Status(ctx, db, migrations.Migrations)

	set := migrationtest.WithFailing(migrations.Migrations)
	res, err := Migrate(ctx, db, migrateOpts(t, dir, set))
	if err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	if res.Snapshot == "" {
		t.Fatal("no pre-migration snapshot")
	}
	if _, err := os.Stat(res.Snapshot); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}

	tablesAfter, _ := UserTables(ctx, db)
	if !slices.Equal(tablesBefore, tablesAfter) {
		t.Fatalf("tables changed: %v -> %v", tablesBefore, tablesAfter)
	}
	appliedAfter, pending, _ := Status(ctx, db, set)
	if !slices.Equal(appliedBefore, appliedAfter) || !slices.Equal(pending, []string{migrationtest.FailingName}) {
		t.Fatalf("applied %v -> %v, pending %v", appliedBefore, appliedAfter, pending)
	}

	// The snapshot is a complete, openable database holding the pre-migration data.
	snap, err := Open(ctx, res.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	if _, ok, err := GetInstance(ctx, snap); err != nil || !ok {
		t.Fatalf("snapshot lacks instance row: ok=%v err=%v", ok, err)
	}
}

func TestUnknownAppliedMigrationFailsClosed(t *testing.T) {
	db, dir := openTemp(t)
	ctx := testutil.Context(t)
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO bun_migrations (name, group_id) VALUES ('30000101000000', 99)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); !errors.Is(err, ErrUnknownMigrations) {
		t.Fatalf("err = %v, want ErrUnknownMigrations", err)
	}
}

func TestSnapshotRetention(t *testing.T) {
	db, dir := openTemp(t)
	ctx := testutil.Context(t)
	snapDir := filepath.Join(dir, "snapshots")
	clk := testutil.FakeClock()
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	var made []string
	for i := 0; i < 5; i++ {
		p, err := Snapshot(ctx, db, snapDir, "pre-x", clk)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, filepath.Base(p))
		if i%2 == 1 {
			clk.Advance(time.Second)
		}
	}
	if err := PruneSnapshots(snapDir, 3); err != nil {
		t.Fatal(err)
	}
	left, _ := ListSnapshots(snapDir)
	if !slices.Equal(left, made[2:]) {
		t.Fatalf("kept %v, want newest %v", left, made[2:])
	}
}

func TestInstanceRoundTrip(t *testing.T) {
	db, dir := openTemp(t)
	ctx := testutil.Context(t)
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := GetInstance(ctx, db); err != nil || ok {
		t.Fatalf("fresh db has instance: %v %v", ok, err)
	}
	created, err := CreateInstance(ctx, db, testutil.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := GetInstance(ctx, db)
	if err != nil || !ok || got.ID != created.ID || !got.CreatedAt.Equal(testutil.Epoch) {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if _, err := CreateInstance(ctx, db, testutil.Epoch); err == nil {
		t.Fatal("second instance row accepted")
	}
}
