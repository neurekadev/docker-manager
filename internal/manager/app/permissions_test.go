package app

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/auth"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// Authorization (#17) through the real manager: sessions, the permission
// service, the job engine and the environment/job/audit routes.

func (e *env) seedEnvironment(id, name string) {
	e.t.Helper()
	now := e.clk.Now().UTC()
	env := domain.Environment{ID: id, Name: name, EngineID: "ENGINE-" + id, InstallID: "install-" + id,
		Status: domain.EnvironmentActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertEnvironment(testutil.Context(e.t), e.m.DB(), &env); err != nil {
		e.t.Fatal(err)
	}
}

// restartJob enqueues a container restart as the owner.
func (e *env) restartJob(ownerID, envID, container string) domain.Job {
	e.t.Helper()
	j, _, err := e.m.Jobs().Enqueue(testutil.Context(e.t), jobs.Request{Kind: "container.restart",
		Principal: authz.Principal{Kind: authz.KindUser, UserID: ownerID}, EnvironmentID: envID,
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: container}}})
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

// apiRules converts rule shorthand to the API representation.
func apiRules(t *testing.T, shorthand ...string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, s := range shorthand {
		r, err := policy.ParseRule(catalog.Default(), s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"capability": r.Capability, "effect": string(r.Effect), "scope": map[string]string{
			"kind": string(r.Scope.Kind), "environmentId": r.Scope.EnvironmentID, "resourceType": r.Scope.ResourceType,
			"resourceId": r.Scope.ResourceID}})
	}
	return out
}

func (c *client) putRules(path string, shorthand ...string) response {
	c.e.t.Helper()
	cur := c.must(http.StatusOK, http.MethodGet, path, nil)
	return c.must(http.StatusOK, http.MethodPut, path, map[string]any{"rules": apiRules(c.e.t, shorthand...)}, header("If-Match", cur.header.Get("ETag")))
}

type groupBody struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MemberCount  int    `json:"memberCount"`
	RuleCount    int    `json:"ruleCount"`
	GrantsAccess bool   `json:"grantsAccess"`
}

func (c *client) groups() []groupBody {
	c.e.t.Helper()
	var out struct {
		Items []groupBody `json:"items"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/groups", nil).json(c.e.t, &out)
	return out.Items
}

func (c *client) groupNamed(name string) groupBody {
	c.e.t.Helper()
	for _, g := range c.groups() {
		if g.Name == name {
			return g
		}
	}
	c.e.t.Fatalf("no group %q", name)
	return groupBody{}
}

func (c *client) createGroup(name string) groupBody {
	c.e.t.Helper()
	var g groupBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]string{"name": name}).json(c.e.t, &g)
	return g
}

func (c *client) moveUser(userID, groupID string) response {
	c.e.t.Helper()
	cur := c.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+userID, nil)
	return c.do(http.MethodPatch, "/api/v1/users/"+userID, map[string]any{"groupIds": []string{groupID}}, header("If-Match", cur.header.Get("ETag")))
}

type envItem struct {
	ID       string   `json:"id"`
	View     string   `json:"view"`
	Actions  []string `json:"actions"`
	EngineID string   `json:"engineId"`
}

func (c *client) environments() []envItem {
	c.e.t.Helper()
	var page struct {
		Items []envItem `json:"items"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil).json(c.e.t, &page)
	return page.Items
}

func (c *client) jobIDs() []string {
	c.e.t.Helper()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/jobs", nil).json(c.e.t, &page)
	out := []string{}
	for _, j := range page.Items {
		out = append(out, j.ID)
	}
	return out
}

type myPermissions struct {
	Owner   bool `json:"owner"`
	Entries []struct {
		Capability string `json:"capability"`
		Allowed    bool   `json:"allowed"`
		Source     string `json:"source"`
	} `json:"entries"`
	Environments []struct {
		ID      string   `json:"id"`
		View    string   `json:"view"`
		Actions []string `json:"actions"`
	} `json:"environments"`
}

func (c *client) myPermissions() myPermissions {
	c.e.t.Helper()
	var out myPermissions
	c.must(http.StatusOK, http.MethodGet, "/api/v1/me/permissions", nil).json(c.e.t, &out)
	return out
}

