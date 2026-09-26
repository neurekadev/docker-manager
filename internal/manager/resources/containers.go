package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// ListContainers returns every container of the environment.
func (s *Service) ListContainers(ctx context.Context, env string) ([]protocol.ContainerSummary, error) {
	out, err := request[protocol.ContainerListOutput](ctx, s, env, protocol.ReqContainerList, protocol.ContainerListInput{})
	if err != nil {
		return nil, err
	}
	for _, c := range out.Containers {
		s.remember(catalog.TypeContainer, env, c.Name, c.Stack)
	}
	return out.Containers, nil
}

// InspectContainer returns a container by ID, unique ID prefix or name.
func (s *Service) InspectContainer(ctx context.Context, env, ref string) (protocol.ContainerDetails, error) {
	if ref == "" || len(ref) > 128 {
		return protocol.ContainerDetails{}, dockerErr(domain.DockerNotFound, "container not found")
	}
	d, err := request[protocol.ContainerDetails](ctx, s, env, protocol.ReqContainerInspect, protocol.ContainerInspectInput{Container: ref})
	if err != nil {
		return d, err
	}
	s.remember(catalog.TypeContainer, env, d.Name, d.Stack)
	return d, nil
}

// StackManaged reports whether a Compose project is a Docker Manager-managed
// stack: the agent found its working directory in a verified stack root, or
// the stack resolver (#7) knows it.
func (s *Service) StackManaged(ctx context.Context, env string, st *protocol.StackRef) bool {
	if st == nil {
		return false
	}
	if st.Managed {
		return true
	}
	if _, ok := s.StackIDs(ctx, env)[st.Project]; ok {
		return true
	}
	return s.retained(ctx, env, st.Project) != ""
}

// retained returns why Docker Manager keeps a Compose project no stack manages
// ("" when it does not): the stopped source of a migrated stack (#35).
// A failed lookup keeps the project (destructive routes refuse; retry).
func (s *Service) retained(ctx context.Context, env, project string) string {
	if s.opts.Retained == nil || project == "" {
		return ""
	}
	m, err := s.opts.Retained(ctx, env)
	if err != nil {
		s.log.Warn("could not list the retained sources of migrated stacks", "environment_id", env, "error", err)
		return "possibly part of the stopped source of a migrated stack (the check failed; try again)"
	}
	return m[project]
}

// managedRefusal refuses changing an object of a Docker Manager stack or of a
// migrated stack's retained source directly (call when StackManaged).
func (s *Service) managedRefusal(ctx context.Context, env, what string, st *protocol.StackRef) error {
	if !st.Managed {
		if _, ok := s.StackIDs(ctx, env)[st.Project]; !ok {
			if reason := s.retained(ctx, env, st.Project); reason != "" {
				return dockerErr(domain.DockerStackManaged,
					"%s is %s; confirm the removal from the source on the stack's migration to delete it", what, reason)
			}
		}
	}
	return stackRefused(what, st)
}

// StackIDs maps the environment's Compose projects to Docker Manager stack IDs
// (empty until #7 provides a resolver).
func (s *Service) StackIDs(ctx context.Context, env string) map[string]string {
	if s.opts.Stacks == nil {
		return nil
	}
	m, err := s.opts.Stacks.StackIDs(ctx, env)
	if err != nil {
		s.log.Warn("stack resolution failed", "environment_id", env, "error", err)
		return nil
	}
	return m
}

func stackRefused(what string, st *protocol.StackRef) error {
	return dockerErr(domain.DockerStackManaged,
		"%s belongs to the Docker Manager-managed stack %q; change the stack's Compose definition and redeploy it instead", what, st.Project)
}

