package domain

import (
	"errors"
	"time"
)

// Metrics (#5): samples as ingested by the manager and query results.
// Units: CPU in percent of the environment's total cores (0..100), memory
// and disk in bytes, rates in bytes per second, load averages as reported
// by the kernel, temperatures in degrees Celsius. A nil value is unknown and is shown as a gap, never as 0.

// Metric series kinds.
const (
	MetricHost      = "host"
	MetricContainer = "container"
	MetricDisk      = "disk"
	// MetricSensor is a host temperature sensor (#146; the name is the
	// sensor's, e.g. "coretemp: Package id 0").
	MetricSensor = "sensor"
)

// Sample flags (stored per sample and ORed per query bucket).
const (
	// SampleSkewCorrected: the agent clock differed from the manager's by
	// more than the tolerance and the timestamp was shifted.
	SampleSkewCorrected = 1
	// SampleClamped: the timestamp lay in the future of the manager's
	// receive time and was clamped to it.
	SampleClamped = 2
	// SampleContainersIncomplete: not every running container was sampled.
	SampleContainersIncomplete = 4
	// SampleEngineUnavailable: the Engine did not answer; only host values.
	SampleEngineUnavailable = 8
)

// HostValues are host-wide values of one sample.
type HostValues struct {
	CPUPercent       *float64
	MemoryUsedBytes  *int64
	MemoryTotalBytes *int64
	// MemoryCacheBytes (buffers and page cache) and MemoryZFSARCBytes (the
	// ZFS ARC, nil without ZFS) are not part of MemoryUsedBytes.
	MemoryCacheBytes  *int64
	MemoryZFSARCBytes *int64
	SwapUsedBytes     *int64
	SwapTotalBytes    *int64
	Load1             *float64
	Load5             *float64
	Load15            *float64
	NetworkRxBPS      *float64
	NetworkTxBPS      *float64
	// DiskReadBPS and DiskWriteBPS are the throughput of the host's disks.
	DiskReadBPS  *float64
	DiskWriteBPS *float64
}

// ContainerValues are one container's values of one sample.
type ContainerValues struct {
	Name             string
	CPUPercent       *float64
	MemoryBytes      *int64
	MemoryLimitBytes *int64
	NetworkRxBPS     *float64
	NetworkTxBPS     *float64
	BlockReadBPS     *float64
	BlockWriteBPS    *float64
	PIDs             *int64
}

// DiskValues are one filesystem's usage (Mount is a role name such as
// docker or stacks, never a host path).
type DiskValues struct {
	Mount      string
	UsedBytes  int64
	TotalBytes int64
}

// TemperatureValues are one temperature sensor's reading (Sensor is the
// hwmon chip and label, never a host path).
type TemperatureValues struct {
	Sensor  string
	Celsius float64
}

// MetricSample is one sampling tick of an environment at a manager-side
// timestamp (corrected for clock skew and aligned to the 10 s slot).
type MetricSample struct {
	At           time.Time
	Flags        int
	Host         *HostValues
	Disks        []DiskValues
	Containers   []ContainerValues
	Temperatures []TemperatureValues
}

// MetricCursor is the collector's position in an agent's sample buffer.
type MetricCursor struct {
	// Epoch is the agent sampler epoch; LastSeq the last ingested batch.
	Epoch   string
	LastSeq uint64
	// Skew is the agent clock's offset applied to its timestamps (manager
	// minus agent); zero within the tolerance.
	Skew time.Duration
}

// MetricQuery selects series of one environment.
type MetricQuery struct {
	EnvironmentID string
	// Kind is host (host, disk and sensor series) or container (Name
	// selects it).
	Kind string
	Name string
	From time.Time
	To   time.Time
	// Step is the bucket width (0: automatic).
	Step time.Duration
	// Keys are the metric keys (MetricKeys); empty: all of the kind.
	Keys []string
}

// MetricSeries is one queried series: one value per bucket (nil: no data).
type MetricSeries struct {
	Key  string
	Unit string
	// Mount labels disk series.
	Mount string
	// Sensor labels temperature sensor series.
	Sensor string
	// Container names the container of a series of an environment-wide
	// container query (QueryContainers).
	Container string
	Values    []*float64
}

// MetricResult answers a MetricQuery.
type MetricResult struct {
	From time.Time
	To   time.Time
	Step time.Duration
	// Resolution is the storage level used: raw, 1m or 15m.
	Resolution string
	// Timestamps are the bucket starts.
	Timestamps []time.Time
	Series     []MetricSeries
	// Flags ORs the sample flags within the range.
	Flags int
}

// LatestContainerMetrics is a container's most recent sample (Values.Name
// is the container name; nil values are unknown, never zero).
type LatestContainerMetrics struct {
	At     time.Time
	Values ContainerValues
}

// LatestMetrics is an environment's most recent host sample and disks.
type LatestMetrics struct {
	At    time.Time
	Flags int
	Host  HostValues
	Disks []DiskValues
}

// Metric query errors.
var (
	// ErrMetricQuery is an invalid query (range, step, keys).
	ErrMetricQuery = errors.New("invalid metrics query")
	// ErrMetricsStorageFull: the storage cap is reached; new series are
	// refused until retention freed space.
	ErrMetricsStorageFull = errors.New("metrics storage cap reached")
)
