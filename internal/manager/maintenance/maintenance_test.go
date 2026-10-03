package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/jobs/jobstest"
	"github.com/neurekadev/docker-manager/internal/manager/resources"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type envs struct{ db *bun.DB }

func (e envs) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	return store.GetEnvironment(ctx, e.db, id)
}

type requester struct {
	mu     sync.Mutex
	inputs []protocol.PruneInput
	// errs are the errors of environments that do not answer.
	errs map[string]error
}

func (r *requester) RequestEnvironment(_ context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != protocol.ReqMaintenancePreview {
		return nil, errors.New("unexpected request " + name)
	}
	if err := r.errs[env]; err != nil {
		return nil, err
	}
	r.inputs = append(r.inputs, input.(protocol.PruneInput))
	return json.Marshal(protocol.PrunePreviewOutput{Categories: []protocol.PruneCategoryPlan{}})
}

type stacks struct{}

func (stacks) List(_ context.Context, f domain.StackFilter) ([]domain.Stack, error) {
	if f.EnvironmentID != "env-1" || f.AfterID != "" {
		return nil, nil
	}
	return []domain.Stack{{ID: "st-1", EnvironmentID: "env-1", Name: "shop",
		Services: []domain.StackServiceDef{{Name: "api", Image: "shop/api:2"}},
		Images:   []domain.StackImage{{Service: "web", Image: "shop/web:1", ImageID: "sha256:abc"}}}}, nil
}

type specs struct{}

func (specs) ManagedSpecRefs(context.Context, string) ([]resources.SpecRef, error) {
	return []resources.SpecRef{{Kind: "image", Ref: "redis:7", Container: "cache"}, {Kind: "volume", Ref: "cache-data", Container: "cache"},
		{Kind: "network", Ref: "backend", Container: "cache"}}, nil
}

type fixture struct {
	ctx   context.Context
	db    *bun.DB
	clk   *clock.Fake
	eng   *jobs.Engine
	sched *scheduler.Service
	req   *requester
	svc   *Service
	user  authz.Principal
}

