package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	agentres "github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/authz/policy"
	"github.com/neurekadev/dockyard/internal/manager/metrics"
	"github.com/neurekadev/dockyard/internal/protocol"
)

type containerJSON struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	EnvironmentID string            `json:"environmentId"`
	State         string            `json:"state"`
	View          string            `json:"view"`
	Actions       []string          `json:"actions"`
	Image         string            `json:"image"`
	Labels        map[string]string `json:"labels"`
	Stack         *struct {
		Project string `json:"project"`
		Service string `json:"service"`
		StackID string `json:"stackId"`
		Managed bool   `json:"managed"`
	} `json:"stack"`
	Managed *struct {
		Kind         string `json:"kind"`
		SpecSaved    bool   `json:"specSaved"`
		ThisInstance bool   `json:"thisInstance"`
	} `json:"managed"`
	Details *struct {
		RestartPolicy string `json:"restartPolicy"`
		Recreate      struct {
			Fields  []string `json:"fields"`
			EnvKeys []string `json:"envKeys"`
		} `json:"recreate"`
		Removal Removal `json:"removal"`
	} `json:"details"`
}

type containerPage struct {
	Items      []containerJSON `json:"items"`
	NextCursor string          `json:"nextCursor"`
	Total      *int64          `json:"total"`
}

type errJSON struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []ErrorDetail `json:"details"`
}

func code(t *testing.T, r authztest.Response) string {
	t.Helper()
	var e errJSON
	_ = json.Unmarshal(r.Body, &e)
	return e.Code
}

func names(items []containerJSON) []string {
	var out []string
	for _, c := range items {
		out = append(out, c.Name)
	}
	return out
}

// TestDockerRoutesRestricted: a Restricted user gets 404 (or an empty page)
// for every Docker route of every environment, before any agent is asked.
func TestDockerRoutesRestricted(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Member("rita", "restricted"))
	authztest.AssertOnly(t, f.h, "rita", nil, f.dockerRoutes())
	if len(f.req.calls) != 0 {
		t.Fatalf("the agent was asked for a hidden environment: %v", f.req.calls)
	}
	if f.jobs.count() != 0 {
		t.Fatal("a job was enqueued")
	}
}

// TestContainerMetricsOnly (#17 Done-when 2): container.metrics.read on one
// container shows only that container, minimally (identity, state,
// stack/service, the granted action), and refuses every other route.
func TestContainerMetricsOnly(t *testing.T) {
	f := newDockerFixture(t, authztest.Only("mia", "allow container.metrics.read @container:env-1/web"))
	allowed, denied := authztest.Split(f.dockerRoutes(), "container.metrics.read")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-containers", "get-container")
	authztest.AssertOnly(t, f.h, "mia", allowed, denied)

	var page containerPage
	r := f.get("mia", "/api/v1/environments/env-1/containers", &page)
	if len(page.Items) != 1 || page.Items[0].Name != "web" || page.Items[0].View != "minimal" ||
		!slices.Equal(page.Items[0].Actions, []string{"container.metrics.read"}) || page.Total == nil || *page.Total != 1 {
		t.Fatalf("metrics-only list %s", r.Body)
	}
	r = f.get("mia", "/api/v1/environments/env-1/containers/web", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	// Only identity and status: no image, command, labels, ports, mounts,
	// configuration or removal data.
	authztest.AssertAbsent(t, "metrics-only container", r.Body, "nginx", "front-env-1", `"ports"`, `"labels"`, `"details"`, `"command"`, "8080")
	for _, p := range []string{"/api/v1/environments/env-1/containers/db", "/api/v1/environments/env-2/containers/web",
		"/api/v1/environments/env-1/containers/" + f.containerID("env-2", "web")} {
		if r := f.get("mia", p, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, r.Status)
		}
	}
	// Other resource types are invisible.
	for _, p := range []string{"/api/v1/environments/env-1/images", "/api/v1/environments/env-1/volumes", "/api/v1/environments/env-1/networks"} {
		var pg struct{ Items []json.RawMessage }
		if r := f.get("mia", p, &pg); r.Status != http.StatusOK || len(pg.Items) != 0 {
			t.Errorf("%s: %d %s", p, r.Status, r.Body)
		}
	}
}

