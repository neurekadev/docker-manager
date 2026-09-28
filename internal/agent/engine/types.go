package engine

import (
	"context"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
)

// Domain-neutral types of the adapter. They carry no SDK types, so code
// outside this package never imports github.com/moby/moby (#21).

// Identity describes the connected Engine.
type Identity struct {
	// EngineID is the Engine's stable ID (Info.ID).
	EngineID string
	// Name is the Engine host name.
	Name string
	// Version is the Engine version, e.g. "29.8.1".
	Version string
	// APIVersion is the highest API version the Engine serves;
	// MinAPIVersion the lowest it accepts.
	APIVersion    string
	MinAPIVersion string
	// NegotiatedAPIVersion is the version this client talks (min of the
	// client's maximum and APIVersion).
	NegotiatedAPIVersion string
	// OS is the Engine OS type ("linux"); Arch the Go architecture name
	// ("amd64", "arm64"); OperatingSystem a description ("Alpine Linux v3.22").
	OS              string
	Arch            string
	OperatingSystem string
	KernelVersion   string
	// DockerRootDir is the Engine's data root (#28).
	DockerRootDir string
	StorageDriver string
	CgroupVersion string
	NCPU          int
	MemTotal      int64
	// Rootless is true for a rootless Engine; SecurityOptions lists the
	// option names ("apparmor", "seccomp", "selinux", "rootless", "userns", ...).
	Rootless        bool
	SecurityOptions []string
	// DockerDesktop is true when the Engine runs inside Docker Desktop's VM.
	DockerDesktop bool
	// Capabilities lists the v1 operations and whether this Engine supports them.
	Capabilities []Capability
}

// Capability reports support for one planned v1 operation.
type Capability struct {
	// Name is a stable operation key, e.g. "image.build".
	Name      string
	Supported bool
	// Reason explains why the operation is unsupported.
	Reason string `json:",omitempty"`
}

// Supports reports whether the named capability is supported.
func (id Identity) Supports(name string) bool {
	for _, c := range id.Capabilities {
		if c.Name == name {
			return c.Supported
		}
	}
	return false
}

// RegistryAuth is one registry credential, used for a single operation and
// never persisted (#19). Password and IdentityToken redact in logs.
type RegistryAuth struct {
	// ServerAddress is the registry host (e.g. "ghcr.io", "registry:5000",
	// "https://index.docker.io/v1/" for Docker Hub).
	ServerAddress string
	Username      string
	Password      logging.Secret
	// IdentityToken is an OAuth refresh token (instead of Password).
	IdentityToken logging.Secret
}

// ContainerFilter selects containers for ListContainers.
type ContainerFilter struct {
	// All includes stopped containers.
	All bool
	// Labels are "key" or "key=value" label filters (all must match).
	Labels []string
	// Names match container names (substring, Engine semantics).
	Names []string
	// IDs match container ID prefixes.
	IDs []string
	// Size computes each container's writable layer size (SizeRw; slower).
	Size bool
}

// Port is a container port and its host bindings.
type Port struct {
	PrivatePort uint16
	PublicPort  uint16
	HostIP      string
	Protocol    string
}

// Mount is a mount of a container.
type Mount struct {
	// Type is "bind", "volume", "tmpfs", "npipe", "cluster" or "image".
	Type string
	// Name is the volume name (volume mounts).
	Name string
	// Source is the host path (bind) or the volume's mountpoint on the host.
	Source      string
	Destination string
	Driver      string
	ReadWrite   bool
	Propagation string
}

// Health is a container health state.
type Health struct {
	// Status is "starting", "healthy", "unhealthy" or "none".
	Status        string
	FailingStreak int
}

// Container is a container list entry.
type Container struct {
	ID      string
	Names   []string
	Image   string
	ImageID string
	Command string
	Created time.Time
	// State is "created", "running", "paused", "restarting", "removing",
	// "exited" or "dead"; Status the Engine's human description.
	State  string
	Status string
	Health string
	Labels map[string]string
	Ports  []Port
	Mounts []Mount
	// SizeRw is the writable layer size (ContainerFilter.Size; else 0).
	SizeRw int64
	// Networks are the names of the networks the container is configured
	// for (also while stopped), sorted.
	Networks []string
	// Endpoints are the container's endpoints by network name (addresses
	// are empty while it is stopped).
	Endpoints map[string]EndpointInfo
}

// ContainerState is the runtime state of a container.
type ContainerState struct {
	Status     string
	Running    bool
	Paused     bool
	Restarting bool
	OOMKilled  bool
	Dead       bool
	Pid        int
	ExitCode   int
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	Health     *Health
}

