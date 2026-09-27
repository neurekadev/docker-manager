package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/policy"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeStacks is an in-memory StackService for the HTTP mapping and
// shaping tests (the real service is tested in internal/manager/stacks).
type fakeStacks struct {
	mu      sync.Mutex
	stacks  map[string]domain.Stack
	err     error
	jobs    []domain.JobKind
	online  bool
	patches int
	builds  []domain.StackBuildOptions
	// renames are the requested new names; renamePlan the preview.
	renames    []string
	renamePlan domain.StackRenamePlan
}

const secretBind = "/srv/secret-bind-path"

func newFakeStacks() *fakeStacks {
	now := testutil.Epoch
	applied := &domain.RevisionRef{ID: "rev-2", Seq: 2, Hash: strings.Repeat("a", 64)}
	return &fakeStacks{online: true, stacks: map[string]domain.Stack{
		"st-1": {ID: "st-1", EnvironmentID: "env-1", Name: "shop", DisplayName: "Shop", Meta: domain.DisplayMeta{Description: "orders"},
			ServiceMeta: map[string]domain.DisplayMeta{"web": {Icon: "globe"}}, Root: "stacks", Dir: "shop", Origin: "created",
			Status: domain.StackDeployed, Applied: applied, AppliedAt: &now, Observed: &domain.RevisionRef{ID: "rev-3", Seq: 3, Hash: strings.Repeat("b", 64)},
			Services:    []domain.StackServiceDef{{Name: "web", Image: "nginx:1.27", DependsOn: []domain.StackDependency{{Service: "db", Condition: "service_started", Required: true}}}},
			Images:      []domain.StackImage{{Service: "web", Image: "nginx:1.27", ImageID: "sha256:img", Digest: "sha256:dig"}},
			Binds:       []domain.StackBind{{Service: "web", Source: secretBind, Target: "/data", External: true}},
			EngineState: domain.EngineStateRunning, Revision: 4, CreatedAt: now, UpdatedAt: now},
		"st-secret": {ID: "st-secret", EnvironmentID: "env-1", Name: "hidden", Status: domain.StackUndeployed, Revision: 1, CreatedAt: now, UpdatedAt: now},
	}}
}

func (f *fakeStacks) List(_ context.Context, flt domain.StackFilter) ([]domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Stack
	for _, id := range slices.Sorted(maps.Keys(f.stacks)) {
		st := f.stacks[id]
		if id > flt.AfterID && (flt.EnvironmentID == "" || st.EnvironmentID == flt.EnvironmentID) {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeStacks) Get(_ context.Context, id string) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.stacks[id]
	if !ok {
		return st, domain.ErrStackNotFound
	}
	return st, nil
}

func (f *fakeStacks) Online(context.Context, string) bool { return f.online }

func (f *fakeStacks) Create(_ context.Context, _ authz.Principal, r domain.StackCreate) (domain.Stack, domain.StackValidation, error) {
	if f.err != nil {
		return domain.Stack{}, domain.StackValidation{}, f.err
	}
	st := domain.Stack{ID: "st-new", EnvironmentID: r.EnvironmentID, Name: r.Name, Status: domain.StackUndeployed, Revision: 1}
	return st, domain.StackValidation{Valid: true, ProjectName: r.Name}, nil
}

func (f *fakeStacks) CreateFromTemplate(_ context.Context, _ authz.Principal, r domain.StackFromTemplate) (domain.Stack, domain.StackValidation, error) {
	if f.err != nil {
		return domain.Stack{}, domain.StackValidation{}, f.err
	}
	if r.TemplateID == "missing" {
		return domain.Stack{}, domain.StackValidation{}, domain.ErrTemplateNotFound
	}
	st := domain.Stack{ID: "st-tpl", EnvironmentID: r.EnvironmentID, Name: r.Name, Status: domain.StackUndeployed, Revision: 1,
		Template: &domain.StackTemplateRef{InstanceID: r.InstanceID, TemplateID: r.TemplateID, Name: "Template", Version: r.Version, VersionLabel: "1.0.0"}}
	return st, domain.StackValidation{Valid: true, ProjectName: r.Name}, nil
}

func (f *fakeStacks) Validate(_ context.Context, d domain.StackDefinition) (domain.StackValidation, error) {
	return domain.StackValidation{Valid: true, ProjectName: d.Name, Warnings: []domain.StackIssue{{Code: "obsolete_version", Message: "version"}}}, f.err
}

func (f *fakeStacks) Update(_ context.Context, id string, rev int64, p domain.StackPatch) (domain.Stack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.stacks[id]
	if st.Revision != rev {
		return st, domain.ErrStackRevisionStale
	}
	if p.DisplayName != nil {
		st.DisplayName = *p.DisplayName
	}
	st.Revision++
	f.stacks[id] = st
	f.patches++
	return st, nil
}

func (f *fakeStacks) job(kind domain.JobKind, st domain.Stack) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Job{}, f.err
	}
	f.jobs = append(f.jobs, kind)
	return domain.Job{ID: "job-1", Kind: kind, State: domain.JobQueued, EnvironmentID: st.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}, Attempt: 1}, nil
}