// TestRestrictedUserSeesNoResources (#17 Done-when 1, #233): a new
// instance has no groups and a new account is in none: it sees no
// environments, agents, jobs, audit records or permissions, and the
// owner-only permission administration is refused.
func TestRestrictedUserSeesNoResources(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	job := e.restartJob(ownerID, "e1", "web")

	if gs := owner.groups(); len(gs) != 0 {
		t.Fatalf("a new instance has groups %+v", gs)
	}
	user, _, s := e.newUser(owner, "rita")
	if s.User.GroupIDs == nil || len(s.User.GroupIDs) != 0 {
		t.Fatalf("new user in groups %v, want none", s.User.GroupIDs)
	}
	if envs := user.environments(); len(envs) != 0 {
		t.Fatalf("environments %+v", envs)
	}
	if ids := user.jobIDs(); len(ids) != 0 {
		t.Fatalf("jobs %v", ids)
	}
	user.must(http.StatusOK, http.MethodGet, "/api/v1/agents", nil)
	for _, p := range []string{"/api/v1/environments/e1", "/api/v1/environments/e1/system", "/api/v1/environments/e1/agents",
		"/api/v1/jobs/" + job.ID, "/api/v1/jobs/" + job.ID + "/events/stream"} {
		user.fail(http.StatusNotFound, "not_found", http.MethodGet, p, nil)
	}
	// Stacks (#7), containers (#6), logs and terminals (#8) and metrics (#5)
	// of the hidden environment do not exist for the new user.
	e.seedStack("st-1", "e1", "shop")
	var stacks struct {
		Items []json.RawMessage `json:"items"`
	}
	user.must(http.StatusOK, http.MethodGet, "/api/v1/stacks", nil).json(t, &stacks)
	if stacks.Items == nil || len(stacks.Items) != 0 {
		t.Fatalf("stacks %+v", stacks)
	}
	for _, p := range []string{"/api/v1/stacks/st-1", "/api/v1/stacks/st-1/services", "/api/v1/stacks/st-1/revisions",
		"/api/v1/stacks/st-1/events/stream", "/api/v1/environments/e1/stacks/discovered", "/api/v1/environments/e1/containers",
		"/api/v1/environments/e1/containers/web", "/api/v1/environments/e1/containers/web/logs",
		"/api/v1/environments/e1/containers/web/logs/stream", "/api/v1/environments/e1/containers/web/metrics",
		"/api/v1/environments/e1/metrics"} {
		user.fail(http.StatusNotFound, "not_found", http.MethodGet, p, nil)
	}
	for _, p := range []string{"/api/v1/stacks/st-1/deployments", "/api/v1/stacks/st-1/builds",
		"/api/v1/environments/e1/containers/web/exec-sessions", "/api/v1/environments/e1/containers/web/restart"} {
		user.fail(http.StatusNotFound, "not_found", http.MethodPost, p, map[string]any{}, header("Idempotency-Key", "k-"+strings.ReplaceAll(p, "/", ".")))
	}
	user.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/audit", nil)
	user.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/audit/exports", nil)
	user.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/agent-enrollments", nil)
	if mp := user.myPermissions(); mp.Owner || len(mp.Entries) != 0 || len(mp.Environments) != 0 {
		t.Fatalf("my permissions %+v", mp)
	}
	var cat struct {
		Version      int `json:"version"`
		Capabilities []struct {
			Key string `json:"key"`
		} `json:"capabilities"`
	}
	user.must(http.StatusOK, http.MethodGet, "/api/v1/permission-catalog", nil).json(t, &cat)
	if cat.Version != catalog.Version || len(cat.Capabilities) != len(catalog.Default().Keys()) {
		t.Fatalf("catalog v%d with %d capabilities", cat.Version, len(cat.Capabilities))
	}
	other := owner.createGroup("Other")
	for _, p := range []string{"/api/v1/groups", "/api/v1/groups/" + other.ID, "/api/v1/groups/" + other.ID + "/permissions",
		"/api/v1/users/" + ownerID + "/permissions", "/api/v1/users/" + ownerID + "/effective-permissions"} {
		user.fail(http.StatusForbidden, "forbidden", http.MethodGet, p, nil)
	}
	user.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/permission-previews", map[string]string{"userId": ownerID})
	user.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/groups", map[string]string{"name": "Mine"})
	e.client().fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/permission-catalog", nil)
}