func newFixture(t *testing.T) *fixture {
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
	clk := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	now := clk.Now()
	for id, status := range map[string]domain.EnvironmentStatus{"env-1": domain.EnvironmentActive, "env-2": domain.EnvironmentActive,
		"env-old": domain.EnvironmentArchived} {
		env := domain.Environment{ID: id, Name: id, EngineID: "E-" + id, InstallID: "i-" + id, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := store.InsertEnvironment(ctx, db, &env); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := jobs.New(jobs.Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Dispatcher: jobstest.New(),
		Authorizer: authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision {
			return authz.Allow("test")
		})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	sched, err := scheduler.New(scheduler.Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Jobs: eng})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, db: db, clk: clk, eng: eng, sched: sched, req: &requester{}, user: authz.Principal{Kind: authz.KindUser, UserID: "u-1"}}
	f.svc, err = New(Options{DB: db, Clock: clk, Logger: testutil.Logger(t), Jobs: eng, Agents: f.req, Environments: envs{db}, Scheduler: sched,
		Stacks: stacks{}})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetSpecs(specs{})
	f.svc.SetBackupReferences(func(_ context.Context, env string) ([]BackupRef, error) {
		return []BackupRef{{Kind: "volume", Name: "restic-repo", Reason: "local backup repository"}}, nil
	})
	if err := sched.Register(scheduler.KindPrune, f.svc.PolicySource()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) update(t *testing.T, patch domain.MaintenanceSetupPatch) domain.MaintenanceSetup {
	t.Helper()
	st, err := f.svc.Setup(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, after, err := f.svc.UpdateSetup(f.ctx, st.Revision, patch)
	if err != nil {
		t.Fatal(err)
	}
	return after
}

func (f *fixture) patch(patch domain.MaintenanceSetupPatch) error {
	st, err := f.svc.Setup(f.ctx)
	if err != nil {
		return err
	}
	_, _, err = f.svc.UpdateSetup(f.ctx, st.Revision, patch)
	return err
}

func fieldOf(err error) string {
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func class(err error) string {
	var rej *scheduler.Rejection
	if errors.As(err, &rej) {
		return rej.Class
	}
	return "error: " + fmt.Sprint(err)
}

var on = true

// TestSetupStartsSafe: maintenance starts disabled with the suggestions
// (every rule off, 30 days) and never runs like that.
func TestSetupStartsSafe(t *testing.T) {
	f := newFixture(t)
	st, err := f.svc.Setup(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID == "" || st.Enabled || st.Cron != "0 3 * * 0" || st.TimeZone != "UTC" || len(st.ExcludeEnvironments) != 0 || st.LastRun != nil ||
		len(st.Rules) != 7 {
		t.Fatalf("setup %+v", st)
	}
	for i, r := range st.Rules {
		if r.Category != domain.PruneCategories()[i] || r.Enabled || r.MinAge != 30*24*time.Hour || r.VolumeOptIn || r.BuildCacheAll {
			t.Fatalf("suggested rule %+v", r)
		}
	}
	if _, err := f.svc.Run(f.ctx, f.user, ""); !errors.Is(err, domain.ErrMaintenanceEmpty) {
		t.Fatalf("run without rules: %v", err)
	}
	src := f.svc.PolicySource()
	if _, err := src.Jobs(f.ctx, scheduler.Due{PolicyID: st.ID}); class(err) != scheduler.RejectPolicyDisabled {
		t.Fatalf("scheduled run while disabled: %v", err)
	}
	scheds, err := src.Schedules(f.ctx)
	if err != nil || len(scheds) != 1 || scheds[0].Enabled || scheds[0].PolicyID != st.ID || scheds[0].EnvironmentID != "" ||
		scheds[0].Name != ScheduleName {
		t.Fatalf("schedules %+v %v", scheds, err)
	}
}

// TestSetupCoversEveryEnvironmentButTheLeftOut: previews and runs reach
// every active environment except the ones left out; unknown IDs are
// dropped when saved; leaving every environment out refuses runs.
func TestSetupCoversEveryEnvironmentButTheLeftOut(t *testing.T) {
	f := newFixture(t)
	rule := domain.MaintenanceRule{Category: domain.PruneDanglingImages, Enabled: true, MinAge: time.Hour}
	st := f.update(t, domain.MaintenanceSetupPatch{Enabled: &on, Rules: []domain.MaintenanceRule{rule}})
	previews, err := f.svc.Preview(f.ctx)
	if err != nil || len(previews) != 2 || previews[0].EnvironmentID == previews[1].EnvironmentID {
		t.Fatalf("previews %+v: %v", previews, err)
	}
	reqs, err := f.svc.PolicySource().Jobs(f.ctx, scheduler.Due{PolicyID: st.ID})
	if err != nil || len(reqs) != 2 || reqs[0].EnvironmentID == reqs[1].EnvironmentID {
		t.Fatalf("scheduled requests %+v: %v", reqs, err)
	}

	st = f.update(t, domain.MaintenanceSetupPatch{ExcludeEnvironments: &[]string{"env-2", "env-gone", "env-2"}})
	if !slices.Equal(st.ExcludeEnvironments, []string{"env-2"}) {
		t.Fatalf("left out %v", st.ExcludeEnvironments)
	}
	if again, _ := f.svc.Setup(f.ctx); !slices.Equal(again.ExcludeEnvironments, []string{"env-2"}) || !again.Excludes("env-2") {
		t.Fatalf("stored left out %v", again.ExcludeEnvironments)
	}
	reqs, err = f.svc.PolicySource().Jobs(f.ctx, scheduler.Due{PolicyID: st.ID})
	if err != nil || len(reqs) != 1 || reqs[0].EnvironmentID != "env-1" {
		t.Fatalf("requests with env-2 left out %+v: %v", reqs, err)
	}
	if previews, err := f.svc.Preview(f.ctx); err != nil || len(previews) != 1 || previews[0].EnvironmentID != "env-1" {
		t.Fatalf("previews with env-2 left out %+v: %v", previews, err)
	}

	f.update(t, domain.MaintenanceSetupPatch{ExcludeEnvironments: &[]string{"env-1", "env-2"}})
	if _, err := f.svc.Run(f.ctx, f.user, ""); !errors.Is(err, domain.ErrMaintenanceNoEnvironments) {
		t.Fatalf("run with every environment left out: %v", err)
	}
	if c := class(f.svc.PolicySource().Validate(f.ctx, st.ID)); c != RejectNoEnvironments {
		t.Fatalf("scheduled run with every environment left out: %s", c)
	}
}

// TestVolumeRulesNeedTheirOwnOptIn: enabling an anonymous or named volume
// rule needs volumeOptIn on that rule.
func TestVolumeRulesNeedTheirOwnOptIn(t *testing.T) {
	f := newFixture(t)
	named := domain.MaintenanceRule{Category: domain.PruneNamedVolumes, Enabled: true, MinAge: time.Hour}
	if err := f.patch(domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{named}}); fieldOf(err) != "rules.named_volumes.volumeOptIn" {
		t.Fatalf("named without opt-in: %v", err)
	}
	named.VolumeOptIn = true
	st := f.update(t, domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{named}})
	for _, r := range st.Rules {
		if r.Category == domain.PruneAnonymousVolumes && r.Enabled {
			t.Fatal("anonymous volumes enabled by the named opt-in")
		}
	}
	anon := domain.MaintenanceRule{Category: domain.PruneAnonymousVolumes, Enabled: true, MinAge: time.Hour}
	if err := f.patch(domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{anon}}); fieldOf(err) != "rules.anonymous_volumes.volumeOptIn" {
		t.Fatalf("anonymous without opt-in: %v", err)
	}
}

func TestRuleValidation(t *testing.T) {
	f := newFixture(t)
	for want, rules := range map[string][]domain.MaintenanceRule{
		"rules":                                    {{Category: "system_prune"}},
		"rules.build_cache.includeLabels":          {{Category: domain.PruneBuildCache, IncludeLabels: []string{"a=b"}}},
		"rules.unused_images.containerStates":      {{Category: domain.PruneUnusedImages, ContainerStates: []string{"exited"}}},
		"rules.stopped_containers.containerStates": {{Category: domain.PruneStoppedContainers, ContainerStates: []string{"running"}}},
		"rules.unused_networks.volumeOptIn":        {{Category: domain.PruneUnusedNetworks, VolumeOptIn: true}},
		"rules.dangling_images.buildCacheAll":      {{Category: domain.PruneDanglingImages, BuildCacheAll: true}},
		"rules.unused_images.excludeLabels":        {{Category: domain.PruneUnusedImages, ExcludeLabels: []string{"=x"}}},
	} {
		if err := f.patch(domain.MaintenanceSetupPatch{Rules: rules}); fieldOf(err) != want {
			t.Errorf("%v: %v, want field %s", rules, err, want)
		}
	}
	dup := []domain.MaintenanceRule{{Category: domain.PruneBuildCache}, {Category: domain.PruneBuildCache}}
	if err := f.patch(domain.MaintenanceSetupPatch{Rules: dup}); fieldOf(err) != "rules" {
		t.Errorf("duplicate: %v", err)
	}
	bad := "61 * * * *"
	if err := f.patch(domain.MaintenanceSetupPatch{Cron: &bad}); !errors.Is(err, domain.ErrScheduleInvalid) {
		t.Errorf("invalid cron: %v", err)
	}
	st, _ := f.svc.Setup(f.ctx)
	if _, _, err := f.svc.UpdateSetup(f.ctx, st.Revision+1, domain.MaintenanceSetupPatch{Enabled: &on}); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Errorf("stale revision: %v", err)
	}
	if again, _ := f.svc.Setup(f.ctx); again.Revision != st.Revision || again.Enabled {
		t.Errorf("a refused change was saved: %+v", again)
	}
}

