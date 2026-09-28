package engine

import (
	"context"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ListContainers lists containers.
func (c *Client) ListContainers(ctx context.Context, f ContainerFilter) ([]Container, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	filters := client.Filters{}
	if len(f.Labels) > 0 {
		filters.Add("label", f.Labels...)
	}
	if len(f.Names) > 0 {
		filters.Add("name", f.Names...)
	}
	if len(f.IDs) > 0 {
		filters.Add("id", f.IDs...)
	}
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: f.All, Size: f.Size, Filters: filters})
	if err != nil {
		return nil, wrap("container.list", err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, s := range res.Items {
		ct := Container{
			ID:      s.ID,
			Names:   trimNames(s.Names),
			Image:   s.Image,
			ImageID: s.ImageID,
			Command: s.Command,
			Created: time.Unix(s.Created, 0).UTC(),
			State:   string(s.State),
			Status:  s.Status,
			Labels:  s.Labels,
			SizeRw:  s.SizeRw,
		}
		if s.Health != nil {
			ct.Health = string(s.Health.Status)
		}
		for _, p := range s.Ports {
			ct.Ports = append(ct.Ports, Port{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, HostIP: addrString(p.IP), Protocol: p.Type})
		}
		for _, m := range s.Mounts {
			ct.Mounts = append(ct.Mounts, mountFrom(m))
		}
		if s.NetworkSettings != nil {
			for n, ep := range s.NetworkSettings.Networks {
				ct.Networks = append(ct.Networks, n)
				if ep != nil {
					if ct.Endpoints == nil {
						ct.Endpoints = map[string]EndpointInfo{}
					}
					ct.Endpoints[n] = endpointFrom(ep)
				}
			}
			sort.Strings(ct.Networks)
		}
		out = append(out, ct)
	}
	return out, nil
}

// InspectContainer returns a container's details.
func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerDetails, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerDetails{}, wrap("container.inspect", err)
	}
	r := res.Container
	d := ContainerDetails{
		ID:           r.ID,
		Name:         strings.TrimPrefix(r.Name, "/"),
		ImageID:      r.Image,
		Created:      parseTime(r.Created),
		Platform:     r.Platform,
		RestartCount: r.RestartCount,
	}
	if r.State != nil {
		s := r.State
		d.State = ContainerState{
			Status: string(s.Status), Running: s.Running, Paused: s.Paused, Restarting: s.Restarting,
			OOMKilled: s.OOMKilled, Dead: s.Dead, Pid: s.Pid, ExitCode: s.ExitCode, Error: s.Error,
			StartedAt: parseTime(s.StartedAt), FinishedAt: parseTime(s.FinishedAt),
		}
		if s.Health != nil {
			d.State.Health = &Health{Status: string(s.Health.Status), FailingStreak: s.Health.FailingStreak}
		}
	}
	if cfg := r.Config; cfg != nil {
		d.Image = cfg.Image
		d.Labels = cfg.Labels
		d.Cmd = cfg.Cmd
		d.Entrypoint = cfg.Entrypoint
		d.WorkingDir = cfg.WorkingDir
		d.User = cfg.User
		d.Tty = cfg.Tty
		d.Hostname = cfg.Hostname
		if h := cfg.Healthcheck; h != nil {
			d.Healthcheck = &HealthcheckSpec{Test: h.Test, Interval: h.Interval, Timeout: h.Timeout,
				StartPeriod: h.StartPeriod, Retries: h.Retries}
		}
	}
	if hc := r.HostConfig; hc != nil {
		d.RestartPolicy = string(hc.RestartPolicy.Name)
		if hc.RestartPolicy.IsOnFailure() && hc.RestartPolicy.MaximumRetryCount > 0 {
			d.RestartMaxRetries = hc.RestartPolicy.MaximumRetryCount
		}
		d.NetworkMode = string(hc.NetworkMode)
		d.Resources = Resources{NanoCPUs: hc.NanoCPUs, CPUShares: hc.CPUShares, Memory: hc.Memory,
			MemorySwap: hc.MemorySwap, PidsLimit: hc.PidsLimit}
	}
	for _, m := range r.Mounts {
		d.Mounts = append(d.Mounts, mountFrom(m))
	}
	if ns := r.NetworkSettings; ns != nil {
		d.Networks = map[string]EndpointInfo{}
		for name, ep := range ns.Networks {
			if ep != nil {
				d.Networks[name] = endpointFrom(ep)
			}
		}
		d.Ports = portsFrom(ns.Ports)
	}
	return d, nil
}

