// Package policy is Docker Manager's deterministic authorization evaluator (#17,
// ADR 0003: no Casbin). It is a pure function over one principal's rules
// and the requested resource's scope chain:
//
//  1. The instance owner is allowed everything (owner bypass).
//  2. API tokens (#31): the token scope must cover the request (token scope
//     ∩ the user's effective permissions).
//  3. Owner-only and unknown capabilities are denied.
//  4. The most specific matching user rule decides (allow or deny).
//  5. Otherwise the most specific matching group rule decides.
//  6. Otherwise deny.
//
// Specificity within one tier: the exact resource beats its parents
// (container > service > stack), which beat the environment, which beats
// instance-wide ("all resources") rules. A user rule always beats a group
// rule, even a more specific one. Validate rejects duplicate rules for the
// same capability and scope, so there is never a tie.
//
// Rules name one capability key; there are no wildcard keys, so a rule on
// all resources applies to future resources of that type but never grants
// a capability key added to the catalog later. Rules on a parent (a stack
// or service) apply to children only for the capabilities they name.
package policy

import (
	"fmt"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
)

// Effect is a rule's effect.
type Effect string

// Effects.
const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
)

// ScopeKind is where a rule applies.
type ScopeKind string

// Scope kinds.
const (
	ScopeInstance    ScopeKind = "instance"
	ScopeEnvironment ScopeKind = "environment"
	ScopeResource    ScopeKind = "resource"
)

// Scope is the extent of a rule.
type Scope struct {
	Kind ScopeKind
	// EnvironmentID is set for environment scopes, and for resource scopes
	// of resource types named per environment (containers, images,
	// volumes, networks).
	EnvironmentID string
	ResourceType  string
	ResourceID    string
}

// Instance returns the all-resources scope.
func Instance() Scope { return Scope{Kind: ScopeInstance} }

// Environment returns the scope of one environment.
func Environment(id string) Scope { return Scope{Kind: ScopeEnvironment, EnvironmentID: id} }

// Resource returns the scope of one resource. environmentID is required for
// resource types named per environment and must be empty otherwise.
func Resource(typ, environmentID, id string) Scope {
	return Scope{Kind: ScopeResource, ResourceType: typ, EnvironmentID: environmentID, ResourceID: id}
}

// Key is a stable identity of the scope (duplicate detection, storage).
func (s Scope) Key() string {
	return string(s.Kind) + "|" + s.EnvironmentID + "|" + s.ResourceType + "|" + s.ResourceID
}

// String renders the scope for reasons and audit.
func (s Scope) String() string {
	switch s.Kind {
	case ScopeInstance:
		return "all resources"
	case ScopeEnvironment:
		return "environment " + s.EnvironmentID
	case ScopeResource:
		out := s.ResourceType + " " + s.ResourceID
		if s.EnvironmentID != "" {
			out += " in environment " + s.EnvironmentID
		}
		return out
	}
	return string(s.Kind)
}

// Rule allows or denies one capability at one scope.
type Rule struct {
	Capability string
	Scope      Scope
	Effect     Effect
}

// String renders the rule.
func (r Rule) String() string {
	return string(r.Effect) + " " + r.Capability + " on " + r.Scope.String()
}

// Ref identifies a resource in a scope chain.
type Ref struct {
	Type string
	ID   string
	// EnvironmentID of the resource; empty means the target's environment.
	EnvironmentID string
}

// Target is the resource a capability is checked on.
type Target struct {
	Type string
	// ID is empty for "any (future) resource of Type in EnvironmentID"
	// (creation routes, reachability probes): only instance and
	// environment rules can match it.
	ID            string
	EnvironmentID string
	// Parents are the containing resources, nearest first (a container's
	// service, then its stack). Rules on them apply to the target.
	Parents []Ref
}

// Subject is what the evaluator knows about a principal.
type Subject struct {
	// Owner: the instance owner (bypass).
	Owner bool
	// Inactive: the account is disabled or deleted: everything is denied
	// (a queued job of a disabled user is rejected at dispatch).
	Inactive   bool
	UserRules  []Rule
	GroupRules []Rule
	// Token is the scope of an API token (#31): allow-only grants. nil for
	// sessions; an empty non-nil slice denies everything.
	Token []Rule
}

// Source says which part of the evaluation decided.
type Source string

// Decision sources.
const (
	SourceOwner     Source = "owner"
	SourceUser      Source = "user_rule"
	SourceGroup     Source = "group_rule"
	SourceDefault   Source = "default_deny"
	SourceToken     Source = "token_scope"
	SourceUnknown   Source = "unknown_capability"
	SourceOwnerOnly Source = "owner_only"
	SourceInactive  Source = "inactive_account"
)

// Decision is an evaluation result with the rule that decided it.
type Decision struct {
	Allowed bool
	Source  Source
	// Rule is the deciding rule (user or group sources).
	Rule *Rule
	// Reason is a short, non-sensitive explanation for previews and logs.
	Reason string
}

// chainEntry is one resource of the target's scope chain.
type chainEntry struct {
	typ, id, env string
}

func chain(t Target) []chainEntry {
	out := make([]chainEntry, 0, 1+len(t.Parents))
	if t.ID != "" {
		out = append(out, chainEntry{t.Type, t.ID, t.EnvironmentID})
	}
	for _, p := range t.Parents {
		env := p.EnvironmentID
		if env == "" {
			env = t.EnvironmentID
		}
		out = append(out, chainEntry{p.Type, p.ID, env})
	}
	return out
}

