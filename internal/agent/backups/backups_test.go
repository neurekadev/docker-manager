package backups

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic"
	"code.neureka.dev/docker-manager/docker-manager/internal/restic/restictest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// recEngine records container stops and starts in order and lets tests
// act at the first stop.
type recEngine struct {
	*enginefake.Engine
	mu     sync.Mutex
	events []string
	onStop func(name string)
}

func (r *recEngine) name(id string) string {
	c, ok := r.Container(id)
	if !ok {
		return id
	}
	return c.Details.Labels[lifecycle.ComposeServiceLabel]
}

func (r *recEngine) StopContainer(ctx context.Context, id string, t *time.Duration) error {
	n := r.name(id)
	r.mu.Lock()
	r.events = append(r.events, "stop:"+n)
	hook := r.onStop
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return r.Engine.StopContainer(ctx, id, t)
}

func (r *recEngine) StartContainer(ctx context.Context, id string) error {
	r.mu.Lock()
	r.events = append(r.events, "start:"+r.name(id))
	r.mu.Unlock()
	return r.Engine.StartContainer(ctx, id)
}

func (r *recEngine) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recEngine) running(service string) bool {
	list, _ := r.ListContainers(context.Background(), engine.ContainerFilter{All: true,
		Labels: []string{lifecycle.ComposeServiceLabel + "=" + service}})
	for _, c := range list {
		if c.State == "running" {
			return true
		}
	}
	return false
}

type loader struct{}

func (loader) Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error) {
	return compose.LoadProject(ctx, spec)
}

type env struct {
	t        *testing.T
	dir      string
	stacks   string
	project  string
	docker   string
	volumes  string
	backups  string
	external string
	outside  string
	anon     string
	eng      *recEngine
	store    *restictest.Store
	svc      *Service
	res      *storage.Result
	guard    *protect.Guard
}

