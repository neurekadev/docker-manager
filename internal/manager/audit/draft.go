package audit

import (
	"context"
	"slices"
	"sync"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// Draft is the audit record of the request being served. api.Register
// creates one for every audited operation and records it when the
// operation has answered; handlers enrich it through the context helpers
// below (all are no-ops when the context carries no draft, e.g. for
// unaudited GET operations).
type Draft struct {
	mu            sync.Mutex
	action        string
	allowed       []string
	actor         *domain.AuditActor
	environmentID string
	targets       []domain.AuditTarget
	details       map[string]any
	jobID         string
	errorClass    string
	outcome       domain.AuditOutcome
}

// NewDraft starts a draft with the operation's default action. allowed
// lists the concrete actions SetAction may choose (the CapabilityValues of
// a selector capability); empty means SetAction may not change it.
func NewDraft(action string, allowed []string) *Draft {
	return &Draft{action: action, allowed: slices.Clone(allowed), details: map[string]any{}}
}

type draftKey struct{}

// WithDraft returns ctx carrying d.
func WithDraft(ctx context.Context, d *Draft) context.Context {
	return context.WithValue(ctx, draftKey{}, d)
}

// DraftFrom returns the request's draft, if the operation is audited.
func DraftFrom(ctx context.Context) (*Draft, bool) {
	d, ok := ctx.Value(draftKey{}).(*Draft)
	return d, ok && d != nil
}

func with(ctx context.Context, fn func(d *Draft)) {
	if d, ok := DraftFrom(ctx); ok {
		d.mu.Lock()
		defer d.mu.Unlock()
		fn(d)
	}
}

// SetAction picks the concrete action of a selector-capability operation
// (for example stack.stop for "stack.{action}"). It reports false and
// keeps the default when action is not one of the operation's values.
func SetAction(ctx context.Context, action string) bool {
	ok := false
	with(ctx, func(d *Draft) {
		if slices.Contains(d.allowed, action) {
			d.action, ok = action, true
		}
	})
	return ok
}

// SetActor sets the actor explicitly (a sign-in handler that just
// established the principal, an agent route).
func SetActor(ctx context.Context, a domain.AuditActor) {
	with(ctx, func(d *Draft) { d.actor = &a })
}

// SetPrincipal sets the actor from an authorization principal.
func SetPrincipal(ctx context.Context, p authz.Principal) { SetActor(ctx, ActorFor(p)) }

// SetEnvironment sets the environment ("host") the action ran in when the
// path does not name it.
func SetEnvironment(ctx context.Context, environmentID string) {
	with(ctx, func(d *Draft) { d.environmentID = environmentID })
}

// AddTarget adds a resource the action touched (for example the ID of a
// created resource, or every container of a stack operation). Path
// parameters are added automatically.
func AddTarget(ctx context.Context, t domain.AuditTarget) {
	with(ctx, func(d *Draft) { d.targets = append(d.targets, t) })
}

// SetDetail adds a details member. Values pass the redaction layer; record
// identifiers, counts, names and paths, never secret values or contents.
func SetDetail(ctx context.Context, key string, value any) {
	with(ctx, func(d *Draft) { d.details[key] = value })
}

// SetDiff records the before/after state of an edit (for example the rule
// sets of a permission change, #17) under details.diff.
func SetDiff(ctx context.Context, before, after any) {
	with(ctx, func(d *Draft) { d.details["diff"] = map[string]any{"before": before, "after": after} })
}

// SetJob links the record to a job (job-starting operations returning
// api.Accepted are linked automatically).
func SetJob(ctx context.Context, jobID string) {
	with(ctx, func(d *Draft) { d.jobID = jobID })
}

// SetErrorClass sets a stable error class (API error codes are captured
// automatically).
func SetErrorClass(ctx context.Context, class string) {
	with(ctx, func(d *Draft) { d.errorClass = class })
}

// SetOutcome overrides the outcome derived from the response status (for
// example a 200 answer that reports a refused step).
func SetOutcome(ctx context.Context, o domain.AuditOutcome) {
	with(ctx, func(d *Draft) { d.outcome = o })
}

// Snapshot returns the draft's contribution to the record: the action,
// the explicit actor (nil when unset) and the enrichment fields.
func (d *Draft) Snapshot() (action string, actor *domain.AuditActor, ev domain.AuditEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	details := make(map[string]any, len(d.details))
	for k, v := range d.details {
		details[k] = v
	}
	var a *domain.AuditActor
	if d.actor != nil {
		cp := *d.actor
		a = &cp
	}
	return d.action, a, domain.AuditEvent{
		EnvironmentID: d.environmentID, Targets: slices.Clone(d.targets), Details: details,
		JobID: d.jobID, ErrorClass: d.errorClass, Outcome: d.outcome,
	}
}

// HasErrorClass reports whether an error class was set.
func (d *Draft) HasErrorClass() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.errorClass != ""
}
