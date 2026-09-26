package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
)

// State is the observed state of a service's containers.
type State struct {
	// Exists: the service has at least one container.
	Exists bool
	// Running: at least one container runs.
	Running bool
	// Exited: every container has stopped (ExitCode is the highest).
	Exited   bool
	ExitCode int
	// Health aggregates the health checks: "" (none), "starting",
	// "healthy" (all healthy) or "unhealthy" (any unhealthy).
	Health string
}

// Runtime starts, stops and observes services.
type Runtime interface {
	Start(ctx context.Context, service string) error
	// Stop stops a service's containers and returns once they no longer run.
	Stop(ctx context.Context, service string, timeout *time.Duration) error
	State(ctx context.Context, service string) (State, error)
}

// Error codes (Error.Code).
const (
	// CodeDependencyFailed: a required dependency did not reach its
	// condition (unhealthy, failed one-shot, stopped).
	CodeDependencyFailed = "dependency_failed"
	// CodeDependencyMissing: a required dependency has no containers or is
	// not part of the project.
	CodeDependencyMissing = "dependency_missing"
	// CodeTimeout: a condition was not reached within Options.WaitTimeout.
	CodeTimeout = "timeout"
	// CodeConflict: resuming would have to start a service that was not
	// running before (#10).
	CodeConflict = "dependency_conflict"
	// CodeNoContainers: a service to start has no containers (deploy first).
	CodeNoContainers = "no_containers"
)

