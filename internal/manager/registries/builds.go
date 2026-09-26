package registries

import (
	"context"
	"sort"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
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

// Usable checks that explicitly named connections exist and are active.
func (s *Service) Usable(ctx context.Context, ids []string) error {
	for _, id := range ids {
		c, err := store.GetRegistryConnection(ctx, s.db, id)
		if err != nil {
			return err
		}
		if !c.Active() {
			return domain.ErrRegistryConnectionRevoked
		}
	}
	return nil
}
