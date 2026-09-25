package stacks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/buildrun"
	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// Stack builds (#33) run in process through the real Compose adapter
// (project loading, build-section mapping, image naming, missing-image
// detection) against a fake Engine that scripts BuildKit: its builds emit
// progress and output, tag images in an in-memory image store, and can
// block until they are cancelled. Up is scripted (the SDK's container
// work is not what is under test here).

// buildEngine is the Engine of the build tests: identity from the
// connected fake Moby API, scripted builds, images and containers.
type buildEngine struct {
	engine.Engine
	mu         sync.Mutex
	images     map[string]string // tag -> image ID
	specs      []engine.BuildSpec
	builds     int
	block      map[string]bool // tag whose build blocks until cancelled
	started    chan string
	logs       []string // output lines every build prints
	fail       error
	containers []engine.Container
}

func (b *buildEngine) Build(ctx context.Context, spec engine.BuildSpec) (engine.BuildResult, error) {
	b.mu.Lock()
	b.specs = append(b.specs, spec)
	b.builds++
	id := fmt.Sprintf("sha256:%064d", b.builds)
	block, logs, fail := b.block[spec.Tags[0]], slices.Clone(b.logs), b.fail
	b.mu.Unlock()
	if spec.Progress != nil {
		spec.Progress(engine.BuildEvent{Step: "[1/2] FROM base", Status: "started"})
		for _, l := range logs {
			spec.Progress(engine.BuildEvent{Step: "[2/2] RUN make", Status: "log", Log: []byte(l + "\n")})
		}
		spec.Progress(engine.BuildEvent{Step: "[1/2] FROM base", Status: "done"})
	}
	if b.started != nil {
		b.started <- spec.Tags[0]
	}
	if block {
		<-ctx.Done()
		return engine.BuildResult{}, &engine.Error{Code: engine.CodeCanceled, Op: "image.build", Message: "context canceled"}
	}
	if fail != nil {
		return engine.BuildResult{}, fail
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range spec.Tags {
		b.images[t] = id
	}
	return engine.BuildResult{ImageID: id}, nil
}

func (b *buildEngine) InspectImage(_ context.Context, ref string) (engine.ImageDetails, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.images[ref]
	if !ok {
		for _, v := range b.images {
			if v == ref {
				id, ok = v, true
			}
		}
	}
	if !ok {
		return engine.ImageDetails{}, engine.Errorf("image.inspect", engine.CodeNotFound, "no such image: %s", ref)
	}
	return engine.ImageDetails{ID: id, OS: "linux", Architecture: "amd64"}, nil
}

func (b *buildEngine) ListContainers(_ context.Context, flt engine.ContainerFilter) ([]engine.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []engine.Container
	for _, c := range b.containers {
		ok := true
		for _, l := range flt.Labels {
			k, v, _ := strings.Cut(l, "=")
			if c.Labels[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (b *buildEngine) imageOf(tag string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.images[tag]
}

func (b *buildEngine) buildSpecs() []engine.BuildSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.specs)
}

// buildComposer is the real Compose adapter with a scripted Up.
type buildComposer struct {
	*compose.Adapter
	eng *buildEngine
	mu  sync.Mutex
	ups int
}

func (c *buildComposer) Up(_ context.Context, p *compose.Project, _ compose.UpOptions) error {
	c.mu.Lock()
	c.ups++
	c.mu.Unlock()
	var cs []engine.Container
	for _, s := range p.Services {
		cs = append(cs, engine.Container{ID: p.Name + "-" + s.Name, Names: []string{"/" + p.Name + "-" + s.Name + "-1"}, Image: s.Image,
			ImageID: c.eng.imageOf(s.Image), State: "running",
			Labels: map[string]string{lifecycle.ComposeProjectLabel: p.Name, lifecycle.ComposeServiceLabel: s.Name}})
	}
	c.eng.mu.Lock()
	c.eng.containers = cs
	c.eng.mu.Unlock()
	return nil
}

func (c *buildComposer) Pull(context.Context, *compose.Project, compose.RunOptions) error { return nil }
func (c *buildComposer) Down(context.Context, string, *compose.Project, compose.DownOptions) error {
	return nil
}

type buildEnv struct {
	t    *testing.T
	root string
	svc  *Service
	eng  *buildEngine
	comp *buildComposer
	clk  *clock.Fake
	set  *canary.Set
}

func newBuildEnv(t *testing.T) *buildEnv {
	t.Helper()
	ctx := testutil.Context(t)
	set := canary.New()
	logger := set.CaptureLogger(t)
	fake := enginetest.Start(t, enginetest.Options{})
	client, err := engine.Connect(ctx, engine.Options{Host: fake.Host, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	eng := &buildEngine{Engine: client, images: map[string]string{}, block: map[string]bool{}}
	a, err := compose.New(ctx, compose.Options{Host: fake.Host, Engine: eng, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	root := t.TempDir()
	res := &storage.Result{StacksDir: filepath.ToSlash(root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(root), OK: true}}}
	e := &buildEnv{t: t, root: root, eng: eng, comp: &buildComposer{Adapter: a, eng: eng}, clk: testutil.FakeClock(), set: set}
	e.svc = New(Options{Deps: fakeDeps{c: e.comp, eng: eng, st: res}, Clock: e.clk, Logger: logger})
	writeTree(t, filepath.Join(root, "shop"), map[string]string{
		"compose.yaml": `services:
  web:
    build:
      context: ./web
      args:
        VERSION: "1.2"
    depends_on: [api]
  api:
    build: ./api
    image: registry.example:5000/shop/api:dev
  db:
    image: registry.example:5000/db:16
`,
		"web/Dockerfile": "FROM registry.example:5000/base:1\n",
		"api/Dockerfile": "FROM registry.example:5000/base:1\n",
	})
	return e
}

type recJournal struct {
	mu    sync.Mutex
	saves []string
}

func (j *recJournal) Save(_ context.Context, st *jobexec.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.saves = append(j.saves, string(b))
	return nil
}

type recReporter struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recReporter) Progress(_ context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Item != nil {
		r.msgs = append(r.msgs, "item "+p.Item.Name+"="+p.Item.Message)
		return
	}
	r.msgs = append(r.msgs, p.Message)
}

func (r *recReporter) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.msgs, "\n")
}

type buildRun struct {
	res     protocol.ResultPayload
	out     protocol.StackJobOutput
	journal *recJournal
	rep     *recReporter
}

func (e *buildEnv) run(kind domain.JobKind, in protocol.StackJobInput, secrets *protocol.CommandSecrets, cancel func() bool) buildRun {
	e.t.Helper()
	var exec jobexec.Executor
	for _, x := range e.svc.Executors() {
		if x.Kind == kind {
			exec = x
		}
	}
	if err := exec.Validate(domain.ExecutorAgent); err != nil {
		e.t.Fatal(err)
	}
	in.Stack = ref("shop")
	b, _ := json.Marshal(in)
	r := buildRun{journal: &recJournal{}, rep: &recReporter{}}
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: kind, Input: b, Secrets: secrets}
	res, err := jobexec.Run(testutil.Context(e.t), exec, st, jobexec.Options{Journal: r.journal, Reporter: r.rep, CancelRequested: cancel})
	if err != nil {
		e.t.Fatal(err)
	}
	r.res = res
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &r.out); err != nil {
			e.t.Fatal(err)
		}
	}
	return r
}