// CreatedConfig returns a container's environment, its image's
// environment and its configured port bindings (see CreatedConfig).
func (c *Client) CreatedConfig(ctx context.Context, id string) (CreatedConfig, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return CreatedConfig{}, wrap("container.inspect", err)
	}
	r := res.Container
	var out CreatedConfig
	if r.Config != nil {
		out.Env = r.Config.Env
	}
	if r.HostConfig != nil {
		out.Ports = portsFrom(r.HostConfig.PortBindings)
	}
	if r.Image != "" {
		img, err := c.api.ImageInspect(ctx, r.Image)
		if err != nil {
			return CreatedConfig{}, wrap("image.inspect", err)
		}
		if cfg := img.Config; cfg != nil {
			out.ImageEnv = cfg.Env
		}
	}
	return out, nil
}

// CreateContainer creates a container and returns its ID and any Engine
// warnings.
func (c *Client) CreateContainer(ctx context.Context, spec ContainerSpec) (string, []string, error) {
	const op = "container.create"
	cfg, hc, nc, platform, err := containerConfig(spec)
	if err != nil {
		return "", nil, &Error{Op: op, Code: CodeInvalidArgument, Message: err.Error(), err: err}
	}
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: spec.Name, Config: cfg, HostConfig: hc, NetworkingConfig: nc, Platform: platform,
	})
	if err != nil {
		return "", nil, wrap(op, err)
	}
	return res.ID, res.Warnings, nil
}

// CloneContainer implements Cloner.
func (c *Client) CloneContainer(ctx context.Context, id string, o CloneOptions) (string, error) {
	const op = "container.clone"
	ctx, cancel := c.bound(ctx)
	defer cancel()
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return "", wrap("container.inspect", err)
	}
	r := res.Container
	if r.Config == nil || r.HostConfig == nil {
		return "", &Error{Op: op, Code: CodeInvalidArgument, Message: "container " + id + " reports no configuration"}
	}
	cfg, hc := *r.Config, *r.HostConfig
	if len(r.ID) >= 12 && cfg.Hostname == r.ID[:12] {
		cfg.Hostname = ""
	}
	// The image the container runs, also when its tag moved since.
	if img, err := c.api.ImageInspect(ctx, cfg.Image); err != nil || img.ID != r.Image {
		cfg.Image = r.Image
	}
	hc.Binds, hc.Mounts = slices.Clone(hc.Binds), slices.Clone(hc.Mounts)
	configured := map[string]bool{}
	for i, b := range hc.Binds {
		parts := strings.SplitN(b, ":", 3)
		if len(parts) < 2 {
			continue
		}
		configured[parts[1]] = true
		if n, ok := o.Volumes[parts[0]]; ok {
			parts[0] = n
			hc.Binds[i] = strings.Join(parts, ":")
		}
	}
	for i, m := range hc.Mounts {
		configured[m.Target] = true
		if m.Type == mount.TypeVolume {
			if n, ok := o.Volumes[m.Source]; ok {
				hc.Mounts[i].Source = n
			}
		}
	}
	// Anonymous volumes (and the image's VOLUMEs) keep their data: the
	// clone mounts them by name.
	for _, m := range r.Mounts {
		if m.Type == mount.TypeVolume && m.Name != "" && !configured[m.Destination] {
			name := m.Name
			if n, ok := o.Volumes[name]; ok {
				name = n
			}
			hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	var nc *network.NetworkingConfig
	if ns := r.NetworkSettings; ns != nil && len(ns.Networks) > 0 && !hc.NetworkMode.IsHost() && !hc.NetworkMode.IsNone() &&
		!hc.NetworkMode.IsContainer() {
		nc = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			var aliases []string
			for _, a := range ep.Aliases {
				if len(r.ID) < 12 || a != r.ID[:12] {
					aliases = append(aliases, a)
				}
			}
			nc.EndpointsConfig[name] = &network.EndpointSettings{IPAMConfig: ep.IPAMConfig, Links: ep.Links, Aliases: aliases,
				DriverOpts: ep.DriverOpts, GwPriority: ep.GwPriority}
		}
	}
	created, err := c.api.ContainerCreate(ctx, client.ContainerCreateOptions{Name: o.Name, Config: &cfg, HostConfig: &hc, NetworkingConfig: nc})
	if err != nil {
		return "", wrap(op, err)
	}
	return created.ID, nil
}