// TestContainerRestartOnly (#17 Done-when 2): container.restart on one
// container (by name or through its stack) allows exactly the restart and
// shows the container minimally; start, stop, update, removal and other
// containers are refused.
func TestContainerRestartOnly(t *testing.T) {
	t.Run("container", func(t *testing.T) {
		f := newDockerFixture(t, authztest.Only("rex", "allow container.restart @container:env-1/web"))
		allowed, denied := authztest.Split(f.dockerRoutes(), "container.restart")
		allowed, denied = authztest.Discoverable(allowed, denied, "list-containers", "get-container")
		authztest.AssertOnly(t, f.h, "rex", allowed, denied)
		r := f.do("rex", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/containers/web/restart"})
		if r.Status != http.StatusAccepted || !strings.HasPrefix(r.Header.Get("Location"), "/api/v1/jobs/") {
			t.Fatalf("restart: %d %s", r.Status, r.Body)
		}
		req := f.jobs.last(t)
		var in protocol.ContainerActionInput
		raw, _ := json.Marshal(req.Input)
		_ = json.Unmarshal(raw, &in)
		if req.Kind != jobspec.ContainerRestart || req.EnvironmentID != "env-1" || req.Targets[0].ID != "web" ||
			in.ID != f.containerID("env-1", "web") || in.Name != "web" {
			t.Fatalf("job %+v input %+v", req, in)
		}
		// Visible but not granted: 403; another container: 404.
		for _, c := range []struct {
			path   string
			status int
		}{
			{"/api/v1/environments/env-1/containers/web/stop", http.StatusForbidden},
			{"/api/v1/environments/env-1/containers/web/start", http.StatusForbidden},
			{"/api/v1/environments/env-1/containers/db/restart", http.StatusNotFound},
			{"/api/v1/environments/env-2/containers/web/restart", http.StatusNotFound},
		} {
			if r := f.do("rex", authztest.Call{Method: http.MethodPost, Path: c.path}); r.Status != c.status {
				t.Errorf("%s: %d, want %d", c.path, r.Status, c.status)
			}
		}
	})
	t.Run("stack", func(t *testing.T) {
		pol := authztest.Only("rex", "allow container.restart @stack:stack-shop")
		f := newDockerFixture(t, pol)
		f.stacks["env-1"]["shop"] = "stack-shop"
		// The resource graph as the manager wires it: the stack Locator (#7)
		// places stack-shop in env-1; the container Locator of the resource
		// service knows the stack membership it saw (used by the job
		// engine's checks, which carry no parents).
		pol.Locate(func(ref authz.ResourceRef) policy.Location {
			if ref.Type == catalog.TypeStack && ref.ID == "stack-shop" {
				return policy.Location{Found: true, EnvironmentID: "env-1"}
			}
			loc, err := f.svc.Locator(ref.Type).Locate(t.Context(), ref)
			if err != nil {
				t.Error(err)
			}
			return loc
		})
		var page containerPage
		f.get("rex", "/api/v1/environments/env-1/containers", &page)
		if got := names(page.Items); !slices.Equal(got, []string{"shop-db-1", "shop-web-1"}) {
			t.Fatalf("stack-scoped restart sees %v", got)
		}
		if s := page.Items[0].Stack; s == nil || s.StackID != "stack-shop" || !s.Managed || s.Service != "db" || page.Items[0].View != "minimal" {
			t.Fatalf("stack identity %+v", page.Items[0])
		}
		if r := f.do("rex", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/containers/shop-web-1/restart"}); r.Status != http.StatusAccepted {
			t.Fatalf("restart stack container: %d %s", r.Status, r.Body)
		}
		if r := f.do("rex", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/containers/web/restart"}); r.Status != http.StatusNotFound {
			t.Fatalf("restart outside the stack: %d", r.Status)
		}
	})
}

// TestContainersAcrossEnvironments: the same name exists on two
// environments with different IDs; every lookup is scoped by the path's
// environment, and an offline environment yields a clear 503.
func TestContainersAcrossEnvironments(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	var a, b containerJSON
	f.get("olga", "/api/v1/environments/env-1/containers/web", &a)
	f.get("olga", "/api/v1/environments/env-2/containers/web", &b)
	if a.ID == "" || a.ID == b.ID || a.EnvironmentID != "env-1" || b.EnvironmentID != "env-2" || a.Labels["tier"] != "front-env-1" {
		t.Fatalf("env-1 %+v env-2 %+v", a, b)
	}
	// env-2's container ID is unknown on env-1 (no cross-environment lookup).
	if r := f.get("olga", "/api/v1/environments/env-1/containers/"+b.ID, nil); r.Status != http.StatusNotFound {
		t.Fatalf("env-2 ID through env-1: %d", r.Status)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/containers/" + b.ID[:12] + "/stop"}); r.Status != http.StatusNotFound {
		t.Fatalf("stop env-2's container through env-1: %d", r.Status)
	}
	if f.jobs.count() != 0 {
		t.Fatal("a job was enqueued for a foreign ID")
	}
	f.req.offline["env-2"] = true
	for _, p := range []string{"/api/v1/environments/env-2/containers", "/api/v1/environments/env-2/containers/web", "/api/v1/environments/env-2/images"} {
		r := f.get("olga", p, nil)
		var e errJSON
		_ = json.Unmarshal(r.Body, &e)
		if r.Status != http.StatusServiceUnavailable || e.Code != CodeEnvironmentOffline || !strings.Contains(e.Message, "not connected") {
			t.Errorf("%s offline: %d %s", p, r.Status, r.Body)
		}
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-2/containers/web/restart"}); r.Status != http.StatusServiceUnavailable || code(t, r) != CodeEnvironmentOffline {
		t.Fatalf("restart offline: %d %s", r.Status, r.Body)
	}
	if r := f.get("olga", "/api/v1/environments/env-9/containers", nil); r.Status != http.StatusNotFound {
		t.Fatalf("unknown environment: %d", r.Status)
	}
}

// TestEngineErrorsMapToStableCodes: Engine failures reach API clients as
// stable codes.
func TestEngineErrorsMapToStableCodes(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	fe := f.engines["env-1"]
	for _, c := range []struct {
		code   engine.Code
		status int
		want   string
	}{
		{engine.CodeUnsupportedAPIVersion, http.StatusConflict, CodeUnsupportedAPIVersion},
		{engine.CodeEngineUnavailable, http.StatusServiceUnavailable, CodeEngineUnavailable},
		{engine.CodeEngineError, http.StatusBadGateway, CodeEngineError},
		{engine.CodeTimeout, http.StatusGatewayTimeout, CodeTimeout},
	} {
		fe.Fail("container.list", enginefake.Err("container.list", c.code, "boom"))
		r := f.get("olga", "/api/v1/environments/env-1/containers", nil)
		if r.Status != c.status || code(t, r) != c.want {
			t.Errorf("%s: %d %s, want %d %s", c.code, r.Status, r.Body, c.status, c.want)
		}
	}
	// An agent that predates a request.
	f.req.drop = map[string]bool{protocol.ReqVolumeList: true}
	if r := f.get("olga", "/api/v1/environments/env-1/volumes", nil); r.Status != http.StatusNotImplemented || code(t, r) != CodeAgentUnsupported {
		t.Fatalf("old agent: %d %s", r.Status, r.Body)
	}
}

// TestContainerLifecycleRoutes: create validates the form and saves the
// recreate specification; update accepts only in-place settings; removal
// refuses running and stack-managed containers; every mutation is a job.
func TestContainerLifecycleRoutes(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	post := func(path string, body any, headers ...string) authztest.Response {
		c := authztest.Call{Method: http.MethodPost, Path: path, Body: body, Headers: map[string]string{}}
		for i := 0; i+1 < len(headers); i += 2 {
			c.Headers[headers[i]] = headers[i+1]
		}
		return f.do("olga", c)
	}
	base := "/api/v1/environments/env-1/containers"
	body := map[string]any{"name": "api", "image": "nginx:1.27", "env": []string{"TOKEN=s3cret"}, "restartPolicy": "unless-stopped",
		"ports": []map[string]any{{"containerPort": 80, "hostPort": 8081}}, "networks": []map[string]any{{"name": "spare"}},
		"resources": map[string]any{"cpus": 1.5, "memoryBytes": 64 << 20}, "labels": map[string]string{"team": "ops"}}
	r := post(base, body, "Idempotency-Key", "create-api")
	if r.Status != http.StatusAccepted {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	req := f.jobs.last(t)
	raw, _ := json.Marshal(req.Input)
	var in protocol.ContainerCreateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if req.Kind != jobspec.ContainerCreate || req.IdempotencyKey != "create-api" || !in.Start || in.Spec.Resources.NanoCPUs != 1_500_000_000 ||
		in.Ownership[protocol.LabelManaged] != protocol.ManagedStandalone || in.Ownership[protocol.LabelInstance] != "instance-1" ||
		in.Ownership[protocol.LabelSpec] == "" || in.Spec.Networks[0].Name != "spare" {
		t.Fatalf("create job %+v %+v", req, in)
	}
	// A retry with the same key carries the same input (same spec ID).
	post(base, body, "Idempotency-Key", "create-api")
	raw2, _ := json.Marshal(f.jobs.last(t).Input)
	if string(raw2) != string(raw) {
		t.Fatalf("idempotent retry changed the input:\n%s\n%s", raw, raw2)
	}

	// Validation before any job exists.
	n := f.jobs.count()
	for _, c := range []struct {
		body  map[string]any
		code  string
		field string
	}{
		{map[string]any{"name": "web", "image": "nginx:1.27"}, CodeResourceNameTaken, ""},
		{map[string]any{"name": "x1", "image": "ghcr.io/org/missing:1"}, CodeValidationFailed, "body.image"},
		{map[string]any{"name": "x2", "image": "nginx:1.27", "networks": []map[string]any{{"name": "nope"}}}, CodeValidationFailed, "body.networks[0].name"},
		{map[string]any{"name": "x3", "image": "nginx:1.27", "labels": map[string]string{"dev.neureka.dockyard.role": "manager"}}, CodeValidationFailed, "body.labels"},
		{map[string]any{"name": "x4", "image": "nginx:1.27", "mounts": []map[string]any{{"type": "bind", "source": "/run", "target": "/s"}}}, CodeValidationFailed, "body.mounts[0].source"},
		{map[string]any{"name": "x5", "image": "nginx:1.27", "env": []string{"no-equals-sign"}}, CodeValidationFailed, "body.env[0]"},
		{map[string]any{"name": "x6", "image": "nginx:1.27", "privileged": true}, CodeValidationFailed, ""},
	} {
		r := post(base, c.body)
		var e errJSON
		_ = json.Unmarshal(r.Body, &e)
		if e.Code != c.code || (c.field != "" && (len(e.Details) == 0 || e.Details[0].Field != c.field)) {
			t.Errorf("%v: %d %s, want %s %s", c.body, r.Status, r.Body, c.code, c.field)
		}
		if strings.Contains(string(r.Body), "no-equals-sign") {
			t.Errorf("an env value was echoed: %s", r.Body)
		}
	}
	if f.jobs.count() != n {
		t.Fatal("an invalid create enqueued a job")
	}

	// The created container (simulated) shows its saved specification with
	// variable names only.
	fe := f.engines["env-1"]
	fe.AddContainer(engine.ContainerSpec{Name: "api", Image: "nginx:1.27", Labels: map[string]string{protocol.LabelManaged: protocol.ManagedStandalone,
		protocol.LabelInstance: "instance-1", protocol.LabelSpec: in.Ownership[protocol.LabelSpec]}}, true)
	var api containerJSON
	r = f.get("olga", base+"/api", &api)
	if api.Managed == nil || !api.Managed.SpecSaved || !api.Managed.ThisInstance || api.Details == nil ||
		!slices.Equal(api.Details.Recreate.EnvKeys, []string{"TOKEN"}) || strings.Contains(string(r.Body), "s3cret") ||
		!slices.Contains(api.Details.Recreate.Fields, "image") {
		t.Fatalf("managed container %s", r.Body)
	}

	// PATCH: in-place settings only.
	r = f.do("olga", authztest.Call{Method: http.MethodPatch, Path: base + "/api", Body: map[string]any{"image": "nginx:1.28", "env": []string{"A=1"}}})
	var e errJSON
	_ = json.Unmarshal(r.Body, &e)
	if r.Status != http.StatusUnprocessableEntity || e.Code != CodeRecreateRequired || len(e.Details) != 2 || e.Details[0].Field != "body.image" {
		t.Fatalf("recreate fields: %d %s", r.Status, r.Body)
	}
	r = f.do("olga", authztest.Call{Method: http.MethodPatch, Path: base + "/api", Body: map[string]any{"restartPolicy": "always",
		"resources": map[string]any{"memoryBytes": 128 << 20}}})
	if r.Status != http.StatusAccepted || f.jobs.last(t).Kind != jobspec.ContainerUpdate {
		t.Fatalf("update: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPatch, Path: base + "/api", Body: map[string]any{}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("empty update: %d", r.Status)
	}

	// Removal: running needs force; stack containers are refused; the
	// removal consequences say so.
	var web containerJSON
	f.get("olga", base+"/web", &web)
	if web.Details == nil || !web.Details.Removal.Allowed || len(web.Details.Removal.Consequences) == 0 {
		t.Fatalf("web removal %+v", web.Details)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: base + "/web"}); r.Status != http.StatusConflict || code(t, r) != CodeContainerRunning {
		t.Fatalf("remove running: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: base + "/web?force=true&removeVolumes=true"}); r.Status != http.StatusAccepted {
		t.Fatalf("remove forced: %d %s", r.Status, r.Body)
	}
	raw, _ = json.Marshal(f.jobs.last(t).Input)
	var act protocol.ContainerActionInput
	_ = json.Unmarshal(raw, &act)
	if !act.Force || !act.RemoveVolumes || act.ID != web.ID {
		t.Fatalf("remove input %+v", act)
	}
	var shop containerJSON
	f.get("olga", base+"/shop-web-1", &shop)
	if shop.Details == nil || shop.Details.Removal.Allowed || shop.Details.Removal.Blockers[0].Code != CodeStackManaged || shop.Stack == nil || !shop.Stack.Managed {
		t.Fatalf("stack container %+v", shop)
	}
	for _, c := range []authztest.Call{
		{Method: http.MethodDelete, Path: base + "/shop-web-1?force=true"},
		{Method: http.MethodPatch, Path: base + "/shop-web-1", Body: map[string]any{"restartPolicy": "always"}},
	} {
		if r := f.do("olga", c); r.Status != http.StatusConflict || code(t, r) != CodeStackManaged {
			t.Errorf("%s: %d %s", c, r.Status, r.Body)
		}
	}
	// Runtime actions on stack containers are fine.
	for _, verb := range []string{"stop", "start", "restart", "pause", "unpause"} {
		if r := post(base+"/shop-web-1/"+verb, nil); r.Status != http.StatusAccepted || string(f.jobs.last(t).Kind) != "container."+verb {
			t.Errorf("%s: %d %s", verb, r.Status, r.Body)
		}
	}
	if r := post(base+"/web/stop", map[string]any{"timeoutSeconds": 5}); r.Status != http.StatusAccepted {
		t.Fatalf("stop with timeout: %d %s", r.Status, r.Body)
	}
	raw, _ = json.Marshal(f.jobs.last(t).Input)
	_ = json.Unmarshal(raw, &act)
	if act.TimeoutSeconds == nil || *act.TimeoutSeconds != 5 {
		t.Fatalf("stop input %+v", act)
	}
}

// TestContainerListFiltersSortAndPages: filters, sort orders and cursors.
func TestContainerListFiltersSortAndPages(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	base := "/api/v1/environments/env-1/containers"
	var page containerPage
	f.get("olga", base, &page)
	if got := names(page.Items); !slices.Equal(got, []string{"db", "shop-db-1", "shop-web-1", "web"}) || *page.Total != 4 {
		t.Fatalf("default order %v", got)
	}
	f.get("olga", base+"?sort=-name&state=running", &page)
	if got := names(page.Items); !slices.Equal(got, []string{"web", "shop-web-1", "shop-db-1"}) {
		t.Fatalf("running, name desc: %v", got)
	}
	f.get("olga", base+"?stack=shop&q=WEB", &page)
	if got := names(page.Items); !slices.Equal(got, []string{"shop-web-1"}) {
		t.Fatalf("stack+q: %v", got)
	}
	f.get("olga", base+"?label=tier%3Dfront-env-1", &page)
	if got := names(page.Items); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("label: %v", got)
	}
	// Pages of two, following the cursor.
	var all []string
	cursor := ""
	for i := 0; i < 5; i++ {
		path := base + "?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page = containerPage{}
		f.get("olga", path, &page)
		all = append(all, names(page.Items)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(all, []string{"db", "shop-db-1", "shop-web-1", "web"}) {
		t.Fatalf("paged %v", all)
	}
	// A cursor of another query is refused.
	f.get("olga", base+"?limit=1", &page)
	if r := f.get("olga", base+"?limit=1&sort=-name&cursor="+page.NextCursor, nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("foreign cursor: %d", r.Status)
	}
	if r := f.get("olga", base+"?sort=bogus", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("bad sort: %d", r.Status)
	}
}

// TestImageVolumeNetworkRoutes: in-use checks, deletion consequences,
// builtin networks, stack-managed volumes and networks, pulls with and
// without a registry connection, tags.
func TestImageVolumeNetworkRoutes(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	env := "/api/v1/environments/env-1"
	nginx := f.imageID("env-1", "nginx:1.27")
	redis := f.imageID("env-1", "redis:7")

	var im struct {
		ID       string         `json:"id"`
		RepoTags []string       `json:"repoTags"`
		UsedBy   []ContainerRef `json:"usedBy"`
		InUse    bool           `json:"inUse"`
		Details  *struct {
			Removal Removal `json:"removal"`
		} `json:"details"`
	}
	f.get("olga", env+"/images/"+nginx, &im)
	if !im.InUse || len(im.UsedBy) != 2 || im.Details == nil || im.Details.Removal.Allowed || im.Details.Removal.Blockers[0].Code != CodeImageInUse {
		t.Fatalf("nginx %+v", im)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/images/" + nginx}); r.Status != http.StatusConflict || code(t, r) != CodeImageInUse {
		t.Fatalf("remove used image: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/images/" + redis}); r.Status != http.StatusConflict {
		t.Fatalf("remove multi-tag image without force: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/images/" + redis + "?force=true"}); r.Status != http.StatusAccepted ||
		f.jobs.last(t).Kind != jobspec.ImageRemove || f.jobs.last(t).Targets[0].ID != redis {
		t.Fatalf("remove image: %d %s", r.Status, r.Body)
	}
	if r := f.get("olga", env+"/images/not-an-id", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid image id: %d", r.Status)
	}
	// Tag (a short request).
	r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/images/" + nginx + "/tags", Body: map[string]any{"repository": "mirror/nginx", "tag": "stable"}})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), "mirror/nginx:stable") {
		t.Fatalf("tag: %d %s", r.Status, r.Body)
	}
	// Pulls: anonymous; an explicit registry connection needs #19.
	r = f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/images/pulls", Body: map[string]any{"reference": "ghcr.io/org/app:2", "platform": "linux/arm64"}})
	if r.Status != http.StatusAccepted {
		t.Fatalf("pull: %d %s", r.Status, r.Body)
	}
	if req := f.jobs.last(t); req.Kind != jobspec.ImagePull || req.Targets[0] != (domain.JobTarget{Type: domain.TargetImage, ID: "ghcr.io/org/app:2"}) {
		t.Fatalf("pull job %+v", req)
	}
	r = f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/images/pulls", Body: map[string]any{"reference": "ghcr.io/org/app:2", "registryConnectionId": "reg-1"}})
	var e errJSON
	_ = json.Unmarshal(r.Body, &e)
	if r.Status != http.StatusUnprocessableEntity || e.Details[0].Field != "body.registryConnectionId" {
		t.Fatalf("registry connection without #19: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/images/pulls", Body: map[string]any{"reference": "Not A Ref"}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("bad reference: %d", r.Status)
	}

	// Volumes.
	var vol struct {
		InUse   bool             `json:"inUse"`
		Stack   *StackMembership `json:"stack"`
		Removal *Removal         `json:"removal"`
	}
	f.get("olga", env+"/volumes/shop_data", &vol)
	if !vol.InUse || vol.Stack == nil || !vol.Stack.Managed || vol.Removal == nil || len(vol.Removal.Blockers) != 2 {
		t.Fatalf("shop_data %+v", vol)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/shop_data"}); code(t, r) != CodeStackManaged {
		t.Fatalf("remove stack volume: %d %s", r.Status, r.Body)
	}
	f.engines["env-1"].AddContainer(engine.ContainerSpec{Name: "user", Image: "nginx:1.27", Mounts: []engine.MountSpec{{Type: "volume", Source: "scratch", Target: "/d"}}}, false)
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/scratch"}); code(t, r) != CodeVolumeInUse {
		t.Fatalf("remove used volume: %d %s", r.Status, r.Body)
	}
	f.engines["env-1"].AddVolume("old", nil)
	f.get("olga", env+"/volumes/old", &vol)
	if vol.Removal == nil || !vol.Removal.Allowed || !strings.Contains(strings.Join(vol.Removal.Consequences, " "), "deleted permanently") {
		t.Fatalf("old volume removal %+v", vol.Removal)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/old"}); r.Status != http.StatusAccepted || f.jobs.last(t).Kind != jobspec.VolumeRemove {
		t.Fatalf("remove volume: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/volumes", Body: map[string]any{"name": "scratch"}}); code(t, r) != CodeResourceNameTaken {
		t.Fatalf("create existing volume: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/volumes", Body: map[string]any{"name": "fresh", "labels": map[string]string{"k": "v"}}}); r.Status != http.StatusAccepted ||
		f.jobs.last(t).Kind != jobspec.VolumeCreate {
		t.Fatalf("create volume: %d %s", r.Status, r.Body)
	}
	var vols struct{ Items []struct{ Name string } }
	f.get("olga", env+"/volumes?inUse=false", &vols)
	if len(vols.Items) != 1 || vols.Items[0].Name != "old" {
		t.Fatalf("unused volumes %+v", vols)
	}

	// Networks.
	for _, c := range []struct{ name, code string }{{"bridge", CodeNetworkBuiltin}, {"shop_default", CodeStackManaged}} {
		if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/networks/" + c.name}); code(t, r) != c.code {
			t.Errorf("remove %s: %d %s, want %s", c.name, r.Status, r.Body, c.code)
		}
	}
	if err := f.engines["env-1"].ConnectNetwork(t.Context(), "spare", "db"); err != nil {
		t.Fatal(err)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/networks/spare"}); code(t, r) != CodeNetworkInUse {
		t.Fatalf("remove used network: %d %s", r.Status, r.Body)
	}
	f.engines["env-1"].AddNetwork("idle", nil)
	if r := f.do("olga", authztest.Call{Method: http.MethodDelete, Path: env + "/networks/idle"}); r.Status != http.StatusAccepted || f.jobs.last(t).Kind != jobspec.NetworkRemove {
		t.Fatalf("remove network: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/networks", Body: map[string]any{"name": "spare"}}); code(t, r) != CodeResourceNameTaken {
		t.Fatalf("create existing network: %d %s", r.Status, r.Body)
	}
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: env + "/networks", Body: map[string]any{"name": "host"}}); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("create predefined name: %d %s", r.Status, r.Body)
	}
}

