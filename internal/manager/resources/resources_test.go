package resources

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	agentres "code.neureka.dev/docker-manager/docker-manager/internal/agent/resources"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

const stacksRoot = "/var/lib/docker/volumes/docker-manager_stacks/_data"

type codedErr struct{ code, msg string }

func (e codedErr) Error() string           { return e.code + ": " + e.msg }
func (e codedErr) ProtocolCode() string    { return e.code }
func (e codedErr) ProtocolMessage() string { return e.msg }

// agentRequester calls the agent's request handlers directly.
type agentRequester struct {
	agent   *agentres.Service
	offline bool
	err     error
}

func (a *agentRequester) RequestEnvironment(ctx context.Context, _, name string, input any, _ time.Duration) (json.RawMessage, error) {
	if a.offline {
		return nil, jobs.ErrAgentOffline
	}
	if a.err != nil {
		return nil, a.err
	}
	raw, _ := json.Marshal(input)
	out, err := a.agent.Requests()[name](ctx, raw)
	var he *session.HandlerError
	if errors.As(err, &he) {
		return nil, codedErr{he.Code, he.Message}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// fakeJobs completes jobs on demand and notifies subscribers.
type fakeJobs struct {
	mu   sync.Mutex
	jobs map[string]domain.Job
	subs map[string][]chan struct{}
}

func (f *fakeJobs) Enqueue(_ context.Context, req jobs.Request) (domain.Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(req.Input)
	j := domain.Job{ID: "job-" + string(rune('a'+len(f.jobs))), Kind: req.Kind, EnvironmentID: req.EnvironmentID, Targets: req.Targets,
		Input: raw, State: domain.JobQueued}
	f.jobs[j.ID] = j
	return j, true, nil
}

func (f *fakeJobs) Get(_ context.Context, id string) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return j, domain.ErrJobNotFound
	}
	return j, nil
}

func (f *fakeJobs) Subscribe(id string) (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{}, 1)
	f.subs[id] = append(f.subs[id], ch)
	return ch, func() {}
}

