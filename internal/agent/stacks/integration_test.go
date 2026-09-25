//go:build integration

package stacks_test

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Compose stack jobs on a real Engine (#7), in the compose-fixtures job:
// the stackjobs fixture runs inside the agent image with the agent's
// identical-path mounts and executes the agent's stack.* executors, like
// the agent's job runner does after a command. Services run the hermetic
// workload image; web is built from a local context (#33) and binds ./data
// relative to the project directory in the stacks volume (#28).

const stacksVolume = "dockyard_stacks"

type rig struct {
	t      *testing.T
	e      *testharness.Engine
	eng    *engine.Client
	img    string
	driver []byte
	bin    []byte
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, testharness.EngineOptions{})
}

// newRigWith starts the rig's Engine with opts (e.g. a registry fixture's
// EngineOptions).
func newRigWith(t *testing.T, opts testharness.EngineOptions) *rig {
	t.Helper()
	img := testharness.AgentImage(t)
	e := testharness.StartEngine(t, opts)
	e.LoadWorkload(t)
	e.LoadHostImage(t, img)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	eng, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: stacksVolume}); err != nil {
		t.Fatal(err)
	}
	driver, err := testharness.BuildFixture(ctx, "stackjobs", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := testharness.BuildWorkload(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, e: e, eng: eng, img: img, driver: driver, bin: bin}
}

const stacksDir = "/var/lib/docker/volumes/" + stacksVolume + "/_data"

func (r *rig) put(project string, files map[string][]byte, exec ...string) {
	r.t.Helper()
	out := map[string][]byte{}
	var ex []string
	for k, v := range files {
		out[project+"/"+k] = v
	}
	for _, e := range exec {
		ex = append(ex, project+"/"+e)
	}
	r.e.PutFiles(r.t, mount.Mount{Type: mount.TypeVolume, Source: stacksVolume}, out, ex...)
}

// job runs one stack job through the agent's executors inside the agent
// image and returns its result.
func (r *rig) job(kind, project string, env []string, services ...string) protocol.ResultPayload {
	r.t.Helper()
	mounts := append(testharness.DefaultAgentMounts("/var/lib/docker"),
		mount.Mount{Type: mount.TypeVolume, Source: stacksVolume, Target: stacksDir})
	code, out := r.e.RunInImage(r.t, testharness.ImageRun{Image: r.img, Entrypoint: []string{"/stackjobs"},
		Cmd: append([]string{kind, project}, services...), Env: env, Mounts: mounts,
		Files: map[string][]byte{"/stackjobs": r.driver}, Exec: []string{"/stackjobs"}})
	var res protocol.ResultPayload
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), `{"outcome"`) {
			if err := json.Unmarshal([]byte(line), &res); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	if res.Outcome == "" {
		r.t.Fatalf("%s %s: exit %d, no result:\n%s", kind, project, code, out)
	}
	r.t.Logf("%s %s: %s %s", kind, project, res.Outcome, res.Message)
	return res
}

// read returns compose.read's view of a project's definition.
func (r *rig) read(project string) protocol.ComposeReadOutput {
	r.t.Helper()
	mounts := append(testharness.DefaultAgentMounts("/var/lib/docker"),
		mount.Mount{Type: mount.TypeVolume, Source: stacksVolume, Target: stacksDir})
	code, out := r.e.RunInImage(r.t, testharness.ImageRun{Image: r.img, Entrypoint: []string{"/stackjobs"},
		Cmd: []string{"read", project}, Mounts: mounts, Files: map[string][]byte{"/stackjobs": r.driver}, Exec: []string{"/stackjobs"}})
	var res protocol.ComposeReadOutput
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), `{"snapshot"`) {
			if err := json.Unmarshal([]byte(line), &res); err != nil {
				r.t.Fatal(err)
			}
			return res
		}
	}
	r.t.Fatalf("read %s: exit %d:\n%s", project, code, out)
	return res
}

func output(t *testing.T, res protocol.ResultPayload) protocol.StackJobOutput {
	t.Helper()
	var o protocol.StackJobOutput
	if err := json.Unmarshal(res.Output, &o); err != nil {
		t.Fatalf("output: %v", err)
	}
	return o
}

func (r *rig) inspect(name string) engine.ContainerDetails {
	r.t.Helper()
	d, err := r.eng.InspectContainer(r.t.Context(), name)
	if err != nil {
		r.t.Fatalf("inspect %s: %v", name, err)
	}
	return d
}

const w = testharness.WorkloadImage