func (f *fakeStacks) Delete(_ context.Context, _ authz.Principal, st domain.Stack, _ domain.StackJobRequest, _ domain.StackRemoveOptions) (domain.Job, error) {
	return f.job("stack.remove", st)
}

func (f *fakeStacks) Deploy(_ context.Context, _ authz.Principal, st domain.Stack, _ domain.StackJobRequest, _ domain.StackDeployOptions) (domain.Job, error) {
	return f.job("stack.deploy", st)
}

func (f *fakeStacks) Build(_ context.Context, _ authz.Principal, st domain.Stack, _ domain.StackJobRequest, o domain.StackBuildOptions) (domain.Job, error) {
	f.mu.Lock()
	f.builds = append(f.builds, o)
	f.mu.Unlock()
	return f.job("stack.build", st)
}

func (f *fakeStacks) Operate(_ context.Context, _ authz.Principal, st domain.Stack, action string, _ domain.StackJobRequest) (domain.Job, error) {
	return f.job(domain.JobKind("stack."+action), st)
}

func (f *fakeStacks) Restore(_ context.Context, _ authz.Principal, st domain.Stack, revisionID string) (domain.StackRestore, error) {
	if f.err != nil {
		return domain.StackRestore{}, f.err
	}
	return domain.StackRestore{Stack: st, Revision: domain.StackRevision{ID: "rev-4", Seq: 4, Source: domain.RevisionRestore, RestoredFrom: revisionID},
		DeployOffered: true}, nil
}

func (f *fakeStacks) Revisions(context.Context, string, int64, int) ([]domain.StackRevision, error) {
	return []domain.StackRevision{{ID: "rev-3", Seq: 3, Hash: strings.Repeat("b", 64), Source: domain.RevisionExternal,
		Files: []domain.StackFile{{Path: ".env", SHA256: "x", Size: 9}}}}, nil
}

func (f *fakeStacks) Revision(_ context.Context, _, id string) (domain.StackRevision, error) {
	if id != "rev-3" {
		return domain.StackRevision{}, domain.ErrStackRevisionNotFound
	}
	return domain.StackRevision{ID: "rev-3", Seq: 3, Source: domain.RevisionExternal, Files: []domain.StackFile{
		{Path: ".env", Content: []byte("DB_PASSWORD=env-canary\n")}, {Path: "bin", Content: []byte{0xff, 0xfe}}}}, nil
}

func (f *fakeStacks) Services(context.Context, domain.Stack) (domain.StackServicesView, error) {
	started := testutil.Epoch
	return domain.StackServicesView{Live: true, Services: []domain.StackServiceView{{Name: "web", Status: "running",
		Containers: []domain.StackContainer{{ID: "c1", Name: "shop-web-1", Service: "web", Image: "nginx:1.27", ImageID: "sha256:img",
			State: "running", RestartPolicy: "unless-stopped", Memory: 1 << 30, StartedAt: &started,
			Ports: []domain.PortMapping{{PrivatePort: 80, PublicPort: 8080, Protocol: "tcp"}}}}}}}, nil
}

