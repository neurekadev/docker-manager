package protocol

import (
	"math"
	"time"
	"unicode/utf8"
)

// Observation requests (#5): the input and output of engine.info and
// host.metrics. docs/architecture/metrics.md describes units, sampling,
// buffering and how the manager ingests them.

// Sampling constants shared by the agent and the manager.
const (
	// MetricsInterval is the agent's sampling interval.
	MetricsInterval = 10 * time.Second
	// MetricsBufferBatches bounds the agent's sample ring (30 min at 10 s):
	// a manager that reconnects within that window backfills the samples,
	// longer outages are gaps.
	MetricsBufferBatches = 180
	// MaxContainerSamples bounds the containers of one batch; more set
	// Truncated.
	MaxContainerSamples = 1000
	// MaxDiskSamples bounds the filesystems of one batch.
	MaxDiskSamples = 16
	// MaxMetricsResponseBytes bounds the encoded batches of one host.metrics
	// response (the frame limit is MaxFrameSize); More asks the manager to
	// fetch again.
	MaxMetricsResponseBytes = 768 << 10
	// MaxMetricsBatchesPerResponse bounds the batches of one response.
	MaxMetricsBatchesPerResponse = 60
)

// HostMetricsInput asks for the buffered sample batches after a cursor.
type HostMetricsInput struct {
	// Epoch is the agent sampler epoch the cursor belongs to; a different
	// (or empty) epoch returns the whole buffer.
	Epoch string `json:"epoch,omitempty"`
	// AfterSeq returns batches with a higher Seq.
	AfterSeq uint64 `json:"afterSeq,omitempty"`
	// MaxBatches bounds the answer (0: MaxMetricsBatchesPerResponse).
	MaxBatches int `json:"maxBatches,omitempty"`
}

// HostMetricsOutput answers host.metrics.
type HostMetricsOutput struct {
	// Epoch identifies the sampler instance (one per agent process); Seq
	// restarts at 1 in every epoch.
	Epoch string `json:"epoch"`
	// Now is the agent's clock when answering: the manager estimates clock
	// skew from it.
	Now             time.Time `json:"now"`
	IntervalSeconds int       `json:"intervalSeconds"`
	// OldestSeq and LastSeq span the buffer (0 when empty).
	OldestSeq uint64 `json:"oldestSeq"`
	LastSeq   uint64 `json:"lastSeq"`
	// More: batches after the last returned one are still buffered.
	More    bool          `json:"more,omitempty"`
	Batches []MetricBatch `json:"batches"`
}

// Metric batch flags.
const (
	// BatchContainersTruncated: more containers ran than MaxContainerSamples,
	// or not every container could be sampled within the interval.
	BatchContainersTruncated = 1 << 0
	// BatchEngineUnavailable: the Engine did not answer; only host values
	// are present.
	BatchEngineUnavailable = 1 << 1
)

// MetricBatch is one sampling tick. Absent (null) values are unknown:
// the first sample after a start has no rates, a failed read has no value.
// Consumers render them as gaps, never as zeros.
type MetricBatch struct {
	Seq uint64 `json:"seq"`
	// At is the agent clock at the start of the tick.
	At         time.Time         `json:"at"`
	Flags      int               `json:"flags,omitempty"`
	Host       HostSample        `json:"host"`
	Disks      []DiskSample      `json:"disks,omitempty"`
	Containers []ContainerSample `json:"containers,omitempty"`
}