// assertClean checks every place a credential could leak to.
func (e *buildEnv) assertClean(r buildRun) {
	e.t.Helper()
	e.set.AssertClean(e.t, "result", r.res)
	e.set.AssertClean(e.t, "progress", r.rep.all())
	e.set.AssertClean(e.t, "journal", r.journal.saves)
}

func builtTags(out protocol.StackJobOutput) []string {
	var tags []string
	for _, b := range out.Built {
		tags = append(tags, b.Service+"="+b.Image)
	}
	slices.Sort(tags)
	return tags
}

func TestStackBuildRebuildsEveryBuildSection(t *testing.T) {
	e := newBuildEnv(t)
	e.eng.images["shop-web"] = "sha256:old-web"
	e.eng.logs = []string{"compiling", "done"}
	r := e.run(jobspec.StackBuild, protocol.StackJobInput{NoCache: true, PullBase: true}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("%+v", r.res)
	}
	// Both build sections were rebuilt (an explicit build ignores existing
	// images), with the request's options, the service's build args and
	// the Compose image names.
	specs := e.eng.buildSpecs()
	if len(specs) != 2 {
		t.Fatalf("builds %+v", specs)
	}
	for _, s := range specs {
		if !s.NoCache || !s.Pull || s.ContextDir == "" || s.RemoteContext != "" {
			t.Errorf("spec %+v", s)
		}
	}
	if specs[0].Tags[0] != "registry.example:5000/shop/api:dev" || specs[1].Tags[0] != "shop-web" || specs[1].BuildArgs["VERSION"] != "1.2" {
		t.Errorf("tags/args %v %v %v", specs[0].Tags, specs[1].Tags, specs[1].BuildArgs)
	}
	if e.eng.imageOf("shop-web") == "sha256:old-web" {
		t.Error("the existing image was not rebuilt")
	}
	if got := builtTags(r.out); !slices.Equal(got, []string{"api=registry.example:5000/shop/api:dev", "web=shop-web"}) {
		t.Errorf("built %v", got)
	}
	// BuildKit progress and output are streamed per image.
	log := r.rep.all()
	for _, want := range []string{"shop-web: building", "shop-web: [1/2] FROM base: started", "shop-web: [2/2] RUN make\ncompiling\ndone",
		"item shop-web=" + e.eng.imageOf("shop-web"), "registry.example:5000/shop/api:dev: building"} {
		if !strings.Contains(log, want) {
			t.Errorf("progress lacks %q:\n%s", want, log)
		}
	}
	if len(r.out.Services) != 3 || !slices.Equal(r.res.CompletedSteps, []string{"fetch_sources", "build_images"}) {
		t.Errorf("output %+v steps %v", r.out.Services, r.res.CompletedSteps)
	}
	// Nothing is deployed by a build.
	if e.comp.ups != 0 {
		t.Error("a stack build ran up")
	}

	// A narrowed build builds only the named service.
	e.eng.specs = nil
	r = e.run(jobspec.StackBuild, protocol.StackJobInput{Services: []string{"api"}}, nil, nil)
	if specs := e.eng.buildSpecs(); r.res.Outcome != jobexec.OutcomeSucceeded || len(specs) != 1 || specs[0].NoCache {
		t.Errorf("narrowed build %+v %+v", r.res, specs)
	}
}

