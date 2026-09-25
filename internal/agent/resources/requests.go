package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/protocol"
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
		protocol.ReqNetworkList:      handle(s, s.listNetworks),
		protocol.ReqNetworkInspect:   handle(s, s.inspectNetwork),
	}
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

func (s *Service) listContainers(ctx context.Context, eng engine.Engine, _ protocol.ContainerListInput) (protocol.ContainerListOutput, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return protocol.ContainerListOutput{}, err
	}
	out := protocol.ContainerListOutput{Containers: make([]protocol.ContainerSummary, 0, len(cs))}
	for _, c := range cs {
		out.Containers = append(out.Containers, s.containerSummary(c))
	}
	return out, nil
}

// containerDetails converts an inspected container.
func (s *Service) containerDetails(d engine.ContainerDetails) protocol.ContainerDetails {
	st := d.State
	out := protocol.ContainerDetails{
		ContainerSummary: protocol.ContainerSummary{ID: d.ID, Name: d.Name, Image: d.Image, ImageID: d.ImageID,
			Command: strings.Join(append(slices.Clone(d.Entrypoint), d.Cmd...), " "), Created: d.Created.UTC(), State: st.Status,
			Status: st.Status, Labels: maps.Clone(d.Labels), Stack: s.stackOf(d.Labels), Mounts: mountsOf(d.Mounts)},
		Cmd: d.Cmd, Entrypoint: d.Entrypoint, WorkingDir: d.WorkingDir, User: d.User, Tty: d.Tty, Hostname: d.Hostname,
		RestartPolicy: d.RestartPolicy, NetworkMode: d.NetworkMode, RestartCount: d.RestartCount, Platform: d.Platform,
		Running: st.Running, Paused: st.Paused, OOMKilled: st.OOMKilled, ExitCode: st.ExitCode, Error: st.Error,
		StartedAt: timePtr(st.StartedAt), FinishedAt: timePtr(st.FinishedAt),
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
	return s.containerDetails(d), nil
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
	users := usersByImage(cs)
	out := protocol.ImageListOutput{Images: make([]protocol.ImageSummary, 0, len(ims))}
	for _, im := range ims {
		out.Images = append(out.Images, protocol.ImageSummary{ID: im.ID, RepoTags: realTags(im.RepoTags), RepoDigests: realTags(im.RepoDigests),
			Created: im.Created.UTC(), Size: im.Size, Labels: maps.Clone(im.Labels), UsedBy: users[im.ID]})
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
			Created: im.Created.UTC(), Size: im.Size, Labels: maps.Clone(im.Labels), UsedBy: usersByImage(cs)[im.ID]},
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

func volumeInfo(v engine.Volume, users []protocol.ContainerRef, managed map[string]bool) protocol.VolumeInfo {
	return protocol.VolumeInfo{Name: v.Name, Driver: v.Driver, Scope: v.Scope, Created: v.CreatedAt.UTC(), Labels: maps.Clone(v.Labels),
		Options: maps.Clone(v.Options), UsedBy: users, Stack: objectStack(v.Labels, managed)}
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
	users, managed := usersByVolume(cs), s.managedProjects(cs)
	out := protocol.VolumeListOutput{Volumes: make([]protocol.VolumeInfo, 0, len(vs))}
	for _, v := range vs {
		out.Volumes = append(out.Volumes, volumeInfo(v, users[v.Name], managed))
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
	return volumeInfo(v, usersByVolume(cs)[v.Name], s.managedProjects(cs)), nil
}

// networkInfo converts a network. Attached containers are only known for
// an inspected network (the list does not report them); byID adds their
// names and states.
func networkInfo(n engine.Network, byID map[string]engine.Container, managed map[string]bool) protocol.NetworkInfo {
	out := protocol.NetworkInfo{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Attachable: n.Attachable,
		EnableIPv6: n.EnableIPv6, Created: n.Created.UTC(), Labels: maps.Clone(n.Labels), Subnets: n.Subnets, Gateways: n.Gateways,
		Builtin: slices.Contains(protocol.BuiltinNetworks, n.Name), Stack: objectStack(n.Labels, managed)}
	ids := slices.Sorted(maps.Keys(n.Containers))
	for _, id := range ids {
		if c, ok := byID[id]; ok {
			out.Containers = append(out.Containers, ref(c))
		} else {
			out.Containers = append(out.Containers, protocol.ContainerRef{ID: id})
		}
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
	managed := s.managedProjects(cs)
	out := protocol.NetworkListOutput{Networks: make([]protocol.NetworkInfo, 0, len(ns))}
	for _, n := range ns {
		out.Networks = append(out.Networks, networkInfo(n, nil, managed))
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
	return networkInfo(n, byID, s.managedProjects(cs)), nil
}
