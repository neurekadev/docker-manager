package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/policy"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// The environment and agent routes obey the #17 model with the real
// evaluator (authztest): Restricted sees nothing; metrics-only and
// restart-only grants show only the minimal environment view and nothing
// else; every other route is refused.

func shapingAPI(t *testing.T, pol *authztest.Policy) http.Handler {
	t.Helper()
	svc := newFakeAgents()
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if ref.Type == catalog.TypeAgent {
			if a, ok := svc.agents[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: a.EnvironmentID}
			}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Agents: svc, Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}, Builds: emptyBuilds{}})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

func agentRoutes(t *testing.T) []authztest.Call {
	all := authztest.Routes(t, map[string]string{"environmentId": "env-1", "agentId": "ag-1", "enrollmentId": "x"},
		"/api/v1/environments", "/api/v1/agents", "/api/v1/agent-enrollments")
	// The Docker resource routes of an environment have their own matrices
	// (docker_authz_test.go, with a Docker service).
	var calls []authztest.Call
	for _, c := range all {
		if !isDockerRoute(c.Path) {
			calls = append(calls, c)
		}
	}
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": "*", "Idempotency-Key": "k-" + calls[i].OperationID}
		if b, ok := sampleBodies[calls[i].OperationID]; ok {
			calls[i].Body = b
		}
	}
	if len(calls) < 14 {
		t.Fatalf("only %d environment/agent routes", len(calls))
	}
	return calls
}

type shapedEnv struct {
	ID       string   `json:"id"`
	View     string   `json:"view"`
	Actions  []string `json:"actions"`
	EngineID string   `json:"engineId"`
	Revision int64    `json:"revision"`
}

func getEnvs(t *testing.T, h http.Handler, user string) []shapedEnv {
	t.Helper()
	r := authztest.Do(t, h, user, authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments"})
	var page struct {
		Items []shapedEnv `json:"items"`
	}
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &page) != nil {
		t.Fatalf("list: %d %s", r.Status, r.Body)
	}
	return page.Items
}

func TestRestrictedSeesNoEnvironmentsOrAgents(t *testing.T) {
	h := shapingAPI(t, authztest.New().Member("rita", "restricted"))
	authztest.AssertOnly(t, h, "rita", nil, agentRoutes(t))
	if envs := getEnvs(t, h, "rita"); len(envs) != 0 {
		t.Fatalf("restricted user sees %+v", envs)
	}
}

