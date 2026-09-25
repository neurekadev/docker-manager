package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// fakeAgents is an in-memory AgentService for the HTTP mapping tests (the
// real service is tested in internal/manager/agents).
type fakeAgents struct {
	mu      sync.Mutex
	enrolls []domain.Enrollment
	agents  map[string]domain.Agent
	envs    map[string]domain.Environment
	specs   []domain.EnrollmentSpec
}

func newFakeAgents() *fakeAgents {
	now := testutil.Epoch
	caps := `{"agentVersion":"1.4.0","protocols":["dockyard.agent/v1"],"os":"linux","arch":"amd64",` +
		`"engine":{"id":"ENG","version":"28.5.2","apiVersion":"1.51","os":"linux","arch":"amd64"},"commands":["stack.deploy"],` +
		`"requests":[],"streams":[],"roots":[{"kind":"stacks","path":"/var/lib/docker/volumes/dockyard_stacks/_data","watch":"inotify"}],` +
		`"transport":{"managerUrl":"http://dockyard-manager:8080","plainHttp":true,"customCa":false},` +
		`"diagnostics":[{"area":"storage","code":"storage_path_mismatch","message":"m","path":"/srv"}]}`
	return &fakeAgents{
		agents: map[string]domain.Agent{
			"ag-1": {ID: "ag-1", EnvironmentID: "env-1", Status: domain.AgentActive, Version: "1.4.0", VersionStatus: "current",
				EngineID: "ENG", InstallID: "inst", Revision: 3, CreatedAt: now, UpdatedAt: now, Capabilities: caps, SessionID: "s1"},
			"ag-secret": {ID: "ag-secret", EnvironmentID: "env-secret", Status: domain.AgentActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		},
		envs: map[string]domain.Environment{
			"env-1":      {ID: "env-1", Name: "NAS", EngineID: "ENG", AgentID: "ag-1", Status: domain.EnvironmentActive, Online: true, Revision: 5, CreatedAt: now, UpdatedAt: now},
			"env-secret": {ID: "env-secret", Name: "Hidden", AgentID: "ag-secret", Status: domain.EnvironmentActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		},
	}
}

func (f *fakeAgents) CreateEnrollment(_ context.Context, spec domain.EnrollmentSpec) (domain.CreatedEnrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if spec.EnvironmentName == "bad name" {
		return domain.CreatedEnrollment{}, &domain.InputError{Field: "environmentName", Message: "must not contain spaces"}
	}
	f.specs = append(f.specs, spec)
	e := domain.Enrollment{ID: "0190a6e0-0000-7000-8000-00000000000" + string(rune('0'+len(f.enrolls))), Intent: spec.Intent, TargetID: spec.TargetID,
		EnvironmentName: spec.EnvironmentName, CreatedAt: testutil.Epoch, ExpiresAt: testutil.Epoch.Add(time.Hour), CreatedBy: spec.CreatedBy}
	f.enrolls = append([]domain.Enrollment{e}, f.enrolls...)
	return domain.CreatedEnrollment{Enrollment: e, Token: "dye_" + e.ID + "_secret", ManagerURL: "https://docker.example.com",
		Install: []domain.InstallCommand{{Variant: "remote", Title: "t", Description: "d", Command: "cmd dye_" + e.ID + "_secret"}}}, nil
}

func (f *fakeAgents) ListEnrollments(_ context.Context, before string, limit int) ([]domain.Enrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Enrollment
	for _, e := range f.enrolls {
		if (before == "" || e.ID < before) && (limit == 0 || len(out) < limit) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeAgents) GetEnrollment(_ context.Context, id string) (domain.Enrollment, error) {
	for _, e := range f.enrolls {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.Enrollment{}, domain.ErrEnrollmentNotFound
}

func (f *fakeAgents) RevokeEnrollment(ctx context.Context, id string) (domain.Enrollment, error) {
	e, err := f.GetEnrollment(ctx, id)
	if err == nil {
		now := testutil.Epoch
		e.RevokedAt = &now
		f.enrolls[0] = e
	}
	return e, err
}

func (f *fakeAgents) ListAgents(_ context.Context, flt domain.AgentFilter) ([]domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Agent
	for _, id := range []string{"ag-secret", "ag-1"} { // newest first
		a := f.agents[id]
		if flt.EnvironmentID != "" && a.EnvironmentID != flt.EnvironmentID {
			continue
		}
		if flt.BeforeID != "" && a.ID >= flt.BeforeID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeAgents) GetAgent(_ context.Context, id string) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return a, domain.ErrAgentNotFound
	}
	return a, nil
}

func (f *fakeAgents) UpdateAgentLabel(_ context.Context, id string, rev int64, label string) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	if a.Revision != rev {
		return a, domain.ErrRevisionMismatch
	}
	a.Label, a.Revision = label, a.Revision+1
	f.agents[id] = a
	return a, nil
}

func (f *fakeAgents) RemoveAgent(_ context.Context, id string, rev int64) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	a.Status, a.Revision = domain.AgentRevoked, a.Revision+1
	f.agents[id] = a
	return a, nil
}

func (f *fakeAgents) RotateCredential(_ context.Context, id string) (domain.CredentialRotation, error) {
	if f.agents[id].Status != domain.AgentActive {
		return domain.CredentialRotation{}, domain.ErrAgentRevoked
	}
	return domain.CredentialRotation{AgentID: id, State: domain.RotationPending, RequestedAt: testutil.Epoch}, nil
}

func (f *fakeAgents) ListEnvironments(_ context.Context, flt domain.EnvironmentFilter) ([]domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Environment
	for _, id := range slices.Sorted(maps.Keys(f.envs)) {
		e := f.envs[id]
		if id > flt.AfterID {
			for _, s := range flt.Statuses {
				if e.Status == s {
					out = append(out, e)
				}
			}
		}
	}
	return out, nil
}

func (f *fakeAgents) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.envs[id]
	if !ok {
		return e, domain.ErrEnvironmentNotFound
	}
	return e, nil
}