// ContainerDetails is an inspected container. Environment variables are
// deliberately not exposed here: they often hold secrets (#7).
type ContainerDetails struct {
	ID           string
	Name         string
	Image        string
	ImageID      string
	Created      time.Time
	Platform     string
	RestartCount int
	State        ContainerState
	Labels       map[string]string
	Cmd          []string
	Entrypoint   []string
	WorkingDir   string
	User         string
	Tty          bool
	Hostname     string
	// RestartPolicy is "no", "always", "on-failure" or "unless-stopped".
	RestartPolicy string
	// RestartMaxRetries is the on-failure policy's maximum retry count
	// (0: unlimited; always 0 for other policies).
	RestartMaxRetries int
	NetworkMode       string
	Mounts            []Mount
	// Networks maps network name to the container's addresses on it.
	Networks map[string]EndpointInfo
	Ports    []Port
	// Resources are the container's limits; Healthcheck its configured
	// health check (nil: the image's or none).
	Resources   Resources
	Healthcheck *HealthcheckSpec
}

// CreatedConfig is what a container was created with beyond
// ContainerDetails, for comparing it with its Compose definition inside the
// agent (#7 import by copy). Env holds secret values: compare them in
// memory only; never log, journal, return or send them.
type CreatedConfig struct {
	Env []string
	// ImageEnv is the environment of the container's image (the Engine
	// merges it into Env).
	ImageEnv []string
	// Ports are the configured port bindings (also while stopped).
	Ports []Port
}

// ConfigInspector is implemented by Engines that report CreatedConfig
// (the Moby adapter and the in-memory fake).
type ConfigInspector interface {
	CreatedConfig(ctx context.Context, id string) (CreatedConfig, error)
}

// CloneOptions configures Cloner.CloneContainer.
type CloneOptions struct {
	// Name is the clone's name (rename the original aside first when it
	// keeps its name).
	Name string
	// Volumes maps volume names the original mounts to the names the clone
	// mounts instead (a volume that moved to a new name).
	Volumes map[string]string
}

// Cloner is implemented by Engines that recreate a container from its
// complete configuration (the Moby adapter and the in-memory fake): a
// container Docker Manager did not create keeps every setting, not only
// the ones ContainerSpec knows.
type Cloner interface {
	// CloneContainer creates (never starts) a container with the whole
	// configuration of id: Config, HostConfig and its networks, the image
	// it runs, with the volumes of o.Volumes renamed and its anonymous
	// volumes mounted by name (their data is kept). A hostname Docker
	// derived from the old ID is left to Docker.
	CloneContainer(ctx context.Context, id string, o CloneOptions) (string, error)
}

// EndpointInfo is a container's attachment to a network.
type EndpointInfo struct {
	NetworkID   string
	IPAddress   string
	IPv6Address string
	MacAddress  string
	Aliases     []string
}

// MountSpec requests a mount for ContainerSpec.
type MountSpec struct {
	// Type is "bind", "volume" or "tmpfs".
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

// PortBinding publishes a container port.
type PortBinding struct {
	ContainerPort uint16
	Protocol      string // "tcp" (default) or "udp"
	HostIP        string
	HostPort      uint16 // 0 = ephemeral
}

// HealthcheckSpec configures a container health check.
type HealthcheckSpec struct {
	// Test is e.g. ["CMD", "/bin/check"] or ["NONE"].
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

// Resources are updatable container limits.
type Resources struct {
	// NanoCPUs is CPU quota in units of 1e-9 CPUs (0 = unchanged/unlimited).
	NanoCPUs int64
	// CPUShares is the relative CPU weight (0 = unchanged).
	CPUShares int64
	// Memory limit in bytes (0 = unchanged); MemorySwap total (-1 = unlimited).
	Memory     int64
	MemorySwap int64
	PidsLimit  *int64
}

// ContainerSpec describes a container to create.
type ContainerSpec struct {
	Name       string
	Image      string
	Cmd        []string
	Entrypoint []string
	// Env holds KEY=value pairs; treat as sensitive.
	Env         []string
	Labels      map[string]string
	WorkingDir  string
	User        string
	Hostname    string
	Tty         bool
	OpenStdin   bool
	StopSignal  string
	StopTimeout *time.Duration
	Mounts      []MountSpec
	// NetworkMode is "bridge" (default), "host", "none", "container:<id>"
	// or a network name.
	NetworkMode string
	// NetworkAliases apply on NetworkMode when it names a user network.
	NetworkAliases []string
	Ports          []PortBinding
	// RestartPolicy is "no" (default), "always", "on-failure" or "unless-stopped".
	RestartPolicy string
	Healthcheck   *HealthcheckSpec
	AutoRemove    bool
	Resources     Resources
	// Platform is "os/arch[/variant]"; empty means the Engine's platform.
	Platform string
}

// Image is an image list entry.
type Image struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	Created     time.Time
	Size        int64
	Containers  int64
	Labels      map[string]string
}

// ImageDetails is an inspected image.
type ImageDetails struct {
	ID            string
	RepoTags      []string
	RepoDigests   []string
	Created       time.Time
	Size          int64
	OS            string
	Architecture  string
	Variant       string
	Author        string
	Labels        map[string]string
	Entrypoint    []string
	Cmd           []string
	WorkingDir    string
	User          string
	ExposedPorts  []string
	Volumes       []string
	HasHealthTest bool
}

// DeletedImage reports an image removal.
type DeletedImage struct {
	Untagged string
	Deleted  string
}

// PullOptions configures PullImage.
type PullOptions struct {
	// Auth is the credential for the image's registry (nil = anonymous).
	Auth *RegistryAuth
	// Platform is "os/arch[/variant]"; empty means the Engine's platform.
	Platform string
	// Progress receives decoded progress messages (may be nil).
	Progress func(Progress)
}

// Progress is one progress message of a pull or build.
type Progress struct {
	// ID is the layer or step the message is about.
	ID     string
	Status string
	// Current and Total are byte counts when known.
	Current int64
	Total   int64
}

// PullResult reports a completed pull.
type PullResult struct {
	// Digest is the manifest digest reported by the Engine, if any.
	Digest string
	// ImageID is the local image ID after the pull.
	ImageID string
}

// Volume is a Docker volume.
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	Scope      string
	CreatedAt  time.Time
	Labels     map[string]string
	Options    map[string]string
}

