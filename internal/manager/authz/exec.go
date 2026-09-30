package authz

import "github.com/neurekadev/docker-manager/internal/manager/authz/catalog"

// CapContainerExec is the capability of interactive exec sessions
// (terminals) in a container (#8).
const CapContainerExec = "container.exec"

// CanExec decides whether the caller of checker c may open an exec session
// in container r. It is the one exec check (#8 calls it for the terminal
// WebSocket; api.AuthorizeExec wraps it with the HTTP answers):
//
//   - exec is its own capability: no other grant implies it (restart,
//     logs, details, files, all-container rules of other keys);
//   - for an API token (#31), container.exec must be named in the token's
//     own scope, chosen explicitly at creation, AND be held by the token's
//     user right now (token scope ∩ current permissions); a token scope
//     never inherits exec from anything else;
//   - the owner's sessions always may; the owner's tokens only with
//     container.exec in their scope.
func CanExec(c Checker, r Resource) Decision {
	if r.Type != catalog.TypeContainer {
		return Deny("exec applies to containers only")
	}
	return c.Can(CapContainerExec, r)
}
