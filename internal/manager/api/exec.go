package api

import (
	"context"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
)

// AuthorizeExec is the shared authorization of container exec (terminal)
// routes (#8): the caller must hold container.exec on the container
// (authz.CanExec). API tokens (#31) are accepted, but only when
// container.exec is named in the token's own scope and the token's user
// holds it now. It returns the principal to record as the exec session's
// actor, or the API error to answer: 401 without a principal, 404 when the
// container is hidden from the caller, 403 when it is visible but exec is
// not granted.
func AuthorizeExec(ctx context.Context, a authz.Authorizer, container authz.Resource) (authz.Principal, error) {
	c, p, err := CheckerFor(ctx, a)
	if err != nil {
		return p, err
	}
	if authz.CanExec(c, container).Allowed {
		return p, nil
	}
	if !authz.ViewOf(c, container).Visible() {
		return p, NotFound("container not found")
	}
	if p.Kind == authz.KindAPIToken {
		return p, Forbidden("opening a terminal needs container.exec in the API token's own scope and the user's current permissions")
	}
	return p, Forbidden("opening a terminal needs the container.exec capability on this container")
}
