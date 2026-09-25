//go:build integration

package migrations_test

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/migrations"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Environment migration (#35) between two Docker-in-Docker Engines of the
// matrix: a real manager in the test process, the real agent image in each
// Engine with the deploy mounts, Compose deploys through the agents'
// Compose SDK. Runs in the extended workflow (DOCKYARD_TEST_AGENT_IMAGE).

const (
	stacksVolume = "dockyard_stacks"
	stacksDir    = "/var/lib/docker/volumes/" + stacksVolume + "/_data"
	workload     = testharness.WorkloadImage
)

// shopCompose: db writes its readiness into the named volume and is
// healthy once it did; web depends on db being healthy and writes into the
// relative bind directory ./data.
const shopCompose = `services:
  db:
    image: ` + workload + `
    command: ["ready-after", "1500", "/data/ready"]
    healthcheck:
      test: ["CMD", "/workload", "check", "/data/ready"]
      interval: 1s
      timeout: 2s
      retries: 60
    volumes:
      - dbdata:/data
  web:
    image: ` + workload + `
    command: ["write", "/srv/data/written.txt", "hello"]
    volumes:
      - ./data:/srv/data
    depends_on:
      db:
        condition: service_healthy
volumes:
  dbdata: {}
`

type rig struct {
	t       *testing.T
	ctx     context.Context
	img     string
	ln      net.Listener
	port    int
	dataDir string
	opts    func(*app.Options)
	m       *app.Manager
	stop    func()
	engines []*testharness.Engine
	agents  []string
	envs    []string
}

// startManager starts (or restarts, on the same data directory) the
// manager and serves it on the rig's listener address.
func (r *rig) startManager() {
	r.t.Helper()
	pub, _ := url.Parse("http://localhost:8080")
	cfg := config.Config{PublicURL: pub, LocalDevelopment: true, ListenAddr: "127.0.0.1:0", DataDir: r.dataDir,
		SecretKeyFile: filepath.Join(r.dataDir, config.SecretKeyFileName)}
	o := app.Options{Config: cfg, Logger: testutil.Logger(r.t), UI: fstest.MapFS{"index.html": {Data: []byte("x")}}}
	if r.opts != nil {
		r.opts(&o)
	}
	m, err := app.Start(r.ctx, o)
	if err != nil {
		r.t.Fatal(err)
	}
	ln := r.ln
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(r.port)); err != nil {
			r.t.Fatal(err)
		}
	}
	r.ln = nil
	serveCtx, stopServe := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- m.Serve(serveCtx, ln) }()
	stopped := false
	r.m = m
	r.stop = func() {
		if stopped {
			return
		}
		stopped = true
		stopServe()
		<-served
		_ = m.Close()
	}
	r.t.Cleanup(r.stop)
}

