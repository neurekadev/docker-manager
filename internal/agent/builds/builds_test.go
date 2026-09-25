package builds

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/gitremote/gittest"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

var (
	mainSHA = strings.Repeat("1", 40)
	tagSHA  = strings.Repeat("2", 40)
)

// fakeEngine records builds; block makes Build wait for its context.
type fakeEngine struct {
	engine.Engine
	mu     sync.Mutex
	specs  []engine.BuildSpec
	events []engine.BuildEvent
	block  bool
	err    error
}

func (f *fakeEngine) Build(ctx context.Context, spec engine.BuildSpec) (engine.BuildResult, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	evs, block, err := f.events, f.block, f.err
	f.mu.Unlock()
	for _, ev := range evs {
		spec.Progress(ev)
	}
	if block {
		<-ctx.Done()
		return engine.BuildResult{}, &engine.Error{Code: engine.CodeCanceled, Op: "image.build", Message: "context canceled"}
	}
	if err != nil {
		return engine.BuildResult{}, err
	}
	return engine.BuildResult{ImageID: "sha256:" + strings.Repeat("f", 64)}, nil
}

type memJournal struct {
	mu    sync.Mutex
	saves []string
}

func (m *memJournal) Save(_ context.Context, st *jobexec.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves = append(m.saves, string(b))
	return nil
}

type reporter struct {
	mu   sync.Mutex
	msgs []string
}

func (r *reporter) Progress(_ context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Item != nil {
		r.msgs = append(r.msgs, "item "+p.Item.Name+"="+p.Item.Message)
		return
	}
	r.msgs = append(r.msgs, p.Message)
}

func (r *reporter) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.msgs, "\n")
}

type fixture struct {
	t       *testing.T
	git     *gittest.Server
	eng     *fakeEngine
	clk     *clock.Fake
	secrets *canary.Set
	token   string
	regPw   string
	exec    jobexec.Executor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	set := canary.New()
	f := &fixture{t: t, eng: &fakeEngine{}, clk: testutil.FakeClock(), secrets: set,
		token: set.New(canary.APIToken, "git token"), regPw: set.New(canary.RegistryCredential, "registry password")}
	f.git = gittest.New(t, true, "builder", f.token)
	f.git.Add("/acme/private.git", &gittest.Repo{Head: "refs/heads/main", Private: true,
		Refs: map[string]string{"refs/heads/main": mainSHA, "refs/tags/v2": tagSHA}})
	f.git.Add("/acme/public.git", &gittest.Repo{Head: "refs/heads/main", Refs: map[string]string{"refs/heads/main": mainSHA}})
	f.exec = Executor(Options{Engine: func() engine.Engine { return f.eng }, HTTP: f.git.Client(), Clock: f.clk,
		Logger: set.CaptureLogger(t)})
	return f
}

func (f *fixture) input(repo string, mut func(*jobspec.ImageBuildInput)) json.RawMessage {
	in := jobspec.ImageBuildInput{GitURL: f.git.URL(repo), Ref: "main", ContextPath: "app", Dockerfile: "build/Dockerfile",
		Target: "prod", BuildArgs: map[string]string{"VERSION": "1.2"}, Tags: []string{"acme/app:1.2"}, NoCache: true, Pull: true}
	if mut != nil {
		mut(&in)
	}
	b, err := json.Marshal(in)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fixture) secretsFor(withGit bool) *protocol.CommandSecrets {
	s := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "r1", Host: "ghcr.io",
		ServerAddress: "ghcr.io", Username: "robot", Secret: f.regPw}}}
	if withGit {
		s.Git = []protocol.GitCredential{{CredentialID: "g1", Host: strings.TrimPrefix(f.git.Server.URL, "https://"), Username: "builder", Secret: f.token}}
	}
	return s
}

func (f *fixture) run(input json.RawMessage, secrets *protocol.CommandSecrets, cancel func() bool, completed ...string) (protocol.ResultPayload, *reporter, *memJournal) {
	f.t.Helper()
	j, r := &memJournal{}, &reporter{}
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: jobspec.ImageBuild, Input: input, Completed: completed, Secrets: secrets}
	res, err := jobexec.Run(testutil.Context(f.t), f.exec, st, jobexec.Options{Journal: j, Reporter: r, CancelRequested: cancel})
	if err != nil {
		f.t.Fatal(err)
	}
	return res, r, j
}

func item(res protocol.ResultPayload, name string) string {
	for _, it := range res.Items {
		if it.Name == name {
			return it.Message
		}
	}
	return ""
}

