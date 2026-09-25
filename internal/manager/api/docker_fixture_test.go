package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	agentres "github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/authztest"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/observe"
	"github.com/neurekadev/dockyard/internal/manager/resources"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/store/storetest"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// The Docker resource routes (#6) run here against the real manager
// resource service and the real agent handlers over in-memory fake Engines
// (no session: the requester calls the agent handlers directly, mapping
// their error frames like the hub does). Jobs are recorded by a fake engine
// that authorizes like the real one (the kind's capabilities on every
// target). App-level tests with a real agent session: internal/manager/app.

const stacksRoot = "/var/lib/docker/volumes/dockyard_stacks/_data"

// isDockerRoute reports whether a path is a Docker resource route of an
// environment.
func isDockerRoute(path string) bool {
	for _, seg := range []string{"/containers", "/images", "/volumes", "/networks"} {
		if strings.Contains(path, seg) {
			return true
		}
	}
	return false
}

type fakeRequester struct {
	mu      sync.Mutex
	agents  map[string]*agentres.Service
	offline map[string]bool
	drop    map[string]bool // request names the agent does not serve
	calls   []string
}

func (f *fakeRequester) RequestEnvironment(ctx context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	f.mu.Lock()
	a, off := f.agents[env], f.offline[env]
	f.calls = append(f.calls, env+":"+name)
	f.mu.Unlock()
	if a == nil || off {
		return nil, jobs.ErrAgentOffline
	}
	h := a.Requests()[name]
	if h == nil || f.drop[name] {
		return nil, codedErr{protocol.CodeUnsupportedRequest, name + " is not served"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := h(ctx, raw)
	if err != nil {
		var he *session.HandlerError
		if errors.As(err, &he) {
			return nil, codedErr{he.Code, he.Message}
		}
		return nil, err
	}
	return json.Marshal(out)
}

// codedErr is an agent error frame as the hub returns it
// (agents.RequestError; this package cannot import agents).
type codedErr struct{ code, msg string }

func (e codedErr) Error() string           { return "agent: " + e.code + ": " + e.msg }
func (e codedErr) ProtocolCode() string    { return e.code }
func (e codedErr) ProtocolMessage() string { return e.msg }

type fakeJobEngine struct {
	mu   sync.Mutex
	auth authz.Authorizer
	reqs []jobs.Request
	jobs map[string]domain.Job
}

func (f *fakeJobEngine) Enqueue(ctx context.Context, req jobs.Request) (domain.Job, bool, error) {
	spec, ok := jobspec.Lookup(req.Kind)
	if !ok {
		return domain.Job{}, false, domain.ErrJobUnknownKind
	}
	raw, err := json.Marshal(req.Input)
	if err != nil {
		return domain.Job{}, false, err
	}
	if _, err := spec.ComputeLocks(req.EnvironmentID, req.Targets); err != nil {
		return domain.Job{}, false, err
	}
	caps, err := spec.Capabilities(req.Targets, raw)
	if err != nil {
		return domain.Job{}, false, err
	}
	for _, r := range authz.TargetResources(req.EnvironmentID, req.Targets) {
		for _, c := range caps {
			if !f.auth.Can(ctx, req.Principal, c, r).Allowed {
				return domain.Job{}, false, domain.ErrJobForbidden
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	origin := domain.OriginManual
	if req.Principal.Kind == authz.KindAPIToken {
		origin = domain.OriginAPIToken
	}
	j := domain.Job{ID: fmt.Sprintf("job-%d", len(f.reqs)+1), Kind: req.Kind, Executor: domain.ExecutorAgent, Origin: origin,
		InitiatorUserID: req.Principal.UserID, InitiatorTokenID: req.Principal.TokenID, EnvironmentID: req.EnvironmentID,
		Targets: req.Targets, Input: raw, Attempt: 1, State: domain.JobQueued, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	f.reqs = append(f.reqs, req)
	if f.jobs == nil {
		f.jobs = map[string]domain.Job{}
	}
	f.jobs[j.ID] = j
	return j, true, nil
}

func (f *fakeJobEngine) Get(_ context.Context, id string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return domain.Job{}, domain.ErrJobNotFound
	}
	return j, nil
}

func (f *fakeJobEngine) Subscribe(string) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func (f *fakeJobEngine) last(t *testing.T) jobs.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no job was enqueued")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeJobEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// fakeObserve answers container metrics queries from the #5 store's
// shape (one series per requested key, no samples).
type fakeObserve struct {
	mu      sync.Mutex
	queries []domain.MetricQuery
}

func (f *fakeObserve) Inventory(string) (observe.Inventory, bool) { return observe.Inventory{}, false }
func (f *fakeObserve) Host(string) (observe.HostExtra, bool)      { return observe.HostExtra{}, false }
func (f *fakeObserve) Skew(string) time.Duration                  { return 0 }
func (f *fakeObserve) Latest(context.Context, string) (domain.LatestMetrics, bool, error) {
	return domain.LatestMetrics{}, false, nil
}
func (f *fakeObserve) Journal() *observe.Journal { return nil }
func (f *fakeObserve) Query(_ context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	r := domain.MetricResult{From: testutil.Epoch, To: testutil.Epoch.Add(time.Hour), Step: time.Minute, Resolution: "raw"}
	for _, k := range q.Keys {
		r.Series = append(r.Series, domain.MetricSeries{Key: k, Unit: "percent", Values: []*float64{nil}})
	}
	return r, nil
}

type stackIDs map[string]map[string]string

func (s stackIDs) StackIDs(_ context.Context, env string) (map[string]string, error) {
	return s[env], nil
}

// dockerFixture: env-1 ("NAS") and env-2 ("Cloud"), each with its own fake
// Engine holding a container named "web" (different IDs), plus on env-1 a
// DockYard-managed stack "shop" (web + db with volume and network), an
// unused volume and network, and a standalone "db" container.
type dockerFixture struct {
	t       *testing.T
	pol     *authztest.Policy
	engines map[string]*enginefake.Engine
	req     *fakeRequester
	jobs    *fakeJobEngine
	observe *fakeObserve
	svc     *resources.Service
	h       http.Handler
	stacks  stackIDs
	// The stack routes (#7) and container logs/terminals (#8) are served
	// too, so the #17 matrices cover them (authz_matrix_test.go).
	stackSvc *fakeStacks
	io       *fakeContainerIO
}

func newDockerFixture(t *testing.T, pol *authztest.Policy) *dockerFixture {
	t.Helper()
	return newDockerFixtureWith(t, pol, nil)
}

// newDockerFixtureWith lets a test add dependencies (e.g. migrations, #35).
func newDockerFixtureWith(t *testing.T, pol *authztest.Policy, with func(d *Deps)) *dockerFixture {
	t.Helper()
	f := &dockerFixture{t: t, pol: pol, engines: map[string]*enginefake.Engine{}, stacks: stackIDs{"env-1": {}}}
	f.req = &fakeRequester{agents: map[string]*agentres.Service{}, offline: map[string]bool{}}
	for _, env := range []string{"env-1", "env-2"} {
		fe := enginefake.New("ENGINE-" + env)
		fe.AddImage("nginx:1.27")
		fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27", Labels: map[string]string{"tier": "front-" + env},
			Ports: []engine.PortBinding{{ContainerPort: 80, HostPort: 8080}}}, true)
		f.engines[env] = fe
		f.req.agents[env] = agentres.New(agentres.Options{Engine: func() engine.Engine { return fe }, Logger: testutil.Logger(t),
			ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	}
	fe := f.engines["env-1"]
	fe.AddImage("postgres:17")
	fe.AddImage("redis:7", "cache:latest")
	fe.AddVolume("shop_data", map[string]string{protocol.ComposeProjectLabel: "shop"})
	fe.AddVolume("scratch", nil)
	fe.AddNetwork("shop_default", map[string]string{protocol.ComposeProjectLabel: "shop"})
	fe.AddNetwork("spare", nil)
	compose := func(svc string) map[string]string {
		return map[string]string{protocol.ComposeProjectLabel: "shop", protocol.ComposeServiceLabel: svc, protocol.ComposeWorkingDirLabel: stacksRoot + "/shop"}
	}
	fe.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27", NetworkMode: "shop_default", Labels: compose("web")}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "shop-db-1", Image: "postgres:17", NetworkMode: "shop_default", Labels: compose("db"),
		Mounts: []engine.MountSpec{{Type: "volume", Source: "shop_data", Target: "/data"}}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "db", Image: "postgres:17"}, false)

	db := storetest.Migrated(t)
	ctx := testutil.Context(t)
	agentsSvc := newFakeAgents()
	for id, name := range map[string]string{"env-1": "NAS", "env-2": "Cloud"} {
		e := domain.Environment{ID: id, Name: name, EngineID: "ENGINE-" + id, InstallID: "i-" + id, Status: domain.EnvironmentActive,
			Online: true, Revision: 1, CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
		if err := store.InsertEnvironment(ctx, db, &e); err != nil {
			t.Fatal(err)
		}
		agentsSvc.envs[id] = e
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.jobs = &fakeJobEngine{auth: pol}
	f.svc, err = resources.New(resources.Options{DB: db, Keyring: secrets.NewKeyring(key), Agents: f.req, Jobs: f.jobs,
		InstanceID: "instance-1", Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Stacks: f.stacks})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.svc.Close)
	f.observe = &fakeObserve{}
	f.stackSvc = newFakeStacks()
	shop := f.stackSvc.stacks["st-1"]
	shop.ID = "stack-shop"
	delete(f.stackSvc.stacks, "st-1")
	f.stackSvc.stacks[shop.ID] = shop
	f.io = &fakeContainerIO{}
	mux := http.NewServeMux()
	deps := Deps{Agents: agentsSvc, Docker: f.svc, Observe: f.observe, Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{},
		InstanceID: "instance-1", Stacks: f.stackSvc, ContainerIO: f.io}
	if with != nil {
		with(&deps)
	}
	New(mux, deps)
	f.h = authztest.Authenticate(withTestContext(t, mux, ""))
	return f
}

func (f *dockerFixture) do(user string, c authztest.Call) authztest.Response {
	f.t.Helper()
	return authztest.Do(f.t, f.h, user, c)
}

func (f *dockerFixture) get(user, path string, v any) authztest.Response {
	f.t.Helper()
	r := f.do(user, authztest.Call{Method: http.MethodGet, Path: path})
	if v != nil && r.Status == http.StatusOK {
		if err := json.Unmarshal(r.Body, v); err != nil {
			f.t.Fatalf("%s: %v %s", path, err, r.Body)
		}
	}
	return r
}

func (f *dockerFixture) containerID(env, name string) string {
	c, ok := f.engines[env].Container(name)
	if !ok {
		f.t.Fatalf("no container %s on %s", name, env)
	}
	return c.Details.ID
}

func (f *dockerFixture) imageID(env, tag string) string {
	for id, tags := range f.engines[env].Images() {
		for _, t := range tags {
			if t == tag {
				return id
			}
		}
	}
	f.t.Fatalf("no image %s on %s", tag, env)
	return ""
}

// dockerRoutesFor are every implemented Docker route of env-1 (including
// container logs and exec sessions, #8) with the container, the nginx
// image, the scratch volume and the spare network as the objects, with
// valid bodies for the create routes (the schema is checked before
// authorization).
func (f *dockerFixture) dockerRoutesFor(container string) []authztest.Call {
	f.t.Helper()
	params := map[string]string{"environmentId": "env-1", "containerId": container, "imageId": f.imageID("env-1", "nginx:1.27"),
		"volumeId": "scratch", "networkId": "spare", "sessionId": "sess-1"}
	bodies := map[string]any{
		"create-container":   map[string]any{"name": "new", "image": "nginx:1.27"},
		"create-image-pull":  map[string]any{"reference": "alpine:3.22"},
		"create-image-tag":   map[string]any{"repository": "mirror/nginx", "tag": "1.27"},
		"create-volume":      map[string]any{"name": "fresh"},
		"create-network":     map[string]any{"name": "fresh"},
		"create-image-build": sampleBodies["create-image-build"],
	}
	var out []authztest.Call
	for _, c := range authztest.Routes(f.t, params, "/api/v1/environments") {
		if !isDockerRoute(c.Path) {
			continue
		}
		if b, ok := bodies[c.OperationID]; ok {
			c.Body = b
		}
		c.Headers = map[string]string{"Idempotency-Key": "k-" + c.OperationID}
		out = append(out, c)
	}
	if len(out) < 24 {
		f.t.Fatalf("only %d Docker routes", len(out))
	}
	return out
}
