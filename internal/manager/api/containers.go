package api

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Container capabilities (#17).
const (
	CapContainerDetailsRead Capability = "container.details.read"
	CapContainerCreate      Capability = "container.create"
	CapContainerUpdate      Capability = "container.update"
	CapContainerRemove      Capability = "container.remove"
	CapContainerStart       Capability = "container.start"
	CapContainerStop        Capability = "container.stop"
	CapContainerRestart     Capability = "container.restart"
	CapContainerPause       Capability = "container.pause"
	CapContainerUnpause     Capability = "container.unpause"
	CapContainerRecreate    Capability = "container.recreate"
)

// ContainerPort is a published or exposed container port.
type ContainerPort struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort,omitempty"`
	HostIP        string `json:"hostIp,omitempty"`
	Protocol      string `json:"protocol" enum:"tcp,udp,sctp"`
}

// ContainerMount is a mount of a container. Volume mounts name the volume;
// their host path is never shown.
type ContainerMount struct {
	Type        string `json:"type" example:"volume"`
	Name        string `json:"name,omitempty" doc:"Volume name (volume mounts)."`
	Source      string `json:"source,omitempty" doc:"Host path (bind mounts)."`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

// ContainerOwnership marks a standalone container created through Docker Manager.
type ContainerOwnership struct {
	Kind string `json:"kind" enum:"standalone"`
	// SpecSaved: the recreate specification is saved (automatic updates, #20).
	SpecSaved bool `json:"specSaved" doc:"Docker Manager saved the container's recreate specification (used by automatic updates, #20)."`
	// ThisInstance: created by this manager (not another Docker Manager instance).
	ThisInstance bool `json:"thisInstance"`
}

// ContainerResources are container limits (0 or absent: unlimited).
type ContainerResources struct {
	CPUs            float64 `json:"cpus,omitempty" minimum:"0" maximum:"1024" doc:"CPU quota in CPUs (e.g. 1.5)."`
	CPUShares       int64   `json:"cpuShares,omitempty" minimum:"0" maximum:"262144" doc:"Relative CPU weight."`
	MemoryBytes     int64   `json:"memoryBytes,omitempty" minimum:"0" doc:"Memory limit; at least 6 MiB when set."`
	MemorySwapBytes int64   `json:"memorySwapBytes,omitempty" minimum:"-1" doc:"Memory plus swap; -1 unlimited swap."`
	PidsLimit       *int64  `json:"pidsLimit,omitempty" minimum:"-1" doc:"Maximum number of processes; -1 unlimited."`
}

func (r ContainerResources) spec() protocol.ResourcesSpec {
	return protocol.ResourcesSpec{NanoCPUs: int64(math.Round(r.CPUs * 1e9)), CPUShares: r.CPUShares, Memory: r.MemoryBytes,
		MemorySwap: r.MemorySwapBytes, PidsLimit: r.PidsLimit}
}

func newResources(r protocol.ResourcesSpec) ContainerResources {
	return ContainerResources{CPUs: float64(r.NanoCPUs) / 1e9, CPUShares: r.CPUShares, MemoryBytes: r.Memory, MemorySwapBytes: r.MemorySwap, PidsLimit: r.PidsLimit}
}

// ContainerHealthcheck is a container health check.
type ContainerHealthcheck struct {
	Test               []string `json:"test" minItems:"1" maxItems:"64" doc:"[\"CMD\", \"/bin/check\"], [\"CMD-SHELL\", \"curl -f http://localhost/\"] or [\"NONE\"]."`
	IntervalSeconds    int      `json:"intervalSeconds,omitempty" minimum:"0" maximum:"86400"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"86400"`
	StartPeriodSeconds int      `json:"startPeriodSeconds,omitempty" minimum:"0" maximum:"86400"`
	Retries            int      `json:"retries,omitempty" minimum:"0" maximum:"100"`
}

func (h ContainerHealthcheck) spec() *protocol.HealthcheckSpec {
	return &protocol.HealthcheckSpec{Test: h.Test, Interval: time.Duration(h.IntervalSeconds) * time.Second,
		Timeout: time.Duration(h.TimeoutSeconds) * time.Second, StartPeriod: time.Duration(h.StartPeriodSeconds) * time.Second, Retries: h.Retries}
}

func newHealthcheck(h *protocol.HealthcheckSpec) *ContainerHealthcheck {
	if h == nil {
		return nil
	}
	return &ContainerHealthcheck{Test: h.Test, IntervalSeconds: int(h.Interval / time.Second), TimeoutSeconds: int(h.Timeout / time.Second),
		StartPeriodSeconds: int(h.StartPeriod / time.Second), Retries: h.Retries}
}

// ContainerNetwork is a container's address on one network.
type ContainerNetwork struct {
	Name        string   `json:"name"`
	IPAddress   string   `json:"ipAddress,omitempty"`
	IPv6Address string   `json:"ipv6Address,omitempty"`
	MacAddress  string   `json:"macAddress,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// ContainerRecreate says which settings need a recreation and what Docker Manager
// saved to recreate the container.
type ContainerRecreate struct {
	Fields []string `json:"fields" doc:"Settings that cannot change in place: changing them needs a new container (PATCH refuses them with recreate_required)."`
	// EnvKeys are the variable names of the saved specification (never
	// values).
	EnvKeys []string `json:"envKeys,omitempty" doc:"Environment variable names of the saved recreate specification; values are sealed and never returned."`
}

// ContainerDetails is the full configuration and state of a container.
type ContainerDetails struct {
	Cmd               []string              `json:"cmd"`
	Entrypoint        []string              `json:"entrypoint"`
	WorkingDir        string                `json:"workingDir,omitempty"`
	User              string                `json:"user,omitempty"`
	Tty               bool                  `json:"tty"`
	Hostname          string                `json:"hostname,omitempty"`
	RestartPolicy     string                `json:"restartPolicy,omitempty"`
	RestartMaxRetries int                   `json:"restartMaxRetries,omitempty" minimum:"1" doc:"Maximum retry count of the on-failure restart policy. Absent: unlimited, another policy, or an agent that does not report it."`
	NetworkMode       string                `json:"networkMode,omitempty"`
	Networks          []ContainerNetwork    `json:"networks"`
	RestartCount      int                   `json:"restartCount"`
	Platform          string                `json:"platform,omitempty"`
	Running           bool                  `json:"running"`
	Paused            bool                  `json:"paused"`
	OOMKilled         bool                  `json:"oomKilled"`
	ExitCode          int                   `json:"exitCode"`
	Error             string                `json:"error,omitempty"`
	StartedAt         *time.Time            `json:"startedAt,omitempty"`
	FinishedAt        *time.Time            `json:"finishedAt,omitempty"`
	Resources         ContainerResources    `json:"resources"`
	Healthcheck       *ContainerHealthcheck `json:"healthcheck,omitempty"`
	Recreate          ContainerRecreate     `json:"recreate"`
	Removal           Removal               `json:"removal"`
}

// Container is a container of an environment. Environment variables are
// never returned (they often hold secrets).
//
// Shaping (#17): container.details.read shows everything; any other
// container capability (stats, restart, ...) shows only id, name,
// environmentId, state, health, stack and service identity, view and the
// granted actions (view minimal).
type Container struct {
	ID            string              `json:"id"`
	Name          string              `json:"name" example:"web"`
	EnvironmentID string              `json:"environmentId"`
	State         string              `json:"state" enum:"created,running,paused,restarting,removing,exited,dead" example:"running"`
	Health        string              `json:"health,omitempty" enum:"starting,healthy,unhealthy,none"`
	Stack         *StackMembership    `json:"stack,omitempty" doc:"Compose project and service."`
	Protection    *ResourceProtection `json:"protection,omitempty" doc:"Set for Docker Manager's own containers (#32): stop, pause, update and removal are refused."`
	View          string              `json:"view" enum:"minimal,full" doc:"full: container.details.read; minimal: identity, state and the granted actions (#17)."`
	Actions       []string            `json:"actions" doc:"Granted container capabilities (e.g. container.restart)."`
	Update        string              `json:"update,omitempty" enum:"ineligible,unchecked,up_to_date,update_available,quarantined,check_failed,run_failed" doc:"Latest image update state when an update policy covers this container. Full view only."`

	Image     string              `json:"image,omitempty" doc:"Full view."`
	ImageID   string              `json:"imageId,omitempty"`
	Command   string              `json:"command,omitempty"`
	Status    string              `json:"status,omitempty" doc:"The Engine's description, e.g. \"Up 3 hours\"."`
	CreatedAt *time.Time          `json:"createdAt,omitempty"`
	StartedAt *time.Time          `json:"startedAt,omitempty" doc:"Last start of a running, paused or restarting container (uptime). Full view."`
	Networks  []ContainerNetwork  `json:"networks,omitempty" doc:"The container's addresses per network (empty while it is stopped). Full view."`
	Labels    map[string]string   `json:"labels,omitempty"`
	Ports     []ContainerPort     `json:"ports,omitempty"`
	Mounts    []ContainerMount    `json:"mounts,omitempty"`
	Managed   *ContainerOwnership `json:"managed,omitempty" doc:"Set for standalone containers created through Docker Manager."`
	Details   *ContainerDetails   `json:"details,omitempty" doc:"Full view of GET only."`
}

// managedStack reports whether st is a Docker Manager-managed stack.
func managedStack(st *protocol.StackRef, stackIDs map[string]string) bool {
	if st == nil {
		return false
	}
	_, known := stackIDs[st.Project]
	return st.Managed || known
}

func containerResource(env string, c protocol.ContainerSummary, stackIDs map[string]string) authz.Resource {
	return authz.Resource{Type: catalog.TypeContainer, ID: c.Name, EnvironmentID: env, Parents: parents(catalog.TypeContainer, stackIDs, c.Stack)}
}

func newContainer(env string, c protocol.ContainerSummary, v authz.View, stackIDs map[string]string, instanceID string) Container {
	out := Container{ID: c.ID, Name: c.Name, EnvironmentID: env, State: c.State, Health: c.Health,
		Stack: membership(c.Stack, stackIDs, managedStack(c.Stack, stackIDs)), Protection: newProtection(c.Protection),
		View: v.Level.String(), Actions: Actions(v)}
	if !v.Full() {
		return out
	}
	created := c.Created
	out.Image, out.ImageID, out.Command, out.Status, out.CreatedAt, out.Labels = c.Image, c.ImageID, c.Command, c.Status, &created, c.Labels
	for _, p := range c.Ports {
		out.Ports = append(out.Ports, ContainerPort{ContainerPort: int(p.ContainerPort), HostPort: int(p.HostPort), HostIP: p.HostIP, Protocol: p.Protocol})
	}
	for _, m := range c.Mounts {
		out.Mounts = append(out.Mounts, ContainerMount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, ReadOnly: m.ReadOnly})
	}
	if c.StartedAt != nil && (c.State == "running" || c.State == "paused" || c.State == "restarting") {
		started := *c.StartedAt
		out.StartedAt = &started
	}
	for _, n := range c.NetworkList {
		out.Networks = append(out.Networks, ContainerNetwork{Name: n.Name, IPAddress: n.IPAddress, IPv6Address: n.IPv6Address})
	}
	if protocol.LabelValue(c.Labels, protocol.LabelManaged) == protocol.ManagedStandalone {
		out.Managed = &ContainerOwnership{Kind: protocol.ManagedStandalone,
			ThisInstance: instanceID != "" && protocol.LabelValue(c.Labels, protocol.LabelInstance) == instanceID}
	}
	return out
}

// visibleContainer inspects a container of the environment and returns it
// with the caller's view; 404 when it does not exist there or is hidden.
func (h *dockerAPI) visibleContainer(ctx context.Context, sc *scope, ref string) (protocol.ContainerDetails, authz.View, error) {
	d, err := h.svc.InspectContainer(ctx, sc.env.ID, ref)
	if err != nil {
		return d, authz.View{}, lookupErr(err, "container")
	}
	d.Protection = h.svc.ContainerProtection(d.ContainerSummary)
	v := authz.ViewOf(sc.c, containerResource(sc.env.ID, d.ContainerSummary, sc.stacks(ctx, h.svc)))
	if !v.Visible() {
		return d, v, NotFound("container not found")
	}
	return d, v, nil
}

type listContainersInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	PageParams
	SortParam
	Q     string   `query:"q" maxLength:"128" doc:"Only containers whose name contains this text (case-insensitive)."`
	State []string `query:"state" enum:"created,running,paused,restarting,removing,exited,dead" doc:"Only containers in these states."`
	Label []string `query:"label" maxItems:"16" doc:"Only containers with these labels (key or key=value; all must match). Full view only."`
	Stack string   `query:"stack" maxLength:"128" doc:"Only containers of this Compose project."`
}

// ContainerPath are the path parameters of a container route (exported:
// Huma reads embedded path parameters of exported types only).
type ContainerPath struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	ContainerID   string `path:"containerId" maxLength:"128" doc:"Container name, ID or unique ID prefix."`
}

type containerOutput struct{ Body Container }
type listContainersOutput struct{ Body Page[Container] }

// ContainerPortSpec publishes a container port.
type ContainerPortSpec struct {
	ContainerPort int    `json:"containerPort" minimum:"1" maximum:"65535"`
	Protocol      string `json:"protocol,omitempty" enum:"tcp,udp" doc:"Default tcp."`
	HostIP        string `json:"hostIp,omitempty" maxLength:"45"`
	HostPort      int    `json:"hostPort,omitempty" minimum:"0" maximum:"65535" doc:"0 or absent: an ephemeral port."`
}

// ContainerMountSpec requests a mount.
type ContainerMountSpec struct {
	Type     string `json:"type" enum:"bind,volume,tmpfs"`
	Source   string `json:"source,omitempty" maxLength:"4096" doc:"bind: absolute host path (never the Docker socket); volume: volume name (absent: anonymous volume)."`
	Target   string `json:"target" minLength:"2" maxLength:"4096" doc:"Absolute path in the container."`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// ContainerNetworkSpec attaches the container to a network.
type ContainerNetworkSpec struct {
	Name    string   `json:"name" minLength:"1" maxLength:"128"`
	Aliases []string `json:"aliases,omitempty" maxItems:"16"`
}

// ContainerCreateBody is the v1 create-container form: the common options
// only. Anything more complex belongs in a Compose stack (#7); other
// options (privileged mode, capabilities, devices, security options,
// ulimits, sysctls, log drivers, DNS settings, extra hosts, init, IPC/PID
// modes, GPUs, ...) are not accepted.
type ContainerCreateBody struct {
	Name          string                 `json:"name" minLength:"1" maxLength:"128" pattern:"^[a-zA-Z0-9][a-zA-Z0-9_.-]*$" example:"web"`
	Image         string                 `json:"image" minLength:"1" maxLength:"512" example:"nginx:1.27" doc:"Image reference or ID; the image must be present (pull it first)."`
	Command       []string               `json:"command,omitempty" maxItems:"64"`
	Entrypoint    []string               `json:"entrypoint,omitempty" maxItems:"64"`
	Env           []string               `json:"env,omitempty" maxItems:"256" doc:"KEY=value. Values are stored sealed in the recreate specification and never returned."`
	Labels        map[string]string      `json:"labels,omitempty" doc:"User labels; docker-manager.* (except the docker-manager.*.exclude labels), dev.neureka.docker-manager.* and com.docker.compose.* are reserved."`
	WorkingDir    string                 `json:"workingDir,omitempty" maxLength:"4096"`
	User          string                 `json:"user,omitempty" maxLength:"256"`
	Ports         []ContainerPortSpec    `json:"ports,omitempty" maxItems:"64"`
	Mounts        []ContainerMountSpec   `json:"mounts,omitempty" maxItems:"64"`
	Networks      []ContainerNetworkSpec `json:"networks,omitempty" maxItems:"16" doc:"The first is the container's network (default bridge; host and none stand alone); the others are connected after creation."`
	RestartPolicy string                 `json:"restartPolicy,omitempty" enum:"no,always,on-failure,unless-stopped"`
	Resources     *ContainerResources    `json:"resources,omitempty"`
	Healthcheck   *ContainerHealthcheck  `json:"healthcheck,omitempty"`
	Start         *bool                  `json:"start,omitempty" doc:"Start the container after creating it (default true)."`
}

func (b ContainerCreateBody) spec() protocol.ContainerSpec {
	s := protocol.ContainerSpec{Name: b.Name, Image: b.Image, Command: b.Command, Entrypoint: b.Entrypoint, Env: b.Env, Labels: b.Labels,
		WorkingDir: b.WorkingDir, User: b.User, RestartPolicy: b.RestartPolicy}
	for _, p := range b.Ports {
		s.Ports = append(s.Ports, protocol.PortSpec{ContainerPort: uint16(p.ContainerPort), Protocol: p.Protocol, HostIP: p.HostIP, HostPort: uint16(p.HostPort)}) //nolint:gosec // bounded by the schema (1..65535)
	}
	for _, m := range b.Mounts {
		s.Mounts = append(s.Mounts, protocol.MountSpec(m))
	}
	for _, n := range b.Networks {
		s.Networks = append(s.Networks, protocol.NetworkAttachment(n))
	}
	if b.Resources != nil {
		s.Resources = b.Resources.spec()
	}
	if b.Healthcheck != nil {
		s.Healthcheck = b.Healthcheck.spec()
	}
	return s
}

type createContainerInput struct {
	EnvironmentID string `path:"environmentId" maxLength:"64" doc:"Environment ID."`
	IdempotencyKeyParam
	Body ContainerCreateBody
}

// ContainerUpdateBody changes in-place settings. The recreate fields are
// accepted only to be refused with 422 recreate_required.
type ContainerUpdateBody struct {
	RestartPolicy string              `json:"restartPolicy,omitempty" example:"unless-stopped" enum:"no,always,on-failure,unless-stopped"`
	Resources     *ContainerResources `json:"resources,omitempty"`

	Name        *string                `json:"name,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Image       *string                `json:"image,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Command     []string               `json:"command,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Entrypoint  []string               `json:"entrypoint,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Env         []string               `json:"env,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Labels      map[string]string      `json:"labels,omitempty" doc:"Needs recreation: refused with recreate_required."`
	WorkingDir  *string                `json:"workingDir,omitempty" doc:"Needs recreation: refused with recreate_required."`
	User        *string                `json:"user,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Ports       []ContainerPortSpec    `json:"ports,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Mounts      []ContainerMountSpec   `json:"mounts,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Networks    []ContainerNetworkSpec `json:"networks,omitempty" doc:"Needs recreation: refused with recreate_required."`
	Healthcheck *ContainerHealthcheck  `json:"healthcheck,omitempty" doc:"Needs recreation: refused with recreate_required."`
}

// recreateFields lists the recreate-only fields a PATCH body sets.
func (b ContainerUpdateBody) recreateFields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(b.Name != nil, "name")
	add(b.Image != nil, "image")
	add(b.Command != nil, "command")
	add(b.Entrypoint != nil, "entrypoint")
	add(b.Env != nil, "env")
	add(b.Labels != nil, "labels")
	add(b.WorkingDir != nil, "workingDir")
	add(b.User != nil, "user")
	add(b.Ports != nil, "ports")
	add(b.Mounts != nil, "mounts")
	add(b.Networks != nil, "networks")
	add(b.Healthcheck != nil, "healthcheck")
	return out
}

