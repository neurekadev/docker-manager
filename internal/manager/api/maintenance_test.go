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

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// fakeMaintenance holds two policies: pol-a in env-1 and pol-b in env-2
// (whose agent is offline).
type fakeMaintenance struct {
	mu   sync.Mutex
	pols map[string]domain.MaintenancePolicy
	runs map[string]domain.Job // idempotency key -> job
}

func newFakeMaintenance() *fakeMaintenance {
	rules := domain.SuggestedMaintenanceRules()
	rules[0].Enabled = true
	return &fakeMaintenance{runs: map[string]domain.Job{}, pols: map[string]domain.MaintenancePolicy{
		"pol-a": {ID: "pol-a", EnvironmentID: "env-1", Name: "A", Description: "secret-ish description", Cron: "0 3 * * 0", TimeZone: "UTC",
			Rules: rules, Revision: 1},
		"pol-b": {ID: "pol-b", EnvironmentID: "env-2", Name: "B", Cron: "0 3 * * 0", TimeZone: "UTC", Rules: rules, Revision: 1},
	}}
}

func (f *fakeMaintenance) Defaults(context.Context) (domain.MaintenanceDefaults, error) {
	return domain.MaintenanceDefaults{Rules: domain.SuggestedMaintenanceRules(), Revision: 1}, nil
}

func (f *fakeMaintenance) UpdateDefaults(_ context.Context, rev int64, rules []domain.MaintenanceRule) (domain.MaintenanceDefaults, domain.MaintenanceDefaults, error) {
	before := domain.MaintenanceDefaults{Rules: domain.SuggestedMaintenanceRules(), Revision: 1}
	return before, domain.MaintenanceDefaults{Rules: domain.CompleteRules(rules, before.Rules), Revision: rev + 1}, nil
}

func (f *fakeMaintenance) Create(_ context.Context, c domain.MaintenancePolicyCreate) (domain.MaintenancePolicy, error) {
	return domain.MaintenancePolicy{ID: "pol-new", EnvironmentID: c.EnvironmentID, Name: c.Name, Rules: domain.SuggestedMaintenanceRules(), Revision: 1}, nil
}

func (f *fakeMaintenance) Get(_ context.Context, id string) (domain.MaintenancePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pols[id]
	if !ok {
		return p, domain.ErrMaintenancePolicyNotFound
	}
	return p, nil
}

func (f *fakeMaintenance) List(_ context.Context, env, after string, limit int) ([]domain.MaintenancePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.MaintenancePolicy
	for _, id := range []string{"pol-a", "pol-b"} {
		p := f.pols[id]
		if id > after && (env == "" || p.EnvironmentID == env) {
			out = append(out, p)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeMaintenance) Update(ctx context.Context, id string, rev int64, _ domain.MaintenancePolicyPatch) (domain.MaintenancePolicy, domain.MaintenancePolicy, error) {
	p, err := f.Get(ctx, id)
	after := p
	after.Revision++
	return p, after, err
}

func (f *fakeMaintenance) Delete(context.Context, string, int64) error { return nil }

func (f *fakeMaintenance) Preview(_ context.Context, pol domain.MaintenancePolicy, _ []domain.MaintenanceRule, _ bool) (protocol.PrunePreviewOutput, error) {
	if pol.EnvironmentID == "env-2" {
		return protocol.PrunePreviewOutput{}, &domain.DockerError{Code: domain.DockerEnvironmentOffline, Message: "offline"}
	}
	return protocol.PrunePreviewOutput{Categories: []protocol.PruneCategoryPlan{{Category: domain.PruneStoppedContainers, Remove: 1, Bytes: 10,
		Items: []protocol.PruneItem{{Category: domain.PruneStoppedContainers, ID: "c1", Name: "old", Decision: protocol.PruneRemove, Bytes: 10}}}}}, nil
}

func (f *fakeMaintenance) Run(_ context.Context, p authz.Principal, pol domain.MaintenancePolicy, key string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.runs[key]; ok && key != "" {
		return j, nil
	}
	if key == "busy" {
		return domain.Job{}, &domain.MaintenanceRunActiveError{JobID: "job-busy"}
	}
	j := domain.Job{ID: "job-" + key, Kind: jobspec.PruneRun, PolicyID: pol.ID, EnvironmentID: pol.EnvironmentID, State: domain.JobQueued,
		Origin: domain.OriginManual, InitiatorUserID: p.UserID, CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
		Targets: []domain.JobTarget{{Type: domain.TargetMaintenancePolicy, ID: pol.ID}}}
	f.runs[key] = j
	return j, nil
}

func (f *fakeMaintenance) PreviewManual(ctx context.Context, env string, rules []domain.MaintenanceRule) (protocol.PrunePreviewOutput, error) {
	if len(domain.EnabledRules(rules)) == 0 {
		return protocol.PrunePreviewOutput{}, &domain.FieldError{Field: "rules", Message: "turn on at least one rule"}
	}
	return f.Preview(ctx, domain.MaintenancePolicy{EnvironmentID: env}, rules, false)
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

func maintenanceHandler(t *testing.T, pol *authztest.Policy) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, Idempotency: &memIdempotency{}, Builds: emptyBuilds{}, Maintenance: newFakeMaintenance()})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

func maintenanceRoutes(t *testing.T) []authztest.Call {
	calls := authztest.Routes(t, map[string]string{"policyId": "pol-a"}, "/api/v1/maintenance-policies", "/api/v1/maintenance-defaults")
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": "*"}
		switch calls[i].OperationID {
		case "create-maintenance-policy":
			calls[i].Body = map[string]any{"environmentId": "env-1", "name": "x"}
		case "create-maintenance-policy-run", "run-maintenance-environments":
			calls[i].Body = map[string]any{"confirm": true}
		case "update-maintenance-defaults":
			calls[i].Body = map[string]any{"rules": []map[string]any{{"category": "build_cache", "enabled": false, "minAgeHours": 24}}}
		}
	}
	if len(calls) != 11 {
		t.Fatalf("maintenance routes: %+v", calls)
	}
	return calls
}