// TestMetricsAndRestartOnlyThroughRealRoutes (#17 Done-when 2):
// metrics-only and restart-only grants expose only the minimal environment
// view and the granted function; direct calls and streams for anything
// else fail.
func TestMetricsAndRestartOnlyThroughRealRoutes(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	e.seedEnvironment("e2", "Cloud")
	webJob := e.restartJob(ownerID, "e1", "web")
	dbJob := e.restartJob(ownerID, "e1", "db")

	// Metrics-only group.
	metrics := owner.createGroup("Metrics")
	owner.putRules("/api/v1/groups/"+metrics.ID+"/permissions",
		"allow environment.metrics.read @env:e1", "allow container.metrics.read @container:e1/web")
	mia, _, ms := e.newUser(owner, "mia")
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+ms.User.ID, map[string]any{"groupIds": []string{metrics.ID}},
		header("If-Match", owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ms.User.ID, nil).header.Get("ETag")))

	envs := mia.environments()
	if len(envs) != 1 || envs[0].ID != "e1" || envs[0].View != "minimal" || !slices.Equal(envs[0].Actions, []string{"environment.metrics.read"}) || envs[0].EngineID != "" {
		t.Fatalf("metrics-only environments %+v", envs)
	}
	r := mia.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1", nil)
	if r.header.Get("ETag") != "" || strings.Contains(string(r.body), "ENGINE-e1") || strings.Contains(string(r.body), "install-") {
		t.Fatalf("minimal environment leaks detail: %v %s", r.header, r.body)
	}
	mia.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/environments/e1/system", nil)
	mia.fail(http.StatusForbidden, "forbidden", http.MethodPatch, "/api/v1/environments/e1", map[string]string{"name": "x"}, header("If-Match", "*"))
	mia.fail(http.StatusForbidden, "forbidden", http.MethodDelete, "/api/v1/environments/e1", nil, header("If-Match", "*"))
	mia.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/environments/e2", nil)
	if ids := mia.jobIDs(); len(ids) != 0 {
		t.Fatalf("metrics-only user sees jobs %v", ids)
	}
	mia.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+webJob.ID+"/events/stream", nil)
	if mp := mia.myPermissions(); len(mp.Environments) != 1 || mp.Environments[0].View != "minimal" || len(mp.Entries) != 2 {
		t.Fatalf("my permissions %+v", mp)
	}

	// Restart-only as a user override (the user stays in no group).
	sam, _, ss := e.newUser(owner, "sam")
	owner.putRules("/api/v1/users/"+ss.User.ID+"/permissions", "allow container.restart @container:e1/web")
	if ids := sam.jobIDs(); !slices.Equal(ids, []string{webJob.ID}) {
		t.Fatalf("restart-only jobs %v, want only the web restart", ids)
	}
	sam.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	done := sam.openStream(e.srv.URL + "/api/v1/jobs/" + webJob.ID + "/events/stream")
	sam.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+dbJob.ID, nil)
	sam.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+dbJob.ID+"/events/stream", nil)
	sam.fail(http.StatusNotFound, "not_found", http.MethodPost, "/api/v1/jobs/"+dbJob.ID+"/cancellations", nil)
	sam.must(http.StatusAccepted, http.MethodPost, "/api/v1/jobs/"+webJob.ID+"/cancellations", nil)
	waitClosed(t, done, "web restart job stream after cancellation")
	envs = sam.environments()
	if len(envs) != 1 || envs[0].ID != "e1" || envs[0].View != "minimal" || len(envs[0].Actions) != 0 {
		t.Fatalf("restart-only environments %+v", envs)
	}
	sam.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/environments/e1/system", nil)

	// Revoking the grant ends an open job stream with a close event.
	webJob2 := e.restartJob(ownerID, "e1", "web")
	rest := sam.openStreamBody(e.srv.URL + "/api/v1/jobs/" + webJob2.ID + "/events/stream")
	owner.putRules("/api/v1/users/" + ss.User.ID + "/permissions")
	select {
	case body := <-rest:
		if !strings.Contains(body, "event: close\ndata: {\"reason\":\"permissions_changed\"}") {
			t.Fatalf("stream end %q", body)
		}
	case <-testutil.Context(t).Done():
		t.Fatal("job stream still open after the grant was revoked")
	}
	sam.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+webJob2.ID+"/events/stream", nil)
	owner.putRules("/api/v1/users/"+ss.User.ID+"/permissions", "allow container.restart @container:e1/web")
	samP := authz.Principal{Kind: authz.KindUser, UserID: ss.User.ID}
	ctx := testutil.Context(t)
	web := []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
	if _, _, err := e.m.Jobs().Enqueue(ctx, jobs.Request{Kind: "container.restart", Principal: samP, EnvironmentID: "e1", Targets: web}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	for _, kind := range []domain.JobKind{"container.start", "container.stop", "container.remove"} {
		if _, _, err := e.m.Jobs().Enqueue(ctx, jobs.Request{Kind: kind, Principal: samP, EnvironmentID: "e1", Targets: web}); err == nil {
			t.Errorf("%s allowed with restart-only", kind)
		}
	}
	// Logs, terminal, details and metrics are separate capabilities.
	var pv struct {
		Checks []struct {
			Capability string `json:"capability"`
			Allowed    bool   `json:"allowed"`
			Source     string `json:"source"`
		} `json:"checks"`
	}
	container := map[string]string{"type": "container", "id": "web", "environmentId": "e1"}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/permission-previews", map[string]any{"userId": ss.User.ID, "checks": []map[string]any{
		{"capability": "container.restart", "resource": container}, {"capability": "container.logs.read", "resource": container},
		{"capability": "container.exec", "resource": container}, {"capability": "container.details.read", "resource": container},
		{"capability": "container.metrics.read", "resource": container}}}).json(t, &pv)
	if len(pv.Checks) != 5 || !pv.Checks[0].Allowed || pv.Checks[0].Source != "user_rule" {
		t.Fatalf("preview %+v", pv)
	}
	for _, c := range pv.Checks[1:] {
		if c.Allowed || c.Source != "default_deny" {
			t.Errorf("restart-only also grants %s: %+v", c.Capability, c)
		}
	}
}

