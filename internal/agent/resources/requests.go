package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Requests returns the request handlers keyed by name.
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqContainerList:    handle(s, s.listContainers),
		protocol.ReqContainerInspect: handle(s, s.inspectContainer),
		protocol.ReqImageList:        handle(s, s.listImages),
		protocol.ReqImageInspect:     handle(s, s.inspectImage),
		protocol.ReqImageTag:         handle(s, s.tagImage),
		protocol.ReqVolumeList:       handle(s, s.listVolumes),
		protocol.ReqVolumeInspect:    handle(s, s.inspectVolume),
		protocol.ReqVolumeUsage:      handle(s, s.volumeUsage),
		protocol.ReqNetworkList:      handle(s, s.listNetworks),
		protocol.ReqNetworkInspect:   handle(s, s.inspectNetwork),
		protocol.ReqManagerIdentity:  s.guard.ManagerIdentityHandler(s.connected),
	}
}

// connected returns the Engine or nil.
func (s *Service) connected() engine.Engine {
	e, _ := s.engine()
	return e
}

// handle decodes the input strictly (unknown fields are refused), runs fn
// against the connected Engine and maps errors to error frames.
func handle[I, O any](s *Service, fn func(ctx context.Context, eng engine.Engine, in I) (O, error)) session.RequestHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in I
		if len(raw) > 0 && string(raw) != "null" {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed request input"}
			}
		}
		eng, err := s.engine()
		if err != nil {
			return nil, handlerErr(err)
		}
		out, err := fn(ctx, eng, in)
		if err != nil {
			return nil, handlerErr(err)
		}
		return out, nil
	}
}

func (s *Service) containerSummary(c engine.Container) protocol.ContainerSummary {
	out := protocol.ContainerSummary{ID: c.ID, Name: containerName(c), Image: c.Image, ImageID: c.ImageID, Command: c.Command,
		Created: c.Created.UTC(), State: c.State, Status: c.Status, Health: c.Health, Labels: maps.Clone(c.Labels),
		Stack: s.stackOf(c.Labels)}
	for _, p := range c.Ports {
		out.Ports = append(out.Ports, protocol.ContainerPort{ContainerPort: p.PrivatePort, HostPort: p.PublicPort, HostIP: p.HostIP, Protocol: p.Protocol})
	}
	out.Mounts = mountsOf(c.Mounts)
	out.Networks = slices.Clone(c.Networks)
	for _, n := range c.Networks {
		ep, ok := c.Endpoints[n]
		if !ok {
			continue
		}
		out.NetworkList = append(out.NetworkList, protocol.ContainerNetwork{Name: n, NetworkID: ep.NetworkID, IPAddress: ep.IPAddress,
			IPv6Address: ep.IPv6Address})
	}
	return out
}

func mountsOf(ms []engine.Mount) []protocol.ContainerMount {
	var out []protocol.ContainerMount
	for _, m := range ms {
		pm := protocol.ContainerMount{Type: m.Type, Name: m.Name, Destination: m.Destination, ReadOnly: !m.ReadWrite}
		if m.Type == "bind" {
			pm.Source = m.Source
		}
		out = append(out, pm)
	}
	return out
}

func (s *Service) listContainers(ctx context.Context, eng engine.Engine, in protocol.ContainerListInput) (protocol.ContainerListOutput, error) {
	f := engine.ContainerFilter{All: true}
	if in.Project != "" {
		f.Labels = []string{protocol.ComposeProjectLabel + "=" + in.Project}
	}
	cs, err := eng.ListContainers(ctx, f)
	if err != nil {
		return protocol.ContainerListOutput{}, err
	}
	// A project's list holds every container of that project, so the
	// protection of its containers is the same as in the full list.
	set := s.guard.Identify(ctx, eng, cs)
	var started []*time.Time
	if !in.NoStartTimes {
		started = startTimes(ctx, s.clock, eng, cs)
	}
	out := protocol.ContainerListOutput{Containers: make([]protocol.ContainerSummary, 0, len(cs))}
	for i, c := range cs {
		sum := s.containerSummary(c)
		sum.Protection = set.Container(c.ID)
		if started != nil {
			sum.StartedAt = started[i]
		}
		out.Containers = append(out.Containers, sum)
	}
	return out, nil
}

// startInspections bounds the concurrent inspections of one container.list.
const startInspections = 8

// startTimesBudget bounds all start-time inspections of one
// container.list, well below the manager's request timeout. The Engine
// holds a container's lock while it stops or removes it, so on a loaded
// host one inspection can block for minutes; the list must not wait for it.
const startTimesBudget = 5 * time.Second

