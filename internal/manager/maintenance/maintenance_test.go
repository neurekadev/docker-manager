package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/resources"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type envs struct{ db *bun.DB }

func (e envs) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	return store.GetEnvironment(ctx, e.db, id)
}

type requester struct {
	mu     sync.Mutex
	inputs []protocol.PruneInput
	err    error
}

func (r *requester) RequestEnvironment(_ context.Context, _, name string, input any, _ time.Duration) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != protocol.ReqMaintenancePreview {
		return nil, errors.New("unexpected request " + name)
	}
	if r.err != nil {
		return nil, r.err
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
	n     int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
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

func (f *fixture) create(t *testing.T, env string, rules ...domain.MaintenanceRule) domain.MaintenancePolicy {
	t.Helper()
	f.n++
	p, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: env, Name: "policy " + strconv.Itoa(f.n), Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fieldOf(err error) string {
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

// TestDefaultsAreSafe: suggestions prefill new policies with every rule
// and the schedule disabled; such a policy never runs.
func TestDefaultsAreSafe(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.Defaults(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rules) != 7 || d.Revision != 1 {
		t.Fatalf("defaults %+v", d)
	}
	for i, r := range d.Rules {
		if r.Category != domain.PruneCategories()[i] || r.Enabled || r.MinAge != 30*24*time.Hour || r.VolumeOptIn || r.BuildCacheAll {
			t.Fatalf("suggested rule %+v", r)
		}
	}
	p := f.create(t, "env-1")
	if p.ScheduleEnabled || p.Cron != "0 3 * * 0" || p.TimeZone != "UTC" || len(domain.EnabledRules(p.Rules)) != 0 || len(p.Rules) != 7 {
		t.Fatalf("new policy %+v", p)
	}
	if _, err := f.svc.Run(f.ctx, f.user, p, ""); !errors.Is(err, domain.ErrMaintenancePolicyEmpty) {
		t.Fatalf("run of a new policy: %v", err)
	}
	src := f.svc.PolicySource()
	var rej *scheduler.Rejection
	if _, err := src.Jobs(f.ctx, scheduler.Due{PolicyID: p.ID}); !errors.As(err, &rej) || rej.Class != scheduler.RejectPolicyDisabled {
		t.Fatalf("scheduled run of a new policy: %v", err)
	}
	scheds, err := src.Schedules(f.ctx)
	if err != nil || len(scheds) != 1 || scheds[0].Enabled {
		t.Fatalf("schedules %+v %v", scheds, err)
	}
	// Changing the defaults prefills later policies only.
	_, after, err := f.svc.UpdateDefaults(f.ctx, 1, []domain.MaintenanceRule{{Category: domain.PruneStoppedContainers, Enabled: true, MinAge: time.Hour}})
	if err != nil || after.Revision != 2 {
		t.Fatalf("update defaults: %+v %v", after, err)
	}
	if again, _ := f.svc.Get(f.ctx, p.ID); len(domain.EnabledRules(again.Rules)) != 0 {
		t.Fatal("changing the defaults changed an existing policy")
	}
	if p2 := f.create(t, "env-2"); len(domain.EnabledRules(p2.Rules)) != 1 || p2.Rules[0].MinAge != time.Hour {
		t.Fatalf("policy after the defaults changed: %+v", p2.Rules)
	}
	if _, _, err := f.svc.UpdateDefaults(f.ctx, 1, nil); !errors.Is(err, domain.ErrRevisionMismatch) {
		t.Fatalf("stale defaults update: %v", err)
	}
}

func TestAllEnvironmentsPolicyFansOutWithoutOverlap(t *testing.T) {
	f := newFixture(t)
	rule := domain.MaintenanceRule{Category: domain.PruneDanglingImages, Enabled: true, MinAge: time.Hour}
	p := f.create(t, "", rule)
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: "overlap", Rules: []domain.MaintenanceRule{rule}}); !errors.Is(err, domain.ErrMaintenanceScopeOverlap) {
		t.Fatalf("overlapping policy: %v", err)
	}
	previews, err := f.svc.PreviewEnvironments(f.ctx, p)
	if err != nil || len(previews) != 2 {
		t.Fatalf("previews %+v: %v", previews, err)
	}
	on := true
	_, p, err = f.svc.Update(f.ctx, p.ID, p.Revision, domain.MaintenancePolicyPatch{ScheduleEnabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := f.svc.PolicySource().Jobs(f.ctx, scheduler.Due{PolicyID: p.ID})
	if err != nil || len(reqs) != 2 || reqs[0].EnvironmentID == reqs[1].EnvironmentID {
		t.Fatalf("scheduled requests %+v: %v", reqs, err)
	}
}

// TestVolumeRulesNeedTheirOwnOptIn: enabling an anonymous or named volume
// rule needs volumeOptIn on that rule.
func TestVolumeRulesNeedTheirOwnOptIn(t *testing.T) {
	f := newFixture(t)
	named := domain.MaintenanceRule{Category: domain.PruneNamedVolumes, Enabled: true, MinAge: time.Hour}
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: "v", Rules: []domain.MaintenanceRule{named}}); fieldOf(err) != "rules.named_volumes.volumeOptIn" {
		t.Fatalf("named without opt-in: %v", err)
	}
	named.VolumeOptIn = true
	p := f.create(t, "env-1", named)
	for _, r := range p.Rules {
		if r.Category == domain.PruneAnonymousVolumes && r.Enabled {
			t.Fatal("anonymous volumes enabled by the named opt-in")
		}
	}
	anon := domain.MaintenanceRule{Category: domain.PruneAnonymousVolumes, Enabled: true, MinAge: time.Hour}
	if _, _, err := f.svc.Update(f.ctx, p.ID, p.Revision, domain.MaintenancePolicyPatch{Rules: []domain.MaintenanceRule{anon}}); fieldOf(err) != "rules.anonymous_volumes.volumeOptIn" {
		t.Fatalf("anonymous without opt-in: %v", err)
	}
	if _, _, err := f.svc.UpdateDefaults(f.ctx, 1, []domain.MaintenanceRule{anon}); fieldOf(err) != "rules.anonymous_volumes.volumeOptIn" {
		t.Fatalf("defaults without opt-in: %v", err)
	}
	// A disabled volume rule needs no opt-in; previews evaluate without it.
	if _, err := f.svc.Preview(f.ctx, p, []domain.MaintenanceRule{anon}, false); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if in := f.req.inputs[len(f.req.inputs)-1]; len(in.Rules) != 2 {
		t.Fatalf("preview rules %+v", in.Rules)
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
		_, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: "x", Rules: rules})
		if fieldOf(err) != want {
			t.Errorf("%v: %v, want field %s", rules, err, want)
		}
	}
	dup := []domain.MaintenanceRule{{Category: domain.PruneBuildCache}, {Category: domain.PruneBuildCache}}
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: "x", Rules: dup}); fieldOf(err) != "rules" {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-old", Name: "x"}); !errors.Is(err, domain.ErrEnvironmentArchived) {
		t.Errorf("archived environment: %v", err)
	}
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: "x", Cron: "61 * * * *"}); !errors.Is(err, domain.ErrScheduleInvalid) {
		t.Errorf("invalid cron: %v", err)
	}
	f.create(t, "env-1")
	p, _ := f.svc.List(f.ctx, "env-1", "", 0)
	if _, err := f.svc.Create(f.ctx, domain.MaintenancePolicyCreate{EnvironmentID: "env-1", Name: p[0].Name}); !errors.Is(err, domain.ErrMaintenanceScopeOverlap) {
		t.Errorf("overlapping scope: %v", err)
	}
}

