// Package authztest helps feature workstreams prove that their routes and
// streams obey the #17 permission model with the real evaluator:
//
//	pol := authztest.New().
//		Member("rita", "ops").
//		Group("ops", "allow container.restart @container:env-1/web")
//	h := authztest.Authenticate(apiMux) // api.New(mux, api.Deps{Authorizer: pol, ...})
//	calls := authztest.Routes(t, map[string]string{"environmentId": "env-1", "containerId": "web"},
//		"/api/v1/environments/{environmentId}/containers")
//	allowed, denied := authztest.Split(calls, "container.restart")
//	authztest.AssertOnly(t, h, "rita", allowed, denied)
//
// Policy is an in-memory rule set (groups, memberships, user overrides,
// token scopes, owner) evaluated by package policy exactly like the
// permission service. Authenticate turns the X-Authztest-User /
// X-Authztest-Token request headers into the request principal. Routes
// lists the implemented routes of api/route-inventory.yaml with their
// declared capabilities; Split partitions them by the granted capability;
// AssertOnly requires the allowed calls to pass authorization and every
// other call to fail with 403 or 404 (streams included: a denied stream
// never starts).
package authztest

import (
	"context"
	"sync"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
)

// Policy is an in-memory permission policy. Methods return the policy for
// chaining and may be called while requests run (the next check sees the
// change, like the permission service).
type Policy struct {
	mu       sync.Mutex
	cat      *catalog.Catalog
	owner    string
	member   map[string]string
	groups   map[string][]policy.Rule
	users    map[string][]policy.Rule
	tokens   map[string]tokenScope
	disabled map[string]bool
	locate   policy.LocateFunc
}

type tokenScope struct {
	user  string
	rules []policy.Rule
}

var _ interface {
	authz.Authorizer
	authz.Compiler
} = (*Policy)(nil)

// New returns an empty policy: everyone is denied (Restricted).
func New() *Policy {
	return &Policy{cat: catalog.Default(), member: map[string]string{}, groups: map[string][]policy.Rule{},
		users: map[string][]policy.Rule{}, tokens: map[string]tokenScope{}, disabled: map[string]bool{}}
}

// Only is a policy where user's group holds exactly rules (shorthand,
// e.g. "allow container.metrics.read @container:env-1/web").
func Only(user string, rules ...string) *Policy {
	return New().Member(user, "g-"+user).Group("g-"+user, rules...)
}

// Catalog replaces the catalog (tests of future keys).
func (p *Policy) Catalog(c *catalog.Catalog) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cat = c
	return p
}

// Owner makes user the instance owner (bypass).
func (p *Policy) Owner(user string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.owner = user
	return p
}

// Member puts user in group (exactly one group per user).
func (p *Policy) Member(user, group string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.member[user] = group
	return p
}

// Group replaces a group's rules (shorthand; panics on invalid rules).
func (p *Policy) Group(group string, rules ...string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.groups[group] = policy.MustParseRules(p.cat, rules...)
	return p
}

// User replaces a user's override rules; no rules resets to inherit.
func (p *Policy) User(user string, rules ...string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.users[user] = policy.MustParseRules(p.cat, rules...)
	return p
}

// Token defines an API token of user with an allow-only scope (#31).
func (p *Policy) Token(tokenID, user string, rules ...string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[tokenID] = tokenScope{user: user, rules: policy.MustParseRules(p.cat, rules...)}
	return p
}

// Disable marks user disabled (everything denied, like a queued job of a
// disabled user at dispatch).
func (p *Policy) Disable(user string) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled[user] = true
	return p
}

// Locate installs the resource graph (what a feature's permissions.Locator
// returns in production).
func (p *Policy) Locate(f policy.LocateFunc) *Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.locate = f
	return p
}

// Subject returns the compiled subject of a principal.
func (p *Policy) Subject(pr authz.Principal) policy.Subject {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !pr.Valid() || pr.IsService() {
		return policy.Subject{Inactive: true}
	}
	s := policy.Subject{Owner: pr.UserID == p.owner && p.owner != "", Inactive: p.disabled[pr.UserID],
		UserRules: append([]policy.Rule{}, p.users[pr.UserID]...), GroupRules: append([]policy.Rule{}, p.groups[p.member[pr.UserID]]...)}
	if pr.Kind == authz.KindAPIToken {
		s.Token = []policy.Rule{}
		if ts, ok := p.tokens[pr.TokenID]; ok && ts.user == pr.UserID {
			s.Token = append(s.Token, ts.rules...)
		} else {
			s.Ended = true // unknown or revoked, as in production
		}
	}
	return s
}

// Compile implements authz.Compiler.
func (p *Policy) Compile(_ context.Context, pr authz.Principal) (authz.Checker, error) {
	if pr.IsService() {
		return serviceChecker{}, nil
	}
	p.mu.Lock()
	cat, locate := p.cat, p.locate
	p.mu.Unlock()
	return policy.NewChecker(cat, p.Subject(pr), locate), nil
}

type serviceChecker struct{}

func (serviceChecker) Can(string, authz.Resource) authz.Decision {
	return authz.Allow("manager service identity")
}

// Can implements authz.Authorizer.
func (p *Policy) Can(ctx context.Context, pr authz.Principal, capability string, r authz.Resource) authz.Decision {
	c, _ := p.Compile(ctx, pr)
	return c.Can(capability, r)
}