// CreateContainer validates the create-container form and starts a
// container.create job. The recreate specification is saved (sealed) and
// the container labeled with its ID and the manager instance (#20).
func (s *Service) CreateContainer(ctx context.Context, p authz.Principal, env string, spec protocol.ContainerSpec, start bool, key string) (domain.Job, error) {
	if err := spec.Validate(); err != nil {
		return domain.Job{}, fieldErr("", err)
	}
	// The name must be free, the image present and the networks known:
	// refuse now rather than with a failed job.
	existing, err := s.ListContainers(ctx, env)
	if err != nil {
		return domain.Job{}, err
	}
	if slices.ContainsFunc(existing, func(c protocol.ContainerSummary) bool { return c.Name == spec.Name }) {
		return domain.Job{}, dockerErr(domain.DockerNameTaken, "a container named %q already exists", spec.Name)
	}
	if _, err := s.InspectImage(ctx, env, spec.Image); IsNotFound(err) {
		return domain.Job{}, invalid("image", "image %s is not present on this environment; pull it first", spec.Image)
	} else if err != nil {
		return domain.Job{}, err
	}
	if err := s.checkMounts(ctx, env, spec.Mounts); err != nil {
		return domain.Job{}, err
	}
	for i, n := range spec.Networks {
		if slices.Contains(protocol.BuiltinNetworks, n.Name) {
			continue
		}
		if _, err := s.InspectNetwork(ctx, env, n.Name); IsNotFound(err) {
			return domain.Job{}, invalid(fmt.Sprintf("networks[%d].name", i), "network %q does not exist on this environment", n.Name)
		} else if err != nil {
			return domain.Job{}, err
		}
	}
	id := specID(p, env, spec.Name, key)
	if id == "" {
		id = ids.New()
	}
	in := protocol.ContainerCreateInput{Spec: spec, Start: start, Ownership: map[string]string{
		protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: id}}
	if s.opts.InstanceID != "" {
		in.Ownership[protocol.LabelInstance] = s.opts.InstanceID
	}
	created, err := s.saveSpec(ctx, domain.ManagedContainer{ID: id, EnvironmentID: env, Name: spec.Name, Start: start}, spec)
	if err != nil {
		return domain.Job{}, err
	}
	j, _, err := s.enqueue(ctx, p, env, jobspec.ContainerCreate, []domain.JobTarget{{Type: domain.TargetContainer, ID: spec.Name}}, in, key)
	if err != nil {
		if created {
			_ = store.DeleteManagedContainer(context.WithoutCancel(ctx), s.opts.DB, id)
		}
		return domain.Job{}, err
	}
	if created {
		if err := store.SetManagedContainerJob(ctx, s.opts.DB, id, j.ID); err != nil {
			return j, err
		}
	}
	return j, nil
}

// saveSpec stores a new recreate specification; created is false when it
// exists already (a repeated idempotent request).
func (s *Service) saveSpec(ctx context.Context, m domain.ManagedContainer, spec protocol.ContainerSpec) (bool, error) {
	if _, _, err := store.GetManagedContainer(ctx, s.opts.DB, m.ID); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrManagedContainerNotFound) {
		return false, err
	}
	sealed, err := s.seal(m.ID, spec)
	if err != nil {
		return false, err
	}
	m.CreatedAt = s.clk.Now()
	return true, store.InsertManagedContainer(ctx, s.opts.DB, m, sealed)
}

func (s *Service) seal(id string, spec protocol.ContainerSpec) (string, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return s.opts.Keyring.Seal(b, "managed_containers/"+id+"/spec")
}

// ManagedSpec returns the saved recreate specification of a container
// created through Docker Manager, found by its spec label in env (nil when the
// container is not Docker Manager-managed). The spec includes environment
// values: callers must not return them.
func (s *Service) ManagedSpec(ctx context.Context, env string, labels map[string]string) (*domain.ManagedContainer, *protocol.ContainerSpec, error) {
	id := labels[protocol.LabelSpec]
	if id == "" || labels[protocol.LabelManaged] != protocol.ManagedStandalone {
		return nil, nil, nil
	}
	m, sealed, err := store.GetManagedContainer(ctx, s.opts.DB, id)
	if errors.Is(err, store.ErrManagedContainerNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if m.EnvironmentID != env {
		// A label copied to another Engine never reveals another
		// environment's specification.
		return nil, nil, nil
	}
	b, err := s.opts.Keyring.Open(sealed, "managed_containers/"+id+"/spec")
	if err != nil {
		return nil, nil, err
	}
	var spec protocol.ContainerSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, nil, err
	}
	return &m, &spec, nil
}