// recreateFields are the create-form settings that need a new container.
var recreateFields = []string{"name", "image", "command", "entrypoint", "env", "labels", "workingDir", "user", "ports", "mounts", "networks", "healthcheck"}

type updateContainerInput struct {
	ContainerPath
	IdempotencyKeyParam
	Body ContainerUpdateBody
}

type deleteContainerInput struct {
	ContainerPath
	IdempotencyKeyParam
	Force         bool `query:"force" doc:"Kill a running container first."`
	RemoveVolumes bool `query:"removeVolumes" doc:"Also remove the container's anonymous volumes (named volumes are never removed)."`
}

type containerActionInput struct {
	ContainerPath
	IdempotencyKeyParam
}

type containerStopInput struct {
	ContainerPath
	IdempotencyKeyParam
	Body *struct {
		TimeoutSeconds *int `json:"timeoutSeconds,omitempty" example:"10" minimum:"0" maximum:"3600" doc:"Seconds to wait before killing; default: the container's stop timeout."`
	}
}

type containerRestartInput struct {
	ContainerPath
	IdempotencyKeyParam
	Body *struct {
		TimeoutSeconds *int `json:"timeoutSeconds,omitempty" example:"10" minimum:"0" maximum:"3600" doc:"Seconds to wait before killing; default: the container's stop timeout."`
		Confirm        bool `json:"confirm,omitempty" doc:"Confirms a restart that interrupts Docker Manager (its manager or proxy, #32); without it such a restart answers 409 confirmation_required."`
	}
}

