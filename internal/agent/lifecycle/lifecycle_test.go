package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// fakeRuntime is a scripted Engine: each service has a behavior that
// decides what State reports after Start (one entry per State call, the
// last one sticks). It records starts and stops in order.
type fakeRuntime struct {
	mu       sync.Mutex
	events   []string
	states   map[string]State
	scripts  map[string][]State // states reported after Start, in order
	pending  map[string][]State
	noExists map[string]bool
}

func newRuntime() *fakeRuntime {
	return &fakeRuntime{states: map[string]State{}, scripts: map[string][]State{}, pending: map[string][]State{}, noExists: map[string]bool{}}
}

var (
	running   = State{Exists: true, Running: true}
	stopped   = State{Exists: true, Exited: true}
	starting  = State{Exists: true, Running: true, Health: "starting"}
	healthy   = State{Exists: true, Running: true, Health: "healthy"}
	unhealthy = State{Exists: true, Running: true, Health: "unhealthy"}
	exited0   = State{Exists: true, Exited: true, ExitCode: 0}
	exited1   = State{Exists: true, Exited: true, ExitCode: 1}
)

// script sets what a service reports after it is started.
func (f *fakeRuntime) script(service string, states ...State) {
	f.scripts[service] = states
	if _, ok := f.states[service]; !ok {
		f.states[service] = stopped
	}
}

func (f *fakeRuntime) Start(_ context.Context, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "start:"+s)
	f.pending[s] = slices.Clone(f.scripts[s])
	if len(f.pending[s]) == 0 {
		f.pending[s] = []State{running}
	}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, s string, _ *time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "stop:"+s)
	f.pending[s] = nil
	f.states[s] = stopped
	return nil
}

func (f *fakeRuntime) State(_ context.Context, s string) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noExists[s] {
		return State{}, nil
	}
	if p := f.pending[s]; len(p) > 0 {
		f.states[s] = p[0]
		if len(p) > 1 {
			f.pending[s] = p[1:]
		}
	}
	st, ok := f.states[s]
	if !ok {
		return stopped, nil
	}
	return st, nil
}

func (f *fakeRuntime) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// drive runs op while advancing the fake clock whenever the operation waits
// (a deadline timer plus a poll timer are armed).
func drive[T any](t *testing.T, clk *clock.Fake, op func() (T, error)) (T, error) {
	t.Helper()
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := op()
		done <- result{v, err}
	}()
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	go func() {
		for {
			if clk.BlockUntilWaiters(ctx, 2) != nil {
				return
			}
			clk.Advance(DefaultPoll)
		}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-testutil.Context(t).Done():
		t.Fatal("operation did not finish")
	}
	var zero T
	return zero, nil
}

func graph(t *testing.T, services ...Service) *Graph {
	t.Helper()
	g, err := NewGraph(services)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// app is the dependency graph of the Compose lifecycle fixtures: a database
// with a health check, a one-shot migration, a web service with an optional
// cache and restart propagation, and a worker without it.
func app(t *testing.T) *Graph {
	return graph(t,
		Service{Name: "db"},
		Service{Name: "migrate", DependsOn: []Dependency{{Service: "db", Condition: ConditionHealthy, Required: true}}},
		Service{Name: "web", DependsOn: []Dependency{
			{Service: "db", Condition: ConditionHealthy, Required: true, Restart: true},
			{Service: "migrate", Condition: ConditionCompleted, Required: true},
			{Service: "cache", Condition: ConditionStarted, Required: false},
		}},
		Service{Name: "worker", DependsOn: []Dependency{{Service: "db", Condition: ConditionStarted, Required: true}}},
	)
}

func opts(clk clock.Clock) Options {
	return Options{Clock: clk, WaitTimeout: 10 * time.Second}
}

func TestStartOrderAndConditions(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", starting, starting, healthy)
	rt.script("migrate", running, exited0)
	rt.script("web", running)
	rt.script("worker", running)
	rep, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, nil, opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start:db", "start:migrate", "start:web", "start:worker"}
	if got := rt.Events(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v (dependencies first)", got, want)
	}
	// The optional cache is not part of the project: only a warning.
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "optional dependency cache") {
		t.Errorf("warnings %v", rep.Warnings)
	}
	if !slices.Equal(rep.Started, []string{"db", "migrate", "web", "worker"}) {
		t.Errorf("started %v", rep.Started)
	}
}

func TestStartSelectedServiceStartsDependencies(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", healthy)
	rt.script("migrate", exited0)
	rt.script("web", running)
	rt.script("worker", running)
	if _, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, []string{"web"}, opts(clk))
	}); err != nil {
		t.Fatal(err)
	}
	if got := rt.Events(); !slices.Equal(got, []string{"start:db", "start:migrate", "start:web"}) {
		t.Errorf("events %v: the worker is not a dependency of web", got)
	}
}