// VolumeSpec describes a volume to create.
type VolumeSpec struct {
	Name       string
	Driver     string
	DriverOpts map[string]string
	Labels     map[string]string
}

// Network is a Docker network.
type Network struct {
	ID         string
	Name       string
	Driver     string
	Scope      string
	Internal   bool
	Attachable bool
	EnableIPv6 bool
	Created    time.Time
	Labels     map[string]string
	Subnets    []string
	Gateways   []string
	// Containers maps container ID to its endpoint (inspect only).
	Containers map[string]EndpointInfo
}

// NetworkSpec describes a network to create.
type NetworkSpec struct {
	Name       string
	Driver     string
	Internal   bool
	Attachable bool
	Labels     map[string]string
	Options    map[string]string
}

// EventFilter selects Engine events.
type EventFilter struct {
	// Types are "container", "image", "volume", "network", "daemon", ...
	Types []string
	// Labels are "key" or "key=value" filters.
	Labels []string
	// Since limits the stream to events after this time (zero = now).
	Since time.Time
}

// Event is an Engine event.
type Event struct {
	Type   string
	Action string
	// ActorID is the object ID; Attributes include "name", "image" and labels.
	ActorID    string
	Attributes map[string]string
	Scope      string
	Time       time.Time
}

// LogOptions configures StreamLogs.
type LogOptions struct {
	Follow     bool
	Stdout     bool
	Stderr     bool
	Timestamps bool
	// Tail is the number of lines from the end ("all" when zero and TailAll).
	Tail    int
	TailAll bool
	Since   time.Time
	Until   time.Time
}

// LogStream identifies the source of a log line.
type LogStream string

// Log streams.
const (
	Stdout LogStream = "stdout"
	Stderr LogStream = "stderr"
)

// LogEntry is one chunk of container output (a line when the container
// writes lines). With LogOptions.Timestamps, Time is the Engine timestamp.
type LogEntry struct {
	Stream LogStream
	Time   time.Time
	Data   []byte
}

// Stats is one resource usage sample of a container.
type Stats struct {
	Read time.Time
	// CPUPercent is the share of one CPU times the number of online CPUs
	// (100% = one full CPU), 0 for a single sample and for the first sample
	// of a stream (it needs a previous sample).
	CPUPercent float64
	OnlineCPUs uint32
	// CPUTotalUsage (the container's CPU time) and SystemCPUUsage (the
	// host's CPU time over all cores) are cumulative nanosecond counters:
	// a caller keeping the previous sample computes CPU use from their
	// deltas (SystemCPUUsage is 0 where the Engine does not report it).
	CPUTotalUsage  uint64
	SystemCPUUsage uint64
	// MemoryUsage excludes the page cache (as `docker stats`); MemoryLimit
	// is the cgroup limit or host memory.
	MemoryUsage   uint64
	MemoryLimit   uint64
	NetworkRx     uint64
	NetworkTx     uint64
	BlockRead     uint64
	BlockWrite    uint64
	PIDs          uint64
	MemoryPercent float64
}

// ExecSpec describes a command to run in a container.
type ExecSpec struct {
	Cmd        []string
	Env        []string
	WorkingDir string
	User       string
	Tty        bool
	// AttachStdin enables Stdin in ExecIO.
	AttachStdin bool
	// Height and Width are the initial terminal size (Tty only).
	Height, Width uint
}

// ExecStatus reports an exec instance's state.
type ExecStatus struct {
	Running  bool
	ExitCode int
	Pid      int
}
