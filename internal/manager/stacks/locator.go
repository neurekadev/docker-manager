package stacks

import (
	"context"
	"errors"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/permissions"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// StackIDs maps the Compose project names of an environment's stacks to
// their stack IDs: the Docker resource service (#6) places containers,
// volumes and networks of these projects in their stack and refuses direct
// edits of them (stack_managed).
func (s *Service) StackIDs(ctx context.Context, environmentID string) (map[string]string, error) {
	list, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: environmentID})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, st := range list {
		out[st.Name] = st.ID
	}
	return out, nil
}

// Locator places stacks in the resource graph (#17): a stack lives in its
// environment and has no parents. Stack-scoped rules follow the stack when
// its environment changes (#35) because they name the stack ID.
func (s *Service) Locator() permissions.Locator {
	return permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		st, err := store.GetStack(ctx, s.db, ref.ID)
		if errors.Is(err, domain.ErrStackNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, EnvironmentID: st.EnvironmentID, Parents: []authz.ResourceRef{}}, nil
	})
}