type containerRecreateInput struct {
	ContainerPath
	IdempotencyKeyParam
	Body *struct {
		TimeoutSeconds *int `json:"timeoutSeconds,omitempty" example:"10" minimum:"0" maximum:"3600" doc:"Seconds to wait before killing the old container; default: the container's stop timeout."`
	}
}

func (h *dockerAPI) listContainers(ctx context.Context, in *listContainersInput) (*listContainersOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	all, err := h.svc.ListContainers(ctx, sc.env.ID)
	if err != nil {
		return nil, dockerErr(err)
	}
	stacks := sc.stacks(ctx, h.svc)
	type item struct {
		c protocol.ContainerSummary
		v authz.View
	}
	var items []item
	for _, c := range all {
		c.Protection = h.svc.ContainerProtection(c)
		v := authz.ViewOf(sc.c, containerResource(sc.env.ID, c, stacks))
		switch {
		case !v.Visible():
			continue
		case in.Q != "" && !containsFold(c.Name, in.Q):
			continue
		case len(in.State) > 0 && !slices.Contains(in.State, c.State):
			continue
		case in.Stack != "" && (c.Stack == nil || c.Stack.Project != in.Stack):
			continue
		}
		if len(in.Label) > 0 && (!v.Full() || slices.ContainsFunc(in.Label, func(l string) bool { return !matchLabel(c.Labels, l) })) {
			continue // label filters apply to the full view only: labels are not minimal fields
		}
		items = append(items, item{c, v})
	}
	key, desc, err := sortSpec(in.Sort, map[string]func(item) string{
		"name":      func(i item) string { return i.c.Name },
		"createdAt": func(i item) string { return timeKey(i.c.Created) },
		"state":     func(i item) string { return i.c.State },
	}, SortKey{Field: "name"}, func(i item) string { return i.c.ID })
	if err != nil {
		return nil, err
	}
	fp := QueryFingerprint("containers", sc.env.ID, in.Sort, in.Q, strings.Join(in.State, ","), strings.Join(in.Label, ","), in.Stack)
	page, next, total, err := memPage(items, key, desc, in.PageParams, fp)
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(page))
	statuses := map[string]string{}
	if h.updates != nil {
		policies, err := h.updates.List(ctx, sc.env.ID, "", 0)
		if err != nil {
			return nil, Internal(err)
		}
		for _, policy := range policies {
			candidates, err := h.updates.Candidates(ctx, policy.ID)
			if err != nil {
				return nil, Internal(err)
			}
			for _, candidate := range candidates {
				key := policy.TargetID
				if policy.TargetType == domain.UpdateTargetStack {
					key += "/" + candidate.Service
				}
				statuses[key] = string(candidate.Status)
			}
		}
	}
	for _, it := range page {
		container := newContainer(sc.env.ID, it.c, it.v, stacks, h.instance)
		if it.v.Full() && !protocol.UpdateExcluded(it.c.Labels) {
			key := it.c.Name
			if it.c.Stack != nil {
				key = stacks[it.c.Stack.Project] + "/" + it.c.Stack.Service
			}
			container.Update = statuses[key]
		}
		out = append(out, container)
	}
	return &listContainersOutput{Body: NewPage(out, next, total)}, nil
}

