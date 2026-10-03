package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/maintenance"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// fakeMaintenance covers env-1 and env-2 (whose agent is offline).
type fakeMaintenance struct {
	mu      sync.Mutex
	setup   domain.MaintenanceSetup
	patches []domain.MaintenanceSetupPatch
	runs    map[string]domain.Job // idempotency key -> job
}

func newFakeMaintenance() *fakeMaintenance {
	rules := domain.SuggestedMaintenanceRules()
	rules[0].Enabled = true
	return &fakeMaintenance{runs: map[string]domain.Job{}, setup: domain.MaintenanceSetup{ID: "pol-a", Cron: "0 3 * * 0", TimeZone: "UTC",
		Rules: rules, ExcludeEnvironments: []string{"env-3"}, Revision: 1}}
}

func (f *fakeMaintenance) Setup(context.Context) (domain.MaintenanceSetup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setup, nil
}

func (f *fakeMaintenance) UpdateSetup(_ context.Context, rev int64, p domain.MaintenanceSetupPatch) (domain.MaintenanceSetup, domain.MaintenanceSetup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.setup
	if rev != before.Revision {
		return before, before, domain.ErrRevisionMismatch
	}
	f.patches = append(f.patches, p)
	after := before
	if p.Enabled != nil {
		after.Enabled = *p.Enabled
	}
	if p.ExcludeEnvironments != nil {
		after.ExcludeEnvironments = *p.ExcludeEnvironments
	}
	after.Rules = domain.CompleteRules(p.Rules, before.Rules)
	after.Revision++
	f.setup = after
	return before, after, nil
}

func preview(env string) (protocol.PrunePreviewOutput, error) {
	if env == "env-2" {
		return protocol.PrunePreviewOutput{}, &domain.DockerError{Code: domain.DockerEnvironmentOffline, Message: "the environment's agent is not connected"}
	}
	return protocol.PrunePreviewOutput{Categories: []protocol.PruneCategoryPlan{{Category: domain.PruneStoppedContainers, Remove: 1, Bytes: 10,
		Items: []protocol.PruneItem{{Category: domain.PruneStoppedContainers, ID: "c1", Name: "old", Decision: protocol.PruneRemove, Bytes: 10}}}}}, nil
}

func (f *fakeMaintenance) Environments(context.Context) ([]domain.Environment, error) {
	return []domain.Environment{{ID: "env-1", Name: "Silo"}, {ID: "env-2", Name: "Rack"}}, nil
}

func (f *fakeMaintenance) Preview(context.Context) ([]maintenance.EnvironmentPreview, error) {
	var out []maintenance.EnvironmentPreview
	for _, env := range []string{"env-1", "env-2"} {
		p, err := preview(env)
		out = append(out, maintenance.EnvironmentPreview{EnvironmentID: env, Preview: p, Err: err})
	}
	return out, nil
}

func (f *fakeMaintenance) Run(_ context.Context, p authz.Principal, key string) ([]domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == "busy" {
		return nil, &domain.MaintenanceRunActiveError{JobID: "job-busy"}
	}
	var out []domain.Job
	for _, env := range []string{"env-1", "env-2"} {
		j := domain.Job{ID: "job-" + key + "-" + env, Kind: jobspec.PruneRun, PolicyID: f.setup.ID, EnvironmentID: env, State: domain.JobQueued,
			Origin: domain.OriginManual, InitiatorUserID: p.UserID, CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
			Targets: []domain.JobTarget{{Type: domain.TargetMaintenancePolicy, ID: f.setup.ID}}}
		out = append(out, j)
	}
	return out, nil
}

func (f *fakeMaintenance) PreviewManual(_ context.Context, env string, rules []domain.MaintenanceRule) (protocol.PrunePreviewOutput, error) {
	if len(domain.EnabledRules(rules)) == 0 {
		return protocol.PrunePreviewOutput{}, &domain.FieldError{Field: "rules", Message: "turn on at least one rule"}
	}
	return preview(env)
}

func (f *fakeMaintenance) RunManual(_ context.Context, p authz.Principal, env string, rules []domain.MaintenanceRule, key string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.runs["manual/"+key]; ok && key != "" {
		return j, nil
	}
	j := domain.Job{ID: "job-manual-" + key, Kind: jobspec.PruneRun, EnvironmentID: env, State: domain.JobQueued,
		Origin: domain.OriginManual, InitiatorUserID: p.UserID, CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC()}
	f.runs["manual/"+key] = j
	return j, nil
}

func (f *fakeMaintenance) ScheduleStatus(context.Context, string, int) (domain.Schedule, []domain.ScheduleRun, bool, error) {
	return domain.Schedule{}, nil, false, nil
}

func maintenanceHandler(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeMaintenance) {
	t.Helper()
	mux := http.NewServeMux()
	fake := newFakeMaintenance()
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Maintenance: fake})
	return authztest.Authenticate(withTestContext(t, mux, "")), fake
}

