package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeComposer loads projects for real (compose-go, no Engine) and records
// the SDK operations.
type fakeComposer struct {
	mu     sync.Mutex
	calls  []string
	upErr  error
	onUp   func(p *compose.Project)
	upAuth []engine.RegistryAuth
	// onCreate scripts Create (#20 updates).
	onCreate func(p *compose.Project, o compose.CreateOptions) error
	// onDown scripts Down (the project's containers go away).
	onDown func(name string)
}

func (f *fakeComposer) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeComposer) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

func (f *fakeComposer) Up(_ context.Context, p *compose.Project, o compose.UpOptions) error {
	f.record("up:" + p.Name)
	f.mu.Lock()
	f.upAuth = o.Auth
	f.mu.Unlock()
	if f.onUp != nil {
		f.onUp(p)
	}
	return f.upErr
}

func (f *fakeComposer) Pull(_ context.Context, p *compose.Project, _ compose.RunOptions) error {
	f.record("pull:" + p.Name)
	return nil
}

func (f *fakeComposer) Build(_ context.Context, p *compose.Project, _ compose.BuildOptions) error {
	f.record("build:" + p.Name)
	return nil
}

func (f *fakeComposer) Down(_ context.Context, name string, _ *compose.Project, _ compose.DownOptions) error {
	f.record("down:" + name)
	if f.onDown != nil {
		f.onDown(name)
	}
	return nil
}

func (f *fakeComposer) Create(_ context.Context, p *compose.Project, o compose.CreateOptions) error {
	f.record("create:" + p.Name + ":" + strings.Join(o.Services, ","))
	if f.onCreate != nil {
		return f.onCreate(p, o)
	}
	return nil
}

// fakeEngine serves the container and image calls the stack code makes.
type fakeEngine struct {
	engine.Engine
	mu         sync.Mutex
	containers []engine.Container
	images     map[string]engine.ImageDetails
	calls      []string
}

func (f *fakeEngine) ListContainers(_ context.Context, flt engine.ContainerFilter) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []engine.Container
	for _, c := range f.containers {
		ok := true
		for _, l := range flt.Labels {
			k, v, hasV := strings.Cut(l, "=")
			got, has := c.Labels[k]
			if !has || (hasV && got != v) {
				ok = false
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.ID == id {
			st := engine.ContainerState{Status: c.State, Running: c.State == "running"}
			if c.Health != "" {
				st.Health = &engine.Health{Status: c.Health}
			}
			return engine.ContainerDetails{ID: c.ID, Name: strings.TrimPrefix(c.Names[0], "/"), Image: c.Image, ImageID: c.ImageID,
				State: st, RestartPolicy: "unless-stopped", Resources: engine.Resources{Memory: 256 << 20},
				Ports: []engine.Port{{PrivatePort: 80, PublicPort: 8080, Protocol: "tcp"}}}, nil
		}
	}
	return engine.ContainerDetails{}, engine.Errorf("container.inspect", engine.CodeNotFound, "no such container")
}

func (f *fakeEngine) InspectImage(_ context.Context, ref string) (engine.ImageDetails, error) {
	if img, ok := f.images[ref]; ok {
		return img, nil
	}
	return engine.ImageDetails{}, engine.Errorf("image.inspect", engine.CodeNotFound, "no such image")
}

func (f *fakeEngine) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start:"+id)
	for i := range f.containers {
		if f.containers[i].ID == id {
			f.containers[i].State = "running"
		}
	}
	return nil
}

func (f *fakeEngine) StopContainer(_ context.Context, id string, _ *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop:"+id)
	for i := range f.containers {
		if f.containers[i].ID == id {
			f.containers[i].State = "exited"
		}
	}
	return nil
}

type fakeDeps struct {
	c   Composer
	eng engine.Engine
	st  *storage.Result
}

func (d fakeDeps) Composer() Composer       { return d.c }
func (d fakeDeps) Engine() engine.Engine    { return d.eng }
func (d fakeDeps) Storage() *storage.Result { return d.st }