// HostSample holds host-wide values (docs/architecture/metrics.md, "Units").
type HostSample struct {
	// CPUPercent is busy time of all cores, 0..100 (% of total capacity).
	CPUPercent *float64 `json:"cpuPercent,omitempty"`
	CPUs       int      `json:"cpus,omitempty"`
	// MemoryUsedBytes is total minus available (page cache is not "used").
	MemoryUsedBytes      *int64   `json:"memoryUsedBytes,omitempty"`
	MemoryTotalBytes     *int64   `json:"memoryTotalBytes,omitempty"`
	MemoryAvailableBytes *int64   `json:"memoryAvailableBytes,omitempty"`
	Load1                *float64 `json:"load1,omitempty"`
	Load5                *float64 `json:"load5,omitempty"`
	Load15               *float64 `json:"load15,omitempty"`
	// Network rates in bytes per second over the observed interfaces.
	NetworkRxBytesPerSecond *float64 `json:"networkRxBytesPerSecond,omitempty"`
	NetworkTxBytesPerSecond *float64 `json:"networkTxBytesPerSecond,omitempty"`
	// NetworkScope is host (the host's network namespace, agent started with
	// pid: host or network_mode: host) or agent (only the agent container's
	// own namespace).
	NetworkScope  string `json:"networkScope,omitempty"`
	UptimeSeconds *int64 `json:"uptimeSeconds,omitempty"`
}

// Disk mount roles: the filesystem holding Docker's data root (volumes
// directory), the stacks volume, or a registered bind stack root.
const (
	DiskDocker = "docker"
	DiskStacks = "stacks"
	DiskBind   = "bind"
)