// Error is a lifecycle failure with a stable code.
type Error struct {
	Code       string
	Service    string
	Dependency string
	Message    string
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the lifecycle code of err ("" when none).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Defaults.
const (
	DefaultWaitTimeout = 2 * time.Minute
	DefaultPoll        = 500 * time.Millisecond
)

// Options configures an operation.
type Options struct {
	Clock clock.Clock
	// WaitTimeout bounds each wait for a dependency condition.
	WaitTimeout time.Duration
	// Poll is the state polling interval while waiting.
	Poll time.Duration
	// StopTimeout overrides the containers' stop grace period.
	StopTimeout *time.Duration
	// Progress receives human-readable steps (may be nil).
	Progress func(service, message string)
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.WaitTimeout <= 0 {
		o.WaitTimeout = DefaultWaitTimeout
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	return o
}

func (o Options) progress(service, format string, args ...any) {
	if o.Progress != nil {
		o.Progress(service, fmt.Sprintf(format, args...))
	}
}

// Report lists what an operation did.
type Report struct {
	Started []string
	Stopped []string
	// Skipped services were left alone (e.g. a completed one-shot that is
	// not restarted on resume).
	Skipped []string
	// Warnings are non-fatal findings (optional dependencies that were
	// missing or failed).
	Warnings []string
}

func all(g *Graph, names []string) []string {
	if len(names) == 0 {
		return g.Services()
	}
	return names
}

// Stop stops services (all when empty) in reverse dependency order:
// dependents before the services they depend on.
func Stop(ctx context.Context, g *Graph, rt Runtime, services []string, o Options) (Report, error) {
	o = o.withDefaults()
	var rep Report
	for _, s := range g.StopOrder(all(g, services)) {
		o.progress(s, "stopping")
		if err := rt.Stop(ctx, s, o.StopTimeout); err != nil {
			return rep, fmt.Errorf("stop %s: %w", s, err)
		}
		rep.Stopped = append(rep.Stopped, s)
	}
	return rep, nil
}

// Start starts services (all when empty) and their dependencies,
// dependencies first; before starting a service it waits for each
// dependency's condition. A required dependency that fails its condition
// stops the operation (services started so far keep running; there is no
// rollback, like Compose); an optional one only adds a warning.
func Start(ctx context.Context, g *Graph, rt Runtime, services []string, o Options) (Report, error) {
	o = o.withDefaults()
	var rep Report
	for _, s := range all(g, services) {
		if !g.Has(s) {
			return rep, &Error{Code: CodeDependencyMissing, Service: s, Message: fmt.Sprintf("service %s is not part of the project", s)}
		}
	}
	for _, s := range g.StartOrder(g.WithDependencies(all(g, services))) {
		if err := startOne(ctx, g, rt, s, o, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// startOne waits for s's dependencies and starts s.
func startOne(ctx context.Context, g *Graph, rt Runtime, s string, o Options, rep *Report) error {
	if err := awaitDependencies(ctx, g, rt, s, o, rep); err != nil {
		return err
	}
	st, err := rt.State(ctx, s)
	if err != nil {
		return err
	}
	if !st.Exists {
		return &Error{Code: CodeNoContainers, Service: s, Message: fmt.Sprintf("service %s has no containers; deploy the stack first", s)}
	}
	o.progress(s, "starting")
	if err := rt.Start(ctx, s); err != nil {
		return fmt.Errorf("start %s: %w", s, err)
	}
	rep.Started = append(rep.Started, s)
	return nil
}

// awaitDependencies waits until every dependency of s meets its condition.
func awaitDependencies(ctx context.Context, g *Graph, rt Runtime, s string, o Options, rep *Report) error {
	for _, d := range g.DependsOn(s) {
		if !g.Has(d.Service) {
			if d.Required {
				return &Error{Code: CodeDependencyMissing, Service: s, Dependency: d.Service,
					Message: fmt.Sprintf("%s depends on %s, which is not part of the project", s, d.Service)}
			}
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: optional dependency %s is not part of the project", s, d.Service))
			continue
		}
		if err := awaitCondition(ctx, rt, s, d, o); err != nil {
			if !d.Required {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: optional dependency %s: %v", s, d.Service, err))
				continue
			}
			return err
		}
	}
	return nil
}

// awaitCondition polls d.Service until d.Condition holds, fails or times out.
func awaitCondition(ctx context.Context, rt Runtime, dependent string, d Dependency, o Options) error {
	fail := func(code, format string, args ...any) error {
		return &Error{Code: code, Service: dependent, Dependency: d.Service, Message: fmt.Sprintf(format, args...)}
	}
	deadline := o.Clock.NewTimer(o.WaitTimeout)
	defer deadline.Stop()
	for {
		st, err := rt.State(ctx, d.Service)
		if err != nil {
			return err
		}
		done, ferr := evaluate(st, d, fail)
		if ferr != nil || done {
			return ferr
		}
		o.progress(dependent, "waiting for %s (%s)", d.Service, d.Condition)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C():
			return fail(CodeTimeout, "dependency %s of %s did not reach %s within %s", d.Service, dependent, d.Condition, o.WaitTimeout)
		case <-o.Clock.After(o.Poll):
		}
	}
}

// evaluate decides a condition on one observation: done, keep waiting
// (false, nil) or failed.
func evaluate(st State, d Dependency, fail func(code, format string, args ...any) error) (bool, error) {
	if !st.Exists {
		return false, fail(CodeDependencyMissing, "dependency %s has no containers", d.Service)
	}
	switch d.Condition {
	case ConditionCompleted:
		switch {
		case st.Exited && st.ExitCode == 0:
			return true, nil
		case st.Exited:
			return false, fail(CodeDependencyFailed, "dependency %s didn't complete successfully: exit %d", d.Service, st.ExitCode)
		}
		return false, nil
	case ConditionHealthy:
		switch {
		case st.Health == "healthy" && st.Running:
			return true, nil
		case st.Health == "unhealthy":
			return false, fail(CodeDependencyFailed, "dependency %s is unhealthy", d.Service)
		case st.Health == "" && (st.Running || st.Exited):
			return false, fail(CodeDependencyFailed, "dependency %s has no healthcheck (condition service_healthy)", d.Service)
		case st.Exited:
			return false, fail(CodeDependencyFailed, "dependency %s exited (%d) before becoming healthy", d.Service, st.ExitCode)
		}
		return false, nil
	default: // service_started: the dependency has been started
		if st.Running || st.Exited {
			return true, nil
		}
		return false, nil
	}
}

// Restart restarts services (all when empty) and propagates to dependents
// declaring restart: true (transitively): the whole set stops in reverse
// dependency order, then starts dependencies first with condition waits.
func Restart(ctx context.Context, g *Graph, rt Runtime, services []string, o Options) (Report, error) {
	o = o.withDefaults()
	set := g.RestartSet(all(g, services))
	rep, err := Stop(ctx, g, rt, set, o)
	if err != nil {
		return rep, err
	}
	for _, s := range g.StartOrder(set) {
		if err := startOne(ctx, g, rt, s, o, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Resume restarts exactly the services that were running before an
// operation stopped them (backup-time shutdown, restore; #10), dependencies
// first with condition waits. Services that were stopped stay stopped and
// completed one-shots stay completed. When a service to resume requires a
// dependency that was not running before and whose condition does not
// already hold (e.g. a one-shot that completed), Resume refuses with
// CodeConflict before starting anything rather than silently starting it.
func Resume(ctx context.Context, g *Graph, rt Runtime, wasRunning []string, o Options) (Report, error) {
	o = o.withDefaults()
	var rep Report
	running := map[string]bool{}
	for _, s := range wasRunning {
		if g.Has(s) {
			running[s] = true
		}
	}
	var conflicts []string
	for s := range running {
		for _, d := range g.DependsOn(s) {
			if !d.Required || running[d.Service] || !g.Has(d.Service) {
				continue
			}
			st, err := rt.State(ctx, d.Service)
			if err != nil {
				return rep, err
			}
			if done, _ := evaluate(st, d, func(string, string, ...any) error { return errors.New("") }); !done {
				conflicts = append(conflicts, fmt.Sprintf("%s needs %s (%s), which was not running", s, d.Service, d.Condition))
			}
		}
	}
	if len(conflicts) > 0 {
		slices.Sort(conflicts)
		return rep, &Error{Code: CodeConflict, Message: "cannot resume without starting services that were stopped: " + strings.Join(conflicts, "; ")}
	}
	for _, s := range g.Services() {
		if !running[s] {
			rep.Skipped = append(rep.Skipped, s)
		}
	}
	for _, s := range g.StartOrder(sortedSet(running)) {
		if err := startOne(ctx, g, rt, s, o, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
