package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// observeAgents answers engine.info for env-1.
type observeAgents struct{}

func (observeAgents) RequestEnvironment(_ context.Context, env, name string, _ any, _ time.Duration) (json.RawMessage, error) {
	if env != "env-1" || name != protocol.ReqEngineInfo {
		return nil, errors.New("offline")
	}
	return json.Marshal(protocol.EngineInventory{EngineID: "ENG", Hostname: "nas-host", Version: "28.5.2", APIVersion: "1.51",
		NegotiatedAPIVersion: "1.51", OS: "linux", Arch: "amd64", OperatingSystem: "Debian GNU/Linux 12", StorageDriver: "overlay2",
		CPUs: 4, MemoryBytes: 8 << 30, Containers: 5, ContainersRunning: 3, ContainersStopped: 2, Images: 7, Volumes: 2, Networks: 3,
		CollectedAt: testutil.Epoch})
}

func (observeAgents) Online(env string) bool { return env == "env-1" }

type observeFixture struct {
	clk *clock.Fake
	obs *observe.Service
	st  *metrics.Store
	h   http.Handler
	ctx context.Context
}

func newObserveFixture(t *testing.T, pol *authztest.Policy) *observeFixture {
	t.Helper()
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	st, err := metrics.Open(ctx, metrics.Options{Path: filepath.Join(t.TempDir(), "metrics.db"), Clock: clk, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	obs := observe.New(observe.Options{Store: st, Agents: observeAgents{}, Bus: events.New(clk), Clock: clk, Logger: testutil.Logger(t)})
	if err := obs.RefreshInventory(ctx, "env-1"); err != nil {
		t.Fatal(err)
	}
	svc := newFakeAgents()
	pol.Locate(func(ref authz.ResourceRef) policy.Location {
		switch ref.Type {
		case catalog.TypeAgent:
			if a, ok := svc.agents[ref.ID]; ok {
				return policy.Location{Found: true, EnvironmentID: a.EnvironmentID}
			}
		case catalog.TypeContainer:
			return policy.Location{Found: true, EnvironmentID: ref.EnvironmentID}
		}
		return policy.Location{}
	})
	mux := http.NewServeMux()
	New(mux, Deps{Agents: svc, Authorizer: pol, Clock: clk, Idempotency: &memIdempotency{}, Observe: obs})
	return &observeFixture{clk: clk, obs: obs, st: st, h: authztest.Authenticate(withTestContext(t, mux, "")), ctx: ctx}
}

func (f *observeFixture) get(t *testing.T, user, path string, v any) int {
	t.Helper()
	r := authztest.Do(t, f.h, user, authztest.Call{Method: http.MethodGet, Path: path})
	if v != nil && r.Status == http.StatusOK {
		if err := json.Unmarshal(r.Body, v); err != nil {
			t.Fatalf("%s: %v %s", path, err, r.Body)
		}
	}
	return r.Status
}

func fp(v float64) *float64 { return &v }
func ip(v int64) *int64     { return &v }

func observePolicy() *authztest.Policy {
	return authztest.New().Owner("olga").
		Member("mia", "metrics").Group("metrics", "allow environment.metrics.read @env:env-1").
		Member("sam", "system").Group("system", "allow environment.system.read @env:env-1").
		Member("eve", "watch").Group("watch", "allow environment.events.read @env:env-1", "allow container.metrics.read @container:env-1/web").
		Member("rita", "restricted")
}

func TestObserveRoutesFollowCapabilities(t *testing.T) {
	f := newObserveFixture(t, observePolicy())
	for _, c := range []struct {
		user, path string
		want       int
	}{
		{"mia", "/api/v1/environments/env-1/metrics", http.StatusOK},
		{"mia", "/api/v1/environments/env-1/capacity", http.StatusOK},
		{"mia", "/api/v1/environments/env-1/system", http.StatusForbidden},
		{"mia", "/api/v1/environments/env-1/events/stream", http.StatusForbidden},
		{"mia", "/api/v1/environments/env-secret/metrics", http.StatusNotFound},
		{"sam", "/api/v1/environments/env-1/system", http.StatusOK},
		{"sam", "/api/v1/environments/env-1/metrics", http.StatusForbidden},
		{"eve", "/api/v1/environments/env-1/metrics", http.StatusForbidden},
		{"eve", "/api/v1/environments/env-1/capacity", http.StatusForbidden},
		{"rita", "/api/v1/environments/env-1/metrics", http.StatusNotFound},
		{"rita", "/api/v1/environments/env-1/capacity", http.StatusNotFound},
		{"rita", "/api/v1/environments/env-1/events/stream", http.StatusNotFound},
	} {
		if got := f.get(t, c.user, c.path, nil); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.user, c.path, got, c.want)
		}
	}
}

