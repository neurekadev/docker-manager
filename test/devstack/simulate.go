package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// simulation serves what a real agent reads from the host and the stacks
// volume, which the fake Engine cannot: host and container metrics
// (host.metrics, a 10 s sampler with a pre-filled 30 min buffer), the
// Engine inventory (engine.info) and the Compose projects of the homelab
// (compose.read from the project directories on disk, files.go;
// compose.services from the fake Engine's containers).
type simulation struct {
	host *homelabHost
	clk  clock.Clock

	mu      sync.Mutex
	epoch   string
	seq     uint64
	batches []protocol.MetricBatch
}

func newSimulation(h *homelabHost, clk clock.Clock) *simulation {
	s := &simulation{host: h, clk: clk, epoch: fmt.Sprintf("devstack-%s-%d", h.name, clk.Now().Unix())}
	now := clk.Now().Truncate(protocol.MetricsInterval)
	for i := protocol.MetricsBufferBatches - 1; i >= 0; i-- {
		s.sample(now.Add(-time.Duration(i) * protocol.MetricsInterval))
	}
	return s
}

func (s *simulation) run(ctx context.Context) {
	t := time.NewTicker(protocol.MetricsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-t.C:
			s.mu.Lock()
			s.sample(at.Truncate(protocol.MetricsInterval))
			s.mu.Unlock()
		}
	}
}

// wave is a smooth, deterministic load curve in [0, 1] for a phase.
func wave(at time.Time, period time.Duration, phase float64) float64 {
	x := float64(at.UnixNano())/float64(period) + phase
	return 0.5 + 0.35*math.Sin(2*math.Pi*x) + 0.15*math.Sin(2*math.Pi*x*3.7+phase)
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

// sample appends one batch at the given tick. Callers hold s.mu (or own s).
func (s *simulation) sample(at time.Time) {
	h := s.host
	s.seq++
	b := protocol.MetricBatch{Seq: s.seq, At: at.UTC()}
	var cpu float64
	var mem int64
	for i, c := range h.running() {
		p := h.profile(c.Details.Name)
		cc := p.cpu * (0.6 + 0.8*wave(at, 7*time.Minute, float64(i)))
		cm := int64(float64(p.memory) * (0.92 + 0.08*wave(at, 23*time.Minute, float64(i)*1.3)))
		cpu += cc
		mem += cm
		b.Containers = append(b.Containers, protocol.ContainerSample{Name: c.Details.Name, ID: c.Details.ID, CPUPercent: f64(round(cc, 2)),
			MemoryBytes: i64(cm), NetworkRxBytesPerSecond: f64(round(p.cpu*9000*wave(at, 3*time.Minute, float64(i)), 0)),
			NetworkTxBytesPerSecond: f64(round(p.cpu*4000*wave(at, 5*time.Minute, float64(i)), 0)), PIDs: i64(int64(4 + i*3))})
	}
	hostCPU := math.Min(100, cpu+h.baseCPU*(0.8+0.4*wave(at, 11*time.Minute, 0.3)))
	used := min(h.memory, mem+h.baseMemory)
	b.Host = protocol.HostSample{CPUPercent: f64(round(hostCPU, 2)), CPUs: h.cpus, MemoryUsedBytes: i64(used), MemoryTotalBytes: i64(h.memory),
		MemoryAvailableBytes: i64(h.memory - used), Load1: f64(round(hostCPU/100*float64(h.cpus)*1.1, 2)),
		Load5: f64(round(hostCPU/100*float64(h.cpus), 2)), Load15: f64(round(hostCPU/100*float64(h.cpus)*0.9, 2)),
		NetworkRxBytesPerSecond: f64(round(120000*wave(at, 4*time.Minute, 0), 0)), NetworkTxBytesPerSecond: f64(round(60000*wave(at, 6*time.Minute, 1), 0)),
		NetworkScope: "host", UptimeSeconds: i64(int64(h.uptime.Seconds()) + at.Unix()%1_000_000)}
	b.Disks = []protocol.DiskSample{{Mount: protocol.DiskDocker, UsedBytes: h.diskUsed, TotalBytes: h.diskTotal},
		{Mount: protocol.DiskStacks, UsedBytes: h.diskUsed / 40, TotalBytes: h.diskTotal}}
	s.batches = append(s.batches, b)
	if len(s.batches) > protocol.MetricsBufferBatches {
		s.batches = s.batches[len(s.batches)-protocol.MetricsBufferBatches:]
	}
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func (s *simulation) requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqHostMetrics:     s.hostMetrics,
		protocol.ReqEngineInfo:      s.engineInfo,
		protocol.ReqComposeRead:     s.composeRead,
		protocol.ReqComposeServices: s.composeServices,
	}
}