// TestRunInputCarriesRulesAndProtections: a run sends only the enabled
// rules and every protection the manager knows.
func TestRunInputCarriesRulesAndProtections(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "env-1", domain.MaintenanceRule{Category: domain.PruneUnusedImages, Enabled: true, MinAge: 48 * time.Hour,
		ExcludeLabels: []string{"keep"}})
	j, err := f.svc.Run(f.ctx, f.user, p, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobspec.PruneRun || j.PolicyID != p.ID || j.EnvironmentID != "env-1" || j.Origin != domain.OriginManual ||
		len(j.Targets) != 1 || j.Targets[0].Type != domain.TargetMaintenancePolicy || j.Targets[0].ID != p.ID {
		t.Fatalf("job %+v", j)
	}
	var in protocol.PruneInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Rules) != 1 || in.Rules[0].Category != domain.PruneUnusedImages || in.Rules[0].MinAgeSeconds != 48*3600 ||
		!slices.Equal(in.Rules[0].ExcludeLabels, []string{"keep"}) {
		t.Fatalf("rules %+v", in.Rules)
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

// TestRunIdempotencyAndOverlap: a repeated key returns the same run; a
// second run of the policy while one is active is refused; a key used for
// another policy conflicts.
func TestRunIdempotencyAndOverlap(t *testing.T) {
	f := newFixture(t)
	rule := domain.MaintenanceRule{Category: domain.PruneStoppedContainers, Enabled: true, MinAge: time.Hour}
	p := f.create(t, "env-1", rule)
	j1, err := f.svc.Run(f.ctx, f.user, p, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.svc.Run(f.ctx, f.user, p, "k1"); err != nil || again.ID != j1.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	var active *domain.MaintenanceRunActiveError
	if _, err := f.svc.Run(f.ctx, f.user, p, "k2"); !errors.As(err, &active) || active.JobID != j1.ID {
		t.Fatalf("overlap: %v", err)
	}
	other := f.create(t, "env-2", rule)
	if _, err := f.svc.Run(f.ctx, f.user, other, "k1"); !errors.Is(err, domain.ErrJobIdempotencyConflict) {
		t.Fatalf("key reused for another policy: %v", err)
	}
	// The same key of another user is another run.
	if _, err := f.svc.Run(f.ctx, authz.Principal{Kind: authz.KindUser, UserID: "u-2"}, other, "k1"); err != nil {
		t.Fatalf("another user's key: %v", err)
	}
}

// TestPolicySourceRevalidates: when due and at dispatch, a disabled,
// emptied, deleted or archived policy is rejected; a valid one yields one
// prune.run request.
func TestPolicySourceRevalidates(t *testing.T) {
	f := newFixture(t)
	src := f.svc.PolicySource()
	rule := domain.MaintenanceRule{Category: domain.PruneDanglingImages, Enabled: true, MinAge: time.Hour}
	p := f.create(t, "env-1", rule)
	class := func(err error) string {
		var rej *scheduler.Rejection
		if errors.As(err, &rej) {
			return rej.Class
		}
		return "error: " + fmt.Sprint(err)
	}
	if c := class(src.Validate(f.ctx, p.ID)); c != scheduler.RejectPolicyDisabled {
		t.Fatalf("disabled: %s", c)
	}
	on := true
	_, p, err := f.svc.Update(f.ctx, p.ID, p.Revision, domain.MaintenancePolicyPatch{ScheduleEnabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Validate(f.ctx, p.ID); err != nil {
		t.Fatalf("valid: %v", err)
	}
	reqs, err := src.Jobs(f.ctx, scheduler.Due{PolicyID: p.ID})
	if err != nil || len(reqs) != 1 || reqs[0].Kind != jobspec.PruneRun || reqs[0].EnvironmentID != "env-1" ||
		reqs[0].Targets[0].ID != p.ID || reqs[0].PolicyID != p.ID {
		t.Fatalf("jobs %+v %v", reqs, err)
	}
	off := rule
	off.Enabled = false
	_, p, err = f.svc.Update(f.ctx, p.ID, p.Revision, domain.MaintenancePolicyPatch{Rules: []domain.MaintenanceRule{off}})
	if err != nil {
		t.Fatal(err)
	}
	if c := class(src.Validate(f.ctx, p.ID)); c != RejectNoRules {
		t.Fatalf("no rules: %s", c)
	}
	if err := f.svc.Delete(f.ctx, p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	if c := class(src.Validate(f.ctx, p.ID)); c != scheduler.RejectPolicyNotFound {
		t.Fatalf("deleted: %s", c)
	}
	// An environment archived after the policy was created.
	p2 := f.create(t, "env-2", rule)
	_, p2, _ = f.svc.Update(f.ctx, p2.ID, p2.Revision, domain.MaintenancePolicyPatch{ScheduleEnabled: &on})
	if _, err := f.db.NewUpdate().Table("environments").Set("status = ?", string(domain.EnvironmentArchived)).Where("id = ?", "env-2").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if c := class(src.Validate(f.ctx, p2.ID)); c != RejectEnvironmentArchived {
		t.Fatalf("archived: %s", c)
	}
}

// TestFinishHookRecordsTheLatestRun: the summary of the latest run is kept
// on the policy; malformed output is tolerated.
func TestFinishHookRecordsTheLatestRun(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "env-1")
	done := f.clk.Now()
	out, _ := json.Marshal(protocol.PruneRunOutput{Removed: 3, Skipped: 1, Failed: 1, Deferred: 2, BytesReclaimed: 99})
	j := domain.Job{ID: "job-1", Kind: jobspec.PruneRun, PolicyID: p.ID, State: domain.JobPartial, Origin: domain.OriginScheduled,
		FinishedAt: &done, ResultOutput: out}
	if err := f.svc.onRunFinished(f.ctx, f.db, j); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(f.ctx, p.ID)
	if r := got.LastRun; r == nil || r.JobID != "job-1" || r.State != domain.JobPartial || r.Removed != 3 || r.Skipped != 1 || r.Failed != 1 ||
		r.Deferred != 2 || r.BytesReclaimed != 99 || r.Origin != domain.OriginScheduled || !r.FinishedAt.Equal(done) {
		t.Fatalf("last run %+v", got.LastRun)
	}
	if got.Revision != p.Revision {
		t.Fatal("recording a run changed the policy revision")
	}
	j.ID, j.ResultOutput, j.State = "job-2", []byte("{not json"), domain.JobCancelled
	if err := f.svc.onRunFinished(f.ctx, f.db, j); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.Get(f.ctx, p.ID)
	if r := got.LastRun; r.JobID != "job-2" || r.State != domain.JobCancelled || r.Removed != 0 {
		t.Fatalf("malformed output: %+v", r)
	}
	// Jobs without a policy (or of a deleted one) are ignored.
	if err := f.svc.onRunFinished(f.ctx, f.db, domain.Job{ID: "x", Kind: jobspec.PruneRun, State: domain.JobSucceeded}); err != nil {
		t.Fatal(err)
	}
}

// TestPreviewMapsAgentErrors: an offline environment and agent failures
// become stable Docker error codes.
func TestPreviewMapsAgentErrors(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "env-1")
	f.req.err = jobs.ErrAgentOffline
	var de *domain.DockerError
	if _, err := f.svc.Preview(f.ctx, p, nil, true); !errors.As(err, &de) || de.Code != domain.DockerEnvironmentOffline {
		t.Fatalf("offline: %v", err)
	}
	f.req.err = nil
	if _, err := f.svc.Preview(f.ctx, p, nil, true); err != nil {
		t.Fatal(err)
	}
	if in := f.req.inputs[len(f.req.inputs)-1]; len(in.Rules) != 7 {
		t.Fatalf("includeDisabled previews every rule: %+v", in.Rules)
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