func TestEnvironmentMetricsAndCapacity(t *testing.T) {
	f := newObserveFixture(t, observePolicy())
	t0 := f.clk.Now().Truncate(time.Minute)
	var batch []domain.MetricSample
	for k := range 6 {
		if k == 2 || k == 3 { // offline
			continue
		}
		batch = append(batch, domain.MetricSample{At: t0.Add(time.Duration(k) * 10 * time.Second),
			Host:  &domain.HostValues{CPUPercent: fp(float64(10 * k)), MemoryUsedBytes: ip(1 << 30), MemoryTotalBytes: ip(8 << 30)},
			Disks: []domain.DiskValues{{Mount: "docker", UsedBytes: 10, TotalBytes: 100}}})
	}
	f.clk.Set(t0.Add(time.Minute))
	if _, err := f.st.Ingest(f.ctx, "env-1", batch, nil); err != nil {
		t.Fatal(err)
	}
	var m EnvironmentMetrics
	path := "/api/v1/environments/env-1/metrics?from=" + t0.Format(time.RFC3339) + "&to=" + t0.Add(time.Minute).Format(time.RFC3339) +
		"&stepSeconds=10&series=cpu.percent,disk.used_bytes"
	if st := f.get(t, "mia", path, &m); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	if m.Resolution != "raw" || m.StepSeconds != 10 || len(m.Timestamps) != 6 || len(m.Series) != 2 || !m.Online {
		t.Fatalf("%+v", m)
	}
	cpu := m.Series[0]
	if cpu.Key != "cpu.percent" || cpu.Unit != "percent" || *cpu.Values[1] != 10 || cpu.Values[2] != nil || cpu.Values[3] != nil || *cpu.Values[5] != 50 {
		t.Fatalf("cpu %+v", cpu)
	}
	if m.Series[1].Mount != "docker" || *m.Series[1].Values[0] != 10 {
		t.Fatalf("disk %+v", m.Series[1])
	}
	// Gaps are JSON null, never 0.
	r := authztest.Do(t, f.h, "mia", authztest.Call{Method: http.MethodGet, Path: path})
	if !strings.Contains(string(r.Body), `"values":[0,10,null,null,40,50]`) {
		t.Fatalf("body %s", r.Body)
	}
	for _, bad := range []string{"?series=pids", "?stepSeconds=10&from=" + t0.Add(-20*time.Hour).Format(time.RFC3339),
		"?from=" + t0.Format(time.RFC3339) + "&to=" + t0.Add(-time.Hour).Format(time.RFC3339)} {
		if st := f.get(t, "mia", "/api/v1/environments/env-1/metrics"+bad, nil); st != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", bad, st)
		}
	}
	var c EnvironmentCapacity
	if st := f.get(t, "mia", "/api/v1/environments/env-1/capacity", &c); st != http.StatusOK {
		t.Fatalf("capacity %d", st)
	}
	if c.CPUs != 4 || *c.CPUPercent != 50 || *c.MemoryTotalBytes != 8<<30 || len(c.Disks) != 1 || c.Disks[0].FreeBytes != 90 ||
		c.SampledAt == nil || !c.SampledAt.Equal(t0.Add(50*time.Second)) {
		t.Fatalf("%+v", c)
	}
}

