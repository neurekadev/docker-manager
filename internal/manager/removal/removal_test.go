package removal

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

type seed struct {
	t   *testing.T
	ctx context.Context
	db  *bun.DB
	now time.Time
}

func newSeed(t *testing.T) *seed {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "docker-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "s"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return &seed{t: t, ctx: ctx, db: db, now: testutil.Epoch}
}

func (s *seed) must(err error) {
	s.t.Helper()
	if err != nil {
		s.t.Fatal(err)
	}
}

func (s *seed) env(id, name string) {
	s.must(store.InsertEnvironment(s.ctx, s.db, &domain.Environment{ID: id, Name: name, EngineID: "ENG-" + id, InstallID: "inst-" + id,
		Status: domain.EnvironmentActive, Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
}

func (s *seed) stack(id, env, name string) {
	s.must(store.InsertStack(s.ctx, s.db, &domain.Stack{ID: id, EnvironmentID: env, Name: name, Root: protocol.RootStacks, Dir: name,
		Origin: domain.StackOriginCreated, Status: domain.StackUndeployed, Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
}

// seedEverything gives environment "nas" one dependent record of every
// kind, plus records of environment "cloud" that must not be listed.
func (s *seed) seedEverything() {
	s.env("nas", "NAS")
	s.env("cloud", "Cloud")
	s.stack("st-shop", "nas", "shop")
	s.stack("st-other", "cloud", "other")
	s.must(store.InsertManagedContainer(s.ctx, s.db, domain.ManagedContainer{ID: ids.New(), EnvironmentID: "nas", Name: "cache",
		CreateJobID: "job-create", CreatedAt: s.now}, "sealed"))
	sched := domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}
	for _, u := range []domain.EnvironmentUpdatePolicy{
		{ID: "up-shop", EnvironmentID: "nas", Name: "NAS updates"},
		{ID: "up-other", EnvironmentID: "cloud", Name: "Cloud updates"},
	} {
		u.Check, u.Run, u.Revision, u.CreatedAt, u.UpdatedAt = sched, sched, 1, s.now, s.now
		s.must(store.InsertEnvironmentUpdatePolicy(s.ctx, s.db, u))
	}
	s.must(store.InsertMaintenancePolicy(s.ctx, s.db, &domain.MaintenancePolicy{ID: "mp-nas", EnvironmentID: "nas", Name: "Weekly prune",
		Cron: "0 3 * * 0", TimeZone: "UTC", Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
	// A local repository on the NAS agent, an S3 repository holding the
	// NAS scope, and one only the manager uses.
	for _, r := range []domain.BackupRepository{
		{ID: "repo-local", Name: "NAS disk", Kind: "local", Executor: "nas", Path: "/backups"},
		{ID: "repo-s3", Name: "Offsite", Kind: "s3", Endpoint: "https://s3.example", Bucket: "b"},
		{ID: "repo-mgr", Name: "Manager only", Kind: "local", Executor: domain.BackupExecutorManager, Path: "/mgr"},
	} {
		r.State, r.VerifyCron, r.VerifyTimeZone, r.Revision, r.CreatedAt, r.UpdatedAt = domain.BackupRepositoryReady, "0 5 * * 0", "UTC", 1, s.now, s.now
		s.must(store.InsertBackupRepository(s.ctx, s.db, &r, store.BackupRepositorySealed{}))
	}
	s.must(store.UpsertBackupLocation(s.ctx, s.db, "repo-s3", backup.EnvironmentScope("nas"), store.LocationUpdate{Initialized: true}, s.now))
	s.must(store.UpsertBackupLocation(s.ctx, s.db, "repo-s3", backup.EnvironmentScope("cloud"), store.LocationUpdate{Initialized: true}, s.now))
	s.must(store.InsertBackupPolicy(s.ctx, s.db, &domain.BackupPolicy{ID: "bp-nightly", EnvironmentID: "nas", Name: "Nightly", RepositoryID: "repo-s3",
		Cron: "0 2 * * *", TimeZone: "UTC", Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
	s.must(store.InsertBackupPolicy(s.ctx, s.db, &domain.BackupPolicy{ID: "bp-cloud", EnvironmentID: "cloud", Name: "Cloud only", RepositoryID: "repo-s3",
		Cron: "0 2 * * *", TimeZone: "UTC", Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
	if _, err := store.InsertBackupSet(s.ctx, s.db, &domain.BackupSet{ID: "set-1", PolicyID: "bp-nightly", PolicyName: "Nightly",
		Origin: domain.OriginScheduled, State: backup.StateComplete, StartedAt: s.now, UpdatedAt: s.now}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := store.InsertBackupSnapshot(s.ctx, s.db, &domain.BackupSnapshot{ID: ids.New(), SetID: "set-1", PolicyID: "bp-nightly",
		RepositoryID: "repo-s3", Scope: backup.EnvironmentScope("nas"), EnvironmentID: "nas", Kind: backup.MemberStack, Item: "stack:st-shop",
		StackID: "st-shop", StackName: "shop", ResticSnapshotID: "abc123", SnapshotTime: s.now, State: "complete", CreatedAt: s.now}); err != nil {
		s.t.Fatal(err)
	}
	for _, r := range []domain.RegistryConnection{
		{ID: "reg-env", Name: "NAS registry", EnvironmentID: "nas"},
		{ID: "reg-stack", Name: "Shop registry", StackID: "st-shop"},
		{ID: "reg-cloud", Name: "Cloud registry", EnvironmentID: "cloud"},
	} {
		r.SecretVersion = 1
		r.Host, r.CredentialType, r.Username, r.Status, r.SecretUpdatedAt, r.Revision, r.CreatedAt, r.UpdatedAt =
			"registry.example", domain.RegistryCredentialToken, "robot", domain.RegistryConnectionActive, s.now, 1, s.now, s.now
		s.must(store.InsertRegistryConnection(s.ctx, s.db, &r, "sealed"))
	}
	s.must(store.InsertBuildDefinition(s.ctx, s.db, &domain.BuildDefinition{ID: "bd-1", EnvironmentID: "nas", Name: "web image",
		Source:   domain.BuildSource{GitURL: "https://git.example/app.git", Ref: "main", Tags: []string{"app:dev"}, GitCredentialID: "git-1"},
		Revision: 1, CreatedAt: s.now, UpdatedAt: s.now}))
	group, err := store.DefaultGroupID(s.ctx, s.db)
	s.must(err)
	doc, err := store.GroupPermissions(s.ctx, s.db, group)
	s.must(err)
	_, err = store.ReplaceGroupPermissions(s.ctx, s.db, group, doc.Revision, []domain.PermissionRule{
		{Capability: "environment.read", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindEnvironment, EnvironmentID: "nas"}},
		{Capability: "container.restart", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindResource,
			EnvironmentID: "nas", ResourceType: "container", ResourceID: "web"}},
		{Capability: "stack.read", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindResource,
			ResourceType: "stack", ResourceID: "st-shop"}},
		{Capability: "environment.read", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindEnvironment, EnvironmentID: "cloud"}},
		{Capability: "environment.read", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindInstance}},
	}, s.now)
	s.must(err)
	for _, sc := range []domain.Schedule{
		{ID: "sc-up", Kind: "update_check", PolicyID: "up-shop", Name: "Shop updates", EnvironmentID: "nas"},
		{ID: "sc-bp", Kind: "backup", PolicyID: "bp-nightly", Name: "Nightly"},
		{ID: "sc-verify", Kind: "backup_verification", PolicyID: "repo-local", Name: "NAS disk"},
		{ID: "sc-cloud", Kind: "backup", PolicyID: "bp-cloud", Name: "Cloud only"},
	} {
		sc.Cron, sc.TimeZone, sc.Cursor, sc.CreatedAt, sc.UpdatedAt = "0 3 * * *", "UTC", s.now, s.now, s.now
		s.must(store.InsertSchedule(s.ctx, s.db, &sc))
	}
	for _, j := range []domain.Job{
		{ID: "job-queued", Kind: "container.restart", EnvironmentID: "nas", State: domain.JobQueued},
		{ID: "job-done", Kind: "container.restart", EnvironmentID: "nas", State: domain.JobSucceeded},
		{ID: "job-cloud", Kind: "container.restart", EnvironmentID: "cloud", State: domain.JobQueued},
	} {
		j.Executor, j.Origin, j.Attempt, j.CreatedAt, j.UpdatedAt = domain.ExecutorAgent, domain.OriginManual, 1, s.now, s.now
		j.Targets = []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
		s.must(store.InsertJob(s.ctx, s.db, &j))
	}
}

// TestPreviewListsEveryDependentKind (#34 Done-when 3): the preview of an
// environment lists a record of every dependent kind with what archiving
// does to it, and nothing of another environment.
func TestPreviewListsEveryDependentKind(t *testing.T) {
	s := newSeed(t)
	s.seedEverything()
	p, err := New(s.db).Preview(s.ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	effects := map[string]string{}
	for _, d := range p.Dependents {
		got[d.Kind] = append(got[d.Kind], d.ID)
		if prev, ok := effects[d.Kind]; ok && prev != d.OnArchive {
			t.Errorf("%s has mixed effects %s/%s", d.Kind, prev, d.OnArchive)
		}
		effects[d.Kind] = d.OnArchive
	}
	want := map[string][]string{
		domain.DependentStack:              {"st-shop"},
		domain.DependentUpdatePolicy:       {"up-shop"},
		domain.DependentBackupPolicy:       {"bp-nightly"},
		domain.DependentMaintenancePolicy:  {"mp-nas"},
		domain.DependentBackupRepository:   {"repo-local", "repo-s3"},
		domain.DependentBackupSet:          {"set-1"},
		domain.DependentRegistryConnection: {"reg-env", "reg-stack"},
		domain.DependentBuildDefinition:    {"bd-1"},
		domain.DependentSchedule:           {"sc-bp", "sc-up", "sc-verify"},
		domain.DependentJob:                {"job-queued"},
	}
	for _, k := range domain.DependentKinds() {
		if len(got[k]) == 0 {
			t.Errorf("no %s listed", k)
		}
	}
	for k, ids := range want {
		g := slices.Clone(got[k])
		slices.Sort(g)
		if !slices.Equal(g, ids) {
			t.Errorf("%s: %v, want %v", k, g, ids)
		}
	}
	if n := len(got[domain.DependentManagedContainer]); n != 1 {
		t.Errorf("managed containers %v", got[domain.DependentManagedContainer])
	}
	// Two rules name the NAS environment (its environment scope and a
	// container in it); the stack rule stays with the stack, the Cloud and
	// instance rules are unrelated.
	if n := len(got[domain.DependentPermissionRule]); n != 2 {
		t.Errorf("permission rules %d, want 2", n)
	}
	wantEffects := map[string]string{domain.DependentStack: domain.OnArchiveKept, domain.DependentUpdatePolicy: domain.OnArchivePaused,
		domain.DependentBackupPolicy: domain.OnArchivePaused, domain.DependentMaintenancePolicy: domain.OnArchivePaused,
		domain.DependentBackupRepository: domain.OnArchiveKept, domain.DependentBackupSet: domain.OnArchiveKept,
		domain.DependentPermissionRule: domain.OnArchiveRemoved, domain.DependentJob: domain.OnArchiveInterrupted}
	for k, e := range wantEffects {
		if effects[k] != e {
			t.Errorf("%s on archive %q, want %q", k, effects[k], e)
		}
	}
	if p.BackupSnapshots != 1 {
		t.Errorf("snapshots %d", p.BackupSnapshots)
	}
	if _, err := New(s.db).Preview(s.ctx, "missing"); err == nil {
		t.Error("preview of an unknown environment")
	}
}

// TestEnvironmentRulesRemoval: DeleteEnvironmentRules removes exactly the
// rules EnvironmentRules lists and bumps the documents' revisions.
func TestEnvironmentRulesRemoval(t *testing.T) {
	s := newSeed(t)
	s.seedEverything()
	group, _ := store.DefaultGroupID(s.ctx, s.db)
	before, _ := store.GroupPermissions(s.ctx, s.db, group)
	listed, err := store.EnvironmentRules(s.ctx, s.db, "nas")
	s.must(err)
	n, err := store.DeleteEnvironmentRules(s.ctx, s.db, "nas", s.now)
	s.must(err)
	after, _ := store.GroupPermissions(s.ctx, s.db, group)
	if n != len(listed) || n != 2 || len(after.Rules) != len(before.Rules)-2 || after.Revision != before.Revision+1 {
		t.Fatalf("removed %d of %d; rules %d -> %d; revision %d -> %d", n, len(listed), len(before.Rules), len(after.Rules),
			before.Revision, after.Revision)
	}
	for _, r := range after.Rules {
		if r.Scope.EnvironmentID == "nas" {
			t.Errorf("rule kept: %+v", r)
		}
	}
}
