package backups

import (
	"context"
	"errors"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type stackLookup map[string]domain.Stack

func (f stackLookup) Get(_ context.Context, id string) (domain.Stack, error) {
	st, ok := f[id]
	if !ok {
		return domain.Stack{}, domain.ErrStackNotFound
	}
	return st, nil
}

// setupFixture is a database with two active environments, one stack in
// each and the ready repositories ids, and a service over it.
func setupFixture(t *testing.T, ids ...string) (context.Context, bun.IDB, *Service) {
	t.Helper()
	ctx := testutil.Context(t)
	db := openDB(t)
	clk := testutil.FakeClock()
	now := clk.Now().UTC()
	stacks := stackLookup{}
	for _, env := range []string{"env-1", "env-2"} {
		e := domain.Environment{ID: env, Name: env, EngineID: "E-" + env, InstallID: "i-" + env, Status: domain.EnvironmentActive,
			Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &e); err != nil {
			t.Fatal(err)
		}
		st := domain.Stack{ID: "st-" + env, EnvironmentID: env, Name: "app", Root: protocol.RootStacks, Dir: "app",
			Origin: domain.StackOriginImported, Status: domain.StackDeployed, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertStack(ctx, db, &st); err != nil {
			t.Fatal(err)
		}
		stacks[st.ID] = st
	}
	for _, id := range ids {
		r := domain.BackupRepository{ID: id, Name: id, Endpoint: "https://s3.example.com", Bucket: "b-" + id,
			State: domain.BackupRepositoryReady, VerifyCron: "0 5 * * 0", VerifyTimeZone: "UTC", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertBackupRepository(ctx, db, &r, store.BackupRepositorySealed{}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, db, &Service{db: db, opts: Options{Clock: clk, Stacks: stacks}, log: testutil.Logger(t)}
}

func setRoles(t *testing.T, ctx context.Context, db bun.IDB, primary, secondary string) domain.BackupSetup {
	t.Helper()
	st, err := store.GetBackupSetup(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	rev := st.Revision
	st.PrimaryRepositoryID, st.SecondaryRepositoryID, st.Revision = primary, secondary, rev+1
	if err := store.UpdateBackupSetup(ctx, db, st, rev); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestRunBacksUpToPrimaryThenSecondary (#246): every environment and the
// manager state go to the Primary repository first, then the same to the
// Secondary; set members are keyed by repository, so the set completes
// only with both copies, and a retry re-runs one repository's member.
func TestRunBacksUpToPrimaryThenSecondary(t *testing.T) {
	ctx, db, s := setupFixture(t, "repo-a", "repo-b")
	st := setRoles(t, ctx, db, "repo-a", "repo-b")
	set, reqs, err := s.planRun(ctx, db, st, "set-1", s.now(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		kind domain.JobKind
		env  string
		repo string
	}
	var got []step
	for _, r := range reqs {
		got = append(got, step{r.Kind, r.EnvironmentID, repositoryOf(domain.Job{Targets: r.Targets})})
	}
	want := []step{
		{jobspec.BackupRun, "env-1", "repo-a"}, {jobspec.BackupRun, "env-2", "repo-a"}, {jobspec.ManagerBackup, "", "repo-a"},
		{jobspec.BackupRun, "env-1", "repo-b"}, {jobspec.BackupRun, "env-2", "repo-b"}, {jobspec.ManagerBackup, "", "repo-b"},
	}
	if len(got) != len(want) {
		t.Fatalf("jobs %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("job %d is %+v, want %+v (all %+v)", i, got[i], want[i], got)
		}
	}
	if len(set.Members) != 6 {
		t.Fatalf("members %+v", set.Members)
	}

	// One copy failed: the set is partial and a retry re-runs that copy only.
	for i := range set.Members {
		m := &set.Members[i]
		m.State, m.SnapshotID = backup.StateComplete, "snap"
		if m.RepositoryID == "repo-b" && m.EnvironmentID == "env-2" {
			m.State, m.SnapshotID, m.ErrorClass = backup.StateFailed, "", "storage_unreachable"
		}
	}
	s.settle(&set)
	if set.State != backup.StatePartial {
		t.Errorf("state %s with one copy missing, want partial", set.State)
	}
	only := retryItems(set.Members)
	_, retry, err := s.planRun(ctx, db, st, "set-1", s.now(), only, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry) != 1 || retry[0].EnvironmentID != "env-2" || repositoryOf(domain.Job{Targets: retry[0].Targets}) != "repo-b" {
		t.Errorf("retry %+v, want env-2 to repo-b only", retry)
	}

	// Without the manager state (a caller who may not back it up) and
	// without a Secondary: one copy, no manager.backup.
	st = setRoles(t, ctx, db, "repo-a", "")
	_, reqs, err = s.planRun(ctx, db, st, "set-2", s.now(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.Kind == jobspec.ManagerBackup || repositoryOf(domain.Job{Targets: r.Targets}) != "repo-a" {
			t.Errorf("unexpected job %s to %s", r.Kind, repositoryOf(domain.Job{Targets: r.Targets}))
		}
	}
	if len(reqs) != 2 {
		t.Errorf("jobs %d, want one per environment", len(reqs))
	}
}

// TestRunLeavesOutExcludedEnvironments: an environment the setup leaves
// out is not backed up.
func TestRunLeavesOutExcludedEnvironments(t *testing.T) {
	ctx, db, s := setupFixture(t, "repo-a")
	st := setRoles(t, ctx, db, "repo-a", "")
	st.ExcludeEnvironments = []string{"env-2", "gone"}
	_, reqs, err := s.planRun(ctx, db, st, "set-1", s.now(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].EnvironmentID != "env-1" {
		t.Errorf("jobs %+v, want env-1 only", reqs)
	}
}

// TestSetupNeedsAPrimary: without a Primary repository nothing runs, and
// the setup cannot be turned on; the Secondary must differ from it.
func TestSetupNeedsAPrimary(t *testing.T) {
	ctx, db, s := setupFixture(t, "repo-a", "repo-b")
	st := setRoles(t, ctx, db, "", "")
	if err := s.checkReady(ctx, st); !errors.Is(err, domain.ErrBackupNoPrimary) {
		t.Errorf("run without a Primary: %v", err)
	}
	on := true
	if _, _, err := s.UpdateSetup(ctx, st.Revision, domain.BackupSetupPatch{Enabled: &on}); !isField(err, "primaryRepositoryId") {
		t.Errorf("turned on without a Primary: %v", err)
	}
	a, b := "repo-a", "repo-a"
	if _, _, err := s.UpdateSetup(ctx, st.Revision, domain.BackupSetupPatch{PrimaryRepositoryID: &a, SecondaryRepositoryID: &b}); !isField(err, "secondaryRepositoryId") {
		t.Errorf("the same repository twice: %v", err)
	}
	b = "repo-b"
	var missing []bool
	s.opts.OnPrimaryMissing = func(_ context.Context, m bool) { missing = append(missing, m) }
	_, after, err := s.UpdateSetup(ctx, st.Revision, domain.BackupSetupPatch{Enabled: &on, PrimaryRepositoryID: &a, SecondaryRepositoryID: &b})
	if err != nil {
		t.Fatal(err)
	}
	if after.PrimaryRepositoryID != "repo-a" || after.SecondaryRepositoryID != "repo-b" || !after.Enabled {
		t.Errorf("setup %+v", after)
	}
	if len(missing) != 1 || missing[0] {
		t.Errorf("primary-missing hook %v, want one false", missing)
	}
	// Swapping the two is one patch.
	_, swapped, err := s.UpdateSetup(ctx, after.Revision, domain.BackupSetupPatch{PrimaryRepositoryID: &b, SecondaryRepositoryID: &a})
	if err != nil || swapped.PrimaryRepositoryID != "repo-b" || swapped.SecondaryRepositoryID != "repo-a" {
		t.Errorf("swap %+v %v", swapped, err)
	}
}

func isField(err error, field string) bool {
	var fe *domain.FieldError
	return errors.As(err, &fe) && fe.Field == field
}

// TestImportReadsTheCopyAtTheDestination: a set backed up to a Primary and
// a Secondary repository names a manager-state copy in each; an import
// from either bucket reads the copy that bucket holds.
func TestImportReadsTheCopyAtTheDestination(t *testing.T) {
	sets := []backup.Manifest{{Members: []backup.Member{
		{Kind: backup.MemberStack, RepositoryID: "primary", SnapshotID: "st"},
		{Kind: backup.MemberManagerState, RepositoryID: "primary", SnapshotID: "p1"},
		{Kind: backup.MemberManagerState, RepositoryID: "secondary", SnapshotID: "s1"},
	}}}
	if got := importRepositoryID(sets, []restic.Snapshot{{ID: "s1"}, {ID: "other"}}); got != "secondary" {
		t.Errorf("from the Secondary bucket: %q", got)
	}
	if got := importRepositoryID(sets, []restic.Snapshot{{ID: "p1"}}); got != "primary" {
		t.Errorf("from the Primary bucket: %q", got)
	}
	if got := importRepositoryID(sets, nil); got != "primary" {
		t.Errorf("without a listing: %q, want the first copy", got)
	}
	set := ImportSet{RepositoryID: "secondary", Members: []ImportMember{
		{Member: backup.Member{Kind: backup.MemberManagerState, RepositoryID: "primary"}},
		{Member: backup.Member{Kind: backup.MemberManagerState, RepositoryID: "secondary", SnapshotID: "s1"}},
	}}
	if m, ok := managerMember(set); !ok || m.SnapshotID != "s1" {
		t.Errorf("manager member %+v %v", m, ok)
	}
}
