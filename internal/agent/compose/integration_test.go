//go:build integration

package compose_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Compose SDK tests (#2 spike, #7, #21): TestCompose* run in the
// compose-fixtures job on the default Engine, TestEngineCompose* in the
// engine-matrix job on every Engine. Services run the workload image, so
// no image comes from Docker Hub.

type env struct {
	e   *testharness.Engine
	eng *engine.Client
	a   *compose.Adapter
}

func setup(t *testing.T, opts testharness.EngineOptions) *env {
	t.Helper()
	e := testharness.StartEngine(t, opts)
	e.LoadWorkload(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	eng, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	a, err := compose.New(ctx, compose.Options{Host: e.Host, Engine: eng, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return &env{e: e, eng: eng, a: a}
}

func ctxFor(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func project(t *testing.T, a *compose.Adapter, name string, files map[string]string) *compose.Project {
	t.Helper()
	dir := t.TempDir()
	for f, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := a.Load(t.Context(), compose.ProjectSpec{Dir: dir, Name: name})
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return p
}

func (v *env) state(t *testing.T, name string) engine.ContainerState {
	t.Helper()
	d, err := v.eng.InspectContainer(t.Context(), name)
	if err != nil {
		t.Fatalf("inspect %s: %v", name, err)
	}
	return d.State
}

func (v *env) down(t *testing.T, p *compose.Project) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = v.a.Down(ctx, p.Name, p, compose.DownOptions{Volumes: true, RemoveOrphans: true})
	})
}

const w = testharness.WorkloadImage

var depsYAML = `
name: deps
services:
  db:
    image: ` + w + `
    command: ["ready-after", "3000", "/tmp/ready"]
    environment:
      GEN: ${GEN:-1}
    healthcheck:
      test: ["CMD", "/workload", "check", "/tmp/ready"]
      interval: 1s
      timeout: 2s
      retries: 60
  migrate:
    image: ` + w + `
    command: ["exit", "0", "migrated"]
    depends_on:
      db:
        condition: service_healthy
  web:
    image: ` + w + `
    command: ["serve", "web up"]
    depends_on:
      db:
        condition: service_healthy
        restart: true
      migrate:
        condition: service_completed_successfully
      cache:
        condition: service_started
        required: false
  worker:
    image: ` + w + `
    command: ["serve"]
    depends_on:
      db:
        condition: service_started
  cache:
    image: ` + w + `
    command: ["serve"]
    profiles: [extras]
`

// TestComposeDependsOnConditions verifies the SDK lifecycle honors
// service_started, service_healthy, service_completed_successfully,
// required: false and restart: true propagation.
func TestComposeDependsOnConditions(t *testing.T) {
	v := setup(t, testharness.EngineOptions{})
	ctx := ctxFor(t, 10*time.Minute)
	p := project(t, v.a, "deps", map[string]string{"compose.yaml": depsYAML})
	v.down(t, p)
	var events []compose.Event
	if err := v.a.Up(ctx, p, compose.UpOptions{RunOptions: compose.RunOptions{Events: func(e compose.Event) { events = append(events, e) }}}); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(events) == 0 {
		t.Error("no progress events")
	}
	db, migrate, web, worker := v.state(t, "deps-db-1"), v.state(t, "deps-migrate-1"), v.state(t, "deps-web-1"), v.state(t, "deps-worker-1")
	t.Logf("db %v migrate %v..%v web %v worker %v", db.StartedAt, migrate.StartedAt, migrate.FinishedAt, web.StartedAt, worker.StartedAt)
	if !db.Running || db.Health == nil || db.Health.Status != "healthy" {
		t.Errorf("db %+v", db)
	}
	// service_healthy: migrate started only after db became healthy (>= 3 s).
	if gap := migrate.StartedAt.Sub(db.StartedAt); gap < 2500*time.Millisecond {
		t.Errorf("migrate started %v after db, before db could be healthy", gap)
	}
	// service_completed_successfully: web started after migrate exited 0.
	if migrate.Running || migrate.ExitCode != 0 || !web.StartedAt.After(migrate.FinishedAt) {
		t.Errorf("migrate %+v, web started %v", migrate, web.StartedAt)
	}
	// service_started: worker did not wait for db's health.
	if !worker.Running || worker.StartedAt.Sub(db.StartedAt) > 2500*time.Millisecond {
		t.Errorf("worker %+v (db started %v)", worker, db.StartedAt)
	}
	// required: false: the disabled optional dependency did not block web.
	if !web.Running {
		t.Errorf("web %+v", web)
	}
	if _, err := v.eng.InspectContainer(ctx, "deps-cache-1"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("optional disabled dependency was started: %v", err)
	}
	ps, err := v.a.Ps(ctx, "deps")
	if err != nil || len(ps) != 4 {
		t.Fatalf("ps %+v, %v", ps, err)
	}

	// restart: true: restarting db restarts web (declared restart: true)
	// but not worker.
	if err := v.a.Restart(ctx, p, []string{"db"}, nil, compose.RunOptions{}); err != nil {
		t.Fatalf("restart db: %v", err)
	}
	web2, worker2 := v.state(t, "deps-web-1"), v.state(t, "deps-worker-1")
	if !web2.StartedAt.After(web.StartedAt) {
		t.Errorf("web not restarted with its restart: true dependency (%v -> %v)", web.StartedAt, web2.StartedAt)
	}
	if !worker2.StartedAt.Equal(worker.StartedAt) {
		t.Errorf("worker (no restart: true) was restarted (%v -> %v)", worker.StartedAt, worker2.StartedAt)
	}

	// Recreating db on a configuration change propagates the same way.
	t.Setenv("GEN", "unused-by-loader") // the agent environment must not matter
	p2 := project(t, v.a, "deps", map[string]string{"compose.yaml": depsYAML, ".env": "GEN=2\n"})
	dbID := mustID(t, v, "deps-db-1")
	if err := v.a.Up(ctx, p2, compose.UpOptions{}); err != nil {
		t.Fatalf("up after change: %v", err)
	}
	if mustID(t, v, "deps-db-1") == dbID {
		t.Error("db not recreated after its environment changed")
	}
	web3, worker3 := v.state(t, "deps-web-1"), v.state(t, "deps-worker-1")
	if !web3.StartedAt.After(web2.StartedAt) {
		t.Errorf("web not restarted after db was recreated (%v -> %v)", web2.StartedAt, web3.StartedAt)
	}
	if !web3.Running || !worker3.Running {
		t.Errorf("web %+v worker %+v", web3, worker3)
	}

	// Stop in reverse order, start in dependency order, down.
	if err := v.a.Stop(ctx, p2, nil, nil, compose.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.state(t, "deps-web-1"); st.Running {
		t.Error("web still running after stop")
	}
	if err := v.a.Start(ctx, p2, compose.RunOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if st := v.state(t, "deps-web-1"); !st.Running {
		t.Error("web not running after start")
	}
	if err := v.a.Down(ctx, "deps", p2, compose.DownOptions{}); err != nil {
		t.Fatal(err)
	}
	if ps, err := v.a.Ps(ctx, "deps"); err != nil || len(ps) != 0 {
		t.Errorf("after down: %+v, %v", ps, err)
	}
}

func mustID(t *testing.T, v *env, name string) string {
	t.Helper()
	d, err := v.eng.InspectContainer(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func TestComposeUnhealthyDependencyFails(t *testing.T) {
	v := setup(t, testharness.EngineOptions{})
	ctx := ctxFor(t, 5*time.Minute)
	p := project(t, v.a, "unhealthy", map[string]string{"compose.yaml": `
services:
  db:
    image: ` + w + `
    command: ["serve"]
    healthcheck:
      test: ["CMD", "/workload", "fail"]
      interval: 1s
      timeout: 1s
      retries: 2
  web:
    image: ` + w + `
    command: ["serve"]
    depends_on:
      db:
        condition: service_healthy
`})
	v.down(t, p)
	err := v.a.Up(ctx, p, compose.UpOptions{})
	if engine.CodeOf(err) != engine.CodeDependencyFailed || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("up with an unhealthy dependency: %v (%s), want dependency_failed", err, engine.CodeOf(err))
	}
	if d, err := v.eng.InspectContainer(ctx, "unhealthy-web-1"); err == nil && d.State.Running {
		t.Error("web started although its dependency is unhealthy")
	}
}

func TestComposeFailedOneShotBlocksDependents(t *testing.T) {
	v := setup(t, testharness.EngineOptions{})
	ctx := ctxFor(t, 5*time.Minute)
	p := project(t, v.a, "oneshot", map[string]string{"compose.yaml": `
services:
  migrate:
    image: ` + w + `
    command: ["exit", "1", "migration failed"]
  web:
    image: ` + w + `
    command: ["serve"]
    depends_on:
      migrate:
        condition: service_completed_successfully
`})
	v.down(t, p)
	err := v.a.Up(ctx, p, compose.UpOptions{})
	if engine.CodeOf(err) != engine.CodeDependencyFailed {
		t.Fatalf("up with a failing one-shot: %v (%s), want dependency_failed", err, engine.CodeOf(err))
	}
	if d, err := v.eng.InspectContainer(ctx, "oneshot-web-1"); err == nil && d.State.Running {
		t.Error("web started although migrate failed")
	}
}

func buildFiles(t *testing.T) map[string]string {
	t.Helper()
	bin, err := testharness.BuildWorkload(t.Context(), runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"app/workload":      string(bin),
		"app/hello.txt":     "hello from a Compose build\n",
		"app/.dockerignore": "secret.env\n",
		"app/secret.env":    "NOT_IN_IMAGE=1\n",
		"app/Dockerfile":    "FROM scratch\nARG GREETING=none\nLABEL greeting=${GREETING}\nCOPY workload /workload\nCOPY hello.txt /hello.txt\nENTRYPOINT [\"/workload\"]\n",
		"compose.yaml": `
services:
  app:
    build:
      context: ./app
      args:
        GREETING: hi
    command: ["serve"]
  web:
    build: ./app
    image: dockyard-test/built-web:1
    command: ["cat", "/hello.txt"]
    restart: "no"
`,
	}
}

// TestComposeBuildLocalContext builds build sections through the Engine's
// BuildKit (no buildx, no legacy builder) and runs the result.
func TestComposeBuildLocalContext(t *testing.T) {
	v := setup(t, testharness.EngineOptions{})
	ctx := ctxFor(t, 10*time.Minute)
	p := project(t, v.a, "built", buildFiles(t))
	v.down(t, p)
	var built []string
	err := v.a.Up(ctx, p, compose.UpOptions{RunOptions: compose.RunOptions{Events: func(e compose.Event) {
		if e.Status == "done" && e.Text == "Built" {
			built = append(built, e.Resource)
		}
	}}})
	if err != nil {
		t.Fatalf("up with build: %v", err)
	}
	if strings.Join(built, ",") != "Image built-app,Image dockyard-test/built-web:1" {
		t.Errorf("built %v", built)
	}
	img, err := v.eng.InspectImage(ctx, "built-app")
	if err != nil || img.Labels["greeting"] != "hi" {
		t.Fatalf("built-app image %+v, %v", img, err)
	}
	if st := v.state(t, "built-app-1"); !st.Running {
		t.Errorf("app %+v", st)
	}
	if code, err := v.eng.WaitContainer(ctx, "built-web-1"); err != nil || code != 0 {
		t.Errorf("web exit %d, %v", code, err)
	}
	// A second up does not rebuild existing images; an explicit build does.
	built = nil
	if err := v.a.Up(ctx, p, compose.UpOptions{RunOptions: compose.RunOptions{Events: func(e compose.Event) {
		if e.Text == "Built" {
			built = append(built, e.Resource)
		}
	}}}); err != nil || len(built) != 0 {
		t.Errorf("second up rebuilt %v (%v)", built, err)
	}
	if err := v.a.Build(ctx, p, compose.BuildOptions{Services: []string{"app"}, NoCache: true}); err != nil {
		t.Fatalf("explicit build: %v", err)
	}
}

func TestComposePrivateRegistryPull(t *testing.T) {
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{})
	v := setup(t, reg.EngineOptions())
	ctx := ctxFor(t, 5*time.Minute)
	img, err := testharness.NewTestImage(runtime.GOARCH, "compose private pull")
	if err != nil {
		t.Fatal(err)
	}
	if err := testharness.PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "dockyard/compose", "v1", img); err != nil {
		t.Fatal(err)
	}
	p := project(t, v.a, "private", map[string]string{"compose.yaml": "services:\n  app:\n    image: " + reg.EngineAddress + "/dockyard/compose:v1\n"})
	if err := v.a.Pull(ctx, p, compose.RunOptions{}); engine.CodeOf(err) != engine.CodeUnauthorized {
		t.Errorf("anonymous compose pull: %v (%s), want unauthorized", err, engine.CodeOf(err))
	}
	auth := []engine.RegistryAuth{{ServerAddress: reg.EngineAddress, Username: reg.User, Password: logging.Secret(reg.Password)}}
	if err := v.a.Pull(ctx, p, compose.RunOptions{Auth: auth}); err != nil {
		t.Fatalf("compose pull with per-operation credentials: %v", err)
	}
	d, err := v.eng.InspectImage(ctx, reg.EngineAddress+"/dockyard/compose:v1")
	if err != nil || len(d.RepoDigests) == 0 || !strings.HasSuffix(d.RepoDigests[0], img.Digest) {
		t.Errorf("pulled image %+v, %v", d, err)
	}
}

// TestEngineComposeLifecycle runs the Compose lifecycle with a build on every
// Engine of the matrix (engine-matrix job).
func TestEngineComposeLifecycle(t *testing.T) {
	v := setup(t, testharness.EngineOptions{})
	ctx := ctxFor(t, 10*time.Minute)
	files := buildFiles(t)
	files["compose.yaml"] = `
services:
  db:
    image: ` + w + `
    command: ["ready-after", "1000", "/tmp/ready"]
    healthcheck:
      test: ["CMD", "/workload", "check", "/tmp/ready"]
      interval: 1s
      retries: 60
  app:
    build: ./app
    command: ["serve"]
    depends_on:
      db:
        condition: service_healthy
        restart: true
`
	p := project(t, v.a, "lifecycle", files)
	v.down(t, p)
	if err := v.a.Up(ctx, p, compose.UpOptions{}); err != nil {
		t.Fatalf("up: %v", err)
	}
	db, app := v.state(t, "lifecycle-db-1"), v.state(t, "lifecycle-app-1")
	if !app.Running || db.Health == nil || db.Health.Status != "healthy" || app.StartedAt.Sub(db.StartedAt) < 500*time.Millisecond {
		t.Fatalf("db %+v app %+v", db, app)
	}
	if err := v.a.Restart(ctx, p, []string{"db"}, nil, compose.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := v.state(t, "lifecycle-app-1"); !st.StartedAt.After(app.StartedAt) {
		t.Error("restart: true not propagated")
	}
	if err := v.a.Stop(ctx, p, nil, nil, compose.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := v.a.Start(ctx, p, compose.RunOptions{}); err != nil {
		for _, name := range []string{"lifecycle-db-1", "lifecycle-app-1"} {
			d, ierr := v.eng.InspectContainer(ctx, name)
			t.Logf("%s after failed start: %+v (%v)\nlogs:\n%s", name, d.State, ierr, v.e.Logs(ctx, t, d.ID))
		}
		t.Fatal(err)
	}
	if err := v.a.Down(ctx, p.Name, p, compose.DownOptions{Volumes: true}); err != nil {
		t.Fatal(err)
	}
	if ps, _ := v.a.Ps(ctx, p.Name); len(ps) != 0 {
		t.Errorf("containers left after down: %+v", ps)
	}
}