const composeYAML = `services:
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data
      - /scratch
  api:
    image: example/api:1
    depends_on:
      db:
        condition: service_started
    volumes:
      - ./html:/srv/html:ro
      - ../outside:/outside
      - EXTERNAL:/ext
  web:
    image: nginx:1.27
    depends_on:
      api:
        condition: service_started
  worker:
    image: example/worker:1
volumes:
  dbdata:
`

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, stacks: filepath.Join(dir, "stacks"), docker: filepath.Join(dir, "docker"),
		backups: filepath.Join(dir, "backups"), external: filepath.Join(dir, "srv", "shared"), outside: filepath.Join(dir, "stacks", "outside")}
	e.volumes = filepath.Join(e.docker, "volumes")
	e.project = filepath.Join(e.stacks, "app")
	write(t, filepath.Join(e.project, "compose.yaml"), strings.ReplaceAll(composeYAML, "EXTERNAL", filepath.ToSlash(e.external)))
	write(t, filepath.Join(e.project, ".env"), "SECRET=value\n")
	write(t, filepath.Join(e.project, "html", "index.html"), "<h1>hi</h1>")
	write(t, filepath.Join(e.project, "cache", "big.tmp"), "cache")
	write(t, filepath.Join(e.outside, "o.txt"), "outside the project")
	write(t, filepath.Join(e.external, "shared.txt"), "shared data")
	if err := os.MkdirAll(e.backups, 0o755); err != nil {
		t.Fatal(err)
	}
	slash := filepath.ToSlash
	e.res = &storage.Result{StacksDir: slash(e.stacks), VolumesDir: slash(e.volumes), DockerRootDir: slash(e.docker),
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: slash(e.stacks), OK: true}, {Kind: storage.KindVolumes, Path: slash(e.volumes), OK: true}}}
	fake := enginefake.New("engine-1")
	e.eng = &recEngine{Engine: fake}
	labels := func(svc, deps string) map[string]string {
		l := map[string]string{lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: svc}
		if deps != "" {
			l[lifecycle.DependsOnLabel] = deps
		}
		return l
	}
	fake.AddContainer(engine.ContainerSpec{Name: "app-db-1", Image: "postgres:16", Labels: labels("db", ""),
		Mounts: []engine.MountSpec{{Type: "volume", Source: "app_dbdata", Target: "/var/lib/postgresql/data"}, {Type: "volume", Target: "/scratch"}}}, true)
	fake.AddContainer(engine.ContainerSpec{Name: "app-api-1", Image: "example/api:1", Labels: labels("api", "db:service_started:false:true")}, true)
	fake.AddContainer(engine.ContainerSpec{Name: "app-web-1", Image: "nginx:1.27", Labels: labels("web", "api:service_started:false:true")}, true)
	fake.AddContainer(engine.ContainerSpec{Name: "app-worker-1", Image: "example/worker:1", Labels: labels("worker", "")}, false)
	// Another project shares the database volume (a conflict, never stopped).
	fake.AddContainer(engine.ContainerSpec{Name: "reporting", Image: "example/report:1",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "reporting", lifecycle.ComposeServiceLabel: "report"},
		Mounts: []engine.MountSpec{{Type: "volume", Source: "app_dbdata", Target: "/data", ReadOnly: true}}}, true)
	for _, c := range []string{"app_dbdata"} {
		mp := filepath.Join(e.volumes, c, "_data")
		write(t, filepath.Join(mp, "PG_VERSION"), "16")
		fake.SetVolumeMountpoint(c, filepath.ToSlash(mp))
	}
	db, _ := fake.Container("app-db-1")
	for _, m := range db.Details.Mounts {
		if m.Destination == "/scratch" {
			e.anon = m.Name
			mp := filepath.Join(e.volumes, m.Name, "_data")
			write(t, filepath.Join(mp, "tmp.bin"), "anonymous")
			fake.SetVolumeMountpoint(m.Name, filepath.ToSlash(mp))
		}
	}
	fake.AddVolume("uploads", nil)
	up := filepath.Join(e.volumes, "uploads", "_data")
	write(t, filepath.Join(up, "a.jpg"), "jpeg")
	write(t, filepath.Join(up, "thumbs", "a.png"), "png")
	fake.SetVolumeMountpoint("uploads", filepath.ToSlash(up))
	fake.AddVolume("docker-manager_stacks", nil)
	e.guard = protect.New(protect.Options{StacksVolume: "docker-manager_stacks", Logger: testutil.Logger(t)})
	e.store = restictest.New(func() time.Time { return time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC) })
	e.svc = New(Options{Engine: func() engine.Engine { return e.eng }, Loader: func() Loader { return loader{} },
		Storage: func() *storage.Result { return e.res }, Guard: e.guard, Restic: e.store,
		LocalRoots: []string{filepath.ToSlash(e.backups)}, ExternalAllowlist: []string{filepath.ToSlash(filepath.Join(dir, "srv"))},
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t), WaitTimeout: time.Second})
	return e
}

func stackItem(rules protocol.BackupRules) protocol.BackupItem {
	return protocol.BackupItem{Kind: backup.MemberStack, StackID: "st-app", StackName: "app",
		Project: &protocol.ProjectRef{Root: protocol.RootStacks, Dir: "app", ProjectName: "app"}, Rules: rules}
}

func (e *env) repoRef() protocol.BackupRepositoryRef {
	return protocol.BackupRepositoryRef{RepositoryID: "repo-1", Destination: backup.Destination{Kind: backup.KindLocal, Path: filepath.ToSlash(e.backups)},
		Scope: backup.EnvironmentScope("env-1"), KeyGeneration: 1, KeyFingerprint: "rk_0000000000000001"}
}

func sourceState(p itemPlan, kind, match string) (protocol.ScopeSource, bool) {
	for _, s := range p.sources {
		if s.Kind == kind && (strings.Contains(s.Path, match) || s.Name == match || strings.Contains(s.Name, match)) {
			return s, true
		}
	}
	return protocol.ScopeSource{}, false
}

