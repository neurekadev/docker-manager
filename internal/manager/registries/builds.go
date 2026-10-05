package registries

import (
	"context"
	"sort"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// BuildCredentials returns the registry connections offered to a build in
// an environment for its base images (#33), whose references are only
// known inside BuildKit: per registry host, the host-wide connection (no
// repository matcher) that applies in the environment, chosen like Match
// (environment binding over unbound, then priority). Hosts where the
// choice ties, and revoked winners, are left out (ambiguous lists the
// tied hosts); name connections explicitly to use them. Stack-bound and
// repository-specific connections are only used when named.
func (s *Service) BuildCredentials(ctx context.Context, environmentID string) (ids, ambiguous []string, err error) {
	conns, err := store.ListRegistryConnections(ctx, s.db, "", "", 0)
	if err != nil {
		return nil, nil, err
	}
	byHost := map[string][]domain.RegistryCandidate{}
	for _, c := range conns {
		if c.RepositoryPattern != "" || c.StackID != "" {
			continue
		}
		binding := bindingNone
		if c.EnvironmentID != "" {
			if c.EnvironmentID != environmentID {
				continue
			}
			binding = bindingEnvironment
		}
		byHost[c.Host] = append(byHost[c.Host], domain.RegistryCandidate{Connection: c, Binding: binding})
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		cands := byHost[h]
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if a.Binding != b.Binding {
				return a.Binding > b.Binding
			}
			if a.Connection.Priority != b.Connection.Priority {
				return a.Connection.Priority > b.Connection.Priority
			}
			return a.Connection.ID < b.Connection.ID
		})
		if len(cands) > 1 && cands[1].Binding == cands[0].Binding && cands[1].Connection.Priority == cands[0].Connection.Priority {
			ambiguous = append(ambiguous, h)
			continue
		}
		if cands[0].Connection.Active() {
			ids = append(ids, cands[0].Connection.ID)
		}
	}
	return ids, ambiguous, nil
}

// Usable checks that explicitly named connections exist, are active and
// may be used in the build's context, like Match's binding rule: an
// environment-bound connection only in its environment, a stack-bound one
// only for its stack (stackID is empty for builds outside a stack).
func (s *Service) Usable(ctx context.Context, ids []string, environmentID, stackID string) error {
	for _, id := range ids {
		c, err := store.GetRegistryConnection(ctx, s.db, id)
		if err != nil {
			return err
		}
		switch {
		case c.StackID != "":
			if c.StackID != stackID {
				return domain.ErrRegistryConnectionMismatch
			}
		case c.EnvironmentID != "":
			if c.EnvironmentID != environmentID {
				return domain.ErrRegistryConnectionMismatch
			}
		}
		if !c.Active() {
			return domain.ErrRegistryConnectionRevoked
		}
	}
	return nil
}
