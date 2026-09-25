//go:build integration

package updates_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/regclient"
	"github.com/neurekadev/dockyard/internal/manager/registries"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/updates"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestRegistryAutomaticUpdateOnTwoAgents is #19's pending
// acceptance criterion and #20's end-to-end check on real Engines (the
// compose-fixtures job): a private image on the registry fixture is
// deployed as a Compose stack on two Engines with a manager-owned registry
// connection (credentials resolved per dispatch, never in the job, the
// Compose files or a Docker config); the tag moves to a new build; the
// scheduled update check (manager, authenticated digest check) finds the
// candidate and the scheduled update run pulls it with the connection on
// both Engines and recreates the service through the agent's executor
// (the stackjobs fixture inside the agent image), leaving the definition
// files byte-for-byte unchanged.

const intStacksVolume = "dockyard_stacks"
const intStacksDir = "/var/lib/docker/volumes/" + intStacksVolume + "/_data"

type intAgent struct {
	t      *testing.T
	env    string
	e      *testharness.Engine
	eng    *engine.Client
	img    string
	driver []byte
}

func (a *intAgent) mounts() []mount.Mount {
	return append(testharness.DefaultAgentMounts("/var/lib/docker"),
		mount.Mount{Type: mount.TypeVolume, Source: intStacksVolume, Target: intStacksDir})
}

// run executes the stackjobs fixture in the agent image and returns the
// JSON line starting with prefix.
func (a *intAgent) run(prefix string, env []string, args ...string) []byte {
	a.t.Helper()
	code, out := a.e.RunInImage(a.t, testharness.ImageRun{Image: a.img, Entrypoint: []string{"/stackjobs"}, Cmd: args, Env: env,
		Mounts: a.mounts(), Files: map[string][]byte{"/stackjobs": a.driver}, Exec: []string{"/stackjobs"}})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return []byte(line)
		}
	}
	a.t.Fatalf("stackjobs %v: exit %d:\n%s", args, code, out)
	return nil
}

func (a *intAgent) job(kind, project string, input any, secrets *protocol.CommandSecrets) protocol.ResultPayload {
	a.t.Helper()
	in, _ := json.Marshal(input)
	env := []string{"STACKJOBS_INPUT=" + string(in), "STACKJOBS_WAIT_TIMEOUT=60s"}
	if secrets != nil {
		s, _ := json.Marshal(secrets)
		env = append(env, "STACKJOBS_SECRETS="+string(s))
	}
	var res protocol.ResultPayload
	if err := json.Unmarshal(a.run(`{"outcome"`, env, kind, project), &res); err != nil {
		a.t.Fatal(err)
	}
	return res
}

func (a *intAgent) read(project string) protocol.ComposeReadOutput {
	a.t.Helper()
	var out protocol.ComposeReadOutput
	if err := json.Unmarshal(a.run(`{"snapshot"`, nil, "read", project), &out); err != nil {
		a.t.Fatal(err)
	}
	return out
}

// intLink serves the manager's compose.read requests with the fixture.
type intLink struct{ agents map[string]*intAgent }

func (l intLink) RequestEnvironment(_ context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a, ok := l.agents[env]
	if !ok || name != protocol.ReqComposeRead {
		return nil, errors.New("unsupported request " + name)
	}
	var in protocol.ComposeReadInput
	b, _ := json.Marshal(input)
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	return json.Marshal(a.read(in.Stack.ProjectName))
}

type intEnvironments struct{}

func (intEnvironments) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	return domain.Environment{ID: id, Status: domain.EnvironmentActive, Online: true}, nil
}

type intStacks struct {
	mu     sync.Mutex
	stacks map[string]domain.Stack
}

func (s *intStacks) Get(_ context.Context, id string) (domain.Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stacks[id]
	if !ok {
		return st, domain.ErrStackNotFound
	}
	return st, nil
}

