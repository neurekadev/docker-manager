package migrations

import (
	"reflect"
	"testing"
)

// TestOrderStacks: stacks linked by a network or volume one creates and
// another joins as external form a group, the creating stack first;
// unlinked stacks are groups of one; groups follow their first stack's
// name; a circle moves in name order and is reported.
func TestOrderStacks(t *testing.T) {
	cases := []struct {
		name   string
		stacks []stackLinks
		groups [][]string
		deps   map[string][]string
		cycles [][]string
	}{
		{
			name: "a proxy network joined by two apps",
			stacks: []stackLinks{
				{ID: "app", Name: "app", ExternalNetworks: []string{"proxy"}},
				{ID: "blog", Name: "blog", ExternalNetworks: []string{"proxy"}},
				{ID: "traefik", Name: "traefik", Networks: []string{"proxy"}},
				{ID: "alone", Name: "alone", Networks: []string{"alone_default"}},
			},
			groups: [][]string{{"alone"}, {"traefik", "app", "blog"}},
			deps:   map[string][]string{"app": {"traefik"}, "blog": {"traefik"}},
		},
		{
			name: "a chain through a network and a volume",
			stacks: []stackLinks{
				{ID: "c", Name: "c", ExternalVolumes: []string{"shared"}},
				{ID: "b", Name: "b", ExternalNetworks: []string{"net"}, Volumes: []string{"shared"}},
				{ID: "a", Name: "a", Networks: []string{"net"}},
			},
			groups: [][]string{{"a", "b", "c"}},
			deps:   map[string][]string{"b": {"a"}, "c": {"b"}},
		},
		{
			name: "two stacks naming the same volume are one group in name order",
			stacks: []stackLinks{
				{ID: "y", Name: "y", Volumes: []string{"media"}},
				{ID: "x", Name: "x", Volumes: []string{"media"}},
			},
			groups: [][]string{{"x", "y"}},
			deps:   map[string][]string{},
		},
		{
			name: "a circle",
			stacks: []stackLinks{
				{ID: "p", Name: "p", Networks: []string{"n1"}, ExternalNetworks: []string{"n2"}},
				{ID: "q", Name: "q", Networks: []string{"n2"}, ExternalNetworks: []string{"n1"}},
			},
			groups: [][]string{{"p", "q"}},
			deps:   map[string][]string{"p": {"q"}, "q": {"p"}},
			cycles: [][]string{{"p", "q"}},
		},
		{
			name: "an external network nobody moving creates links nothing",
			stacks: []stackLinks{
				{ID: "a", Name: "a", ExternalNetworks: []string{"legacy"}},
				{ID: "b", Name: "b", ExternalNetworks: []string{"legacy"}},
			},
			groups: [][]string{{"a"}, {"b"}},
			deps:   map[string][]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := orderStacks(c.stacks)
			if !reflect.DeepEqual(got.Groups, c.groups) {
				t.Errorf("groups %v, want %v", got.Groups, c.groups)
			}
			if !reflect.DeepEqual(got.DependsOn, c.deps) {
				t.Errorf("depends on %v, want %v", got.DependsOn, c.deps)
			}
			if !reflect.DeepEqual(got.Cycles, c.cycles) {
				t.Errorf("cycles %v, want %v", got.Cycles, c.cycles)
			}
		})
	}
}