func TestSystemAndOverviewShapeByCapability(t *testing.T) {
	f := newObserveFixture(t, observePolicy().Member("both", "both").Group("both", "allow environment.metrics.read @env:env-1",
		"allow environment.system.read @env:env-1"))
	t0 := f.clk.Now().Truncate(10 * time.Second)
	if _, err := f.st.Ingest(f.ctx, "env-1", []domain.MetricSample{{At: t0, Host: &domain.HostValues{CPUPercent: fp(12.5),
		MemoryUsedBytes: ip(2 << 30), MemoryTotalBytes: ip(8 << 30)}, Disks: []domain.DiskValues{{Mount: "docker", UsedBytes: 1, TotalBytes: 2}}}}, nil); err != nil {
		t.Fatal(err)
	}
	var sys EnvironmentSystem
	if st := f.get(t, "sam", "/api/v1/environments/env-1/system", &sys); st != http.StatusOK {
		t.Fatal(st)
	}
	if sys.Host == nil || sys.Host.Hostname != "nas-host" || sys.Host.CPUs != 4 || sys.Docker == nil || sys.Docker.ContainersRunning != 3 ||
		sys.Engine == nil || sys.Engine.StorageDriver != "overlay2" || sys.Engine.MaxAPIVersion != "1.51" || sys.InventoryAt == nil {
		t.Fatalf("%+v %+v %+v", sys, sys.Host, sys.Engine)
	}
	overview := func(user string) Overview {
		var o Overview
		if st := f.get(t, user, "/api/v1/overview", &o); st != http.StatusOK {
			t.Fatalf("%s: %d", user, st)
		}
		return o
	}
	// The owner sees both environments; usage and counts where known.
	o := overview("olga")
	if o.Totals.Environments != 2 || o.Totals.Online != 1 || o.Totals.Offline != 1 || o.Totals.CountedEnvironments != 1 ||
		o.Totals.Containers != 5 || o.Totals.ContainersRunning != 3 {
		t.Fatalf("owner %+v", o.Totals)
	}
	// Metrics-only: env-1 minimal with usage, no Docker counts, no totals.
	o = overview("mia")
	if len(o.Environments) != 1 || o.Environments[0].View != "minimal" || o.Environments[0].Usage == nil || *o.Environments[0].Usage.CPUPercent != 12.5 ||
		o.Environments[0].Docker != nil || o.Totals.CountedEnvironments != 0 || *o.Environments[0].Usage.DiskTotalBytes != 2 {
		t.Fatalf("metrics-only %+v", o)
	}
	// System-only: counts, no usage.
	o = overview("sam")
	if len(o.Environments) != 1 || o.Environments[0].Usage != nil || o.Environments[0].Docker == nil || o.Totals.Containers != 5 {
		t.Fatalf("system-only %+v", o)
	}
	o = overview("both")
	if o.Environments[0].Usage == nil || o.Environments[0].Docker == nil {
		t.Fatalf("both %+v", o)
	}
	// Restricted: an empty overview, not an error.
	if o := overview("rita"); len(o.Environments) != 0 || o.Totals.Environments != 0 {
		t.Fatalf("restricted %+v", o)
	}
}

// sseClient reads an SSE stream line by line.
type sseClient struct {
	t     *testing.T
	lines chan string
	stop  context.CancelFunc
}

func openStream(t *testing.T, h http.Handler, user, path string, hdr ...string) *sseClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	req.Header.Set(authztest.UserHeader, user)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed below or by the reader goroutine
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("status %d %v", resp.StatusCode, resp.Header)
	}
	c := &sseClient{t: t, lines: make(chan string, 256), stop: cancel}
	go func() {
		defer close(c.lines)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	t.Cleanup(cancel)
	return c
}

type envSSEEvent struct {
	id, name, data string
}

// next returns the next event (skipping heartbeats).
func (c *sseClient) next() envSSEEvent {
	c.t.Helper()
	var e envSSEEvent
	for l := range c.lines {
		switch {
		case l == "":
			if e.name != "" {
				return e
			}
		case strings.HasPrefix(l, "id: "):
			e.id = l[4:]
		case strings.HasPrefix(l, "event: "):
			e.name = l[7:]
		case strings.HasPrefix(l, "data: "):
			e.data = l[6:]
		}
	}
	c.t.Fatal("stream ended")
	return e
}