// startTimes returns the start time of each running container (the list
// entry has only the Engine's "Up 3 hours" text): one inspection per
// running container, at most startInspections at a time, all within
// startTimesBudget. A container that vanished, could not be inspected or
// was not inspected in time simply has none.
func startTimes(ctx context.Context, clk clock.Clock, eng engine.Engine, cs []engine.Container) []*time.Time {
	out := make([]*time.Time, len(cs))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := clk.NewTimer(startTimesBudget)
	defer budget.Stop()
	go func() {
		select {
		case <-budget.C():
			cancel()
		case <-ctx.Done():
		}
	}()
	sem := make(chan struct{}, startInspections)
	var wg sync.WaitGroup
	for i, c := range cs {
		if c.State != "running" && c.State != "paused" && c.State != "restarting" {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			d, err := eng.InspectContainer(ctx, c.ID)
			if err != nil || !d.State.Running {
				return
			}
			out[i] = timePtr(d.State.StartedAt)
		}()
	}
	wg.Wait()
	return out
}

// containerDetails converts an inspected container.
func (s *Service) containerDetails(d engine.ContainerDetails) protocol.ContainerDetails {
	st := d.State
	out := protocol.ContainerDetails{
		ContainerSummary: protocol.ContainerSummary{ID: d.ID, Name: d.Name, Image: d.Image, ImageID: d.ImageID,
			Command: strings.Join(append(slices.Clone(d.Entrypoint), d.Cmd...), " "), Created: d.Created.UTC(), State: st.Status,
			Status: st.Status, Labels: maps.Clone(d.Labels), Stack: s.stackOf(d.Labels), Mounts: mountsOf(d.Mounts)},
		Cmd: d.Cmd, Entrypoint: d.Entrypoint, WorkingDir: d.WorkingDir, User: d.User, Tty: d.Tty, Hostname: d.Hostname,
		RestartPolicy: d.RestartPolicy, RestartMaxRetries: d.RestartMaxRetries, NetworkMode: d.NetworkMode, RestartCount: d.RestartCount, Platform: d.Platform,
		Running: st.Running, Paused: st.Paused, OOMKilled: st.OOMKilled, ExitCode: st.ExitCode, Error: st.Error,
		FinishedAt: timePtr(st.FinishedAt),
		Resources: protocol.ResourcesSpec{NanoCPUs: d.Resources.NanoCPUs, CPUShares: d.Resources.CPUShares, Memory: d.Resources.Memory,
			MemorySwap: d.Resources.MemorySwap, PidsLimit: d.Resources.PidsLimit},
	}
	if st.Health != nil {
		out.Health = st.Health.Status
	}
	if h := d.Healthcheck; h != nil {
		out.Healthcheck = &protocol.HealthcheckSpec{Test: h.Test, Interval: h.Interval, Timeout: h.Timeout, StartPeriod: h.StartPeriod, Retries: h.Retries}
	}
	for _, p := range d.Ports {
		out.Ports = append(out.Ports, protocol.ContainerPort{ContainerPort: p.PrivatePort, HostPort: p.PublicPort, HostIP: p.HostIP, Protocol: p.Protocol})
	}
	out.StartedAt = timePtr(st.StartedAt)
	names := slices.Sorted(maps.Keys(d.Networks))
	for _, n := range names {
		ep := d.Networks[n]
		out.Networks = append(out.Networks, n)
		out.NetworkList = append(out.NetworkList, protocol.ContainerNetwork{Name: n, NetworkID: ep.NetworkID, IPAddress: ep.IPAddress,
			IPv6Address: ep.IPv6Address, MacAddress: ep.MacAddress, Aliases: ep.Aliases})
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() || t.Year() <= 1 {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Service) inspectContainer(ctx context.Context, eng engine.Engine, in protocol.ContainerInspectInput) (protocol.ContainerDetails, error) {
	if in.Container == "" {
		return protocol.ContainerDetails{}, &protocol.FieldError{Field: "container", Message: "required"}
	}
	d, err := eng.InspectContainer(ctx, in.Container)
	if err != nil {
		return protocol.ContainerDetails{}, err
	}
	set, err := s.protected(ctx, eng)
	if err != nil {
		return protocol.ContainerDetails{}, err
	}
	out := s.containerDetails(d)
	out.Protection = set.Container(d.ID)
	return out, nil
}

// usersByImage maps image IDs to the containers created from them.
func usersByImage(cs []engine.Container) map[string][]protocol.ContainerRef {
	out := map[string][]protocol.ContainerRef{}
	for _, c := range cs {
		out[c.ImageID] = append(out[c.ImageID], ref(c))
	}
	return out
}

func (s *Service) listImages(ctx context.Context, eng engine.Engine, _ protocol.ImageListInput) (protocol.ImageListOutput, error) {
	ims, err := eng.ListImages(ctx, false)
	if err != nil {
		return protocol.ImageListOutput{}, err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.ImageListOutput{}, err
	}
	users, set := usersByImage(cs), s.guard.Identify(ctx, eng, cs)
	out := protocol.ImageListOutput{Images: make([]protocol.ImageSummary, 0, len(ims))}
	for _, im := range ims {
		out.Images = append(out.Images, protocol.ImageSummary{ID: im.ID, RepoTags: realTags(im.RepoTags), RepoDigests: realTags(im.RepoDigests),
			Created: im.Created.UTC(), Size: im.Size, Labels: maps.Clone(im.Labels), UsedBy: users[im.ID], Protection: set.Image(im.ID)})
	}
	return out, nil
}

// realTags drops the "<none>:<none>" placeholders of untagged images.
func realTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if !strings.Contains(t, "<none>") {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) imageDetails(ctx context.Context, eng engine.Engine, im engine.ImageDetails) (protocol.ImageDetails, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.ImageDetails{}, err
	}
	return protocol.ImageDetails{
		ImageSummary: protocol.ImageSummary{ID: im.ID, RepoTags: realTags(im.RepoTags), RepoDigests: realTags(im.RepoDigests),
			Created: im.Created.UTC(), Size: im.Size, Labels: maps.Clone(im.Labels), UsedBy: usersByImage(cs)[im.ID],
			Protection: s.guard.Identify(ctx, eng, cs).Image(im.ID)},
		OS: im.OS, Architecture: im.Architecture, Variant: im.Variant, Author: im.Author, Entrypoint: im.Entrypoint, Cmd: im.Cmd,
		WorkingDir: im.WorkingDir, User: im.User, ExposedPorts: im.ExposedPorts, Volumes: im.Volumes, HasHealthTest: im.HasHealthTest,
	}, nil
}

func (s *Service) inspectImage(ctx context.Context, eng engine.Engine, in protocol.ImageInspectInput) (protocol.ImageDetails, error) {
	if !protocol.ValidImageID(in.Image) && !protocol.ValidImageReference(in.Image) {
		return protocol.ImageDetails{}, &protocol.FieldError{Field: "image", Message: "must be an image ID or reference"}
	}
	im, err := eng.InspectImage(ctx, in.Image)
	if err != nil {
		return protocol.ImageDetails{}, err
	}
	return s.imageDetails(ctx, eng, im)
}

// tagImage adds a tag (repository[:tag]) to an image. Moving an existing
// tag away from another image is what the Engine does; the caller's
// image.tag capability is checked on the source image by the manager.
func (s *Service) tagImage(ctx context.Context, eng engine.Engine, in protocol.ImageTagInput) (protocol.ImageDetails, error) {
	if !protocol.ValidImageID(in.Image) {
		return protocol.ImageDetails{}, &protocol.FieldError{Field: "image", Message: "must be an image ID"}
	}
	if err := protocol.ValidateTagTarget(in.Target); err != nil {
		return protocol.ImageDetails{}, err
	}
	if err := eng.TagImage(ctx, in.Image, in.Target); err != nil {
		return protocol.ImageDetails{}, err
	}
	im, err := eng.InspectImage(ctx, in.Image)
	if err != nil {
		return protocol.ImageDetails{}, err
	}
	return s.imageDetails(ctx, eng, im)
}

// usersByVolume maps volume names to the containers mounting them.
func usersByVolume(cs []engine.Container) map[string][]protocol.ContainerRef {
	out := map[string][]protocol.ContainerRef{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" && !slices.ContainsFunc(out[m.Name], func(r protocol.ContainerRef) bool { return r.ID == c.ID }) {
				out[m.Name] = append(out[m.Name], ref(c))
			}
		}
	}
	return out
}

