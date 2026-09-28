package backups

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestSkippedMembersCountNeitherWay: members removed before their turn do
// not make a set partial; a set of skipped members only is skipped and
// gets no retention follow-up; a retry never re-runs them.
func TestSkippedMembersCountNeitherWay(t *testing.T) {
	ctx := testutil.Context(t)
	s := &Service{opts: Options{Clock: testutil.FakeClock()}}
	env := backup.EnvironmentScope("env-1")
	snap := "snap-a"
	cases := []struct {
		name    string
		members []domain.BackupSetMember
		want    string
	}{
		{"others complete", []domain.BackupSetMember{
			{Item: "stack/a", Scope: env, State: backup.StateComplete, SnapshotID: snap},
			{Item: "volume/tmp", Scope: env, State: backup.StateSkipped, ErrorClass: backup.ClassItemGone},
		}, backup.StateComplete},
		{"another failed", []domain.BackupSetMember{
			{Item: "stack/a", Scope: env, State: backup.StateComplete, SnapshotID: snap},
			{Item: "volume/tmp", Scope: env, State: backup.StateSkipped, ErrorClass: backup.ClassItemGone},
			{Item: "volume/db", Scope: env, State: backup.StateFailed, ErrorClass: "restic_failed"},
		}, backup.StatePartial},
		{"only skipped", []domain.BackupSetMember{
			{Item: "volume/tmp", Scope: env, State: backup.StateSkipped, ErrorClass: backup.ClassItemGone},
			{Item: "volume/tmp2", Scope: env, State: backup.StateSkipped, ErrorClass: backup.ClassItemGone},
		}, backup.StateSkipped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := domain.BackupSet{PolicyID: "pol-1", State: backup.StatePending, Members: c.members}
			s.settle(&set)
			if set.State != c.want || set.FinishedAt == nil {
				t.Errorf("state %s finished %v, want %s", set.State, set.FinishedAt, c.want)
			}
		})
	}

	// A skipped set is never flagged for retention (it wrote nothing; no
	// database access happens).
	set := domain.BackupSet{PolicyID: "pol-1", State: backup.StatePending, Members: cases[2].members}
	s.settle(&set)
	s.flagRetention(ctx, nil, &set, true)
	if set.FollowUp != "" {
		t.Errorf("a skipped set was flagged for retention: %q", set.FollowUp)
	}

	if got := retryItems(cases[1].members); len(got) != 1 || !got[env+"\x00volume/db"] {
		t.Errorf("retry items = %v, want only the failed volume", got)
	}
	if got := retryItems(cases[2].members); len(got) != 0 {
		t.Errorf("retry items of a skipped set = %v, want none", got)
	}
}

// TestBackupRunHookRecordsSkippedMembers: a new agent's skipped member is
// recorded as skipped (not failed) and the set completes with the others.
func TestBackupRunHookRecordsSkippedMembers(t *testing.T) {
	ctx := testutil.Context(t)
	db := openDB(t)
	clk := testutil.FakeClock()
	s := &Service{db: db, opts: Options{Clock: clk}, log: testutil.Logger(t)}
	env := backup.EnvironmentScope("env-1")
	started := clk.Now().UTC()
	set := domain.BackupSet{ID: "set-1", Origin: domain.OriginManual, State: backup.StatePending, StartedAt: started, UpdatedAt: started,
		Members: []domain.BackupSetMember{
			{Item: backup.StackItem("st-a"), Kind: backup.MemberStack, Scope: env, RepositoryID: "repo-1", EnvironmentID: "env-1",
				StackID: "st-a", State: backup.StatePending},
			{Item: backup.VolumeItem("ci-tmp"), Kind: backup.MemberVolume, Scope: env, RepositoryID: "repo-1", EnvironmentID: "env-1",
				Volume: "ci-tmp", State: backup.StatePending},
		}}
	if _, err := store.InsertBackupSet(ctx, db, &set); err != nil {
		t.Fatal(err)
	}
	in := protocol.BackupRunInput{SetID: "set-1", Repository: protocol.BackupRepositoryRef{RepositoryID: "repo-1", Scope: env},
		Items: []protocol.BackupItem{
			{Kind: backup.MemberStack, StackID: "st-a", StackName: "a", Project: &protocol.ProjectRef{Root: protocol.RootStacks, Dir: "a", ProjectName: "a"}},
			{Kind: backup.MemberVolume, Volume: "ci-tmp"},
		}}
	out := protocol.BackupRunOutput{Members: []backup.Member{
		{Item: backup.StackItem("st-a"), Kind: backup.MemberStack, Scope: env, SnapshotID: "snap-a", SnapshotTime: started.Add(time.Minute),
			State: backup.StateComplete},
		{Item: backup.VolumeItem("ci-tmp"), Kind: backup.MemberVolume, Scope: env, State: backup.StateSkipped, ErrorClass: backup.ClassItemGone},
	}}
	rawIn, _ := json.Marshal(in)
	rawOut, _ := json.Marshal(out)
	j := domain.Job{ID: "job-1", EnvironmentID: "env-1", State: domain.JobSucceeded, Input: rawIn, ResultOutput: rawOut}
	if err := s.onBackupRun(ctx, db, j); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBackupSet(ctx, db, "set-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != backup.StateComplete || got.FinishedAt == nil {
		t.Errorf("set state %s finished %v, want complete", got.State, got.FinishedAt)
	}
	states := map[string]domain.BackupSetMember{}
	for _, m := range got.Members {
		states[m.Item] = m
	}
	if m := states[backup.VolumeItem("ci-tmp")]; m.State != backup.StateSkipped || m.ErrorClass != backup.ClassItemGone || m.SnapshotID != "" {
		t.Errorf("skipped member = %+v", m)
	}
	if m := states[backup.StackItem("st-a")]; m.State != backup.StateComplete || m.SnapshotID != "snap-a" {
		t.Errorf("stack member = %+v", m)
	}
}

