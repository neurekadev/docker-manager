package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func backupTestDB(t *testing.T) (context.Context, *bun.DB) {
	t.Helper()
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func testRepository(id string) domain.BackupRepository {
	return domain.BackupRepository{ID: id, Name: "repo " + id, Kind: "local", Executor: "manager", Path: "/backups/" + id,
		State: domain.BackupRepositoryReady, VerifyCron: "0 4 * * 0", VerifyTimeZone: "UTC", Revision: 1,
		CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
}

// TestDeleteBackupRepositoryRemovesItsIndex: removing a repository drops
// its snapshots (forgotten ones too) and the sets held only by it; other
// repositories' snapshots, sets spanning several repositories and sets
// without members stay.
func TestDeleteBackupRepositoryRemovesItsIndex(t *testing.T) {
	ctx, db := backupTestDB(t)
	for _, id := range []string{"r1", "r2"} {
		r := testRepository(id)
		if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := UpsertBackupLocation(ctx, db, "r1", "docker-manager", LocationUpdate{ResticRepositoryID: "x", Initialized: true},
		testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	forgotten := testutil.Epoch.Add(time.Hour)
	for _, sn := range []domain.BackupSnapshot{
		{ID: "sn1", SetID: "only-r1", RepositoryID: "r1"},
		{ID: "sn2", SetID: "only-r1", RepositoryID: "r1", ForgottenAt: &forgotten},
		{ID: "sn3", SetID: "mixed", RepositoryID: "r1"},
		{ID: "sn4", SetID: "mixed", RepositoryID: "r2"},
	} {
		sn.Scope, sn.Kind, sn.Item, sn.State = "docker-manager", "volume", "volume/"+sn.ID, "complete"
		sn.ResticSnapshotID, sn.SnapshotTime, sn.CreatedAt = "restic-"+sn.ID, testutil.Epoch, testutil.Epoch
		if _, err := InsertBackupSnapshot(ctx, db, &sn); err != nil {
			t.Fatal(err)
		}
	}
	for _, set := range []domain.BackupSet{
		{ID: "only-r1", Members: []domain.BackupSetMember{{Item: "volume/sn1", RepositoryID: "r1"}, {Item: "volume/sn2", RepositoryID: "r1"}}},
		{ID: "mixed", Members: []domain.BackupSetMember{{Item: "volume/sn3", RepositoryID: "r1"}, {Item: "volume/sn4", RepositoryID: "r2"}}},
		{ID: "only-r2", Members: []domain.BackupSetMember{{Item: "volume/x", RepositoryID: "r2"}}},
		{ID: "empty"},
	} {
		set.Origin, set.State, set.StartedAt, set.UpdatedAt = domain.JobOrigin("user"), "complete", testutil.Epoch, testutil.Epoch
		if _, err := InsertBackupSet(ctx, db, &set); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := DeleteBackupRepository(ctx, db, "r1", 7); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	if got, _ := ListBackupSnapshots(ctx, db, domain.BackupSnapshotFilter{IncludeForgotten: true}); len(got) != 4 {
		t.Fatalf("a refused delete removed snapshots: %d left", len(got))
	}

	removed, err := DeleteBackupRepository(ctx, db, "r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"sn1", "sn2", "sn3"}) {
		t.Errorf("removed %v", removed)
	}
	snaps, err := ListBackupSnapshots(ctx, db, domain.BackupSnapshotFilter{IncludeForgotten: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != "sn4" {
		t.Errorf("snapshots left %+v", snaps)
	}
	sets, err := ListBackupSets(ctx, db, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var setIDs []string
	for _, s := range sets {
		setIDs = append(setIDs, s.ID)
	}
	slices.Sort(setIDs)
	if !slices.Equal(setIDs, []string{"empty", "mixed", "only-r2"}) {
		t.Errorf("sets left %v", setIDs)
	}
	if locs, err := ListBackupLocations(ctx, db, "r1"); err != nil || len(locs) != 0 {
		t.Errorf("locations left %+v %v", locs, err)
	}
	if _, err := GetBackupRepository(ctx, db, "r1"); !errors.Is(err, domain.ErrBackupRepositoryNotFound) {
		t.Errorf("repository still there: %v", err)
	}
}

// TestDeleteBackupRepositoryInUse: a repository a policy uses is refused
// and keeps its snapshots.
func TestDeleteBackupRepositoryInUse(t *testing.T) {
	ctx, db := backupTestDB(t)
	r := testRepository("r1")
	if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
		t.Fatal(err)
	}
	p := domain.BackupPolicy{ID: "p1", Name: "Nightly", RepositoryID: "r1", Cron: "0 3 * * *", TimeZone: "UTC", Revision: 1,
		CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	if err := InsertBackupPolicy(ctx, db, &p); err != nil {
		t.Fatal(err)
	}
	sn := domain.BackupSnapshot{ID: "sn1", RepositoryID: "r1", Scope: "docker-manager", Kind: "manager_state", Item: "manager-state",
		State: "complete", ResticSnapshotID: "a", SnapshotTime: testutil.Epoch, CreatedAt: testutil.Epoch}
	if _, err := InsertBackupSnapshot(ctx, db, &sn); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteBackupRepository(ctx, db, "r1", 1); !errors.Is(err, domain.ErrBackupRepositoryInUse) {
		t.Fatalf("in use: %v", err)
	}
	if got, _ := ListBackupSnapshots(ctx, db, domain.BackupSnapshotFilter{}); len(got) != 1 {
		t.Errorf("snapshots left %d, want 1", len(got))
	}
}

// TestMigrationDropsOrphanedBackupIndex: the index left behind by
// repositories removed before their index went with them is dropped on
// upgrade; the index of remaining repositories stays.
func TestMigrationDropsOrphanedBackupIndex(t *testing.T) {
	const cleanup = "20260927015828"
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < cleanup {
			before.Add(m)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gone", "kept"} {
		r := testRepository(id)
		if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, repo := range []string{"gone", "gone", "kept"} {
		sn := domain.BackupSnapshot{ID: fmt.Sprintf("sn%d", i), RepositoryID: repo, Scope: "docker-manager", Kind: "volume",
			Item: fmt.Sprintf("volume/v%d", i), State: "complete", ResticSnapshotID: fmt.Sprintf("r%d", i), SnapshotTime: testutil.Epoch,
			CreatedAt: testutil.Epoch}
		if _, err := InsertBackupSnapshot(ctx, db, &sn); err != nil {
			t.Fatal(err)
		}
	}
	for _, set := range []domain.BackupSet{
		{ID: "only-gone", Members: []domain.BackupSetMember{{RepositoryID: "gone"}, {RepositoryID: "gone"}}},
		{ID: "mixed", Members: []domain.BackupSetMember{{RepositoryID: "gone"}, {RepositoryID: "kept"}}},
		{ID: "no-repository", Members: []domain.BackupSetMember{{RepositoryID: "gone"}, {}}},
		{ID: "empty"},
	} {
		set.Origin, set.State, set.StartedAt, set.UpdatedAt = domain.JobOrigin("user"), "failed", testutil.Epoch, testutil.Epoch
		if _, err := InsertBackupSet(ctx, db, &set); err != nil {
			t.Fatal(err)
		}
	}
	// Removed the old way: the repository row only.
	if _, err := db.NewDelete().Model((*backupRepositoryRow)(nil)).Where("id = ?", "gone").Exec(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	snaps, err := ListBackupSnapshots(ctx, db, domain.BackupSnapshotFilter{IncludeForgotten: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].RepositoryID != "kept" {
		t.Errorf("snapshots left %+v", snaps)
	}
	sets, err := ListBackupSets(ctx, db, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var setIDs []string
	for _, s := range sets {
		setIDs = append(setIDs, s.ID)
	}
	slices.Sort(setIDs)
	if !slices.Equal(setIDs, []string{"empty", "mixed", "no-repository"}) {
		t.Errorf("sets left %v", setIDs)
	}
}