func (f *fakeStacks) ImageStatus(st domain.Stack) []domain.StackImageView {
	return []domain.StackImageView{{Service: "web", Image: "nginx:1.27", Digest: "sha256:dig", Eligible: true}}
}

func (f *fakeStacks) Discovered(context.Context, string) ([]domain.DiscoveredStack, error) {
	return []domain.DiscoveredStack{{Name: "legacy", WorkingDir: "/home/me/legacy", Reason: "outside"}}, f.err
}

func (f *fakeStacks) Import(_ context.Context, _ authz.Principal, r domain.StackImport) (domain.Stack, error) {
	if f.err != nil {
		return domain.Stack{}, f.err
	}
	return domain.Stack{ID: "st-imp", EnvironmentID: r.EnvironmentID, Name: r.ProjectName, Status: domain.StackDeployed, Revision: 1}, nil
}

func (f *fakeStacks) ImportCopy(_ context.Context, _ authz.Principal, r domain.StackImport, _ domain.StackJobRequest) (domain.Stack, domain.Job, error) {
	if f.err != nil {
		return domain.Stack{}, domain.Job{}, f.err
	}
	st := domain.Stack{ID: "st-imp", EnvironmentID: r.EnvironmentID, Name: r.ProjectName, Status: domain.StackDeployed, Revision: 1}
	return st, domain.Job{ID: "job-imp", Kind: "stack.import", EnvironmentID: r.EnvironmentID,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}}, nil
}

func (f *fakeStacks) PreviewRename(_ context.Context, st domain.Stack, name string) (domain.StackRenamePlan, error) {
	if f.err != nil {
		return domain.StackRenamePlan{}, f.err
	}
	p := f.renamePlan
	p.From, p.To, p.FromDir, p.ToDir = st.Name, name, st.Dir, name
	return p, nil
}

func (f *fakeStacks) Pull(_ context.Context, _ authz.Principal, st domain.Stack, _ domain.StackJobRequest) (domain.Job, error) {
	return f.job("stack.pull", st)
}

func (f *fakeStacks) Rename(_ context.Context, _ authz.Principal, st domain.Stack, name string, _ domain.StackJobRequest) (domain.Job, error) {
	f.mu.Lock()
	f.renames = append(f.renames, name)
	f.mu.Unlock()
	return f.job("stack.rename", st)
}

// fakeStacksRoot is the stacks volume's host path in the fake (#22 header).
const fakeStacksRoot = "/var/lib/docker/volumes/docker-manager_stacks/_data"

func (f *fakeStacks) HostPath(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.stacks[id]
	if !ok {
		return "", domain.ErrStackNotFound
	}
	return fakeStacksRoot + "/" + st.Dir, nil
}

var (
	_ StackService   = (*fakeStacks)(nil)
	_ stackHostPaths = (*fakeStacks)(nil)
)

func stacksAPIFor(t *testing.T, pol *authztest.Policy) (http.Handler, *fakeStacks) {
	t.Helper()
	svc := newFakeStacks()
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		if ref.Type == catalog.TypeStack {
			if st, ok := svc.stacks[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: st.EnvironmentID}
			}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Stacks: svc, Agents: newFakeAgents(), Authorizer: pol, Clock: testutil.FakeClock(), Idempotency: &memIdempotency{}})
	return authztest.Authenticate(withTestContext(t, mux, "")), svc
}

func stackRoutes(t *testing.T) []authztest.Call {
	t.Helper()
	return stackRoutesFor(t, "st-1")
}