func (h *dockerAPI) getContainer(ctx context.Context, in *ContainerPath) (*containerOutput, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, false)
	if err != nil {
		return nil, err
	}
	d, v, err := h.visibleContainer(ctx, sc, in.ContainerID)
	if err != nil {
		return nil, err
	}
	stacks := sc.stacks(ctx, h.svc)
	out := newContainer(sc.env.ID, d.ContainerSummary, v, stacks, h.instance)
	if v.Full() && h.updates != nil && !protocol.UpdateExcluded(d.Labels) {
		typ, id, service := domain.UpdateTargetContainer, d.Name, d.Name
		if d.Stack != nil {
			typ, id, service = domain.UpdateTargetStack, stacks[d.Stack.Project], d.Stack.Service
		}
		if id != "" {
			policy, err := h.updates.ForTarget(ctx, sc.env.ID, typ, id)
			if err != nil {
				return nil, Internal(err)
			}
			if policy != nil {
				candidates, err := h.updates.Candidates(ctx, policy.ID)
				if err != nil {
					return nil, Internal(err)
				}
				for _, candidate := range candidates {
					if candidate.Service == service {
						out.Update = string(candidate.Status)
						break
					}
				}
			}
		}
	}
	if !v.Full() {
		return &containerOutput{Body: out}, nil
	}
	det := &ContainerDetails{Cmd: nonNil(d.Cmd), Entrypoint: nonNil(d.Entrypoint), WorkingDir: d.WorkingDir, User: d.User, Tty: d.Tty,
		Hostname: d.Hostname, RestartPolicy: d.RestartPolicy, RestartMaxRetries: max(d.RestartMaxRetries, 0), NetworkMode: d.NetworkMode, Networks: []ContainerNetwork{}, RestartCount: d.RestartCount,
		Platform: d.Platform, Running: d.Running, Paused: d.Paused, OOMKilled: d.OOMKilled, ExitCode: d.ExitCode, Error: d.Error,
		StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, Resources: newResources(d.Resources), Healthcheck: newHealthcheck(d.Healthcheck),
		Recreate: ContainerRecreate{Fields: slices.Clone(recreateFields)}}
	for _, n := range d.NetworkList {
		det.Networks = append(det.Networks, ContainerNetwork{Name: n.Name, IPAddress: n.IPAddress, IPv6Address: n.IPv6Address, MacAddress: n.MacAddress, Aliases: n.Aliases})
	}
	m, spec, err := h.svc.ManagedSpec(ctx, sc.env.ID, d.Labels)
	if err != nil {
		return nil, Internal(err)
	}
	if m != nil && out.Managed != nil {
		out.Managed.SpecSaved = true
		for _, e := range spec.Env {
			k, _, _ := strings.Cut(e, "=")
			det.Recreate.EnvKeys = append(det.Recreate.EnvKeys, k)
		}
	}
	det.Removal = h.containerRemoval(ctx, sc, d, m != nil)
	out.Details = det
	return &containerOutput{Body: out}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *dockerAPI) containerRemoval(ctx context.Context, sc *scope, d protocol.ContainerDetails, specSaved bool) Removal {
	r := newRemoval("The container and its writable layer are deleted; its logs are lost.",
		"Anonymous volumes are kept unless removeVolumes=true; named volumes are never removed.",
		"Exact permission rules on this container are removed.")
	if specSaved {
		r.Consequences = append(r.Consequences, "Docker Manager forgets the container's saved recreate specification (automatic updates stop).")
	}
	r.blockProtected(d.Protection)
	if managedStack(d.Stack, sc.stacks(ctx, h.svc)) {
		r.block(CodeStackManaged, "The container belongs to a Docker Manager-managed stack; change the stack instead.")
	}
	if d.Running {
		r.Consequences = append(r.Consequences, "The container is running: removal needs force=true, which kills it first.")
	}
	return r
}