func appFiles(bin []byte, webGreeting string) map[string][]byte {
	return map[string][]byte{
		"compose.yaml": []byte(`services:
  db:
    image: ` + w + `
    command: ["ready-after", "2000", "/tmp/ready"]
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
    build: ./app
    command: ["write", "/data/written.txt", "${GREETING}"]
    volumes:
      - ./data:/data
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
`),
		".env":              []byte("GREETING=" + webGreeting + "\n"),
		"data/seed.txt":     []byte("seed\n"),
		"app/Dockerfile":    []byte("FROM scratch\nCOPY workload /workload\nENTRYPOINT [\"/workload\"]\n"),
		"app/workload":      bin,
		"app/.dockerignore": []byte("*.tmp\n"),
	}
}

// TestComposeStackDeployUpdateAndLifecycle deploys a multi-service stack with a
// build section, a relative bind, a health-gated dependency, a one-shot and
// an optional dependency; updates it; and runs restart (propagation), stop,
// start and down through the shared lifecycle.
func TestComposeStackDeployUpdateAndLifecycle(t *testing.T) {
	r := newRig(t)
	files := appFiles(r.bin, "v1")
	r.put("shop", files, "app/workload")

	res := r.job("stack.deploy", "shop", nil)
	if res.Outcome != "succeeded" {
		t.Fatalf("deploy: %+v", res)
	}
	out := output(t, res)
	// The reported sources are exactly the files on disk (compose.yaml
	// and .env); the deploy did not touch them.
	want := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: ".env", Content: files[".env"]}, {Path: "compose.yaml", Content: files["compose.yaml"]}})
	if out.Sources == nil || out.Sources.Hash != want.Hash {
		t.Fatalf("sources %+v, want hash %s", out.Sources, want.Hash)
	}
	if got := r.read("shop"); got.Snapshot.Hash != want.Hash {
		t.Errorf("definition on disk after the deploy: %s, want %s", got.Snapshot.Hash, want.Hash)
	}
	db, migrate, web, worker := r.inspect("shop-db-1"), r.inspect("shop-migrate-1"), r.inspect("shop-web-1"), r.inspect("shop-worker-1")
	if !db.State.Running || migrate.State.Running || migrate.State.ExitCode != 0 || !web.State.Running || !worker.State.Running {
		t.Fatalf("states: db %+v migrate %+v web %+v worker %+v", db.State, migrate.State, web.State, worker.State)
	}
	// Dependency order and conditions from the Engine's own timestamps:
	// web started after db was healthy and migrate completed.
	if !web.State.StartedAt.After(migrate.State.FinishedAt) || !migrate.State.StartedAt.After(db.State.StartedAt) {
		t.Errorf("start order: db %s, migrate %s-%s, web %s", db.State.StartedAt, migrate.State.StartedAt, migrate.State.FinishedAt, web.State.StartedAt)
	}
	// ./data is the project directory's data in the stacks volume.
	var bind engine.Mount
	for _, m := range web.Mounts {
		if m.Destination == "/data" {
			bind = m
		}
	}
	if bind.Type != "bind" || bind.Source != stacksDir+"/shop/data" {
		t.Errorf("./data mounted as %+v", bind)
	}
	var built bool
	for _, i := range out.Images {
		if i.Service == "web" && i.Build && i.ImageID != "" {
			built = true
		}
		if i.Service == "db" && (i.Image != w || i.ImageID == "") {
			t.Errorf("db image %+v", i)
		}
	}
	if !built || len(out.Binds) != 1 || out.Binds[0].RelPath != "data" {
		t.Errorf("images %+v binds %+v", out.Images, out.Binds)
	}

	// Update: a changed .env value recreates web only.
	r.put("shop", map[string][]byte{".env": []byte("GREETING=v2\n")})
	res = r.job("stack.deploy", "shop", nil)
	if res.Outcome != "succeeded" {
		t.Fatalf("update: %+v", res)
	}
	if r.inspect("shop-web-1").ID == web.ID || r.inspect("shop-db-1").ID != db.ID || r.inspect("shop-worker-1").ID != worker.ID {
		t.Error("update recreated the wrong services")
	}
	if o := output(t, res); o.Sources == nil || o.Sources.Hash == want.Hash {
		t.Error("the update's sources are the old ones")
	}

	// Restart db: web (restart: true) is restarted with it, worker is not.
	web, worker = r.inspect("shop-web-1"), r.inspect("shop-worker-1")
	res = r.job("stack.restart", "shop", nil, "db")
	if res.Outcome != "succeeded" {
		t.Fatalf("restart: %+v", res)
	}
	if !r.inspect("shop-web-1").State.StartedAt.After(web.State.StartedAt) {
		t.Error("web was not restarted with db (restart: true)")
	}
	if !r.inspect("shop-worker-1").State.StartedAt.Equal(worker.State.StartedAt) {
		t.Error("worker was restarted although it has no restart: true")
	}

	// Stop: everything stops (dependents first); start: dependencies first.
	if res = r.job("stack.stop", "shop", nil); res.Outcome != "succeeded" {
		t.Fatalf("stop: %+v", res)
	}
	for _, n := range []string{"shop-db-1", "shop-web-1", "shop-worker-1"} {
		if r.inspect(n).State.Running {
			t.Errorf("%s still running after stop", n)
		}
	}
	if res = r.job("stack.start", "shop", nil); res.Outcome != "succeeded" {
		t.Fatalf("start: %+v", res)
	}
	db, web = r.inspect("shop-db-1"), r.inspect("shop-web-1")
	if !db.State.Running || !web.State.Running || !web.State.StartedAt.After(db.State.StartedAt) {
		t.Errorf("start order: db %+v web %+v", db.State, web.State)
	}

	// Down removes containers and networks, keeps the files.
	if res = r.job("stack.down", "shop", nil); res.Outcome != "succeeded" {
		t.Fatalf("down: %+v", res)
	}
	list, err := r.eng.ListContainers(t.Context(), engine.ContainerFilter{All: true, Labels: []string{"com.docker.compose.project=shop"}})
	if err != nil || len(list) != 0 {
		t.Errorf("containers after down: %+v %v", list, err)
	}
}

