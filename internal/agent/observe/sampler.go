// Package observe is the agent side of observation (#5): host telemetry
// from procfs, per-container usage and the Engine inventory through the
// Moby adapter (#21), sampled every 10 s into a bounded ring the manager
// fetches with the host.metrics request, and the Docker event relay
// (events.go). It never listens on a socket and never runs the docker CLI.
// Units, buffering and the host mounts it needs: docs/architecture/metrics.md.
package observe

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// EngineAPI is the part of engine.Engine observation uses.
type EngineAPI interface {
	Identity() engine.Identity
	ListContainers(ctx context.Context, f engine.ContainerFilter) ([]engine.Container, error)
	Stats(ctx context.Context, id string, stream bool, fn func(engine.Stats) error) error
	ListImages(ctx context.Context, all bool) ([]engine.Image, error)
	ListVolumes(ctx context.Context, labels ...string) ([]engine.Volume, error)
	ListNetworks(ctx context.Context, labels ...string) ([]engine.Network, error)
	Events(ctx context.Context, f engine.EventFilter, fn func(engine.Event) error) error
}

// Root is a verified storage root (#28) whose filesystem usage is
// reported: kind volumes (Docker's volume directory, i.e. the Docker root
// filesystem), stacks or bind.
type Root struct {
	Kind string
	Path string
}

// DiskStat is one filesystem's usage.
type DiskStat struct {
	UsedBytes, TotalBytes int64
	// Device identifies the filesystem (dedupes roots sharing one).
	Device string
}

// DefaultProcRoot is where procfs is read (DOCKYARD_HOST_PROC).
const DefaultProcRoot = "/proc"

// DefaultStatsConcurrency bounds concurrent per-container stats calls.
const DefaultStatsConcurrency = 8

// Options configures a Sampler.
type Options struct {
	Clock  clock.Clock
	Logger *slog.Logger
	// ProcRoot is the procfs mount (default DefaultProcRoot); Proc reads it
	// (default os.DirFS(ProcRoot)).
	ProcRoot string
	Proc     fs.FS
	// NetNS returns the network namespace of "1" or "self" (default:
	// readlink <ProcRoot>/<pid>/ns/net).
	NetNS func(pid string) (string, error)
	// Engine returns the connected Engine or nil.
	Engine func() EngineAPI
	// Roots returns the verified storage roots.
	Roots func() []Root
	// Statfs reads a filesystem's usage (default: statfs(2) on Linux).
	Statfs func(path string) (DiskStat, error)
	// Interval (default protocol.MetricsInterval) and Buffer (batches kept,
	// default protocol.MetricsBufferBatches).
	Interval time.Duration
	Buffer   int
	// StatsConcurrency bounds concurrent stats calls (default 8).
	StatsConcurrency int
	// Epoch identifies this sampler instance (default a new UUIDv7).
	Epoch string

	onTick func(protocol.MetricBatch) // test hook
}

// Sampler samples host and container usage every Interval into a bounded
// ring and serves it to the manager.
type Sampler struct {
	opts  Options
	log   *slog.Logger
	epoch string

	mu   sync.Mutex
	ring []protocol.MetricBatch
	seq  uint64

	// Sampling state, touched only by the sampling goroutine.
	prevCPU     *cpuTimes
	prevNet     *netCounters
	prevCtr     map[string]ctrCounters
	loggedErrs  map[string]bool
	lastCPUs    int
	lastMemSize int64
	// resume is the first container (by name) whose stats were not even
	// requested in the last tick: the next tick starts there, so on a host
	// with more containers than one interval can sample, every container
	// is sampled in turn instead of always the same ones.
	resume string
}

type netCounters struct {
	rx, tx uint64
	at     time.Time
}

type ctrCounters struct {
	rx, tx, br, bw uint64
	at             time.Time
}