// TestStandaloneVolumesLeaveOutTemporaryObjects: a volume only temporary
// containers use and a volume of an unfinished (or unknown) environment
// migration are not selected; a volume a real container also uses and one
// of a succeeded migration are. Prune's references keep them all.
func TestStandaloneVolumesLeaveOutTemporaryObjects(t *testing.T) {
	ctx := testutil.Context(t)
	db := openDB(t)
	clk := testutil.FakeClock()
	now := clk.Now().UTC()
	for id, state := range map[string]domain.MigrationState{
		"mig-ok": domain.MigrationCompleted, "mig-removed": domain.MigrationSourceRemoved, "mig-failed": domain.MigrationFailed,
		"mig-running": domain.MigrationRunning, "mig-cancelled": domain.MigrationCancelled,
	} {
		m := domain.Migration{ID: id, Kind: domain.MigrationKindVolume, SourceEnvironmentID: "e0", TargetEnvironmentID: "e1",
			State: state, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertMigration(ctx, db, &m); err != nil {
			t.Fatal(err)
		}
	}
	migrated := func(id string) map[string]string { return map[string]string{protocol.LabelMigration: id} }
	s := &Service{db: db, opts: Options{Clock: clk}, volumes: fakeVolumes{
		volumes: []protocol.VolumeInfo{
			{Name: "media", UsedBy: []protocol.ContainerRef{{ID: "real"}, {ID: "aside"}}},
			{Name: "aside-only", UsedBy: []protocol.ContainerRef{{ID: "aside"}}},
			{Name: "replace-only", UsedBy: []protocol.ContainerRef{{ID: "replace"}}},
			{Name: "helper-only", UsedBy: []protocol.ContainerRef{{ID: "helper"}, {ID: "rename"}}},
			// Compose keeps the replace label after renaming its replacement:
			// the final container is a real user.
			{Name: "recreated", UsedBy: []protocol.ContainerRef{{ID: "recreated"}}},
			{Name: "unused"},
			{Name: "copy-ok", Labels: migrated("mig-ok")},
			{Name: "copy-removed", Labels: migrated("mig-removed")},
			{Name: "copy-failed", Labels: migrated("mig-failed")},
			{Name: "copy-running", Labels: migrated("mig-running")},
			{Name: "copy-cancelled", Labels: migrated("mig-cancelled")},
			{Name: "copy-unknown", Labels: migrated("mig-unknown")},
		},
		containers: []protocol.ContainerSummary{
			{ID: "real", Name: "web"},
			{ID: "aside", Name: "web-docker-manager-update-0123456789ab"},
			{ID: "replace", Name: "0123456789ab_app-db-1", Labels: map[string]string{protocol.ComposeReplaceLabel: "app-db-1"}},
			{ID: "helper", Name: "docker-agent-self-update", Labels: map[string]string{protocol.LabelRole: protocol.RoleSelfUpdate}},
			{ID: "rename", Name: "/old-db-1-docker-manager-rename-abcdef012345"},
			{ID: "recreated", Name: "app-cache-1", Labels: map[string]string{protocol.ComposeReplaceLabel: "0123456789abcdef"}},
		},
	}}
	p := domain.BackupPolicy{EnvironmentID: "e1"}
	got, err := s.standaloneVolumes(ctx, p, "e1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"media", "recreated", "unused", "copy-ok", "copy-removed"}; !slices.Equal(got, want) {
		t.Errorf("volumes %v, want %v", got, want)
	}
	// Prune's backup references are unchanged: every volume stays protected.
	all, err := s.selectVolumes(ctx, p, "e1", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 12 {
		t.Errorf("prune references %v, want all 12 volumes", all)
	}
}