// TestRunInputCarriesRulesAndProtections: a run sends only the enabled
// rules and every protection the manager knows.
func TestRunInputCarriesRulesAndProtections(t *testing.T) {
	f := newFixture(t)
	st := f.update(t, domain.MaintenanceSetupPatch{ExcludeEnvironments: &[]string{"env-2"}, Rules: []domain.MaintenanceRule{{
		Category: domain.PruneUnusedImages, Enabled: true, MinAge: 48 * time.Hour, ExcludeLabels: []string{"keep"}}}})
	js, err := f.svc.Run(f.ctx, f.user, "k1")
	if err != nil || len(js) != 1 {
		t.Fatalf("run: %+v %v", js, err)
	}
	j := js[0]
	if j.Kind != jobspec.PruneRun || j.PolicyID != st.ID || j.EnvironmentID != "env-1" || j.Origin != domain.OriginManual ||
		j.IdempotencyKey != "k1/env-1" || len(j.Targets) != 1 || j.Targets[0].Type != domain.TargetMaintenancePolicy || j.Targets[0].ID != st.ID {
		t.Fatalf("job %+v", j)
	}
	var in protocol.PruneInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if in.PolicyID != st.ID || len(in.Rules) != 1 || in.Rules[0].Category != domain.PruneUnusedImages || in.Rules[0].MinAgeSeconds != 48*3600 ||
		!slices.Equal(in.Rules[0].ExcludeLabels, []string{"keep"}) {
		t.Fatalf("input %+v", in)
	}
	refs := func(rs []protocol.ProtectedRef) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Ref)
		}
		slices.Sort(out)
		return out
	}
	if got := refs(in.Protect.Projects); !slices.Equal(got, []string{"shop"}) {
		t.Errorf("projects %v", got)
	}
	if got := refs(in.Protect.Images); !slices.Equal(got, []string{"redis:7", "sha256:abc", "shop/api:2", "shop/web:1"}) {
		t.Errorf("images %v", got)
	}
	if got := refs(in.Protect.Volumes); !slices.Equal(got, []string{"cache-data", "restic-repo"}) {
		t.Errorf("volumes %v", got)
	}
	if got := refs(in.Protect.Networks); !slices.Equal(got, []string{"backend"}) {
		t.Errorf("networks %v", got)
	}
	for _, r := range in.Protect.Volumes {
		if r.Ref == "restic-repo" && r.Reason != "local backup repository" {
			t.Errorf("backup reason %q", r.Reason)
		}
	}
}