// TestDockerMutationsNeedTheirOwnCapability: grants per action (#17); job
// requests are authorized by the engine on the target too.
func TestDockerMutationsNeedTheirOwnCapability(t *testing.T) {
	f := newDockerFixture(t, authztest.Only("ivy", "allow container.details.read @env:env-1", "allow container.create @env:env-1",
		"allow volume.read @env:env-1", "allow image.pull @env:env-1"))
	if r := f.do("ivy", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/containers", Body: map[string]any{"name": "n", "image": "nginx:1.27"}}); r.Status != http.StatusAccepted {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	if r := f.do("ivy", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-2/containers", Body: map[string]any{"name": "n", "image": "nginx:1.27"}}); r.Status != http.StatusNotFound {
		t.Fatalf("create on env-2: %d %s", r.Status, r.Body)
	}
	if r := f.do("ivy", authztest.Call{Method: http.MethodDelete, Path: "/api/v1/environments/env-1/volumes/scratch"}); r.Status != http.StatusForbidden {
		t.Fatalf("remove volume with read only: %d", r.Status)
	}
	if r := f.do("ivy", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/volumes", Body: map[string]any{"name": "v"}}); r.Status != http.StatusForbidden {
		t.Fatalf("create volume without volume.create: %d", r.Status)
	}
	if r := f.do("ivy", authztest.Call{Method: http.MethodPost, Path: "/api/v1/environments/env-1/images/pulls", Body: map[string]any{"reference": "alpine:3.22"}}); r.Status != http.StatusAccepted {
		t.Fatalf("pull: %d %s", r.Status, r.Body)
	}
	var page containerPage
	f.get("ivy", "/api/v1/environments/env-1/containers", &page)
	for _, c := range page.Items {
		if c.View != "full" || !slices.Equal(c.Actions, []string{"container.create", "container.details.read"}) {
			t.Fatalf("details view %+v", c)
		}
	}
}