func (s *Service) volumeInfo(v engine.Volume, users []protocol.ContainerRef, managed map[string]bool, set *protect.Set) protocol.VolumeInfo {
	return protocol.VolumeInfo{Name: v.Name, Driver: v.Driver, Scope: v.Scope, Created: v.CreatedAt.UTC(), Labels: maps.Clone(v.Labels),
		ComposeLabels: s.opts.VolumeLabels.Compose(v.Name, v.CreatedAt), Options: maps.Clone(v.Options), UsedBy: users,
		Stack: objectStack(v.Labels, managed), Protection: set.Volume(v.Name, v.Labels)}
}

func (s *Service) listVolumes(ctx context.Context, eng engine.Engine, _ protocol.VolumeListInput) (protocol.VolumeListOutput, error) {
	vs, err := eng.ListVolumes(ctx)
	if err != nil {
		return protocol.VolumeListOutput{}, err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.VolumeListOutput{}, err
	}
	users, managed, set := usersByVolume(cs), s.managedProjects(cs), s.guard.Identify(ctx, eng, cs)
	out := protocol.VolumeListOutput{Volumes: make([]protocol.VolumeInfo, 0, len(vs))}
	for _, v := range vs {
		out.Volumes = append(out.Volumes, s.volumeInfo(v, users[v.Name], managed, set))
	}
	return out, nil
}