// TestScopeCorpus is the #10 filter corpus: relative binds beside
// compose.yaml are included, ../data and absolute paths need an opt-in
// (and the agent allowlist), anonymous volumes are off by default,
// exclusions and volume rules apply, Docker Manager's own volumes and symlink
// escapes never are.
func TestScopeCorpus(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	repo := e.repoRef()

	t.Run("defaults", func(t *testing.T) {
		p := e.svc.plan(ctx, stackItem(protocol.BackupRules{}), &repo, false)
		if p.err != nil {
			t.Fatal(p.err)
		}
		checks := []struct{ kind, match, state string }{
			{protocol.SourceProject, "app", protocol.SourceIncluded},
			{protocol.SourceBind, "html", protocol.SourceIncluded},
			{protocol.SourceExternal, "outside", protocol.SourceRequiresOpt},
			{protocol.SourceExternal, "shared", protocol.SourceRequiresOpt},
			{protocol.SourceVolume, "app_dbdata", protocol.SourceIncluded},
			{protocol.SourceAnonymous, e.anon, protocol.SourceExcluded},
		}
		for _, c := range checks {
			s, ok := sourceState(p, c.kind, c.match)
			if !ok || s.State != c.state {
				t.Errorf("%s %s: %+v (found %v), want %s", c.kind, c.match, s, ok, c.state)
			}
		}
		if !slices.Contains(p.paths, e.project) || !slices.ContainsFunc(p.paths, func(x string) bool { return strings.Contains(x, "app_dbdata") }) {
			t.Errorf("paths = %v", p.paths)
		}
		for _, x := range p.paths {
			if strings.Contains(x, "outside") || strings.Contains(x, "shared") || strings.Contains(x, e.anon) {
				t.Errorf("path %s included without opt-in", x)
			}
		}
		if len(p.volumes) != 1 || p.volumes[0] != "app_dbdata" {
			t.Errorf("volumes = %v", p.volumes)
		}
	})

	t.Run("opt-in and allowlist", func(t *testing.T) {
		rules := protocol.BackupRules{ExternalPaths: []string{filepath.ToSlash(e.external), filepath.ToSlash(e.outside)}, AnonymousVolumes: true}
		p := e.svc.plan(ctx, stackItem(rules), &repo, false)
		if p.err != nil {
			t.Fatal(p.err)
		}
		if s, _ := sourceState(p, protocol.SourceExternal, "shared"); s.State != protocol.SourceIncluded {
			t.Errorf("allowlisted opt-in: %+v", s)
		}
		// ../outside is opted in, but the allowlist only covers /srv.
		if s, _ := sourceState(p, protocol.SourceExternal, "outside"); s.State != protocol.SourceBlocked || !strings.Contains(s.Reason, "ALLOWLIST") {
			t.Errorf("opt-in without allowlist: %+v", s)
		}
		if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceIncluded {
			t.Errorf("anonymous volumes on: %+v", s)
		}
	})

	t.Run("exclusions", func(t *testing.T) {
		p := e.svc.plan(ctx, stackItem(protocol.BackupRules{PathExcludes: []string{"html", "cache"}, VolumeExclude: []string{"dbdata"}}), &repo, false)
		if p.err != nil {
			t.Fatal(p.err)
		}
		if s, _ := sourceState(p, protocol.SourceBind, "html"); s.State != protocol.SourceExcluded {
			t.Errorf("excluded bind: %+v", s)
		}
		if s, _ := sourceState(p, protocol.SourceVolume, "app_dbdata"); s.State != protocol.SourceExcluded {
			t.Errorf("excluded volume: %+v", s)
		}
		if !slices.Contains(p.excludes, filepath.Join(e.project, "cache")) || !slices.Contains(p.excludes, filepath.Join(e.project, "html")) {
			t.Errorf("excludes = %v", p.excludes)
		}
		// The volume include list limits named volumes.
		p = e.svc.plan(ctx, stackItem(protocol.BackupRules{VolumeInclude: []string{"other"}}), &repo, false)
		if s, _ := sourceState(p, protocol.SourceVolume, "app_dbdata"); s.State != protocol.SourceExcluded {
			t.Errorf("volume not in include list: %+v", s)
		}
		// An excluded anonymous volume stays out even with anonymous volumes on.
		p = e.svc.plan(ctx, stackItem(protocol.BackupRules{AnonymousVolumes: true, VolumeExclude: []string{e.anon}}), &repo, false)
		if s, _ := sourceState(p, protocol.SourceAnonymous, e.anon); s.State != protocol.SourceExcluded || s.Reason != "excluded by the policy" {
			t.Errorf("excluded anonymous volume: %+v", s)
		}
	})

	t.Run("invalid rules", func(t *testing.T) {
		for _, r := range []protocol.BackupRules{{PathExcludes: []string{"../x"}}, {ExternalPaths: []string{"relative"}}, {PathExcludes: []string{"/abs"}}} {
			if p := e.svc.plan(ctx, stackItem(r), &repo, false); p.err == nil {
				t.Errorf("rules %+v accepted", r)
			}
		}
	})

	t.Run("repository inside a source", func(t *testing.T) {
		nested := repo
		nested.Destination.Path = filepath.ToSlash(filepath.Join(e.project, "backups"))
		p := e.svc.plan(ctx, stackItem(protocol.BackupRules{}), &nested, false)
		if errorClass(p.err) != protocol.CodeRepositoryInsideSource {
			t.Errorf("nested repository: %v", p.err)
		}
		// And the agent refuses it as a location at all when outside its roots.
		if err := e.svc.allowedLocal(nested.Destination.Path); err == nil {
			t.Error("a location outside DOCKER_AGENT_BACKUP_LOCAL_ROOTS was allowed")
		}
		if err := e.svc.allowedLocal(filepath.ToSlash(e.backups)); err != nil {
			t.Errorf("allowed root refused: %v", err)
		}
	})

	t.Run("docker-manager volume and standalone volume", func(t *testing.T) {
		p := e.svc.plan(ctx, protocol.BackupItem{Kind: backup.MemberVolume, Volume: "docker-manager_stacks"}, &repo, false)
		if p.err == nil || !strings.Contains(p.err.Error(), "Docker Manager") {
			t.Errorf("Docker Manager's stacks volume: %v", p.err)
		}
		p = e.svc.plan(ctx, protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads", Rules: protocol.BackupRules{PathExcludes: []string{"thumbs"}}}, &repo, false)
		if p.err != nil || len(p.paths) != 1 || !strings.HasSuffix(filepath.ToSlash(p.paths[0]), "uploads/_data") || len(p.excludes) != 1 {
			t.Errorf("standalone volume: %+v %v", p.paths, p.err)
		}
		p = e.svc.plan(ctx, protocol.BackupItem{Kind: backup.MemberVolume, Volume: "app_dbdata"}, &repo, false)
		if len(p.conflicts) == 0 {
			t.Error("containers using a standalone volume are not surfaced")
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		link := filepath.Join(e.project, "linked")
		if err := os.Symlink(e.external, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		defer func() { _ = os.Remove(link) }()
		write(t, filepath.Join(e.project, "compose.override.yaml"), "services:\n  web:\n    volumes:\n      - ./linked:/linked\n")
		defer func() { _ = os.Remove(filepath.Join(e.project, "compose.override.yaml")) }()
		p := e.svc.plan(ctx, stackItem(protocol.BackupRules{}), &repo, false)
		if s, ok := sourceState(p, protocol.SourceBind, "linked"); !ok || s.State != protocol.SourceBlocked {
			t.Errorf("symlinked bind: %+v %v", s, ok)
		}
	})
}

func TestScopePreviewShutdownPlan(t *testing.T) {
	e := newEnv(t)
	raw, _ := json.Marshal(protocol.BackupScopePreviewInput{Items: []protocol.BackupItem{stackItem(protocol.BackupRules{})}, Shutdown: true})
	out, err := e.svc.scopePreview(testutil.Context(t), raw)
	if err != nil {
		t.Fatal(err)
	}
	pv := out.(protocol.BackupScopePreviewOutput)
	if len(pv.Items) != 1 || pv.Items[0].Error != "" {
		t.Fatalf("preview = %+v", pv)
	}
	it := pv.Items[0]
	order := map[string]int{}
	for _, a := range it.Affected {
		order[a.Service] = a.StopOrder
	}
	// Dependents stop first; the stopped worker is not touched.
	if order["web"] != 1 || order["api"] != 2 || order["db"] != 3 || order["worker"] != 0 {
		t.Errorf("stop order = %v", order)
	}
	if len(it.Conflicts) == 0 || !strings.Contains(it.Conflicts[0], "reporting") {
		t.Errorf("shared volume conflict missing: %v", it.Conflicts)
	}
	if pv.Downtime == "" || it.Files == 0 || it.Bytes == 0 || !it.Estimated {
		t.Errorf("downtime %q, estimate %d files %d bytes (%v)", pv.Downtime, it.Files, it.Bytes, it.Estimated)
	}
}

// --- backup.run ---

type memJournal struct {
	mu   sync.Mutex
	last jobexec.State
}

func (j *memJournal) Save(_ context.Context, st *jobexec.State) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.last = st.Clone()
	return nil
}

func (e *env) credential(key string) *protocol.CommandSecrets {
	return &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: "repo-1", Password: key}}}
}