// TestContainerMetricsRoute (#5 storage with #6 identity, #17 metrics-only):
// the metrics of a container are read by its name in the environment,
// with container.metrics.read only; offline environments keep their
// history readable by name; restart-only callers are refused.
func TestContainerMetricsRoute(t *testing.T) {
	pol := authztest.Only("mia", "allow container.metrics.read @container:env-1/web").
		Member("rex", "restarters").Group("restarters", "allow container.restart @container:env-1/web")
	f := newDockerFixture(t, pol)
	var m ContainerMetrics
	r := f.get("mia", "/api/v1/environments/env-1/containers/"+f.containerID("env-1", "web")[:12]+"/metrics?series=cpu.percent,memory.used_bytes", &m)
	if r.Status != http.StatusOK || m.Container != "web" || len(m.Series) != 2 || m.Series[0].Key != "cpu.percent" || m.Series[0].Values[0] != nil {
		t.Fatalf("metrics: %d %s", r.Status, r.Body)
	}
	q := f.observe.queries[len(f.observe.queries)-1]
	if q.Kind != domain.MetricContainer || q.Name != "web" || q.EnvironmentID != "env-1" {
		t.Fatalf("query %+v", q)
	}
	if r := f.get("mia", "/api/v1/environments/env-1/containers/web/metrics?series=bogus", nil); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown series: %d", r.Status)
	}
	for _, c := range []struct {
		user, path string
		status     int
	}{
		{"mia", "/api/v1/environments/env-1/containers/db/metrics", http.StatusNotFound},
		{"mia", "/api/v1/environments/env-2/containers/web/metrics", http.StatusNotFound},
		{"rex", "/api/v1/environments/env-1/containers/web/metrics", http.StatusForbidden},
	} {
		if r := f.get(c.user, c.path, nil); r.Status != c.status {
			t.Errorf("%s %s: %d, want %d", c.user, c.path, r.Status, c.status)
		}
	}
	// Offline: the history stays readable by name; other names stay hidden.
	f.req.offline["env-1"] = true
	if r := f.get("mia", "/api/v1/environments/env-1/containers/web/metrics", &m); r.Status != http.StatusOK || m.Container != "web" {
		t.Fatalf("offline metrics: %d %s", r.Status, r.Body)
	}
	if r := f.get("mia", "/api/v1/environments/env-1/containers/db/metrics", nil); r.Status != http.StatusNotFound {
		t.Fatalf("offline hidden container: %d", r.Status)
	}
}

