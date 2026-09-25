package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/testutil"
)

// updateApp is the multi-service fixture of #9/#20: a database with a
// health check, a completed one-shot migration, a web service that waits
// for the healthy database (restart: true), the completed migration and an
// optional cache (restart: true), a worker without restart propagation and
// an admin UI that restarts with web but was stopped before the update.
func updateApp(t *testing.T) *Graph {
	return graph(t,
		Service{Name: "db"},
		Service{Name: "cache"},
		Service{Name: "migrate", DependsOn: []Dependency{{Service: "db", Condition: ConditionHealthy, Required: true}}},
		Service{Name: "web", DependsOn: []Dependency{
			{Service: "db", Condition: ConditionHealthy, Required: true, Restart: true},
			{Service: "migrate", Condition: ConditionCompleted, Required: true},
			{Service: "cache", Condition: ConditionHealthy, Required: false, Restart: true},
		}},
		Service{Name: "worker", DependsOn: []Dependency{{Service: "db", Condition: ConditionStarted, Required: true}}},
		Service{Name: "admin", DependsOn: []Dependency{{Service: "web", Condition: ConditionStarted, Required: true, Restart: true}}},
	)
}

// updateRuntime is the fixture's state before the update: everything runs
// except the completed migration and the stopped admin UI.
func updateRuntime() *fakeRuntime {
	rt := newRuntime()
	for _, s := range []string{"db", "cache", "web", "worker"} {
		rt.states[s] = healthy
	}
	rt.states["migrate"] = exited0
	rt.states["admin"] = stopped
	return rt
}

var wasRunning = []string{"cache", "db", "web", "worker"}

// recorder returns a Recreator that records the recreated services.
func recorder(rt *fakeRuntime, err error) Recreator {
	return func(_ context.Context, services []string) error {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.events = append(rt.events, "recreate:"+strings.Join(services, ","))
		return err
	}
}

func TestUpdateOrderRestartPropagationAndStoppedServices(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.script("db", starting, starting, healthy)
	rt.script("web", running)
	rep, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"db", "admin"}, wasRunning, recorder(rt, nil), opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	// web restarts with db (restart: true) and waits for the healthy db,
	// the completed migration and the optional cache; the worker (no
	// restart propagation) keeps running; admin was stopped and stays so
	// (its image changed, but a stopped service is not recreated).
	want := []string{"stop:web", "stop:db", "recreate:db", "start:db", "start:web"}
	if got := rt.Events(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if !slices.Equal(rep.Recreated, []string{"db"}) || !slices.Equal(rep.Restarted, []string{"web"}) || !slices.Equal(rep.KeptStopped, []string{"admin"}) {
		t.Errorf("report %+v", rep)
	}
	if st, _ := rt.State(testutil.Context(t), "admin"); st.Running {
		t.Error("the stopped admin UI was started")
	}
	if st, _ := rt.State(testutil.Context(t), "migrate"); !st.Exited || st.ExitCode != 0 {
		t.Errorf("the completed one-shot was touched: %+v", st)
	}
}

func TestUpdateOptionalDependencyOnlyWarns(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.script("cache", starting, unhealthy)
	rt.script("web", running)
	rep, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"cache"}, wasRunning, recorder(rt, nil), opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Events(); !slices.Equal(got, []string{"stop:web", "stop:cache", "recreate:cache", "start:cache", "start:web"}) {
		t.Fatalf("events %v", got)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "optional dependency cache") {
		t.Errorf("warnings %v", rep.Warnings)
	}
	// The health confirmation of the updated service reports it.
	err = Confirm(testutil.Context(t), rt, rep.Recreated, opts(clk))
	if CodeOf(err) != CodeUnhealthy {
		t.Fatalf("confirm: %v, want unhealthy", err)
	}
}