// New returns a Sampler.
func New(opts Options) *Sampler {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ProcRoot == "" {
		opts.ProcRoot = DefaultProcRoot
	}
	if opts.Proc == nil {
		opts.Proc = os.DirFS(opts.ProcRoot)
	}
	if opts.NetNS == nil {
		root := opts.ProcRoot
		opts.NetNS = func(pid string) (string, error) { return os.Readlink(filepath.Join(root, pid, "ns", "net")) }
	}
	if opts.Engine == nil {
		opts.Engine = func() EngineAPI { return nil }
	}
	if opts.Roots == nil {
		opts.Roots = func() []Root { return nil }
	}
	if opts.Statfs == nil {
		opts.Statfs = statfs
	}
	if opts.Interval <= 0 {
		opts.Interval = protocol.MetricsInterval
	}
	if opts.Buffer <= 0 {
		opts.Buffer = protocol.MetricsBufferBatches
	}
	if opts.StatsConcurrency <= 0 {
		opts.StatsConcurrency = DefaultStatsConcurrency
	}
	if opts.Epoch == "" {
		opts.Epoch = ids.New()
	}
	return &Sampler{opts: opts, log: opts.Logger.With("component", "observe"), epoch: opts.Epoch,
		prevCtr: map[string]ctrCounters{}, loggedErrs: map[string]bool{}}
}

// Epoch returns the sampler epoch.
func (s *Sampler) Epoch() string { return s.epoch }

// Run samples at every multiple of Interval until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	clk, iv := s.opts.Clock, s.opts.Interval
	for {
		now := clk.Now()
		next := now.Truncate(iv).Add(iv)
		t := clk.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		b := s.Tick(ctx, next)
		if s.opts.onTick != nil {
			s.opts.onTick(b)
		}
	}
}

// Tick takes one sample stamped at (and appends it to the ring).
func (s *Sampler) Tick(ctx context.Context, at time.Time) protocol.MetricBatch {
	b := protocol.MetricBatch{At: at.UTC()}
	b.Host = s.sampleHost(at)
	b.Disks = s.sampleDisks()
	b.Containers, b.Flags = s.sampleContainers(ctx, at)
	s.mu.Lock()
	s.seq++
	b.Seq = s.seq
	s.ring = append(s.ring, b)
	if over := len(s.ring) - s.opts.Buffer; over > 0 {
		s.ring = slices.Delete(s.ring, 0, over)
	}
	s.mu.Unlock()
	return b
}