func TestMetricsOnlyShowsTheMinimalEnvironment(t *testing.T) {
	h := shapingAPI(t, authztest.Only("mia", "allow environment.metrics.read @env:env-1"))
	allowed, denied := authztest.Split(agentRoutes(t), "environment.metrics.read")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-environments", "get-environment")
	authztest.AssertOnly(t, h, "mia", allowed, denied)

	envs := getEnvs(t, h, "mia")
	if len(envs) != 1 || envs[0].ID != "env-1" || envs[0].View != "minimal" || !slices.Equal(envs[0].Actions, []string{"environment.metrics.read"}) ||
		envs[0].EngineID != "" || envs[0].Revision != 0 {
		t.Fatalf("metrics-only list %+v", envs)
	}
	r := authztest.Do(t, h, "mia", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1"})
	if r.Status != http.StatusOK || r.Header.Get("ETag") != "" {
		t.Fatalf("get: %d %v", r.Status, r.Header)
	}
	authztest.AssertAbsent(t, "minimal environment", r.Body, "ENG", "ag-1", `"revision"`, `"createdAt"`)
	for _, p := range []string{"/api/v1/environments/env-secret", "/api/v1/agents/ag-1"} {
		if r := authztest.Do(t, h, "mia", authztest.Call{Method: http.MethodGet, Path: p}); r.Status != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
}

func TestRestartOnlyShowsOnlyTheEnvironmentIdentity(t *testing.T) {
	h := shapingAPI(t, authztest.Only("rex", "allow container.restart @container:env-1/web"))
	allowed, denied := authztest.Split(agentRoutes(t), "container.restart")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-environments", "get-environment")
	authztest.AssertOnly(t, h, "rex", allowed, denied)
	envs := getEnvs(t, h, "rex")
	if len(envs) != 1 || envs[0].View != "minimal" || len(envs[0].Actions) != 0 || envs[0].EngineID != "" {
		t.Fatalf("restart-only list %+v", envs)
	}
}

func TestEditGrantShowsTheRevision(t *testing.T) {
	h := shapingAPI(t, authztest.Only("max", "allow environment.manage @env:env-1", "allow agent.read @agent:ag-1"))
	r := authztest.Do(t, h, "max", authztest.Call{Method: http.MethodGet, Path: "/api/v1/environments/env-1"})
	if r.Status != http.StatusOK || r.Header.Get("ETag") != `"5"` {
		t.Fatalf("get: %d %v %s", r.Status, r.Header, r.Body)
	}
	r = authztest.Do(t, h, "max", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/environments/env-1",
		Body: map[string]string{"name": "Renamed"}, Headers: map[string]string{"If-Match": `"5"`}})
	if r.Status != http.StatusOK {
		t.Fatalf("patch: %d %s", r.Status, r.Body)
	}
	// agent.read on one agent: that agent in full, its environment only
	// minimally (reached through the agent), no other agent.
	r = authztest.Do(t, h, "max", authztest.Call{Method: http.MethodGet, Path: "/api/v1/agents"})
	var page struct {
		Items []struct {
			ID, View, EngineID string
		} `json:"items"`
	}
	_ = json.Unmarshal(r.Body, &page)
	if len(page.Items) != 1 || page.Items[0].ID != "ag-1" || page.Items[0].View != "full" || page.Items[0].EngineID != "ENG" {
		t.Fatalf("agents %s", r.Body)
	}
	if r := authztest.Do(t, h, "max", authztest.Call{Method: http.MethodDelete, Path: "/api/v1/agents/ag-1",
		Headers: map[string]string{"If-Match": `"3"`}}); r.Status != http.StatusForbidden {
		t.Fatalf("delete agent with agent.read: %d", r.Status)
	}
}

// TestAuthztestHelpers pins the helper contract feature workstreams rely on.
func TestAuthztestHelpers(t *testing.T) {
	calls := authztest.Routes(t, map[string]string{"jobId": "j1"}, "/api/v1/jobs")
	if len(calls) != 4 {
		t.Fatalf("job routes %v", calls)
	}
	allowed, denied := authztest.Split(calls, "job.cancel")
	if len(allowed) != 1 || allowed[0].OperationID != "create-job-cancellation" || len(denied) != 3 {
		t.Fatalf("split %v / %v", allowed, denied)
	}
	allowed, denied = authztest.Discoverable(allowed, denied, "get-job")
	if len(allowed) != 2 || len(denied) != 2 {
		t.Fatalf("discoverable %v / %v", allowed, denied)
	}
	if !authztest.EmptyPage([]byte(`{"items":[]}`)) || authztest.EmptyPage([]byte(`{"items":[1]}`)) || authztest.EmptyPage([]byte(`{}`)) {
		t.Fatal("EmptyPage")
	}
	// Tokens: scope ∩ user grants.
	pol := authztest.Only("tia", "allow container.restart @all").Token("t1", "tia", "allow container.restart @container:e1/web")
	tok := authz.Principal{Kind: authz.KindAPIToken, UserID: "tia", TokenID: "t1"}
	web := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	db := web
	db.ID = "db"
	if !pol.Can(t.Context(), tok, "container.restart", web).Allowed || pol.Can(t.Context(), tok, "container.restart", db).Allowed {
		t.Fatal("token scope")
	}
	if !pol.Owner("tia").Can(t.Context(), authz.Principal{Kind: authz.KindUser, UserID: "tia"}, "container.exec", db).Allowed {
		t.Fatal("owner bypass")
	}
}