// stackRoutesFor are every implemented stack route (#7, #33) of stack
// stackID in env-1 with valid bodies and headers (the schema is checked
// before authorization); the stack file routes are #15's.
func stackRoutesFor(t *testing.T, stackID string) []authztest.Call {
	t.Helper()
	all := authztest.Routes(t, map[string]string{"stackId": stackID, "environmentId": "env-1", "revisionId": "rev-3"},
		"/api/v1/stacks", "/api/v1/environments/{environmentId}/stacks")
	var calls []authztest.Call
	for _, c := range all {
		// The stack file scope is #15's and migrations are #35's (tested there).
		if !strings.Contains(c.OperationID, "-file") && !strings.Contains(c.OperationID, "-migration") {
			calls = append(calls, c)
		}
	}
	for i := range calls {
		calls[i].Headers = map[string]string{"If-Match": `"4"`, "Idempotency-Key": "k-" + calls[i].OperationID}
		switch calls[i].OperationID {
		case "create-stack", "create-stack-validation":
			calls[i].Body = map[string]any{"environmentId": "env-1", "name": "new", "compose": "services: {}\n"}
		case "create-stack-operation":
			calls[i].Body = map[string]any{"action": "restart"}
		case "create-stack-revision-restore":
			calls[i].Body = map[string]any{"revisionId": "rev-3"}
		case "create-stack-import", "create-stack-import-copy":
			calls[i].Body = map[string]any{"projectName": "legacy"}
		case "create-stack-rename-preview", "create-stack-rename":
			calls[i].Body = map[string]any{"name": "store"}
		}
	}
	if len(calls) != 21 {
		t.Fatalf("%d stack routes, want 21", len(calls))
	}
	return calls
}

