package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeEngine implements EngineAPI in memory.
type fakeEngine struct {
	mu         sync.Mutex
	id         engine.Identity
	containers []engine.Container
	stats      map[string]engine.Stats
	statsErr   map[string]error
	// block makes Stats of these containers wait for the deadline.
	block     map[string]bool
	listErr   error
	events    []engine.Event
	eventsErr error
	since     []time.Time
	// eventsCalled receives one value per Events call once its events
	// were handled (when set).
	eventsCalled chan struct{}
}

func (f *fakeEngine) Identity() engine.Identity { return f.id }

func (f *fakeEngine) ListContainers(_ context.Context, fl engine.ContainerFilter) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []engine.Container
	for _, c := range f.containers {
		if fl.All || c.State == "running" {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeEngine) Stats(ctx context.Context, id string, _ bool, fn func(engine.Stats) error) error {
	f.mu.Lock()
	st, ok := f.stats[id]
	err := f.statsErr[id]
	block := f.block[id]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return &engine.Error{Code: engine.CodeTimeout, Op: "stats.stream"}
	}
	if err != nil {
		return err
	}
	if !ok {
		return &engine.Error{Code: engine.CodeNotFound, Op: "stats.stream"}
	}
	return fn(st)
}

func (f *fakeEngine) ListImages(context.Context, bool) ([]engine.Image, error) {
	return make([]engine.Image, 3), nil
}

func (f *fakeEngine) ListVolumes(context.Context, ...string) ([]engine.Volume, error) {
	return nil, errors.New("volumes unavailable")
}

func (f *fakeEngine) ListNetworks(context.Context, ...string) ([]engine.Network, error) {
	return make([]engine.Network, 4), nil
}

