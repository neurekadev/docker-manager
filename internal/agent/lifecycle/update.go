package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Codes of Confirm failures.
const (
	// CodeUnhealthy: an updated service's health check reports unhealthy.
	CodeUnhealthy = "unhealthy"
	// CodeExited: an updated service stopped right after it was started.
	CodeExited = "service_exited"
)

// UpdateReport lists what Update did.
type UpdateReport struct {
	Report
	// Recreated services were replaced (new image) and started again.
	Recreated []string
	// Restarted are dependents declaring depends_on restart: true on a
	// recreated service (transitively) that ran before: stopped and
	// started again around the recreation.
	Restarted []string
	// KeptStopped are changed services that were not running before the
	// update: they are left alone (a stopped service stays stopped and
	// picks up the new image at its next deploy).
	KeptStopped []string
}

// Recreator replaces the containers of services with ones created from
// the unchanged definition (the Compose SDK's create: services whose image
// changed get new containers, not started).
type Recreator func(ctx context.Context, services []string) error

// Update applies new images to changed services (#20, #9) and preserves
// the prior running state:
//
//   - only changed services that ran before are recreated; changed
//     services that were stopped stay stopped (KeptStopped);
//   - dependents that declare restart: true on a recreated service and ran
//     before are restarted with it (Compose restart propagation), stopped
//     dependents stay stopped;
//   - before anything is stopped, every required dependency of the set
//     must either run, be part of the set, or already meet its condition
//     (a completed one-shot): otherwise Update refuses with CodeConflict
//     (or CodeDependencyMissing) and changes nothing;
//   - the set stops in reverse dependency order, recreate replaces the
//     changed services' containers, then the set starts dependencies
//     first, waiting for each dependency's condition (service_started,
//     service_healthy, service_completed_successfully; optional
//     dependencies only warn), bounded by Options.WaitTimeout.
//
// There is no rollback (#25): when recreate fails, the set is started
// again as far as possible (best effort, reported as warnings) and the
// error is returned; when a start fails, services started so far keep
// running. Call Confirm afterwards to wait for the recreated services'
// own health.
func Update(ctx context.Context, g *Graph, rt Runtime, changed, wasRunning []string, recreate Recreator, o Options) (UpdateReport, error) {
	o = o.withDefaults()
	var rep UpdateReport
	running := map[string]bool{}
	for _, s := range wasRunning {
		if g.Has(s) {
			running[s] = true
		}
	}
	var apply []string
	for _, s := range changed {
		if !g.Has(s) {
			return rep, &Error{Code: CodeDependencyMissing, Service: s, Message: fmt.Sprintf("service %s is not part of the project", s)}
		}
		if slices.Contains(apply, s) || slices.Contains(rep.KeptStopped, s) {
			continue
		}
		if running[s] {
			apply = append(apply, s)
		} else {
			rep.KeptStopped = append(rep.KeptStopped, s)
		}
	}
	slices.Sort(apply)
	slices.Sort(rep.KeptStopped)
	if len(apply) == 0 {
		return rep, nil
	}
	var restart []string
	for _, s := range g.RestartSet(apply) {
		if !slices.Contains(apply, s) && running[s] {
			restart = append(restart, s)
		}
	}
	set := append(slices.Clone(apply), restart...)
	if err := checkUpdateDependencies(ctx, g, rt, set, running); err != nil {
		return rep, err
	}
	stopped, err := Stop(ctx, g, rt, set, o)
	rep.Stopped = stopped.Stopped
	if err != nil {
		return rep, err
	}
	o.progress("", "recreating %s", strings.Join(apply, ", "))
	if err := recreate(ctx, apply); err != nil {
		// No rollback: start what was stopped again as far as possible,
		// so the stack is not silently left stopped.
		for _, s := range g.StartOrder(set) {
			if serr := startOne(ctx, g, rt, s, o, &rep.Report); serr != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: could not start it again after the failed update: %v", s, serr))
			}
		}
		return rep, err
	}
	rep.Recreated, rep.Restarted = apply, restart
	for _, s := range g.StartOrder(set) {
		if err := startOne(ctx, g, rt, s, o, &rep.Report); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// checkUpdateDependencies refuses an update that would have to start a
// required dependency that was not running (like Resume).
func checkUpdateDependencies(ctx context.Context, g *Graph, rt Runtime, set []string, running map[string]bool) error {
	var conflicts []string
	for _, s := range set {
		for _, d := range g.DependsOn(s) {
			if !d.Required || running[d.Service] || slices.Contains(set, d.Service) {
				continue
			}
			if !g.Has(d.Service) {
				return &Error{Code: CodeDependencyMissing, Service: s, Dependency: d.Service,
					Message: fmt.Sprintf("%s depends on %s, which is not part of the project", s, d.Service)}
			}
			st, err := rt.State(ctx, d.Service)
			if err != nil {
				return err
			}
			if done, _ := evaluate(st, d, func(string, string, ...any) error { return errors.New("") }); !done {
				conflicts = append(conflicts, fmt.Sprintf("%s needs %s (%s), which is not running", s, d.Service, d.Condition))
			}
		}
	}
	if len(conflicts) > 0 {
		slices.Sort(conflicts)
		return &Error{Code: CodeConflict, Message: "cannot update without starting services that are stopped: " + strings.Join(conflicts, "; ")}
	}
	return nil
}

// Confirm waits until each service runs and, when it has a health check,
// reports healthy (bounded by Options.WaitTimeout per service). It fails
// with CodeUnhealthy, CodeExited (the service stopped), CodeNoContainers or
// CodeTimeout.
func Confirm(ctx context.Context, rt Runtime, services []string, o Options) error {
	o = o.withDefaults()
	names := slices.Clone(services)
	slices.Sort(names)
	for _, s := range slices.Compact(names) {
		if err := confirmOne(ctx, rt, s, o); err != nil {
			return err
		}
	}
	return nil
}

func confirmOne(ctx context.Context, rt Runtime, s string, o Options) error {
	deadline := o.Clock.NewTimer(o.WaitTimeout)
	defer deadline.Stop()
	for {
		st, err := rt.State(ctx, s)
		if err != nil {
			return err
		}
		switch {
		case !st.Exists:
			return &Error{Code: CodeNoContainers, Service: s, Message: fmt.Sprintf("service %s has no containers after the update", s)}
		case st.Health == "unhealthy":
			return &Error{Code: CodeUnhealthy, Service: s, Message: fmt.Sprintf("%s is unhealthy after the update", s)}
		case st.Exited:
			return &Error{Code: CodeExited, Service: s, Message: fmt.Sprintf("%s stopped after the update (exit %d)", s, st.ExitCode)}
		case st.Running && (st.Health == "" || st.Health == "healthy"):
			return nil
		}
		o.progress(s, "waiting until it is healthy")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C():
			return &Error{Code: CodeTimeout, Service: s, Message: fmt.Sprintf("%s did not become healthy within %s", s, o.WaitTimeout)}
		case <-o.Clock.After(o.Poll):
		}
	}
}
