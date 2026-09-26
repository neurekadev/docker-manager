package authz

import (
	"context"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Checker answers many capability checks for one principal, typically one
// request (list filtering, response shaping). It is bound to the context
// it was created with.
type Checker interface {
	Can(capability string, r Resource) Decision
}

// Compiler is implemented by authorizers that can load a principal's
// policy once and evaluate many checks against it (the permission
// service). For uses it when available.
type Compiler interface {
	Compile(ctx context.Context, p Principal) (Checker, error)
}

// Reacher is implemented by checkers that know their rules: Reaches
// reports whether any capability applies at or below r (a grant on a
// container inside an environment or stack makes the environment or stack
// reachable, so it is listed with its minimal view).
type Reacher interface {
	Reaches(r Resource) bool
}

// For returns a Checker for p. With a Compiler it compiles p's policy once
// (a failure denies everything, logged by the compiler); otherwise every
// check calls a.Can.
func For(ctx context.Context, a Authorizer, p Principal) Checker {
	a = OrDenyAll(a)
	if c, ok := a.(Compiler); ok {
		if ch, err := c.Compile(ctx, p); err == nil {
			return ch
		}
		return denyChecker{}
	}
	return funcChecker{ctx: ctx, a: a, p: p}
}

type funcChecker struct {
	ctx context.Context
	a   Authorizer
	p   Principal
}

func (f funcChecker) Can(capability string, r Resource) Decision {
	return f.a.Can(f.ctx, f.p, capability, r)
}

type denyChecker struct{}

func (denyChecker) Can(string, Resource) Decision {
	return Deny("the permission policy could not be loaded")
}

// Job capabilities evaluated on the job's targets.
const (
	CapJobRead   = "job.read"
	CapJobCancel = "job.cancel"
)

// EvaluateJob decides job.read and job.cancel on a job resource (r.Type
// "job"): allowed when, for every target, the caller holds the capability
// itself on that target or holds all of the job kind's capabilities
// (r.JobCapabilities) on it. can evaluates one capability on one resource.
// Authorizers call it for job resources; other capabilities on a job are
// denied.
func EvaluateJob(can func(capability string, r Resource) Decision, capability string, r Resource) Decision {
	if capability != CapJobRead && capability != CapJobCancel {
		return Deny(capability + " does not apply to jobs")
	}
	targets := r.Targets
	if len(targets) == 0 {
		targets = TargetResources(r.EnvironmentID, nil)
	}
	for _, t := range targets {
		d := can(capability, t)
		if d.Allowed {
			continue
		}
		viaKind := len(r.JobCapabilities) > 0
		for _, kc := range r.JobCapabilities {
			if !can(kc, t).Allowed {
				viaKind = false
				break
			}
		}
		if !viaKind {
			return d
		}
	}
	return Allow(capability + " on every target of the job")
}

// Level is how much of a resource a caller may see.
type Level int

// Levels.
const (
	// Hidden: not listed, 404 on direct access.
	Hidden Level = iota
	// Minimal: identity and status only (the catalog's ResourceType.Minimal
	// fields) plus the granted actions: a metrics-only or restart-only
	// grant shows the resource so it can be found and acted on, nothing
	// more.
	Minimal
	// Full: the resource type's read capability (container.details.read,
	// environment.read, ...) is granted.
	Full
)

// String renders the level as used in API responses.
func (l Level) String() string {
	switch l {
	case Minimal:
		return "minimal"
	case Full:
		return "full"
	}
	return "hidden"
}

// View is the response-shaping decision for one resource.
type View struct {
	Level Level
	// Actions are the granted catalog capabilities applicable to the
	// resource (sorted), for the UI to show exactly these actions.
	Actions []string
}

// Visible reports whether the resource may be listed or returned at all.
func (v View) Visible() bool { return v.Level != Hidden }

// Full reports whether every field may be returned.
func (v View) Full() bool { return v.Level == Full }

// Has reports whether capability is among the granted actions.
func (v View) Has(capability string) bool { return slices.Contains(v.Actions, capability) }

// ViewOf computes how c may see r: Full with the type's read capability;
// otherwise Minimal when any capability applicable to r is granted, or when
// a grant applies to something inside r (Reacher); otherwise Hidden.
func ViewOf(c Checker, r Resource) View {
	cat := catalog.Default()
	var v View
	for _, k := range cat.Applicable(r.Type) {
		if c.Can(k, r).Allowed {
			v.Actions = append(v.Actions, k)
		}
	}
	rt, _ := cat.Type(r.Type)
	switch {
	case rt.Read != "" && v.Has(rt.Read):
		v.Level = Full
	case len(v.Actions) > 0:
		v.Level = Minimal
	default:
		if rc, ok := c.(Reacher); ok && rc.Reaches(r) {
			v.Level = Minimal
		}
	}
	return v
}
