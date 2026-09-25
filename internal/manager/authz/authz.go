// Package authz holds the manager's authorization contract (#17): the
// principal carried in a request context, the Resource a capability is
// checked on (with its scope chain), the Authorizer interface every feature
// calls, per-request Checkers and the response-shaping helpers (View).
//
// Rules (#17, #26):
//   - Everything is denied unless an Authorizer explicitly allows it; the
//     fallback Authorizer is DenyAll, so routes fail closed (401 without a
//     principal, 403/404 without a grant). The identity layer (#16,
//     internal/manager/auth) sets request principals; the permission
//     service (internal/manager/permissions) is the Authorizer: owner
//     bypass, then user rules, then group rules, then deny (package
//     policy). Capability keys live in package catalog.
//   - The manager service identity (scheduled jobs) is not an HTTP
//     principal; it can only be created in-process via Service().
//   - Initiators are audit metadata; authorization is by capability and
//     resource, never by creator identity.
package authz

import (
	"context"
	"errors"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
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

// Resource is what a capability is checked against. Types are the
// catalog's resource types (catalog.TypeContainer, ...); "instance" for
// manager-wide capabilities.
type Resource struct {
	Type string
	// ID is the resource's identity: a DockYard ID (stacks, agents,
	// policies), or the Docker name within its environment (containers,
	// images, volumes, networks). Empty means "any resource of Type in
	// EnvironmentID" (creation routes): only instance and environment
	// rules apply.
	ID string
	// EnvironmentID is the environment the resource currently lives in;
	// empty for instance resources (backup repositories, registries).
	EnvironmentID string
	// Parents are the resources containing this one, nearest first (a
	// container's service, then its stack; service IDs are
	// ServiceID(stackID, name)). Rules on parents apply to the resource.
	// When nil, the permission service asks the Locator registered for
	// Type; use an empty non-nil slice for "no parents".
	Parents []ResourceRef
	// Targets are the resources a job acts on (#17: job visibility follows
	// the job's targets). Set by JobResource.
	Targets []Resource
	// JobCapabilities are the capabilities the job's kind needs on its
	// targets; holding them also shows and cancels the job. Set by
	// JobResource.
	JobCapabilities []string
}

// Ref returns the resource's reference.
func (r Resource) Ref() ResourceRef {
	return ResourceRef{Type: r.Type, ID: r.ID, EnvironmentID: r.EnvironmentID}
}

// Instance is the resource of manager-wide capabilities.
func Instance() Resource { return Resource{Type: catalog.TypeInstance, Parents: []ResourceRef{}} }

// EnvironmentResource is an environment (environment-scoped capabilities).
func EnvironmentResource(id string) Resource {
	return Resource{Type: catalog.TypeEnvironment, ID: id, EnvironmentID: id, Parents: []ResourceRef{}}
}

// InEnvironment is "any (new) resource of typ in the environment", for
// creation routes such as stack.create or image.pull: only instance and
// environment rules apply.
func InEnvironment(typ, environmentID string) Resource {
	return Resource{Type: typ, EnvironmentID: environmentID, Parents: []ResourceRef{}}
}

// ServiceID is the resource ID of a Compose service of a stack.
func ServiceID(stackID, service string) string { return stackID + "/" + service }

// targetType maps job target types to catalog resource types.
func targetType(t domain.TargetType) string {
	if t == domain.TargetRepository {
		return catalog.TypeBackupRepository
	}
	return string(t)
}

// TargetResources are the resources a job with these targets is authorized
// on (the operation's full effect, #17): every target, except that file
// paths inside a stack or volume root are covered by that root (the file
// service enforces containment) and the images a build definition run
// tags are covered by the definition (its tags are part of the definition,
// managed with build_definition.manage, #33). Repository targets are
// instance resources. A job without targets is authorized on its
// environment (or the instance).
func TargetResources(environmentID string, targets []domain.JobTarget) []Resource {
	hasRoot, hasDefinition := false, false
	for _, t := range targets {
		if t.Type == domain.TargetStack || t.Type == domain.TargetVolume {
			hasRoot = true
		}
		if t.Type == domain.TargetBuildDefinition {
			hasDefinition = true
		}
	}
	var out []Resource
	for _, t := range targets {
		if hasRoot && (t.Type == domain.TargetPath || t.Type == domain.TargetDestinationPath) {
			continue
		}
		if hasDefinition && t.Type == domain.TargetImage {
			continue
		}
		env := t.EnvironmentID
		if env == "" {
			env = environmentID
		}
		if t.Type == domain.TargetRepository {
			env = ""
		}
		out = append(out, Resource{Type: targetType(t.Type), ID: t.ID, EnvironmentID: env})
	}
	if len(out) == 0 {
		if environmentID == "" {
			return []Resource{Instance()}
		}
		return []Resource{EnvironmentResource(environmentID)}
	}
	return out
}

// JobResource is the authorization resource of a job: job.read and
// job.cancel are evaluated against the resources it targets, never against
// its initiator (#17). Holding the kind's own capabilities on every target
// counts too (EvaluateJob).
func JobResource(j domain.Job) Resource {
	r := Resource{Type: catalog.TypeJob, ID: j.ID, EnvironmentID: j.EnvironmentID, Parents: []ResourceRef{},
		Targets: TargetResources(j.EnvironmentID, j.Targets)}
	if spec, ok := jobspec.Lookup(j.Kind); ok {
		if caps, err := spec.Capabilities(j.Targets, j.Input); err == nil {
			r.JobCapabilities = caps
		}
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

// DenyAll denies everything: the fallback when no Authorizer is configured.
type DenyAll struct{}

// Can implements Authorizer.
func (DenyAll) Can(context.Context, Principal, string, Resource) Decision {
	return Deny("no authorization policy configured")
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

// Causes of request contexts cancelled by the identity layer (context
// cause): streams report them to the client before closing (CloseReason).
var (
	// ErrPermissionsChanged: the caller's effective permissions changed
	// (#17 rule edits, group moves); reconnect to be filtered anew.
	ErrPermissionsChanged = errors.New("authz: the caller's permissions changed")
	// ErrSessionEnded: the caller's session was revoked, expired or the
	// account disabled.
	ErrSessionEnded = errors.New("authz: the caller's session ended")
)

// CloseReason is the stream close reason (docs/api/streams.md) of a request
// context ended by the identity layer: "permissions_changed",
// "session_expired", or "" when the context is live or ended otherwise.
func CloseReason(ctx context.Context) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, ErrPermissionsChanged):
		return "permissions_changed"
	case errors.Is(cause, ErrSessionEnded):
		return "session_expired"
	}
	return ""
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