// DiskSample is one filesystem's usage. Mount is a role name (docker,
// stacks, bind-1, ...): host paths are never sent.
type DiskSample struct {
	Mount      string `json:"mount"`
	UsedBytes  int64  `json:"usedBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// ContainerSample is one running container's usage.
type ContainerSample struct {
	// Name is the container name (the authorization identity, #17); ID the
	// Engine container ID.
	Name string `json:"name"`
	ID   string `json:"id"`
	// CPUPercent is a share of the environment's total cores, 0..100.
	CPUPercent       *float64 `json:"cpuPercent,omitempty"`
	MemoryBytes      *int64   `json:"memoryBytes,omitempty"`
	MemoryLimitBytes *int64   `json:"memoryLimitBytes,omitempty"`
	// Rates in bytes per second since the previous sample.
	NetworkRxBytesPerSecond  *float64 `json:"networkRxBytesPerSecond,omitempty"`
	NetworkTxBytesPerSecond  *float64 `json:"networkTxBytesPerSecond,omitempty"`
	BlockReadBytesPerSecond  *float64 `json:"blockReadBytesPerSecond,omitempty"`
	BlockWriteBytesPerSecond *float64 `json:"blockWriteBytesPerSecond,omitempty"`
	PIDs                     *int64   `json:"pids,omitempty"`
}

// EngineInventory answers engine.info: the Engine's identity, capacity and
// Docker object counts, collected through the Moby adapter (#21). Host
// paths (Docker root dir) are not included.
type EngineInventory struct {
	EngineID             string `json:"engineId"`
	Hostname             string `json:"hostname"`
	Version              string `json:"version"`
	APIVersion           string `json:"apiVersion"`
	MinAPIVersion        string `json:"minApiVersion,omitempty"`
	NegotiatedAPIVersion string `json:"negotiatedApiVersion"`
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	OperatingSystem      string `json:"operatingSystem,omitempty"`
	KernelVersion        string `json:"kernelVersion,omitempty"`
	StorageDriver        string `json:"storageDriver,omitempty"`
	CgroupVersion        string `json:"cgroupVersion,omitempty"`
	CPUs                 int    `json:"cpus"`
	MemoryBytes          int64  `json:"memoryBytes"`
	Rootless             bool   `json:"rootless,omitempty"`
	DockerDesktop        bool   `json:"dockerDesktop,omitempty"`
	// Counts of Docker objects. A count the agent could not read is -1.
	Containers        int `json:"containers"`
	ContainersRunning int `json:"containersRunning"`
	ContainersPaused  int `json:"containersPaused"`
	ContainersStopped int `json:"containersStopped"`
	Images            int `json:"images"`
	Volumes           int `json:"volumes"`
	Networks          int `json:"networks"`
	// CollectedAt is the agent clock when the inventory was read.
	CollectedAt time.Time `json:"collectedAt"`
}

// Validate bounds an inventory reported by an agent.
func (inv EngineInventory) Validate() error {
	for _, s := range []string{inv.EngineID, inv.Hostname, inv.Version, inv.APIVersion, inv.MinAPIVersion, inv.NegotiatedAPIVersion,
		inv.OS, inv.Arch, inv.OperatingSystem, inv.KernelVersion, inv.StorageDriver, inv.CgroupVersion} {
		if len(s) > 255 || !utf8.ValidString(s) {
			return invalid("engine inventory field too long or not UTF-8")
		}
	}
	if inv.EngineID == "" || inv.CPUs < 0 || inv.MemoryBytes < 0 || inv.CollectedAt.IsZero() {
		return invalid("engine inventory needs engineId, collectedAt and non-negative capacity")
	}
	for _, n := range []int{inv.Containers, inv.ContainersRunning, inv.ContainersPaused, inv.ContainersStopped, inv.Images, inv.Volumes, inv.Networks} {
		if n < -1 {
			return invalid("engine inventory count out of range")
		}
	}
	return nil
}

func finite(p *float64, lo, hi float64) bool {
	return p == nil || (!math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= lo && *p <= hi)
}

func nonNegative(ps ...*int64) bool {
	for _, p := range ps {
		if p != nil && *p < 0 {
			return false
		}
	}
	return true
}

// maxRate bounds reported byte rates (1 TB/s) so a corrupt counter cannot
// poison aggregates.
const maxRate = 1e12

// Validate bounds a host.metrics answer (the manager calls it before
// ingesting anything).
func (o HostMetricsOutput) Validate() error {
	if o.Epoch == "" || len(o.Epoch) > 64 || o.Now.IsZero() || o.IntervalSeconds <= 0 || len(o.Batches) > MaxMetricsBatchesPerResponse {
		return invalid("host.metrics output needs an epoch, now, an interval and at most %d batches", MaxMetricsBatchesPerResponse)
	}
	var prev uint64
	for _, b := range o.Batches {
		if b.Seq == 0 || b.Seq <= prev || b.At.IsZero() {
			return invalid("host.metrics batches need increasing seq and a timestamp")
		}
		prev = b.Seq
		if err := b.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (b MetricBatch) validate() error {
	h := b.Host
	if !finite(h.CPUPercent, 0, 100) || !finite(h.Load1, 0, 1e6) || !finite(h.Load5, 0, 1e6) || !finite(h.Load15, 0, 1e6) ||
		!finite(h.NetworkRxBytesPerSecond, 0, maxRate) || !finite(h.NetworkTxBytesPerSecond, 0, maxRate) ||
		!nonNegative(h.MemoryUsedBytes, h.MemoryTotalBytes, h.MemoryAvailableBytes, h.UptimeSeconds) || h.CPUs < 0 ||
		(h.NetworkScope != "" && h.NetworkScope != "host" && h.NetworkScope != "agent") {
		return invalid("host sample out of range")
	}
	if len(b.Disks) > MaxDiskSamples || len(b.Containers) > MaxContainerSamples {
		return invalid("metric batch lists too many disks or containers")
	}
	for _, d := range b.Disks {
		if d.Mount == "" || len(d.Mount) > 32 || d.UsedBytes < 0 || d.TotalBytes < 0 {
			return invalid("disk sample out of range")
		}
	}
	for _, c := range b.Containers {
		if c.Name == "" || len(c.Name) > 255 || len(c.ID) > 128 || !utf8.ValidString(c.Name) ||
			!finite(c.CPUPercent, 0, 100) || !finite(c.NetworkRxBytesPerSecond, 0, maxRate) || !finite(c.NetworkTxBytesPerSecond, 0, maxRate) ||
			!finite(c.BlockReadBytesPerSecond, 0, maxRate) || !finite(c.BlockWriteBytesPerSecond, 0, maxRate) ||
			!nonNegative(c.MemoryBytes, c.MemoryLimitBytes, c.PIDs) {
			return invalid("container sample out of range")
		}
	}
	return nil
}