func TestUpdateUnhealthyRequiredDependencyStopsDependents(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.script("db", starting, unhealthy)
	rt.script("web", running)
	_, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"db"}, wasRunning, recorder(rt, nil), opts(clk))
	})
	var le *Error
	if !errors.As(err, &le) || le.Code != CodeDependencyFailed || le.Service != "web" || le.Dependency != "db" {
		t.Fatalf("error %v, want dependency_failed of web on db", err)
	}
	if slices.Contains(rt.Events(), "start:web") {
		t.Error("web started although its required dependency is unhealthy")
	}
}

func TestUpdateRefusesBeforeStoppingWhenADependencyIsStopped(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.states["migrate"] = exited1 // the one-shot failed before: web's condition cannot hold
	_, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"web"}, wasRunning, recorder(rt, nil), opts(clk))
	})
	if CodeOf(err) != CodeConflict || !strings.Contains(err.Error(), "web needs migrate") {
		t.Fatalf("error %v, want dependency_conflict", err)
	}
	if got := rt.Events(); len(got) != 0 {
		t.Errorf("events %v: nothing may be stopped", got)
	}
}

func TestUpdateCompletedOneShotSatisfiesDependents(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.script("web", running)
	rep, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"web"}, wasRunning, recorder(rt, nil), opts(clk))
	})
	if err != nil {
		t.Fatal(err)
	}
	// admin (restart: true on web) was stopped: it is not restarted.
	if got := rt.Events(); !slices.Equal(got, []string{"stop:web", "recreate:web", "start:web"}) {
		t.Fatalf("events %v", got)
	}
	if len(rep.Restarted) != 0 {
		t.Errorf("restarted %v", rep.Restarted)
	}
}

func TestUpdateRecreateFailureStartsTheSetAgain(t *testing.T) {
	clk := testutil.FakeClock()
	rt := updateRuntime()
	rt.script("db", healthy)
	rt.script("web", running)
	boom := errors.New("create failed")
	_, err := drive(t, clk, func() (UpdateReport, error) {
		return Update(testutil.Context(t), updateApp(t), rt, []string{"db"}, wasRunning, recorder(rt, boom), opts(clk))
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error %v", err)
	}
	if got := rt.Events(); !slices.Equal(got, []string{"stop:web", "stop:db", "recreate:db", "start:db", "start:web"}) {
		t.Errorf("events %v: the stopped set must be started again", got)
	}
}

func TestUpdateNothingChangedIsANoOp(t *testing.T) {
	rt := updateRuntime()
	rep, err := Update(testutil.Context(t), updateApp(t), rt, nil, wasRunning, recorder(rt, nil), opts(testutil.FakeClock()))
	if err != nil || len(rt.Events()) != 0 || len(rep.Recreated) != 0 {
		t.Fatalf("rep %+v err %v events %v", rep, err, rt.Events())
	}
	if _, err := Update(testutil.Context(t), updateApp(t), rt, []string{"ghost"}, wasRunning, recorder(rt, nil), opts(testutil.FakeClock())); CodeOf(err) != CodeDependencyMissing {
		t.Fatalf("unknown service: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	clk := testutil.FakeClock()
	for name, c := range map[string]struct {
		states []State
		code   string
	}{
		"healthy":         {[]State{starting, starting, healthy}, ""},
		"no health check": {[]State{running}, ""},
		"unhealthy":       {[]State{starting, unhealthy}, CodeUnhealthy},
		"exited":          {[]State{running, exited1}, CodeExited},
		"never healthy":   {[]State{starting}, CodeTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime()
			rt.states["web"] = c.states[0]
			rt.pending["web"] = c.states
			if c.states[0] == running && len(c.states) > 1 {
				rt.pending["web"] = c.states[1:]
			}
			_, err := drive(t, clk, func() (struct{}, error) {
				return struct{}{}, Confirm(testutil.Context(t), rt, []string{"web"}, opts(clk))
			})
			if CodeOf(err) != c.code || (c.code == "" && err != nil) {
				t.Fatalf("error %v, want code %q", err, c.code)
			}
		})
	}
}
