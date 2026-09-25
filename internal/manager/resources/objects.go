package resources

import (
	"context"
	"slices"
	"strings"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Images.

// ListImages returns every image of the environment with its users.
func (s *Service) ListImages(ctx context.Context, env string) ([]protocol.ImageSummary, error) {
	out, err := request[protocol.ImageListOutput](ctx, s, env, protocol.ReqImageList, protocol.ImageListInput{})
	return out.Images, err
}

// InspectImage returns an image by ID or reference.
func (s *Service) InspectImage(ctx context.Context, env, ref string) (protocol.ImageDetails, error) {
	if !protocol.ValidImageID(ref) && !protocol.ValidImageReference(ref) {
		return protocol.ImageDetails{}, dockerErr(domain.DockerNotFound, "image not found")
	}
	return request[protocol.ImageDetails](ctx, s, env, protocol.ReqImageInspect, protocol.ImageInspectInput{Image: ref})
}

// TagImage adds repository[:tag] to the image (a bounded, mutating agent
// request: never retried automatically).
func (s *Service) TagImage(ctx context.Context, env string, im protocol.ImageDetails, target string) (protocol.ImageDetails, error) {
	if err := protocol.ValidateTagTarget(target); err != nil {
		return protocol.ImageDetails{}, invalid("repository", "must be repository[:tag] without a digest, e.g. registry.example.com/team/app:1.2")
	}
	return request[protocol.ImageDetails](ctx, s, env, protocol.ReqImageTag, protocol.ImageTagInput{Image: im.ID, Target: target})
}

// PullImage starts an image.pull job. The registry connection is selected
// by the #19 resolver (explicit connectionID or the matching one);
// without a resolver only anonymous pulls are possible.
func (s *Service) PullImage(ctx context.Context, p authz.Principal, env string, in protocol.ImagePullInput, connectionID, key string) (domain.Job, error) {
	if err := in.Validate(); err != nil {
		return domain.Job{}, fieldErr("", err)
	}
	switch {
	case s.opts.Registries != nil:
		sel, err := s.opts.Registries.Select(ctx, domain.RegistrySelectRequest{Reference: in.Reference, EnvironmentID: env, ConnectionID: connectionID})
		if err != nil {
			return domain.Job{}, err
		}
		if sel.Selected != nil {
			in.RegistryConnections = []string{sel.Selected.ID}
		}
	case connectionID != "":
		return domain.Job{}, invalid("registryConnectionId", "registry connections are not available on this manager")
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.ImagePull, []domain.JobTarget{{Type: domain.TargetImage, ID: in.Reference}}, in, key)
	return j, err
}

// RemoveImage starts an image.remove job for an unused image.
func (s *Service) RemoveImage(ctx context.Context, p authz.Principal, env string, im protocol.ImageDetails, force bool, key string) (domain.Job, error) {
	if len(im.UsedBy) > 0 {
		return domain.Job{}, dockerErr(domain.DockerImageInUse, "image is used by %d container(s): %s; remove them first", len(im.UsedBy), names(im.UsedBy))
	}
	if len(im.RepoTags) > 1 && !force {
		return domain.Job{}, dockerErr(domain.DockerConflict, "image has %d tags (%s); remove it with force=true to remove every tag",
			len(im.RepoTags), strings.Join(im.RepoTags, ", "))
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.ImageRemove, []domain.JobTarget{{Type: domain.TargetImage, ID: im.ID}},
		protocol.ImageRemoveInput{Image: im.ID, Force: force}, key)
	if err != nil {
		return domain.Job{}, err
	}
	s.afterSuccess(j.ID, func(ctx context.Context) {
		s.forget(ctx, authz.ResourceRef{Type: catalog.TypeImage, ID: im.ID, EnvironmentID: env})
	})
	return j, nil
}

func names(cs []protocol.ContainerRef) string {
	n := make([]string, 0, len(cs))
	for _, c := range cs {
		n = append(n, c.Name)
	}
	slices.Sort(n)
	if len(n) > 5 {
		return strings.Join(n[:5], ", ") + " and more"
	}
	return strings.Join(n, ", ")
}

// Volumes.

// ListVolumes returns every volume of the environment with its users.
func (s *Service) ListVolumes(ctx context.Context, env string) ([]protocol.VolumeInfo, error) {
	out, err := request[protocol.VolumeListOutput](ctx, s, env, protocol.ReqVolumeList, protocol.VolumeListInput{})
	if err != nil {
		return nil, err
	}
	for _, v := range out.Volumes {
		s.remember(catalog.TypeVolume, env, v.Name, v.Stack)
	}
	return out.Volumes, nil
}

// InspectVolume returns a volume by name.
func (s *Service) InspectVolume(ctx context.Context, env, name string) (protocol.VolumeInfo, error) {
	if !protocol.ValidDockerName(name) {
		return protocol.VolumeInfo{}, dockerErr(domain.DockerNotFound, "volume not found")
	}
	v, err := request[protocol.VolumeInfo](ctx, s, env, protocol.ReqVolumeInspect, protocol.VolumeInspectInput{Name: name})
	if err == nil {
		s.remember(catalog.TypeVolume, env, v.Name, v.Stack)
	}
	return v, err
}

// CreateVolume starts a volume.create job for a new name.
func (s *Service) CreateVolume(ctx context.Context, p authz.Principal, env string, in protocol.VolumeCreateInput, key string) (domain.Job, error) {
	if err := in.Validate(); err != nil {
		return domain.Job{}, fieldErr("", err)
	}
	if _, err := s.InspectVolume(ctx, env, in.Name); err == nil {
		return domain.Job{}, dockerErr(domain.DockerNameTaken, "a volume named %q already exists", in.Name)
	} else if !IsNotFound(err) {
		return domain.Job{}, err
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.VolumeCreate, []domain.JobTarget{{Type: domain.TargetVolume, ID: in.Name}}, in, key)
	return j, err
}

// RemoveVolume starts a volume.remove job for an unused volume outside any
// DockYard-managed stack. Its data is deleted permanently.
func (s *Service) RemoveVolume(ctx context.Context, p authz.Principal, env string, v protocol.VolumeInfo, key string) (domain.Job, error) {
	if s.StackManaged(ctx, env, v.Stack) {
		return domain.Job{}, stackRefused("volume "+v.Name, v.Stack)
	}
	if len(v.UsedBy) > 0 {
		return domain.Job{}, dockerErr(domain.DockerVolumeInUse, "volume is used by %d container(s): %s; remove them first", len(v.UsedBy), names(v.UsedBy))
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.VolumeRemove, []domain.JobTarget{{Type: domain.TargetVolume, ID: v.Name}},
		protocol.VolumeRemoveInput{Name: v.Name}, key)
	if err != nil {
		return domain.Job{}, err
	}
	s.afterSuccess(j.ID, func(ctx context.Context) {
		s.forget(ctx, authz.ResourceRef{Type: catalog.TypeVolume, ID: v.Name, EnvironmentID: env})
	})
	return j, nil
}

// Networks.

// ListNetworks returns every network of the environment.
func (s *Service) ListNetworks(ctx context.Context, env string) ([]protocol.NetworkInfo, error) {
	out, err := request[protocol.NetworkListOutput](ctx, s, env, protocol.ReqNetworkList, protocol.NetworkListInput{})
	if err != nil {
		return nil, err
	}
	for _, n := range out.Networks {
		s.remember(catalog.TypeNetwork, env, n.Name, n.Stack)
	}
	return out.Networks, nil
}

// InspectNetwork returns a network by ID or name with its containers.
func (s *Service) InspectNetwork(ctx context.Context, env, ref string) (protocol.NetworkInfo, error) {
	if !protocol.ValidDockerName(ref) {
		return protocol.NetworkInfo{}, dockerErr(domain.DockerNotFound, "network not found")
	}
	n, err := request[protocol.NetworkInfo](ctx, s, env, protocol.ReqNetworkInspect, protocol.NetworkInspectInput{Network: ref})
	if err == nil {
		s.remember(catalog.TypeNetwork, env, n.Name, n.Stack)
	}
	return n, err
}

// CreateNetwork starts a network.create job for a new name.
func (s *Service) CreateNetwork(ctx context.Context, p authz.Principal, env string, in protocol.NetworkCreateInput, key string) (domain.Job, error) {
	if err := in.Validate(); err != nil {
		return domain.Job{}, fieldErr("", err)
	}
	existing, err := s.ListNetworks(ctx, env)
	if err != nil {
		return domain.Job{}, err
	}
	if slices.ContainsFunc(existing, func(n protocol.NetworkInfo) bool { return n.Name == in.Name }) {
		return domain.Job{}, dockerErr(domain.DockerNameTaken, "a network named %q already exists", in.Name)
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.NetworkCreate, []domain.JobTarget{{Type: domain.TargetNetwork, ID: in.Name}}, in, key)
	return j, err
}

// RemoveNetwork starts a network.remove job for an unused, user-defined
// network outside any DockYard-managed stack.
func (s *Service) RemoveNetwork(ctx context.Context, p authz.Principal, env string, n protocol.NetworkInfo, key string) (domain.Job, error) {
	if n.Builtin {
		return domain.Job{}, dockerErr(domain.DockerNetworkBuiltin, "%s is a predefined network and cannot be removed", n.Name)
	}
	if s.StackManaged(ctx, env, n.Stack) {
		return domain.Job{}, stackRefused("network "+n.Name, n.Stack)
	}
	if len(n.Containers) > 0 {
		return domain.Job{}, dockerErr(domain.DockerNetworkInUse, "network has %d attached container(s): %s; disconnect or remove them first",
			len(n.Containers), names(n.Containers))
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.NetworkRemove, []domain.JobTarget{{Type: domain.TargetNetwork, ID: n.Name}},
		protocol.NetworkRemoveInput{Name: n.Name, ID: n.ID}, key)
	if err != nil {
		return domain.Job{}, err
	}
	s.afterSuccess(j.ID, func(ctx context.Context) {
		s.forget(ctx, authz.ResourceRef{Type: catalog.TypeNetwork, ID: n.Name, EnvironmentID: env})
	})
	return j, nil
}