func TestStackReadDoesNotOpenTheDefinition(t *testing.T) {
	h, _ := stacksAPIFor(t, authztest.Only("sam", "allow stack.read @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.read")
	authztest.AssertOnly(t, h, "sam", allowed, denied)

	r := authztest.Do(t, h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	var st Stack
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &st) != nil {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	if st.View != "full" || st.AppliedRevision == nil || st.SourceRevision == nil || !st.UndeployedChanges || len(st.Images) != 1 ||
		st.Engine == nil || st.Engine.State != "running" || len(st.Services) != 1 || st.Services[0].Icon != "globe" {
		t.Errorf("full view %+v", st)
	}
	// Bind sources come from the Compose definition, and host paths follow
	// the same rule: stack.definition.read only.
	authztest.AssertAbsent(t, "stack.read view", r.Body, secretBind, fakeStacksRoot)
	if st.Location == nil || st.Location.Dir != "shop" || st.Location.HostPath != "" {
		t.Errorf("location %+v", st.Location)
	}
	if r.Header.Get("ETag") != `"4"` {
		t.Errorf("ETag %q", r.Header.Get("ETag"))
	}
	// The hidden stack is not listed.
	r = authztest.Do(t, h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks"})
	if !strings.Contains(string(r.Body), `"st-1"`) || strings.Contains(string(r.Body), "st-secret") {
		t.Errorf("list %s", r.Body)
	}
	// Containers are shown minimally without container.details.read.
	r = authztest.Do(t, h, "sam", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/services"})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"state":"running"`) {
		t.Fatalf("services %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "minimal containers", r.Body, "sha256:img", "8080", "unless-stopped")
}

func TestDefinitionReadOpensRevisionsAndBinds(t *testing.T) {
	h, _ := stacksAPIFor(t, authztest.Only("dana", "allow stack.read @stack:st-1", "allow stack.definition.read @stack:st-1",
		"allow container.details.read @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.read", "stack.definition.read")
	authztest.AssertOnly(t, h, "dana", allowed, denied)
	r := authztest.Do(t, h, "dana", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	if !strings.Contains(string(r.Body), secretBind) {
		t.Errorf("binds missing with stack.definition.read: %s", r.Body)
	}
	var full Stack
	if json.Unmarshal(r.Body, &full) != nil || full.Location == nil || full.Location.HostPath != fakeStacksRoot+"/shop" {
		t.Errorf("host path missing with stack.definition.read: %s", r.Body)
	}
	// Lists never carry host paths (one agent lookup per stack).
	r = authztest.Do(t, h, "dana", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks"})
	authztest.AssertAbsent(t, "stack list", r.Body, fakeStacksRoot)
	r = authztest.Do(t, h, "dana", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/revisions/rev-3"})
	var rev StackRevision
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &rev) != nil || len(rev.Files) != 2 {
		t.Fatalf("revision %d %s", r.Status, r.Body)
	}
	if rev.Files[0].Content != "DB_PASSWORD=env-canary\n" || rev.Files[0].Encoding != "utf-8" || rev.Files[1].Encoding != "base64" || rev.Files[1].Content != "//4=" {
		t.Errorf("files %+v", rev.Files)
	}
	// With container.details.read on the stack the containers are full.
	r = authztest.Do(t, h, "dana", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1/services"})
	if !strings.Contains(string(r.Body), `"publicPort":8080`) || !strings.Contains(string(r.Body), `"memory":1073741824`) {
		t.Errorf("full containers %s", r.Body)
	}
}

func TestDeployOnlyShowsTheMinimalStack(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("dev", "allow stack.deploy @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.deploy")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-stacks", "get-stack")
	authztest.AssertOnly(t, h, "dev", allowed, denied)
	r := authztest.Do(t, h, "dev", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	var st Stack
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &st) != nil || st.View != "minimal" || len(st.Actions) != 1 || st.Actions[0] != "stack.deploy" {
		t.Fatalf("minimal get: %d %s", r.Status, r.Body)
	}
	authztest.AssertAbsent(t, "minimal stack", r.Body, "nginx", "orders", `"revision"`, "appliedRevision", "engine")
	svc.jobs = nil
	r = authztest.Do(t, h, "dev", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/deployments", Body: map[string]any{}})
	if r.Status != http.StatusAccepted || r.Header.Get("Location") != "/api/v1/jobs/job-1" || len(svc.jobs) != 1 || svc.jobs[0] != "stack.deploy" {
		t.Errorf("deploy %d %s %v", r.Status, r.Body, svc.jobs)
	}
}

func TestStackBuildNeedsItsOwnCapability(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("ci", "allow stack.build @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.build")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-stacks", "get-stack")
	authztest.AssertOnly(t, h, "ci", allowed, denied)
	svc.jobs, svc.builds = nil, nil
	r := authztest.Do(t, h, "ci", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/builds",
		Body: map[string]any{"services": []string{"web"}, "noCache": true, "pull": true, "timeoutSeconds": 7200, "registryIds": []string{"reg-1"}}})
	if r.Status != http.StatusAccepted || r.Header.Get("Location") != "/api/v1/jobs/job-1" || len(svc.jobs) != 1 || svc.jobs[0] != "stack.build" {
		t.Fatalf("build %d %s %v", r.Status, r.Body, svc.jobs)
	}
	if b := svc.builds[0]; !b.NoCache || !b.Pull || b.TimeoutSeconds != 7200 || len(b.RegistryIDs) != 1 {
		t.Errorf("options %+v", b)
	}
	// Out-of-range options are refused before the service is called.
	r = authztest.Do(t, h, "ci", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/builds",
		Body: map[string]any{"timeoutSeconds": 21601}})
	if r.Status != http.StatusUnprocessableEntity || len(svc.builds) != 1 {
		t.Errorf("timeout out of range: %d %s", r.Status, r.Body)
	}
	// stack.deploy does not open builds, stack.build does not open deploys.
	h, _ = stacksAPIFor(t, authztest.Only("dev", "allow stack.deploy @stack:st-1"))
	if r := authztest.Do(t, h, "dev", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/builds", Body: map[string]any{}}); r.Status != http.StatusForbidden {
		t.Errorf("build with stack.deploy: %d", r.Status)
	}
}

func TestRestrictedSeesNoStacks(t *testing.T) {
	h, _ := stacksAPIFor(t, authztest.New().Member("rita", "restricted"))
	authztest.AssertOnly(t, h, "rita", nil, stackRoutes(t))
}

func TestStackOperationSelectsCapability(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("op", "allow stack.stop @stack:st-1"))
	do := func(action string) int {
		return authztest.Do(t, h, "op", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/operations",
			Body: map[string]any{"action": action}}).Status
	}
	if s := do("stop"); s != http.StatusAccepted {
		t.Errorf("stop: %d", s)
	}
	if s := do("restart"); s != http.StatusForbidden {
		t.Errorf("restart without stack.restart: %d", s)
	}
	if s := do("explode"); s != http.StatusUnprocessableEntity {
		t.Errorf("unknown action: %d", s)
	}
	if len(svc.jobs) != 1 || svc.jobs[0] != "stack.stop" {
		t.Errorf("jobs %v", svc.jobs)
	}
}

// TestStackPullNeedsStackUpdate: pulling without deploying is stack.update's
// (stack.deploy does not open it) and starts a stack.pull job.
func TestStackPullNeedsStackUpdate(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("up", "allow stack.update @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.update")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-stacks", "get-stack")
	authztest.AssertOnly(t, h, "up", allowed, denied)
	svc.jobs = nil
	r := authztest.Do(t, h, "up", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/pulls"})
	if r.Status != http.StatusAccepted || r.Header.Get("Location") != "/api/v1/jobs/job-1" || !slices.Equal(svc.jobs, []domain.JobKind{"stack.pull"}) {
		t.Fatalf("pull %d %s %v", r.Status, r.Body, svc.jobs)
	}
	h, _ = stacksAPIFor(t, authztest.Only("dev", "allow stack.deploy @stack:st-1"))
	if r := authztest.Do(t, h, "dev", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/pulls"}); r.Status != http.StatusForbidden {
		t.Errorf("pull with stack.deploy: %d", r.Status)
	}
}

func TestStackErrorMapping(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.New().Owner("own"))
	create := func() Response {
		r := authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks",
			Body: map[string]any{"environmentId": "env-1", "name": "shop", "compose": "services: {}\n"}})
		var e Error
		_ = json.Unmarshal(r.Body, &e)
		return Response{r.Status, e.Code, e}
	}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrStackNameTaken, 409, CodeStackNameTaken},
		{&domain.StackError{Code: domain.StackErrProjectExists}, 409, CodeComposeProjectExists},
		{&domain.StackError{Code: domain.StackErrDirectoryExists}, 409, CodeStackDirectoryExists},
		{&domain.StackError{Code: domain.StackErrOffline}, 503, CodeEnvironmentOffline},
		{&domain.StackError{Code: domain.StackErrAgent}, 502, CodeEngineError},
		{&domain.StackError{Code: domain.StackErrEngineUnavailable}, 503, CodeEngineUnavailable},
		{&domain.StackError{Code: domain.StackErrEnvironmentUnsupported}, 501, CodeAgentUnsupported},
		{&domain.StackError{Code: domain.StackErrInvalidDefinition, Message: "invalid",
			Issues: []domain.StackIssue{{Code: "unsupported_compose_feature", Message: "use_api_socket", Service: "x"}}}, 422, CodeInvalidDefinition},
		{&domain.InputError{Field: "name", Message: "bad"}, 422, CodeValidationFailed},
	}
	for _, c := range cases {
		svc.err = c.err
		r := create()
		if r.Status != c.status || r.Code != c.code {
			t.Errorf("%v: %d %s, want %d %s", c.err, r.Status, r.Code, c.status, c.code)
		}
		if c.code == CodeInvalidDefinition && (len(r.Err.Details) != 1 || !strings.Contains(r.Err.Details[0].Message, "service x: use_api_socket")) {
			t.Errorf("invalid definition details %+v", r.Err.Details)
		}
	}
	svc.err = nil
	r := create()
	if r.Status != http.StatusCreated {
		t.Errorf("create: %d", r.Status)
	}
	// A stale If-Match on a metadata edit: 412 with the current ETag.
	rr := authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/stacks/st-1",
		Headers: map[string]string{"If-Match": `"3"`}, Body: map[string]any{"displayName": "x"}})
	if rr.Status != http.StatusPreconditionFailed || rr.Header.Get("ETag") != `"4"` {
		t.Errorf("stale edit %d %v", rr.Status, rr.Header)
	}
	rr = authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPatch, Path: "/api/v1/stacks/st-1",
		Headers: map[string]string{"If-Match": `"4"`}, Body: map[string]any{"displayName": "Shop 2"}})
	if rr.Status != http.StatusOK || rr.Header.Get("ETag") != `"5"` || svc.patches != 1 {
		t.Errorf("edit %d %s", rr.Status, rr.Body)
	}
	// Restore answers with the new revision and offers a deploy.
	rr = authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/revision-restores",
		Body: map[string]any{"revisionId": "rev-2"}})
	if rr.Status != http.StatusOK || !strings.Contains(string(rr.Body), `"deployOffered":true`) || !strings.Contains(string(rr.Body), `"restoredFrom":"rev-2"`) {
		t.Errorf("restore %d %s", rr.Status, rr.Body)
	}
	// Registry selection failures of a deploy (#19).
	for err, code := range map[error]string{
		&domain.AmbiguousRegistryError{Host: "ghcr.io", CandidateIDs: []string{"a", "b"}}: CodeAmbiguousRegistryConnection,
		domain.ErrRegistryConnectionRevoked:                                               CodeRegistryConnectionRevoked,
	} {
		svc.err = err
		rr = authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/deployments", Body: map[string]any{}})
		var e Error
		_ = json.Unmarshal(rr.Body, &e)
		if rr.Status != http.StatusConflict || e.Code != code {
			t.Errorf("%v: %d %s", err, rr.Status, rr.Body)
		}
	}
	svc.err = nil
	// Offline: the stack is read-only.
	svc.online = false
	rr = authztest.Do(t, h, "own", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	if !strings.Contains(string(rr.Body), `"readOnly":true`) {
		t.Errorf("offline get %s", rr.Body)
	}
}