func TestContainerMetricKeysMatchTheStore(t *testing.T) {
	if !slices.Equal(ContainerMetricKeys, metrics.MetricKeys(domain.MetricContainer)) {
		t.Fatalf("api %v\nstore %v", ContainerMetricKeys, metrics.MetricKeys(domain.MetricContainer))
	}
}

type fakeRegistries struct{ err error }

func (f fakeRegistries) Select(_ context.Context, req domain.RegistrySelectRequest) (domain.RegistrySelection, error) {
	if f.err != nil {
		return domain.RegistrySelection{}, f.err
	}
	if strings.HasPrefix(req.Reference, "ghcr.io/") {
		return domain.RegistrySelection{Reference: req.Reference, Host: "ghcr.io", Selected: &domain.RegistryConnection{ID: "reg-ghcr"}}, nil
	}
	return domain.RegistrySelection{Reference: req.Reference, Host: "docker.io"}, nil
}

// TestPullRegistrySelection (#19): the selected connection's ID (never a
// credential) goes into the pull job; selection failures are stable API
// errors before any job exists.
func TestPullRegistrySelection(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	pulls := "/api/v1/environments/env-1/images/pulls"
	f.svc.SetRegistryResolver(fakeRegistries{})
	if r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: pulls, Body: map[string]any{"reference": "ghcr.io/org/app:1"}}); r.Status != http.StatusAccepted {
		t.Fatalf("pull: %d %s", r.Status, r.Body)
	}
	raw, _ := json.Marshal(f.jobs.last(t).Input)
	if !strings.Contains(string(raw), `"registryConnections":["reg-ghcr"]`) {
		t.Fatalf("pull input %s", raw)
	}
	f.do("olga", authztest.Call{Method: http.MethodPost, Path: pulls, Body: map[string]any{"reference": "alpine:3.22"}})
	if raw, _ := json.Marshal(f.jobs.last(t).Input); strings.Contains(string(raw), "registryConnections") {
		t.Fatalf("anonymous pull input %s", raw)
	}
	n := f.jobs.count()
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{&domain.AmbiguousRegistryError{Host: "ghcr.io", CandidateIDs: []string{"a", "b"}}, http.StatusConflict, CodeAmbiguousRegistryConnection},
		{domain.ErrRegistryConnectionRevoked, http.StatusConflict, CodeRegistryConnectionRevoked},
		{domain.ErrRegistryConnectionNotFound, http.StatusUnprocessableEntity, CodeValidationFailed},
		{domain.ErrRegistryConnectionMismatch, http.StatusUnprocessableEntity, CodeValidationFailed},
	} {
		f.svc.SetRegistryResolver(fakeRegistries{err: c.err})
		r := f.do("olga", authztest.Call{Method: http.MethodPost, Path: pulls, Body: map[string]any{"reference": "ghcr.io/org/app:1", "registryConnectionId": "x"}})
		if r.Status != c.status || code(t, r) != c.code {
			t.Errorf("%v: %d %s", c.err, r.Status, r.Body)
		}
	}
	if f.jobs.count() != n {
		t.Fatal("a refused pull enqueued a job")
	}
}