// action authorizes one capability on a visible container.
func (h *dockerAPI) action(ctx context.Context, envID, ref string, want Capability, mutation bool) (*scope, protocol.ContainerDetails, error) {
	sc, err := h.environment(ctx, envID, mutation)
	if err != nil {
		return nil, protocol.ContainerDetails{}, err
	}
	d, v, err := h.visibleContainer(ctx, sc, ref)
	if err != nil {
		return nil, d, err
	}
	if !v.Has(string(want)) {
		return nil, d, Forbidden("not permitted to " + strings.TrimPrefix(string(want), "container.") + " this container")
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeContainer, ID: d.Name, EnvironmentID: sc.env.ID})
	return sc, d, nil
}

func (h *dockerAPI) createContainer(ctx context.Context, in *createContainerInput) (*JobAccepted, error) {
	sc, err := h.environment(ctx, in.EnvironmentID, true)
	if err != nil {
		return nil, err
	}
	if !sc.c.Can(string(CapContainerCreate), authz.InEnvironment(catalog.TypeContainer, sc.env.ID)).Allowed {
		return nil, Forbidden("not permitted to create containers in this environment")
	}
	start := in.Body.Start == nil || *in.Body.Start
	j, err := h.svc.CreateContainer(ctx, sc.p, sc.env.ID, in.Body.spec(), start, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: catalog.TypeContainer, ID: in.Body.Name, EnvironmentID: sc.env.ID})
	audit.SetDetail(ctx, "image", in.Body.Image)
	return Accepted(j), nil
}

