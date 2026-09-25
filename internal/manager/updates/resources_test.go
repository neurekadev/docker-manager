package updates_test

import (
	"context"
	"sync"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/protection"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// fakeResources is the manager's Docker resource service over the
// in-memory Engine (what the agent's container requests would answer).
type fakeResources struct {
	eng   *enginefake.Engine
	mu    sync.Mutex
	specs map[string]protocol.ContainerSpec
}

func summary(c engine.ContainerDetails) protocol.ContainerSummary {
	s := protocol.ContainerSummary{ID: c.ID, Name: c.Name, Image: c.Image, ImageID: c.ImageID, State: c.State.Status, Labels: c.Labels}
	if p := c.Labels[lifecycle.ComposeProjectLabel]; p != "" {
		s.Stack = &protocol.StackRef{Project: p, Service: c.Labels[lifecycle.ComposeServiceLabel]}
	}
	return s
}

func (f *fakeResources) ListContainers(ctx context.Context, _ string) ([]protocol.ContainerSummary, error) {
	list, err := f.eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, err
	}
	var out []protocol.ContainerSummary
	for _, c := range list {
		d, err := f.eng.InspectContainer(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, summary(d))
	}
	return out, nil
}

func (f *fakeResources) InspectContainer(ctx context.Context, _, ref string) (protocol.ContainerDetails, error) {
	d, err := f.eng.InspectContainer(ctx, ref)
	if engine.IsCode(err, engine.CodeNotFound) {
		return protocol.ContainerDetails{}, &domain.DockerError{Code: domain.DockerNotFound, Message: "container not found"}
	}
	if err != nil {
		return protocol.ContainerDetails{}, err
	}
	return protocol.ContainerDetails{ContainerSummary: summary(d), Running: d.State.Running}, nil
}

func (f *fakeResources) InspectImage(ctx context.Context, _, ref string) (protocol.ImageDetails, error) {
	im, err := f.eng.InspectImage(ctx, ref)
	if err != nil {
		return protocol.ImageDetails{}, &domain.DockerError{Code: domain.DockerNotFound, Message: err.Error()}
	}
	return protocol.ImageDetails{ImageSummary: protocol.ImageSummary{ID: im.ID, RepoTags: im.RepoTags, RepoDigests: im.RepoDigests},
		OS: im.OS, Architecture: im.Architecture, Variant: im.Variant}, nil
}

func (f *fakeResources) ManagedSpec(_ context.Context, env string, labels map[string]string) (*domain.ManagedContainer, *protocol.ContainerSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := labels[protocol.LabelSpec]
	spec, ok := f.specs[id]
	if !ok || labels[protocol.LabelManaged] != protocol.ManagedStandalone {
		return nil, nil, nil
	}
	return &domain.ManagedContainer{ID: id, EnvironmentID: env, Name: spec.Name}, &spec, nil
}

func (f *fakeResources) ContainerProtection(c protocol.ContainerSummary) *protocol.Protection {
	if role := c.Labels[protocol.LabelRole]; role != "" {
		return &protocol.Protection{Role: role, Reason: "a DockYard " + role + " container"}
	}
	return nil
}

func (f *fakeResources) ProjectProtection(ctx context.Context, env, project string) (*protocol.Protection, error) {
	cs, err := f.ListContainers(ctx, env)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c.Stack != nil && c.Stack.Project == project && f.ContainerProtection(c) != nil {
			return &protocol.Protection{Role: protection.RoleProject, Reason: "DockYard's own Compose project " + project}, nil
		}
	}
	return nil, nil
}

// standalone creates a DockYard-managed standalone container from spec
// (ownership labels and a saved specification) and returns its ID.
func (f *fakeResources) standalone(ctx context.Context, spec protocol.ContainerSpec, running bool) string {
	f.mu.Lock()
	specID := "spec-" + spec.Name
	f.specs[specID] = spec
	f.mu.Unlock()
	labels := map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: specID}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	id := f.eng.AddContainer(engine.ContainerSpec{Name: spec.Name, Image: spec.Image, Env: spec.Env, Labels: labels}, false)
	if running {
		if err := f.eng.StartContainer(ctx, id); err != nil {
			panic(err)
		}
	}
	return id
}
