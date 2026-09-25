//go:build integration

package agents_test

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineTwoAgentsEnrollAndServeJobs runs a real manager (the full
// app: migrations, agent service, job engine, HTTP stack) in the test
// process and the real agent image in two Docker-in-Docker Engines of the
// matrix. Both agents enroll with one-use tokens, open their sessions and
// report their Engines; both environments appear online with the correct
// identity; a job is dispatched to each environment in the same pass and
// each agent answers it over its own session; a credential rotation
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
	for i, e := range engines {
		e.LoadHostImage(t, img)
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

	// One job per environment, dispatched in one pass. The agent image has
	// no executor for container.restart yet (#6), so each agent answers
	// with a rejecting ack over its own session: the command reached the
	// right agent, carried its fencing token and came back correlated.
	// The manager's authorizer denies users until #17, so the jobs run as
	// the service identity (like scheduled jobs).
	eng := m.Jobs()
	var jobIDs []string
	for _, env := range envByEngine {
		j, _, err := eng.Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: authz.Service(), EnvironmentID: env.ID,
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, j.ID)
	}
	if err := eng.DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range jobIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
					if j.State != domain.JobFailed || j.ErrorClass != domain.ErrorRejected || !strings.Contains(j.ErrorMessage, "unsupported_kind") ||
						j.DispatchedAt == nil || j.FencingToken == 0 {
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
		}()
	}
	wg.Wait()

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
