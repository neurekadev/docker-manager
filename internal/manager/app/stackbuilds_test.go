package app

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// Stack builds (#33) through the real manager: POST /stacks/{id}/builds
// answers 202 with a stack.build job whose input names registry
// connections by ID only; the audit record carries the capability and the
// service names, never credentials (the database scan at the end covers
// every table and the WAL).

func (e *env) seedStack(id, envID, name string) {
	e.t.Helper()
	now := e.clk.Now().UTC()
	st := domain.Stack{ID: id, EnvironmentID: envID, Name: name, Root: protocol.RootStacks, Dir: name, Origin: domain.StackOriginCreated,
		Status: domain.StackUndeployed, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertStack(testutil.Context(e.t), e.m.DB(), &st); err != nil {
		e.t.Fatal(err)
	}
}

func TestStackBuildThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	t.Cleanup(func() { scanDatabase(t, e) })
	owner, _ := e.setupOwner()
	e.seedEnvironment("e1", "NAS")
	e.seedStack("st-1", "e1", "shop")

	var reg struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{
		"name": "Base images", "host": "registry.example:5000", "username": "robot",
		"secret": e.secrets.New(canary.RegistryCredential, "base image registry password"),
	}).json(t, &reg)

	var job struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	r := owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/stacks/st-1/builds", map[string]any{
		"services": []string{"web"}, "noCache": true, "pull": true, "timeoutSeconds": 5400, "registryIds": []string{reg.ID},
	}, header("Idempotency-Key", "build-1"))
	r.json(t, &job)
	if job.Kind != "stack.build" || r.header.Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatalf("%+v %v", job, r.header)
	}
	j, err := e.m.Jobs().Get(testutil.Context(t), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var in protocol.StackJobInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if !in.NoCache || !in.PullBase || in.BuildTimeoutSeconds != 5400 || !slices.Equal(in.Services, []string{"web"}) ||
		!slices.Equal(in.RegistryConnections, []string{reg.ID}) || in.Stack.ProjectName != "shop" {
		t.Fatalf("input %+v", in)
	}
	// Replays return the same job; unknown connections are refused.
	var again struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/stacks/st-1/builds", map[string]any{
		"services": []string{"web"}, "noCache": true, "pull": true, "timeoutSeconds": 5400, "registryIds": []string{reg.ID},
	}, header("Idempotency-Key", "build-1")).json(t, &again)
	if again.ID != job.ID {
		t.Errorf("replay %s != %s", again.ID, job.ID)
	}
	owner.fail(http.StatusNotFound, "not_found", http.MethodPost, "/api/v1/stacks/st-1/builds",
		map[string]any{"registryIds": []string{"00000000-0000-0000-0000-000000000000"}}, header("Idempotency-Key", "build-2"))

	var found bool
	for _, row := range e.auditRows() {
		if row.Action == "stack.build" && row.Outcome == "success" {
			found = true
			if !strings.Contains(row.Details, "web") || !strings.Contains(row.Targets, "st-1") {
				t.Errorf("audit %+v", row)
			}
		}
	}
	if !found {
		t.Error("no stack.build audit record")
	}
}
