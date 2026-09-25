//go:build integration

package observe_test

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineObserveTwoEnvironments runs a real manager in the test process
// and the real agent image in two Docker-in-Docker Engines (the #3
// fixture). It proves, per Engine of the matrix:
//
//   - both environments appear separately with the identity, capacity and
//     Docker counts of their own Engine and advertise the observation
//     requests;
//   - host and container samples arrive every 10 s through the collector;
//   - a container started directly on one Engine is relayed as a Docker
//     event of that environment only and refreshes its inventory;
//   - an agent that is stopped and started again leaves a gap in the
//     series (no zeros) and no sample is stored twice.
func TestEngineObserveTwoEnvironments(t *testing.T) {
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
	sub := m.Events().Subscribe(8192, nil)
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
		engine *testharness.Engine
		info   client.SystemInfoResult
		agent  string
		env    string
	}
	all := make([]*started, len(engines))
	byEngine := map[string]*started{}
	for i, e := range engines {
		e.LoadHostImage(t, img)
		info, err := e.Client(t).Info(ctx, client.InfoOptions{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: []string{"alpha", "beta"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		id := e.StartAgent(t, testharness.AgentOptions{Image: img, Name: "dockyard-agent", Env: []string{
			"DOCKYARD_MANAGER_URL=http://" + e.HostAccessAddress(t, port),
			"DOCKYARD_MANAGER_ALLOW_HTTP=true",
			"DOCKYARD_ENROLLMENT_TOKEN=" + c.Token,
		}})
		all[i] = &started{engine: e, info: info, agent: id}
		byEngine[info.Info.ID] = all[i]
	}
	engines[0].LoadWorkload(t)

	// Wait for bus events matching want (also returns earlier matches).
	var seen []events.Event
	waitEvent := func(what string, timeout time.Duration, want func(events.Event) bool) events.Event {
		t.Helper()
		for _, e := range seen {
			if want(e) {
				return e
			}
		}
		deadline := time.After(timeout)
		for {
			select {
			case e := <-sub.C():
				seen = append(seen, e)
				if want(e) {
					return e
				}
			case <-deadline:
				for _, s := range all {
					t.Logf("agent on %s:\n%s", s.engine.Alias, s.engine.Logs(ctx, t, s.agent))
				}
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitUntil := func(what string, timeout time.Duration, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			select {
			case e := <-sub.C():
				seen = append(seen, e)
			case <-time.After(time.Second):
			}
		}
	}

	// Two environments, each with its own Engine's identity and capacity.
	online := map[string]bool{}
	for len(online) < 2 {
		e := waitEvent("two environments online", 4*time.Minute, func(e events.Event) bool {
			return e.Type == events.EnvironmentOnline && !online[e.EnvironmentID]
		})
		online[e.EnvironmentID] = true
	}
	for id := range online {
		env, err := m.Agents().GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		s := byEngine[env.EngineID]
		if s == nil {
			t.Fatalf("environment %s has Engine %s of no started Engine", id, env.EngineID)
		}
		s.env = id
		inv, ok := m.Observe().Inventory(id)
		info := s.info.Info
		if !ok || inv.EngineID != info.ID || inv.Hostname != info.Name || inv.Version != s.engine.Version.Version || inv.CPUs != info.NCPU ||
			inv.MemoryBytes != info.MemTotal || inv.OS != "linux" || inv.ContainersRunning < 1 || inv.NegotiatedAPIVersion == "" {
			t.Fatalf("inventory of %s (Engine %s): %+v", env.Name, info.ID, inv)
		}
		sys, err := m.Agents().EnvironmentSystem(ctx, id)
		if err != nil || sys.Agent == nil {
			t.Fatal(err)
		}
		for _, req := range []string{protocol.ReqEngineInfo, protocol.ReqHostMetrics} {
			if !strings.Contains(sys.Agent.Capabilities, `"`+req+`"`) {
				t.Errorf("%s does not advertise %s: %s", env.Name, req, sys.Agent.Capabilities)
			}
		}
		t.Logf("environment %q: Engine %s (%s, Docker %s), %d CPUs, %d containers running", env.Name, inv.EngineID, inv.Hostname,
			inv.Version, inv.CPUs, inv.ContainersRunning)
	}
	if all[0].env == all[1].env {
		t.Fatal("both Engines map to one environment")
	}

	// Host samples arrive for both environments.
	for _, s := range all {
		waitUntil("host samples of "+s.env, 2*time.Minute, func() bool {
			l, ok, err := m.Metrics().Latest(ctx, s.env)
			return err == nil && ok && l.Host.MemoryTotalBytes != nil && *l.Host.MemoryTotalBytes > 0
		})
	}

	// A container started directly on the first Engine: a Docker event of
	// that environment only, its samples, and a refreshed inventory.
	a := all[0]
	before, _ := m.Observe().Inventory(a.env)
	wl := a.engine.StartWorkload(t, "tick 200")
	insp, err := a.engine.Client(t).ContainerInspect(ctx, wl, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(insp.Container.Name, "/")
	ev := waitEvent("the workload's start event", time.Minute, func(e events.Event) bool {
		return e.Type == events.DockerEvent && e.ResourceType == "container" && e.ResourceID == name && e.Attributes["action"] == "start"
	})
	if ev.EnvironmentID != a.env {
		t.Fatalf("start event in environment %s, want %s", ev.EnvironmentID, a.env)
	}
	waitUntil("the inventory refresh", time.Minute, func() bool {
		inv, _ := m.Observe().Inventory(a.env)
		return inv.ContainersRunning == before.ContainersRunning+1
	})
	waitUntil("samples of "+name, 2*time.Minute, func() bool {
		r, err := m.Metrics().Query(ctx, domain.MetricQuery{EnvironmentID: a.env, Kind: domain.MetricContainer, Name: name,
			From: time.Now().Add(-5 * time.Minute), To: time.Now(), Step: 10 * time.Second, Keys: []string{"memory.used_bytes"}})
		return err == nil && slices.ContainsFunc(r.Series[0].Values, func(v *float64) bool { return v != nil && *v > 0 })
	})
	for _, e := range seen {
		if e.Type == events.DockerEvent && e.ResourceID == name && e.EnvironmentID != a.env {
			t.Fatalf("the workload's event leaked into %s: %+v", e.EnvironmentID, e)
		}
	}

	// Offline interval: stop the second agent for 40 s, start it again.
	b := all[1]
	cli := b.engine.Client(t)
	stopAt := time.Now()
	if _, err := cli.ContainerStop(ctx, b.agent, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	waitEvent("the second environment offline", time.Minute, func(e events.Event) bool {
		return e.Type == events.EnvironmentOffline && e.EnvironmentID == b.env && !e.At.Before(stopAt.Add(-time.Second))
	})
	time.Sleep(40 * time.Second) // the offline interval under test (real time: real agents)
	restartAt := time.Now()
	if _, err := cli.ContainerStart(ctx, b.agent, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	waitEvent("the second environment back online", 2*time.Minute, func(e events.Event) bool {
		return e.Type == events.EnvironmentOnline && e.EnvironmentID == b.env && e.At.After(restartAt)
	})
	waitUntil("samples after the restart", 2*time.Minute, func() bool {
		l, ok, _ := m.Metrics().Latest(ctx, b.env)
		return ok && l.At.After(restartAt)
	})
	r, err := m.Metrics().Query(ctx, domain.MetricQuery{EnvironmentID: b.env, Kind: domain.MetricHost, From: stopAt.Add(-time.Minute),
		To: time.Now(), Step: 10 * time.Second, Keys: []string{"memory.used_bytes"}})
	if err != nil {
		t.Fatal(err)
	}
	var gap, before0, after0 int
	for i, ts := range r.Timestamps {
		v := r.Series[0].Values[i]
		switch {
		case ts.After(stopAt.Add(10*time.Second)) && ts.Before(restartAt.Add(-10*time.Second)):
			if v != nil {
				t.Fatalf("value %v at %s while the agent was stopped", *v, ts)
			}
			gap++
		case v != nil && *v == 0:
			t.Fatalf("zero at %s", ts)
		case v != nil && ts.Before(stopAt):
			before0++
		case v != nil && ts.After(restartAt):
			after0++
		}
	}
	if gap < 2 || before0 == 0 || after0 == 0 {
		t.Fatalf("gap %d buckets, %d before, %d after: %v", gap, before0, after0, r.Timestamps)
	}
	// Every stored slot is unique (the key forbids duplicates): the number
	// of rows equals the number of distinct timestamps.
	var rows, distinct int
	if err := m.Metrics().DB().QueryRowContext(ctx, `SELECT count(*), count(DISTINCT r.ts) FROM host_raw r JOIN series s ON s.id = r.series_id
		WHERE s.environment_id = ?`, b.env).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != distinct || rows == 0 {
		t.Fatalf("%d rows, %d distinct slots", rows, distinct)
	}
	t.Logf("offline gap of %d buckets; %d host samples stored for %s", gap, rows, b.env)
}