// specificity of rule scope s for target chain c; -1 when s does not match.
func specificity(cat *catalog.Catalog, s Scope, t Target, c []chainEntry) int {
	switch s.Kind {
	case ScopeInstance:
		return 0
	case ScopeEnvironment:
		if t.EnvironmentID != "" && s.EnvironmentID == t.EnvironmentID {
			return 1
		}
		return -1
	case ScopeResource:
		named := false
		if rt, ok := cat.Type(s.ResourceType); ok {
			named = rt.NamedPerEnvironment
		}
		for i, e := range c {
			if e.typ != s.ResourceType || e.id != s.ResourceID {
				continue
			}
			if named && e.env != s.EnvironmentID {
				continue
			}
			return 2 + len(c) - 1 - i
		}
	}
	return -1
}

// best returns the most specific rule for capability among rules.
func best(cat *catalog.Catalog, rules []Rule, capability string, t Target, c []chainEntry) *Rule {
	var found *Rule
	bestSpec := -1
	for i := range rules {
		r := &rules[i]
		if r.Capability != capability {
			continue
		}
		if s := specificity(cat, r.Scope, t, c); s > bestSpec {
			found, bestSpec = r, s
		}
	}
	return found
}

// Evaluate decides whether s may use capability on t.
func Evaluate(cat *catalog.Catalog, s Subject, capability string, t Target) Decision {
	if s.Inactive {
		return Decision{Source: SourceInactive, Reason: "the account is disabled or deleted"}
	}
	c := chain(t)
	if s.Token != nil {
		r := best(cat, s.Token, capability, t, c)
		if r == nil || r.Effect != Allow {
			return Decision{Source: SourceToken, Reason: "outside the API token's scope"}
		}
	}
	if s.Owner {
		return Decision{Allowed: true, Source: SourceOwner, Reason: "instance owner"}
	}
	cp, ok := cat.Lookup(capability)
	if !ok {
		return Decision{Source: SourceUnknown, Reason: "capability " + capability + " is not in permission catalog v" + fmt.Sprint(cat.Version())}
	}
	if cp.OwnerOnly {
		return Decision{Source: SourceOwnerOnly, Reason: capability + " is reserved to the instance owner"}
	}
	if r := best(cat, s.UserRules, capability, t, c); r != nil {
		return Decision{Allowed: r.Effect == Allow, Source: SourceUser, Rule: r, Reason: "user rule: " + r.String()}
	}
	if r := best(cat, s.GroupRules, capability, t, c); r != nil {
		return Decision{Allowed: r.Effect == Allow, Source: SourceGroup, Rule: r, Reason: "group rule: " + r.String()}
	}
	return Decision{Source: SourceDefault, Reason: "no rule grants " + capability + " here (default deny)"}
}

// FieldError is one invalid rule.
type FieldError struct {
	// Index of the rule in the submitted list.
	Index   int
	Message string
}

func (e FieldError) Error() string { return fmt.Sprintf("rule %d: %s", e.Index, e.Message) }

// Validate checks rules against the catalog: known, grantable capabilities,
// valid effects, scopes the capability supports, well-formed scope
// identities, and no two rules for the same capability and scope (an
// ambiguous duplicate, even with the same effect).
func Validate(cat *catalog.Catalog, rules []Rule) []FieldError {
	var errs []FieldError
	seen := map[string]int{}
	for i, r := range rules {
		fail := func(format string, args ...any) {
			errs = append(errs, FieldError{Index: i, Message: fmt.Sprintf(format, args...)})
		}
		cp, ok := cat.Lookup(r.Capability)
		switch {
		case !ok:
			fail("unknown capability %q (permission catalog v%d)", r.Capability, cat.Version())
			continue
		case cp.OwnerOnly:
			fail("%s is reserved to the instance owner and cannot be granted", r.Capability)
			continue
		}
		if r.Effect != Allow && r.Effect != Deny {
			fail("effect must be allow or deny")
		}
		s := r.Scope
		switch s.Kind {
		case ScopeInstance:
			if s.EnvironmentID != "" || s.ResourceType != "" || s.ResourceID != "" {
				fail("an instance scope names no environment or resource")
			} else if !cp.Instance {
				fail("%s cannot be granted on all resources", r.Capability)
			}
		case ScopeEnvironment:
			if s.EnvironmentID == "" || s.ResourceType != "" || s.ResourceID != "" {
				fail("an environment scope names exactly one environment")
			} else if !cp.Environment {
				fail("%s cannot be granted per environment", r.Capability)
			}
		case ScopeResource:
			rt, known := cat.Type(s.ResourceType)
			switch {
			case s.ResourceID == "" || !known:
				fail("a resource scope needs a known resource type and an ID")
			case !cp.AllowsScope(string(ScopeResource), s.ResourceType):
				fail("%s cannot be granted on a single %s (compatible: %s)", r.Capability, s.ResourceType, strings.Join(cp.Resources, ", "))
			case rt.NamedPerEnvironment && s.EnvironmentID == "":
				fail("a %s is identified by its environment and name: set the environment", s.ResourceType)
			case !rt.NamedPerEnvironment && s.EnvironmentID != "":
				fail("a %s has a global ID: do not set an environment (its rules follow it when it moves)", s.ResourceType)
			}
		default:
			fail("scope kind must be instance, environment or resource")
		}
		k := r.Capability + "#" + s.Key()
		if j, dup := seen[k]; dup {
			fail("duplicates rule %d (same capability and scope); keep one", j)
		} else {
			seen[k] = i
		}
	}
	return errs
}