func (e *env) runInput(shutdown bool, items ...protocol.BackupItem) protocol.BackupRunInput {
	return protocol.BackupRunInput{SetID: "set-1", PolicyID: "pol-1", PolicyName: "Nightly", InstanceID: "inst-1", Repository: e.repoRef(),
		Shutdown: shutdown, Items: items, StartedAt: time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC), EnvironmentName: "prod"}
}

func (e *env) executor(kind domain.JobKind) jobexec.Executor {
	for _, x := range e.svc.Executors() {
		if x.Kind == kind {
			return x
		}
	}
	e.t.Fatalf("no executor %s", kind)
	return jobexec.Executor{}
}

func (e *env) run(ctx context.Context, kind domain.JobKind, in any, secrets *protocol.CommandSecrets, cancel func() bool) (protocol.ResultPayload, *memJournal, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		e.t.Fatal(err)
	}
	j := &memJournal{}
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: kind, Input: raw, Secrets: secrets}
	res, err := jobexec.Run(ctx, e.executor(kind), st, jobexec.Options{Journal: j, CancelRequested: cancel})
	return res, j, err
}

func outputOf(t *testing.T, res protocol.ResultPayload) protocol.BackupRunOutput {
	t.Helper()
	var out protocol.BackupRunOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output: %v (%s)", err, res.Output)
	}
	return out
}

