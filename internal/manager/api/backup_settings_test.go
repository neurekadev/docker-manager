package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
)

// fakeSetupBackups serves the backup setup (enabled, two recent sets) and
// records the runs it is asked for. The other BackupService methods are
// not used by these tests.
type fakeSetupBackups struct {
	BackupService

	mu    sync.Mutex
	setup domain.BackupSetup
	runs  []backups.RunOptions
}

var (
	fakeSetStart = time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	fakeNextRun  = time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
)

func newFakeSetupBackups() *fakeSetupBackups {
	return &fakeSetupBackups{setup: domain.BackupSetup{ID: "bs-1", Enabled: true, Cron: "0 2 * * *", TimeZone: "UTC",
		PrimaryRepositoryID: "r1", SecondaryRepositoryID: "r2", Revision: 3}}
}

func (f *fakeSetupBackups) Setup(context.Context) (domain.BackupSetup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setup, nil
}

func (f *fakeSetupBackups) UpdateSetup(_ context.Context, revision int64, p domain.BackupSetupPatch) (before, after domain.BackupSetup, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision != f.setup.Revision {
		return f.setup, f.setup, domain.ErrRevisionMismatch
	}
	before = f.setup
	if p.PrimaryRepositoryID != nil {
		f.setup.PrimaryRepositoryID = *p.PrimaryRepositoryID
	}
	if p.SecondaryRepositoryID != nil {
		f.setup.SecondaryRepositoryID = *p.SecondaryRepositoryID
	}
	if p.ExcludeEnvironments != nil {
		f.setup.ExcludeEnvironments = *p.ExcludeEnvironments
	}
	f.setup.Revision++
	return before, f.setup, nil
}

func fakeSet(id string, hours int) domain.BackupSet {
	at := fakeSetStart.Add(-time.Duration(hours) * time.Hour)
	return domain.BackupSet{ID: id, PolicyID: "bs-1", Origin: domain.OriginScheduled, State: "complete", StartedAt: at, FinishedAt: &at,
		Members: []domain.BackupSetMember{
			{Item: "stack/st-1", Kind: "stack", Scope: "docker-manager-env-e1", RepositoryID: "r1", EnvironmentID: "e1", StackID: "st-1",
				StackName: "shop", State: "complete", SnapshotID: "restic-" + id, SnapshotTime: &at, JobID: "job-" + id},
			{Item: "stack/st-1", Kind: "stack", Scope: "docker-manager-env-e1", RepositoryID: "r2", EnvironmentID: "e1", StackID: "st-1",
				StackName: "shop", State: "failed", ErrorClass: "storage_unreachable"},
		}}
}

func (f *fakeSetupBackups) RecentSets(_ context.Context, policyIDs []string, perPolicy int) (map[string][]domain.BackupSet, error) {
	out := map[string][]domain.BackupSet{}
	for _, id := range policyIDs {
		if id == "bs-1" {
			out[id] = []domain.BackupSet{fakeSet("s1b", 0), fakeSet("s1a", 24)}[:min(perPolicy, 2)]
		}
	}
	return out, nil
}

func (f *fakeSetupBackups) SetBackups(_ context.Context, setIDs []string) ([]domain.BackupSnapshot, error) {
	var out []domain.BackupSnapshot
	for _, id := range setIDs {
		out = append(out, domain.BackupSnapshot{ID: "bk-" + id, SetID: id, RepositoryID: "r1", Scope: "docker-manager-env-e1",
			EnvironmentID: "e1", Kind: "stack", Item: "stack/st-1", ResticSnapshotID: "restic-" + id})
	}
	return out, nil
}

func (f *fakeSetupBackups) NextRuns(_ context.Context, _ string, policyIDs []string) map[string]time.Time {
	return map[string]time.Time{policyIDs[0]: fakeNextRun}
}

func (f *fakeSetupBackups) RunNow(_ context.Context, o backups.RunOptions) (backups.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, o)
	return backups.RunResult{Set: domain.BackupSet{ID: "set-new", PolicyID: "bs-1", State: "pending"}}, nil
}

func backupHandler(t *testing.T, pol *authztest.Policy, svc BackupService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Backups: svc})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

func getJSON(t *testing.T, h http.Handler, user, path string, out any) {
	t.Helper()
	r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: path})
	if r.Status != http.StatusOK {
		t.Fatalf("GET %s as %s: %d %s", path, user, r.Status, r.Body)
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		t.Fatal(err)
	}
}

