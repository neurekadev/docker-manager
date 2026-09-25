package policy

import (
	"fmt"
	"strings"

	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
)

// Rule shorthand, used by the decision corpus, authztest and audit diffs:
//
//	"<allow|deny> <capability> @<scope>"
//
// where scope is "all" (instance), "env:<id>", "<type>:<id>" for resources
// with a global ID (stack, service, agent, ...) or "<type>:<env>/<name>" for
// resources named per environment (container, image, volume, network).

// Shorthand renders r in rule shorthand.
func (r Rule) Shorthand() string {
	scope := "all"
	switch r.Scope.Kind {
	case ScopeEnvironment:
		scope = "env:" + r.Scope.EnvironmentID
	case ScopeResource:
		scope = r.Scope.ResourceType + ":"
		if r.Scope.EnvironmentID != "" {
			scope += r.Scope.EnvironmentID + "/"
		}
		scope += r.Scope.ResourceID
	}
	return string(r.Effect) + " " + r.Capability + " @" + scope
}

// ParseRule parses rule shorthand. It does not validate the rule against
// the catalog (Validate does); cat tells which types are named per
// environment.
func ParseRule(cat *catalog.Catalog, s string) (Rule, error) {
	f := strings.Fields(s)
	if len(f) != 3 || !strings.HasPrefix(f[2], "@") {
		return Rule{}, fmt.Errorf("rule %q: want \"<allow|deny> <capability> @<scope>\"", s)
	}
	r := Rule{Effect: Effect(f[0]), Capability: f[1]}
	scope := strings.TrimPrefix(f[2], "@")
	switch {
	case scope == "all":
		r.Scope = Instance()
	case strings.HasPrefix(scope, "env:"):
		r.Scope = Environment(strings.TrimPrefix(scope, "env:"))
	default:
		typ, id, ok := strings.Cut(scope, ":")
		if !ok || id == "" {
			return Rule{}, fmt.Errorf("rule %q: scope must be all, env:<id>, <type>:<id> or <type>:<env>/<name>", s)
		}
		env := ""
		if rt, _ := cat.Type(typ); rt.NamedPerEnvironment {
			if env, id, ok = strings.Cut(id, "/"); !ok || env == "" || id == "" {
				return Rule{}, fmt.Errorf("rule %q: %s scopes are %s:<env>/<name>", s, typ, typ)
			}
		}
		r.Scope = Resource(typ, env, id)
	}
	return r, nil
}

// MustParseRules parses and validates shorthand rules, panicking on error
// (tests and fixtures).
func MustParseRules(cat *catalog.Catalog, rules ...string) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, s := range rules {
		r, err := ParseRule(cat, s)
		if err != nil {
			panic(err)
		}
		out = append(out, r)
	}
	if errs := Validate(cat, out); len(errs) > 0 {
		panic(fmt.Sprintf("policy: invalid rules %v: %v", rules, errs))
	}
	return out
}