func (f *fakeEngine) Events(ctx context.Context, fl engine.EventFilter, fn func(engine.Event) error) error {
	f.mu.Lock()
	evs, err := f.events, f.eventsErr
	f.since = append(f.since, fl.Since)
	called := f.eventsCalled
	f.mu.Unlock()
	for _, e := range evs {
		if !fl.Since.IsZero() && e.Time.Before(fl.Since) {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if called != nil {
		called <- struct{}{} // after the events were handled
	}
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func procFS(stat, mem, load, uptime, netdev string) fstest.MapFS {
	return fstest.MapFS{
		"stat":       {Data: []byte(stat)},
		"meminfo":    {Data: []byte(mem)},
		"loadavg":    {Data: []byte(load)},
		"uptime":     {Data: []byte(uptime)},
		"1/net/dev":  {Data: []byte(netdev)},
		"irrelevant": {Data: []byte("x")},
	}
}

const (
	statA = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\nintr 1\n"
	// +300 busy (user 200, system 100), +100 idle, +0 iowait: 75% busy.
	statB       = "cpu  300 0 200 800 100 0 0 0 0 0\ncpu0 150 0 100 400 50 0 0 0 0 0\ncpu1 150 0 100 400 50 0 0 0 0 0\n"
	meminfoText = "MemTotal:       8000000 kB\nMemFree:         1000000 kB\nMemAvailable:    6000000 kB\nBuffers: 1 kB\n"
	netA        = "Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n" +
		"    lo: 5000 1 0 0 0 0 0 0 5000 1 0 0 0 0 0 0\n  eth0: 1000 1 0 0 0 0 0 0 2000 1 0 0 0 0 0 0\n" +
		"veth12: 999999 1 0 0 0 0 0 0 999999 1 0 0 0 0 0 0\n"
	netB = "Inter-|   Receive |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n" +
		"    lo: 9000 1 0 0 0 0 0 0 9000 1 0 0 0 0 0 0\n  eth0: 11000 1 0 0 0 0 0 0 7000 1 0 0 0 0 0 0\n" +
		"veth12: 1999999 1 0 0 0 0 0 0 1999999 1 0 0 0 0 0 0\n"
)

func newTestSampler(t *testing.T, fsys fstest.MapFS, eng *fakeEngine) *Sampler {
	t.Helper()
	var e func() EngineAPI
	if eng != nil {
		e = func() EngineAPI { return eng }
	}
	return New(Options{Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Proc: fsys, Sys: fstest.MapFS{}, Engine: e, Epoch: "epoch-1",
		NetNS: func(pid string) (string, error) { return "net:[4026531840]", nil },
		Statfs: func(path string) (DiskStat, error) {
			switch path {
			case "/var/lib/docker/volumes":
				return DiskStat{UsedBytes: 40, TotalBytes: 100, Device: "2049"}, nil
			case "/var/lib/docker/volumes/docker-manager_stacks/_data":
				return DiskStat{UsedBytes: 40, TotalBytes: 100, Device: "2049"}, nil // same filesystem
			case "/srv/stacks":
				return DiskStat{UsedBytes: 5, TotalBytes: 50, Device: "2065"}, nil
			}
			return DiskStat{}, errors.New("no such path")
		},
		Roots: func() []Root {
			return []Root{{Kind: "bind", Path: "/srv/stacks"}, {Kind: "stacks", Path: "/var/lib/docker/volumes/docker-manager_stacks/_data"},
				{Kind: "volumes", Path: "/var/lib/docker/volumes"}, {Kind: "bind", Path: "/missing"}}
		},
	})
}

func TestHostSamplesFromProcfs(t *testing.T) {
	fsys := procFS(statA, meminfoText, "0.50 1.25 2.00 1/100 42\n", "3600.55 100.00\n", netA)
	s := newTestSampler(t, fsys, nil)
	t0 := testutil.Epoch
	b1 := s.Tick(context.Background(), t0)
	h := b1.Host
	// The first sample has no previous counters: CPU and network rates
	// are unknown (gaps), never zero.
	if h.CPUPercent != nil || h.NetworkRxBytesPerSecond != nil || h.NetworkTxBytesPerSecond != nil {
		t.Fatalf("first sample has rates: %+v", h)
	}
	if h.CPUs != 2 || *h.MemoryTotalBytes != 8000000*1024 || *h.MemoryAvailableBytes != 6000000*1024 ||
		*h.MemoryUsedBytes != 2000000*1024 || *h.Load1 != 0.5 || *h.Load15 != 2 || *h.UptimeSeconds != 3600 || h.NetworkScope != "agent" {
		t.Fatalf("host sample %+v", h)
	}
	if b1.Flags&protocol.BatchEngineUnavailable == 0 || len(b1.Containers) != 0 {
		t.Fatalf("without an Engine the batch must be flagged: %+v", b1)
	}
	fsys["stat"] = &fstest.MapFile{Data: []byte(statB)}
	fsys["1/net/dev"] = &fstest.MapFile{Data: []byte(netB)}
	b2 := s.Tick(context.Background(), t0.Add(10*time.Second))
	if b2.Host.CPUPercent == nil || *b2.Host.CPUPercent != 75 {
		t.Fatalf("cpu %v", b2.Host.CPUPercent)
	}
	// eth0 only: loopback and veths are excluded. rx +10000, tx +5000 in 10 s.
	if *b2.Host.NetworkRxBytesPerSecond != 1000 || *b2.Host.NetworkTxBytesPerSecond != 500 {
		t.Fatalf("network %v %v", *b2.Host.NetworkRxBytesPerSecond, *b2.Host.NetworkTxBytesPerSecond)
	}
	// Disks: the Docker filesystem once (the stacks volume shares it), the
	// bind root, the unreadable root skipped; labels, never paths.
	want := []protocol.DiskSample{{Mount: "docker", UsedBytes: 40, TotalBytes: 100}, {Mount: "bind-1", UsedBytes: 5, TotalBytes: 50}}
	if fmt.Sprint(b2.Disks) != fmt.Sprint(want) {
		t.Fatalf("disks %+v", b2.Disks)
	}
	// Counters going backwards (reboot, wrap) give a gap, not a negative.
	fsys["1/net/dev"] = &fstest.MapFile{Data: []byte(netA)}
	b3 := s.Tick(context.Background(), t0.Add(20*time.Second))
	if b3.Host.NetworkRxBytesPerSecond != nil {
		t.Fatalf("rate after a counter reset: %v", *b3.Host.NetworkRxBytesPerSecond)
	}
	// A missing file is a gap for that value only.
	delete(fsys, "loadavg")
	b4 := s.Tick(context.Background(), t0.Add(30*time.Second))
	if b4.Host.Load1 != nil || b4.Host.MemoryTotalBytes == nil {
		t.Fatalf("missing loadavg: %+v", b4.Host)
	}
	s.opts.NetNS = func(pid string) (string, error) { return "net:[" + pid + "]", nil }
	if b := s.Tick(context.Background(), t0.Add(40*time.Second)); b.Host.NetworkScope != "host" {
		t.Fatalf("scope %q", b.Host.NetworkScope)
	}
	// network_mode: host without pid: host: PID 1 is the agent, but the
	// counters are the host's (Docker's default bridge is visible; the
	// bridge itself is excluded from the sum: eth0 +10000 in 10 s).
	s.opts.NetNS = func(string) (string, error) { return "net:[1]", nil }
	fsys["1/net/dev"] = &fstest.MapFile{Data: []byte(netB + "docker0: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n")}
	if b := s.Tick(context.Background(), t0.Add(50*time.Second)); b.Host.NetworkScope != "host" || *b.Host.NetworkRxBytesPerSecond != 1000 {
		t.Fatalf("network_mode host: scope %q %+v", b.Host.NetworkScope, b.Host)
	}
}

func TestProcParsersRejectGarbage(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"stat":    {"stat": {Data: []byte("intr 1\n")}},
		"meminfo": {"meminfo": {Data: []byte("MemFree: 1 kB\n")}},
		"loadavg": {"loadavg": {Data: []byte("x y z")}},
		"uptime":  {"uptime": {Data: []byte("")}},
		"netdev":  {"1/net/dev": {Data: []byte("header only\n")}},
	} {
		var err error
		switch name {
		case "stat":
			_, _, err = readCPU(fsys)
		case "meminfo":
			_, err = readMem(fsys)
		case "loadavg":
			_, err = readLoad(fsys)
		case "uptime":
			_, err = readUptime(fsys)
		case "netdev":
			_, _, _, err = readNetDev(fsys, "1/net/dev")
		}
		if err == nil {
			t.Errorf("%s: garbage accepted", name)
		}
	}
	// Kernels without MemAvailable: free + buffers + cached.
	m, err := readMem(fstest.MapFS{"meminfo": {Data: []byte("MemTotal: 100 kB\nMemFree: 10 kB\nBuffers: 5 kB\nCached: 20 kB\n")}})
	if err != nil || m.available != 35*1024 {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestContainerSamples(t *testing.T) {
	eng := &fakeEngine{
		id: engine.Identity{NCPU: 4, MemTotal: 1000},
		containers: []engine.Container{
			{ID: "c1", Names: []string{"/web"}, State: "running"},
			{ID: "c2", Names: []string{"/db"}, State: "running"},
			{ID: "c3", Names: []string{"/gone"}, State: "running"},
			{ID: "c4", Names: []string{"/broken"}, State: "running"},
			{ID: "c5", Names: []string{"/stopped"}, State: "exited"},
		},
		// One-shot stats: cumulative CPU counters, no CPU percentage.
		stats: map[string]engine.Stats{
			"c1": {CPUTotalUsage: 1000, SystemCPUUsage: 10_000, OnlineCPUs: 4, MemoryUsage: 300, MemoryLimit: 1000, NetworkRx: 100,
				NetworkTx: 50, BlockRead: 10, BlockWrite: 20, PIDs: 7},
			// A limit below the host memory is the container's own limit.
			"c2": {CPUTotalUsage: 0, SystemCPUUsage: 10_000, OnlineCPUs: 4, MemoryUsage: 100, MemoryLimit: 256},
		},
		statsErr: map[string]error{"c4": &engine.Error{Code: engine.CodeTimeout, Op: "stats.stream"}},
	}
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), eng)
	b := s.Tick(context.Background(), testutil.Epoch)
	// gone (not found) is skipped silently; broken (timeout) marks the
	// batch as incomplete.
	if len(b.Containers) != 2 || b.Flags != protocol.BatchContainersTruncated {
		t.Fatalf("batch %+v", b)
	}
	// The first read has no previous counters: CPU and rates are gaps.
	db, web := b.Containers[0], b.Containers[1]
	if web.Name != "web" || web.CPUPercent != nil || *web.MemoryBytes != 300 || web.MemoryLimitBytes != nil || *web.PIDs != 7 ||
		web.NetworkRxBytesPerSecond != nil {
		t.Fatalf("web %+v", web)
	}
	if db.Name != "db" || db.CPUPercent != nil || *db.MemoryLimitBytes != 256 {
		t.Fatalf("db %+v", db)
	}
	eng.mu.Lock()
	// web: +2000 of +4000 host CPU time on 4 online cores = 200% of one
	// core, 50% of the environment's 4 cores; db: +400 = 10%.
	eng.stats["c1"] = engine.Stats{CPUTotalUsage: 3000, SystemCPUUsage: 14_000, OnlineCPUs: 4, MemoryUsage: 300, MemoryLimit: 1000,
		NetworkRx: 1100, NetworkTx: 150, BlockRead: 10, BlockWrite: 520}
	eng.stats["c2"] = engine.Stats{CPUTotalUsage: 400, SystemCPUUsage: 14_000, OnlineCPUs: 4, MemoryUsage: 100, MemoryLimit: 256}
	delete(eng.statsErr, "c4")
	eng.stats["c4"] = engine.Stats{}
	eng.mu.Unlock()
	b = s.Tick(context.Background(), testutil.Epoch.Add(10*time.Second))
	db, web = b.Containers[1], b.Containers[2]
	if b.Flags != 0 || web.Name != "web" || *web.CPUPercent != 50 || *web.NetworkRxBytesPerSecond != 100 || *web.NetworkTxBytesPerSecond != 10 ||
		*web.BlockReadBytesPerSecond != 0 || *web.BlockWriteBytesPerSecond != 50 {
		t.Fatalf("second batch %+v web %+v", b, web)
	}
	if db.Name != "db" || *db.CPUPercent != 10 {
		t.Fatalf("db %+v", db)
	}
	if broken := b.Containers[0]; broken.Name != "broken" || broken.CPUPercent != nil {
		t.Fatalf("broken %+v", broken)
	}
	// A restarted container's counters reset: no negative rate or CPU.
	eng.mu.Lock()
	eng.stats["c1"] = engine.Stats{NetworkRx: 5, CPUTotalUsage: 10, SystemCPUUsage: 15_000, OnlineCPUs: 4}
	eng.mu.Unlock()
	b = s.Tick(context.Background(), testutil.Epoch.Add(20*time.Second))
	if web := b.Containers[2]; web.NetworkRxBytesPerSecond != nil || web.CPUPercent != nil {
		t.Fatalf("values after reset %+v", web)
	}
	// The Engine failing to list is flagged.
	eng.mu.Lock()
	eng.listErr = &engine.Error{Code: engine.CodeEngineUnavailable, Op: "container.list"}
	eng.mu.Unlock()
	if b := s.Tick(context.Background(), testutil.Epoch.Add(30*time.Second)); b.Flags != protocol.BatchEngineUnavailable || len(b.Containers) != 0 {
		t.Fatalf("list failure %+v", b)
	}
}

// TestSlowStatsRotateThroughContainers: when the interval ends before every
// container's stats were requested, the next tick starts with the ones
// left out, so no container is starved on a large host.
func TestSlowStatsRotateThroughContainers(t *testing.T) {
	eng := &fakeEngine{
		id: engine.Identity{NCPU: 1, MemTotal: 1000},
		containers: []engine.Container{
			{ID: "a", Names: []string{"/a"}, State: "running"}, {ID: "b", Names: []string{"/b"}, State: "running"},
			{ID: "c", Names: []string{"/c"}, State: "running"}, {ID: "d", Names: []string{"/d"}, State: "running"},
		},
		stats: map[string]engine.Stats{"a": {}, "b": {}, "c": {}, "d": {}},
		// b's stats never answer within the interval.
		block: map[string]bool{"b": true},
	}
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), eng)
	// One stats call at a time and a short interval: the deadline (80% of
	// it) ends the tick while b blocks, before c and d were requested.
	s.opts.StatsConcurrency, s.opts.Interval = 1, 50*time.Millisecond
	names := func(b protocol.MetricBatch) string {
		var out []string
		for _, c := range b.Containers {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	b := s.Tick(context.Background(), testutil.Epoch)
	if names(b) != "a" || b.Flags != protocol.BatchContainersTruncated {
		t.Fatalf("first tick %q flags %d", names(b), b.Flags)
	}
	// The next tick starts with c: c, d and a are sampled, then b blocks.
	b = s.Tick(context.Background(), testutil.Epoch.Add(10*time.Second))
	if names(b) != "a,c,d" || b.Flags != protocol.BatchContainersTruncated {
		t.Fatalf("second tick %q flags %d", names(b), b.Flags)
	}
	// Once b answers again every container is sampled, in name order.
	eng.mu.Lock()
	eng.block = nil
	eng.mu.Unlock()
	b = s.Tick(context.Background(), testutil.Epoch.Add(20*time.Second))
	if names(b) != "a,b,c,d" || b.Flags != 0 {
		t.Fatalf("third tick %q flags %d", names(b), b.Flags)
	}
}

func hostMetrics(t *testing.T, s *Sampler, in protocol.HostMetricsInput) protocol.HostMetricsOutput {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := s.HostMetrics(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	o := out.(protocol.HostMetricsOutput)
	if err := o.Validate(); err != nil {
		t.Fatalf("output invalid: %v", err)
	}
	return o
}

func TestRingIsBoundedAndServedByCursor(t *testing.T) {
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), nil)
	s.opts.Buffer = 5
	for i := range 8 {
		s.Tick(context.Background(), testutil.Epoch.Add(time.Duration(i)*10*time.Second))
	}
	// Unknown epoch: the whole buffer (the oldest 3 were dropped).
	o := hostMetrics(t, s, protocol.HostMetricsInput{Epoch: "other", AfterSeq: 7})
	if o.Epoch != "epoch-1" || o.OldestSeq != 4 || o.LastSeq != 8 || len(o.Batches) != 5 || o.Batches[0].Seq != 4 || o.More {
		t.Fatalf("%+v", o)
	}
	o = hostMetrics(t, s, protocol.HostMetricsInput{Epoch: "epoch-1", AfterSeq: 6})
	if len(o.Batches) != 2 || o.Batches[0].Seq != 7 {
		t.Fatalf("%+v", o)
	}
	o = hostMetrics(t, s, protocol.HostMetricsInput{Epoch: "epoch-1", AfterSeq: 3, MaxBatches: 2})
	if len(o.Batches) != 2 || !o.More || o.Batches[1].Seq != 5 {
		t.Fatalf("%+v", o)
	}
	if o := hostMetrics(t, s, protocol.HostMetricsInput{Epoch: "epoch-1", AfterSeq: 8}); len(o.Batches) != 0 || o.More {
		t.Fatalf("%+v", o)
	}
	if _, err := s.HostMetrics(context.Background(), json.RawMessage(`{"afterSeq":"x"}`)); err == nil {
		t.Fatal("malformed input accepted")
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	eng := &fakeEngine{id: engine.Identity{NCPU: 1}, stats: map[string]engine.Stats{}}
	for i := range protocol.MaxContainerSamples + 5 {
		id := fmt.Sprintf("c%04d", i)
		eng.containers = append(eng.containers, engine.Container{ID: id, Names: []string{"/container-with-a-long-name-" + id}, State: "running"})
		eng.stats[id] = engine.Stats{CPUPercent: 1, MemoryUsage: 123456789, PIDs: 3}
	}
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), eng)
	s.opts.StatsConcurrency = 64
	for i := range 12 {
		b := s.Tick(context.Background(), testutil.Epoch.Add(time.Duration(i)*10*time.Second))
		if len(b.Containers) != protocol.MaxContainerSamples || b.Flags&protocol.BatchContainersTruncated == 0 {
			t.Fatalf("containers %d flags %d", len(b.Containers), b.Flags)
		}
	}
	o := hostMetrics(t, s, protocol.HostMetricsInput{})
	raw, _ := json.Marshal(o)
	if !o.More || len(raw) > protocol.MaxFrameSize-4096 {
		t.Fatalf("response of %d bytes with %d batches (more=%v)", len(raw), len(o.Batches), o.More)
	}
}