func maintenanceRoutes(t *testing.T) []authztest.Call {
	calls := authztest.Routes(t, nil, "/api/v1/maintenance-settings")
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": "*", "Idempotency-Key": "k-" + calls[i].OperationID}
		switch calls[i].OperationID {
		case "create-maintenance-run":
			calls[i].Body = map[string]any{"confirm": true}
		case "update-maintenance-settings":
			calls[i].Body = map[string]any{"enabled": true}
		}
	}
	if len(calls) != 4 {
		t.Fatalf("maintenance routes: %+v", calls)
	}
	return calls
}

// TestMaintenanceRoutesAuthorization: every route of the settings needs
// its capability on all environments; a grant in one environment (the
// one-off prunes' scope) is not enough.
func TestMaintenanceRoutesAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("mia", "readers").Group("readers", "allow maintenance_policy.read @all").
		Member("pete", "previewers").Group("previewers", "allow maintenance.preview @all").
		Member("rex", "runners").Group("runners", "allow maintenance.run @all").
		Member("max", "managers").Group("managers", "allow maintenance_policy.manage @all").
		Member("eve", "one").Group("one", "allow maintenance.run @env:env-1", "allow maintenance.preview @env:env-1").
		Member("dan", "most").Group("most", "allow maintenance_policy.read @all", "allow maintenance.run @all",
		"allow maintenance.preview @all", "deny maintenance.run @env:env-2", "deny maintenance.preview @env:env-2").
		Member("rita", "nobody")
	h, _ := maintenanceHandler(t, pol)
	calls := maintenanceRoutes(t)
	authztest.AssertOnly(t, h, "olga", calls, nil)
	for user, caps := range map[string][]string{
		"mia":  {"maintenance_policy.read"},
		"pete": {"maintenance.preview"},
		"rex":  {"maintenance.run"},
		"max":  {"maintenance_policy.manage"},
	} {
		allowed, denied := authztest.Split(calls, caps...)
		authztest.AssertOnly(t, h, user, allowed, denied)
	}
	authztest.AssertOnly(t, h, "eve", nil, calls)
	authztest.AssertOnly(t, h, "rita", nil, calls)
	// Denied in one covered environment: no preview (it would show its
	// objects) and no run there.
	allowed, denied := authztest.Split(calls, "maintenance_policy.read")
	authztest.AssertOnly(t, h, "dan", allowed, denied)
	if r := authztest.Do(t, h, "dan", authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-settings/previews"}); r.Status != http.StatusForbidden ||
		!strings.Contains(string(r.Body), "Rack") {
		t.Errorf("preview denied in one environment: %d %s", r.Status, r.Body)
	}

	// The caller's actions say what they may do.
	actions := func(user string) []string {
		r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-settings"})
		var s MaintenanceSettings
		if r.Status != http.StatusOK || json.Unmarshal(r.Body, &s) != nil {
			t.Fatalf("get as %s: %d %s", user, r.Status, r.Body)
		}
		slices.Sort(s.Actions)
		return s.Actions
	}
	if got := actions("mia"); !slices.Equal(got, []string{"maintenance_policy.read"}) {
		t.Errorf("reader's actions %v", got)
	}
	if got := actions("olga"); !slices.Contains(got, "maintenance.run") || !slices.Contains(got, "maintenance_policy.manage") {
		t.Errorf("owner's actions %v", got)
	}
}