// TestSelfProtectionRoutes (#32 Done-when 1, API side): on a host running
// DockYard, its containers, images, volumes and network are shown as
// protected with a reason; stopping or removing the agent or manager,
// deleting the manager data or stacks volume, removing DockYard's images
// and network and mounting its volumes fail with 409 protected for the
// owner too; the manager restarts only with confirm: true; nothing is
// enqueued for a refusal.
func TestSelfProtectionRoutes(t *testing.T) {
	f := newDockerFixture(t, authztest.New().Owner("olga"))
	fe := f.engines["env-1"]
	d := fe.Deploy(true)
	g := protect.New(protect.Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks})
	g.SetManager("instance-1", d.ManagerID)
	f.req.agents["env-1"] = agentres.New(agentres.Options{Engine: func() engine.Engine { return fe }, Guard: g,
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	env := "/api/v1/environments/env-1"
	base := env + "/containers"

	var page struct {
		Items []struct {
			Name       string              `json:"name"`
			View       string              `json:"view"`
			Protection *ResourceProtection `json:"protection"`
		} `json:"items"`
	}
	f.get("olga", base, &page)
	roles := map[string]string{}
	for _, c := range page.Items {
		if c.Protection != nil {
			roles[c.Name] = c.Protection.Role
			if c.Protection.Reason == "" {
				t.Errorf("%s without reason", c.Name)
			}
		}
	}
	if len(roles) != 3 || roles["dockyard-dockyard-agent-1"] != "agent" || roles["dockyard-dockyard-manager-1"] != "manager" ||
		roles["dockyard-caddy-1"] != "dockyard_project" {
		t.Fatalf("protected containers %v", roles)
	}
	var agent struct {
		Protection *ResourceProtection `json:"protection"`
		Details    *struct {
			Removal Removal `json:"removal"`
		} `json:"details"`
	}
	f.get("olga", base+"/dockyard-dockyard-agent-1", &agent)
	if agent.Protection == nil || !agent.Protection.Self || agent.Protection.RestartAllowed || agent.Details == nil ||
		agent.Details.Removal.Allowed || agent.Details.Removal.Blockers[0].Code != CodeProtected {
		t.Fatalf("agent %+v", agent)
	}

	n := f.jobs.count()
	for _, c := range []struct {
		call authztest.Call
		code string
	}{
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-dockyard-agent-1/stop"}, CodeProtected},
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-dockyard-agent-1/pause"}, CodeProtected},
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-dockyard-agent-1/restart", Body: map[string]any{"confirm": true}}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: base + "/dockyard-dockyard-agent-1?force=true"}, CodeProtected},
		{authztest.Call{Method: http.MethodPatch, Path: base + "/dockyard-dockyard-agent-1", Body: map[string]any{"restartPolicy": "no"}}, CodeProtected},
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-dockyard-manager-1/stop"}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: base + "/dockyard-dockyard-manager-1?force=true"}, CodeProtected},
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-dockyard-manager-1/restart"}, CodeConfirmationRequired},
		{authztest.Call{Method: http.MethodPost, Path: base + "/dockyard-caddy-1/stop"}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/" + d.ManagerData}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/" + d.Stacks}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: env + "/volumes/" + d.AgentState}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: env + "/images/" + d.ManagerImage + "?force=true"}, CodeProtected},
		{authztest.Call{Method: http.MethodDelete, Path: env + "/networks/" + d.Network}, CodeProtected},
		{authztest.Call{Method: http.MethodPost, Path: base, Body: map[string]any{"name": "thief", "image": "nginx:1.27",
			"mounts": []map[string]any{{"type": "volume", "source": d.ManagerData, "target": "/steal"}}}}, CodeProtected},
	} {
		r := f.do("olga", c.call)
		if r.Status != http.StatusConflict || code(t, r) != c.code {
			t.Errorf("%s: %d %s, want 409 %s", c.call, r.Status, r.Body, c.code)
		}
	}
	if f.jobs.count() != n {
		t.Fatal("a refused operation enqueued a job")
	}
	// Allowed: a confirmed manager restart, starting the agent.
	for _, c := range []authztest.Call{
		{Method: http.MethodPost, Path: base + "/dockyard-dockyard-manager-1/restart", Body: map[string]any{"confirm": true}},
		{Method: http.MethodPost, Path: base + "/dockyard-dockyard-agent-1/start"},
	} {
		if r := f.do("olga", c); r.Status != http.StatusAccepted {
			t.Errorf("%s: %d %s", c, r.Status, r.Body)
		}
	}
	raw := f.jobs.jobs["job-1"].Input
	if !strings.Contains(string(raw), `"confirmed":true`) {
		t.Fatalf("confirmed restart input %s", raw)
	}
	var vol struct {
		Protection *ResourceProtection `json:"protection"`
		Removal    *Removal            `json:"removal"`
	}
	f.get("olga", env+"/volumes/"+d.Stacks, &vol)
	if vol.Protection == nil || vol.Protection.Role != "stacks" || vol.Removal == nil || vol.Removal.Allowed {
		t.Fatalf("stacks volume %+v", vol)
	}
}
