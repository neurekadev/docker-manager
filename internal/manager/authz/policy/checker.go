package policy

import (
	"slices"
	"strings"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
)

// Location is where a resource currently is (the resource graph).
type Location struct {
	// Found is false for deleted or unknown resources.
	Found         bool
	EnvironmentID string
	// Parents are the containing resources, nearest first.
	Parents []authz.ResourceRef
}

// LocateFunc resolves a resource's location.
type LocateFunc func(ref authz.ResourceRef) Location

// Checker evaluates authz resources for one compiled Subject: it
// implements authz.Checker and authz.Reacher. It is used by one request at
// a time (not safe for concurrent use).
type Checker struct {
	cat    *catalog.Catalog
	subj   Subject
	locate LocateFunc
	locs   map[authz.ResourceRef]Location
}

var _ interface {
	authz.Checker
	authz.Reacher
} = (*Checker)(nil)

// NewChecker returns a checker for subj. locate (optional) resolves
// resources that come without Parents; without it only the resource's own
// fields are used.
func NewChecker(cat *catalog.Catalog, subj Subject, locate LocateFunc) *Checker {
	if locate == nil {
		locate = func(authz.ResourceRef) Location { return Location{} }
	}
	return &Checker{cat: cat, subj: subj, locate: locate, locs: map[authz.ResourceRef]Location{}}
}

// Subject returns the compiled subject.
func (c *Checker) Subject() Subject { return c.subj }

// location resolves ref through locate. When locate does not know the
// resource, built-in rules apply: a service's parent is the stack named by
// its ID (authz.ServiceID), itself located; a resource named per
// environment lives in the environment of its reference, without parents.
func (c *Checker) location(ref authz.ResourceRef) Location {
	if loc, ok := c.locs[ref]; ok {
		return loc
	}
	loc := c.locate(ref)
	if !loc.Found {
		if ref.Type == catalog.TypeService {
			if stackID, _, ok := strings.Cut(ref.ID, "/"); ok && stackID != "" {
				stack := authz.ResourceRef{Type: catalog.TypeStack, ID: stackID}
				sl := c.location(stack)
				env := sl.EnvironmentID
				if env == "" {
					env = ref.EnvironmentID
				}
				loc = Location{Found: true, EnvironmentID: env, Parents: append([]authz.ResourceRef{stack}, sl.Parents...)}
			}
		} else if rt, ok := c.cat.Type(ref.Type); ok && rt.NamedPerEnvironment && ref.EnvironmentID != "" {
			loc = Location{Found: true, EnvironmentID: ref.EnvironmentID}
		}
	}
	c.locs[ref] = loc
	return loc
}

// Target converts a resource to an evaluator target, resolving its parents
// (and environment) through locate when the resource has no Parents.
func (c *Checker) Target(r authz.Resource) Target {
	t := Target{Type: r.Type, ID: r.ID, EnvironmentID: r.EnvironmentID}
	parents := r.Parents
	if parents == nil && r.ID != "" {
		loc := c.location(r.Ref())
		parents = loc.Parents
		if t.EnvironmentID == "" {
			t.EnvironmentID = loc.EnvironmentID
		}
	}
	for _, p := range parents {
		t.Parents = append(t.Parents, Ref{Type: p.Type, ID: p.ID, EnvironmentID: p.EnvironmentID})
	}
	return t
}

// Decide evaluates capability on r with the deciding rule (previews).
func (c *Checker) Decide(capability string, r authz.Resource) Decision {
	return Evaluate(c.cat, c.subj, capability, c.Target(r))
}

// Can implements authz.Checker; job resources use authz.EvaluateJob.
func (c *Checker) Can(capability string, r authz.Resource) authz.Decision {
	if r.Type == catalog.TypeJob {
		return authz.EvaluateJob(c.Can, capability, r)
	}
	d := c.Decide(capability, r)
	return authz.Decision{Allowed: d.Allowed, Reason: d.Reason}
}

// Reaches implements authz.Reacher: whether any granted capability applies
// at or below r — a grant on a container inside an environment or stack
// makes the environment or stack visible (minimal view) so the container
// can be found.
func (c *Checker) Reaches(r authz.Resource) bool {
	if c.subj.Inactive {
		return false
	}
	if c.subj.Owner && c.subj.Token == nil {
		return true
	}
	env := r.EnvironmentID
	if r.Type == catalog.TypeEnvironment {
		env = r.ID
	}
	for _, rule := range slices.Concat(c.subj.UserRules, c.subj.GroupRules, c.subj.Token) {
		if rule.Effect != Allow {
			continue
		}
		cp, ok := c.cat.Lookup(rule.Capability)
		if !ok || cp.OwnerOnly {
			continue
		}
		for _, point := range c.points(rule, cp, r, env) {
			if c.Can(rule.Capability, point).Allowed {
				return true
			}
		}
	}
	return false
}

// points are resources inside r that rule could apply to: a hypothetical
// new child of the capability's resource type (instance, environment and
// parent scopes), and the rule's own resource when it lies inside r.
func (c *Checker) points(rule Rule, cp catalog.Capability, r authz.Resource, env string) []authz.Resource {
	var out []authz.Resource
	ct, _ := c.cat.Type(cp.Type)
	switch {
	case r.Type == catalog.TypeEnvironment:
		if ct.EnvironmentBound && cp.Type != catalog.TypeEnvironment {
			out = append(out, authz.InEnvironment(cp.Type, env))
		}
	case slices.Contains(ct.Parents, r.Type) && r.ID != "":
		out = append(out, authz.Resource{Type: cp.Type, EnvironmentID: env, Parents: []authz.ResourceRef{r.Ref()}})
	}
	if rule.Scope.Kind != ScopeResource || (rule.Scope.ResourceType == r.Type && rule.Scope.ResourceID == r.ID) {
		return out
	}
	ref := authz.ResourceRef{Type: rule.Scope.ResourceType, ID: rule.Scope.ResourceID, EnvironmentID: rule.Scope.EnvironmentID}
	loc := c.location(ref)
	inside := false
	switch {
	case !loc.Found:
	case r.Type == catalog.TypeEnvironment:
		inside = loc.EnvironmentID == r.ID
	default:
		inside = slices.ContainsFunc(loc.Parents, func(p authz.ResourceRef) bool { return p.Type == r.Type && p.ID == r.ID })
	}
	if !inside {
		return out
	}
	if ref.Type != cp.Type {
		// A rule on a parent resource: probe a new child of the
		// capability's type inside it.
		return append(out, authz.Resource{Type: cp.Type, EnvironmentID: loc.EnvironmentID,
			Parents: append([]authz.ResourceRef{ref}, loc.Parents...)})
	}
	parents := loc.Parents
	if parents == nil {
		parents = []authz.ResourceRef{}
	}
	return append(out, authz.Resource{Type: ref.Type, ID: ref.ID, EnvironmentID: loc.EnvironmentID, Parents: parents})
}