func (f *fakeAgents) UpdateEnvironment(_ context.Context, id string, rev int64, p domain.EnvironmentPatch) (domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.envs[id]
	if e.Status == domain.EnvironmentArchived {
		return e, domain.ErrEnvironmentArchived
	}
	if e.Revision != rev {
		return e, domain.ErrRevisionMismatch
	}
	if p.Name != nil {
		if *p.Name == "" {
			return e, &domain.InputError{Field: "name", Message: "must not be empty"}
		}
		e.Name = *p.Name
	}
	if p.ServiceAddress != nil {
		e.ServiceAddress = *p.ServiceAddress
	}
	e.Revision++
	f.envs[id] = e
	return e, nil
}

func (f *fakeAgents) ArchiveEnvironment(_ context.Context, id string, rev int64) (domain.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.envs[id]
	e.Status, e.Revision = domain.EnvironmentArchived, e.Revision+1
	f.envs[id] = e
	return e, nil
}

func (f *fakeAgents) EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error) {
	e, err := f.GetEnvironment(ctx, id)
	if err != nil {
		return domain.EnvironmentSystem{}, err
	}
	a, _ := f.GetAgent(ctx, e.AgentID)
	return domain.EnvironmentSystem{Environment: e, Agent: &a}, nil
}

// agentsAuthz grants everything except: user "eve" cannot see env-secret or
// ag-secret, and "reader" can read but not change anything.
type agentsAuthz struct{}

func (agentsAuthz) Can(_ context.Context, p authz.Principal, c string, r authz.Resource) authz.Decision {
	if p.UserID == "eve" && (r.EnvironmentID == "env-secret" || r.ID == "ag-secret") {
		return authz.Deny("hidden")
	}
	if p.UserID == "reader" && !strings.HasSuffix(c, ".read") {
		return authz.Deny("read only")
	}
	return authz.Allow("test")
}