func TestStackBuildRefusesWhatCannotBeBuilt(t *testing.T) {
	e := newBuildEnv(t)
	for _, svcs := range [][]string{{"db"}, {"nope"}} {
		r := e.run(jobspec.StackBuild, protocol.StackJobInput{Services: svcs}, nil, nil)
		if r.res.Outcome != jobexec.OutcomeFailed || r.res.ErrorClass != classNothingToBuild || r.res.Recovery == "" {
			t.Errorf("%v: %+v", svcs, r.res)
		}
	}
	writeTree(t, filepath.Join(e.root, "shop"), map[string]string{"compose.yaml": "services:\n  db:\n    image: registry.example:5000/db:16\n"})
	r := e.run(jobspec.StackBuild, protocol.StackJobInput{}, nil, nil)
	if r.res.ErrorClass != classNothingToBuild || len(e.eng.buildSpecs()) != 0 {
		t.Errorf("%+v", r.res)
	}
	// Unsupported build keys are refused before anything is built.
	writeTree(t, filepath.Join(e.root, "shop"), map[string]string{"compose.yaml": "services:\n  web:\n    build:\n      context: .\n      ssh: [default]\n"})
	r = e.run(jobspec.StackBuild, protocol.StackJobInput{}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeFailed || r.res.ErrorClass != string(engine.CodeUnsupportedFeature) || len(e.eng.buildSpecs()) != 0 {
		t.Errorf("ssh: %+v", r.res)
	}
}

func TestDeployBuildsMissingImagesThroughTheSamePath(t *testing.T) {
	e := newBuildEnv(t)
	// First deploy: both build images are missing and built by the build
	// step (progress streamed), then up runs with them.
	r := e.run(jobspec.StackDeploy, protocol.StackJobInput{}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeSucceeded || len(e.eng.buildSpecs()) != 2 || e.comp.ups != 1 {
		t.Fatalf("%+v builds %d ups %d", r.res, len(e.eng.buildSpecs()), e.comp.ups)
	}
	if got := builtTags(r.out); len(got) != 2 || !strings.Contains(r.rep.all(), "shop-web: [1/2] FROM base: started") {
		t.Errorf("built %v progress:\n%s", got, r.rep.all())
	}
	web := ""
	for _, i := range r.out.Images {
		if i.Service == "web" {
			web = i.ImageID
		}
	}
	if web == "" || web != e.eng.imageOf("shop-web") {
		t.Errorf("deployed web image %q, built %q", web, e.eng.imageOf("shop-web"))
	}
	// A redeploy does not rebuild existing images...
	r = e.run(jobspec.StackDeploy, protocol.StackJobInput{}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeSucceeded || len(e.eng.buildSpecs()) != 2 || len(r.out.Built) != 0 {
		t.Errorf("redeploy rebuilt: %+v %d", r.res, len(e.eng.buildSpecs()))
	}
	// ...unless asked to (deploy with build: true, or a stack build).
	r = e.run(jobspec.StackDeploy, protocol.StackJobInput{Build: true}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeSucceeded || len(e.eng.buildSpecs()) != 4 || e.eng.imageOf("shop-web") == web {
		t.Errorf("deploy with build: %+v %d", r.res, len(e.eng.buildSpecs()))
	}
	for _, i := range r.out.Images {
		if i.Service == "web" && i.ImageID != e.eng.imageOf("shop-web") {
			t.Errorf("the redeploy runs %s, not the rebuilt %s", i.ImageID, e.eng.imageOf("shop-web"))
		}
	}
}

func TestStackBuildCancelsCleanlyMidBuild(t *testing.T) {
	for _, kind := range []domain.JobKind{jobspec.StackBuild, jobspec.StackDeploy} {
		t.Run(string(kind), func(t *testing.T) {
			e := newBuildEnv(t)
			e.eng.images["shop-web"] = "sha256:previous"
			// api builds first (sorted), then web blocks until cancelled.
			e.eng.block["shop-web"] = true
			e.eng.started = make(chan string, 4)
			var mu sync.Mutex
			requested := false
			cancel := func() bool { mu.Lock(); defer mu.Unlock(); return requested }
			done := make(chan buildRun, 1)
			go func() { done <- e.run(kind, protocol.StackJobInput{Build: true}, nil, cancel) }()
			for tag := range e.eng.started {
				if tag == "shop-web" {
					break
				}
			}
			ctx := testutil.Context(t)
			if err := e.clk.BlockUntilWaiters(ctx, 2); err != nil { // poll ticker + timeout timer
				t.Fatal(err)
			}
			mu.Lock()
			requested = true
			mu.Unlock()
			e.clk.Advance(buildrun.DefaultCancelPoll)
			r := <-done
			if r.res.Outcome != jobexec.OutcomeCancelled || r.res.ErrorClass != domain.ErrorCancelled {
				t.Fatalf("%+v", r.res)
			}
			// The interrupted image was not replaced; the one built before
			// it is reported; nothing was deployed.
			if e.eng.imageOf("shop-web") != "sha256:previous" {
				t.Error("the cancelled build replaced the image")
			}
			if got := builtTags(r.out); !slices.Equal(got, []string{"api=registry.example:5000/shop/api:dev"}) {
				t.Errorf("built %v", got)
			}
			if e.comp.ups != 0 || slices.Contains(r.res.CompletedSteps, "build_images") {
				t.Errorf("ups %d steps %v", e.comp.ups, r.res.CompletedSteps)
			}
		})
	}
}

func TestStackBuildTimeout(t *testing.T) {
	e := newBuildEnv(t)
	e.eng.block["registry.example:5000/shop/api:dev"] = true
	done := make(chan buildRun, 1)
	go func() { done <- e.run(jobspec.StackBuild, protocol.StackJobInput{BuildTimeoutSeconds: 90}, nil, nil) }()
	if err := e.clk.BlockUntilWaiters(testutil.Context(t), 2); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(90 * time.Second)
	r := <-done
	if r.res.Outcome != jobexec.OutcomeFailed || !strings.Contains(r.res.Message, "did not finish within 1m30s") {
		t.Fatalf("%+v", r.res)
	}
	// Out-of-range timeouts are refused by the executor too.
	r = e.run(jobspec.StackBuild, protocol.StackJobInput{BuildTimeoutSeconds: int(jobspec.MaxBuildTimeout/time.Second) + 1}, nil, nil)
	if r.res.Outcome != jobexec.OutcomeFailed || !strings.Contains(r.res.Message, "build timeout") {
		t.Errorf("%+v", r.res)
	}
}

func TestStackBuildCredentialsReachBuildKitButNeverTheOutput(t *testing.T) {
	e := newBuildEnv(t)
	pw := e.set.New(canary.RegistryCredential, "registry password")
	secrets := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "reg-1", Host: "registry.example:5000",
		ServerAddress: "registry.example:5000", Username: "bot", Secret: pw}}}
	// BuildKit output that echoes the credential in its usual forms.
	e.eng.logs = []string{"auth " + pw, "header Basic " + base64.StdEncoding.EncodeToString([]byte("bot:"+pw)),
		"token " + base64.StdEncoding.EncodeToString([]byte(pw))}
	in := protocol.StackJobInput{RegistryConnections: []string{"reg-1"}}

	// The input names a connection but the command carries no credential:
	// the build fails instead of pulling base images anonymously.
	r := e.run(jobspec.StackBuild, in, nil, nil)
	if r.res.Outcome != jobexec.OutcomeFailed || r.res.ErrorClass != classCredentialUnavailable || len(e.eng.buildSpecs()) != 0 {
		t.Fatalf("without secrets: %+v", r.res)
	}

	for _, kind := range []domain.JobKind{jobspec.StackBuild, jobspec.StackDeploy} {
		e.eng.specs = nil
		r = e.run(kind, protocol.StackJobInput{RegistryConnections: []string{"reg-1"}, Build: true}, secrets, nil)
		if r.res.Outcome != jobexec.OutcomeSucceeded {
			t.Fatalf("%s: %+v", kind, r.res)
		}
		for _, s := range e.eng.buildSpecs() {
			if len(s.RegistryAuth) != 1 || s.RegistryAuth[0].ServerAddress != "registry.example:5000" || string(s.RegistryAuth[0].Password) != pw {
				t.Errorf("%s: base-image auth %+v", kind, s.RegistryAuth)
			}
		}
		if !strings.Contains(r.rep.all(), "auth [redacted]") {
			t.Errorf("%s: build output not streamed:\n%s", kind, r.rep.all())
		}
		e.assertClean(r)
	}

	// A build failure whose message carries the credential is scrubbed and
	// keeps its class.
	e.eng.fail = &engine.Error{Code: engine.CodeBuildFailed, Op: "image.build", Message: "failed to fetch base with " + pw}
	r = e.run(jobspec.StackBuild, in, secrets, nil)
	if r.res.Outcome != jobexec.OutcomeFailed || r.res.ErrorClass != string(engine.CodeBuildFailed) || !strings.Contains(r.res.Message, "[redacted]") {
		t.Fatalf("%+v", r.res)
	}
	e.assertClean(r)
}
