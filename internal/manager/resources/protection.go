package resources

import (
	"context"
	"errors"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Self-protection on the manager side (#32). The agent annotates its
// inventory with protocol.Protection and refuses on its own; the manager
// refuses first, from the annotation and from what it knows itself: the
// Docker Manager role labels of the documented compose.yaml and its own container ID.
// Other workstreams call these helpers (and internal/protection) to leave
// Docker Manager's resources out of prune candidates (#14), automatic updates
// (#20), backup/restore shutdown plans (#10), bulk selections and
// migrations (#35), and to refuse deploy/down/stop of Docker Manager's own Compose
// project (#7).

// ContainerProtection returns the effective protection of a container of
// the inventory (nil: not protected).
func (s *Service) ContainerProtection(c protocol.ContainerSummary) *protocol.Protection {
	if c.Protection != nil {
		return c.Protection
	}
	switch {
	case s.opts.ManagerContainerID != "" && c.ID == s.opts.ManagerContainerID:
		return &protocol.Protection{Role: protection.RoleManager, Reason: "the Docker Manager of this installation: the UI and API run in it",
			Self: true, RestartAllowed: true}
	case protocol.HasRole(c.Labels, protection.RoleAgent):
		return &protocol.Protection{Role: protection.RoleAgent, Reason: "a Docker Agent container", RestartAllowed: true}
	case protocol.HasRole(c.Labels, protection.RoleManager):
		return &protocol.Protection{Role: protection.RoleManager, Reason: "a Docker Manager container", RestartAllowed: true}
	}
	return nil
}

// ProjectProtection returns the protection of a Compose project of the
// environment: Docker Manager's own project (a Docker Manager container belongs to it)
// is protected; #7 refuses deploy, down and stop on it with
// protection.Check(p, protection.Deploy|Down|Stop, false).
func (s *Service) ProjectProtection(ctx context.Context, env, project string) (*protocol.Protection, error) {
	cs, err := s.ListContainers(ctx, env)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c.Stack == nil || c.Stack.Project != project {
			continue
		}
		if p := s.ContainerProtection(c); p != nil {
			return &protocol.Protection{Role: protection.RoleProject, Reason: "Docker Manager's own Compose project " + project}, nil
		}
	}
	return nil, nil
}

// ProtectedContainers returns the environment's containers split into
// those bulk operations may touch and Docker Manager's own (with reasons): the
// exclusion list of prune, update, backup shutdown and bulk selections.
func (s *Service) ProtectedContainers(ctx context.Context, env string) (kept []protocol.ContainerSummary,
	excluded []protection.Exclusion[protocol.ContainerSummary], err error) {
	cs, err := s.ListContainers(ctx, env)
	if err != nil {
		return nil, nil, err
	}
	kept, excluded = protection.Filter(cs, s.ContainerProtection)
	return kept, excluded, nil
}

// refusal converts a protection refusal to a Docker error.
func refusal(err error) error {
	var r *protection.Refusal
	if errors.As(err, &r) {
		code := domain.DockerProtected
		if r.Code == protection.CodeConfirmationRequired {
			code = domain.DockerConfirmationRequired
		}
		return &domain.DockerError{Code: code, Message: r.Reason}
	}
	return err
}

var containerActions = map[domain.JobKind]protection.Action{
	"container.start": protection.Start, "container.stop": protection.Stop, "container.restart": protection.Restart,
	"container.pause": protection.Pause, "container.unpause": protection.Unpause, "container.remove": protection.Remove,
	"container.update": protection.Update, "container.recreate": protection.Update,
}

// checkContainer refuses an action on one of Docker Manager's own containers.
func (s *Service) checkContainer(c protocol.ContainerSummary, kind domain.JobKind, confirmed bool) error {
	return refusal(protection.Check(s.ContainerProtection(c), containerActions[kind], confirmed))
}

// checkMounts refuses mounting Docker Manager's own volumes into a new container.
func (s *Service) checkMounts(ctx context.Context, env string, mounts []protocol.MountSpec) error {
	for _, m := range mounts {
		if m.Type != "volume" || m.Source == "" {
			continue
		}
		v, err := s.InspectVolume(ctx, env, m.Source)
		if IsNotFound(err) {
			continue // created by the Engine on first use
		}
		if err != nil {
			return err
		}
		if err := protection.Check(v.Protection, protection.Mount, false); err != nil {
			return refusal(err)
		}
	}
	return nil
}
