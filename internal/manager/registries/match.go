package registries

import (
	"sort"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/imageref"
)

// Binding specificities (domain.RegistryCandidate.Binding).
const (
	bindingNone        = 0
	bindingEnvironment = 1
	bindingStack       = 2
)

// Match is the deterministic matching rule (#19). A connection is a
// candidate for a reference when
//
//  1. its host equals the reference's normalized host (every Docker Hub
//     alias is docker.io; host:port must match exactly, so a self-hosted
//     registry on another port is another registry);
//  2. its repository matcher matches the repository (empty matches all);
//  3. its binding allows the context: a stack-bound connection only for
//     that stack, an environment-bound one only in that environment.
//
// Candidates are ordered by binding (stack > environment > none), then
// matcher specificity (exact repository > deeper namespace > shallower
// namespace > any), then priority (higher first), then ID. The first
// candidate is selected unless the second one ties with it on binding,
// specificity and priority: then the match is ambiguous and the request
// must name a connection explicitly. An explicit ConnectionID must be one
// of the candidates. Revoked connections stay candidates (a revoked
// selection fails; it never falls back to another credential or to
// anonymous access).
func Match(conns []domain.RegistryConnection, ref imageref.Ref, req domain.RegistrySelectRequest) (domain.RegistrySelection, []string, error) {
	sel := domain.RegistrySelection{Reference: ref.String(), Host: ref.Host, Repository: ref.Repository}
	for _, c := range conns {
		if c.Host != ref.Host {
			continue
		}
		ok, spec := imageref.Pattern(c.RepositoryPattern).Match(ref.Repository)
		if !ok {
			continue
		}
		binding := bindingNone
		switch {
		case c.StackID != "":
			if c.StackID != req.StackID {
				continue
			}
			binding = bindingStack
		case c.EnvironmentID != "":
			if c.EnvironmentID != req.EnvironmentID {
				continue
			}
			binding = bindingEnvironment
		}
		sel.Candidates = append(sel.Candidates, domain.RegistryCandidate{Connection: c, Binding: binding, Pattern: spec})
	}
	sort.SliceStable(sel.Candidates, func(i, j int) bool {
		a, b := sel.Candidates[i], sel.Candidates[j]
		if a.Binding != b.Binding {
			return a.Binding > b.Binding
		}
		if a.Pattern != b.Pattern {
			return a.Pattern > b.Pattern
		}
		if a.Connection.Priority != b.Connection.Priority {
			return a.Connection.Priority > b.Connection.Priority
		}
		return a.Connection.ID < b.Connection.ID
	})
	if req.ConnectionID != "" {
		for _, cand := range sel.Candidates {
			if cand.Connection.ID == req.ConnectionID {
				c := cand.Connection
				sel.Selected, sel.Explicit = &c, true
				return sel, nil, nil
			}
		}
		return sel, nil, domain.ErrRegistryConnectionMismatch
	}
	if len(sel.Candidates) == 0 {
		return sel, nil, nil
	}
	top := sel.Candidates[0]
	var tied []string
	for _, cand := range sel.Candidates {
		if cand.Binding == top.Binding && cand.Pattern == top.Pattern && cand.Connection.Priority == top.Connection.Priority {
			tied = append(tied, cand.Connection.ID)
		}
	}
	if len(tied) > 1 {
		return sel, tied, nil
	}
	c := top.Connection
	sel.Selected = &c
	return sel, nil, nil
}
