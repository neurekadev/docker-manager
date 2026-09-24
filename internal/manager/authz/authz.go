// Package authz holds the manager's authorization hooks until the #17
// evaluator lands: the principal carried in a request context, the
// Authorizer interface every feature calls, and a deny-all default.
//
// Rules (#17, #26):
//   - Everything is denied unless an Authorizer explicitly allows it; the
//     default Authorizer is DenyAll, so routes fail closed (401 without a
//     principal, 403/404 without a grant) until #16/#17 wire real principals
//     and grants.
//   - The manager service identity (scheduled jobs) is not an HTTP
//     principal; it can only be created in-process via Service().
//   - Initiators are audit metadata; authorization is by capability and
//     resource, never by creator identity.
package authz

import (
	"context"
	"errors"

	"github.com/neurekadev/dockyard/internal/domain"
)

// PrincipalKind distinguishes the callers the manager knows.
type PrincipalKind string

// Principal kinds.
const (
	KindUser     PrincipalKind = "user"
	KindAPIToken PrincipalKind = "api_token"
	KindService  PrincipalKind = "service"
)

// Principal is an authenticated caller.
type Principal struct {
	Kind PrincipalKind
	// UserID is the user (for api_token: the token's owner).
	UserID string
	// TokenID is set for API tokens.
	TokenID string
}

// Service returns the manager's internal service identity, used for
// scheduled jobs. Never derive it from request data.
func Service() Principal { return Principal{Kind: KindService} }

// IsService reports whether p is the service identity.
func (p Principal) IsService() bool { return p.Kind == KindService }

// Valid reports whether p is well-formed.
func (p Principal) Valid() bool {
	switch p.Kind {
	case KindUser:
		return p.UserID != "" && p.TokenID == ""
	case KindAPIToken:
		return p.UserID != "" && p.TokenID != ""
	case KindService:
		return p.UserID == "" && p.TokenID == ""
	}
	return false
}

// Key is a stable string identifying the principal (idempotency scope).
func (p Principal) Key() string {
	switch p.Kind {
	case KindUser:
		return "user:" + p.UserID
	case KindAPIToken:
		return "token:" + p.TokenID
	case KindService:
		return "service"
	}
	return ""
}

// ResourceRef references one resource.
type ResourceRef struct {
	Type          string
	ID            string
	EnvironmentID string
}

// Resource is what a capability is checked against: the resource itself
// plus, for jobs, the resources the job targets.
type Resource struct {
	Type string
	ID   string
	// EnvironmentID is empty for instance-scoped resources.
	EnvironmentID string
	// Targets are the resources a job acts on (#17: job visibility follows
	// the job's targets).
	Targets []ResourceRef
}

// JobResource is the authorization resource of a job: job.read and
// job.cancel are evaluated against the job and the resources it targets,
// never against its initiator (#17).
func JobResource(j domain.Job) Resource {
	r := Resource{Type: "job", ID: j.ID, EnvironmentID: j.EnvironmentID}
	for _, t := range j.Targets {
		env := t.EnvironmentID
		if env == "" {
			env = j.EnvironmentID
		}
		r.Targets = append(r.Targets, ResourceRef{Type: string(t.Type), ID: t.ID, EnvironmentID: env})
	}
	return r
}

// Decision is an authorization result.
type Decision struct {
	Allowed bool
	// Reason is a short, non-sensitive explanation for logs and audit.
	Reason string
}

// Allow and Deny build decisions.
func Allow(reason string) Decision { return Decision{Allowed: true, Reason: reason} }

// Deny builds a denying decision.
func Deny(reason string) Decision { return Decision{Reason: reason} }

// Authorizer decides whether a principal may use a capability on a resource.
type Authorizer interface {
	Can(ctx context.Context, p Principal, capability string, r Resource) Decision
}

// DenyAll denies everything. It is the default until #17.
type DenyAll struct{}

// Can implements Authorizer.
func (DenyAll) Can(context.Context, Principal, string, Resource) Decision {
	return Deny("no authorization policy configured (#17)")
}

// Func adapts a function to Authorizer (tests, wiring).
type Func func(ctx context.Context, p Principal, capability string, r Resource) Decision

// Can implements Authorizer.
func (f Func) Can(ctx context.Context, p Principal, capability string, r Resource) Decision {
	return f(ctx, p, capability, r)
}

// OrDenyAll returns a, or DenyAll when a is nil.
func OrDenyAll(a Authorizer) Authorizer {
	if a == nil {
		return DenyAll{}
	}
	return a
}

type ctxKey struct{}

// WithPrincipal returns ctx carrying the authenticated principal. Only the
// authentication middleware (#16/#31) and tests may call it; the service
// identity is rejected so it can never enter through HTTP.
func WithPrincipal(ctx context.Context, p Principal) (context.Context, error) {
	if !p.Valid() || p.IsService() {
		return ctx, errors.New("authz: invalid request principal")
	}
	return context.WithValue(ctx, ctxKey{}, p), nil
}

// PrincipalFrom returns the request principal, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.Valid() && !p.IsService()
}