type env struct {
	root string
	svc  *Service
	c    *fakeComposer
	eng  *fakeEngine
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	res := &storage.Result{StacksDir: filepath.ToSlash(root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(root), OK: true}}}
	e := &env{root: root, c: &fakeComposer{}, eng: &fakeEngine{images: map[string]engine.ImageDetails{}}}
	e.svc = New(Options{Deps: fakeDeps{c: e.c, eng: e.eng, st: res}, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	return e
}

func ref(dir string) protocol.ProjectRef {
	return protocol.ProjectRef{Root: protocol.RootStacks, Dir: dir, ProjectName: dir}
}

func call[T any](t *testing.T, h session.RequestHandler, in any) (T, error) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h(testutil.Context(t), b)
	var zero T
	if err != nil {
		return zero, err
	}
	// Round-trip like the session does.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v, nil
}

func code(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const appYAML = `services:
  db:
    image: registry.example:5000/db:${DB_TAG}
    env_file: [db.env]
    labels:
      dev.neureka.docker-manager.description: "Primary database"
      dev.neureka.docker-manager.icon: database
    healthcheck:
      test: ["CMD", "/check"]
  web:
    image: nginx:1.27
    depends_on:
      db:
        condition: service_healthy
    volumes:
      - ./html:/usr/share/nginx/html:ro
`

func TestResolveStaysInVerifiedRoots(t *testing.T) {
	e := newEnv(t)
	h := e.svc.Requests()[protocol.ReqComposeRead]
	for name, r := range map[string]protocol.ProjectRef{
		"escape":         {Root: protocol.RootStacks, Dir: "../etc", ProjectName: "x"},
		"absolute":       {Root: protocol.RootStacks, Dir: "/etc", ProjectName: "x"},
		"root itself":    {Root: protocol.RootStacks, Dir: ".", ProjectName: "x"},
		"unknown bind":   {Root: protocol.RootBind, RootPath: "/opt/stacks", Dir: "app", ProjectName: "app"},
		"bad name":       {Root: protocol.RootStacks, Dir: "app", ProjectName: "App!"},
		"escaping files": {Root: protocol.RootStacks, Dir: "app", ProjectName: "app", ConfigFiles: []string{"../x.yaml"}},
	} {
		_, err := call[protocol.ComposeReadOutput](t, h, protocol.ComposeReadInput{Stack: r})
		if c := code(err); c != protocol.CodeInvalidFrame && c != protocol.CodeForbiddenPath {
			t.Errorf("%s: error %v", name, err)
		}
	}
	// An unverified storage layout refuses everything.
	e.svc.opts.Deps = fakeDeps{c: e.c, eng: e.eng}
	if _, err := call[protocol.ComposeReadOutput](t, h, protocol.ComposeReadInput{Stack: ref("app")}); code(err) != protocol.CodeForbiddenPath {
		t.Errorf("unverified storage: %v", err)
	}
}

func TestResolveRefusesSymlinkOutOfRoot(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.root, "app")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := call[protocol.ComposeReadOutput](t, e.svc.Requests()[protocol.ReqComposeRead], protocol.ComposeReadInput{Stack: ref("app")})
	if code(err) != protocol.CodeForbiddenPath {
		t.Errorf("symlinked project directory: %v", err)
	}
}

func TestWriteCreateNeverOverwrites(t *testing.T) {
	e := newEnv(t)
	h := e.svc.Requests()[protocol.ReqComposeWrite]
	files := []protocol.SourceFile{{Path: "compose.yaml", Content: []byte("services:\n  web:\n    image: nginx\n")},
		{Path: ".env", Content: []byte("TAG=1\n")}}
	out, err := call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("shop"), Mode: protocol.WriteCreate, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if want := protocol.NewSourceSnapshot(files).Hash; out.Snapshot.Hash != want || len(out.Snapshot.Files) != 2 {
		t.Errorf("snapshot %+v, want hash %s", out.Snapshot, want)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(e.root, "shop", ".env")); st.Mode().Perm() != 0o600 {
			t.Errorf(".env mode %v, want 0600", st.Mode().Perm())
		}
	}
	// A second create of the same directory is a conflict; the bytes stay.
	_, err = call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("shop"), Mode: protocol.WriteCreate,
		Files: []protocol.SourceFile{{Path: "compose.yaml", Content: []byte("services: {}\n")}}})
	if code(err) != protocol.CodeConflict {
		t.Fatalf("second create: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "shop", "compose.yaml")); !bytes.Equal(b, files[0].Content) {
		t.Error("an existing project was overwritten")
	}
	// A path that exists as a file is a conflict as well.
	writeTree(t, e.root, map[string]string{"file": "x"})
	if _, err := call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("file"), Mode: protocol.WriteCreate, Files: files}); code(err) != protocol.CodeConflict {
		t.Errorf("create over a file: %v", err)
	}
}