// TestComposeStackDeployAndUpdateOnTwoHosts (#7 Done-when 1): the same
// multi-service app (health-gated dependency, completed one-shot, build
// section, relative bind) deploys and updates on either of two hosts, each
// an Engine of its own with the agent image running the agent's stack
// executors (official Compose SDK, no Docker CLI); dependency order holds
// on both and the definition files stay byte-identical.
func TestComposeStackDeployAndUpdateOnTwoHosts(t *testing.T) {
	hosts := []*rig{newRig(t), newRig(t)}
	for i, r := range hosts {
		files := appFiles(r.bin, "host")
		r.put("shop", files, "app/workload")
		before := r.read("shop").Snapshot.Hash
		if res := r.job("stack.deploy", "shop", nil); res.Outcome != "succeeded" {
			t.Fatalf("host %d deploy: %+v", i, res)
		}
		db, migrate, web := r.inspect("shop-db-1"), r.inspect("shop-migrate-1"), r.inspect("shop-web-1")
		if !db.State.Running || migrate.State.Running || migrate.State.ExitCode != 0 || !web.State.Running ||
			!web.State.StartedAt.After(migrate.State.FinishedAt) || !migrate.State.StartedAt.After(db.State.StartedAt) {
			t.Fatalf("host %d states/order: db %+v migrate %+v web %+v", i, db.State, migrate.State, web.State)
		}
		// Update on this host: a changed .env value recreates web only.
		r.put("shop", map[string][]byte{".env": []byte("GREETING=host-v2\n")})
		updated := r.read("shop").Snapshot.Hash
		res := r.job("stack.deploy", "shop", nil)
		if res.Outcome != "succeeded" {
			t.Fatalf("host %d update: %+v", i, res)
		}
		if r.inspect("shop-web-1").ID == web.ID || r.inspect("shop-db-1").ID != db.ID {
			t.Errorf("host %d: the update recreated the wrong services", i)
		}
		if o := output(t, res); o.Sources == nil || o.Sources.Hash != updated || updated == before {
			t.Errorf("host %d: update sources %+v, want %s", i, o.Sources, updated)
		}
		if got := r.read("shop").Snapshot.Hash; got != updated {
			t.Errorf("host %d: the deploy changed the definition on disk", i)
		}
	}
}

// TestComposeStackDeployUnhealthyDependency: a dependency that never becomes
// healthy fails the deploy with the sources and the state after reported
// (recovery data); the dependent is not started.
func TestComposeStackDeployUnhealthyDependency(t *testing.T) {
	r := newRig(t)
	r.put("sick", map[string][]byte{"compose.yaml": []byte(`services:
  db:
    image: ` + w + `
    command: ["serve"]
    healthcheck:
      test: ["CMD", "/workload", "fail"]
      interval: 1s
      retries: 2
  web:
    image: ` + w + `
    command: ["serve"]
    depends_on:
      db:
        condition: service_healthy
`)})
	res := r.job("stack.deploy", "sick", nil)
	if res.Outcome != "failed" || !strings.Contains(res.Message, "unhealthy") {
		t.Fatalf("deploy: %+v", res)
	}
	out := output(t, res)
	if out.Sources == nil || out.After == nil {
		t.Errorf("recovery data missing: %+v", out)
	}
	if d, err := r.eng.InspectContainer(t.Context(), "sick-web-1"); err == nil && d.State.Running {
		t.Error("web started although its dependency is unhealthy")
	}
	// Starting it again through the lifecycle refuses the same way.
	res = r.job("stack.start", "sick", []string{"STACKJOBS_WAIT_TIMEOUT=30s"}, "web")
	if res.Outcome != "failed" || !strings.Contains(res.Message, "unhealthy") {
		t.Errorf("start: %+v", res)
	}
}

