package migrations

import (
	"cmp"
	"slices"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// stackLinks are the Docker networks and volumes a stack's project
// creates (its own, under their Docker names) and joins as external.
type stackLinks struct {
	ID, Name                          string
	Networks, Volumes                 []string
	ExternalNetworks, ExternalVolumes []string
}

// linksOf reads a project's links from the source's facts.
func linksOf(id string, p *protocol.MigrationProjectFacts) stackLinks {
	l := stackLinks{ID: id, Name: p.Name}
	for _, n := range p.Networks {
		if n.External {
			l.ExternalNetworks = append(l.ExternalNetworks, n.Name)
		} else {
			l.Networks = append(l.Networks, n.Name)
		}
	}
	for _, v := range p.Volumes {
		switch {
		case v.Anonymous:
		case v.External:
			l.ExternalVolumes = append(l.ExternalVolumes, v.Name)
		default:
			l.Volumes = append(l.Volumes, v.Name)
		}
	}
	return l
}

// stackOrder is the order an environment migration moves its stacks in.
type stackOrder struct {
	// Groups are the stack IDs of each group in their order: a stack
	// that creates a network or volume another joins as external comes
	// before it. Stacks without links are groups of one.
	Groups [][]string
	// DependsOn lists, per stack, the stacks of its group it joins a
	// network or volume of.
	DependsOn map[string][]string
	// Cycles are groups whose stacks depend on each other in a circle:
	// they move in name order (the first may not start on the
	// destination until the others are there).
	Cycles [][]string
}

// orderStacks groups linked stacks and orders each group (the order of
// links, then names). Groups follow the name of their first stack.
func orderStacks(stacks []stackLinks) stackOrder {
	sorted := slices.Clone(stacks)
	slices.SortFunc(sorted, func(a, b stackLinks) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	name := map[string]string{}
	parent := map[string]string{}
	for _, s := range sorted {
		name[s.ID], parent[s.ID] = s.Name, s.ID
	}
	var find func(string) string
	find = func(id string) string {
		if parent[id] != id {
			parent[id] = find(parent[id])
		}
		return parent[id]
	}
	union := func(a, b string) { parent[find(a)] = find(b) }

	// Who creates each network and volume (several stacks may name the
	// same one: they are linked without an order).
	creators := map[string][]string{}
	for _, s := range sorted {
		for _, n := range s.Networks {
			creators["n:"+n] = append(creators["n:"+n], s.ID)
		}
		for _, v := range s.Volumes {
			creators["v:"+v] = append(creators["v:"+v], s.ID)
		}
	}
	for _, ids := range creators {
		for _, id := range ids[1:] {
			union(ids[0], id)
		}
	}
	deps := map[string][]string{}
	for _, s := range sorted {
		var joined []string
		for _, n := range s.ExternalNetworks {
			joined = append(joined, creators["n:"+n]...)
		}
		for _, v := range s.ExternalVolumes {
			joined = append(joined, creators["v:"+v]...)
		}
		for _, d := range joined {
			if d != s.ID && !slices.Contains(deps[s.ID], d) {
				deps[s.ID] = append(deps[s.ID], d)
				union(s.ID, d)
			}
		}
		slices.SortFunc(deps[s.ID], func(a, b string) int { return cmp.Compare(name[a], name[b]) })
	}

	members := map[string][]string{}
	var roots []string
	for _, s := range sorted {
		r := find(s.ID)
		if _, ok := members[r]; !ok {
			roots = append(roots, r)
		}
		members[r] = append(members[r], s.ID)
	}
	out := stackOrder{DependsOn: map[string][]string{}}
	for _, r := range roots {
		group, cycle := topo(members[r], deps)
		out.Groups = append(out.Groups, group)
		if len(cycle) > 0 {
			out.Cycles = append(out.Cycles, cycle)
		}
	}
	for id, d := range deps {
		out.DependsOn[id] = d
	}
	slices.SortStableFunc(out.Groups, func(a, b []string) int { return cmp.Compare(name[a[0]], name[b[0]]) })
	return out
}

// topo orders a group's members (in name order) so every stack follows
// the stacks it depends on; members left in a cycle follow in name order.
func topo(members []string, deps map[string][]string) (order, cycle []string) {
	placed := map[string]bool{}
	for len(order) < len(members) {
		progressed := false
		for _, id := range members {
			if placed[id] {
				continue
			}
			ready := true
			for _, d := range deps[id] {
				if !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				order, placed[id], progressed = append(order, id), true, true
				// Restart from the first name: a stack that became ready
				// moves before later names.
				break
			}
		}
		if !progressed {
			for _, id := range members {
				if !placed[id] {
					cycle = append(cycle, id)
				}
			}
			order = append(order, cycle...)
			break
		}
	}
	return order, cycle
}
