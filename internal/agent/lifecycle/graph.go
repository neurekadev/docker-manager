// Package lifecycle is DockYard's shared dependency-aware lifecycle for
// Compose services (#7): stop in reverse dependency order, start
// dependencies first and wait for their depends_on conditions
// (service_started, service_healthy, service_completed_successfully) with
// bounded timeouts, honor optional (required: false) dependencies and
// restart propagation (restart: true), and resume only what was running
// before an operation.
//
// It is used by stack start/stop/restart (#7) and exported for automatic
// updates (#20), backup-time shutdown/resume and restore (#10), migrations
// (#35) and container actions on stack services (#9). It works on a Runtime
// (EngineRuntime drives the containers of a Compose project through the
// Engine adapter); the dependency graph comes from the loaded project or
// from the containers' labels (GraphFromContainers), so a stack whose
// sources were edited but not deployed is operated as deployed.
package lifecycle

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// depends_on conditions.
const (
	ConditionStarted   = "service_started"
	ConditionHealthy   = "service_healthy"
	ConditionCompleted = "service_completed_successfully"
)

// Labels read (and, for DependsOnLabel, written by the Compose adapter).
const (
	// DependsOnLabel is DockYard's dependency label on every service
	// container it deploys: "service:condition:restart:required,..." (the
	// Compose label below lacks `required`).
	DependsOnLabel = "dev.neureka.dockyard.depends_on"
	// ComposeDependsOnLabel is Compose's "service:condition:restart,..."
	// label (containers deployed by other Compose clients).
	ComposeDependsOnLabel = "com.docker.compose.depends_on"
	ComposeProjectLabel   = "com.docker.compose.project"
	ComposeServiceLabel   = "com.docker.compose.service"
	ComposeOneoffLabel    = "com.docker.compose.oneoff"
)

// Dependency is a depends_on entry.
type Dependency struct {
	Service   string
	Condition string
	// Required: false makes the dependency optional (Compose only warns
	// when it is missing or fails).
	Required bool
	// Restart: true restarts the dependent when this dependency is
	// restarted by an explicit operation.
	Restart bool
}

// Service is a node of the graph.
type Service struct {
	Name      string
	DependsOn []Dependency
}

// Graph is a validated, acyclic service dependency graph.
type Graph struct {
	services map[string]Service
	names    []string // sorted
}

// NewGraph validates services (unique names, known conditions, no cycles).
// Dependencies on services outside the graph are allowed: optional ones are
// skipped, required ones fail when an operation needs them.
func NewGraph(services []Service) (*Graph, error) {
	g := &Graph{services: map[string]Service{}}
	for _, s := range services {
		if s.Name == "" {
			return nil, errors.New("lifecycle: service without a name")
		}
		if _, dup := g.services[s.Name]; dup {
			return nil, fmt.Errorf("lifecycle: duplicate service %q", s.Name)
		}
		deps := slices.Clone(s.DependsOn)
		for i, d := range deps {
			switch d.Condition {
			case "":
				deps[i].Condition = ConditionStarted
			case ConditionStarted, ConditionHealthy, ConditionCompleted:
			default:
				return nil, fmt.Errorf("lifecycle: service %q: unknown depends_on condition %q", s.Name, d.Condition)
			}
		}
		slices.SortFunc(deps, func(a, b Dependency) int { return strings.Compare(a.Service, b.Service) })
		s.DependsOn = deps
		g.services[s.Name] = s
		g.names = append(g.names, s.Name)
	}
	slices.Sort(g.names)
	if _, err := g.topo(g.names); err != nil {
		return nil, err
	}
	return g, nil
}

// Services returns the service names, sorted.
func (g *Graph) Services() []string { return slices.Clone(g.names) }

// Has reports whether the graph has the service.
func (g *Graph) Has(name string) bool { _, ok := g.services[name]; return ok }

// DependsOn returns a service's dependencies.
func (g *Graph) DependsOn(name string) []Dependency { return slices.Clone(g.services[name].DependsOn) }

// topo orders names so that every service comes after its dependencies in
// the graph (ties by name). It fails on a cycle.
func (g *Graph) topo(names []string) ([]string, error) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var out []string
	var visit func(n string, path []string) error
	visit = func(n string, path []string) error {
		switch color[n] {
		case grey:
			return fmt.Errorf("lifecycle: dependency cycle: %s", strings.Join(append(path, n), " -> "))
		case black:
			return nil
		}
		color[n] = grey
		for _, d := range g.services[n].DependsOn {
			if set[d.Service] && g.Has(d.Service) {
				if err := visit(d.Service, append(path, n)); err != nil {
					return err
				}
			}
		}
		color[n] = black
		out = append(out, n)
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// StartOrder returns names ordered dependencies first.
func (g *Graph) StartOrder(names []string) []string {
	order, _ := g.topo(sortedKnown(g, names))
	return order
}

// StopOrder returns names ordered dependents first (reverse dependency
// order).
func (g *Graph) StopOrder(names []string) []string {
	order := g.StartOrder(names)
	slices.Reverse(order)
	return order
}

// WithDependencies returns names plus their transitive dependencies that
// exist in the graph (optional ones included when present), sorted.
func (g *Graph) WithDependencies(names []string) []string {
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if seen[n] || !g.Has(n) {
			return
		}
		seen[n] = true
		for _, d := range g.services[n].DependsOn {
			walk(d.Service)
		}
	}
	for _, n := range names {
		walk(n)
	}
	return sortedSet(seen)
}

// RestartSet returns names plus every service that must be restarted with
// them: dependents declaring restart: true on a restarted service,
// transitively (Compose restart propagation).
func (g *Graph) RestartSet(names []string) []string {
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if seen[n] || !g.Has(n) {
			return
		}
		seen[n] = true
		for _, other := range g.names {
			for _, d := range g.services[other].DependsOn {
				if d.Service == n && d.Restart {
					walk(other)
				}
			}
		}
	}
	for _, n := range names {
		walk(n)
	}
	return sortedSet(seen)
}

func sortedKnown(g *Graph, names []string) []string {
	var out []string
	for _, n := range names {
		if g.Has(n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// FormatDependsOn renders DependsOnLabel's value (sorted by service).
func FormatDependsOn(deps []Dependency) string {
	parts := make([]string, 0, len(deps))
	for _, d := range deps {
		cond := d.Condition
		if cond == "" {
			cond = ConditionStarted
		}
		parts = append(parts, d.Service+":"+cond+":"+strconv.FormatBool(d.Restart)+":"+strconv.FormatBool(d.Required))
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// ParseDependsOn parses DependsOnLabel (four fields) or Compose's label
// (three fields; its dependencies are required, like Compose treats them).
func ParseDependsOn(v string) ([]Dependency, error) {
	var out []Dependency
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	for _, part := range strings.Split(v, ",") {
		f := strings.Split(part, ":")
		if f[0] == "" || len(f) > 4 {
			return nil, fmt.Errorf("lifecycle: malformed depends_on label entry %q", part)
		}
		d := Dependency{Service: f[0], Condition: ConditionStarted, Required: true, Restart: true}
		if len(f) > 1 {
			d.Condition = f[1]
		}
		if len(f) > 2 {
			b, err := strconv.ParseBool(f[2])
			if err != nil {
				return nil, fmt.Errorf("lifecycle: malformed depends_on label entry %q", part)
			}
			d.Restart = b
		}
		if len(f) > 3 {
			b, err := strconv.ParseBool(f[3])
			if err != nil {
				return nil, fmt.Errorf("lifecycle: malformed depends_on label entry %q", part)
			}
			d.Required = b
		}
		out = append(out, d)
	}
	return out, nil
}
