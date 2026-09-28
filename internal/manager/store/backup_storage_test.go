package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func storageRepos(t *testing.T, ctx context.Context, db bun.IDB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		r := testRepository(id)
		if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
			t.Fatal(err)
		}
	}
}

func measure(t *testing.T, ctx context.Context, db bun.IDB, repo, scope string, at time.Time, size, uncompressed int64) {
	t.Helper()
	if err := UpsertBackupLocation(ctx, db, repo, scope, LocationUpdate{Stats: &domain.LocationStats{SizeBytes: size,
		UncompressedBytes: uncompressed}}, at); err != nil {
		t.Fatal(err)
	}
}

func storageSamples(t *testing.T, ctx context.Context, db bun.IDB) []domain.BackupStorageSample {
	t.Helper()
	far := testutil.Epoch.Add(100 * 365 * 24 * time.Hour)
	s, err := ListBackupStorageSamples(ctx, db, time.Time{}, far, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type storageValue struct {
	known        bool
	size, uncomp int64
}

func storageValues(points []domain.BackupStoragePoint) []storageValue {
	out := make([]storageValue, 0, len(points))
	for _, p := range points {
		out = append(out, storageValue{p.Known, p.SizeBytes, p.UncompressedBytes})
	}
	return out
}

// TestBackupStorageSamplesPerMeasurement: every stats update of a
// location appends a sample (other location updates do not); a later
// measurement in the same UTC hour replaces the earlier one.
func TestBackupStorageSamplesPerMeasurement(t *testing.T) {
	ctx, db := backupTestDB(t)
	storageRepos(t, ctx, db, "r1")
	t0 := testutil.Epoch // 12:00 UTC
	measure(t, ctx, db, "r1", "env:e1", t0.Add(5*time.Minute), 100, 300)
	measure(t, ctx, db, "r1", "env:e1", t0.Add(40*time.Minute), 110, 320) // same hour: replaces
	measure(t, ctx, db, "r1", "env:e1", t0.Add(70*time.Minute), 120, 340)
	measure(t, ctx, db, "r1", "manager", t0.Add(10*time.Minute), 7, 9)
	// A verification updates the location without measuring it.
	verified := t0.Add(2 * time.Hour)
	if err := UpsertBackupLocation(ctx, db, "r1", "env:e1", LocationUpdate{VerifyResult: "ok", VerifiedAt: &verified}, verified); err != nil {
		t.Fatal(err)
	}

	got := storageSamples(t, ctx, db)
	want := []domain.BackupStorageSample{
		{RepositoryID: "r1", Scope: "manager", At: t0.Add(10 * time.Minute), SizeBytes: 7, UncompressedBytes: 9},
		{RepositoryID: "r1", Scope: "env:e1", At: t0.Add(40 * time.Minute), SizeBytes: 110, UncompressedBytes: 320},
		{RepositoryID: "r1", Scope: "env:e1", At: t0.Add(70 * time.Minute), SizeBytes: 120, UncompressedBytes: 340},
	}
	if !slices.EqualFunc(got, want, func(a, b domain.BackupStorageSample) bool {
		return a.RepositoryID == b.RepositoryID && a.Scope == b.Scope && a.At.Equal(b.At) && a.SizeBytes == b.SizeBytes &&
			a.UncompressedBytes == b.UncompressedBytes
	}) {
		t.Errorf("samples\n got %+v\nwant %+v", got, want)
	}
}

// TestBackupStorageHistoryCarriesForward: each point sums every
// location's latest sample at or before it, a sample before the range
// carries into it, points before the first sample are unknown, and the
// scope and repository filters leave locations out.
func TestBackupStorageHistoryCarriesForward(t *testing.T) {
	ctx, db := backupTestDB(t)
	storageRepos(t, ctx, db, "r1", "r2")
	day := 24 * time.Hour
	d0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	measure(t, ctx, db, "r1", "env:e1", d0.Add(-3*day), 50, 60)             // before the range: carried in
	measure(t, ctx, db, "r1", "env:e1", d0.Add(day+2*time.Hour), 80, 100)   // day 1, 02:00
	measure(t, ctx, db, "r2", "env:e1", d0.Add(2*day+6*time.Hour), 10, 10)  // day 2, 06:00
	measure(t, ctx, db, "r1", "manager", d0.Add(2*day+6*time.Hour), 5, 15)  // day 2, 06:00
	measure(t, ctx, db, "r1", "env:e1", d0.Add(3*day+23*time.Hour), 70, 90) // after the range

	from, to := d0.Add(12*time.Hour), d0.Add(3*day+12*time.Hour)
	q := domain.BackupStorageQuery{From: from, To: to, Step: day}
	points, err := BackupStorageHistory(ctx, db, q)
	if err != nil {
		t.Fatal(err)
	}
	wantAt := []time.Time{from, d0.Add(day), d0.Add(2 * day), d0.Add(3 * day), to}
	if len(points) != len(wantAt) {
		t.Fatalf("points %+v", points)
	}
	for i, p := range points {
		if !p.At.Equal(wantAt[i]) {
			t.Errorf("point %d at %s, want %s", i, p.At, wantAt[i])
		}
	}
	want := []storageValue{{true, 50, 60}, {true, 50, 60}, {true, 80, 100}, {true, 95, 125}, {true, 95, 125}}
	if got := storageValues(points); !slices.Equal(got, want) {
		t.Errorf("all locations %+v, want %+v", got, want)
	}

	// One environment: the manager state is left out.
	q.Scope = "env:e1"
	points, _ = BackupStorageHistory(ctx, db, q)
	want = []storageValue{{true, 50, 60}, {true, 50, 60}, {true, 80, 100}, {true, 90, 110}, {true, 90, 110}}
	if got := storageValues(points); !slices.Equal(got, want) {
		t.Errorf("environment e1 %+v, want %+v", got, want)
	}

	// Only r2 counts: unknown until its first measurement.
	q.Scope, q.Repository = "", func(id string) bool { return id == "r2" }
	points, _ = BackupStorageHistory(ctx, db, q)
	want = []storageValue{{}, {}, {}, {true, 10, 10}, {true, 10, 10}}
	if got := storageValues(points); !slices.Equal(got, want) {
		t.Errorf("repository r2 %+v, want %+v", got, want)
	}
}

// TestBackupStorageRemovedRepositoryStopsCounting: a removed repository
// counts until its removal and zero afterwards; the other repositories
// are unchanged.
func TestBackupStorageRemovedRepositoryStopsCounting(t *testing.T) {
	ctx, db := backupTestDB(t)
	storageRepos(t, ctx, db, "r1", "r2")
	t0 := testutil.Epoch
	measure(t, ctx, db, "r1", "env:e1", t0, 100, 200)
	measure(t, ctx, db, "r1", "manager", t0, 10, 20)
	measure(t, ctx, db, "r2", "env:e1", t0, 1, 1)

	removedAt := t0.Add(5 * time.Hour)
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := DeleteBackupRepository(ctx, tx, "r1", 1); err != nil {
			return err
		}
		return EndBackupStorage(ctx, tx, "r1", removedAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	points, err := BackupStorageHistory(ctx, db, domain.BackupStorageQuery{From: t0.Add(time.Hour), To: t0.Add(10 * time.Hour), Step: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range points {
		want := storageValue{true, 111, 221}
		if !p.At.Before(removedAt) {
			want = storageValue{true, 1, 1}
		}
		if got := (storageValue{p.Known, p.SizeBytes, p.UncompressedBytes}); got != want {
			t.Errorf("at %s: %+v, want %+v", p.At, got, want)
		}
	}
}

// TestBackupStoragePrune: samples older than the retention go, except
// each location's newest older one (its value carries into the kept
// range); a zero one without anything newer goes too.
func TestBackupStoragePrune(t *testing.T) {
	ctx, db := backupTestDB(t)
	storageRepos(t, ctx, db, "r1", "r2")
	now := testutil.Epoch
	old := now.Add(-BackupStorageRetention)
	measure(t, ctx, db, "r1", "env:e1", old.Add(-72*time.Hour), 1, 1)
	measure(t, ctx, db, "r1", "env:e1", old.Add(-48*time.Hour), 2, 2) // the newest older one: stays
	measure(t, ctx, db, "r1", "env:e1", old.Add(24*time.Hour), 3, 3)
	measure(t, ctx, db, "r2", "env:e1", old.Add(-96*time.Hour), 4, 4)
	if err := EndBackupStorage(ctx, db, "r2", old.Add(-90*time.Hour)); err != nil { // removed long ago
		t.Fatal(err)
	}
	// Appending a sample prunes.
	measure(t, ctx, db, "r1", "manager", now, 9, 9)

	var got []storageValue
	for _, s := range storageSamples(t, ctx, db) {
		got = append(got, storageValue{true, s.SizeBytes, s.UncompressedBytes})
	}
	want := []storageValue{{true, 2, 2}, {true, 3, 3}, {true, 9, 9}}
	if !slices.Equal(got, want) {
		t.Errorf("kept %+v, want %+v", got, want)
	}
}

// TestStorageTimes: from, the whole UTC hours or days in between, and to.
func TestStorageTimes(t *testing.T) {
	from := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	got := storageTimes(from, from.Add(3*time.Hour), time.Hour)
	want := []time.Time{from, from.Add(30 * time.Minute), from.Add(90 * time.Minute), from.Add(150 * time.Minute), from.Add(3 * time.Hour)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("hours %v", got)
	}
	got = storageTimes(from, from.Add(48*time.Hour), 24*time.Hour)
	want = []time.Time{from, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), from.Add(48 * time.Hour)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("days %v", got)
	}
	aligned := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if got := storageTimes(aligned, aligned.Add(24*time.Hour), 24*time.Hour); len(got) != 2 {
		t.Errorf("aligned day %v", got)
	}
	if got := storageTimes(from, from.Add(-time.Hour), time.Hour); got != nil {
		t.Errorf("reversed range %v", got)
	}
}

// TestBackupStorageMigrationSeedsHistory: locations measured before the
// storage history existed start it with their current size.
func TestBackupStorageMigrationSeedsHistory(t *testing.T) {
	const history = "20260928192533"
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < history {
			before.Add(m)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
	storageRepos(t, ctx, db, "r1")
	measuredAt := time.Date(2026, 9, 27, 3, 5, 23, 123456000, time.UTC)
	for _, row := range []backupLocationRow{
		{RepositoryID: "r1", Scope: "env:e1", SizeBytes: 100, UncompressedBytes: 250, StatsAt: &measuredAt, UpdatedAt: measuredAt},
		{RepositoryID: "r1", Scope: "manager", UpdatedAt: measuredAt}, // never measured
	} {
		if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	got := storageSamples(t, ctx, db)
	if len(got) != 1 || got[0].Scope != "env:e1" || !got[0].At.Equal(measuredAt) || got[0].SizeBytes != 100 || got[0].UncompressedBytes != 250 {
		t.Fatalf("seeded samples %+v", got)
	}
	// The seeded sample is in its hour: a measurement in the same hour replaces it.
	measure(t, ctx, db, "r1", "env:e1", measuredAt.Add(10*time.Minute), 120, 260)
	if got := storageSamples(t, ctx, db); len(got) != 1 || got[0].SizeBytes != 120 {
		t.Errorf("after a measurement in the same hour %+v", got)
	}
}