// until collects events up to and including the first one named name.
func (c *sseClient) until(name string) []envSSEEvent {
	c.t.Helper()
	var out []envSSEEvent
	for {
		e := c.next()
		out = append(out, e)
		if e.name == name {
			return out
		}
	}
}

func dockerEvent(env, typ, id, action string, attrs map[string]string, at time.Time) events.Event {
	a := map[string]string{"source": "engine", "type": typ, "action": action}
	for k, v := range attrs {
		a[k] = v
	}
	return events.Event{Type: events.DockerEvent, ResourceType: typ, ResourceID: id, EnvironmentID: env, Attributes: a, At: at}
}

// TestEnvironmentEventStreamIsPermissionFiltered: a user who may watch
// the environment's events and chart one container sees that container's
// status events (minimal attributes) and metric invalidations, the
// environment's status, and nothing else; the owner sees everything.
func TestEnvironmentEventStreamIsPermissionFiltered(t *testing.T) {
	f := newObserveFixture(t, observePolicy())
	j := f.obs.Journal()
	at := f.clk.Now()
	publish := func() {
		for _, e := range []events.Event{
			dockerEvent("env-1", "container", "web", "start", map[string]string{"name": "web", "image": "secret-registry/app:1"}, at),
			dockerEvent("env-1", "container", "db", "die", map[string]string{"name": "db", "exitCode": "1"}, at),
			dockerEvent("env-1", "volume", "data", "create", nil, at),
			{Type: events.MetricsSampled, ResourceType: events.ResourceEnvironment, ResourceID: "env-1", EnvironmentID: "env-1",
				Attributes: map[string]string{"host": "true"}, Members: []string{"db"}, At: at},
			{Type: events.MetricsSampled, ResourceType: events.ResourceEnvironment, ResourceID: "env-1", EnvironmentID: "env-1",
				Attributes: map[string]string{"host": "true"}, Members: []string{"db", "web"}, At: at},
			{Type: events.InventoryUpdated, ResourceType: events.ResourceEnvironment, ResourceID: "env-1", EnvironmentID: "env-1", At: at},
			dockerEvent("env-secret", "container", "web", "start", map[string]string{"name": "web"}, at),
			{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: "env-1", EnvironmentID: "env-1", At: at},
		} {
			j.Append(e)
		}
	}
	eve := openStream(t, f.h, "eve", "/api/v1/environments/env-1/events/stream")
	hello := eve.next()
	if hello.name != "hello" || !strings.Contains(hello.data, `"version":"docker-manager.environment-events/v1"`) {
		t.Fatalf("hello %+v", hello)
	}
	olga := openStream(t, f.h, "olga", "/api/v1/environments/env-1/events/stream")
	olga.next()
	publish()
	got := eve.until("status")
	var names []string
	for _, e := range got {
		names = append(names, e.name)
	}
	if !slices.Equal(names, []string{"engine", "metrics", "status"}) {
		t.Fatalf("eve received %v", got)
	}
	if got[0].data != `{"type":"container","action":"start","resourceId":"web","attributes":{"name":"web"},"at":"`+at.UTC().Format(time.RFC3339Nano)+`"}` {
		t.Fatalf("minimal container event %s", got[0].data)
	}
	if !strings.Contains(got[1].data, `"host":false,"containers":true`) {
		t.Fatalf("metrics %s", got[1].data)
	}
	if !strings.Contains(got[2].data, `"status":"online"`) || got[2].id == "" {
		t.Fatalf("status %+v", got[2])
	}
	all := olga.until("status")
	if len(all) != 7 || !strings.Contains(all[0].data, "secret-registry/app:1") {
		t.Fatalf("owner received %d: %+v", len(all), all)
	}

	// Resume: the events after a cursor are replayed (filtered again);
	// an unknown or expired cursor starts with a reset.
	resumed := openStream(t, f.h, "eve", "/api/v1/environments/env-1/events/stream", "Last-Event-ID", got[0].id)
	if e := resumed.next(); e.name != "hello" {
		t.Fatal(e)
	}
	if r := resumed.until("status"); len(r) != 2 || r[0].name != "metrics" {
		t.Fatalf("replay %+v", r)
	}
	for id, reason := range map[string]string{"zzzz.1": "server_restart", j.Epoch() + ".999": "cursor_expired", "garbage": "server_restart"} {
		s := openStream(t, f.h, "eve", "/api/v1/environments/env-1/events/stream", "Last-Event-ID", id)
		s.next()
		if e := s.next(); e.name != "reset" || !strings.Contains(e.data, `"reason":"`+reason+`"`) {
			t.Fatalf("%s: %+v", id, e)
		}
	}
}