// logOnce logs a sampling problem the first time it occurs (and again
// after it cleared), so a missing host file does not flood the log.
func (s *Sampler) logOnce(key string, err error) {
	if err == nil {
		delete(s.loggedErrs, key)
		return
	}
	if !s.loggedErrs[key] {
		s.loggedErrs[key] = true
		s.log.Warn("metrics sampling: value unavailable (reported as a gap)", "value", key, "error", err)
	}
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

// round2 keeps two decimals (the stored precision).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func clampPercent(v float64) float64 { return round2(min(max(v, 0), 100)) }

func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func (s *Sampler) sampleHost(at time.Time) protocol.HostSample {
	var h protocol.HostSample
	fsys := s.opts.Proc
	cpu, cpus, err := readCPU(fsys)
	s.logOnce("cpu", err)
	if err == nil {
		h.CPUs = cpus
		s.lastCPUs = cpus
		if p := s.prevCPU; p != nil && cpu.total > p.total && cpu.busy >= p.busy {
			h.CPUPercent = f64(clampPercent(float64(cpu.busy-p.busy) / float64(cpu.total-p.total) * 100))
		}
		s.prevCPU = &cpu
	} else {
		s.prevCPU = nil
	}
	if m, err := readMem(fsys); err == nil {
		h.MemoryTotalBytes, h.MemoryAvailableBytes, h.MemoryUsedBytes = i64(m.total), i64(m.available), i64(m.total-m.available)
		s.lastMemSize = m.total
		s.logOnce("memory", nil)
	} else {
		s.logOnce("memory", err)
	}
	if l, err := readLoad(fsys); err == nil {
		h.Load1, h.Load5, h.Load15 = f64(round2(l[0])), f64(round2(l[1])), f64(round2(l[2]))
		s.logOnce("load", nil)
	} else {
		s.logOnce("load", err)
	}
	if up, err := readUptime(fsys); err == nil {
		h.UptimeSeconds = i64(up)
		s.logOnce("uptime", nil)
	} else {
		s.logOnce("uptime", err)
	}
	rx, tx, bridge, err := readNetDev(fsys, "1/net/dev")
	s.logOnce("network", err)
	if err == nil {
		h.NetworkScope = s.netScope(bridge)
		if p := s.prevNet; p != nil && rx >= p.rx && tx >= p.tx {
			if dt := at.Sub(p.at).Seconds(); dt > 0 {
				h.NetworkRxBytesPerSecond = f64(round2(float64(rx-p.rx) / dt))
				h.NetworkTxBytesPerSecond = f64(round2(float64(tx-p.tx) / dt))
			}
		}
		s.prevNet = &netCounters{rx: rx, tx: tx, at: at}
	} else {
		s.prevNet = nil
	}
	return h
}

// netScope is host when PID 1 lives in another network namespace than the
// agent (the agent shares the host's PID namespace) or when the counters
// include Docker's default bridge (the agent shares the host's network,
// network_mode: host), agent otherwise.
func (s *Sampler) netScope(bridge bool) string {
	if bridge {
		return "host"
	}
	one, err1 := s.opts.NetNS("1")
	self, err2 := s.opts.NetNS("self")
	if err1 == nil && err2 == nil && one != self {
		return "host"
	}
	return "agent"
}

// sampleDisks reports each distinct filesystem of the verified roots once.
func (s *Sampler) sampleDisks() []protocol.DiskSample {
	roots := s.opts.Roots()
	// The Docker root filesystem first, then stacks, then bind roots.
	order := map[string]int{"volumes": 0, "stacks": 1, "bind": 2}
	slices.SortStableFunc(roots, func(a, b Root) int { return order[a.Kind] - order[b.Kind] })
	seen := map[string]bool{}
	var out []protocol.DiskSample
	binds := 0
	for _, r := range roots {
		label := ""
		switch r.Kind {
		case "volumes":
			label = protocol.DiskDocker
		case "stacks":
			label = protocol.DiskStacks
		case "bind":
			binds++
			label = protocol.DiskBind + "-" + strconv.Itoa(binds)
		default:
			continue
		}
		st, err := s.opts.Statfs(r.Path)
		s.logOnce("disk "+label, err)
		if err != nil || st.TotalBytes <= 0 {
			continue
		}
		key := st.Device
		if key == "" {
			key = "path:" + r.Path
		}
		if seen[key] || len(out) >= protocol.MaxDiskSamples {
			continue
		}
		seen[key] = true
		out = append(out, protocol.DiskSample{Mount: label, UsedBytes: st.UsedBytes, TotalBytes: st.TotalBytes})
	}
	return out
}

func containerName(c engine.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

// sampleContainers reads one stats sample of every running container,
// StatsConcurrency at a time, within 80% of the interval. Containers that
// could not be sampled in time are left out and the batch is flagged; the
// next tick starts with them (round robin by name).
func (s *Sampler) sampleContainers(ctx context.Context, at time.Time) ([]protocol.ContainerSample, int) {
	eng := s.opts.Engine()
	if eng == nil {
		return nil, protocol.BatchEngineUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Interval*8/10)
	defer cancel()
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{})
	s.logOnce("containers", err)
	if err != nil {
		return nil, protocol.BatchEngineUnavailable
	}
	flags := 0
	slices.SortFunc(list, func(a, b engine.Container) int { return strings.Compare(containerName(a), containerName(b)) })
	if len(list) > protocol.MaxContainerSamples {
		list, flags = list[:protocol.MaxContainerSamples], protocol.BatchContainersTruncated
	}
	id := eng.Identity()
	cpus := id.NCPU
	if cpus <= 0 {
		cpus = s.lastCPUs
	}
	memTotal := id.MemTotal
	if memTotal <= 0 {
		memTotal = s.lastMemSize
	}
	results := make([]*engine.Stats, len(list))
	errs := make([]error, len(list))
	sem := make(chan struct{}, s.opts.StatsConcurrency)
	var wg sync.WaitGroup
	start := 0
	if s.resume != "" {
		start, _ = slices.BinarySearchFunc(list, s.resume, func(c engine.Container, name string) int {
			return strings.Compare(containerName(c), name)
		})
		if start >= len(list) {
			start = 0
		}
		s.resume = ""
	}
	for k := range list {
		i := (start + k) % len(list)
		c := list[i]
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			s.resume = containerName(c)
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			errs[i] = eng.Stats(ctx, c.ID, false, func(st engine.Stats) error {
				results[i] = &st
				return nil
			})
		}()
	}
	wg.Wait()
	out := make([]protocol.ContainerSample, 0, len(list))
	seen := make(map[string]ctrCounters, len(list))
	for i, c := range list {
		st := results[i]
		if st == nil {
			// A container that stopped between the list and its stats is
			// simply gone; anything else (timeout, Engine error) is missing.
			if errs[i] == nil || engine.CodeOf(errs[i]) != engine.CodeNotFound {
				flags |= protocol.BatchContainersTruncated
			}
			continue
		}
		cs := protocol.ContainerSample{Name: containerName(c), ID: c.ID, MemoryBytes: i64(clampInt64(st.MemoryUsage)),
			PIDs: i64(clampInt64(st.PIDs))}
		n := cpus
		if n <= 0 {
			n = int(st.OnlineCPUs)
		}
		if n > 0 {
			cs.CPUPercent = f64(clampPercent(st.CPUPercent / float64(n)))
		}
		if st.MemoryLimit > 0 && (memTotal <= 0 || int64(min(st.MemoryLimit, math.MaxInt64)) < memTotal) { //nolint:gosec // G115: clamped
			cs.MemoryLimitBytes = i64(clampInt64(st.MemoryLimit))
		}
		cur := ctrCounters{rx: st.NetworkRx, tx: st.NetworkTx, br: st.BlockRead, bw: st.BlockWrite, at: at}
		if p, ok := s.prevCtr[c.ID]; ok {
			if dt := at.Sub(p.at).Seconds(); dt > 0 {
				cs.NetworkRxBytesPerSecond = rate(p.rx, cur.rx, dt)
				cs.NetworkTxBytesPerSecond = rate(p.tx, cur.tx, dt)
				cs.BlockReadBytesPerSecond = rate(p.br, cur.br, dt)
				cs.BlockWriteBytesPerSecond = rate(p.bw, cur.bw, dt)
			}
		}
		seen[c.ID] = cur
		out = append(out, cs)
	}
	s.prevCtr = seen // containers that stopped are forgotten
	return out, flags
}