// TestRunOverlap: a run starts one job per environment; another run while
// one is active is refused.
func TestRunOverlap(t *testing.T) {
	f := newFixture(t)
	f.update(t, domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{{Category: domain.PruneStoppedContainers, Enabled: true,
		MinAge: time.Hour}}})
	js, err := f.svc.Run(f.ctx, f.user, "")
	if err != nil || len(js) != 2 {
		t.Fatalf("run: %+v %v", js, err)
	}
	var active *domain.MaintenanceRunActiveError
	if _, err := f.svc.Run(f.ctx, f.user, "k2"); !errors.As(err, &active) || (active.JobID != js[0].ID && active.JobID != js[1].ID) {
		t.Fatalf("overlap: %v", err)
	}
}

// TestPolicySourceRevalidates: when due and at dispatch, a disabled or
// emptied setup, or a schedule of another policy ID, is rejected; a valid
// one yields one prune.run request per environment.
func TestPolicySourceRevalidates(t *testing.T) {
	f := newFixture(t)
	src := f.svc.PolicySource()
	rule := domain.MaintenanceRule{Category: domain.PruneDanglingImages, Enabled: true, MinAge: time.Hour}
	st := f.update(t, domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{rule}})
	if c := class(src.Validate(f.ctx, st.ID)); c != scheduler.RejectPolicyDisabled {
		t.Fatalf("disabled: %s", c)
	}
	st = f.update(t, domain.MaintenanceSetupPatch{Enabled: &on})
	if err := src.Validate(f.ctx, st.ID); err != nil {
		t.Fatalf("valid: %v", err)
	}
	reqs, err := src.Jobs(f.ctx, scheduler.Due{PolicyID: st.ID})
	if err != nil || len(reqs) != 2 || reqs[0].Kind != jobspec.PruneRun || reqs[0].Targets[0].ID != st.ID || reqs[0].PolicyID != st.ID {
		t.Fatalf("jobs %+v %v", reqs, err)
	}
	if c := class(src.Validate(f.ctx, "old-policy")); c != scheduler.RejectPolicyNotFound {
		t.Fatalf("another policy ID: %s", c)
	}
	off := rule
	off.Enabled = false
	f.update(t, domain.MaintenanceSetupPatch{Rules: []domain.MaintenanceRule{off}})
	if c := class(src.Validate(f.ctx, st.ID)); c != RejectNoRules {
		t.Fatalf("no rules: %s", c)
	}
}

