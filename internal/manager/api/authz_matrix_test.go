package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/authz/policy"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// The #17 matrices over every resource route of an environment: the Docker
// routes (#6), the stack routes (#7, #33) and container logs and terminals
// (#8). Each grant scenario (Restricted, metrics-only, restart-only by
// container or by stack, stack.read, logs-only, exec-only) must open
// exactly the routes of its capability, show the resources it reaches
// minimally, and refuse everything else with 403/404 (or an empty page).

// fakeContainerIO serves logs and terminals once authorization passed.
type fakeContainerIO struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeContainerIO) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeContainerIO) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeContainerIO) Logs(_ context.Context, env string, in protocol.ContainerLogsInput) (protocol.ContainerLogsOutput, error) {
	f.record("logs:" + env + "/" + in.ContainerID)
	return protocol.ContainerLogsOutput{Lines: []protocol.LogLine{{At: testutil.Epoch, Stream: "stdout", Data: []byte("hello")}}}, nil
}

type endedFeed struct{}

func (endedFeed) Next(context.Context) (LogEvent, error) { return LogEvent{}, domain.ErrContainerGone }
func (endedFeed) Close()                                 {}

func (f *fakeContainerIO) FollowLogs(_ context.Context, env string, in protocol.ContainerLogsInput) (LogFeed, error) {
	f.record("follow:" + env + "/" + in.ContainerID)
	return endedFeed{}, nil
}

func (f *fakeContainerIO) CreateExec(_ context.Context, req ExecRequest) (ExecSession, error) {
	f.record("exec:" + req.EnvironmentID + "/" + req.Container.ID)
	return ExecSession{ID: "sess-1", Ticket: "ticket", ExpiresAt: testutil.Epoch.Add(time.Minute)}, nil
}

func (f *fakeContainerIO) CheckAttach(_ context.Context, a ExecAttach) error {
	f.record("check:" + a.SessionID)
	return nil
}

// Attach answers like a refused upgrade (the relay itself is #8's test).
func (f *fakeContainerIO) Attach(_ context.Context, w http.ResponseWriter, _ *http.Request, _ ExecAttach) {
	w.WriteHeader(http.StatusUpgradeRequired)
}

func (f *fakeContainerIO) DeleteExec(_ context.Context, a ExecAttach) error {
	f.record("delete:" + a.SessionID)
	return nil
}

var _ ContainerIOService = (*fakeContainerIO)(nil)

// matrixRoutes are the Docker routes of env-1 (including the container
// logs and exec-session routes) plus every stack route of stack-shop.
func (f *dockerFixture) matrixRoutes() []authztest.Call {
	f.t.Helper()
	return f.matrixRoutesFor("web")
}

// matrixRoutesFor is matrixRoutes with another container.
func (f *dockerFixture) matrixRoutesFor(container string) []authztest.Call {
	f.t.Helper()
	calls := append(f.dockerRoutesFor(container), stackRoutesFor(f.t, "stack-shop")...)
	var io int
	for _, c := range calls {
		if strings.Contains(c.Path, "/logs") || strings.Contains(c.Path, "/exec-sessions") {
			io++
		}
	}
	if io != 5 { // get/stream logs, create/stream/delete exec session
		f.t.Fatalf("%d container log/exec routes in the matrix, want 5", io)
	}
	return calls
}

// locateStack adds the stack Locator (#7) to the fixture's resource graph:
// stack-shop lives in env-1 and holds the shop-* containers.
func (f *dockerFixture) locateStack() {
	f.stacks["env-1"]["shop"] = "stack-shop"
	f.pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if ref.Type == catalog.TypeStack {
			if ref.ID == "stack-shop" {
				return policy.Location{Found: true, EnvironmentID: "env-1"}
			}
			return policy.Location{}
		}
		loc, err := f.svc.Locator(ref.Type).Locate(f.t.Context(), ref)
		if err != nil {
			f.t.Error(err)
		}
		return loc
	})
}

func (f *dockerFixture) status(user, method, path string, body any) int {
	f.t.Helper()
	return f.do(user, authztest.Call{Method: method, Path: path, Body: body}).Status
}