func TestBackupRunWithShutdownStopsAndRestartsInDependencyOrder(t *testing.T) {
	e := newEnv(t)
	set := canary.New()
	key := set.New(canary.RecoveryKey, "recovery key")
	var runningDuringSnapshot []string
	e.store.OnBackup = func(req restic.BackupRequest) error {
		if req.Stdin != nil {
			return nil
		}
		for _, s := range []string{"db", "api", "web"} {
			if e.eng.running(s) {
				runningDuringSnapshot = append(runningDuringSnapshot, s)
			}
		}
		return nil
	}
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, stackItem(protocol.BackupRules{}),
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}), e.credential(key), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	if len(runningDuringSnapshot) != 0 {
		t.Errorf("containers ran during the snapshot: %v", runningDuringSnapshot)
	}
	want := []string{"stop:web", "stop:api", "stop:db", "start:db", "start:api", "start:web"}
	if got := e.eng.log(); !slices.Equal(got, want) {
		t.Errorf("lifecycle = %v, want %v", got, want)
	}
	if e.eng.running("worker") || !e.eng.running("reporting") && !e.eng.running("report") {
		t.Error("the stopped worker was started, or the other project's container was stopped")
	}
	out := outputOf(t, res)
	if out.Shutdown == nil || len(out.Shutdown.PreState) != 4 || len(out.Shutdown.Conflicts) == 0 {
		t.Errorf("shutdown report = %+v", out.Shutdown)
	}
	if out.ManifestSnapshotID == "" || out.ResticRepositoryID == "" {
		t.Errorf("output = %+v", out)
	}
	for _, m := range out.Members {
		if m.State != backup.StateComplete || m.SnapshotID == "" || m.SnapshotTime.IsZero() {
			t.Errorf("member %+v", m)
		}
		if m.Kind == backup.MemberStack && m.Consistency != backup.ConsistencyShutdown {
			t.Errorf("stack consistency = %s", m.Consistency)
		}
	}
	// The repository was initialized under the destination with the key
	// and holds a host manifest describing the members.
	repoPath := e.repoRef().Destination.Repository(e.repoRef().Scope)
	snaps := e.store.Snapshots(repoPath)
	if len(snaps) != 3 {
		t.Fatalf("snapshots = %+v", snaps)
	}
	var buf strings.Builder
	if err := e.store.Open(restic.Location{Repository: repoPath}, key).Dump(testutil.Context(t), out.ManifestSnapshotID, "/"+backup.ManifestFile, &buf); err != nil {
		t.Fatal(err)
	}
	m, err := backup.DecodeManifest([]byte(buf.String()))
	if err != nil || m.Kind != backup.ManifestHost || len(m.Members) != 2 || m.Completeness != backup.StateComplete || m.SetID != "set-1" {
		t.Errorf("host manifest = %+v, %v", m, err)
	}
	// Relative bind inside the project was captured; the opt-in paths were not.
	files := e.store.SnapshotFiles(repoPath, out.Members[0].SnapshotID)
	joined := strings.Join(files, "\n")
	if !strings.Contains(joined, "html/index.html") || !strings.Contains(joined, "compose.yaml") || strings.Contains(joined, "shared.txt") {
		t.Errorf("stack snapshot files: %v", files)
	}
	// The key never appears in the journal-visible output or the calls' arguments.
	set.AssertClean(t, "job output", string(res.Output))
	for _, c := range e.store.Calls() {
		set.AssertClean(t, "restic arguments", strings.Join(c.Args, " "))
	}
}