// TestComposeStackDeployRefusesUnsupportedFeatures: the agent rejects what the
// support matrix rejects before touching the Engine.
func TestComposeStackDeployRefusesUnsupportedFeatures(t *testing.T) {
	r := newRig(t)
	r.put("sock", map[string][]byte{"compose.yaml": []byte("services:\n  x:\n    image: " + w + "\n    use_api_socket: true\n")})
	res := r.job("stack.deploy", "sock", nil)
	if res.Outcome != "failed" || !strings.Contains(res.Message, "use_api_socket") || slices.Contains(res.CompletedSteps, "resolve_sources") {
		t.Fatalf("deploy: %+v", res)
	}
	list, _ := r.eng.ListContainers(t.Context(), engine.ContainerFilter{All: true, Labels: []string{"com.docker.compose.project=sock"}})
	if len(list) != 0 {
		t.Errorf("containers of a refused stack: %+v", list)
	}
}

func item(res protocol.ResultPayload, name string) string {
	for _, it := range res.Items {
		if it.Name == name {
			return it.Message
		}
	}
	return ""
}

// TestComposeStackBuildRebuildAndCancel (#33): a stack with a build section
// deploys (the deploy builds the missing image through the Engine's
// BuildKit), rebuilds on request (stack.build, no deploy) and the next
// deploy runs the rebuilt image; a stack build cancelled mid-build stops
// BuildKit and tags nothing.
func TestComposeStackBuildRebuildAndCancel(t *testing.T) {
	r := newRig(t)
	r.put("forge", map[string][]byte{
		"compose.yaml":   []byte("services:\n  app:\n    build: ./app\n    command: [\"serve\"]\n"),
		"app/Dockerfile": []byte("FROM scratch\nCOPY workload /workload\nENTRYPOINT [\"/workload\"]\n"),
		"app/workload":   r.bin,
	}, "app/workload")

	res := r.job("stack.deploy", "forge", nil)
	if res.Outcome != "succeeded" {
		t.Fatalf("deploy: %+v", res)
	}
	first := item(res, "forge-app")
	if o := output(t, res); first == "" || len(o.Built) != 1 || o.Built[0].ImageID != first {
		t.Fatalf("deploy built %q %+v", first, o.Built)
	}
	if got := r.inspect("forge-app-1"); got.ImageID != first || !got.State.Running {
		t.Fatalf("deployed %+v", got)
	}

	// Rebuild on request: a changed Dockerfile, stack.build without cache.
	r.put("forge", map[string][]byte{"app/Dockerfile": []byte("FROM scratch\nCOPY workload /workload\nLABEL rev=\"2\"\nENTRYPOINT [\"/workload\"]\n")})
	res = r.job("stack.build", "forge", []string{"STACKJOBS_NO_CACHE=1"})
	if res.Outcome != "succeeded" || !slices.Equal(res.CompletedSteps, []string{"fetch_sources", "build_images"}) {
		t.Fatalf("build: %+v", res)
	}
	second := item(res, "forge-app")
	if img, err := r.eng.InspectImage(t.Context(), "forge-app"); err != nil || second == "" || second == first || img.ID != second {
		t.Fatalf("rebuilt %q (was %q): %+v %v", second, first, img, err)
	}
	if got := r.inspect("forge-app-1"); got.ImageID != first {
		t.Error("stack.build changed the running container")
	}
	if res = r.job("stack.deploy", "forge", nil); res.Outcome != "succeeded" || r.inspect("forge-app-1").ImageID != second {
		t.Errorf("redeploy: %+v", res)
	}

	// Cancellation mid-build: a RUN step that never ends.
	r.put("slow", map[string][]byte{
		"compose.yaml":    []byte("services:\n  slow:\n    build: ./slow\n    image: dockyard-test/slow:1\n"),
		"slow/Dockerfile": []byte("FROM " + w + "\nRUN [\"/workload\", \"tick\", \"1000\"]\n"),
	})
	res = r.job("stack.build", "slow", []string{"STACKJOBS_CANCEL_AFTER=5s"})
	if res.Outcome != "cancelled" || res.ErrorClass != "cancelled" {
		t.Fatalf("cancelled build: %+v", res)
	}
	if _, err := r.eng.InspectImage(t.Context(), "dockyard-test/slow:1"); !engine.IsCode(err, engine.CodeNotFound) {
		t.Errorf("the cancelled build tagged an image: %v", err)
	}
}