// TestMaintenanceSettingsRoutes: the settings carry every category and the
// suggestions; a change needs If-Match and is passed on; minAgeHours is
// required; a run needs confirmation and starts a job per environment; a
// preview reports each environment, also one that cannot answer.
func TestMaintenanceSettingsRoutes(t *testing.T) {
	h, fake := maintenanceHandler(t, authztest.New().Owner("olga"))
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-settings"})
	var s MaintenanceSettings
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &s) != nil || s.ID != "pol-a" || len(s.Rules) != 7 || len(s.SuggestedRules) != 7 ||
		len(s.Categories) != 7 || !slices.Equal(s.ExcludeEnvironments, []string{"env-3"}) || s.Schedule.Cron != "0 3 * * 0" ||
		r.Header.Get("ETag") != `"1"` {
		t.Fatalf("settings: %d %s", r.Status, r.Body)
	}
	for _, c := range s.Categories {
		if len(c.Limitations) == 0 || c.Description == "" {
			t.Errorf("category %+v", c)
		}
	}

	patch := func(etag string, body any) authztest.Response {
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/maintenance-settings",
			Headers: map[string]string{"If-Match": etag}, Body: body})
	}
	if r := patch(`"7"`, map[string]any{"enabled": true}); r.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %s", r.Status, r.Body)
	}
	// minAgeHours is required: omitting it never means "any age".
	if r := patch(`"1"`, map[string]any{"rules": []map[string]any{{"category": "unused_images", "enabled": true}}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("rule without minAgeHours: %d %s", r.Status, r.Body)
	}
	r = patch(`"1"`, map[string]any{"enabled": true, "excludeEnvironments": []string{}, "schedule": map[string]any{"cron": "0 4 * * *"}})
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &s) != nil || !s.Enabled || len(s.ExcludeEnvironments) != 0 || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	if p := fake.patches[len(fake.patches)-1]; p.Cron == nil || *p.Cron != "0 4 * * *" || p.TimeZone != nil || p.ExcludeEnvironments == nil {
		t.Fatalf("patch passed on %+v", p)
	}

	run := func(key string, body any) authztest.Response {
		return authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-settings/runs", Body: body,
			Headers: map[string]string{"Idempotency-Key": key}})
	}
	if r := run("k0", map[string]any{"confirm": false}); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodePruneConfirmationRequired) {
		t.Fatalf("unconfirmed run: %d %s", r.Status, r.Body)
	}
	var jobs struct{ Jobs []Job }
	if r := run("k1", map[string]any{"confirm": true}); r.Status != http.StatusOK || json.Unmarshal(r.Body, &jobs) != nil || len(jobs.Jobs) != 2 {
		t.Fatalf("run: %d %s", r.Status, r.Body)
	}
	if r := run("busy", map[string]any{"confirm": true}); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodeMaintenanceRunActive) ||
		!strings.Contains(string(r.Body), "job-busy") {
		t.Fatalf("overlap: %d %s", r.Status, r.Body)
	}

	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-settings/previews"})
	var pv maintenancePreviewOutput
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &pv.Body) != nil || len(pv.Body.Items) != 2 {
		t.Fatalf("preview: %d %s", r.Status, r.Body)
	}
	one, two := pv.Body.Items[0], pv.Body.Items[1]
	if one.EnvironmentID != "env-1" || one.Preview == nil || one.Preview.Remove != 1 || one.Preview.Bytes != 10 || len(one.Preview.Notes) == 0 ||
		one.Preview.Categories[0].Items[0].Name != "old" || one.ErrorClass != "" {
		t.Fatalf("env-1 preview: %+v", one)
	}
	if two.EnvironmentID != "env-2" || two.Preview != nil || two.ErrorClass != CodeEnvironmentOffline || two.ErrorMessage == "" {
		t.Fatalf("offline preview: %+v", two)
	}
}

// TestManualPruneRoutes: one-off prunes of an environment need
// maintenance.preview / maintenance.run in the environment, a visible
// environment, confirmation and at least one enabled rule.
func TestManualPruneRoutes(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("pete", "previewers").Group("previewers", "allow maintenance.preview @env:env-1").
		Member("rex", "runners").Group("runners", "allow maintenance.run @env:env-1", "allow environment.read @env:env-1").
		Member("rita", "nobody")
	h, _ := maintenanceHandler(t, pol)
	rules := []map[string]any{{"category": "dangling_images", "enabled": true, "minAgeHours": 24}}
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/prune-previews", Body: map[string]any{"rules": rules}}
	run := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/prunes",
		Body: map[string]any{"rules": rules, "confirm": true}, Headers: map[string]string{"Idempotency-Key": "k1"}}

	for user, want := range map[string][2]int{
		"olga": {http.StatusOK, http.StatusAccepted},
		"pete": {http.StatusOK, http.StatusForbidden},
		"rex":  {http.StatusForbidden, http.StatusAccepted},
		"rita": {http.StatusNotFound, http.StatusNotFound},
	} {
		if r := authztest.Do(t, h, user, preview); r.Status != want[0] {
			t.Errorf("preview as %s: %d %s", user, r.Status, r.Body)
		}
		if r := authztest.Do(t, h, user, run); r.Status != want[1] {
			t.Errorf("run as %s: %d %s", user, r.Status, r.Body)
		}
	}

	r := authztest.Do(t, h, "olga", preview)
	var pv PrunePreview
	if json.Unmarshal(r.Body, &pv) != nil || pv.Remove != 1 || pv.EnvironmentID != "env-1" {
		t.Fatalf("preview body: %s", r.Body)
	}
	unconfirmed := run
	unconfirmed.Body = map[string]any{"rules": rules}
	if r := authztest.Do(t, h, "olga", unconfirmed); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodePruneConfirmationRequired) {
		t.Fatalf("unconfirmed run: %d %s", r.Status, r.Body)
	}
	empty := preview
	empty.Body = map[string]any{"rules": []map[string]any{{"category": "dangling_images", "enabled": false, "minAgeHours": 24}}}
	if r := authztest.Do(t, h, "olga", empty); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("no enabled rule: %d %s", r.Status, r.Body)
	}
	var first, again Job
	_ = json.Unmarshal(authztest.Do(t, h, "olga", run).Body, &first)
	_ = json.Unmarshal(authztest.Do(t, h, "olga", run).Body, &again)
	if first.ID == "" || first.ID != again.ID {
		t.Fatalf("repeated key started %q and %q", first.ID, again.ID)
	}
}
