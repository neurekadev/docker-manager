//go:build integration

package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/agents"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineTwoAgentsEnrollAndServeJobs runs a real manager (the full
// app: migrations, agent service, job engine, HTTP stack) in the test
// process and the real agent image in two Docker-in-Docker Engines of the
// matrix. Both agents enroll with one-use tokens, open their sessions and
// report their Engines; both environments appear online with the correct
// identity; each environment lists and resolves only its own Engine's
// containers (#6: the same name, different IDs); a restart job is
// dispatched to each environment in the same pass and each agent runs it
// against its own Engine over its own session; a credential rotation
// completes over each live session. The agents run as containers with the
// deploy mounts, so this also proves the manager needs no Docker socket.
func TestEngineTwoAgentsEnrollAndServeJobs(t *testing.T) {
	img := testharness.AgentImage(t)
	ctx := testutil.Context(t)

	// Manager in the test process, reachable from the Engines through
	// testcontainers' host port access.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	dataDir := filepath.Join(t.TempDir(), "data")
	pub, _ := url.Parse("http://localhost:8080")
	cfg := config.Config{PublicURL: pub, LocalDevelopment: true, ListenAddr: "127.0.0.1:0", DataDir: dataDir,
		SecretKeyFile: filepath.Join(dataDir, config.SecretKeyFileName)}
	m, err := app.Start(ctx, app.Options{Config: cfg, Logger: testutil.Logger(t), UI: fstest.MapFS{"index.html": {Data: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	sub := m.Events().Subscribe(4096, nil)
	serveCtx, stopServe := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- m.Serve(serveCtx, ln) }()
	t.Cleanup(func() {
		stopServe()
		<-served
		_ = m.Close()
	})

	engines := testharness.StartEngines(t, 2, testharness.EngineOptions{HostAccessPorts: []int{port}})
	type started struct {
		engine   *testharness.Engine
		engineID string
		name     string
		agent    string
	}
	var all []started
	webIDs := map[string]string{} // Engine ID -> ID of its "web" container
	for i, e := range engines {
		e.LoadHostImage(t, img)
		e.LoadWorkload(t)
		created, err := e.Client(t).ContainerCreate(ctx, client.ContainerCreateOptions{Name: "web",
			Config: &container.Config{Image: testharness.WorkloadImage, Cmd: []string{"serve", "up"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Client(t).ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		info, err := e.Client(t).Info(ctx, client.InfoOptions{})
		if err != nil {
			t.Fatal(err)
		}
		name := []string{"alpha", "beta"}[i]
		c, err := m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: name})
		if err != nil {
			t.Fatal(err)
		}
		id := e.StartAgent(t, testharness.AgentOptions{Image: img, Name: "dockyard-agent", Env: []string{
			"DOCKYARD_MANAGER_URL=http://" + e.HostAccessAddress(t, port),
			"DOCKYARD_MANAGER_ALLOW_HTTP=true",
			"DOCKYARD_ENROLLMENT_TOKEN=" + c.Token,
		}})
		all = append(all, started{engine: e, engineID: info.Info.ID, name: name, agent: id})
		webIDs[info.Info.ID] = created.ID
	}

	// Both environments come online (after their job reports were
	// reconciled), with the identity of their own Engine.
	online := map[string]bool{}
	deadline := time.After(4 * time.Minute)
	for len(online) < 2 {
		select {
		case ev := <-sub.C():
			if ev.Type == events.EnvironmentOnline {
				online[ev.EnvironmentID] = true
			}
		case <-deadline:
			for _, s := range all {
				t.Logf("agent on %s:\n%s", s.engine.Alias, s.engine.Logs(ctx, t, s.agent))
			}
			t.Fatalf("environments online: %v", online)
		}
	}
	envByEngine := map[string]domain.Environment{}
	for id := range online {
		env, err := m.Agents().GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		envByEngine[env.EngineID] = env
	}
	for _, s := range all {
		env, ok := envByEngine[s.engineID]
		if !ok || env.Name != s.name || !env.Online {
			t.Fatalf("Engine %s (%s): environment %+v", s.engineID, s.engine.Alias, env)
		}
		sys, err := m.Agents().EnvironmentSystem(ctx, env.ID)
		if err != nil || sys.Agent == nil {
			t.Fatalf("system of %s: %v", env.ID, err)
		}
		caps := sys.Agent.Capabilities
		for _, want := range []string{`"id":"` + s.engineID + `"`, `"version":"` + s.engine.Version.Version + `"`, `"plainHttp":true`} {
			if !strings.Contains(caps, want) {
				t.Errorf("%s capabilities lack %s: %s", s.name, want, caps)
			}
		}
		if sys.Agent.VersionStatus != "current" {
			t.Errorf("%s agent version status %s", s.name, sys.Agent.VersionStatus)
		}
		t.Logf("environment %q: Engine %s (Docker %s), agent %s", env.Name, env.EngineID, s.engine.Version.Version, sys.Agent.ID)
	}

	// The Docker resource requests (#6) answer from each environment's own
	// Engine: both have a container named web, with different IDs, and
	// each environment sees only its own.
	for engineID, env := range envByEngine {
		raw, err := m.Agents().Hub().RequestEnvironment(ctx, env.ID, protocol.ReqContainerList, protocol.ContainerListInput{}, 0)
		if err != nil {
			t.Fatalf("container.list on %s: %v", env.Name, err)
		}
		var out protocol.ContainerListOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, c := range out.Containers {
			if c.Name == "web" {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) != 1 || ids[0] != webIDs[engineID] {
			t.Errorf("%s lists web as %v, want %s", env.Name, ids, webIDs[engineID])
		}
		for other, id := range webIDs {
			if other == engineID {
				continue
			}
			_, err := m.Agents().Hub().RequestEnvironment(ctx, env.ID, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: id}, 0)
			var re *agents.RequestError
			if !errors.As(err, &re) || re.Code != protocol.CodeNotFound {
				t.Errorf("%s resolved another Engine's container %s: %v", env.Name, id, err)
			}
		}
	}

	// One restart job per environment, dispatched in one pass; each agent
	// runs it with the #6 executor over its own session against its own
	// Engine. The jobs run as the service identity (like scheduled jobs).
	eng := m.Jobs()
	jobEngine := map[string]string{}
	var jobIDs []string
	for engineID, env := range envByEngine {
		j, _, err := eng.Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: authz.Service(), EnvironmentID: env.ID,
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}},
			Input:   protocol.ContainerActionInput{Name: "web", ID: webIDs[engineID]}})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, j.ID)
		jobEngine[j.ID] = engineID
	}
	if err := eng.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	waitJob := func(id string) {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		changed, unsub := eng.Subscribe(id)
		defer unsub()
		for {
			j, err := eng.Get(waitCtx, id)
			if err != nil {
				t.Error(err)
				return
			}
			if j.State.Terminal() {
				if j.State != domain.JobSucceeded || j.DispatchedAt == nil || j.FencingToken == 0 {
					t.Errorf("job %s: %s %s %q", id, j.State, j.ErrorClass, j.ErrorMessage)
				}
				return
			}
			select {
			case <-changed:
			case <-waitCtx.Done():
				t.Errorf("job %s stuck in %s", id, j.State)
				return
			}
		}
	}
	var wg sync.WaitGroup
	for _, id := range jobIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waitJob(id)
		}()
	}
	wg.Wait()
	for _, s := range all {
		insp, err := s.engine.Client(t).ContainerInspect(ctx, webIDs[s.engineID], client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if insp.Container.State == nil || !insp.Container.State.Running {
			t.Errorf("web on %s not running after the restart", s.name)
		}
	}

	// The #6 operations are reflected in each environment's Docker events
	// (#5 relay): each restart reaches the manager's bus as a docker.event
	// of its own environment, and a volume created through DockYard on one
	// environment is an event of that environment only.
	var dockerEvents []events.Event
	waitDocker := func(what string, want func(events.Event) bool) {
		t.Helper()
		for _, e := range dockerEvents {
			if want(e) {
				return
			}
		}
		deadline := time.After(2 * time.Minute)
		for {
			select {
			case e := <-sub.C():
				if e.Type != events.DockerEvent {
					continue
				}
				dockerEvents = append(dockerEvents, e)
				if want(e) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s; Docker events seen: %+v", what, dockerEvents)
			}
		}
	}
	for _, env := range envByEngine {
		waitDocker("the restart event of web on "+env.Name, func(e events.Event) bool {
			return e.EnvironmentID == env.ID && e.ResourceType == "container" && e.ResourceID == "web" && e.Attributes["action"] == "restart"
		})
	}
	alpha := envByEngine[all[0].engineID]
	vj, err := m.Resources().CreateVolume(ctx, authz.Service(), alpha.ID, protocol.VolumeCreateInput{Name: "dy-events"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	waitJob(vj.ID)
	waitDocker("the volume create event on "+alpha.Name, func(e events.Event) bool {
		return e.EnvironmentID == alpha.ID && e.ResourceType == "volume" && e.ResourceID == "dy-events" && e.Attributes["action"] == "create"
	})
	for _, e := range dockerEvents {
		if e.ResourceID == "dy-events" && e.EnvironmentID != alpha.ID {
			t.Errorf("the volume event leaked into %s: %+v", e.EnvironmentID, e)
		}
	}

	// Credential rotation over each live session: the agent persists the
	// new credential in its state volume and the old one stops working.
	for _, env := range envByEngine {
		r, err := m.Agents().RotateCredential(ctx, env.AgentID)
		if err != nil || r.State != domain.RotationCompleted {
			t.Errorf("rotation for %s: %+v %v", env.Name, r, err)
		}
		if !m.Agents().Hub().Online(env.ID) {
			t.Errorf("%s went offline during rotation", env.Name)
		}
	}
	for _, s := range all {
		logs := s.engine.Logs(ctx, t, s.agent)
		if strings.Contains(logs, "dya_") || strings.Contains(logs, "dye_") {
			t.Errorf("agent on %s logged a secret:\n%s", s.engine.Alias, logs)
		}
	}
}
