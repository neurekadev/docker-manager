package api

import (
	"context"

	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// Response shaping (#17). The contract every resource route follows:
//
//  1. Build one checker per request: c := CheckerFor(ctx, deps.Authorizer)
//     (or authz.For). It loads the caller's rules once; never call the
//     Authorizer per list item.
//  2. Compute the view of each resource: v := authz.ViewOf(c, resource).
//     Hidden resources are dropped from lists, searches, counts and event
//     streams, and answer 404 on direct access (existence does not leak).
//  3. Full view (the type's read capability, e.g. container.details.read):
//     return the whole DTO. Minimal view (any other capability applying to
//     the resource, or a grant on something inside it): return only the
//     type's minimal fields (catalog.ResourceType.Minimal: identity and
//     status) plus view/actions. Every resource DTO carries
//     `view` ("minimal"|"full") and `actions` (Actions(v): the granted
//     capability keys) so the UI shows exactly those actions.
//  4. Actions still check their own capability (v.Has(key) or c.Can): a
//     visible resource is not an authorized action. Answer 403 when the
//     resource is visible but the action is not granted.
//  5. Counts/aggregates count visible items only; event streams filter
//     with authz.EventVisible; job lists use c.Can(job.read, JobResource).
//
// Tests for a route use package authz/authztest (AssertOnly, Routes).

// CheckerFor returns the caller's per-request checker, or 401.
func CheckerFor(ctx context.Context, a authz.Authorizer) (authz.Checker, authz.Principal, error) {
	p, ok := authz.PrincipalFrom(ctx)
	if !ok {
		return nil, p, Unauthenticated("authentication required")
	}
	return authz.For(ctx, a, p), p, nil
}

// Actions returns the granted actions of a view for a DTO (never nil).
func Actions(v authz.View) []string {
	if v.Actions == nil {
		return []string{}
	}
	return append([]string{}, v.Actions...)
}