func (h *dockerAPI) updateContainer(ctx context.Context, in *updateContainerInput) (*JobAccepted, error) {
	sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, CapContainerUpdate, true)
	if err != nil {
		return nil, err
	}
	if f := in.Body.recreateFields(); len(f) > 0 {
		details := make([]ErrorDetail, 0, len(f))
		for _, name := range f {
			details = append(details, Field("body."+name, "changing it needs the container recreated"))
		}
		return nil, NewError(http.StatusUnprocessableEntity, CodeRecreateRequired,
			"these settings cannot change in place: create a new container (or use a Compose stack) instead", details...)
	}
	u := protocol.ContainerUpdateInput{RestartPolicy: in.Body.RestartPolicy}
	if in.Body.Resources != nil {
		r := in.Body.Resources.spec()
		u.Resources = &r
	}
	j, err := h.svc.UpdateContainer(ctx, sc.p, sc.env.ID, d, u, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

func (h *dockerAPI) deleteContainer(ctx context.Context, in *deleteContainerInput) (*JobAccepted, error) {
	sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, CapContainerRemove, true)
	if err != nil {
		return nil, err
	}
	j, err := h.svc.ContainerAction(ctx, sc.p, sc.env.ID, jobspec.ContainerRemove, d,
		protocol.ContainerActionInput{Force: in.Force, RemoveVolumes: in.RemoveVolumes}, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

// lifecycle returns the handler of a start/restart/pause/unpause route.
func (h *dockerAPI) lifecycle(kind domain.JobKind) func(ctx context.Context, in *containerActionInput) (*JobAccepted, error) {
	return func(ctx context.Context, in *containerActionInput) (*JobAccepted, error) {
		sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, Capability(kind), true)
		if err != nil {
			return nil, err
		}
		j, err := h.svc.ContainerAction(ctx, sc.p, sc.env.ID, kind, d, protocol.ContainerActionInput{}, in.IdempotencyKey)
		if err != nil {
			return nil, dockerErr(err)
		}
		return Accepted(j), nil
	}
}

func (h *dockerAPI) stopContainer(ctx context.Context, in *containerStopInput) (*JobAccepted, error) {
	sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, CapContainerStop, true)
	if err != nil {
		return nil, err
	}
	var a protocol.ContainerActionInput
	if in.Body != nil {
		a.TimeoutSeconds = in.Body.TimeoutSeconds
	}
	j, err := h.svc.ContainerAction(ctx, sc.p, sc.env.ID, jobspec.ContainerStop, d, a, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

func (h *dockerAPI) recreateContainer(ctx context.Context, in *containerRecreateInput) (*JobAccepted, error) {
	sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, CapContainerRecreate, true)
	if err != nil {
		return nil, err
	}
	var a protocol.ContainerActionInput
	if in.Body != nil {
		a.TimeoutSeconds = in.Body.TimeoutSeconds
	}
	j, err := h.svc.ContainerAction(ctx, sc.p, sc.env.ID, jobspec.ContainerRecreate, d, a, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	audit.SetDetail(ctx, "image", d.Image)
	return Accepted(j), nil
}

func (h *dockerAPI) restartContainer(ctx context.Context, in *containerRestartInput) (*JobAccepted, error) {
	sc, d, err := h.action(ctx, in.EnvironmentID, in.ContainerID, CapContainerRestart, true)
	if err != nil {
		return nil, err
	}
	var a protocol.ContainerActionInput
	if in.Body != nil {
		a.TimeoutSeconds, a.Confirmed = in.Body.TimeoutSeconds, in.Body.Confirm
	}
	j, err := h.svc.ContainerAction(ctx, sc.p, sc.env.ID, jobspec.ContainerRestart, d, a, in.IdempotencyKey)
	if err != nil {
		return nil, dockerErr(err)
	}
	return Accepted(j), nil
}

var (
	dockerReadErrors = []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	dockerJobErrors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
)

func registerContainers(a huma.API, deps Deps) {
	h := newDockerAPI(deps)
	base := BasePath + "/environments/{environmentId}/containers"
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-containers", Method: http.MethodGet, Path: base, Summary: "List containers",
			Description: "Every container of the environment (running or not), filtered per item (#17): container.details.read shows a " +
				"container in full; any other container capability shows only its identity, state, health, stack and service and the " +
				"granted actions (view minimal). Sort fields: name (default), createdAt, state. total counts the visible matches. " +
				"503 environment_offline while the environment's agent is not connected.",
			Tags: []string{tagContainers}, Errors: dockerReadErrors,
		},
		Capability: CapContainerDetailsRead, Scope: ScopeEnvironment,
	}, h.listContainers)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-container", Method: http.MethodPost, Path: base, Summary: "Create a container",
			Description: "Validates the v1 create-container form (common options only; anything more complex belongs in a Compose stack) " +
				"and starts a container.create job (202). The image must be present on the environment (pull it first). Docker Manager labels " +
				"the container as its own standalone container and saves its recreate specification (sealed) for automatic updates.",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapContainerCreate, Scope: ScopeEnvironment, Idempotency: IdempotencyJob,
	}, h.createContainer)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-container", Method: http.MethodGet, Path: base + "/{containerId}", Summary: "Get a container",
			Description: "Full view with container.details.read (configuration, state, networks, resources, recreate fields and the " +
				"removal consequences; never environment variable values), minimal view with any other container capability, 404 otherwise.",
			Tags: []string{tagContainers}, Errors: dockerReadErrors,
		},
		Capability: CapContainerDetailsRead, Scope: ScopeResource,
	}, h.getContainer)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-container", Method: http.MethodPatch, Path: base + "/{containerId}", Summary: "Update a container",
			Description: "Changes the in-place settings (restart policy, resource limits) with a container.update job (202). Settings that " +
				"need a new container are refused with 422 recreate_required; containers of a Docker Manager-managed stack with 409 stack_managed.",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapContainerUpdate, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.updateContainer)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-container", Method: http.MethodDelete, Path: base + "/{containerId}", Summary: "Remove a container",
			Description: "Starts a container.remove job (202). A running container needs force=true (409 container_running); containers " +
				"of a Docker Manager-managed stack are refused (409 stack_managed). See the container's removal consequences.",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapContainerRemove, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.deleteContainer)
	for _, act := range []struct {
		kind    domain.JobKind
		verb    string
		summary string
	}{
		{jobspec.ContainerStart, "start", "Start a container"},
		{jobspec.ContainerPause, "pause", "Pause a container"},
		{jobspec.ContainerUnpause, "unpause", "Unpause a container"},
	} {
		Register(a, Operation{
			Operation: huma.Operation{
				OperationID: act.verb + "-container", Method: http.MethodPost, Path: base + "/{containerId}/" + act.verb, Summary: act.summary,
				Description: "Starts a " + string(act.kind) + " job (202). Already in the target state: the job succeeds without changes.",
				Tags:        []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
			},
			Capability: Capability(act.kind), Scope: ScopeResource, Idempotency: IdempotencyJob,
		}, h.lifecycle(act.kind))
	}
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "stop-container", Method: http.MethodPost, Path: base + "/{containerId}/stop", Summary: "Stop a container",
			Description: "Starts a container.stop job (202); timeoutSeconds bounds the graceful stop before the container is killed.",
			Tags:        []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapContainerStop, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.stopContainer)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "restart-container", Method: http.MethodPost, Path: base + "/{containerId}/restart", Summary: "Restart a container",
			Description: "Starts a container.restart job (202). A restart grant allows nothing else (no start, stop, logs, terminal or files). " +
				"Restarting the Docker Manager container (or another container of its own deployment) needs confirm: true (409 confirmation_required); " +
				"the connected agent is never restarted through Docker Manager (409 protected).",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: dockerJobErrors,
		},
		Capability: CapContainerRestart, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.restartContainer)
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "recreate-container", Method: http.MethodPost, Path: base + "/{containerId}/recreate", Summary: "Recreate a container",
			Description: "Starts a container.recreate job (202): replaces a standalone container with a new one of the same name and " +
				"configuration from the image its reference names on the host now (nothing is pulled), like docker compose up " +
				"--force-recreate. Volumes are kept (anonymous ones are mounted again by name); the new container is started only when " +
				"the old one was running; timeoutSeconds bounds the old one's graceful stop. Refused: containers of a Docker " +
				"Manager-managed stack (409 stack_managed), of another Compose project, temporary or being removed (409 conflict), " +
				"Docker Manager's own (409 protected), and agents that cannot recreate containers (501 agent_unsupported).",
			Tags: []string{tagContainers}, DefaultStatus: http.StatusAccepted, Errors: append(slices.Clone(dockerJobErrors), http.StatusNotImplemented),
		},
		Capability: CapContainerRecreate, Scope: ScopeResource, Idempotency: IdempotencyJob,
	}, h.recreateContainer)
}