func TestUnhealthyDependencyBlocksDependents(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", starting, unhealthy)
	rt.script("migrate", exited0)
	rt.script("web", running)
	rt.script("worker", running)
	_, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, nil, opts(clk))
	})
	var le *Error
	if !errors.As(err, &le) || le.Code != CodeDependencyFailed || le.Dependency != "db" || le.Service != "migrate" {
		t.Fatalf("error %v, want dependency_failed of migrate on db", err)
	}
	if got := rt.Events(); !slices.Equal(got, []string{"start:db"}) {
		t.Errorf("events %v: nothing that needs a healthy db may start", got)
	}
}

func TestFailedOneShotBlocksDependents(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", healthy)
	rt.script("migrate", running, running, exited1)
	rt.script("web", running)
	_, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, []string{"web"}, opts(clk))
	})
	if CodeOf(err) != CodeDependencyFailed || !strings.Contains(err.Error(), "didn't complete successfully: exit 1") {
		t.Fatalf("error %v", err)
	}
	if slices.Contains(rt.Events(), "start:web") {
		t.Error("web started after its one-shot dependency failed")
	}
}

func TestCompletedOneShotSatisfiesDependents(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", healthy)
	rt.script("migrate", running, exited0)
	rt.script("web", running)
	if _, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, []string{"web"}, opts(clk))
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := rt.State(testutil.Context(t), "migrate")
	if !st.Exited || st.ExitCode != 0 || !slices.Contains(rt.Events(), "start:web") {
		t.Errorf("migrate %+v events %v", st, rt.Events())
	}
}

func TestOptionalDependencyFailureOnlyWarns(t *testing.T) {
	clk := testutil.FakeClock()
	g := graph(t,
		Service{Name: "cache"},
		Service{Name: "web", DependsOn: []Dependency{{Service: "cache", Condition: ConditionHealthy, Required: false}}},
	)
	rt := newRuntime()
	rt.script("cache", unhealthy)
	rt.script("web", running)
	rep, err := drive(t, clk, func() (Report, error) { return Start(testutil.Context(t), g, rt, nil, opts(clk)) })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Started, "web") || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "unhealthy") {
		t.Errorf("report %+v", rep)
	}
	// A required dependency outside the project fails instead.
	g2 := graph(t, Service{Name: "web", DependsOn: []Dependency{{Service: "db", Required: true}}})
	if _, err := Start(testutil.Context(t), g2, newRuntime(), nil, opts(clk)); CodeOf(err) != CodeDependencyMissing {
		t.Errorf("required missing dependency: %v", err)
	}
}

func TestDependencyConditionTimesOut(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", starting) // never becomes healthy
	rt.script("migrate", exited0)
	start := clk.Now()
	_, err := drive(t, clk, func() (Report, error) {
		return Start(testutil.Context(t), app(t), rt, []string{"migrate"}, opts(clk))
	})
	if CodeOf(err) != CodeTimeout {
		t.Fatalf("error %v, want timeout", err)
	}
	if waited := clk.Since(start); waited < 10*time.Second || waited > 11*time.Second {
		t.Errorf("timed out after %s, want the 10s bound", waited)
	}
}

