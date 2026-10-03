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

	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/testutil"
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
	return domain.BackupRepository{ID: id, Name: "repo " + id, Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: id,
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
		// Columns added after the cleanup do not exist yet.
		r := testRepository(id)
		row := fromBackupRepository(&r, BackupRepositorySealed{})
		if _, err := db.NewInsert().Model(&row).ExcludeColumn("compression").Exec(ctx); err != nil {
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

// TestRecentBackupSetsPerPolicy: one query returns each policy's newest
// sets (newest first, ties by ID), at most perPolicy of them; other
// policies' sets are left out and policies without sets are absent.
func TestRecentBackupSetsPerPolicy(t *testing.T) {
	ctx, db := backupTestDB(t)
	add := func(id, policy string, minutes int) {
		t.Helper()
		at := testutil.Epoch.Add(time.Duration(minutes) * time.Minute)
		s := domain.BackupSet{ID: id, PolicyID: policy, Origin: domain.JobOrigin("manual"), State: "complete", StartedAt: at, UpdatedAt: at}
		if _, err := InsertBackupSet(ctx, db, &s); err != nil {
			t.Fatal(err)
		}
	}
	add("a1", "pa", 1)
	add("a2", "pa", 2)
	add("a3", "pa", 3)
	add("a4", "pa", 3) // same start as a3: the higher ID first
	add("b1", "pb", 1)
	add("c1", "pc", 5)

	got, err := RecentBackupSets(ctx, db, []string{"pa", "pb", "none"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(sets []domain.BackupSet) []string {
		out := []string{}
		for _, s := range sets {
			out = append(out, s.ID)
		}
		return out
	}
	if a := ids(got["pa"]); !slices.Equal(a, []string{"a4", "a3", "a2"}) {
		t.Errorf("pa: %v", a)
	}
	if b := ids(got["pb"]); !slices.Equal(b, []string{"b1"}) {
		t.Errorf("pb: %v", b)
	}
	if _, ok := got["pc"]; ok {
		t.Error("a policy that was not asked for is listed")
	}
	if _, ok := got["none"]; ok || len(got) != 2 {
		t.Errorf("policies %v", got)
	}
	if empty, err := RecentBackupSets(ctx, db, nil, 3); err != nil || len(empty) != 0 {
		t.Errorf("no policies: %v %v", empty, err)
	}
}

// TestBackupSnapshotsOfSets: the backups of several sets in one query,
// without forgotten ones and without other sets' backups.
func TestBackupSnapshotsOfSets(t *testing.T) {
	ctx, db := backupTestDB(t)
	forgotten := testutil.Epoch.Add(time.Hour)
	for _, sn := range []domain.BackupSnapshot{
		{ID: "sn1", SetID: "s1"},
		{ID: "sn2", SetID: "s2"},
		{ID: "sn3", SetID: "s2", ForgottenAt: &forgotten},
		{ID: "sn4", SetID: "s3"},
	} {
		sn.RepositoryID, sn.Scope, sn.Kind, sn.Item, sn.State = "r1", "docker-manager", "volume", "volume/"+sn.ID, "complete"
		sn.ResticSnapshotID, sn.SnapshotTime, sn.CreatedAt = "restic-"+sn.ID, testutil.Epoch, testutil.Epoch
		if _, err := InsertBackupSnapshot(ctx, db, &sn); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListBackupSnapshotsOfSets(ctx, db, []string{"s1", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sn := range got {
		ids = append(ids, sn.ID+"@"+sn.SetID+"/"+sn.ResticSnapshotID)
	}
	if !slices.Equal(ids, []string{"sn1@s1/restic-sn1", "sn2@s2/restic-sn2"}) {
		t.Errorf("backups %v", ids)
	}
	if none, err := ListBackupSnapshotsOfSets(ctx, db, nil); err != nil || len(none) != 0 {
		t.Errorf("no sets: %v %v", none, err)
	}
}

// TestNextScheduledRuns: the next runs of several policies of one kind in
// one query; schedules without a next run, of another kind or of other
// policies are left out.
func TestNextScheduledRuns(t *testing.T) {
	ctx, db := backupTestDB(t)
	next := testutil.Epoch.Add(2 * time.Hour)
	for _, sc := range []domain.Schedule{
		{ID: "sc1", Kind: "backup", PolicyID: "pa", Enabled: true, NextRunAt: &next},
		{ID: "sc2", Kind: "backup", PolicyID: "pb"},
		{ID: "sc3", Kind: "prune", PolicyID: "pa", Enabled: true, NextRunAt: &next},
		{ID: "sc4", Kind: "backup", PolicyID: "pc", Enabled: true, NextRunAt: &next},
	} {
		sc.Cron, sc.TimeZone, sc.Cursor, sc.CreatedAt, sc.UpdatedAt = "0 2 * * *", "UTC", testutil.Epoch, testutil.Epoch, testutil.Epoch
		if err := InsertSchedule(ctx, db, &sc); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NextScheduledRuns(ctx, db, "backup", []string{"pa", "pb", "none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["pa"].Equal(next) {
		t.Errorf("next runs %v", got)
	}
}

// TestBackupRepositoryCompressionRoundTrip: an unset mode is stored as
// auto, a chosen one survives an update, and the column refuses unknown
// modes.
func TestBackupRepositoryCompressionRoundTrip(t *testing.T) {
	ctx, db := backupTestDB(t)
	r := testRepository("r1")
	if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
		t.Fatal(err)
	}
	got, err := GetBackupRepository(ctx, db, "r1")
	if err != nil || got.Compression != domain.BackupCompressionAuto {
		t.Fatalf("inserted compression = %q (%v), want auto", got.Compression, err)
	}
	got.Compression, got.Revision = domain.BackupCompressionMax, 2
	if err := UpdateBackupRepository(ctx, db, &got, 1, nil); err != nil {
		t.Fatal(err)
	}
	if got, err = GetBackupRepository(ctx, db, "r1"); err != nil || got.Compression != domain.BackupCompressionMax || got.Revision != 2 {
		t.Fatalf("updated = %q rev %d (%v), want max rev 2", got.Compression, got.Revision, err)
	}
	bad := testRepository("r2")
	bad.Compression = "fastest"
	if err := InsertBackupRepository(ctx, db, &bad, BackupRepositorySealed{}); err == nil {
		t.Error("stored an unknown compression mode")
	}
}

// TestMigrationFoldsRetentionFloorIntoLast: the removed minimum recovery
// floor did what "last" does. A policy with rules and a floor above its
// "last" keeps the same backups with last raised to the floor (and a new
// revision); a policy without rules keeps everything and stays so.
// backupPolicyColumnsAfterFold are the backup_policies columns migrations
// after 20260928174844 added; the fold test inserts rows without them.
var backupPolicyColumnsAfterFold = []string{"external_binds"}

func TestMigrationFoldsRetentionFloorIntoLast(t *testing.T) {
	const fold = "20260928174844"
	ctx := testutil.Context(t)
	db, dir := openTemp(t)
	before := migrate.NewMigrations()
	for _, m := range migrations.Migrations.Sorted() {
		if m.Name < fold {
			before.Add(m)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, before)); err != nil {
		t.Fatal(err)
	}
	r := testRepository("r1")
	if err := InsertBackupRepository(ctx, db, &r, BackupRepositorySealed{}); err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{
		"raised":   `{"Last":1,"Daily":7,"MinKeep":3}`,
		"kept":     `{"Last":10,"MinKeep":3}`,
		"no-rules": `{"MinKeep":3}`,
	}
	for id, retention := range stored {
		p := domain.BackupPolicy{ID: id, Name: id, EnvironmentID: "env-" + id, RepositoryID: "r1", Cron: "0 3 * * *", TimeZone: "UTC",
			Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
		// The table as it was before the fold: without the columns later
		// migrations added.
		row := fromBackupPolicy(&p)
		if _, err := db.NewInsert().Model(&row).ExcludeColumn(backupPolicyColumnsAfterFold...).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE backup_policies SET retention = ? WHERE id = ?", retention, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(ctx, db, migrateOpts(t, dir, migrations.Migrations)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]struct {
		last     int
		revision int64
	}{"raised": {3, 2}, "kept": {10, 1}, "no-rules": {0, 1}} {
		p, err := GetBackupPolicy(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Retention.Last != want.last || p.Revision != want.revision {
			t.Errorf("%s: last %d revision %d, want %d and %d", id, p.Retention.Last, p.Revision, want.last, want.revision)
		}
		var left int
		if err := db.NewRaw("SELECT COUNT(*) FROM backup_policies WHERE id = ? AND json_type(retention, '$.MinKeep') IS NOT NULL", id).Scan(ctx, &left); err != nil || left != 0 {
			t.Errorf("%s still stores the floor (%d, %v)", id, left, err)
		}
	}
}