func containerConfig(spec ContainerSpec) (*container.Config, *container.HostConfig, *network.NetworkingConfig, *ocispec.Platform, error) {
	cfg := &container.Config{
		Image:      spec.Image,
		Cmd:        spec.Cmd,
		Entrypoint: spec.Entrypoint,
		Env:        spec.Env,
		Labels:     spec.Labels,
		WorkingDir: spec.WorkingDir,
		User:       spec.User,
		Hostname:   spec.Hostname,
		Tty:        spec.Tty,
		OpenStdin:  spec.OpenStdin,
		StopSignal: spec.StopSignal,
	}
	if spec.StopTimeout != nil {
		s := int(spec.StopTimeout.Round(time.Second) / time.Second)
		cfg.StopTimeout = &s
	}
	if h := spec.Healthcheck; h != nil {
		cfg.Healthcheck = &container.HealthConfig{
			Test: h.Test, Interval: h.Interval, Timeout: h.Timeout, StartPeriod: h.StartPeriod, Retries: h.Retries,
		}
	}
	hc := &container.HostConfig{
		NetworkMode: container.NetworkMode(spec.NetworkMode),
		AutoRemove:  spec.AutoRemove,
		Resources:   resourcesTo(spec.Resources),
	}
	if spec.RestartPolicy != "" {
		hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy)}
	}
	for _, m := range spec.Mounts {
		t := mount.Type(m.Type)
		switch t {
		case mount.TypeBind, mount.TypeVolume, mount.TypeTmpfs:
		default:
			return nil, nil, nil, nil, errorf("unsupported mount type %q", m.Type)
		}
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: t, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	if len(spec.Ports) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		hc.PortBindings = network.PortMap{}
		for _, p := range spec.Ports {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			port, err := network.ParsePort(strconv.Itoa(int(p.ContainerPort)) + "/" + proto)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			b := network.PortBinding{}
			if p.HostPort != 0 {
				b.HostPort = strconv.Itoa(int(p.HostPort))
			}
			if p.HostIP != "" {
				ip, err := netip.ParseAddr(p.HostIP)
				if err != nil {
					return nil, nil, nil, nil, errorf("invalid host IP %q", p.HostIP)
				}
				b.HostIP = ip
			}
			cfg.ExposedPorts[port] = struct{}{}
			hc.PortBindings[port] = append(hc.PortBindings[port], b)
		}
	}
	var nc *network.NetworkingConfig
	if len(spec.NetworkAliases) > 0 && spec.NetworkMode != "" {
		nc = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			spec.NetworkMode: {Aliases: spec.NetworkAliases},
		}}
	}
	var platform *ocispec.Platform
	if spec.Platform != "" {
		p, err := parsePlatform(spec.Platform)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		platform = &p
	}
	return cfg, hc, nc, platform, nil
}

func resourcesTo(r Resources) container.Resources {
	return container.Resources{
		NanoCPUs:   r.NanoCPUs,
		CPUShares:  r.CPUShares,
		Memory:     r.Memory,
		MemorySwap: r.MemorySwap,
		PidsLimit:  r.PidsLimit,
	}
}

// StartContainer starts a container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return wrap("container.start", err)
}

// StopContainer stops a container; timeout nil uses the container's stop
// timeout (or the Engine default), 0 kills immediately.
func (c *Client) StopContainer(ctx context.Context, id string, timeout *time.Duration) error {
	secs, extra := stopTimeout(timeout)
	ctx, cancel := c.boundExtra(ctx, extra)
	defer cancel()
	_, err := c.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: secs})
	return wrap("container.stop", err)
}

// RestartContainer restarts a container (timeout as for StopContainer).
func (c *Client) RestartContainer(ctx context.Context, id string, timeout *time.Duration) error {
	secs, extra := stopTimeout(timeout)
	ctx, cancel := c.boundExtra(ctx, extra)
	defer cancel()
	_, err := c.api.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: secs})
	return wrap("container.restart", err)
}

// defaultStopGrace is the extra request time allowed when the container's
// own stop timeout applies (the Engine default is 10s; Compose files may
// set stop_grace_period higher).
const defaultStopGrace = 5 * time.Minute