func TestStopReverseDependencyOrder(t *testing.T) {
	rt := newRuntime()
	rep, err := Stop(testutil.Context(t), app(t), rt, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Dependents before their dependencies; ties by name.
	want := []string{"stop:worker", "stop:web", "stop:migrate", "stop:db"}
	if got := rt.Events(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	if len(rep.Stopped) != 4 {
		t.Errorf("stopped %v", rep.Stopped)
	}
}

func TestRestartPropagation(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", healthy)
	rt.script("migrate", exited0)
	rt.script("web", running)
	rt.states["migrate"] = exited0
	rep, err := drive(t, clk, func() (Report, error) {
		return Restart(testutil.Context(t), app(t), rt, []string{"db"}, opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	// web declares restart: true on db and is restarted with it; migrate
	// and worker do not and are left alone.
	want := []string{"stop:web", "stop:db", "start:db", "start:web"}
	if got := rt.Events(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	if !slices.Equal(rep.Stopped, []string{"web", "db"}) || !slices.Equal(rep.Started, []string{"db", "web"}) {
		t.Errorf("report %+v", rep)
	}
	if got := app(t).RestartSet([]string{"migrate"}); !slices.Equal(got, []string{"migrate"}) {
		t.Errorf("restart set of migrate %v", got)
	}
}

func TestResumeOnlyPreviouslyRunning(t *testing.T) {
	clk := testutil.FakeClock()
	rt := newRuntime()
	rt.script("db", healthy)
	rt.script("web", running)
	rt.states["migrate"] = exited0 // a completed one-shot
	rt.states["worker"] = stopped  // was stopped before the backup
	rep, err := drive(t, clk, func() (Report, error) {
		return Resume(testutil.Context(t), app(t), rt, []string{"db", "web"}, opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Events(); !slices.Equal(got, []string{"start:db", "start:web"}) {
		t.Errorf("events %v: only db and web were running", got)
	}
	if !slices.Equal(rep.Skipped, []string{"migrate", "worker"}) {
		t.Errorf("skipped %v", rep.Skipped)
	}
}

func TestResumeRefusesToStartStoppedDependency(t *testing.T) {
	rt := newRuntime()
	rt.states["db"] = stopped
	rt.states["migrate"] = exited0
	_, err := Resume(testutil.Context(t), app(t), rt, []string{"web"}, Options{})
	if CodeOf(err) != CodeConflict || !strings.Contains(err.Error(), "web needs db (service_healthy)") {
		t.Fatalf("error %v, want dependency_conflict", err)
	}
	if len(rt.Events()) != 0 {
		t.Errorf("events %v: nothing may start on a conflict", rt.Events())
	}
}

func TestStartWithoutContainers(t *testing.T) {
	rt := newRuntime()
	rt.noExists["db"] = true
	g := graph(t, Service{Name: "db"})
	if _, err := Start(testutil.Context(t), g, rt, nil, Options{}); CodeOf(err) != CodeNoContainers {
		t.Errorf("error %v, want no_containers", err)
	}
}

func TestGraphRejectsCyclesAndUnknownConditions(t *testing.T) {
	_, err := NewGraph([]Service{
		{Name: "a", DependsOn: []Dependency{{Service: "b"}}},
		{Name: "b", DependsOn: []Dependency{{Service: "a"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle: %v", err)
	}
	if _, err := NewGraph([]Service{{Name: "a", DependsOn: []Dependency{{Service: "b", Condition: "whenever"}}}}); err == nil {
		t.Error("unknown condition accepted")
	}
}

func TestDependsOnLabels(t *testing.T) {
	deps := []Dependency{
		{Service: "web", Condition: ConditionStarted, Required: false, Restart: false},
		{Service: "db", Condition: ConditionHealthy, Required: true, Restart: true},
	}
	label := FormatDependsOn(deps)
	if label != "db:service_healthy:true:true,web:service_started:false:false" {
		t.Errorf("label %q", label)
	}
	got, err := ParseDependsOn(label)
	if err != nil || len(got) != 2 || got[0].Service != "db" || !got[0].Required || got[1].Required || got[1].Restart {
		t.Errorf("parsed %+v %v", got, err)
	}
	// Compose's own label has no required field: required.
	got, err = ParseDependsOn("db:service_healthy:false")
	if err != nil || len(got) != 1 || !got[0].Required || got[0].Restart {
		t.Errorf("compose label %+v %v", got, err)
	}
	if _, err := ParseDependsOn("db:x:maybe"); err == nil {
		t.Error("malformed label accepted")
	}
}

func TestGraphFromContainers(t *testing.T) {
	c := func(svc string, labels map[string]string) engine.Container {
		l := map[string]string{ComposeProjectLabel: "app", ComposeServiceLabel: svc, ComposeOneoffLabel: "False"}
		for k, v := range labels {
			l[k] = v
		}
		return engine.Container{ID: svc + "-1", Labels: l}
	}
	g, err := GraphFromContainers([]engine.Container{
		c("db", nil),
		c("web", map[string]string{DependsOnLabel: "cache:service_started:false:false,db:service_healthy:true:true",
			ComposeDependsOnLabel: "cache:service_started:false,db:service_healthy:true"}),
		c("legacy", map[string]string{ComposeDependsOnLabel: "db:running_or_healthy:true"}),
		{ID: "run-1", Labels: map[string]string{ComposeServiceLabel: "web", ComposeOneoffLabel: "True"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Services(), []string{"db", "legacy", "web"}) {
		t.Errorf("services %v", g.Services())
	}
	web := g.DependsOn("web")
	if len(web) != 2 || web[0].Service != "cache" || web[0].Required || !web[1].Required {
		t.Errorf("web deps %+v: Docker Manager's label wins", web)
	}
	if l := g.DependsOn("legacy"); len(l) != 1 || l[0].Condition != ConditionStarted {
		t.Errorf("legacy deps %+v", l)
	}
}

func TestEngineRuntimeAggregatesState(t *testing.T) {
	eng := &fakeEngine{containers: []engine.Container{
		{ID: "a", Names: []string{"/app-web-1"}, Labels: map[string]string{ComposeProjectLabel: "app", ComposeServiceLabel: "web"}},
		{ID: "b", Names: []string{"/app-web-2"}, Labels: map[string]string{ComposeProjectLabel: "app", ComposeServiceLabel: "web"}},
		{ID: "c", Names: []string{"/app-job-1"}, Labels: map[string]string{ComposeProjectLabel: "app", ComposeServiceLabel: "job"}},
	}, states: map[string]engine.ContainerState{
		"a": {Status: "running", Running: true, Health: &engine.Health{Status: "healthy"}},
		"b": {Status: "running", Running: true, Health: &engine.Health{Status: "starting"}},
		"c": {Status: "exited", ExitCode: 3},
	}}
	rt := EngineRuntime{Engine: eng, Project: "app", Clock: testutil.FakeClock()}
	ctx := testutil.Context(t)
	st, err := rt.State(ctx, "web")
	if err != nil || !st.Running || st.Health != "starting" || st.Exited {
		t.Errorf("web %+v %v", st, err)
	}
	st, _ = rt.State(ctx, "job")
	if !st.Exited || st.ExitCode != 3 || st.Running {
		t.Errorf("job %+v", st)
	}
	st, _ = rt.State(ctx, "none")
	if st.Exists {
		t.Errorf("missing service %+v", st)
	}
	if err := rt.Stop(ctx, "web", nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"stop:a", "stop:b", "start:c"}; !slices.Equal(eng.calls, want) {
		t.Errorf("calls %v, want %v", eng.calls, want)
	}
}

// fakeEngine implements the container calls EngineRuntime uses.
type fakeEngine struct {
	engine.Engine
	containers []engine.Container
	states     map[string]engine.ContainerState
	calls      []string
}

func (f *fakeEngine) ListContainers(_ context.Context, flt engine.ContainerFilter) ([]engine.Container, error) {
	if !flt.All || len(flt.Labels) != 1 || flt.Labels[0] != ComposeProjectLabel+"=app" {
		return nil, fmt.Errorf("unexpected filter %+v", flt)
	}
	return slices.Clone(f.containers), nil
}

func (f *fakeEngine) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, error) {
	return engine.ContainerDetails{ID: id, State: f.states[id]}, nil
}

func (f *fakeEngine) StartContainer(_ context.Context, id string) error {
	f.calls = append(f.calls, "start:"+id)
	return nil
}

func (f *fakeEngine) StopContainer(_ context.Context, id string, _ *time.Duration) error {
	f.calls = append(f.calls, "stop:"+id)
	s := f.states[id]
	s.Running, s.Status, s.Health = false, "exited", nil
	f.states[id] = s
	return nil
}

// TestGraphFromLegacyDependsOnLabel: containers deployed before the label
// prefix changed carry Docker Manager's dependency label under its legacy
// key; it is read like the current one (the current key wins), and before
// Compose's label.
func TestGraphFromLegacyDependsOnLabel(t *testing.T) {
	legacy := protocol.LegacyLabel(DependsOnLabel)
	if DependsOnLabel != "docker-manager.depends_on" || legacy != "dev.neureka.docker-manager.depends_on" {
		t.Fatalf("keys %q / %q", DependsOnLabel, legacy)
	}
	c := func(svc string, labels map[string]string) engine.Container {
		l := map[string]string{ComposeProjectLabel: "app", ComposeServiceLabel: svc}
		for k, v := range labels {
			l[k] = v
		}
		return engine.Container{ID: svc + "-1", Labels: l}
	}
	g, err := GraphFromContainers([]engine.Container{
		c("db", nil),
		c("cache", nil),
		// Legacy key only: optional dependency kept (Compose's label says required).
		c("web", map[string]string{legacy: "db:service_healthy:false:false", ComposeDependsOnLabel: "db:service_healthy:false"}),
		// Both keys: the current one wins.
		c("api", map[string]string{DependsOnLabel: "cache:service_started:false:true", legacy: "db:service_healthy:false:true"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if web := g.DependsOn("web"); len(web) != 1 || web[0].Service != "db" || web[0].Required {
		t.Errorf("web deps %+v: the legacy label must be read", web)
	}
	if api := g.DependsOn("api"); len(api) != 1 || api[0].Service != "cache" {
		t.Errorf("api deps %+v: the current label wins", api)
	}
}
