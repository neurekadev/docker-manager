package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeUpdateSettings is the update service with its setup: it covers
// env-1 (Silo) and env-2 (Rack).
type fakeUpdateSettings struct {
	*fakeUpdates
	smu     sync.Mutex
	setup   domain.UpdateSetup
	changes []updates.SetupChange
}

func (f *fakeUpdateSettings) Setup(context.Context) (domain.UpdateSetup, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	return f.setup, nil
}

// permitCovered checks the environments the setup covers with permit.
func permitCovered(permit updates.Permit) error {
	for _, env := range []domain.Environment{{ID: "env-1", Name: "Silo"}, {ID: "env-2", Name: "Rack"}} {
		if permit != nil {
			if err := permit(env); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeUpdateSettings) UpdateSetup(_ context.Context, rev int64, c updates.SetupChange) (domain.UpdateSetup, domain.UpdateSetup, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	before := f.setup
	if rev != before.Revision {
		return before, before, domain.ErrRevisionMismatch
	}
	f.changes = append(f.changes, c)
	after := before
	if c.ExcludeEnvironments != nil {
		after.ExcludeEnvironments = *c.ExcludeEnvironments
	}
	if c.Check != nil {
		after.Check = *c.Check
	}
	if c.ClearWindow {
		after.Window = nil
	}
	if c.Window != nil {
		after.Window = c.Window
	}
	after.Revision++
	f.setup = after
	return before, after, nil
}

func (f *fakeUpdateSettings) Targets(context.Context) ([]updates.ManagedTarget, error) {
	return []updates.ManagedTarget{
		{Policy: domain.UpdatePolicy{ID: "pol-1", ParentID: "set-1", EnvironmentID: "env-1", TargetType: domain.UpdateTargetStack, TargetID: "st-1"}},
		{Policy: domain.UpdatePolicy{ID: "pol-2", ParentID: "set-1", EnvironmentID: "env-2", TargetType: domain.UpdateTargetContainer, TargetID: "api",
			Inactive: true}, InactiveReason: domain.UpdateTargetExcluded},
	}, nil
}

func (f *fakeUpdateSettings) CheckSetup(_ context.Context, _ authz.Principal, _ string, permit updates.Permit) ([]domain.Job, error) {
	if err := permitCovered(permit); err != nil {
		return nil, err
	}
	return []domain.Job{{ID: "job-c1", Kind: "update.check", State: domain.JobQueued, PolicyID: "set-1"}}, nil
}

func (f *fakeUpdateSettings) PreviewSetup(_ context.Context, permit updates.Permit) (updates.SetupPreview, error) {
	if err := permitCovered(permit); err != nil {
		return updates.SetupPreview{}, err
	}
	return updates.SetupPreview{Fingerprint: "fp-all", Targets: []updates.TargetPreview{{Policy: domain.UpdatePolicy{ID: "pol-1",
		EnvironmentID: "env-1", TargetType: domain.UpdateTargetStack, TargetID: "st-1"}}}}, nil
}

func (f *fakeUpdateSettings) RunSetup(_ context.Context, _ authz.Principal, fingerprint, _ string, permit updates.Permit) ([]domain.Job, error) {
	if err := permitCovered(permit); err != nil {
		return nil, err
	}
	if fingerprint != "fp-all" {
		return nil, &domain.UpdateError{Code: domain.UpdateErrPreviewStale, Message: "the update plan changed; preview again"}
	}
	return []domain.Job{{ID: "job-r1", Kind: "update.run", State: domain.JobQueued, PolicyID: "set-1"}}, nil
}

func updateSettingsAPIFor(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeUpdateSettings) {
	t.Helper()
	svc := &fakeUpdateSettings{fakeUpdates: &fakeUpdates{policies: map[string]domain.UpdatePolicy{}}, setup: domain.UpdateSetup{ID: "set-1",
		ExcludeEnvironments: []string{}, ExcludeStacks: []string{"st-9"}, ExcludeContainers: []string{"env-2/api"},
		Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}, Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"},
		Revision: 1}}
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: newFakeStacks(), Updates: svc, Agents: newFakeAgents(), Authorizer: pol, Clock: testutil.FakeClock(),
		Idempotency: &memIdempotency{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), svc
}

func updateSettingsRoutes(t *testing.T) []authztest.Call {
	calls := authztest.Routes(t, nil, "/api/v1/update-settings")
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": "*", "Idempotency-Key": "k-" + calls[i].OperationID}
		switch calls[i].OperationID {
		case "create-update-run":
			calls[i].Body = map[string]any{"fingerprint": "fp-all"}
		case "update-update-settings":
			calls[i].Body = map[string]any{"clearWindow": true}
		}
	}
	if len(calls) != 6 {
		t.Fatalf("update settings routes: %+v", calls)
	}
	return calls
}