func newAgentsAPI(t *testing.T, az authz.Authorizer) (http.Handler, *fakeAgents) {
	t.Helper()
	svc := newFakeAgents()
	mux := http.NewServeMux()
	New(mux, Deps{Agents: svc, Authorizer: az, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}})
	return withTestContext(t, mux, ""), svc
}

func call(h http.Handler, method, target, user, body string, hdr ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAgentRoutesFailClosed(t *testing.T) {
	h, _ := newAgentsAPI(t, nil) // DenyAll until #17
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/agent-enrollments"}, {"POST", "/api/v1/agent-enrollments"}, {"DELETE", "/api/v1/agent-enrollments/x"},
		{"GET", "/api/v1/agents"}, {"GET", "/api/v1/agents/ag-1"}, {"PATCH", "/api/v1/agents/ag-1"}, {"DELETE", "/api/v1/agents/ag-1"},
		{"POST", "/api/v1/agents/ag-1/credential-rotations"}, {"GET", "/api/v1/environments"}, {"GET", "/api/v1/environments/env-1"},
		{"PATCH", "/api/v1/environments/env-1"}, {"DELETE", "/api/v1/environments/env-1"}, {"GET", "/api/v1/environments/env-1/agents"},
		{"GET", "/api/v1/environments/env-1/system"},
	} {
		body := ""
		if r.method == "POST" || r.method == "PATCH" {
			body = "{}"
		}
		if rec := call(h, r.method, r.path, "", body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: %d", r.method, r.path, rec.Code)
		}
		rec := call(h, r.method, r.path, "alice", body, "If-Match", `"3"`)
		if r.path == "/api/v1/agents" || r.path == "/api/v1/environments" {
			// List routes filter per item instead of failing.
			if rec.Code != http.StatusOK || rec.Body.String() != `{"items":[]}`+"\n" && rec.Body.String() != `{"items":[]}` {
				t.Errorf("%s %s without grants: %d %s", r.method, r.path, rec.Code, rec.Body)
			}
			continue
		}
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s without grants: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

func TestEnrollmentRoutes(t *testing.T) {
	h, svc := newAgentsAPI(t, agentsAuthz{})
	rec := call(h, "POST", "/api/v1/agent-enrollments", "alice", `{"environmentName":"NAS","expiresInSeconds":600}`, "Idempotency-Key", "k1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created CreatedAgentEnrollment
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !strings.HasPrefix(created.Token, "dye_") || created.Enrollment.State != "pending" || created.Enrollment.Intent != "new" ||
		len(created.InstallCommands) != 1 || created.ManagerURL != "https://docker.example.com" {
		t.Fatalf("created %+v", created)
	}
	if s := svc.specs[0]; s.TTL != 10*time.Minute || s.CreatedBy != "alice" || s.EnvironmentName != "NAS" || s.Intent != domain.IntentNew {
		t.Fatalf("spec %+v", s)
	}
	// Idempotent replay returns the same token without a second enrollment.
	again := call(h, "POST", "/api/v1/agent-enrollments", "alice", `{"environmentName":"NAS","expiresInSeconds":600}`, "Idempotency-Key", "k1")
	if again.Code != http.StatusCreated || again.Body.String() != rec.Body.String() || again.Header().Get(HeaderIdempotentReplayed) != "true" || len(svc.specs) != 1 {
		t.Fatalf("replay %d %v", again.Code, again.Header())
	}
	// Intents and validation.
	rec = call(h, "POST", "/api/v1/agent-enrollments", "alice", `{"intent":"replace:0190a6e0-0000-7000-8000-000000000001"}`)
	if rec.Code != http.StatusCreated || svc.specs[1].Intent != domain.IntentReplace || svc.specs[1].TargetID != "0190a6e0-0000-7000-8000-000000000001" {
		t.Fatalf("replace intent: %d %+v", rec.Code, svc.specs)
	}
	for _, body := range []string{`{"intent":"replace:agent"}`, `{"intent":"adopt"}`, `{"expiresInSeconds":5}`, `{"environmentName":"bad name"}`, `{"token":"x"}`} {
		if rec := call(h, "POST", "/api/v1/agent-enrollments", "alice", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// List never returns tokens.
	rec = call(h, "GET", "/api/v1/agent-enrollments?limit=1", "alice", "")
	var page Page[AgentEnrollment]
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" || strings.Contains(rec.Body.String(), "dye_") {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/api/v1/agent-enrollments?cursor="+page.NextCursor, "alice", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), created.Enrollment.ID) {
		t.Fatalf("page 2 %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/api/v1/agent-enrollments/"+created.Enrollment.ID, "alice", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/api/v1/agent-enrollments/nope", "alice", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown %d", rec.Code)
	}
	if rec := call(h, "POST", "/api/v1/agent-enrollments", "reader", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("reader creates: %d", rec.Code)
	}
}

func TestAgentAndEnvironmentRoutes(t *testing.T) {
	h, _ := newAgentsAPI(t, agentsAuthz{})
	// Lists are filtered per item.
	rec := call(h, "GET", "/api/v1/agents", "eve", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "ag-secret") || !strings.Contains(rec.Body.String(), `"id":"ag-1"`) {
		t.Fatalf("agents for eve: %s", rec.Body)
	}
	if rec := call(h, "GET", "/api/v1/agents/ag-secret", "eve", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden agent: %d", rec.Code)
	}
	rec = call(h, "GET", "/api/v1/agents/ag-1", "alice", "")
	var a Agent
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"3"` || !a.Connected || a.Transport == nil || !a.Transport.PlainHTTP || a.EngineID != "ENG" {
		t.Fatalf("agent %d %+v", rec.Code, a)
	}
	if rec := call(h, "PATCH", "/api/v1/agents/ag-1", "alice", `{"label":"rack 2"}`); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("patch without If-Match: %d", rec.Code)
	}
	if rec := call(h, "PATCH", "/api/v1/agents/ag-1", "alice", `{"label":"rack 2"}`, "If-Match", `"2"`); rec.Code != http.StatusPreconditionFailed || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("stale patch: %d %v", rec.Code, rec.Header())
	}
	rec = call(h, "PATCH", "/api/v1/agents/ag-1", "alice", `{"label":"rack 2"}`, "If-Match", `"3"`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` || !strings.Contains(rec.Body.String(), `"label":"rack 2"`) {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PATCH", "/api/v1/agents/ag-1", "reader", `{"label":"x"}`, "If-Match", `"4"`); rec.Code != http.StatusForbidden {
		t.Fatalf("reader patch: %d", rec.Code)
	}
	if rec := call(h, "POST", "/api/v1/agents/ag-1/credential-rotations", "alice", ""); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"state":"pending"`) {
		t.Fatalf("rotation: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/api/v1/agents/ag-1", "alice", "", "If-Match", `"4"`); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/api/v1/agents/ag-1/credential-rotations", "alice", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeAgentRevoked) {
		t.Fatalf("rotate revoked: %d %s", rec.Code, rec.Body)
	}

	// Environments.
	rec = call(h, "GET", "/api/v1/environments", "eve", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "env-secret") || !strings.Contains(rec.Body.String(), `"name":"NAS"`) {
		t.Fatalf("environments for eve: %s", rec.Body)
	}
	if rec := call(h, "GET", "/api/v1/environments?status=gone", "alice", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	rec = call(h, "PATCH", "/api/v1/environments/env-1", "alice", `{"name":"Prod","serviceAddress":"nas.lan"}`, "If-Match", `"5"`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"6"` || !strings.Contains(rec.Body.String(), `"serviceAddress":"nas.lan"`) {
		t.Fatalf("patch env: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PATCH", "/api/v1/environments/env-1", "alice", `{"name":""}`, "If-Match", `"6"`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty name: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, "GET", "/api/v1/environments/env-1/system", "alice", "")
	var sys EnvironmentSystem
	_ = json.Unmarshal(rec.Body.Bytes(), &sys)
	if rec.Code != 200 || sys.Engine == nil || sys.Engine.APIVersion != "1.51" || len(sys.Roots) != 1 || len(sys.Diagnostics) != 1 ||
		strings.Contains(rec.Body.String(), "/var/lib/docker") || strings.Contains(rec.Body.String(), `"/srv"`) {
		t.Fatalf("system %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/api/v1/environments/env-secret/system", "eve", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("hidden system: %d", rec.Code)
	}
	if rec := call(h, "GET", "/api/v1/environments/env-1/agents", "alice", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"ag-1"`) || strings.Contains(rec.Body.String(), "ag-secret") {
		t.Fatalf("environment agents: %s", rec.Body)
	}
	if rec := call(h, "DELETE", "/api/v1/environments/env-1", "alice", "", "If-Match", `"6"`); rec.Code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PATCH", "/api/v1/environments/env-1", "alice", `{"name":"x"}`, "If-Match", `"7"`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeEnvironmentArchived) {
		t.Fatalf("edit archived: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/api/v1/environments?status=archived", "alice", ""); !strings.Contains(rec.Body.String(), `"status":"archived"`) {
		t.Fatalf("archived list: %s", rec.Body)
	}
}

// TestAgentVersionCompatibilityFlags: environments, agents and system
// information flag outdated and unsupported agents against the running
// manager's version, with upgrade instructions (#34).
func TestAgentVersionCompatibilityFlags(t *testing.T) {
	for _, c := range []struct {
		agentVersion, want string
		instructions       bool
	}{
		{"1.4.1", "current", false},
		{"1.3.9", "outdated", true},
		{"1.2.0", "unsupported", true},
		{"1.5.0", "unsupported", true},
	} {
		svc := newFakeAgents()
		a := svc.agents["ag-1"]
		a.Version = c.agentVersion
		svc.agents["ag-1"] = a
		mux := http.NewServeMux()
		New(mux, Deps{Agents: svc, Authorizer: agentsAuthz{}, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{},
			Build: buildinfo.Info{Version: "1.4.0"}})
		h := withTestContext(t, mux, "")

		var env Environment
		rec := call(h, "GET", "/api/v1/environments/env-1", "alice", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if rec.Code != 200 || env.AgentVersion != c.agentVersion || env.Compatibility != c.want || (env.UpgradeInstructions != "") != c.instructions {
			t.Errorf("%s: environment %d %+v", c.agentVersion, rec.Code, env)
		}
		var page Page[Environment]
		rec = call(h, "GET", "/api/v1/environments", "alice", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		found := false
		for _, e := range page.Items {
			if e.ID == "env-1" {
				found = e.Compatibility == c.want && e.AgentVersion == c.agentVersion
			}
		}
		if !found {
			t.Errorf("%s: environment list %s", c.agentVersion, rec.Body)
		}
		var ag Agent
		rec = call(h, "GET", "/api/v1/agents/ag-1", "alice", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &ag)
		if rec.Code != 200 || ag.Compatibility != c.want || (ag.UpgradeInstructions != "") != c.instructions {
			t.Errorf("%s: agent %d %+v", c.agentVersion, rec.Code, ag)
		}
		var sys EnvironmentSystem
		rec = call(h, "GET", "/api/v1/environments/env-1/system", "alice", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &sys)
		if rec.Code != 200 || sys.Agent == nil || sys.Agent.Compatibility != c.want {
			t.Errorf("%s: system %d %s", c.agentVersion, rec.Code, rec.Body)
		}
		if c.want == "unsupported" && !strings.Contains(env.UpgradeInstructions, "upgrade") {
			t.Errorf("%s: instructions %q", c.agentVersion, env.UpgradeInstructions)
		}
	}
}