// TestMaintenanceRoutesAuthorization: every route needs its capability on
// the policy (or in its environment); settings capabilities govern the
// defaults; other grants on a policy show only its minimal view.
func TestMaintenanceRoutesAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("mia", "readers").Group("readers", "allow maintenance_policy.read @env:env-1").
		Member("pete", "previewers").Group("previewers", "allow maintenance.preview @maintenance_policy:pol-a").
		Member("rex", "runners").Group("runners", "allow maintenance.run @env:env-1").
		Member("max", "managers").Group("managers", "allow maintenance_policy.manage @env:env-1").
		Member("sue", "settings").Group("settings", "allow settings.read @all", "allow settings.manage @all").
		Member("rita", "nobody")
	h := maintenanceHandler(t, pol)
	calls := maintenanceRoutes(t)
	authztest.AssertOnly(t, h, "olga", calls, nil)
	for user, caps := range map[string][]string{
		"mia":  {"maintenance_policy.read"},
		"pete": {"maintenance.preview"},
		"rex":  {"maintenance.run"},
		"max":  {"maintenance_policy.manage"},
		"sue":  {"settings.read", "settings.manage"},
	} {
		allowed, denied := authztest.Split(calls, caps...)
		if user != "sue" && user != "mia" {
			allowed, denied = authztest.Discoverable(allowed, denied, "list-maintenance-policies", "get-maintenance-policy")
		}
		authztest.AssertOnly(t, h, user, allowed, denied)
	}
	authztest.AssertOnly(t, h, "rita", nil, calls)

	// Lists are filtered per policy.
	list := func(user string) []string {
		r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-policies"})
		var page Page[MaintenancePolicy]
		if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil {
			t.Fatalf("list as %s: %d %s", user, r.Status, r.Body)
		}
		var ids []string
		for _, p := range page.Items {
			ids = append(ids, p.ID+":"+p.View)
		}
		return ids
	}
	if got := list("olga"); !slices.Equal(got, []string{"pol-a:full", "pol-b:full"}) {
		t.Errorf("owner lists %v", got)
	}
	if got := list("mia"); !slices.Equal(got, []string{"pol-a:full"}) {
		t.Errorf("reader lists %v", got)
	}
	if got := list("pete"); !slices.Equal(got, []string{"pol-a:minimal"}) {
		t.Errorf("previewer lists %v", got)
	}
	// The minimal view shows neither rules nor schedule nor description.
	r := authztest.Do(t, h, "pete", authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-policies/pol-a"})
	authztest.AssertAbsent(t, "minimal policy", r.Body, "secret-ish", `"rules"`, `"schedule"`)
	if r := authztest.Do(t, h, "pete", authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-policies/pol-b"}); r.Status != http.StatusNotFound {
		t.Errorf("other environment's policy: %d", r.Status)
	}
}