func (s *Service) inspectVolume(ctx context.Context, eng engine.Engine, in protocol.VolumeInspectInput) (protocol.VolumeInfo, error) {
	if !protocol.ValidDockerName(in.Name) {
		return protocol.VolumeInfo{}, &protocol.FieldError{Field: "name", Message: "must be a volume name"}
	}
	v, err := eng.InspectVolume(ctx, in.Name)
	if err != nil {
		return protocol.VolumeInfo{}, err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.VolumeInfo{}, err
	}
	return s.volumeInfo(v, usersByVolume(cs)[v.Name], s.managedProjects(cs), s.guard.Identify(ctx, eng, cs)), nil
}

// volumeUsage serves volume.usage: the Engine's per-volume disk usage
// (sorted by name; -1 where the Engine does not know a size).
func (s *Service) volumeUsage(ctx context.Context, eng engine.Engine, _ protocol.VolumeUsageInput) (protocol.VolumeUsageOutput, error) {
	usage, err := eng.VolumeUsage(ctx)
	if err != nil {
		return protocol.VolumeUsageOutput{}, err
	}
	out := protocol.VolumeUsageOutput{Volumes: make([]protocol.VolumeUsage, 0, len(usage))}
	for _, name := range slices.Sorted(maps.Keys(usage)) {
		u := usage[name]
		out.Volumes = append(out.Volumes, protocol.VolumeUsage{Name: name, Size: u.Size, RefCount: u.RefCount})
	}
	return out, nil
}

// networkInfo converts a network. Attached containers are only known for
// an inspected network (the list does not report them) with their
// addresses on it; byID adds their names and states.
func networkInfo(n engine.Network, byID map[string]engine.Container, managed map[string]bool, set *protect.Set) protocol.NetworkInfo {
	out := protocol.NetworkInfo{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Attachable: n.Attachable,
		EnableIPv6: n.EnableIPv6, Created: n.Created.UTC(), Labels: maps.Clone(n.Labels), Subnets: n.Subnets, Gateways: n.Gateways,
		Builtin: slices.Contains(protocol.BuiltinNetworks, n.Name), Stack: objectStack(n.Labels, managed), Protection: set.Network(n.ID, n.Name, n.Labels)}
	ids := slices.Sorted(maps.Keys(n.Containers))
	for _, id := range ids {
		r := protocol.ContainerRef{ID: id}
		if c, ok := byID[id]; ok {
			r = ref(c)
		}
		ep := n.Containers[id]
		r.IPAddress, r.IPv6Address = ep.IPAddress, ep.IPv6Address
		out.Containers = append(out.Containers, r)
	}
	return out
}

func (s *Service) listNetworks(ctx context.Context, eng engine.Engine, _ protocol.NetworkListInput) (protocol.NetworkListOutput, error) {
	ns, err := eng.ListNetworks(ctx)
	if err != nil {
		return protocol.NetworkListOutput{}, err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.NetworkListOutput{}, err
	}
	managed, set := s.managedProjects(cs), s.guard.Identify(ctx, eng, cs)
	out := protocol.NetworkListOutput{Networks: make([]protocol.NetworkInfo, 0, len(ns))}
	for _, n := range ns {
		out.Networks = append(out.Networks, networkInfo(n, nil, managed, set))
	}
	sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].Name < out.Networks[j].Name })
	return out, nil
}

func (s *Service) inspectNetwork(ctx context.Context, eng engine.Engine, in protocol.NetworkInspectInput) (protocol.NetworkInfo, error) {
	if !protocol.ValidDockerName(in.Network) {
		return protocol.NetworkInfo{}, &protocol.FieldError{Field: "network", Message: "must be a network ID or name"}
	}
	n, err := eng.InspectNetwork(ctx, in.Network)
	if err != nil {
		return protocol.NetworkInfo{}, err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.NetworkInfo{}, err
	}
	byID := map[string]engine.Container{}
	for _, c := range cs {
		byID[c.ID] = c
	}
	return networkInfo(n, byID, s.managedProjects(cs), s.guard.Identify(ctx, eng, cs)), nil
}