// TestFinishHookRecordsTheLatestRun: the summary of the latest run is kept
// on the setup; malformed output is tolerated.
func TestFinishHookRecordsTheLatestRun(t *testing.T) {
	f := newFixture(t)
	st, _ := f.svc.Setup(f.ctx)
	done := f.clk.Now()
	out, _ := json.Marshal(protocol.PruneRunOutput{Removed: 3, Skipped: 1, Failed: 1, Deferred: 2, BytesReclaimed: 99})
	j := domain.Job{ID: "job-1", Kind: jobspec.PruneRun, PolicyID: st.ID, State: domain.JobPartial, Origin: domain.OriginScheduled,
		FinishedAt: &done, ResultOutput: out}
	if err := f.svc.onRunFinished(f.ctx, f.db, j); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Setup(f.ctx)
	if r := got.LastRun; r == nil || r.JobID != "job-1" || r.State != domain.JobPartial || r.Removed != 3 || r.Skipped != 1 || r.Failed != 1 ||
		r.Deferred != 2 || r.BytesReclaimed != 99 || r.Origin != domain.OriginScheduled || !r.FinishedAt.Equal(done) {
		t.Fatalf("last run %+v", got.LastRun)
	}
	if got.Revision != st.Revision {
		t.Fatal("recording a run changed the setup's revision")
	}
	j.ID, j.ResultOutput, j.State = "job-2", []byte("{not json"), domain.JobCancelled
	if err := f.svc.onRunFinished(f.ctx, f.db, j); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.Setup(f.ctx)
	if r := got.LastRun; r.JobID != "job-2" || r.State != domain.JobCancelled || r.Removed != 0 {
		t.Fatalf("malformed output: %+v", r)
	}
	// Jobs without a policy (one-off prunes) or of another policy are ignored.
	for _, other := range []domain.Job{{ID: "x", Kind: jobspec.PruneRun, State: domain.JobSucceeded},
		{ID: "y", Kind: jobspec.PruneRun, PolicyID: "old-policy", State: domain.JobSucceeded}} {
		if err := f.svc.onRunFinished(f.ctx, f.db, other); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ = f.svc.Setup(f.ctx); got.LastRun.JobID != "job-2" {
		t.Fatalf("another job recorded: %+v", got.LastRun)
	}
}

// TestPreviewReportsEachEnvironment: an environment whose agent cannot
// answer reports a stable Docker error; the others are previewed.
func TestPreviewReportsEachEnvironment(t *testing.T) {
	f := newFixture(t)
	f.req.errs = map[string]error{"env-2": jobs.ErrAgentOffline}
	previews, err := f.svc.Preview(f.ctx)
	if err != nil || len(previews) != 2 {
		t.Fatalf("previews %+v: %v", previews, err)
	}
	for _, p := range previews {
		var de *domain.DockerError
		switch p.EnvironmentID {
		case "env-1":
			if p.Err != nil {
				t.Fatalf("env-1: %v", p.Err)
			}
		case "env-2":
			if !errors.As(p.Err, &de) || de.Code != domain.DockerEnvironmentOffline {
				t.Fatalf("env-2 offline: %v", p.Err)
			}
		}
	}
	// A preview evaluates the enabled rules only.
	if in := f.req.inputs[len(f.req.inputs)-1]; len(in.Rules) != 0 {
		t.Fatalf("preview rules %+v", in.Rules)
	}
}

// TestManualPrune: a one-off prune runs exactly the given rules with the
// policies' protections, as a job without policy or targets (authorized on
// the environment); volume rules need their opt-in, a repeated key returns
// the same run, empty rule sets and archived environments are refused.
func TestManualPrune(t *testing.T) {
	f := newFixture(t)
	rules := []domain.MaintenanceRule{{Category: domain.PruneDanglingImages, Enabled: true, MinAge: 24 * time.Hour},
		{Category: domain.PruneUnusedImages, MinAge: 24 * time.Hour}}
	j, err := f.svc.RunManual(f.ctx, f.user, "env-1", rules, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobspec.PruneRun || j.PolicyID != "" || j.EnvironmentID != "env-1" || len(j.Targets) != 0 || j.Origin != domain.OriginManual {
		t.Fatalf("job %+v", j)
	}
	var in protocol.PruneInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Rules) != 1 || in.Rules[0].Category != domain.PruneDanglingImages || in.Rules[0].MinAgeSeconds != 24*3600 ||
		len(in.PolicyID) <= len(ManualPolicyPrefix) || in.PolicyID[:len(ManualPolicyPrefix)] != ManualPolicyPrefix || len(in.Protect.Projects) != 1 {
		t.Fatalf("input %+v", in)
	}
	again, err := f.svc.RunManual(f.ctx, f.user, "env-1", rules, "m1")
	if err != nil || again.ID != j.ID {
		t.Fatalf("repeated key: %v %s != %s", err, again.ID, j.ID)
	}
	if _, err := f.svc.RunManual(f.ctx, f.user, "env-2", rules, "m1"); !errors.Is(err, domain.ErrJobIdempotencyConflict) {
		t.Fatalf("key reused for another environment: %v", err)
	}

	volume := []domain.MaintenanceRule{{Category: domain.PruneAnonymousVolumes, Enabled: true, MinAge: 24 * time.Hour}}
	var fe *domain.FieldError
	if _, err := f.svc.RunManual(f.ctx, f.user, "env-1", volume, ""); !errors.As(err, &fe) || fe.Field != "rules.anonymous_volumes.volumeOptIn" {
		t.Fatalf("volume rule without opt-in: %v", err)
	}
	if _, err := f.svc.PreviewManual(f.ctx, "env-1", volume); err != nil {
		t.Fatalf("previews evaluate volume rules without the opt-in: %v", err)
	}
	volume[0].VolumeOptIn = true
	if _, err := f.svc.RunManual(f.ctx, f.user, "env-1", volume, ""); err != nil {
		t.Fatalf("volume rule with opt-in: %v", err)
	}
	off := []domain.MaintenanceRule{{Category: domain.PruneDanglingImages, MinAge: 24 * time.Hour}}
	if _, err := f.svc.PreviewManual(f.ctx, "env-1", off); !errors.As(err, &fe) || fe.Field != "rules" {
		t.Fatalf("no enabled rule: %v", err)
	}
	if _, err := f.svc.RunManual(f.ctx, f.user, "env-old", rules, ""); !errors.Is(err, domain.ErrEnvironmentArchived) {
		t.Fatalf("archived environment: %v", err)
	}
}
