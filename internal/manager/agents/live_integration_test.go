//go:build integration

package agents_test

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/live"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineLiveStreamAcrossTwoHosts (#23 Done-when 2): with the real
// manager and the real agent image on two Docker-in-Docker Engines, a
// change made directly through Docker on either host (docker stop on the
// Engine) and a change made through DockYard (a restart job) reach an open
// live stream subscription as records of the right environment: Docker
// events relayed by each agent, and the job's state changes.
func TestEngineLiveStreamAcrossTwoHosts(t *testing.T) {
	img := testharness.AgentImage(t)
	ctx := testutil.Context(t)
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
	bus := m.Events().Subscribe(4096, func(e events.Event) bool { return e.Type == events.EnvironmentOnline })
	serveCtx, stopServe := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- m.Serve(serveCtx, ln) }()
	t.Cleanup(func() {
		stopServe()
		<-served
		_ = m.Close()
	})

	engines := testharness.StartEngines(t, 2, testharness.EngineOptions{HostAccessPorts: []int{port}})
	webIDs := map[*testharness.Engine]string{}
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
		webIDs[e] = created.ID
		c, err := m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: []string{"alpha", "beta"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		e.StartAgent(t, testharness.AgentOptions{Image: img, Name: "dockyard-agent", Env: []string{
			"DOCKYARD_MANAGER_URL=http://" + e.HostAccessAddress(t, port),
			"DOCKYARD_MANAGER_ALLOW_HTTP=true",
			"DOCKYARD_ENROLLMENT_TOKEN=" + c.Token,
		}})
	}
	online := map[string]bool{}
	deadline := time.After(4 * time.Minute)
	for len(online) < 2 {
		select {
		case ev := <-bus.C():
			online[ev.EnvironmentID] = true
		case <-deadline:
			t.Fatalf("environments online: %v", online)
		}
	}
	envOf := map[*testharness.Engine]string{}
	for id := range online {
		env, err := m.Agents().GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range engines {
			info, err := e.Client(t).Info(ctx, client.InfoOptions{})
			if err == nil && info.Info.ID == env.EngineID {
				envOf[e] = env.ID
			}
		}
	}

	// An open stream (the owner's view: no filtering to worry about here;
	// per-user filtering is covered by the unit and app tests).
	sub, err := m.Live().Subscribe("user:it", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Sub.Close()
	// wait returns once every predicate matched a record (in any order).
	wait := func(what string, preds ...func(live.Record) bool) {
		t.Helper()
		timeout := time.After(2 * time.Minute)
		done := make([]bool, len(preds))
		left := len(preds)
		for left > 0 {
			select {
			case r := <-sub.Sub.C():
				for i, p := range preds {
					if !done[i] && p(r) {
						done[i] = true
						left--
					}
				}
			case <-timeout:
				t.Fatalf("no live record for %s (matched %v)", what, done)
			}
		}
	}

	// Directly through Docker, on each host.
	for _, e := range engines {
		if _, err := e.Client(t).ContainerStop(ctx, webIDs[e], client.ContainerStopOptions{}); err != nil {
			t.Fatal(err)
		}
		env := envOf[e]
		wait("docker stop on "+e.Alias, func(r live.Record) bool {
			return r.Event.Type == events.DockerEvent && r.Event.EnvironmentID == env && r.Event.ResourceID == "web" &&
				(r.Event.Attributes["action"] == "die" || r.Event.Attributes["action"] == "stop")
		})
	}

	// Through DockYard: a restart job on each environment.
	for _, e := range engines {
		env := envOf[e]
		j, _, err := m.Jobs().Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: authz.Service(), EnvironmentID: env,
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}},
			Input:   protocol.ContainerActionInput{Name: "web", ID: webIDs[e]}})
		if err != nil {
			t.Fatal(err)
		}
		wait("restart through DockYard on "+e.Alias, func(r live.Record) bool {
			return r.Event.Type == events.JobUpdated && r.Event.ResourceID == j.ID && r.Event.Attributes["state"] == string(domain.JobSucceeded)
		}, func(r live.Record) bool {
			return r.Event.Type == events.DockerEvent && r.Event.EnvironmentID == env && r.Event.ResourceID == "web" &&
				(r.Event.Attributes["action"] == "start" || r.Event.Attributes["action"] == "restart")
		})
	}
}