func newRig(t *testing.T, opts func(*app.Options)) *rig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	r := &rig{t: t, ctx: ctx, img: testharness.AgentImage(t), dataDir: filepath.Join(t.TempDir(), "data"), opts: opts}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.ln, r.port = ln, ln.Addr().(*net.TCPAddr).Port
	r.startManager()
	sub := r.m.Events().Subscribe(4096, nil)
	defer sub.Close()
	r.engines = testharness.StartEngines(t, 2, testharness.EngineOptions{HostAccessPorts: []int{r.port}})
	for i, e := range r.engines {
		e.LoadHostImage(t, r.img)
		e.LoadWorkload(t)
		if _, err := e.Client(t).VolumeCreate(ctx, client.VolumeCreateOptions{Name: stacksVolume}); err != nil {
			t.Fatal(err)
		}
		c, err := r.m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: []string{"source", "destination"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		mounts := append(testharness.DefaultAgentMounts("/var/lib/docker"),
			mount.Mount{Type: mount.TypeVolume, Source: stacksVolume, Target: stacksDir})
		r.agents = append(r.agents, e.StartAgent(t, testharness.AgentOptions{Image: r.img, Name: "dockyard-agent", Mounts: mounts, Env: []string{
			"DOCKYARD_MANAGER_URL=http://" + e.HostAccessAddress(t, r.port), "DOCKYARD_MANAGER_ALLOW_HTTP=true",
			"DOCKYARD_ENROLLMENT_TOKEN=" + c.Token}}))
	}
	// Both environments come online; their IDs by name.
	byName := map[string]string{}
	deadline := time.After(4 * time.Minute)
	for len(byName) < 2 {
		select {
		case ev := <-sub.C():
			if ev.Type != events.EnvironmentOnline {
				continue
			}
			env, err := r.m.Agents().GetEnvironment(ctx, ev.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			byName[env.Name] = env.ID
		case <-deadline:
			r.logAgents()
			t.Fatalf("environments online: %v", byName)
		}
	}
	r.envs = []string{byName["source"], byName["destination"]}
	return r
}

func (r *rig) logAgents() {
	for i, e := range r.engines {
		r.t.Logf("agent on %s:\n%s", e.Alias, e.Logs(r.ctx, r.t, r.agents[i]))
	}
}

// waitJob waits until a job is terminal.
func (r *rig) waitJob(id string) domain.Job {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Minute)
	defer cancel()
	changed, unsub := r.m.Jobs().Subscribe(id)
	defer unsub()
	for {
		j, err := r.m.Jobs().Get(ctx, id)
		if err != nil {
			r.t.Fatal(err)
		}
		if j.State.Terminal() {
			return j
		}
		select {
		case <-changed:
		case <-ctx.Done():
			r.t.Fatalf("job %s (%s) stuck in %s", id, j.Kind, j.State)
		}
	}
}