func TestWriteReplaceNeedsExpectedHash(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.root, "shop")
	writeTree(t, dir, map[string]string{
		"compose.yaml":          "services:\n  web:\n    image: nginx:1\n",
		"compose.override.yaml": "services:\n  web:\n    environment:\n      A: b\n",
		"html/index.html":       "not a definition file",
	})
	read := e.svc.Requests()[protocol.ReqComposeRead]
	cur, err := call[protocol.ComposeReadOutput](t, read, protocol.ComposeReadInput{Stack: ref("shop")})
	if err != nil || cur.Missing || len(cur.Snapshot.Files) != 2 {
		t.Fatalf("read %+v %v", cur, err)
	}
	h := e.svc.Requests()[protocol.ReqComposeWrite]
	restored := []protocol.SourceFile{{Path: "compose.yaml", Content: []byte("services:\n  web:\n    image: nginx:0\n")}}
	// Stale hash: conflict, nothing written.
	if _, err := call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("shop"), Mode: protocol.WriteReplace,
		Files: restored, ExpectHash: "0000"}); code(err) != protocol.CodeConflict {
		t.Fatalf("stale hash: %v", err)
	}
	// Only definition files can be removed.
	if _, err := call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("shop"), Mode: protocol.WriteReplace,
		Files: restored, ExpectHash: cur.Snapshot.Hash, Remove: []string{"html/index.html"}}); code(err) != protocol.CodeForbiddenPath {
		t.Fatalf("removing a non-definition file: %v", err)
	}
	out, err := call[protocol.ComposeWriteOutput](t, h, protocol.ComposeWriteInput{Stack: ref("shop"), Mode: protocol.WriteReplace,
		Files: restored, ExpectHash: cur.Snapshot.Hash, Remove: []string{"compose.override.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Snapshot.Hash != protocol.NewSourceSnapshot(restored).Hash {
		t.Errorf("after restore %+v", out.Snapshot)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.override.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Error("override not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "html", "index.html")); err != nil {
		t.Error("a non-definition file was touched")
	}
}

func TestReadDefinition(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.root, "app")
	writeTree(t, dir, map[string]string{
		"compose.yaml":          appYAML,
		"compose.override.yaml": "services:\n  web:\n    env_file: [../shared.env]\n",
		".env":                  "DB_TAG=16\n",
		"db.env":                "POSTGRES_PASSWORD=pw\n",
	})
	writeTree(t, e.root, map[string]string{"shared.env": "X=1\n"})
	out, err := call[protocol.ComposeReadOutput](t, e.svc.Requests()[protocol.ReqComposeRead], protocol.ComposeReadInput{Stack: ref("app")})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range out.Snapshot.Files {
		paths = append(paths, f.Path)
		if f.SHA256 != protocol.FileHash(f.Content) {
			t.Errorf("%s: hash mismatch", f.Path)
		}
	}
	if want := []string{".env", "compose.override.yaml", "compose.yaml", "db.env"}; !slices.Equal(paths, want) {
		t.Errorf("definition files %v, want %v (the env_file outside the project is not part of it)", paths, want)
	}
	// A broken definition can still be read (and recorded as a revision).
	writeTree(t, dir, map[string]string{"compose.yaml": "services: [broken"})
	out, err = call[protocol.ComposeReadOutput](t, e.svc.Requests()[protocol.ReqComposeRead], protocol.ComposeReadInput{Stack: ref("app")})
	if err != nil || out.Missing || !slices.ContainsFunc(out.Snapshot.Files, func(f protocol.SourceFile) bool { return f.Path == "compose.yaml" }) {
		t.Errorf("broken definition: %+v %v", out, err)
	}
	// A project directory that does not exist is reported missing.
	out, err = call[protocol.ComposeReadOutput](t, e.svc.Requests()[protocol.ReqComposeRead], protocol.ComposeReadInput{Stack: ref("gone")})
	if err != nil || !out.Missing {
		t.Errorf("missing project: %+v %v", out, err)
	}
}

func TestValidateInMemory(t *testing.T) {
	e := newEnv(t)
	h := e.svc.Requests()[protocol.ReqComposeValidate]
	content := "version: \"3.9\"\n" + appYAML + "  ext:\n    image: busybox\n    volumes:\n      - /srv/data:/data\n"
	// db.env must exist on disk: service env_files are not in-memory inputs.
	writeTree(t, filepath.Join(e.root, "app"), map[string]string{"db.env": "A=1\n"})
	out, err := call[protocol.ComposeValidateOutput](t, h, protocol.ComposeValidateInput{Stack: ref("app"),
		Files: []protocol.SourceFile{{Path: "compose.yaml", Content: []byte(content)}, {Path: ".env", Content: []byte("DB_TAG=16\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Valid || out.ProjectName != "app" || len(out.Services) != 3 {
		t.Fatalf("output %+v", out)
	}
	var codes []string
	for _, w := range out.Warnings {
		codes = append(codes, w.Code)
	}
	slices.Sort(codes)
	if !slices.Equal(codes, []string{protocol.IssueBindOutsideProject, protocol.IssueObsoleteVersion}) {
		t.Errorf("warnings %+v", out.Warnings)
	}
	var html, ext protocol.ComposeBind
	for _, b := range out.Binds {
		switch b.Target {
		case "/usr/share/nginx/html":
			html = b
		case "/data":
			ext = b
		}
	}
	if html.RelPath != "html" || html.External || !html.ReadOnly || !ext.External || ext.RelPath != "" {
		t.Errorf("binds %+v", out.Binds)
	}
	for _, s := range out.Services {
		if s.Name == "db" && (s.Description != "Primary database" || s.Icon != "database" || s.Image != "registry.example:5000/db:16") {
			t.Errorf("db %+v: .env interpolation and display labels", s)
		}
	}
	// Nothing was written.
	if _, err := os.Stat(filepath.Join(e.root, "app", "compose.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Error("validation wrote a file")
	}

	bad := map[string]string{
		"services: [": protocol.IssueInvalidProject,
		"services:\n  x:\n    image: a\n    use_api_socket: true\n": protocol.IssueUnsupportedFeature,
	}
	for src, want := range bad {
		out, err := call[protocol.ComposeValidateOutput](t, h, protocol.ComposeValidateInput{Stack: ref("app"),
			Files: []protocol.SourceFile{{Path: "compose.yaml", Content: []byte(src)}}})
		if err != nil || out.Valid || len(out.Errors) != 1 || out.Errors[0].Code != want {
			t.Errorf("%q: %+v %v", src, out, err)
		}
	}
}

func TestDiscoverProjects(t *testing.T) {
	e := newEnv(t)
	root := filepath.ToSlash(e.root)
	lbl := func(project, svc, wd, files string) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: project, lifecycle.ComposeServiceLabel: svc,
			labelWorkingDir: wd, labelConfigFiles: files}
	}
	e.eng.containers = []engine.Container{
		{ID: "1", Names: []string{"/shop-web-1"}, Image: "nginx", State: "running", Labels: lbl("shop", "web", root+"/shop", root+"/shop/compose.yaml")},
		{ID: "2", Names: []string{"/shop-db-1"}, Image: "pg", State: "exited", Labels: lbl("shop", "db", root+"/shop", root+"/shop/compose.yaml")},
		{ID: "3", Names: []string{"/legacy-app-1"}, Image: "app", State: "running", Labels: lbl("legacy", "app", "/home/me/legacy", "/home/me/legacy/docker-compose.yml")},
		{ID: "4", Names: []string{"/split-app-1"}, Image: "app", State: "running", Labels: lbl("split", "app", root+"/split", "/etc/compose.yaml")},
		{ID: "5", Names: []string{"/shop-web-run-1"}, Image: "nginx", State: "running",
			Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web", lifecycle.ComposeOneoffLabel: "True"}},
	}
	out, err := call[protocol.ComposeDiscoverOutput](t, e.svc.Requests()[protocol.ReqComposeDiscover], struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 3 {
		t.Fatalf("projects %+v", out.Projects)
	}
	by := map[string]protocol.DiscoveredProject{}
	for _, p := range out.Projects {
		by[p.Name] = p
	}
	shop := by["shop"]
	if !shop.Adoptable || shop.Root != protocol.RootStacks || shop.Dir != "shop" || len(shop.Services) != 2 ||
		shop.Services[1].Name != "web" || shop.Services[1].Running != 1 || shop.Services[1].Containers != 1 {
		t.Errorf("shop %+v", shop)
	}
	if l := by["legacy"]; l.Adoptable || !strings.Contains(l.Reason, "outside the stacks volume") {
		t.Errorf("legacy %+v", l)
	}
	if s := by["split"]; s.Adoptable || !strings.Contains(s.Reason, "/etc/compose.yaml") {
		t.Errorf("split %+v", s)
	}
}

func TestServicesReportsLiveState(t *testing.T) {
	e := newEnv(t)
	e.eng.containers = []engine.Container{
		{ID: "w1", Names: []string{"/shop-web-1"}, Image: "nginx:1", ImageID: "sha256:img", State: "running", Health: "healthy",
			Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web"}},
	}
	out, err := call[protocol.ComposeServicesOutput](t, e.svc.Requests()[protocol.ReqComposeServices], protocol.ComposeServicesInput{ProjectName: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Containers) != 1 {
		t.Fatalf("containers %+v", out.Containers)
	}
	c := out.Containers[0]
	if c.Service != "web" || c.Health != "healthy" || c.RestartPolicy != "unless-stopped" || c.Resources.Memory != 256<<20 ||
		len(c.Ports) != 1 || c.Ports[0].PublicPort != 8080 {
		t.Errorf("container %+v", c)
	}
}

// memJournal keeps journal entries in memory.
type memJournal struct{ saves int }

func (m *memJournal) Save(context.Context, *jobexec.State) error { m.saves++; return nil }

func run(t *testing.T, s *Service, kind domain.JobKind, in protocol.StackJobInput) (protocol.ResultPayload, protocol.StackJobOutput) {
	t.Helper()
	return runWith(t, s, kind, in, nil)
}

func runWith(t *testing.T, s *Service, kind domain.JobKind, in protocol.StackJobInput, secrets *protocol.CommandSecrets) (protocol.ResultPayload, protocol.StackJobOutput) {
	t.Helper()
	var exec jobexec.Executor
	for _, x := range s.Executors() {
		if x.Kind == kind {
			exec = x
		}
	}
	if err := exec.Validate(domain.ExecutorAgent); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(in)
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: kind, Input: b, Secrets: secrets}
	res, err := jobexec.Run(testutil.Context(t), exec, st, jobexec.Options{Journal: &memJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	var out protocol.StackJobOutput
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

type fileState struct {
	content []byte
	mod     time.Time
}

func snapshotTree(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fileState{b, info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func deployFixture(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	dir := filepath.Join(e.root, "app")
	writeTree(t, dir, map[string]string{
		"compose.yaml": appYAML, ".env": "DB_TAG=16\n", "db.env": "POSTGRES_PASSWORD=pw\n", "html/index.html": "hi",
	})
	lbl := func(svc string) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: svc,
			lifecycle.DependsOnLabel: map[string]string{"web": "db:service_healthy:false:true"}[svc]}
	}
	e.c.onUp = func(*compose.Project) {
		e.eng.mu.Lock()
		defer e.eng.mu.Unlock()
		e.eng.containers = []engine.Container{
			{ID: "db1", Names: []string{"/app-db-1"}, ImageID: "sha256:db", State: "running", Health: "healthy", Labels: lbl("db")},
			{ID: "web1", Names: []string{"/app-web-1"}, ImageID: "sha256:web", State: "running", Labels: lbl("web")},
		}
	}
	e.eng.images["sha256:web"] = engine.ImageDetails{ID: "sha256:web", OS: "linux", Architecture: "arm64", Variant: "v8",
		RepoDigests: []string{"nginx@sha256:1111"}}
	e.eng.images["sha256:db"] = engine.ImageDetails{ID: "sha256:db", OS: "linux", Architecture: "arm64",
		RepoDigests: []string{"registry.example:5000/db@sha256:2222", "mirror.example/db@sha256:3333"}}
	return e, dir
}

func TestDeployUsesAndReportsOnDiskBytesWithoutModifyingThem(t *testing.T) {
	e, dir := deployFixture(t)
	before := snapshotTree(t, dir)
	res, out := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{StackID: "s1", Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	after := snapshotTree(t, dir)
	for p, st := range before {
		if a := after[p]; !bytes.Equal(a.content, st.content) || !a.mod.Equal(st.mod) {
			t.Errorf("%s was modified by the deploy", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("the deploy created files: %d -> %d", len(before), len(after))
	}
	// The reported sources are exactly the on-disk definition.
	want := protocol.NewSourceSnapshot([]protocol.SourceFile{
		{Path: ".env", Content: []byte("DB_TAG=16\n")}, {Path: "compose.yaml", Content: []byte(appYAML)},
		{Path: "db.env", Content: []byte("POSTGRES_PASSWORD=pw\n")}})
	if out.Sources == nil || out.Sources.Hash != want.Hash || out.Sources.ContentOmitted || len(out.Sources.Files[1].Content) == 0 {
		t.Fatalf("sources %+v, want hash %s", out.Sources, want.Hash)
	}
	if !slices.Equal(e.c.calls, []string{"up:app"}) {
		t.Errorf("compose calls %v: pull missing / build missing happen inside up", e.c.calls)
	}
	img := map[string]protocol.AppliedImage{}
	for _, i := range out.Images {
		img[i.Service] = i
	}
	if img["web"].Digest != "sha256:1111" || img["web"].Platform != "linux/arm64/v8" ||
		img["db"].Digest != "sha256:2222" || img["db"].Image != "registry.example:5000/db:16" {
		t.Errorf("images %+v", out.Images)
	}
	if len(out.Before) != 0 || len(out.After) != 2 || out.After[1].Running != 1 {
		t.Errorf("before %+v after %+v", out.Before, out.After)
	}
	if len(out.Binds) != 1 || out.Binds[0].RelPath != "html" {
		t.Errorf("binds %+v", out.Binds)
	}
}

func TestDeployWithPullAndBuild(t *testing.T) {
	e, _ := deployFixture(t)
	// No service has a build section: nothing is built (build_test.go
	// covers builds through the real adapter).
	res, _ := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app"), Pull: "always", Build: true})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(e.c.calls, []string{"pull:app", "up:app"}) {
		t.Errorf("result %+v calls %v", res, e.c.calls)
	}
	writeTree(t, filepath.Join(e.root, "app"), map[string]string{"compose.yaml": appYAML + "  api:\n    build: ./api\n"})
	e.c.calls = nil
	res, _ = run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app"), Pull: "always", Build: true})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(e.c.calls, []string{"pull:app", "build:app", "up:app"}) {
		t.Errorf("result %+v calls %v", res, e.c.calls)
	}
}

func TestFailedDeployKeepsRecoveryData(t *testing.T) {
	e, _ := deployFixture(t)
	// The stack was running an older version before the failed deploy.
	e.eng.containers = []engine.Container{{ID: "old", Names: []string{"/app-web-1"}, ImageID: "sha256:old", State: "running",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: "web"}}}
	e.c.upErr = engine.Errorf("compose.up", engine.CodeDependencyFailed, "dependency failed to start: container app-db-1 is unhealthy")
	res, out := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "unhealthy") || res.ErrorClass != "dependency_failed" ||
		!strings.Contains(res.Recovery, "restore the last applied revision") {
		t.Fatalf("result %+v", res)
	}
	if out.Sources == nil || len(out.Before) != 1 || out.Before[0].ImageIDs[0] != "sha256:old" || len(out.After) == 0 {
		t.Errorf("output %+v: a failed deploy reports the sources it tried, the previous images and the state after", out)
	}
}

func TestDeployRefusesInvalidProject(t *testing.T) {
	e := newEnv(t)
	writeTree(t, filepath.Join(e.root, "app"), map[string]string{"compose.yaml": "services:\n  x:\n    image: a\n    use_api_socket: true\n"})
	res, _ := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "use_api_socket") || len(e.c.calls) != 0 ||
		res.ErrorClass != "unsupported_compose_feature" {
		t.Errorf("result %+v calls %v", res, e.c.calls)
	}
}

func TestStackStopUsesDeployedGraph(t *testing.T) {
	e, _ := deployFixture(t)
	e.c.onUp(nil)
	res, out := run(t, e.svc, jobspec.StackStop, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	if want := []string{"stop:web1", "stop:db1"}; !slices.Equal(e.eng.calls, want) {
		t.Errorf("calls %v, want %v (dependents first)", e.eng.calls, want)
	}
	if len(out.Before) != 2 || out.Before[0].Running != 1 || out.After[0].Running != 0 {
		t.Errorf("before %+v after %+v", out.Before, out.After)
	}
	// Starting brings db up before web (db reports healthy when running).
	e.eng.calls = nil
	res, _ = run(t, e.svc, jobspec.StackStart, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(e.eng.calls, []string{"start:db1", "start:web1"}) {
		t.Errorf("start %+v calls %v", res, e.eng.calls)
	}
}

func TestStackDownAndRemove(t *testing.T) {
	e, _ := deployFixture(t)
	res, _ := run(t, e.svc, jobspec.StackDown, protocol.StackJobInput{Stack: ref("app")})
	// Nothing deployed: down is a no-op.
	if res.Outcome != jobexec.OutcomeSucceeded || len(e.c.calls) != 0 {
		t.Errorf("down without containers: %+v %v", res, e.c.calls)
	}
	e.c.onUp(nil)
	res, _ = run(t, e.svc, jobspec.StackRemove, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeSucceeded || !slices.Equal(e.c.calls, []string{"down:app"}) {
		t.Errorf("remove %+v %v", res, e.c.calls)
	}
}

func TestDeployUsesTheCommandsRegistryCredentials(t *testing.T) {
	e, _ := deployFixture(t)
	in := protocol.StackJobInput{Stack: ref("app"), RegistryConnections: []string{"reg-1"}}
	// The input named a connection but the command carries no credential:
	// fail instead of pulling anonymously.
	res, _ := runWith(t, e.svc, jobspec.StackDeploy, in, nil)
	if res.Outcome != jobexec.OutcomeFailed || !strings.Contains(res.Message, "no credential") || len(e.c.calls) != 0 ||
		res.ErrorClass != "credential_unavailable" {
		t.Fatalf("without secrets: %+v %v", res, e.c.calls)
	}
	secrets := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "reg-1", Host: "registry.example:5000",
		Username: "bot", Secret: "registry-canary"}}}
	res, out := runWith(t, e.svc, jobspec.StackDeploy, in, secrets)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("with secrets: %+v", res)
	}
	if len(e.c.upAuth) != 1 || e.c.upAuth[0].ServerAddress != "registry.example:5000" || e.c.upAuth[0].Username != "bot" ||
		string(e.c.upAuth[0].Password) != "registry-canary" {
		t.Errorf("auth passed to up: %+v", e.c.upAuth)
	}
	// Credentials never appear in the result.
	if b, _ := json.Marshal(out); strings.Contains(string(b), "registry-canary") || strings.Contains(string(res.Message), "registry-canary") {
		t.Error("a credential leaked into the result")
	}
}

func TestDigestFor(t *testing.T) {
	cases := []struct {
		ref     string
		digests []string
		want    string
	}{
		{"nginx:1.27", []string{"nginx@sha256:a"}, "sha256:a"},
		{"docker.io/library/nginx:1.27", []string{"nginx@sha256:a"}, "sha256:a"},
		{"nginx", []string{"docker.io/library/nginx@sha256:a"}, "sha256:a"},
		{"ghcr.io/o/app:1", []string{"nginx@sha256:a", "ghcr.io/o/app@sha256:b"}, "sha256:b"},
		{"registry:5000/app", []string{"registry:5000/app@sha256:c"}, "sha256:c"},
		{"app@sha256:pinned", []string{"app@sha256:other"}, "sha256:pinned"},
		{"local/built:dev", nil, ""},
	}
	for _, c := range cases {
		if got := DigestFor(c.ref, c.digests); got != c.want {
			t.Errorf("DigestFor(%q, %v) = %q, want %q", c.ref, c.digests, got, c.want)
		}
	}
}