// TestStackRenameNeedsItsOwnCapability (#7): only stack.rename opens the
// rename preview and the rename (stack.manage edits display settings);
// the rename needs If-Match, enqueues stack.rename and outside containers
// the caller cannot see are counted, never named.
func TestStackRenameNeedsItsOwnCapability(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.Only("ren", "allow stack.rename @stack:st-1"))
	allowed, denied := authztest.Split(stackRoutes(t), "stack.rename")
	allowed, denied = authztest.Discoverable(allowed, denied, "list-stacks", "get-stack")
	authztest.AssertOnly(t, h, "ren", allowed, denied)
	r := authztest.Do(t, h, "ren", authztest.Call{Method: http.MethodGet, Path: "/api/v1/stacks/st-1"})
	var st Stack
	if json.Unmarshal(r.Body, &st) != nil || !slices.Contains(st.Actions, "stack.rename") {
		t.Fatalf("actions %s", r.Body)
	}
	svc.jobs, svc.renames = nil, nil
	svc.renamePlan = domain.StackRenamePlan{Running: []string{"web"},
		Volumes:    []domain.StackRenameVolume{{Key: "data", Name: "shop_data", NewName: "store_data", Action: domain.RenameVolumeMove}},
		Containers: []domain.StackRenameContainer{{ID: "c-backup", Name: "secret-backup", Running: true, Volumes: []string{"shop_data"}}}}
	r = authztest.Do(t, h, "ren", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/rename-previews", Body: map[string]any{"name": "store"}})
	var prev StackRenamePreview
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &prev) != nil {
		t.Fatalf("preview %d %s", r.Status, r.Body)
	}
	if prev.From != "shop" || prev.To != "store" || len(prev.Volumes) != 1 || prev.Volumes[0].Action != "move" ||
		len(prev.Containers) != 1 || !prev.Containers[0].Hidden || prev.Blockers == nil {
		t.Errorf("preview %+v", prev)
	}
	authztest.AssertAbsent(t, "hidden outside container", r.Body, "secret-backup", "c-backup")
	if len(svc.jobs) != 0 {
		t.Errorf("the preview queued %v", svc.jobs)
	}
	// If-Match is required and must be current.
	do := func(headers map[string]string) Response {
		rr := authztest.Do(t, h, "ren", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/renames", Headers: headers,
			Body: map[string]any{"name": "store"}})
		var e Error
		_ = json.Unmarshal(rr.Body, &e)
		return Response{rr.Status, e.Code, e}
	}
	if r := do(nil); r.Status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d", r.Status)
	}
	if r := do(map[string]string{"If-Match": `"3"`}); r.Status != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d", r.Status)
	}
	if len(svc.renames) != 0 {
		t.Fatalf("renamed without a current If-Match: %v", svc.renames)
	}
	if r := do(map[string]string{"If-Match": `"4"`}); r.Status != http.StatusAccepted || !slices.Equal(svc.renames, []string{"store"}) ||
		!slices.Equal(svc.jobs, []domain.JobKind{"stack.rename"}) {
		t.Errorf("rename %d %v %v", r.Status, svc.renames, svc.jobs)
	}
	// A caller who sees the container gets its name.
	h, svc = stacksAPIFor(t, authztest.Only("ops", "allow stack.rename @stack:st-1", "allow container.metrics.read @env:env-1"))
	svc.renamePlan = domain.StackRenamePlan{Containers: []domain.StackRenameContainer{{ID: "c-backup", Name: "secret-backup", Volumes: []string{"shop_data"}}}}
	r = authztest.Do(t, h, "ops", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/rename-previews", Body: map[string]any{"name": "store"}})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"name":"secret-backup"`) {
		t.Errorf("visible container %d %s", r.Status, r.Body)
	}
	// stack.manage does not open renames.
	h, _ = stacksAPIFor(t, authztest.Only("ed", "allow stack.manage @stack:st-1"))
	if r := authztest.Do(t, h, "ed", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/rename-previews", Body: map[string]any{"name": "store"}}); r.Status != http.StatusForbidden {
		t.Errorf("preview with stack.manage: %d", r.Status)
	}
}

