package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
)

// fakePolicyBackups serves three policies (p1 enabled with two sets, p2
// disabled with one, p3 without runs) and counts the batched lookups. The
// other BackupService methods are not used by these tests.
type fakePolicyBackups struct {
	BackupService

	mu         sync.Mutex
	recentArgs [][]string
	backupArgs [][]string
	nextArgs   [][]string
}

var (
	fakeSetStart = time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	fakeNextRun  = time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
)

func (f *fakePolicyBackups) policies() []domain.BackupPolicy {
	return []domain.BackupPolicy{
		{ID: "p1", Name: "Nightly", Enabled: true, Cron: "0 2 * * *", TimeZone: "UTC", RepositoryID: "r1", Revision: 3},
		{ID: "p2", Name: "Weekly", Cron: "0 3 * * 0", TimeZone: "UTC", RepositoryID: "r1", Revision: 1},
		{ID: "p3", Name: "New", Enabled: true, Cron: "0 4 * * *", TimeZone: "UTC", RepositoryID: "r1", Revision: 1},
	}
}

func (f *fakePolicyBackups) ListPolicies(_ context.Context, afterID string, limit int) ([]domain.BackupPolicy, error) {
	var out []domain.BackupPolicy
	for _, p := range f.policies() {
		if p.ID > afterID {
			out = append(out, p)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakePolicyBackups) GetPolicy(_ context.Context, id string) (domain.BackupPolicy, error) {
	for _, p := range f.policies() {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.BackupPolicy{}, domain.ErrBackupPolicyNotFound
}

func fakeSet(id, policyID string, hours int) domain.BackupSet {
	at := fakeSetStart.Add(-time.Duration(hours) * time.Hour)
	return domain.BackupSet{ID: id, PolicyID: policyID, Origin: domain.OriginScheduled, State: "complete", StartedAt: at, FinishedAt: &at,
		Members: []domain.BackupSetMember{
			{Item: "stack/st-1", Kind: "stack", Scope: "docker-manager-env-e1", EnvironmentID: "e1", StackID: "st-1", StackName: "shop",
				State: "complete", SnapshotID: "restic-" + id, SnapshotTime: &at, JobID: "job-" + id},
			{Item: "volume/media", Kind: "volume", Scope: "docker-manager-env-e1", EnvironmentID: "e1", Volume: "media", State: "failed",
				ErrorClass: "agent_offline"},
		}}
}

func (f *fakePolicyBackups) RecentSets(_ context.Context, policyIDs []string, perPolicy int) (map[string][]domain.BackupSet, error) {
	f.mu.Lock()
	f.recentArgs = append(f.recentArgs, slices.Clone(policyIDs))
	f.mu.Unlock()
	all := map[string][]domain.BackupSet{
		"p1": {fakeSet("s1b", "p1", 0), fakeSet("s1a", "p1", 24)},
		"p2": {fakeSet("s2a", "p2", 48)},
	}
	out := map[string][]domain.BackupSet{}
	for _, id := range policyIDs {
		if sets := all[id]; len(sets) > 0 {
			out[id] = sets[:min(perPolicy, len(sets))]
		}
	}
	return out, nil
}

func (f *fakePolicyBackups) SetBackups(_ context.Context, setIDs []string) ([]domain.BackupSnapshot, error) {
	f.mu.Lock()
	ids := slices.Clone(setIDs)
	slices.Sort(ids)
	f.backupArgs = append(f.backupArgs, ids)
	f.mu.Unlock()
	var out []domain.BackupSnapshot
	for _, id := range setIDs {
		out = append(out, domain.BackupSnapshot{ID: "bk-" + id, SetID: id, RepositoryID: "r1", Scope: "docker-manager-env-e1",
			EnvironmentID: "e1", Kind: "stack", Item: "stack/st-1", ResticSnapshotID: "restic-" + id})
	}
	return out, nil
}

func (f *fakePolicyBackups) NextRuns(_ context.Context, kind string, policyIDs []string) map[string]time.Time {
	f.mu.Lock()
	f.nextArgs = append(f.nextArgs, append([]string{kind}, policyIDs...))
	f.mu.Unlock()
	out := map[string]time.Time{}
	for _, id := range policyIDs {
		if id == "p1" {
			out[id] = fakeNextRun
		}
	}
	return out
}

func policyListHandler(t *testing.T, pol *authztest.Policy, svc BackupService) http.Handler {
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

// TestBackupPolicyListCarriesRuns: every policy of the list shown in full
// has the recent sets and next run of its detail, looked up in one batch
// per page (one call for the sets, one for their backups, one for the
// next runs) instead of once per policy; members link to their backups.
func TestBackupPolicyListCarriesRuns(t *testing.T) {
	svc := &fakePolicyBackups{}
	h := policyListHandler(t, authztest.New().Owner("olga"), svc)

	var page Page[BackupPolicy]
	getJSON(t, h, "olga", "/api/v1/backup-policies", &page)
	if len(page.Items) != 3 {
		t.Fatalf("policies %+v", page.Items)
	}
	if !slices.EqualFunc(svc.recentArgs, [][]string{{"p1", "p2", "p3"}}, slices.Equal) {
		t.Errorf("recent-set lookups %v", svc.recentArgs)
	}
	if !slices.EqualFunc(svc.backupArgs, [][]string{{"s1a", "s1b", "s2a"}}, slices.Equal) {
		t.Errorf("backup lookups %v", svc.backupArgs)
	}
	// Only enabled policies have a next run to look up.
	if !slices.EqualFunc(svc.nextArgs, [][]string{{"backup", "p1", "p3"}}, slices.Equal) {
		t.Errorf("next-run lookups %v", svc.nextArgs)
	}

	p1, p2, p3 := page.Items[0], page.Items[1], page.Items[2]
	if len(p1.RecentSets) != 2 || p1.RecentSets[0].ID != "s1b" || p1.RecentSets[1].ID != "s1a" {
		t.Fatalf("p1 sets %+v", p1.RecentSets)
	}
	if p1.Schedule == nil || p1.Schedule.NextRun == nil || !p1.Schedule.NextRun.Equal(fakeNextRun) {
		t.Errorf("p1 schedule %+v", p1.Schedule)
	}
	m := p1.RecentSets[0].Members
	if len(m) != 2 || m[0].BackupID != "bk-s1b" || m[0].JobID != "job-s1b" || m[1].BackupID != "" || m[1].ErrorClass != "agent_offline" {
		t.Errorf("p1 members %+v", m)
	}
	if len(p2.RecentSets) != 1 || p2.Schedule == nil || p2.Schedule.NextRun != nil {
		t.Errorf("p2 (disabled) %+v %+v", p2.RecentSets, p2.Schedule)
	}
	if len(p3.RecentSets) != 0 || p3.Schedule == nil || p3.Schedule.NextRun != nil {
		t.Errorf("p3 (no runs) %+v %+v", p3.RecentSets, p3.Schedule)
	}

	// The detail has the same shape.
	var one BackupPolicy
	getJSON(t, h, "olga", "/api/v1/backup-policies/p1", &one)
	a, _ := json.Marshal(one.RecentSets)
	b, _ := json.Marshal(p1.RecentSets)
	if string(a) != string(b) || one.Schedule == nil || one.Schedule.NextRun == nil || !one.Schedule.NextRun.Equal(*p1.Schedule.NextRun) {
		t.Errorf("detail %s %+v differs from the list %s", a, one.Schedule, b)
	}
}

// TestBackupPolicyListPagesAndShapes: a page looks up only its own
// policies and keeps its cursor; minimal views carry no runs; members
// link only to backups the caller may see.
func TestBackupPolicyListPagesAndShapes(t *testing.T) {
	svc := &fakePolicyBackups{}
	pol := authztest.New().Owner("olga").
		Member("mia", "readers").Group("readers", "allow backup_policy.read @backup_policy:p1", "allow backup.run @backup_policy:p2").
		Member("bea", "backups").Group("backups", "allow backup_policy.read @backup_policy:p1", "allow backup.read @backup_repository:r1")
	h := policyListHandler(t, pol, svc)

	var first Page[BackupPolicy]
	getJSON(t, h, "olga", "/api/v1/backup-policies?limit=2", &first)
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	var rest Page[BackupPolicy]
	getJSON(t, h, "olga", "/api/v1/backup-policies?limit=2&cursor="+url.QueryEscape(first.NextCursor), &rest)
	if len(rest.Items) != 1 || rest.Items[0].ID != "p3" {
		t.Fatalf("second page %+v", rest)
	}
	if !slices.EqualFunc(svc.recentArgs, [][]string{{"p1", "p2"}, {"p3"}}, slices.Equal) {
		t.Errorf("recent-set lookups per page %v", svc.recentArgs)
	}

	svc.recentArgs = nil
	var mia Page[BackupPolicy]
	getJSON(t, h, "mia", "/api/v1/backup-policies", &mia)
	if len(mia.Items) != 2 || mia.Items[0].View != "full" || mia.Items[1].View != "minimal" {
		t.Fatalf("mia sees %+v", mia.Items)
	}
	if !slices.EqualFunc(svc.recentArgs, [][]string{{"p1"}}, slices.Equal) {
		t.Errorf("minimal views looked up: %v", svc.recentArgs)
	}
	if len(mia.Items[1].RecentSets) != 0 || mia.Items[1].Schedule != nil {
		t.Errorf("minimal view carries runs: %+v", mia.Items[1])
	}
	// mia may not see the backups: the members do not link to them.
	if s := mia.Items[0].RecentSets; len(s) != 2 || s[0].Members[0].BackupID != "" {
		t.Errorf("mia's members %+v", s)
	}

	var bea Page[BackupPolicy]
	getJSON(t, h, "bea", "/api/v1/backup-policies", &bea)
	if len(bea.Items) != 1 || len(bea.Items[0].RecentSets) != 2 || bea.Items[0].RecentSets[0].Members[0].BackupID != "bk-s1b" {
		t.Errorf("bea sees %+v", bea.Items)
	}
}

// TestNoRunsWithoutFullViews: a page without full views looks nothing up.
func TestNoRunsWithoutFullViews(t *testing.T) {
	svc := &fakePolicyBackups{}
	pol := authztest.New().Member("rex", "runners").Group("runners", "allow backup.run @backup_policy:p2")
	h := policyListHandler(t, pol, svc)
	var page Page[BackupPolicy]
	getJSON(t, h, "rex", "/api/v1/backup-policies", &page)
	if len(page.Items) != 1 || page.Items[0].View != "minimal" {
		t.Fatalf("rex sees %+v", page.Items)
	}
	if svc.recentArgs != nil || svc.backupArgs != nil || svc.nextArgs != nil {
		t.Errorf("lookups for minimal views: %v %v %v", svc.recentArgs, svc.backupArgs, svc.nextArgs)
	}
}