// ContainerAction starts a container lifecycle job (start, stop, restart,
// pause, unpause, remove) on the inspected container d.
func (s *Service) ContainerAction(ctx context.Context, p authz.Principal, env string, kind domain.JobKind, d protocol.ContainerDetails, in protocol.ContainerActionInput, key string) (domain.Job, error) {
	if err := s.checkContainer(d.ContainerSummary, kind, in.Confirmed); err != nil {
		return domain.Job{}, err
	}
	switch kind {
	case jobspec.ContainerStart, jobspec.ContainerStop, jobspec.ContainerRestart, jobspec.ContainerPause, jobspec.ContainerUnpause:
	case jobspec.ContainerRemove:
		if s.StackManaged(ctx, env, d.Stack) {
			return domain.Job{}, s.managedRefusal(ctx, env, "container "+d.Name, d.Stack)
		}
		if d.Running && !in.Force {
			return domain.Job{}, dockerErr(domain.DockerContainerRunning, "container %s is running; stop it first or remove it with force=true", d.Name)
		}
	default:
		return domain.Job{}, fmt.Errorf("resources: %s is not a container action", kind)
	}
	if in.TimeoutSeconds != nil && (*in.TimeoutSeconds < 0 || *in.TimeoutSeconds > 3600) {
		return domain.Job{}, invalid("timeoutSeconds", "must be between 0 and 3600")
	}
	in.Name, in.ID = d.Name, d.ID
	j, _, err := s.enqueue(ctx, p, env, kind, []domain.JobTarget{{Type: domain.TargetContainer, ID: d.Name}}, in, key)
	if err != nil {
		return domain.Job{}, err
	}
	if kind == jobspec.ContainerRemove {
		specLabel := d.Labels[protocol.LabelSpec]
		s.afterSuccess(j.ID, func(ctx context.Context) {
			s.forget(ctx, authz.ResourceRef{Type: catalog.TypeContainer, ID: d.Name, EnvironmentID: env})
			if m, _, err := store.GetManagedContainer(ctx, s.opts.DB, specLabel); err == nil && m.EnvironmentID == env {
				_ = store.DeleteManagedContainer(ctx, s.opts.DB, m.ID)
			}
		})
	}
	return j, nil
}

// UpdateContainer starts a container.update job with in-place settings
// (restart policy, resource limits). The saved recreate specification of a
// Docker Manager-managed container is updated at the same time.
func (s *Service) UpdateContainer(ctx context.Context, p authz.Principal, env string, d protocol.ContainerDetails, in protocol.ContainerUpdateInput, key string) (domain.Job, error) {
	if err := s.checkContainer(d.ContainerSummary, "container.update", false); err != nil {
		return domain.Job{}, err
	}
	if s.StackManaged(ctx, env, d.Stack) {
		return domain.Job{}, s.managedRefusal(ctx, env, "container "+d.Name, d.Stack)
	}
	if !protocol.ValidRestartPolicy(in.RestartPolicy) {
		return domain.Job{}, invalid("restartPolicy", "must be no, always, on-failure or unless-stopped")
	}
	if in.Resources != nil {
		if err := in.Resources.Validate("resources"); err != nil {
			return domain.Job{}, fieldErr("", err)
		}
	}
	if in.RestartPolicy == "" && in.Resources == nil {
		return domain.Job{}, invalid("", "nothing to change: set restartPolicy and/or resources")
	}
	in.Name, in.ID = d.Name, d.ID
	j, _, err := s.enqueue(ctx, p, env, jobspec.ContainerUpdate, []domain.JobTarget{{Type: domain.TargetContainer, ID: d.Name}}, in, key)
	if err != nil {
		return domain.Job{}, err
	}
	if m, spec, err := s.ManagedSpec(ctx, env, d.Labels); err == nil && m != nil {
		if in.RestartPolicy != "" {
			spec.RestartPolicy = in.RestartPolicy
		}
		if in.Resources != nil {
			spec.Resources = *in.Resources
		}
		if sealed, err := s.seal(m.ID, *spec); err == nil {
			if err := store.UpdateManagedContainerSpec(ctx, s.opts.DB, m.ID, sealed, s.clk.Now()); err != nil {
				s.log.Warn("could not update a recreate specification", "spec_id", m.ID, "error", err)
			}
		}
	}
	return j, nil
}