func (s *intStacks) List(_ context.Context, f domain.StackFilter) ([]domain.Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Stack
	for _, st := range s.stacks {
		if f.EnvironmentID == "" || st.EnvironmentID == f.EnvironmentID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *intStacks) RecordUpdatedImages(_ context.Context, _ bun.IDB, id string, images []domain.StackImage, _ []domain.StackServiceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stacks[id]
	for _, img := range images {
		for i := range st.Images {
			if st.Images[i].Service == img.Service {
				st.Images[i] = img
			}
		}
	}
	s.stacks[id] = st
	return nil
}

func TestRegistryAutomaticUpdateOnTwoAgents(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	nw := testharness.NewNetwork(t)
	reg := testharness.StartRegistry(t, testharness.RegistryOptions{Network: nw})
	agentImage := testharness.AgentImage(t)
	driver, err := testharness.BuildFixture(ctx, "stackjobs", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := testharness.BuildWorkload(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	push := func(revision string) string {
		img, err := testharness.NewWorkloadImage(runtime.GOARCH, bin, revision)
		if err != nil {
			t.Fatal(err)
		}
		if err := testharness.PushOCIImage(ctx, http.DefaultClient, reg.Direct, reg.User, reg.Password, "team/shop", "2.1", img); err != nil {
			t.Fatal(err)
		}
		return img.Digest
	}
	v1 := push("1")
	ref := reg.EngineAddress + "/team/shop:2.1"
	compose := []byte("services:\n  web:\n    image: " + ref + "\n    command: [\"serve\"]\n")

	// The manager: job engine, audit, registry connections (checks reach
	// the fault proxy from the test process under the Engines' address).
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snap"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Date(2026, 9, 25, 2, 50, 0, 0, time.UTC))
	log := testutil.Logger(t)
	auditLog, err := audit.New(audit.Options{DB: db, Clock: clk, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey(nil)
	dialer := &net.Dialer{}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == reg.EngineAddress {
			addr = strings.TrimPrefix(reg.ProxyURL, "http://")
		}
		return dialer.DialContext(ctx, network, addr)
	}}}
	regs, err := registries.New(registries.Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: clk, Logger: log, Guard: ownerGuard{},
		Audit: auditLog, Client: regclient.New(regclient.Options{HTTP: hc, Clock: clk, Logger: log})})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := regs.Create(ctx, domain.RegistryConnectionInput{Name: "registry fixture", Host: reg.EngineAddress, Username: reg.User,
		Secret: reg.Password, PlainHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	disp := jobstest.New()
	commandSecrets := func(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
		creds, err := regs.CommandSecrets(ctx, j)
		if err != nil || len(creds) == 0 {
			return nil, err
		}
		return &protocol.CommandSecrets{Registries: creds}, nil
	}
	eng, err := jobs.New(jobs.Options{DB: db, Clock: clk, Logger: log, Dispatcher: disp, Audit: auditLog, CommandSecrets: commandSecrets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)

	// Two Engines, each with an agent (the fixture) and the stack.
	engines := testharness.StartEngines(t, 2, reg.EngineOptions())
	agents := map[string]*intAgent{}
	st := &intStacks{stacks: map[string]domain.Stack{}}
	for i, e := range engines {
		envID := []string{"env-a", "env-b"}[i]
		e.LoadWorkload(t)
		e.LoadHostImage(t, agentImage)
		c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if _, err := c.CreateVolume(ctx, engine.VolumeSpec{Name: intStacksVolume}); err != nil {
			t.Fatal(err)
		}
		a := &intAgent{t: t, env: envID, e: e, eng: c, img: agentImage, driver: driver}
		agents[envID] = a
		e.PutFiles(t, mount.Mount{Type: mount.TypeVolume, Source: intStacksVolume}, map[string][]byte{"shop/compose.yaml": compose})
		// Deploy with the connection, credentials resolved like a dispatch.
		in := protocol.StackJobInput{StackID: "st-" + envID, Stack: protocol.ProjectRef{Root: protocol.RootStacks, Dir: "shop", ProjectName: "shop"},
			RegistryConnections: []string{conn.ID}}
		raw, _ := json.Marshal(in)
		sec, err := commandSecrets(ctx, &domain.Job{ID: "deploy-" + envID, EnvironmentID: envID, Input: raw})
		if err != nil {
			t.Fatal(err)
		}
		res := a.job("stack.deploy", "shop", in, sec)
		if res.Outcome != "succeeded" {
			t.Fatalf("deploy on %s: %+v", envID, res)
		}
		var out protocol.StackJobOutput
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
		stack := domain.Stack{ID: "st-" + envID, EnvironmentID: envID, Name: "shop", Root: domain.StackRootStacks, Dir: "shop", Status: domain.StackDeployed,
			Applied: &domain.RevisionRef{ID: "rev-" + envID, Seq: 1, Hash: out.Sources.Hash}}
		stack.Observed = stack.Applied
		for _, s := range out.Services {
			stack.Services = append(stack.Services, domain.StackServiceDef{Name: s.Name, Image: s.Image, PullPolicy: s.PullPolicy})
		}
		for _, img := range out.Images {
			if img.Digest != v1 {
				t.Fatalf("%s applied %s, want %s", envID, img.Digest, v1)
			}
			stack.Images = append(stack.Images, domain.StackImage{Service: img.Service, Image: img.Image, ImageID: img.ImageID, Digest: img.Digest,
				Platform: img.Platform})
		}
		st.stacks[stack.ID] = stack
		disp.Connect(envID)
	}

	svc, err := updates.New(updates.Options{DB: db, Clock: clk, Logger: log, Jobs: eng, Stacks: st, Registries: regs,
		Agents: intLink{agents: agents}, Environments: intEnvironments{}, Audit: auditLog})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := scheduler.New(scheduler.Options{DB: db, Clock: clk, Logger: log, Jobs: eng, Audit: auditLog})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(sched); err != nil {
		t.Fatal(err)
	}
	if err := eng.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	for envID := range agents {
		on := domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC", Enabled: true}
		run := domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC", Enabled: true}
		if _, err := svc.Create(ctx, updates.NewPolicy{EnvironmentID: envID, Name: "shop", TargetType: domain.UpdateTargetStack,
			TargetID: "st-" + envID, Check: &on, Run: &run}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for envID, a := range agents {
		before[envID] = a.read("shop").Snapshot.Hash
	}

	// The tag moves to a new build.
	v2 := push("2")

	// 03:00: the scheduled checks run on the manager.
	clk.Set(time.Date(2026, 9, 25, 3, 0, 5, 0, time.UTC))
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := eng.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	eng.Wait()
	// 04:00: the scheduled runs pull and recreate on both agents.
	clk.Set(time.Date(2026, 9, 25, 4, 0, 5, 0, time.UTC))
	if err := sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := eng.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	for envID, a := range agents {
		for _, f := range disp.Drain(envID) {
			if f.Type != protocol.TypeCommand {
				continue
			}
			cmd, err := protocol.DecodePayload[protocol.CommandPayload](f)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Secrets == nil || len(cmd.Secrets.Registries) != 1 {
				t.Fatalf("%s: command without the connection's credential", envID)
			}
			var in protocol.UpdateRunInput
			if err := json.Unmarshal(cmd.Input, &in); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(cmd.Input), reg.Password) {
				t.Fatal("the job input carries the password")
			}
			res := a.job(cmd.Kind, "shop", in, cmd.Secrets)
			ack, _ := protocol.NewFrame(protocol.TypeAck, "ack-"+f.ID, f.ID, f.Ref(), protocol.AckPayload{Accepted: true})
			result, _ := protocol.NewFrame(protocol.TypeResult, "res-"+f.ID, f.ID, f.Ref(), res)
			for _, fr := range []*protocol.Frame{ack, result} {
				if _, err := eng.HandleAgentFrame(ctx, envID, fr); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	runs, err := eng.List(ctx, domain.JobFilter{Kinds: []domain.JobKind{"update.run"}, Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("update runs %d %v", len(runs), err)
	}
	for _, j := range runs {
		if j.State != domain.JobSucceeded || j.Origin != domain.OriginScheduled {
			t.Errorf("run %s on %s: %s %s %s", j.ID, j.EnvironmentID, j.State, j.ErrorClass, j.ErrorMessage)
		}
	}
	for envID, a := range agents {
		stack, _ := st.Get(ctx, "st-"+envID)
		if stack.Images[0].Digest != v2 {
			t.Errorf("%s baseline %s, want %s", envID, stack.Images[0].Digest, v2)
		}
		d, err := a.eng.InspectContainer(ctx, "shop-web-1")
		if err != nil || !d.State.Running {
			t.Fatalf("%s web: %+v %v", envID, d.State, err)
		}
		img, err := a.eng.InspectImage(ctx, d.ImageID)
		if err != nil || !strings.Contains(strings.Join(img.RepoDigests, ","), v2) {
			t.Errorf("%s runs %v, want %s", envID, img.RepoDigests, v2)
		}
		read := a.read("shop")
		if read.Snapshot.Hash != before[envID] {
			t.Errorf("%s definition changed", envID)
		}
		for _, f := range read.Snapshot.Files {
			if strings.Contains(string(f.Content), reg.Password) {
				t.Errorf("%s: %s contains the credential", envID, f.Path)
			}
		}
	}
	// The database never holds the plaintext credential outside its sealed column.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), reg.Password) {
		t.Fatal("the database contains the registry password in plaintext")
	}
}