// TestMaintenanceRunAndPreviewRoutes: confirmation, the background
// preference as presentation only, overlap and offline mapping.
func TestMaintenanceRunAndPreviewRoutes(t *testing.T) {
	h := maintenanceHandler(t, authztest.New().Owner("olga"))
	run := func(key string, body any) authztest.Response {
		c := authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-policies/pol-a/runs", Body: body}
		if key != "" {
			c.Headers = map[string]string{"Idempotency-Key": key}
		}
		return authztest.Do(t, h, "olga", c)
	}
	for _, body := range []any{nil, map[string]any{}, map[string]any{"background": true, "confirm": false}} {
		if r := run("", body); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodePruneConfirmationRequired) {
			t.Fatalf("unconfirmed run %v: %d %s", body, r.Status, r.Body)
		}
	}
	jobID := func(r authztest.Response) string {
		var j Job
		if r.Status != http.StatusAccepted || json.Unmarshal(r.Body, &j) != nil || r.Header.Get("Location") != "/api/v1/jobs/"+j.ID {
			t.Fatalf("run: %d %s", r.Status, r.Body)
		}
		return j.ID
	}
	bg := jobID(run("k1", map[string]any{"confirm": true, "background": true}))
	fg := jobID(run("k1", map[string]any{"confirm": true, "background": false}))
	if bg != fg {
		t.Fatalf("foreground %s and background %s are different jobs", fg, bg)
	}
	if r := run("busy", map[string]any{"confirm": true}); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodeMaintenanceRunActive) ||
		!strings.Contains(string(r.Body), "job-busy") {
		t.Fatalf("overlap: %d %s", r.Status, r.Body)
	}
	r := authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-policies/pol-a/previews"})
	var pv PrunePreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &pv) != nil || pv.Remove != 1 || pv.Bytes != 10 || len(pv.Notes) == 0 ||
		pv.Categories[0].Items[0].Name != "old" {
		t.Fatalf("preview: %d %s", r.Status, r.Body)
	}
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/maintenance-policies/pol-b/previews", Body: map[string]any{}})
	if r.Status != http.StatusServiceUnavailable || !strings.Contains(string(r.Body), CodeEnvironmentOffline) {
		t.Fatalf("offline preview: %d %s", r.Status, r.Body)
	}
	// Defaults carry every category with its limitations.
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodGet, Path: "/api/v1/maintenance-defaults"})
	var d MaintenanceDefaults
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &d) != nil || len(d.Rules) != 7 || len(d.Categories) != 7 || r.Header.Get("ETag") != `"1"` {
		t.Fatalf("defaults: %d %s", r.Status, r.Body)
	}
	for _, c := range d.Categories {
		if len(c.Limitations) == 0 || c.Description == "" {
			t.Errorf("category %+v", c)
		}
	}
	// minAgeHours is required: omitting it never means "any age".
	r = authztest.Do(t, h, "olga", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/maintenance-policies/pol-a",
		Headers: map[string]string{"If-Match": `"1"`}, Body: map[string]any{"rules": []map[string]any{{"category": "unused_images", "enabled": true}}}})
	if r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("rule without minAgeHours: %d %s", r.Status, r.Body)
	}
}

// TestManualPruneRoutes: one-off prunes of an environment need
// maintenance.preview / maintenance.run in the environment (a grant on a
// policy is not enough), a visible environment, confirmation and at least
// one enabled rule.
func TestManualPruneRoutes(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("pete", "previewers").Group("previewers", "allow maintenance.preview @env:env-1").
		Member("rex", "runners").Group("runners", "allow maintenance.run @env:env-1", "allow environment.read @env:env-1").
		Member("paula", "policy").Group("policy", "allow maintenance.run @maintenance_policy:pol-a", "allow environment.read @env:env-1").
		Member("rita", "nobody")
	h := maintenanceHandler(t, pol)
	rules := []map[string]any{{"category": "dangling_images", "enabled": true, "minAgeHours": 24}}
	preview := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/prune-previews", Body: map[string]any{"rules": rules}}
	run := authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/prunes",
		Body: map[string]any{"rules": rules, "confirm": true}, Headers: map[string]string{"Idempotency-Key": "k1"}}

	for user, want := range map[string][2]int{
		"olga":  {http.StatusOK, http.StatusAccepted},
		"pete":  {http.StatusOK, http.StatusForbidden},
		"rex":   {http.StatusForbidden, http.StatusAccepted},
		"paula": {http.StatusForbidden, http.StatusForbidden},
		"rita":  {http.StatusNotFound, http.StatusNotFound},
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
	if json.Unmarshal(r.Body, &pv) != nil || pv.Remove != 1 || pv.EnvironmentID != "env-1" || pv.PolicyID != "" {
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