func TestPrivateBuildUsesTheResolvedCommitAndNeverLeaksCredentials(t *testing.T) {
	f := newFixture(t)
	basic := base64.StdEncoding.EncodeToString([]byte("builder:" + f.token))
	f.eng.events = []engine.BuildEvent{
		{Step: "[internal] load git source", Status: "started"},
		{Step: "[internal] load git source", Status: "log", Log: []byte("fetching with header Authorization: basic " + basic + "\n")},
		{Step: "[internal] load git source", Status: "log", Log: []byte("token " + f.token + " and " + f.regPw + "\n")},
		{Step: "[internal] load git source", Status: "done"},
		{Step: "[2/2] RUN make", Status: "cached"},
	}
	res, r, j := f.run(f.input("/acme/private.git", nil), f.secretsFor(true), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("%+v", res)
	}
	if item(res, jobspec.BuildItemCommit) != mainSHA || item(res, jobspec.BuildItemRef) != "refs/heads/main" ||
		!strings.HasPrefix(item(res, jobspec.BuildItemImage), "sha256:") {
		t.Fatalf("items %+v", res.Items)
	}
	if len(f.eng.specs) != 1 {
		t.Fatalf("%d builds", len(f.eng.specs))
	}
	s := f.eng.specs[0]
	if s.RemoteContext != f.git.URL("/acme/private.git")+"#"+mainSHA+":app" || s.Dockerfile != "build/Dockerfile" || s.Target != "prod" ||
		s.BuildArgs["VERSION"] != "1.2" || !s.NoCache || !s.Pull || s.Tags[0] != "acme/app:1.2" {
		t.Fatalf("spec %+v", s)
	}
	if strings.Contains(s.RemoteContext, f.token) || len(s.GitAuth) != 1 || string(s.GitAuth[0].Token) != f.token ||
		s.GitAuth[0].Host != strings.TrimPrefix(f.git.Server.URL, "https://") {
		t.Fatalf("git auth %+v", s.GitAuth)
	}
	if len(s.RegistryAuth) != 1 || string(s.RegistryAuth[0].Password) != f.regPw {
		t.Fatal("registry credentials for base images not passed")
	}
	out := r.all()
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "[2/2] RUN make: cached") || !strings.Contains(out, mainSHA) {
		t.Fatalf("progress:\n%s", out)
	}
	f.secrets.AssertClean(t, "progress", out)
	f.secrets.AssertClean(t, "journal", j.saves)
	f.secrets.AssertClean(t, "result", res)
}

func TestPublicBuildAndTags(t *testing.T) {
	f := newFixture(t)
	res, _, _ := f.run(f.input("/acme/public.git", func(in *jobspec.ImageBuildInput) { in.Ref = ""; in.ContextPath = "" }), nil, nil)
	if res.Outcome != jobexec.OutcomeSucceeded || f.eng.specs[0].GitAuth != nil || f.eng.specs[0].RemoteContext != f.git.URL("/acme/public.git")+"#"+mainSHA {
		t.Fatalf("%+v %+v", res, f.eng.specs)
	}
	res, _, _ = f.run(f.input("/acme/private.git", func(in *jobspec.ImageBuildInput) { in.Ref = "v2" }), f.secretsFor(true), nil)
	if item(res, jobspec.BuildItemCommit) != tagSHA {
		t.Fatalf("%+v", res.Items)
	}
}

func TestNamedCredentialIsNeverReplacedByAnonymousAccess(t *testing.T) {
	f := newFixture(t)
	in := f.input("/acme/public.git", func(in *jobspec.ImageBuildInput) { in.GitCredentials = []string{"g1"} })
	res, _, _ := f.run(in, f.secretsFor(false), nil)
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "names a Git credential") {
		t.Fatalf("%+v", res)
	}
	if f.git.Hits() != 0 || len(f.eng.specs) != 0 {
		t.Fatal("contacted the Git server or the Engine")
	}
	// Anonymous access to a private repository fails at the ref listing.
	res, _, _ = f.run(f.input("/acme/private.git", nil), nil, nil)
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "401") {
		t.Fatalf("%+v", res)
	}
}

func TestCancelStopsARunningBuild(t *testing.T) {
	f := newFixture(t)
	f.eng.block = true
	var mu sync.Mutex
	requested := false
	cancel := func() bool { mu.Lock(); defer mu.Unlock(); return requested }
	done := make(chan protocol.ResultPayload, 1)
	go func() {
		res, _, _ := f.run(f.input("/acme/public.git", nil), nil, cancel)
		done <- res
	}()
	ctx := testutil.Context(t)
	if err := f.clk.BlockUntilWaiters(ctx, 2); err != nil { // poll ticker + timeout timer
		t.Fatal(err)
	}
	f.clk.Advance(DefaultCancelPoll)
	mu.Lock()
	requested = true
	mu.Unlock()
	f.clk.Advance(DefaultCancelPoll)
	res := <-done
	if res.Outcome != jobexec.OutcomeCancelled || !strings.Contains(res.Message, "build") {
		t.Fatalf("%+v", res)
	}
}

func TestTimeoutStopsTheBuild(t *testing.T) {
	f := newFixture(t)
	f.eng.block = true
	done := make(chan protocol.ResultPayload, 1)
	go func() {
		res, _, _ := f.run(f.input("/acme/public.git", func(in *jobspec.ImageBuildInput) { in.TimeoutSeconds = 90 }), nil, nil)
		done <- res
	}()
	if err := f.clk.BlockUntilWaiters(testutil.Context(t), 2); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(90 * time.Second)
	res := <-done
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "did not finish within 1m30s") {
		t.Fatalf("%+v", res)
	}
}

func TestResumedBuildResolvesAgain(t *testing.T) {
	f := newFixture(t)
	res, _, _ := f.run(f.input("/acme/private.git", nil), f.secretsFor(true), nil, "fetch_context")
	if res.Outcome != jobexec.OutcomeSucceeded || item(res, jobspec.BuildItemCommit) != mainSHA || f.git.Hits() == 0 {
		t.Fatalf("%+v", res)
	}
}

func TestEngineFailureIsReportedScrubbed(t *testing.T) {
	f := newFixture(t)
	f.eng.err = &engine.Error{Code: engine.CodeBuildFailed, Op: "image.build", Message: "failed to fetch with " + f.token}
	res, _, _ := f.run(f.input("/acme/private.git", nil), f.secretsFor(true), nil)
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "[redacted]") {
		t.Fatalf("%+v", res)
	}
	f.secrets.AssertClean(t, "result", res)
}