// deployShop creates and deploys the stack on the source.
func (r *rig) deployShop() domain.Stack {
	r.t.Helper()
	st, v, err := r.m.Stacks().Create(r.ctx, authz.Service(), domain.StackCreate{StackDefinition: domain.StackDefinition{
		EnvironmentID: r.envs[0], Name: "shop", Files: []domain.StackFile{{Path: "compose.yaml", Content: []byte(shopCompose)}}}})
	if err != nil || !v.Valid {
		r.t.Fatalf("create: %v %+v", err, v)
	}
	r.engines[0].PutFiles(r.t, mount.Mount{Type: mount.TypeVolume, Source: stacksVolume}, map[string][]byte{"shop/data/seed.txt": []byte("seed\n")})
	j, err := r.m.Stacks().Deploy(r.ctx, authz.Service(), st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	if j := r.waitJob(j.ID); j.State != domain.JobSucceeded {
		r.t.Fatalf("source deploy: %s %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	st, _ = r.m.Stacks().Get(r.ctx, st.ID)
	return st
}

func (r *rig) inspect(i int, name string) (container.InspectResponse, bool) {
	r.t.Helper()
	res, err := r.engines[i].Client(r.t).ContainerInspect(r.ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, false
	}
	return res.Container, true
}

// read returns a file's content from a volume of engine i (exit code != 0
// when it does not exist).
func (r *rig) read(i int, volume, file string) (int64, string) {
	r.t.Helper()
	return r.engines[i].RunInImage(r.t, testharness.ImageRun{Image: workload, Cmd: []string{"cat", "/v/" + file},
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/v"}}})
}

// TestEngineStackMigrationBetweenTwoEngines (#35 Done-when 1): a
// multi-service stack with a relative bind directory and a named volume
// moves from one Engine to another: every part's checksum agrees on the
// source, the manager and the destination; the destination starts db
// before web (service_healthy); the source stays stopped with its data
// until its removal is confirmed.
func TestEngineStackMigrationBetweenTwoEngines(t *testing.T) {
	r := newRig(t, nil)
	st := r.deployShop()
	if c, ok := r.inspect(0, "shop-web-1"); !ok || !c.State.Running {
		t.Fatal("web does not run on the source")
	}
	svc := r.m.Migrations()
	plan, err := svc.PreviewStack(r.ctx, authz.Service(), true, st, migrations.StackRequest{TargetEnvironmentID: r.envs[1]})
	if err != nil || !plan.Allowed() {
		t.Fatalf("preview %+v %v", plan.Blockers, err)
	}
	j, m, err := svc.StartStack(r.ctx, authz.Service(), st, migrations.StackRequest{TargetEnvironmentID: r.envs[1]})
	if err != nil {
		t.Fatal(err)
	}
	if j := r.waitJob(j.ID); j.State != domain.JobSucceeded {
		t.Fatalf("migration: %s %s %s %s", j.State, j.ErrorClass, j.ErrorMessage, j.Recovery)
	}
	m, err = svc.Get(r.ctx, m.ID)
	if err != nil || m.State != domain.MigrationCompleted || len(m.Parts) != 2 {
		t.Fatalf("record %+v %v", m, err)
	}
	for _, p := range m.Parts {
		if len(p.SHA256) != 64 || p.Bytes == 0 {
			t.Errorf("part %+v", p)
		}
	}
	moved, _ := r.m.Stacks().Get(r.ctx, st.ID)
	if moved.EnvironmentID != r.envs[1] || moved.Status != domain.StackDeployed || moved.Applied == nil {
		t.Fatalf("stack after the migration %+v", moved)
	}
	// Destination: running, db healthy before web started.
	db, ok1 := r.inspect(1, "shop-db-1")
	web, ok2 := r.inspect(1, "shop-web-1")
	if !ok1 || !ok2 || !db.State.Running || !web.State.Running {
		t.Fatalf("destination containers %v %v", db.State, web.State)
	}
	dbStart, _ := time.Parse(time.RFC3339Nano, db.State.StartedAt)
	webStart, _ := time.Parse(time.RFC3339Nano, web.State.StartedAt)
	if !webStart.After(dbStart.Add(time.Second)) {
		t.Errorf("web started %v, db %v: web must wait for db to be healthy", webStart, dbStart)
	}
	// The data arrived: the volume and the relative bind directory.
	if code, out := r.read(1, "shop_dbdata", "ready"); code != 0 || !strings.Contains(out, "ready") {
		t.Errorf("destination volume: %d %s", code, out)
	}
	for file, want := range map[string]string{"shop/data/seed.txt": "seed", "shop/data/written.txt": "hello"} {
		if code, out := r.read(1, stacksVolume, file); code != 0 || !strings.Contains(out, want) {
			t.Errorf("destination %s: %d %s", file, code, out)
		}
	}
	// Source: stopped, data kept.
	for _, n := range []string{"shop-db-1", "shop-web-1"} {
		if c, ok := r.inspect(0, n); !ok || c.State.Running {
			t.Errorf("source %s: %+v", n, c.State)
		}
	}
	if code, _ := r.read(0, "shop_dbdata", "ready"); code != 0 {
		t.Error("the source volume lost its data")
	}
	// Confirmed removal.
	rj, err := svc.RemoveSource(r.ctx, authz.Service(), st.ID, m.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if j := r.waitJob(rj.ID); j.State != domain.JobSucceeded {
		t.Fatalf("source removal: %s %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	if _, ok := r.inspect(0, "shop-db-1"); ok {
		t.Error("source containers remain")
	}
	if _, err := r.engines[0].Client(t).VolumeInspect(r.ctx, "shop_dbdata", client.VolumeInspectOptions{}); err == nil {
		t.Error("the source volume remains")
	}
	if code, _ := r.read(0, stacksVolume, "shop/compose.yaml"); code == 0 {
		t.Error("the source project directory remains")
	}
	if m, _ := svc.Get(r.ctx, m.ID); m.State != domain.MigrationSourceRemoved {
		t.Errorf("record state %s", m.State)
	}
}