type previewBody struct {
	Checks []struct {
		Allowed bool   `json:"allowed"`
		Source  string `json:"source"`
		Reason  string `json:"reason"`
		Rule    *struct {
			Effect string `json:"effect"`
			Scope  struct {
				Kind string `json:"kind"`
			} `json:"scope"`
		} `json:"rule"`
	} `json:"checks"`
}

// TestUserOverridesInheritanceAndPreview (#17 Done-when 3): a user allow
// or deny overrides the group; clearing it restores inheritance; exact /
// environment / all precedence is shown with the deciding rule.
func TestUserOverridesInheritanceAndPreview(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	ops := owner.createGroup("Ops")
	owner.putRules("/api/v1/groups/"+ops.ID+"/permissions", "allow container.restart @all", "deny container.restart @env:e2")
	_, _, rs := e.newUser(owner, "rita")
	rita := rs.User.ID
	if r := owner.moveUser(rita, ops.ID); r.status != http.StatusOK {
		t.Fatalf("move: %d %s", r.status, r.body)
	}
	preview := func() previewBody {
		t.Helper()
		var out previewBody
		c := func(env, name string) map[string]any {
			return map[string]any{"capability": "container.restart", "resource": map[string]string{"type": "container", "id": name, "environmentId": env}}
		}
		owner.must(http.StatusOK, http.MethodPost, "/api/v1/permission-previews", map[string]any{"userId": rita,
			"checks": []map[string]any{c("e1", "web"), c("e1", "db"), c("e2", "web")}}).json(t, &out)
		return out
	}
	want := func(p previewBody, decisions ...string) {
		t.Helper()
		for i, d := range decisions {
			got := p.Checks[i].Source + "/" + map[bool]string{true: "allow", false: "deny"}[p.Checks[i].Allowed]
			if p.Checks[i].Rule != nil {
				got += "@" + p.Checks[i].Rule.Scope.Kind
			}
			if got != d || p.Checks[i].Reason == "" {
				t.Errorf("check %d: %s (%s), want %s", i, got, p.Checks[i].Reason, d)
			}
		}
	}
	// Group only: all allows, the environment deny wins in e2.
	want(preview(), "group_rule/allow@instance", "group_rule/allow@instance", "group_rule/deny@environment")

	// A user deny on one container and a user allow in e2 override the group.
	path := "/api/v1/users/" + rita + "/permissions"
	owner.putRules(path, "deny container.restart @container:e1/web", "allow container.restart @env:e2")
	want(preview(), "user_rule/deny@resource", "group_rule/allow@instance", "user_rule/allow@environment")
	var eff struct {
		Entries []struct {
			Capability string `json:"capability"`
			Allowed    bool   `json:"allowed"`
			Source     string `json:"source"`
		} `json:"entries"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+rita+"/effective-permissions", nil).json(t, &eff)
	// One entry per capability and scope named by any rule (env:e2 is
	// named by both the group and the user; the user decides).
	if len(eff.Entries) != 3 || eff.Entries[1].Source != "user_rule" || !eff.Entries[1].Allowed {
		t.Fatalf("effective %+v", eff)
	}
	// The engine enforces the same decisions.
	ritaP := authz.Principal{Kind: authz.KindUser, UserID: rita}
	enq := func(env, name string) error {
		_, _, err := e.m.Jobs().Enqueue(testutil.Context(t), jobs.Request{Kind: "container.restart", Principal: ritaP, EnvironmentID: env,
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: name}}})
		return err
	}
	if enq("e1", "web") == nil || enq("e1", "db") != nil || enq("e2", "web") != nil {
		t.Fatal("engine decisions differ from the preview")
	}

	// Reset to inherit.
	owner.putRules(path)
	want(preview(), "group_rule/allow@instance", "group_rule/allow@instance", "group_rule/deny@environment")
	if enq("e1", "web") != nil {
		t.Fatal("reset to inherit did not restore the group grant")
	}

	// Editing contract: If-Match, stale revisions, ambiguous and
	// ungrantable rules, the protected owner.
	cur := owner.must(http.StatusOK, http.MethodGet, path, nil)
	owner.fail(http.StatusPreconditionRequired, "precondition_required", http.MethodPut, path, map[string]any{"rules": []any{}})
	owner.fail(http.StatusPreconditionFailed, "precondition_failed", http.MethodPut, path, map[string]any{"rules": []any{}}, header("If-Match", `"1"`))
	r := owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPut, path,
		map[string]any{"rules": apiRules(t, "allow container.restart @all", "deny container.restart @all")}, header("If-Match", cur.header.Get("ETag")))
	if !strings.Contains(string(r.body), "body.rules[1]") {
		t.Fatalf("duplicate rule error %s", r.body)
	}
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPut, path,
		map[string]any{"rules": []map[string]any{{"capability": "users.manage", "effect": "allow", "scope": map[string]string{"kind": "instance"}}}},
		header("If-Match", cur.header.Get("ETag")))
	ownerDoc := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ownerID+"/permissions", nil)
	owner.fail(http.StatusConflict, "owner_protected", http.MethodPut, "/api/v1/users/"+ownerID+"/permissions",
		map[string]any{"rules": apiRules(t, "deny container.exec @all")}, header("If-Match", ownerDoc.header.Get("ETag")))
}

// TestGroupChangesUpdateAccess (#17, #233): creating, renaming,
// deleting and reordering groups and changing memberships need a recent
// step-up, are audited with diffs, and change access immediately: the
// affected users' open streams are closed. New accounts are in no group.
func TestGroupChangesUpdateAccess(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	e.seedEnvironment("e2", "Cloud")
	streams := e.streamServer()

	restricted := owner.createGroup("Restricted")
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+restricted.ID, nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/groups/"+restricted.ID, map[string]string{"name": "Newcomers"}, header("If-Match", r.header.Get("ETag")))
	if g := owner.groupNamed("Newcomers"); g.ID != restricted.ID {
		t.Fatalf("renamed group %+v", g)
	}
	viewers := owner.createGroup("Viewers")
	owner.fail(http.StatusConflict, "group_name_taken", http.MethodPost, "/api/v1/groups", map[string]string{"name": "viewers"})
	owner.putRules("/api/v1/groups/"+viewers.ID+"/permissions", "allow environment.read @all")

	// New users are in no group and see nothing until they join one.
	rita, _, rs := e.newUser(owner, "rita")
	if len(rs.User.GroupIDs) != 0 || len(rita.environments()) != 0 {
		t.Fatalf("new user in %v sees environments", rs.User.GroupIDs)
	}
	vic, _, vs := e.newUser(owner, "vic")
	if r := owner.moveUser(vs.User.ID, viewers.ID); r.status != http.StatusOK {
		t.Fatalf("add vic: %d %s", r.status, r.body)
	}
	if len(vic.environments()) != 2 {
		t.Fatalf("vic in Viewers sees %+v", vic.environments())
	}

	// Moving rita ends her open streams and grants the new group at once.
	done := rita.openStream(streams.URL + "/api/v1/test/stream")
	if r := owner.moveUser(rs.User.ID, viewers.ID); r.status != http.StatusOK {
		t.Fatalf("move: %d %s", r.status, r.body)
	}
	waitClosed(t, done, "group move")
	if envs := rita.environments(); len(envs) != 2 || envs[0].View != "full" {
		t.Fatalf("after the move %+v", envs)
	}
	// Narrowing the group's rules ends members' streams and narrows access.
	done = rita.openStream(streams.URL + "/api/v1/test/stream")
	doneVic := vic.openStream(streams.URL + "/api/v1/test/stream")
	owner.putRules("/api/v1/groups/"+viewers.ID+"/permissions", "allow environment.read @env:e1")
	waitClosed(t, done, "group rule change (rita)")
	waitClosed(t, doneVic, "group rule change (vic)")
	if envs := rita.environments(); len(envs) != 1 || envs[0].ID != "e1" {
		t.Fatalf("after narrowing %+v", envs)
	}
	// A user override change ends that user's streams.
	done = rita.openStream(streams.URL + "/api/v1/test/stream")
	owner.putRules("/api/v1/users/"+rs.User.ID+"/permissions", "deny environment.read @all")
	waitClosed(t, done, "user override change")
	if envs := rita.environments(); len(envs) != 0 {
		t.Fatalf("after the user deny %+v", envs)
	}
	// Editing a group never cuts the owner's request.
	owner.putRules("/api/v1/groups/"+restricted.ID+"/permissions", "allow stack.read @all")

	// A group with members cannot be deleted; users are never moved
	// implicitly.
	if r := owner.moveUser(rs.User.ID, restricted.ID); r.status != http.StatusOK {
		t.Fatalf("move back: %d %s", r.status, r.body)
	}
	r = owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+restricted.ID, nil)
	owner.fail(http.StatusConflict, "group_not_empty", http.MethodDelete, "/api/v1/groups/"+restricted.ID, nil, header("If-Match", r.header.Get("ETag")))
	if r := owner.moveUser(rs.User.ID, viewers.ID); r.status != http.StatusOK {
		t.Fatalf("move: %d %s", r.status, r.body)
	}
	// The owner is in no group, so the emptied group is deleted.
	var g groupBody
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+restricted.ID, nil).json(t, &g)
	if g.MemberCount != 0 {
		t.Fatalf("emptied group: %+v", g)
	}
	r = owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+restricted.ID, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/groups/"+restricted.ID, nil, header("If-Match", r.header.Get("ETag")))
	if gs := owner.groups(); len(gs) != 1 || gs[0].ID != viewers.ID || gs[0].MemberCount != 2 {
		t.Fatalf("groups after delete %+v", gs)
	}
	var acct struct {
		GroupIDs []string `json:"groupIds"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ownerID, nil).json(t, &acct)
	if acct.GroupIDs == nil || len(acct.GroupIDs) != 0 {
		t.Fatalf("owner in %v, want no group", acct.GroupIDs)
	}
	owner.fail(http.StatusConflict, "owner_protected", http.MethodPatch, "/api/v1/users/"+ownerID, map[string]any{"groupIds": []string{viewers.ID}},
		header("If-Match", owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ownerID, nil).header.Get("ETag")))

	// Several groups: the first group with a matching rule decides, and
	// the owner reorders the groups by the order's ETag.
	blocked := owner.createGroup("Blocked")
	owner.putRules("/api/v1/groups/"+blocked.ID+"/permissions", "deny environment.read @env:e1")
	cur := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+vs.User.ID, nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+vs.User.ID, map[string]any{"groupIds": []string{viewers.ID, blocked.ID}},
		header("If-Match", cur.header.Get("ETag")))
	if envs := vic.environments(); len(envs) != 1 {
		t.Fatalf("with Viewers first %+v", envs)
	}
	list := owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups", nil)
	owner.fail(http.StatusPreconditionRequired, "precondition_required", http.MethodPut, "/api/v1/group-order", map[string]any{"groupIds": []string{blocked.ID, viewers.ID}})
	done = vic.openStream(streams.URL + "/api/v1/test/stream")
	owner.must(http.StatusOK, http.MethodPut, "/api/v1/group-order", map[string]any{"groupIds": []string{blocked.ID, viewers.ID}},
		header("If-Match", list.header.Get("ETag")))
	waitClosed(t, done, "group order change (vic)")
	if envs := vic.environments(); len(envs) != 0 {
		t.Fatalf("with Blocked first %+v", envs)
	}
	owner.fail(http.StatusPreconditionFailed, "precondition_failed", http.MethodPut, "/api/v1/group-order", map[string]any{"groupIds": []string{viewers.ID, blocked.ID}},
		header("If-Match", list.header.Get("ETag")))
	if gs := owner.groups(); len(gs) != 2 || gs[0].ID != blocked.ID {
		t.Fatalf("groups after the reorder %+v", gs)
	}
	cur = owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+vs.User.ID, nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+vs.User.ID, map[string]any{"groupIds": []string{viewers.ID}},
		header("If-Match", cur.header.Get("ETag")))
	r = owner.must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+blocked.ID, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/groups/"+blocked.ID, nil, header("If-Match", r.header.Get("ETag")))

	// Every permission or group change needs a recent step-up.
	e.clk.Advance(auth.StepUpWindow + time.Second)
	path := "/api/v1/groups/" + viewers.ID + "/permissions"
	cur = owner.must(http.StatusOK, http.MethodGet, path, nil)
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPut, path, map[string]any{"rules": []any{}}, header("If-Match", cur.header.Get("ETag")))
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPost, "/api/v1/groups", map[string]string{"name": "Late"})
	other := owner.createGroupAfterStepUp(pw)
	r = owner.moveUser(rs.User.ID, other.ID)
	if r.status != http.StatusOK {
		t.Fatalf("move after step-up: %d %s", r.status, r.body)
	}
	e.clk.Advance(auth.StepUpWindow + time.Second)
	if r := owner.moveUser(rs.User.ID, viewers.ID); r.status != http.StatusForbidden || r.code() != "step_up_required" {
		t.Fatalf("move without step-up: %d %s", r.status, r.body)
	}

	// The audit trail holds the rule diffs.
	var page struct {
		Items []struct {
			Action   string         `json:"action"`
			Category string         `json:"category"`
			Details  map[string]any `json:"details"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/audit?action=group_permissions.replace", nil).json(t, &page)
	if len(page.Items) < 3 {
		t.Fatalf("audit records %+v", page.Items)
	}
	b, _ := json.Marshal(page.Items)
	for _, want := range []string{`"category":"authorization"`, `"rulesAdded":["allow environment.read @env:e1"]`,
		`"rulesRemoved":["allow environment.read @all"]`, `"before":{"revision":2,"rules":["allow environment.read @all"]}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %s: %s", want, b)
		}
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/audit?action=user.update", nil).json(t, &page)
	moves := 0
	for _, it := range page.Items {
		d := string(mustJSON(t, it.Details))
		if strings.Contains(d, `"event":"user.group_change"`) && strings.Contains(d, `"diff":{"after":{"groupIds":`) {
			moves++
		}
	}
	if moves < 3 {
		t.Fatalf("group move audit records %d: %+v", moves, page.Items)
	}
}

// openStreamBody opens a stream with c's session and returns a channel
// receiving the rest of the stream once the server ends it.
func (c *client) openStreamBody(url string) <-chan string {
	t := c.e.t
	t.Helper()
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", publicHost)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	out := make(chan string, 1)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		out <- string(b)
	}()
	return out
}

func (c *client) createGroupAfterStepUp(pw string) groupBody {
	c.e.t.Helper()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": pw})
	return c.createGroup("Late")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