// The update settings need their capability on all environments; a grant
// in one environment (the target records' scope) is not enough, and a
// rule denying checks or runs in a covered environment refuses checks,
// previews and runs of everything.
func TestUpdateSettingsAuthorization(t *testing.T) {
	pol := authztest.New().Owner("olga").
		Member("mia", "readers").Group("readers", "allow update_policy.read @all").
		Member("max", "managers").Group("managers", "allow update_policy.manage @all").
		Member("cho", "checkers").Group("checkers", "allow update.check @all").
		Member("rex", "runners").Group("runners", "allow update.run @all").
		Member("eve", "one").Group("one", "allow update_policy.read @env:env-1", "allow update.check @env:env-1", "allow update.run @env:env-1").
		Member("dan", "most").Group("most", "allow update.check @all", "deny update.check @env:env-2").
		Member("rita", "nobody")
	h, _ := updateSettingsAPIFor(t, pol)
	calls := updateSettingsRoutes(t)
	authztest.AssertOnly(t, h, "olga", calls, nil)
	for user, caps := range map[string][]string{
		"mia": {"update_policy.read"},
		"max": {"update_policy.manage"},
		"cho": {"update.check"},
		"rex": {"update.run"},
	} {
		allowed, denied := authztest.Split(calls, caps...)
		authztest.AssertOnly(t, h, user, allowed, denied)
	}
	authztest.AssertOnly(t, h, "eve", nil, calls)
	authztest.AssertOnly(t, h, "dan", nil, calls)
	if r := authztest.Do(t, h, "dan", authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-settings/previews"}); r.Status != http.StatusForbidden ||
		!strings.Contains(string(r.Body), "Rack") {
		t.Errorf("preview denied in one environment: %d %s", r.Status, r.Body)
	}
	authztest.AssertOnly(t, h, "rita", nil, calls)
}

func TestUpdateSettingsRoutes(t *testing.T) {
	h, svc := updateSettingsAPIFor(t, authztest.New().Owner("olga"))
	do := func(c authztest.Call) authztest.Response { return authztest.Do(t, h, "olga", c) }

	r := do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-settings"})
	var s UpdateSettings
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"1"` || json.Unmarshal(r.Body, &s) != nil || s.ID != "set-1" ||
		!slices.Equal(s.ExcludeStacks, []string{"st-9"}) || !slices.Equal(s.ExcludeContainers, []string{"env-2/api"}) ||
		s.ExcludeEnvironments == nil || s.CheckSchedule.Cron != "0 3 * * *" || s.CheckSchedule.Enabled || !slices.Contains(s.Actions, "update.run") {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}

	patch := func(etag string, body any) authztest.Response {
		return do(authztest.Call{Method: http.MethodPatch, Path: "/api/v1/update-settings", Headers: map[string]string{"If-Match": etag}, Body: body})
	}
	if r := patch(`"9"`, map[string]any{"clearWindow": true}); r.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %s", r.Status, r.Body)
	}
	r = patch(`"1"`, map[string]any{"excludeEnvironments": []string{"env-2"}, "checkSchedule": map[string]any{"cron": "0 5 * * *",
		"timeZone": "UTC", "enabled": true}, "window": map[string]any{"start": "01:00", "end": "02:00"}})
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &s) != nil || !slices.Equal(s.ExcludeEnvironments, []string{"env-2"}) ||
		!s.CheckSchedule.Enabled || s.Window == nil || r.Header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	if c := svc.changes[len(svc.changes)-1]; c.ExcludeStacks != nil || c.Run != nil || c.Window == nil || c.ClearWindow {
		t.Fatalf("change passed on %+v", c)
	}
	var cleared UpdateSettings
	if r := patch(`"2"`, map[string]any{"clearWindow": true}); r.Status != http.StatusOK || json.Unmarshal(r.Body, &cleared) != nil ||
		cleared.Window != nil {
		t.Fatalf("clear the window: %d %s", r.Status, r.Body)
	}

	r = do(authztest.Call{Method: http.MethodGet, Path: "/api/v1/update-settings/targets"})
	var targets updateTargetsOutput
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &targets.Body) != nil || len(targets.Body.Items) != 2 ||
		targets.Body.Items[1].InactiveReason != "excluded" || targets.Body.Items[0].CandidateSummary.Quarantined != 1 {
		t.Fatalf("targets: %d %s", r.Status, r.Body)
	}
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-settings/checks", Headers: map[string]string{"Idempotency-Key": "c1"}})
	var jobs updateSettingsJobsOutput
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &jobs.Body) != nil || len(jobs.Body.Jobs) != 1 {
		t.Fatalf("check: %d %s", r.Status, r.Body)
	}
	r = do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-settings/previews"})
	var pv updateSettingsPreviewOutput
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &pv.Body) != nil || pv.Body.Fingerprint != "fp-all" || len(pv.Body.Targets) != 1 {
		t.Fatalf("preview: %d %s", r.Status, r.Body)
	}
	run := func(fp string) authztest.Response {
		return do(authztest.Call{Method: http.MethodPost, Path: "/api/v1/update-settings/runs", Body: map[string]any{"fingerprint": fp},
			Headers: map[string]string{"Idempotency-Key": "r-" + fp}})
	}
	if r := run("old"); r.Status != http.StatusConflict || !strings.Contains(string(r.Body), CodeUpdatePreviewStale) {
		t.Fatalf("stale run: %d %s", r.Status, r.Body)
	}
	if r := run("fp-all"); r.Status != http.StatusOK {
		t.Fatalf("run: %d %s", r.Status, r.Body)
	}
}