func TestStackRenameErrors(t *testing.T) {
	h, svc := stacksAPIFor(t, authztest.New().Owner("own"))
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrStackNameTaken, 409, CodeStackNameTaken},
		{&domain.InputError{Field: "name", Message: "must be a Compose project name"}, 422, CodeValidationFailed},
		{&domain.StackError{Code: domain.StackErrEnvironmentUnsupported, Message: "upgrade"}, 501, CodeAgentUnsupported},
		{&domain.StackError{Code: domain.StackErrOffline, Message: "offline"}, 503, CodeEnvironmentOffline},
		{&domain.StackError{Code: domain.StackErrRenameBlocked, Message: "blocked",
			Issues: []domain.StackIssue{{Code: "declared_name", Message: "the Compose file sets name: shop"}}}, 409, CodeStackRenameBlocked},
		{&domain.DockerError{Code: domain.DockerProtected, Message: "own project"}, 409, "protected"},
	}
	for _, c := range cases {
		svc.err = c.err
		rr := authztest.Do(t, h, "own", authztest.Call{Method: http.MethodPost, Path: "/api/v1/stacks/st-1/rename-previews", Body: map[string]any{"name": "store"}})
		var e Error
		_ = json.Unmarshal(rr.Body, &e)
		if rr.Status != c.status || e.Code != c.code {
			t.Errorf("%v: %d %s, want %d %s", c.err, rr.Status, e.Code, c.status, c.code)
		}
		if c.code == CodeStackRenameBlocked && (len(e.Details) != 1 || e.Details[0].Field != "rename.declared_name") {
			t.Errorf("blocker details %+v", e.Details)
		}
	}
}

// Response is a decoded error answer.
type Response struct {
	Status int
	Code   string
	Err    Error
}

var _ = time.Second