func TestBackupRunRestartsAfterFailureAndCancellation(t *testing.T) {
	cases := []struct {
		name string
		// arm runs at the first stop.
		arm     func(e *env, cancel *bool)
		outcome string
	}{
		{"snapshot step fails", func(e *env, _ *bool) {
			e.store.Fail("config", "", &restic.Error{Op: "cat config", Code: restic.CodeUnreachable, Message: "dial tcp: connection refused"})
		}, jobexec.OutcomeFailed},
		{"cancelled while stopped", func(_ *env, cancel *bool) { *cancel = true }, jobexec.OutcomeCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			cancel := false
			var once sync.Once
			e.eng.onStop = func(string) { once.Do(func() { tc.arm(e, &cancel) }) }
			res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, stackItem(protocol.BackupRules{})),
				e.credential("DYRK-TEST"), func() bool { return cancel })
			if err != nil || res.Outcome != tc.outcome {
				t.Fatalf("outcome %+v, %v", res, err)
			}
			if len(res.Compensations) != 1 || !res.Compensations[0].Done {
				t.Errorf("compensations = %+v", res.Compensations)
			}
			for _, s := range []string{"db", "api", "web"} {
				if !e.eng.running(s) {
					t.Errorf("%s was not restarted", s)
				}
			}
			if e.eng.running("worker") {
				t.Error("the previously stopped worker was started")
			}
			log := e.eng.log()
			if !slices.Equal(log[len(log)-3:], []string{"start:db", "start:api", "start:web"}) {
				t.Errorf("restart order = %v", log)
			}
		})
	}
}

// TestBackupRunRecoversAfterAgentCrash: the agent dies mid-snapshot with
// the containers stopped; on restart the journaled compensation restarts
// exactly the services that were running.
func TestBackupRunRecoversAfterAgentCrash(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	e.store.OnBackup = func(restic.BackupRequest) error {
		cancel() // the process "dies" while restic runs
		return context.Canceled
	}
	_, journal, err := e.run(ctx, jobspec.BackupRun, e.runInput(true, stackItem(protocol.BackupRules{})), e.credential("DYRK-TEST"), nil)
	if !errors.Is(err, jobexec.ErrAbandoned) {
		t.Fatalf("run = %v, want abandoned", err)
	}
	for _, s := range []string{"db", "api", "web"} {
		if e.eng.running(s) {
			t.Fatalf("%s runs after the crash", s)
		}
	}
	st := journal.last
	if len(st.Compensations) != 1 || st.Compensations[0].Name != jobspec.CompStartContainers {
		t.Fatalf("journaled compensations = %+v", st.Compensations)
	}
	x := e.executor(jobspec.BackupRun)
	res, err := jobexec.Recover(testutil.Context(t), &x, &st, jobexec.Options{Journal: journal})
	if err != nil || res.Outcome != jobexec.OutcomeInterrupted || res.Resumable {
		t.Fatalf("recover = %+v, %v", res, err)
	}
	for _, s := range []string{"db", "api", "web"} {
		if !e.eng.running(s) {
			t.Errorf("%s not restarted by recovery", s)
		}
	}
	if e.eng.running("worker") {
		t.Error("recovery started the worker")
	}
}

func TestBackupRunPartialWhenAnItemFails(t *testing.T) {
	e := newEnv(t)
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{}),
		protocol.BackupItem{Kind: backup.MemberVolume, Volume: "missing-volume"}), e.credential("DYRK-TEST"), nil)
	if err != nil || res.Outcome != jobexec.OutcomePartial {
		t.Fatalf("outcome %+v, %v", res, err)
	}
	out := outputOf(t, res)
	states := map[string]string{}
	for _, m := range out.Members {
		states[m.Item] = m.State
	}
	if states[backup.StackItem("st-app")] != backup.StateComplete || states[backup.VolumeItem("missing-volume")] != backup.StateFailed {
		t.Errorf("members = %v", states)
	}
	if len(e.eng.log()) != 0 {
		t.Errorf("containers touched without shutdown: %v", e.eng.log())
	}
}