// TestLogsAndTerminalsNeedTheirOwnGrants (#8, #17): metrics, restart,
// details and stack grants never open a container's logs or a terminal;
// container.logs.read opens only the logs and container.exec only the
// terminal of the containers it covers.
func TestLogsAndTerminalsNeedTheirOwnGrants(t *testing.T) {
	web := "/api/v1/environments/env-1/containers/web"
	shopWeb := "/api/v1/environments/env-1/containers/shop-web-1"
	io := func(base string) []authztest.Call {
		return []authztest.Call{
			{Method: http.MethodGet, Path: base + "/logs"},
			{Method: http.MethodGet, Path: base + "/logs/stream"},
			{Method: http.MethodPost, Path: base + "/exec-sessions", Body: map[string]any{}},
			{Method: http.MethodGet, Path: base + "/exec-sessions/sess-1/stream"},
			{Method: http.MethodDelete, Path: base + "/exec-sessions/sess-1"},
		}
	}
	for _, rules := range [][]string{
		{"allow container.metrics.read @container:env-1/web", "allow container.metrics.read @stack:stack-shop"},
		{"allow container.restart @container:env-1/web", "allow container.restart @stack:stack-shop"},
		{"allow container.details.read @env:env-1", "allow container.start @env:env-1", "allow container.stop @env:env-1"},
		{"allow stack.read @stack:stack-shop", "allow stack.definition.read @stack:stack-shop", "allow stack.deploy @stack:stack-shop",
			"allow container.restart @stack:stack-shop"},
	} {
		f := newDockerFixture(t, authztest.Only("u", rules...))
		f.locateStack()
		authztest.AssertOnly(t, f.h, "u", nil, append(io(web), io(shopWeb)...))
		if calls := f.io.Calls(); len(calls) != 0 {
			t.Errorf("%v reached the log/exec service: %v", rules, calls)
		}
	}

	// container.logs.read on web: its logs (tail and follow), nothing else.
	f := newDockerFixture(t, authztest.Only("lou", "allow container.logs.read @container:env-1/web"))
	allowed, denied := authztest.Split(f.matrixRoutes(), "container.logs.read")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-containers", "get-container")
	authztest.AssertOnly(t, f.h, "lou", allowed, denied)
	if s := f.status("lou", http.MethodGet, web+"/logs", nil); s != http.StatusOK {
		t.Errorf("logs: %d", s)
	}
	if s := f.status("lou", http.MethodPost, web+"/exec-sessions", map[string]any{}); s != http.StatusForbidden {
		t.Errorf("exec with logs only: %d", s)
	}
	if s := f.status("lou", http.MethodGet, "/api/v1/environments/env-1/containers/db/logs", nil); s != http.StatusNotFound {
		t.Errorf("another container's logs: %d", s)
	}

	// container.exec on web: its terminal, never its logs.
	f = newDockerFixture(t, authztest.Only("eve", "allow container.exec @container:env-1/web"))
	allowed, denied = authztest.Split(f.matrixRoutes(), "container.exec")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-containers", "get-container")
	authztest.AssertOnly(t, f.h, "eve", allowed, denied)
	if s := f.status("eve", http.MethodPost, web+"/exec-sessions", map[string]any{}); s != http.StatusCreated {
		t.Errorf("exec: %d", s)
	}
	if s := f.status("eve", http.MethodGet, web+"/logs", nil); s != http.StatusForbidden {
		t.Errorf("logs with exec only: %d", s)
	}
}

// TestStackReadAndDefinitionAreSeparateGrants (#7, #17): stack.read opens
// the stack's state but neither its definition (revisions with contents,
// bind sources), its jobs, nor its containers' logs, terminals or
// details; stack.definition.read adds exactly the definition routes.
func TestStackReadAndDefinitionAreSeparateGrants(t *testing.T) {
	f := newDockerFixture(t, authztest.Only("sam", "allow stack.read @stack:stack-shop"))
	f.locateStack()
	allowed, denied := authztest.Split(f.matrixRoutes(), "stack.read")
	authztest.AssertOnly(t, f.h, "sam", allowed, denied)
	r := f.do("sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/stack-shop"})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"view":"full"`) {
		t.Fatalf("stack.read get: %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "stack.read view", r.Body, secretBind)

	f = newDockerFixture(t, authztest.Only("dana", "allow stack.read @stack:stack-shop", "allow stack.definition.read @stack:stack-shop"))
	f.locateStack()
	allowed, denied = authztest.Split(f.matrixRoutes(), "stack.read", "stack.definition.read")
	authztest.AssertOnly(t, f.h, "dana", allowed, denied)
	r = f.do("dana", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/stack-shop"})
	if !strings.Contains(string(r.Body), secretBind) {
		t.Errorf("bind sources missing with stack.definition.read: %s", r.Body)
	}
	if calls := f.io.Calls(); len(calls) != 0 {
		t.Errorf("stack grants reached the log/exec service: %v", calls)
	}
}