func (f *fakeJobs) finish(id string, state domain.JobState) {
	f.mu.Lock()
	j := f.jobs[id]
	j.State = state
	f.jobs[id] = j
	subs := f.subs[id]
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type forgetter struct {
	mu   sync.Mutex
	refs []authz.ResourceRef
	done chan struct{}
}

func (f *forgetter) ForgetResource(_ context.Context, ref authz.ResourceRef) (int, error) {
	f.mu.Lock()
	f.refs = append(f.refs, ref)
	f.mu.Unlock()
	f.done <- struct{}{}
	return 1, nil
}

type stacks map[string]string

func (s stacks) StackIDs(context.Context, string) (map[string]string, error) { return s, nil }

func fixture(t *testing.T) (*Service, *enginefake.Engine, *fakeJobs, *forgetter, *agentRequester) {
	t.Helper()
	fe := enginefake.New("ENG")
	fe.AddImage("nginx:1.27")
	fe.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27", Labels: map[string]string{
		protocol.ComposeProjectLabel: "shop", protocol.ComposeServiceLabel: "web", protocol.ComposeWorkingDirLabel: stacksRoot + "/shop"}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27"}, false)
	req := &agentRequester{agent: agentres.New(agentres.Options{Engine: func() engine.Engine { return fe },
		ManagedStackDir: func(d string) bool { return strings.HasPrefix(d, stacksRoot+"/") }})}
	db := storetest.Migrated(t)
	env := domain.Environment{ID: "env-1", Name: "NAS", EngineID: "ENG", InstallID: "i", Status: domain.EnvironmentActive, Revision: 1,
		CreatedAt: testutil.Epoch, UpdatedAt: testutil.Epoch}
	if err := store.InsertEnvironment(testutil.Context(t), db, &env); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey(nil)
	fj := &fakeJobs{jobs: map[string]domain.Job{}, subs: map[string][]chan struct{}{}}
	fg := &forgetter{done: make(chan struct{}, 8)}
	svc, err := New(Options{DB: db, Keyring: secrets.NewKeyring(key), Agents: req, Jobs: fj, Permissions: fg, InstanceID: "inst",
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Stacks: stacks{"shop": "stack-shop"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, fe, fj, fg, req
}

// TestRemovalForgetsRulesOnlyOnSuccess (#17): exact permission rules of a
// removed container are dropped once its removal job succeeded, never
// when it fails; the saved recreate specification goes with it.
func TestRemovalForgetsRulesOnlyOnSuccess(t *testing.T) {
	svc, fe, fj, fg, _ := fixture(t)
	ctx := testutil.Context(t)
	p := authz.Principal{Kind: authz.KindUser, UserID: "u"}
	j, err := svc.CreateContainer(ctx, p, "env-1", protocol.ContainerSpec{Name: "api", Image: "nginx:1.27", Env: []string{"K=v"}}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	var in protocol.ContainerCreateInput
	_ = json.Unmarshal(j.Input, &in)
	specID := in.Ownership[protocol.LabelSpec]
	m, _, err := store.GetManagedContainer(ctx, svc.opts.DB, specID)
	if err != nil || m.CreateJobID != j.ID || m.Name != "api" {
		t.Fatalf("saved spec %+v %v", m, err)
	}
	fe.AddContainer(engine.ContainerSpec{Name: "api", Image: "nginx:1.27", Labels: map[string]string{protocol.LabelManaged: protocol.ManagedStandalone,
		protocol.LabelSpec: specID}}, false)
	d, err := svc.InspectContainer(ctx, "env-1", "api")
	if err != nil {
		t.Fatal(err)
	}
	failedJob, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRemove, d, protocol.ContainerActionInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	fj.finish(failedJob.ID, domain.JobFailed)
	okJob, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRemove, d, protocol.ContainerActionInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	fj.finish(okJob.ID, domain.JobSucceeded)
	select {
	case <-fg.done:
	case <-ctx.Done():
		t.Fatal("rules not forgotten")
	}
	svc.Close() // waits for the watchers
	if len(fg.refs) != 1 || fg.refs[0] != (authz.ResourceRef{Type: catalog.TypeContainer, ID: "api", EnvironmentID: "env-1"}) {
		t.Fatalf("forgotten %+v", fg.refs)
	}
	if _, _, err := store.GetManagedContainer(ctx, svc.opts.DB, specID); !errors.Is(err, store.ErrManagedContainerNotFound) {
		t.Fatalf("spec kept after removal: %v", err)
	}
}

// TestLocatorAndReconcile: the Locators place stack members in their
// stack and service; the reconciler refreshes membership and drops stale
// recreate specifications but keeps those still being created.
func TestLocatorAndReconcile(t *testing.T) {
	svc, _, fj, _, req := fixture(t)
	ctx := testutil.Context(t)
	loc := svc.Locator(catalog.TypeContainer)
	ref := authz.ResourceRef{Type: catalog.TypeContainer, ID: "shop-web-1", EnvironmentID: "env-1"}
	if l, _ := loc.Locate(ctx, ref); l.Found {
		t.Fatalf("located before the inventory was seen: %+v", l)
	}
	svc.ReconcileSession(ctx, "env-1", func(ctx context.Context, name string, input any) ([]byte, error) {
		return req.RequestEnvironment(ctx, "env-1", name, input, 0)
	})
	l, err := loc.Locate(ctx, ref)
	if err != nil || !l.Found || len(l.Parents) != 2 || l.Parents[0].ID != "stack-shop/web" || l.Parents[1].ID != "stack-shop" {
		t.Fatalf("location %+v %v", l, err)
	}
	// Standalone containers are left to the built-in rule (their
	// environment, no parents).
	if l, _ := loc.Locate(ctx, authz.ResourceRef{Type: catalog.TypeContainer, ID: "web", EnvironmentID: "env-1"}); l.Found {
		t.Fatalf("standalone location %+v", l)
	}

	// Specs: one whose create job failed (stale), one still queued.
	p := authz.Principal{Kind: authz.KindUser, UserID: "u"}
	stale, _ := svc.CreateContainer(ctx, p, "env-1", protocol.ContainerSpec{Name: "a", Image: "nginx:1.27"}, false, "")
	pending, _ := svc.CreateContainer(ctx, p, "env-1", protocol.ContainerSpec{Name: "b", Image: "nginx:1.27"}, false, "")
	fj.finish(stale.ID, domain.JobFailed)
	svc.ReconcileSession(ctx, "env-1", func(ctx context.Context, name string, input any) ([]byte, error) {
		return req.RequestEnvironment(ctx, "env-1", name, input, 0)
	})
	specs, _ := store.ListManagedContainers(ctx, svc.opts.DB, "env-1")
	if len(specs) != 1 || specs[0].CreateJobID != pending.ID {
		t.Fatalf("specs after reconcile %+v", specs)
	}
	// An agent that cannot answer never fails the reconnect.
	req.err = codedErr{protocol.CodeEngineUnavailable, "down"}
	svc.ReconcileSession(ctx, "env-1", func(ctx context.Context, name string, input any) ([]byte, error) {
		return req.RequestEnvironment(ctx, "env-1", name, input, 0)
	})
}

// TestAgentErrorsBecomeStableCodes: transport and agent failures map to the
// documented Docker error codes.
func TestAgentErrorsBecomeStableCodes(t *testing.T) {
	svc, _, _, _, req := fixture(t)
	ctx := testutil.Context(t)
	for _, c := range []struct {
		err  error
		want string
	}{
		{jobs.ErrAgentOffline, domain.DockerEnvironmentOffline},
		{protocol.ErrRequestTimeout, domain.DockerTimeout},
		{codedErr{protocol.CodeUnsupportedAPIVersion, "old"}, domain.DockerUnsupportedAPIVersion},
		{codedErr{protocol.CodeEngineUnavailable, "down"}, domain.DockerEngineUnavailable},
		{codedErr{protocol.CodeUnsupportedRequest, "no"}, domain.DockerAgentUnsupported},
		{codedErr{protocol.CodeEngineError, "boom"}, domain.DockerEngineError},
		{codedErr{protocol.CodeNotFound, "gone"}, domain.DockerNotFound},
		{codedErr{protocol.CodeBusy, "busy"}, domain.DockerBusy},
	} {
		req.err = c.err
		_, err := svc.ListContainers(ctx, "env-1")
		var de *domain.DockerError
		if !errors.As(err, &de) || de.Code != c.want {
			t.Errorf("%v: %v, want %s", c.err, err, c.want)
		}
	}
	req.err = nil
	// Stack membership: agent-detected and resolver-known projects.
	if !svc.StackManaged(ctx, "env-1", &protocol.StackRef{Project: "other", Managed: true}) ||
		!svc.StackManaged(ctx, "env-1", &protocol.StackRef{Project: "shop"}) ||
		svc.StackManaged(ctx, "env-1", &protocol.StackRef{Project: "legacy"}) || svc.StackManaged(ctx, "env-1", nil) {
		t.Fatal("stack management")
	}
}

// TestManagerSideProtection (#32): the manager protects Docker Manager's
// containers on its own too — by the role labels and by its own container
// ID — even when an agent does not annotate them; it reports Docker Manager's
// Compose project as protected and excludes its containers from bulk
// selections.
func TestManagerSideProtection(t *testing.T) {
	svc, fe, fj, _, _ := fixture(t)
	ctx := testutil.Context(t)
	d := fe.Deploy(true)
	// This agent has no Guard: it annotates by labels only; the manager
	// knows its own container.
	svc.opts.ManagerContainerID = d.ManagerID
	unlabeled := protocol.ContainerSummary{ID: d.ManagerID, Name: "mgr"}
	if p := svc.ContainerProtection(unlabeled); p == nil || p.Role != protection.RoleManager || !p.Self || !p.RestartAllowed {
		t.Fatalf("own container %+v", p)
	}
	if p := svc.ContainerProtection(protocol.ContainerSummary{ID: "x", Labels: map[string]string{protocol.LabelRole: "agent"}}); p == nil || p.Role != protection.RoleAgent {
		t.Fatalf("labeled agent %+v", p)
	}
	if svc.ContainerProtection(protocol.ContainerSummary{ID: "y", Name: "web"}) != nil {
		t.Fatal("user container protected")
	}
	p := authz.Principal{Kind: authz.KindUser, UserID: "u"}
	for _, kind := range []domain.JobKind{jobspec.ContainerStop, jobspec.ContainerRemove, jobspec.ContainerPause} {
		_, err := svc.ContainerAction(ctx, p, "env-1", kind, protocol.ContainerDetails{ContainerSummary: unlabeled}, protocol.ContainerActionInput{Force: true}, "")
		var de *domain.DockerError
		if !errors.As(err, &de) || de.Code != domain.DockerProtected {
			t.Errorf("%s of the manager: %v", kind, err)
		}
	}
	_, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRestart, protocol.ContainerDetails{ContainerSummary: unlabeled}, protocol.ContainerActionInput{}, "")
	var de *domain.DockerError
	if !errors.As(err, &de) || de.Code != domain.DockerConfirmationRequired {
		t.Fatalf("unconfirmed restart: %v", err)
	}
	if len(fj.jobs) != 0 {
		t.Fatal("a refused action enqueued a job")
	}
	// Docker Manager's Compose project, and a user project.
	if pp, err := svc.ProjectProtection(ctx, "env-1", "docker-manager"); err != nil || pp == nil || pp.Role != protection.RoleProject {
		t.Fatalf("docker-manager project %+v %v", pp, err)
	}
	if pp, err := svc.ProjectProtection(ctx, "env-1", "shop"); err != nil || pp != nil {
		t.Fatalf("user project %+v %v", pp, err)
	}
	kept, excluded, err := svc.ProtectedContainers(ctx, "env-1")
	// Agent and manager by their labels, the proxy as a member of their
	// Compose project; the user's containers stay selectable.
	if err != nil || len(kept) != 2 || len(excluded) != 3 {
		t.Fatalf("kept %d excluded %+v %v", len(kept), excluded, err)
	}
	for _, c := range kept {
		if strings.HasPrefix(c.Name, "docker-manager-") {
			t.Errorf("Docker Manager container %s selectable", c.Name)
		}
	}
}