func stopTimeout(timeout *time.Duration) (*int, time.Duration) {
	if timeout == nil {
		return nil, defaultStopGrace
	}
	s := int(timeout.Round(time.Second) / time.Second)
	return &s, *timeout
}

// RenameContainer gives a container a new name (#20: a standalone
// container keeps its name across an update; the old one steps aside until
// its replacement exists).
func (c *Client) RenameContainer(ctx context.Context, id, name string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name})
	return wrap("container.rename", err)
}

// PauseContainer pauses a container.
func (c *Client) PauseContainer(ctx context.Context, id string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return wrap("container.pause", err)
}

// UnpauseContainer resumes a paused container.
func (c *Client) UnpauseContainer(ctx context.Context, id string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return wrap("container.unpause", err)
}

// KillContainer sends a signal (default SIGKILL).
func (c *Client) KillContainer(ctx context.Context, id, signal string) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: signal})
	return wrap("container.kill", err)
}

// RemoveOptions configures RemoveContainer.
type RemoveOptions struct {
	// Force kills a running container first.
	Force bool
	// Volumes also removes anonymous volumes.
	Volumes bool
}

// RemoveContainer removes a container.
func (c *Client) RemoveContainer(ctx context.Context, id string, o RemoveOptions) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: o.Force, RemoveVolumes: o.Volumes})
	return wrap("container.remove", err)
}

// ContainerUpdate changes a container's resources and/or restart policy.
type ContainerUpdate struct {
	Resources *Resources
	// RestartPolicy is "no", "always", "on-failure" or "unless-stopped"
	// (empty = unchanged).
	RestartPolicy string
}

// UpdateContainer applies u and returns Engine warnings.
func (c *Client) UpdateContainer(ctx context.Context, id string, u ContainerUpdate) ([]string, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	o := client.ContainerUpdateOptions{}
	if u.Resources != nil {
		r := resourcesTo(*u.Resources)
		o.Resources = &r
	}
	if u.RestartPolicy != "" {
		o.RestartPolicy = &container.RestartPolicy{Name: container.RestartPolicyMode(u.RestartPolicy)}
	}
	res, err := c.api.ContainerUpdate(ctx, id, o)
	if err != nil {
		return nil, wrap("container.update", err)
	}
	return res.Warnings, nil
}

// WaitContainer blocks until the container is not running and returns its
// exit code. It is bounded only by ctx.
func (c *Client) WaitContainer(ctx context.Context, id string) (int64, error) {
	const op = "container.wait"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := c.api.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case r := <-res.Result:
		if r.Error != nil && r.Error.Message != "" {
			return r.StatusCode, newError(op, CodeEngineError, "%s", r.Error.Message)
		}
		return r.StatusCode, nil
	case err := <-res.Error:
		return -1, wrap(op, err)
	case <-ctx.Done():
		return -1, wrap(op, ctx.Err())
	}
}

func trimNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimPrefix(n, "/"))
	}
	return out
}

func mountFrom(m container.MountPoint) Mount {
	return Mount{
		Type:        string(m.Type),
		Name:        m.Name,
		Source:      m.Source,
		Destination: m.Destination,
		Driver:      m.Driver,
		ReadWrite:   m.RW,
		Propagation: string(m.Propagation),
	}
}

func endpointFrom(ep *network.EndpointSettings) EndpointInfo {
	return EndpointInfo{
		NetworkID:   ep.NetworkID,
		IPAddress:   addrString(ep.IPAddress),
		IPv6Address: addrString(ep.GlobalIPv6Address),
		MacAddress:  ep.MacAddress.String(),
		Aliases:     ep.Aliases,
	}
}

func portsFrom(pm network.PortMap) []Port {
	var out []Port
	for p, bindings := range pm {
		if len(bindings) == 0 {
			out = append(out, Port{PrivatePort: p.Num(), Protocol: string(p.Proto())})
			continue
		}
		for _, b := range bindings {
			hp, _ := strconv.ParseUint(b.HostPort, 10, 16)
			out = append(out, Port{PrivatePort: p.Num(), PublicPort: uint16(hp), HostIP: addrString(b.HostIP), Protocol: string(p.Proto())})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PrivatePort != out[j].PrivatePort {
			return out[i].PrivatePort < out[j].PrivatePort
		}
		return out[i].HostIP < out[j].HostIP
	})
	return out
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// parseTime parses Engine RFC 3339 timestamps; the zero value for empty or
// "0001-01-01T00:00:00Z".
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t.UTC()
}