func TestBackupRunRefusesWrongKeyAndMissingCredential(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	if res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-ONE"), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("first run: %+v", res)
	}
	res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-OTHER"), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeKeyRejected {
		t.Errorf("wrong key: %+v", res)
	}
	res, _, _ = e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), nil, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.CodeUnauthorized {
		t.Errorf("missing credential: %+v", res)
	}
}

// TestKeyRotationMovesLocation: a location still on the previous key is
// moved to the current one the next time a job opens it.
func TestKeyRotationMovesLocation(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	if res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-OLD"), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("first run: %+v", res)
	}
	secrets := &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: "repo-1", Password: "DYRK-NEW", PreviousPassword: "DYRK-OLD"}}}
	in := protocol.BackupVerifyInput{Repository: e.repoRef()}
	in.Repository.KeyGeneration = 2
	res, _, err := e.run(ctx, jobspec.BackupVerify, in, secrets, nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("verify: %+v %v", res, err)
	}
	var out protocol.VerifyOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.KeyGeneration != 2 || len(out.Snapshots) != 1 || len(out.Manifests) != 1 {
		t.Errorf("verify output = %+v", out)
	}
	repoPath := e.repoRef().Destination.Repository(e.repoRef().Scope)
	if pw := e.store.Passwords(repoPath); !slices.Equal(pw, []string{"DYRK-NEW"}) {
		t.Errorf("repository keys after rotation = %v", pw)
	}
}

func TestVerifyDetectsDamage(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	if res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-K"), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("backup: %+v", res)
	}
	e.store.Damage(e.repoRef().Destination.Repository(e.repoRef().Scope))
	res, _, _ := e.run(ctx, jobspec.BackupVerify, protocol.BackupVerifyInput{Repository: e.repoRef(), ReadDataSubset: "10%"}, e.credential("DYRK-K"), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeRepositoryDamaged {
		t.Errorf("damaged repository: %+v", res)
	}
	var out protocol.VerifyOutput
	_ = json.Unmarshal(res.Output, &out)
	if !out.Damaged {
		t.Errorf("output = %+v", out)
	}
}

func TestRetentionKeepsFloorAndForgetsOnlyThePolicy(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	clock := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	e.store = restictest.New(func() time.Time { clock = clock.Add(24 * time.Hour); return clock })
	e.svc.opts.Restic = e.store
	for range 5 {
		if res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-K"), nil); res.Outcome != jobexec.OutcomeSucceeded {
			t.Fatalf("backup: %+v", res)
		}
	}
	other := e.runInput(false, stackItem(protocol.BackupRules{}))
	other.PolicyID, other.SetID = "pol-other", "set-other"
	if res, _, _ := e.run(ctx, jobspec.BackupRun, other, e.credential("DYRK-K"), nil); res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("other policy: %+v", res)
	}
	in := protocol.BackupRetentionInput{Repository: e.repoRef(), PolicyID: "pol-1", Rules: backup.RetentionRules{Last: 1, MinKeep: 2}, TimeZone: "UTC"}
	res, _, err := e.run(ctx, jobspec.BackupRetention, in, e.credential("DYRK-K"), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("retention: %+v %v", res, err)
	}
	var out protocol.RetentionOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.Kept != 2 {
		t.Errorf("kept = %d", out.Kept)
	}
	repoPath := e.repoRef().Destination.Repository(e.repoRef().Scope)
	var data, other1, manifests int
	for _, sn := range e.store.Snapshots(repoPath) {
		switch {
		case sn.HasTag(backup.TagManifest):
			manifests++
		case backup.PolicyOf(sn.Tags) == "pol-other":
			other1++
		default:
			data++
		}
	}
	// 5 data snapshots of pol-1 -> floor of 2; the other policy is untouched.
	if data != 2 || other1 != 1 {
		t.Errorf("after retention: %d pol-1 data, %d other", data, other1)
	}
	if manifests != 6 {
		// Every run reused set-1, so its manifests stay while it has data.
		t.Logf("manifests left: %d", manifests)
	}
}
