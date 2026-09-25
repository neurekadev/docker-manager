package protocol

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Docker resource requests and job inputs (#6). The manager sends only these
// named, typed operations; there is no Engine API passthrough. Requests
// (container.list, container.inspect, image.list, image.inspect, image.tag,
// volume.list, volume.inspect, network.list, network.inspect) answer with
// the outputs below; the container.*, image.pull/remove, volume.* and
// network.* job kinds take the inputs below. Both sides validate inputs with
// the same functions.

// Labels DockYard sets or reads on Docker objects.
const (
	// LabelPrefix is reserved: users cannot set labels under it (#6, #32).
	LabelPrefix = "dev.neureka.dockyard."
	// LabelRole marks DockYard's own containers in the deploy examples:
	// "manager" or "agent" (#32).
	LabelRole = LabelPrefix + "role"
	// LabelManaged marks a container DockYard created: "standalone" (#6).
	LabelManaged = LabelPrefix + "managed"
	// LabelInstance is the manager instance ID that created the object.
	LabelInstance = LabelPrefix + "instance"
	// LabelSpec is the ID of the saved recreate specification of a
	// DockYard-managed standalone container (manager side, used by #20).
	LabelSpec = LabelPrefix + "spec"

	// ManagedStandalone is the LabelManaged value of standalone containers.
	ManagedStandalone = "standalone"

	// Compose labels (set by the Compose SDK and CLI).
	ComposeLabelPrefix     = "com.docker.compose."
	ComposeProjectLabel    = "com.docker.compose.project"
	ComposeServiceLabel    = "com.docker.compose.service"
	ComposeWorkingDirLabel = "com.docker.compose.project.working_dir"
	ComposeOneoffLabel     = "com.docker.compose.oneoff"
)

// ContainerPort is a container port and its host binding.
type ContainerPort struct {
	ContainerPort uint16 `json:"containerPort"`
	HostPort      uint16 `json:"hostPort,omitempty"`
	HostIP        string `json:"hostIp,omitempty"`
	Protocol      string `json:"protocol"`
}

// ContainerMount is a mount of a container.
type ContainerMount struct {
	// Type is bind, volume, tmpfs, ...
	Type string `json:"type"`
	// Name is the volume name (volume mounts).
	Name string `json:"name,omitempty"`
	// Source is the host path of a bind mount (empty for volumes: their
	// host mountpoint is not exposed).
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
}

// StackRef is the Compose project a Docker object belongs to.
type StackRef struct {
	Project string `json:"project"`
	Service string `json:"service,omitempty"`
	// Managed: the project's working directory lies in one of the agent's
	// verified stack roots (#28), so DockYard manages the stack (#7) and
	// direct edits of its containers conflict with it.
	Managed bool `json:"managed,omitempty"`
}

// Protection says why DockYard refuses destructive operations on one of its
// own resources (#32).
type Protection struct {
	// Role is manager, agent, manager_image, agent_image, manager_data,
	// stacks, backup_repository or dockyard_project.
	Role string `json:"role"`
	// Reason is shown to users.
	Reason string `json:"reason"`
	// Self: the resource belongs to this DockYard installation (the
	// connected agent, or the manager whose instance ID matched).
	Self bool `json:"self,omitempty"`
	// RestartAllowed: a restart is allowed after an explicit confirmation
	// (the co-located manager).
	RestartAllowed bool `json:"restartAllowed,omitempty"`
}

// ContainerRef names a container using another object.
type ContainerRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// ContainerSummary is a container list entry.
type ContainerSummary struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	ImageID string            `json:"imageId,omitempty"`
	Command string            `json:"command,omitempty"`
	Created time.Time         `json:"created"`
	State   string            `json:"state"`
	Status  string            `json:"status,omitempty"`
	Health  string            `json:"health,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Ports   []ContainerPort   `json:"ports,omitempty"`
	Mounts  []ContainerMount  `json:"mounts,omitempty"`
	// Networks are the names of the networks the container is attached to.
	Networks   []string    `json:"networks,omitempty"`
	Stack      *StackRef   `json:"stack,omitempty"`
	Protection *Protection `json:"protection,omitempty"`
}

// ContainerNetwork is a container's attachment to a network.
type ContainerNetwork struct {
	Name        string   `json:"name"`
	NetworkID   string   `json:"networkId,omitempty"`
	IPAddress   string   `json:"ipAddress,omitempty"`
	IPv6Address string   `json:"ipv6Address,omitempty"`
	MacAddress  string   `json:"macAddress,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// ContainerDetails is an inspected container. Environment variables are
// never included (they often hold secrets, #7).
type ContainerDetails struct {
	ContainerSummary
	Cmd           []string           `json:"cmd,omitempty"`
	Entrypoint    []string           `json:"entrypoint,omitempty"`
	WorkingDir    string             `json:"workingDir,omitempty"`
	User          string             `json:"user,omitempty"`
	Tty           bool               `json:"tty,omitempty"`
	Hostname      string             `json:"hostname,omitempty"`
	RestartPolicy string             `json:"restartPolicy,omitempty"`
	NetworkMode   string             `json:"networkMode,omitempty"`
	NetworkList   []ContainerNetwork `json:"networkList,omitempty"`
	RestartCount  int                `json:"restartCount"`
	Platform      string             `json:"platform,omitempty"`
	Running       bool               `json:"running"`
	Paused        bool               `json:"paused"`
	OOMKilled     bool               `json:"oomKilled,omitempty"`
	ExitCode      int                `json:"exitCode"`
	Error         string             `json:"error,omitempty"`
	StartedAt     *time.Time         `json:"startedAt,omitempty"`
	FinishedAt    *time.Time         `json:"finishedAt,omitempty"`
	Resources     ResourcesSpec      `json:"resources"`
	Healthcheck   *HealthcheckSpec   `json:"healthcheck,omitempty"`
}

// ImageSummary is an image list entry.
type ImageSummary struct {
	ID          string            `json:"id"`
	RepoTags    []string          `json:"repoTags,omitempty"`
	RepoDigests []string          `json:"repoDigests,omitempty"`
	Created     time.Time         `json:"created"`
	Size        int64             `json:"size"`
	Labels      map[string]string `json:"labels,omitempty"`
	// UsedBy lists the containers (running or not) created from the image.
	UsedBy     []ContainerRef `json:"usedBy,omitempty"`
	Protection *Protection    `json:"protection,omitempty"`
}

// ImageDetails is an inspected image.
type ImageDetails struct {
	ImageSummary
	OS            string   `json:"os,omitempty"`
	Architecture  string   `json:"architecture,omitempty"`
	Variant       string   `json:"variant,omitempty"`
	Author        string   `json:"author,omitempty"`
	Entrypoint    []string `json:"entrypoint,omitempty"`
	Cmd           []string `json:"cmd,omitempty"`
	WorkingDir    string   `json:"workingDir,omitempty"`
	User          string   `json:"user,omitempty"`
	ExposedPorts  []string `json:"exposedPorts,omitempty"`
	Volumes       []string `json:"volumes,omitempty"`
	HasHealthTest bool     `json:"hasHealthTest,omitempty"`
}

// VolumeInfo is a Docker volume with its users. The host mountpoint is
// not included.
type VolumeInfo struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope,omitempty"`
	Created    time.Time         `json:"created,omitzero"`
	Labels     map[string]string `json:"labels,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
	UsedBy     []ContainerRef    `json:"usedBy,omitempty"`
	Stack      *StackRef         `json:"stack,omitempty"`
	Protection *Protection       `json:"protection,omitempty"`
}

// NetworkInfo is a Docker network with its attached containers.
type NetworkInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope,omitempty"`
	Internal   bool              `json:"internal,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
	EnableIPv6 bool              `json:"enableIpv6,omitempty"`
	Created    time.Time         `json:"created,omitzero"`
	Labels     map[string]string `json:"labels,omitempty"`
	Subnets    []string          `json:"subnets,omitempty"`
	Gateways   []string          `json:"gateways,omitempty"`
	// Builtin: a predefined network (bridge, host, none) that cannot be
	// removed.
	Builtin    bool           `json:"builtin,omitempty"`
	Containers []ContainerRef `json:"containers,omitempty"`
	Stack      *StackRef      `json:"stack,omitempty"`
	Protection *Protection    `json:"protection,omitempty"`
}

// Request inputs and outputs.
type (
	// ContainerListInput lists every container (running or not).
	ContainerListInput struct{}
	// ContainerListOutput answers container.list.
	ContainerListOutput struct {
		Containers []ContainerSummary `json:"containers"`
	}
	// ContainerInspectInput names a container by ID, unique ID prefix or name.
	ContainerInspectInput struct {
		Container string `json:"container"`
	}
	// ImageListInput lists every image.
	ImageListInput struct{}
	// ImageListOutput answers image.list.
	ImageListOutput struct {
		Images []ImageSummary `json:"images"`
	}
	// ImageInspectInput names an image by ID or reference.
	ImageInspectInput struct {
		Image string `json:"image"`
	}
	// ImageTagInput adds Target (repository[:tag]) to Image.
	ImageTagInput struct {
		Image  string `json:"image"`
		Target string `json:"target"`
	}
	// VolumeListInput lists every volume.
	VolumeListInput struct{}
	// VolumeListOutput answers volume.list.
	VolumeListOutput struct {
		Volumes []VolumeInfo `json:"volumes"`
	}
	// VolumeInspectInput names a volume.
	VolumeInspectInput struct {
		Name string `json:"name"`
	}
	// NetworkListInput lists every network.
	NetworkListInput struct{}
	// NetworkListOutput answers network.list.
	NetworkListOutput struct {
		Networks []NetworkInfo `json:"networks"`
	}
	// NetworkInspectInput names a network by ID or name.
	NetworkInspectInput struct {
		Network string `json:"network"`
	}
)

// PortSpec publishes a container port.
type PortSpec struct {
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      uint16 `json:"hostPort,omitempty"`
}

// MountSpec requests a mount: bind (Source is an absolute host path),
// volume (Source is a volume name; empty = anonymous) or tmpfs.
type MountSpec struct {
	Type     string `json:"type"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// NetworkAttachment attaches a container to a network.
type NetworkAttachment struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

// ResourcesSpec are container limits (zero = unlimited/Engine default).
type ResourcesSpec struct {
	NanoCPUs   int64  `json:"nanoCpus,omitempty"`
	CPUShares  int64  `json:"cpuShares,omitempty"`
	Memory     int64  `json:"memoryBytes,omitempty"`
	MemorySwap int64  `json:"memorySwapBytes,omitempty"`
	PidsLimit  *int64 `json:"pidsLimit,omitempty"`
}

// HealthcheckSpec configures a container health check.
type HealthcheckSpec struct {
	Test        []string      `json:"test"`
	Interval    time.Duration `json:"interval,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	StartPeriod time.Duration `json:"startPeriod,omitempty"`
	Retries     int           `json:"retries,omitempty"`
}

// ContainerSpec is the v1 create-container form (#6): the common options
// only. Anything more complex belongs in a Compose stack.
type ContainerSpec struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Env        []string          `json:"env,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
	User       string            `json:"user,omitempty"`
	Ports      []PortSpec        `json:"ports,omitempty"`
	Mounts     []MountSpec       `json:"mounts,omitempty"`
	// Networks: the first is the container's network mode (a user network
	// or bridge/host/none), the others are connected after creation.
	Networks      []NetworkAttachment `json:"networks,omitempty"`
	RestartPolicy string              `json:"restartPolicy,omitempty"`
	Resources     ResourcesSpec       `json:"resources"`
	Healthcheck   *HealthcheckSpec    `json:"healthcheck,omitempty"`
}

// Job inputs.
type (
	// ContainerCreateInput is the input of container.create.
	ContainerCreateInput struct {
		Spec ContainerSpec `json:"spec"`
		// Start the container after creating it.
		Start bool `json:"start,omitempty"`
		// Ownership are the DockYard labels the manager sets on the
		// container (LabelManaged, LabelInstance, LabelSpec only).
		Ownership map[string]string `json:"ownership,omitempty"`
	}
	// ContainerActionInput is the input of container.start, .stop,
	// .restart, .pause, .unpause and .remove. ID is the container ID the
	// manager resolved when the job was requested: a container recreated
	// under the same name since then is not touched.
	ContainerActionInput struct {
		Name string `json:"name"`
		ID   string `json:"id"`
		// Timeout (stop, restart) in seconds; nil uses the container's.
		TimeoutSeconds *int `json:"timeoutSeconds,omitempty"`
		// Force (remove) kills a running container first.
		Force bool `json:"force,omitempty"`
		// RemoveVolumes (remove) also removes its anonymous volumes.
		RemoveVolumes bool `json:"removeVolumes,omitempty"`
		// Confirmed (restart of the co-located manager, #32).
		Confirmed bool `json:"confirmed,omitempty"`
	}
	// ContainerUpdateInput is the input of container.update: in-place
	// settings only.
	ContainerUpdateInput struct {
		Name          string         `json:"name"`
		ID            string         `json:"id"`
		RestartPolicy string         `json:"restartPolicy,omitempty"`
		Resources     *ResourcesSpec `json:"resources,omitempty"`
	}
	// ImagePullInput is the input of image.pull. RegistryConnections is
	// jobspec.CredentialRefs (#19): the connection selected for the
	// reference's registry, resolved to a credential at every dispatch
	// (protocol.CommandSecrets); never a credential itself.
	ImagePullInput struct {
		Reference           string   `json:"reference"`
		Platform            string   `json:"platform,omitempty"`
		RegistryConnections []string `json:"registryConnections,omitempty"`
	}
	// ImageRemoveInput is the input of image.remove (Image is the ID).
	ImageRemoveInput struct {
		Image string `json:"image"`
		// Force removes an image with several tags (never one in use).
		Force bool `json:"force,omitempty"`
	}
	// VolumeCreateInput is the input of volume.create.
	VolumeCreateInput struct {
		Name       string            `json:"name"`
		Driver     string            `json:"driver,omitempty"`
		DriverOpts map[string]string `json:"driverOpts,omitempty"`
		Labels     map[string]string `json:"labels,omitempty"`
	}
	// VolumeRemoveInput is the input of volume.remove.
	VolumeRemoveInput struct {
		Name string `json:"name"`
	}
	// NetworkCreateInput is the input of network.create.
	NetworkCreateInput struct {
		Name       string            `json:"name"`
		Driver     string            `json:"driver,omitempty"`
		Internal   bool              `json:"internal,omitempty"`
		Attachable bool              `json:"attachable,omitempty"`
		Labels     map[string]string `json:"labels,omitempty"`
		Options    map[string]string `json:"options,omitempty"`
	}
	// NetworkRemoveInput is the input of network.remove (ID resolved when
	// the job was requested).
	NetworkRemoveInput struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
)

// Validation of Docker names and specs, shared by the manager (422 before a
// job exists) and the agent (defense in depth).

var (
	// Container, volume and network names (Docker's own rule).
	dockerNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// Image references: [registry[:port]/]path[:tag][@digest], no spaces.
	imageRefRE = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-]+[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)
	imageIDRE  = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	envKeyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)
	driverRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:/-]{0,127}$`)
)

// DockerSocketPaths are the usual host paths of the Docker socket. A
// created container may not bind them or a directory containing them: the
// socket would give it the unrestricted Engine API (#6).
var DockerSocketPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

// Restart policies of the v1 create form.
var restartPolicies = []string{"", "no", "always", "on-failure", "unless-stopped"}

// Limits of the v1 create form.
const (
	maxListItems = 64
	maxArgLen    = 4096
	maxLabels    = 64
)

// FieldError is a validation failure of one input field (JSON path
// relative to the spec, e.g. "ports[0].hostPort").
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// ValidDockerName reports whether name is a valid container, volume or
// network name.
func ValidDockerName(name string) bool { return dockerNameRE.MatchString(name) }

// ValidImageReference reports whether ref is a plausible image reference
// (lowercase repository, optional registry host, tag and digest).
func ValidImageReference(ref string) bool {
	return len(ref) <= 512 && imageRefRE.MatchString(ref)
}

// ValidImageID reports whether id is a full or short (12+ hex) image ID.
func ValidImageID(id string) bool { return imageIDRE.MatchString(id) }

// ValidatePlatform checks an os/arch[/variant] platform ("" = the Engine's).
func ValidatePlatform(p string) bool { return p == "" || platformRE.MatchString(p) }

// ValidateLabels checks user labels: bounded, and never under DockYard's or
// Compose's reserved prefixes (ownership and stack membership cannot be
// forged, #6, #32).
func ValidateLabels(field string, labels map[string]string) error {
	if len(labels) > maxLabels {
		return fieldErr(field, "at most %d labels", maxLabels)
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch {
		case k == "" || len(k) > 256 || strings.ContainsAny(k, " \t\r\n="):
			return fieldErr(field, "invalid label key %q", k)
		case strings.HasPrefix(k, LabelPrefix):
			return fieldErr(field, "label %q uses the reserved prefix %s", k, LabelPrefix)
		case strings.HasPrefix(k, ComposeLabelPrefix):
			return fieldErr(field, "label %q uses the reserved prefix %s (Compose stack membership)", k, ComposeLabelPrefix)
		case len(labels[k]) > maxArgLen:
			return fieldErr(field, "label %q value is too long", k)
		}
	}
	return nil
}

func validArgs(field string, args []string) error {
	if len(args) > maxListItems {
		return fieldErr(field, "at most %d items", maxListItems)
	}
	for i, a := range args {
		if len(a) > maxArgLen || strings.ContainsRune(a, 0) {
			return fieldErr(fmt.Sprintf("%s[%d]", field, i), "too long or contains a NUL byte")
		}
	}
	return nil
}

// Validate checks container limits (field is the JSON path prefix).
func (r ResourcesSpec) Validate(field string) error {
	switch {
	case r.NanoCPUs < 0:
		return fieldErr(field+".nanoCpus", "must not be negative")
	case r.CPUShares < 0 || r.CPUShares > 262144:
		return fieldErr(field+".cpuShares", "must be between 0 and 262144")
	case r.Memory != 0 && r.Memory < 6<<20:
		return fieldErr(field+".memoryBytes", "must be 0 (unlimited) or at least 6 MiB")
	case r.MemorySwap < -1:
		return fieldErr(field+".memorySwapBytes", "must be -1 (unlimited), 0 or a byte count")
	case r.MemorySwap > 0 && r.Memory > 0 && r.MemorySwap < r.Memory:
		return fieldErr(field+".memorySwapBytes", "must be at least memoryBytes (it includes memory)")
	case r.PidsLimit != nil && *r.PidsLimit < -1:
		return fieldErr(field+".pidsLimit", "must be -1 (unlimited), 0 or a positive count")
	}
	return nil
}

// ValidRestartPolicy reports whether p is a v1 restart policy.
func ValidRestartPolicy(p string) bool { return slices.Contains(restartPolicies, p) }

// Validate checks the create-container form.
func (s ContainerSpec) Validate() error {
	if !ValidDockerName(s.Name) {
		return fieldErr("name", "must start with a letter or digit and contain only letters, digits, _ . - (at most 128)")
	}
	if !ValidImageReference(s.Image) && !ValidImageID(s.Image) {
		return fieldErr("image", "must be an image reference such as nginx:1.27 or ghcr.io/org/app:tag, or an image ID")
	}
	if err := validArgs("command", s.Command); err != nil {
		return err
	}
	if err := validArgs("entrypoint", s.Entrypoint); err != nil {
		return err
	}
	if len(s.Env) > 256 {
		return fieldErr("env", "at most 256 variables")
	}
	for i, e := range s.Env {
		k, _, ok := strings.Cut(e, "=")
		if !ok || !envKeyRE.MatchString(k) || len(e) > 32<<10 || strings.ContainsRune(e, 0) {
			// Never echo the value: it may be a secret.
			return fieldErr(fmt.Sprintf("env[%d]", i), "must be KEY=value with a valid variable name")
		}
	}
	if err := ValidateLabels("labels", s.Labels); err != nil {
		return err
	}
	if len(s.WorkingDir) > 0 && !path.IsAbs(s.WorkingDir) {
		return fieldErr("workingDir", "must be an absolute path")
	}
	if len(s.User) > 256 {
		return fieldErr("user", "too long")
	}
	if len(s.Ports) > maxListItems {
		return fieldErr("ports", "at most %d ports", maxListItems)
	}
	for i, p := range s.Ports {
		f := fmt.Sprintf("ports[%d]", i)
		switch {
		case p.ContainerPort == 0:
			return fieldErr(f+".containerPort", "must be 1..65535")
		case p.Protocol != "" && p.Protocol != "tcp" && p.Protocol != "udp":
			return fieldErr(f+".protocol", "must be tcp or udp")
		case p.HostIP != "" && !validIP(p.HostIP):
			return fieldErr(f+".hostIp", "must be an IP address")
		}
	}
	if len(s.Mounts) > maxListItems {
		return fieldErr("mounts", "at most %d mounts", maxListItems)
	}
	for i, m := range s.Mounts {
		if err := m.validate(fmt.Sprintf("mounts[%d]", i)); err != nil {
			return err
		}
	}
	if len(s.Networks) > 16 {
		return fieldErr("networks", "at most 16 networks")
	}
	seen := map[string]bool{}
	for i, n := range s.Networks {
		f := fmt.Sprintf("networks[%d]", i)
		if !ValidDockerName(n.Name) {
			return fieldErr(f+".name", "must be a network name")
		}
		if seen[n.Name] {
			return fieldErr(f+".name", "network %q is listed twice", n.Name)
		}
		seen[n.Name] = true
		if (n.Name == "host" || n.Name == "none") && len(s.Networks) > 1 {
			return fieldErr(f+".name", "%s cannot be combined with other networks", n.Name)
		}
		if len(n.Aliases) > 16 {
			return fieldErr(f+".aliases", "at most 16 aliases")
		}
		for _, a := range n.Aliases {
			if !ValidDockerName(a) {
				return fieldErr(f+".aliases", "invalid alias %q", a)
			}
		}
	}
	if !ValidRestartPolicy(s.RestartPolicy) {
		return fieldErr("restartPolicy", "must be no, always, on-failure or unless-stopped")
	}
	if err := s.Resources.Validate("resources"); err != nil {
		return err
	}
	if h := s.Healthcheck; h != nil {
		if len(h.Test) == 0 || !slices.Contains([]string{"CMD", "CMD-SHELL", "NONE"}, h.Test[0]) {
			return fieldErr("healthcheck.test", `must start with "CMD", "CMD-SHELL" or "NONE"`)
		}
		if err := validArgs("healthcheck.test", h.Test); err != nil {
			return err
		}
		if h.Interval < 0 || h.Timeout < 0 || h.StartPeriod < 0 || h.Retries < 0 {
			return fieldErr("healthcheck", "durations and retries must not be negative")
		}
	}
	return nil
}

func (m MountSpec) validate(f string) error {
	if !path.IsAbs(m.Target) || path.Clean(m.Target) != m.Target || m.Target == "/" {
		return fieldErr(f+".target", "must be a clean absolute path other than /")
	}
	switch m.Type {
	case "bind":
		if !path.IsAbs(m.Source) || path.Clean(m.Source) != m.Source {
			return fieldErr(f+".source", "a bind mount needs a clean absolute host path")
		}
		for _, p := range DockerSocketPaths {
			if m.Source == "/" || m.Source == p || strings.HasPrefix(p, m.Source+"/") || strings.HasSuffix(m.Source, "/docker.sock") {
				return fieldErr(f+".source", "binding the Docker socket (or a directory containing it) is not allowed: it would give the container unrestricted Engine access")
			}
		}
	case "volume":
		if m.Source != "" && !ValidDockerName(m.Source) {
			return fieldErr(f+".source", "must be a volume name (empty for an anonymous volume)")
		}
	case "tmpfs":
		if m.Source != "" {
			return fieldErr(f+".source", "a tmpfs mount has no source")
		}
	default:
		return fieldErr(f+".type", "must be bind, volume or tmpfs")
	}
	return nil
}

func validIP(s string) bool {
	if strings.Contains(s, ":") {
		return strings.Count(s, ":") >= 2 && len(s) <= 45 && strings.Trim(s, "0123456789abcdefABCDEF:.") == ""
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 || strings.Trim(p, "0123456789") != "" {
			return false
		}
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// OwnershipLabels are the only DockYard labels a create input may set.
var OwnershipLabels = []string{LabelManaged, LabelInstance, LabelSpec}

// Validate checks a create-container input.
func (in ContainerCreateInput) Validate() error {
	if err := in.Spec.Validate(); err != nil {
		return err
	}
	for k, v := range in.Ownership {
		if !slices.Contains(OwnershipLabels, k) || v == "" || len(v) > 128 {
			return fieldErr("ownership", "label %q is not an ownership label", k)
		}
	}
	return nil
}

// Validate checks a volume create input.
func (in VolumeCreateInput) Validate() error {
	if !ValidDockerName(in.Name) {
		return fieldErr("name", "must start with a letter or digit and contain only letters, digits, _ . - (at most 128)")
	}
	if in.Driver != "" && !driverRE.MatchString(in.Driver) {
		return fieldErr("driver", "invalid driver name")
	}
	if len(in.DriverOpts) > maxLabels {
		return fieldErr("driverOpts", "at most %d options", maxLabels)
	}
	return ValidateLabels("labels", in.Labels)
}

// Validate checks a network create input.
func (in NetworkCreateInput) Validate() error {
	if !ValidDockerName(in.Name) {
		return fieldErr("name", "must start with a letter or digit and contain only letters, digits, _ . - (at most 128)")
	}
	if slices.Contains(BuiltinNetworks, in.Name) {
		return fieldErr("name", "%q is a predefined network", in.Name)
	}
	if in.Driver != "" && !driverRE.MatchString(in.Driver) {
		return fieldErr("driver", "invalid driver name")
	}
	if len(in.Options) > maxLabels {
		return fieldErr("options", "at most %d options", maxLabels)
	}
	return ValidateLabels("labels", in.Labels)
}

// Validate checks an image pull input.
func (in ImagePullInput) Validate() error {
	if !ValidImageReference(in.Reference) {
		return fieldErr("reference", "must be an image reference such as nginx:1.27 or ghcr.io/org/app:tag")
	}
	if !ValidatePlatform(in.Platform) {
		return fieldErr("platform", "must be os/arch[/variant], e.g. linux/arm64/v8")
	}
	return nil
}

// ValidateTagTarget checks the repository[:tag] of image.tag.
func ValidateTagTarget(target string) error {
	if !ValidImageReference(target) || strings.Contains(target, "@") {
		return fieldErr("target", "must be repository[:tag] without a digest")
	}
	return nil
}

// BuiltinNetworks are the predefined Docker networks.
var BuiltinNetworks = []string{"bridge", "host", "none"}