func (s *simulation) hostMetrics(_ context.Context, raw json.RawMessage) (any, error) {
	var in protocol.HostMetricsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := protocol.HostMetricsOutput{Epoch: s.epoch, Now: s.clk.Now().UTC(), IntervalSeconds: int(protocol.MetricsInterval / time.Second),
		Batches: []protocol.MetricBatch{}}
	if len(s.batches) > 0 {
		out.OldestSeq, out.LastSeq = s.batches[0].Seq, s.batches[len(s.batches)-1].Seq
	}
	after := in.AfterSeq
	if in.Epoch != s.epoch {
		after = 0
	}
	limit := in.MaxBatches
	if limit <= 0 || limit > protocol.MaxMetricsBatchesPerResponse {
		limit = protocol.MaxMetricsBatchesPerResponse
	}
	for _, b := range s.batches {
		if b.Seq <= after {
			continue
		}
		if len(out.Batches) == limit {
			out.More = true
			break
		}
		out.Batches = append(out.Batches, b)
	}
	return out, nil
}

func (s *simulation) engineInfo(context.Context, json.RawMessage) (any, error) {
	h := s.host
	id := h.engine.Identity()
	inv := protocol.EngineInventory{EngineID: id.EngineID, Hostname: h.hostname, Version: id.Version, APIVersion: id.APIVersion,
		MinAPIVersion: id.MinAPIVersion, NegotiatedAPIVersion: id.NegotiatedAPIVersion, OS: id.OS, Arch: id.Arch,
		OperatingSystem: h.os, KernelVersion: h.kernel, StorageDriver: "overlay2", CgroupVersion: "2", CPUs: h.cpus, MemoryBytes: h.memory,
		CollectedAt: s.clk.Now().UTC()}
	ctx := context.Background()
	cs, _ := h.engine.ListContainers(ctx, engine.ContainerFilter{All: true})
	for _, c := range cs {
		inv.Containers++
		switch c.State {
		case "running":
			inv.ContainersRunning++
		case "paused":
			inv.ContainersPaused++
		default:
			inv.ContainersStopped++
		}
	}
	ims, _ := h.engine.ListImages(ctx, false)
	vols, _ := h.engine.ListVolumes(ctx)
	nets, _ := h.engine.ListNetworks(ctx)
	inv.Images, inv.Volumes, inv.Networks = len(ims), len(vols), len(nets)
	return inv, nil
}

func (s *simulation) composeRead(_ context.Context, raw json.RawMessage) (any, error) {
	var in protocol.ComposeReadInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	if err := in.Stack.Validate(); err != nil || in.Stack.Root != protocol.RootStacks {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "the devstack serves stacks in the stacks volume only"}
	}
	snap, ok := readProject(filepath.Join(filepath.FromSlash(s.host.stacksDir), filepath.FromSlash(in.Stack.Dir)))
	if !ok {
		return protocol.ComposeReadOutput{Missing: true, Snapshot: snap}, nil
	}
	return protocol.ComposeReadOutput{Snapshot: snap}, nil
}

func (s *simulation) composeServices(ctx context.Context, raw json.RawMessage) (any, error) {
	var in protocol.ComposeServicesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: err.Error()}
	}
	out := protocol.ComposeServicesOutput{Containers: []protocol.StackContainer{}}
	cs, err := s.host.engine.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{protocol.ComposeProjectLabel + "=" + in.ProjectName}})
	if err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: err.Error()}
	}
	for _, c := range cs {
		d, err := s.host.engine.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		sc := protocol.StackContainer{ID: d.ID, Name: d.Name, Service: d.Labels[protocol.ComposeServiceLabel], Image: d.Image, ImageID: d.ImageID,
			State: d.State.Status, RestartPolicy: d.RestartPolicy, CreatedAt: d.Created,
			Resources: protocol.ContainerResources{NanoCPUs: d.Resources.NanoCPUs, Memory: d.Resources.Memory}}
		if d.State.Health != nil {
			sc.Health = d.State.Health.Status
		}
		if d.State.Running {
			started := d.State.StartedAt
			sc.StartedAt = &started
		}
		for _, p := range d.Ports {
			sc.Ports = append(sc.Ports, protocol.PortMapping{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, HostIP: p.HostIP, Protocol: p.Protocol})
		}
		out.Containers = append(out.Containers, sc)
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].Name < out.Containers[j].Name })
	return out, nil
}