func TestRunSamplesOnIntervalBoundaries(t *testing.T) {
	clk := testutil.FakeClock()
	clk.Set(testutil.Epoch.Add(3 * time.Second))
	ticked := make(chan protocol.MetricBatch, 3)
	s := New(Options{Clock: clk, Logger: testutil.Logger(t), Proc: procFS(statA, meminfoText, "0 0 0", "1", netA), Sys: fstest.MapFS{}, Epoch: "e",
		NetNS: func(string) (string, error) { return "", errors.New("no") }, Statfs: func(string) (DiskStat, error) { return DiskStat{}, nil },
		onTick: func(b protocol.MetricBatch) { ticked <- b }})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	for i := range 3 {
		if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(10 * time.Second)
		if b := <-ticked; b.Seq != uint64(i+1) {
			t.Fatalf("tick %d has seq %d", i, b.Seq)
		}
	}
	cancel()
	<-done
	for i, b := range s.ring {
		if want := testutil.Epoch.Add(time.Duration(i+1) * 10 * time.Second); !b.At.Equal(want) {
			t.Errorf("batch %d at %s, want %s", i, b.At, want)
		}
	}
}

func TestEngineInventory(t *testing.T) {
	eng := &fakeEngine{
		id: engine.Identity{EngineID: "ENG", Name: "nas", Version: "28.5.2", APIVersion: "1.51", NegotiatedAPIVersion: "1.51", OS: "linux",
			Arch: "amd64", NCPU: 8, MemTotal: 16 << 30, DockerRootDir: "/var/lib/docker"},
		containers: []engine.Container{{State: "running"}, {State: "paused"}, {State: "exited"}, {State: "restarting"}},
	}
	s := newTestSampler(t, procFS(statA, meminfoText, "0 0 0", "1", netA), eng)
	out, err := s.EngineInfo(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := out.(protocol.EngineInventory)
	if err := inv.Validate(); err != nil {
		t.Fatal(err)
	}
	if inv.EngineID != "ENG" || inv.Hostname != "nas" || inv.CPUs != 8 || inv.Containers != 4 || inv.ContainersRunning != 2 ||
		inv.ContainersPaused != 1 || inv.ContainersStopped != 1 || inv.Images != 3 || inv.Volumes != -1 || inv.Networks != 4 {
		t.Fatalf("%+v", inv)
	}
	raw, _ := json.Marshal(inv)
	if strings.Contains(string(raw), "/var/lib/docker") {
		t.Fatalf("inventory exposes a host path: %s", raw)
	}
	s.opts.Engine = func() EngineAPI { return nil }
	if _, err := s.EngineInfo(context.Background(), nil); err == nil {
		t.Fatal("no error without an Engine")
	}
}
