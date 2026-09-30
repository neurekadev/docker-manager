package lifecycle

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// EngineRuntime drives the service containers of one Compose project
// through the Engine adapter. One-off (`compose run`) containers are
// ignored. State inspects every container (the container list can lag
// behind a stop on Engines before 26, docs/internal/support-matrix.md).
type EngineRuntime struct {
	Engine  engine.Engine
	Project string
	// Clock paces the wait for stopped containers (default clock.Real()).
	Clock clock.Clock
}

// ProjectContainers lists a project's service containers (all states).
func ProjectContainers(ctx context.Context, eng engine.Engine, project string) ([]engine.Container, error) {
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{ComposeProjectLabel + "=" + project}})
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, c := range list {
		if c.Labels[ComposeOneoffLabel] != "True" && c.Labels[ComposeServiceLabel] != "" {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b engine.Container) int { return strings.Compare(name(a), name(b)) })
	return out, nil
}

func name(c engine.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

func (r EngineRuntime) containers(ctx context.Context, service string) ([]engine.Container, error) {
	list, err := ProjectContainers(ctx, r.Engine, r.Project)
	if err != nil {
		return nil, err
	}
	var out []engine.Container
	for _, c := range list {
		if c.Labels[ComposeServiceLabel] == service {
			out = append(out, c)
		}
	}
	return out, nil
}

// Start starts the service's containers.
func (r EngineRuntime) Start(ctx context.Context, service string) error {
	list, err := r.containers(ctx, service)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := r.Engine.StartContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	return nil
}

// Container stop settling (see Stop).
const (
	stopSettleTimeout = 30 * time.Second
	stopSettlePoll    = 100 * time.Millisecond
)

// Stop stops the service's containers and returns once inspection no
// longer reports them running.
func (r EngineRuntime) Stop(ctx context.Context, service string, timeout *time.Duration) error {
	list, err := r.containers(ctx, service)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := r.Engine.StopContainer(ctx, c.ID, timeout); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			return err
		}
	}
	clk := r.Clock
	if clk == nil {
		clk = clock.Real()
	}
	deadline := clk.NewTimer(stopSettleTimeout)
	defer deadline.Stop()
	for {
		st, err := r.State(ctx, service)
		if err != nil {
			return err
		}
		if !st.Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C():
			return engine.Errorf("lifecycle.stop", engine.CodeTimeout, "containers of %s still running %s after stop", service, stopSettleTimeout)
		case <-clk.After(stopSettlePoll):
		}
	}
}

// State aggregates the service's containers.
func (r EngineRuntime) State(ctx context.Context, service string) (State, error) {
	list, err := r.containers(ctx, service)
	if err != nil {
		return State{}, err
	}
	var st State
	if len(list) == 0 {
		return st, nil
	}
	st.Exists, st.Exited = true, true
	healthy, anyHealth := true, false
	for _, c := range list {
		d, err := r.Engine.InspectContainer(ctx, c.ID)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			return State{}, err
		}
		s := d.State
		if s.Running || s.Restarting || s.Paused {
			st.Running, st.Exited = true, false
		} else if s.Status != "exited" && s.Status != "dead" {
			st.Exited = false // created, never started
		}
		if s.ExitCode > st.ExitCode {
			st.ExitCode = s.ExitCode
		}
		if s.Health != nil && s.Health.Status != "" && s.Health.Status != "none" {
			anyHealth = true
			switch s.Health.Status {
			case "unhealthy":
				st.Health = "unhealthy"
			case "healthy":
			default:
				healthy = false
			}
		}
	}
	if anyHealth && st.Health == "" {
		st.Health = "starting"
		if healthy {
			st.Health = "healthy"
		}
	}
	return st, nil
}

// GraphFromContainers builds the deployed dependency graph of a project
// from its containers' labels: Docker Manager's DependsOnLabel when present
// (under its current or legacy key), otherwise Compose's label (dependencies treated as required). Unknown
// conditions are treated as service_started.
func GraphFromContainers(containers []engine.Container) (*Graph, error) {
	byService := map[string]Service{}
	for _, c := range containers {
		svc := c.Labels[ComposeServiceLabel]
		if svc == "" || c.Labels[ComposeOneoffLabel] == "True" {
			continue
		}
		if _, ok := byService[svc]; ok {
			continue
		}
		raw, ok := protocol.LookupLabel(c.Labels, DependsOnLabel)
		if !ok {
			raw = c.Labels[ComposeDependsOnLabel]
		}
		deps, err := ParseDependsOn(raw)
		if err != nil {
			return nil, err
		}
		for i, d := range deps {
			if d.Condition != ConditionStarted && d.Condition != ConditionHealthy && d.Condition != ConditionCompleted {
				deps[i].Condition = ConditionStarted
			}
		}
		byService[svc] = Service{Name: svc, DependsOn: deps}
	}
	services := make([]Service, 0, len(byService))
	for _, s := range byService {
		services = append(services, s)
	}
	return NewGraph(services)
}