func TestEnvironmentEventStreamHeartbeatAndMaxAge(t *testing.T) {
	f := newObserveFixture(t, observePolicy())
	s := openStream(t, f.h, "olga", "/api/v1/environments/env-1/events/stream")
	s.next()
	// heartbeat ticker + max-age timer
	if err := f.clk.BlockUntilWaiters(f.ctx, 2); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(DefaultSSEHeartbeat)
	for l := range s.lines {
		if l == ": heartbeat" {
			break
		}
	}
	f.clk.Advance(DefaultStreamMaxAge)
	if e := s.until("close"); !strings.Contains(e[len(e)-1].data, "max_age") {
		t.Fatalf("%+v", e)
	}
}

func TestHostMetricKeysMatchTheStore(t *testing.T) {
	if !slices.Equal(HostMetricKeys, metrics.MetricKeys(domain.MetricHost)) {
		t.Fatalf("api %v\nstore %v", HostMetricKeys, metrics.MetricKeys(domain.MetricHost))
	}
}

func TestEnvironmentMetricsLabelTemperatureSensors(t *testing.T) {
	f := newObserveFixture(t, observePolicy())
	t0 := f.clk.Now().Truncate(time.Minute)
	var batch []domain.MetricSample
	for k := range 6 {
		smp := domain.MetricSample{At: t0.Add(time.Duration(k) * 10 * time.Second), Host: &domain.HostValues{CPUPercent: fp(5)},
			Disks:        []domain.DiskValues{{Mount: "docker", UsedBytes: 10, TotalBytes: 100}},
			Temperatures: []domain.TemperatureValues{{Sensor: "coretemp: Package id 0", Celsius: 48.5 + float64(k)}}}
		if k != 3 { // no reading: a gap
			smp.Temperatures = append(smp.Temperatures, domain.TemperatureValues{Sensor: "nvme: Composite", Celsius: 38})
		}
		batch = append(batch, smp)
	}
	f.clk.Set(t0.Add(time.Minute))
	if _, err := f.st.Ingest(f.ctx, "env-1", batch, nil); err != nil {
		t.Fatal(err)
	}
	var m EnvironmentMetrics
	path := "/api/v1/environments/env-1/metrics?from=" + t0.Format(time.RFC3339) + "&to=" + t0.Add(time.Minute).Format(time.RFC3339) +
		"&stepSeconds=10&series=disk.used_bytes,temperature.celsius,temperature.celsius.max"
	if st := f.get(t, "mia", path, &m); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	// The disk keeps its mount; each sensor has its average and maximum,
	// labelled with its name and in degrees Celsius.
	var got []string
	for _, s := range m.Series {
		got = append(got, s.Key+"|"+s.Unit+"|"+s.Mount+"|"+s.Sensor)
	}
	want := []string{"disk.used_bytes|bytes|docker|", "temperature.celsius|celsius||coretemp: Package id 0",
		"temperature.celsius.max|celsius||coretemp: Package id 0", "temperature.celsius|celsius||nvme: Composite",
		"temperature.celsius.max|celsius||nvme: Composite"}
	if !slices.Equal(got, want) {
		t.Fatalf("series\n got %v\nwant %v", got, want)
	}
	if v := m.Series[1].Values; *v[0] != 48.5 || *v[5] != 53.5 {
		t.Fatalf("cpu %+v", m.Series[1])
	}
	r := authztest.Do(t, f.h, "mia", authztest.Call{Method: http.MethodGet, Path: path})
	if !strings.Contains(string(r.Body), `"sensor":"nvme: Composite","values":[38,38,38,null,38,38]`) {
		t.Fatalf("body %s", r.Body)
	}
}
