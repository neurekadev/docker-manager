package resources

import (
	"context"
	"encoding/json"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// SpecRef is an object a saved recreate specification references (#6).
type SpecRef struct {
	// Kind is "image", "volume" or "network".
	Kind string
	Ref  string
	// Container is the name of the container the specification recreates.
	Container string
}

// ManagedSpecRefs returns the images, named volumes and networks the saved
// recreate specifications of an environment's Docker Manager-managed standalone
// containers reference; prune policies (#14) protect them so an automatic
// update (#20) can recreate the container. Environment values of the
// specifications are never returned.
func (s *Service) ManagedSpecRefs(ctx context.Context, env string) ([]SpecRef, error) {
	ms, err := store.ListManagedContainers(ctx, s.opts.DB, env)
	if err != nil {
		return nil, err
	}
	var out []SpecRef
	for _, m := range ms {
		_, sealed, err := store.GetManagedContainer(ctx, s.opts.DB, m.ID)
		if err != nil {
			return nil, err
		}
		b, err := s.opts.Keyring.Open(sealed, "managed_containers/"+m.ID+"/spec")
		if err != nil {
			return nil, err
		}
		var spec protocol.ContainerSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, err
		}
		if spec.Image != "" {
			out = append(out, SpecRef{Kind: "image", Ref: spec.Image, Container: m.Name})
		}
		for _, mt := range spec.Mounts {
			if mt.Type == "volume" && mt.Source != "" {
				out = append(out, SpecRef{Kind: "volume", Ref: mt.Source, Container: m.Name})
			}
		}
		for _, n := range spec.Networks {
			if n.Name != "" {
				out = append(out, SpecRef{Kind: "network", Ref: n.Name, Container: m.Name})
			}
		}
	}
	return out, nil
}