// rate is the per-second increase of a counter, nil when it went backwards
// (container restarted: counters reset).
func rate(prev, cur uint64, dt float64) *float64 {
	if cur < prev {
		return nil
	}
	return f64(round2(float64(cur-prev) / dt))
}

// HostMetrics serves the host.metrics request: the buffered batches after
// the manager's cursor, bounded in count and encoded size.
func (s *Sampler) HostMetrics(_ context.Context, input json.RawMessage) (any, error) {
	var in protocol.HostMetricsInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed host.metrics input"}
		}
	}
	limit := in.MaxBatches
	if limit <= 0 || limit > protocol.MaxMetricsBatchesPerResponse {
		limit = protocol.MaxMetricsBatchesPerResponse
	}
	after := in.AfterSeq
	if in.Epoch != s.epoch {
		after = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := protocol.HostMetricsOutput{Epoch: s.epoch, Now: s.opts.Clock.Now().UTC(), IntervalSeconds: int(s.opts.Interval / time.Second),
		Batches: []protocol.MetricBatch{}}
	if out.IntervalSeconds <= 0 {
		out.IntervalSeconds = 1
	}
	if len(s.ring) > 0 {
		out.OldestSeq, out.LastSeq = s.ring[0].Seq, s.ring[len(s.ring)-1].Seq
	}
	size := 0
	for _, b := range s.ring {
		if b.Seq <= after {
			continue
		}
		if len(out.Batches) >= limit {
			out.More = true
			break
		}
		enc, err := json.Marshal(b)
		if err != nil {
			continue
		}
		if size+len(enc) > protocol.MaxMetricsResponseBytes && len(out.Batches) > 0 {
			out.More = true
			break
		}
		size += len(enc)
		out.Batches = append(out.Batches, b)
	}
	return out, nil
}