// TestBackupSettingsCarryRolesAndRuns (#246): the settings name the
// Primary and Secondary repositories and carry the recent runs, each item
// once per repository, members linking to their backups; the next run
// shows while backups are on.
func TestBackupSettingsCarryRolesAndRuns(t *testing.T) {
	svc := newFakeSetupBackups()
	h := backupHandler(t, authztest.New().Owner("olga"), svc)
	var st BackupSettings
	getJSON(t, h, "olga", "/api/v1/backup-settings", &st)
	if st.ID != "bs-1" || st.PrimaryRepositoryID != "r1" || st.SecondaryRepositoryID != "r2" || !st.Enabled {
		t.Fatalf("settings %+v", st)
	}
	if st.Schedule.NextRun == nil || !st.Schedule.NextRun.Equal(fakeNextRun) {
		t.Errorf("schedule %+v", st.Schedule)
	}
	if len(st.RecentSets) != 2 || st.RecentSets[0].ID != "s1b" {
		t.Fatalf("sets %+v", st.RecentSets)
	}
	m := st.RecentSets[0].Members
	if len(m) != 2 || m[0].RepositoryID != "r1" || m[0].BackupID != "bk-s1b" || m[1].RepositoryID != "r2" || m[1].BackupID != "" {
		t.Errorf("members %+v", m)
	}
	if !slices.Contains(st.Actions, "backup_policy.manage") || !slices.Contains(st.Actions, "backup.run") {
		t.Errorf("owner actions %v", st.Actions)
	}
	if st.ExcludeEnvironments == nil || st.ExcludeStacks == nil || st.ExcludeVolumes == nil {
		t.Errorf("exclusion lists must be arrays: %+v", st)
	}
}

// TestBackupSettingsNeedInstanceGrants: the setup covers every
// environment, so only instance grants apply: a grant on one environment
// or repository does not reach it.
func TestBackupSettingsNeedInstanceGrants(t *testing.T) {
	svc := newFakeSetupBackups()
	pol := authztest.New().
		Member("ivy", "readers").Group("readers", "allow backup_policy.read @all").
		Member("eve", "env").Group("env", "allow backup.run @env:e1", "allow backup_repository.read @backup_repository:r1")
	h := backupHandler(t, pol, svc)
	var st BackupSettings
	getJSON(t, h, "ivy", "/api/v1/backup-settings", &st)
	if slices.Contains(st.Actions, "backup_policy.manage") || slices.Contains(st.Actions, "backup.run") {
		t.Errorf("reader actions %v", st.Actions)
	}
	if r := authztest.Do(t, h, "ivy", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/backup-settings",
		Body: map[string]any{"excludeEnvironments": []string{"e1"}}, Headers: map[string]string{"If-Match": `"3"`}}); r.Status != http.StatusForbidden {
		t.Errorf("reader edits: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "eve", authztest.Call{Method: http.MethodPost, Path: "/api/v1/backup-settings/runs"}); r.Status != http.StatusForbidden {
		t.Errorf("environment grant runs everything: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "eve", authztest.Call{Method: http.MethodGet, Path: "/api/v1/backup-settings"}); r.Status != http.StatusForbidden {
		t.Errorf("environment grant reads the settings: %d", r.Status)
	}
}

// TestBackupSettingsSwapAndRun: making the Secondary the Primary is one
// patch (If-Match), and a run by someone who may not back up the manager
// state leaves it out.
func TestBackupSettingsSwapAndRun(t *testing.T) {
	svc := newFakeSetupBackups()
	pol := authztest.New().Owner("olga").Member("ron", "ops").Group("ops", "allow backup.run @all", "allow backup_policy.read @all")
	h := backupHandler(t, pol, svc)
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/backup-settings",
		Body:    map[string]any{"primaryRepositoryId": "r2", "secondaryRepositoryId": "r1"},
		Headers: map[string]string{"If-Match": `"3"`}})
	if r.Status != http.StatusOK {
		t.Fatalf("swap: %d %s", r.Status, r.Body)
	}
	var st BackupSettings
	if err := json.Unmarshal(r.Body, &st); err != nil {
		t.Fatal(err)
	}
	if st.PrimaryRepositoryID != "r2" || st.SecondaryRepositoryID != "r1" || st.Revision != 4 {
		t.Errorf("after the swap %+v", st)
	}
	if r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/backup-settings",
		Body: map[string]any{"secondaryRepositoryId": ""}, Headers: map[string]string{"If-Match": `"3"`}}); r.Status != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d %s", r.Status, r.Body)
	}

	for _, user := range []string{"olga", "ron"} {
		if r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodPost, Path: "/api/v1/backup-settings/runs"}); r.Status != http.StatusCreated {
			t.Fatalf("%s runs: %d %s", user, r.Status, r.Body)
		}
	}
	if len(svc.runs) != 2 || svc.runs[0].WithoutManager || !svc.runs[1].WithoutManager {
		t.Errorf("runs %+v, want the owner's with the manager state and ron's without", svc.runs)
	}
}
