package api

import "net/http"

// ErrorCode documents one stable error code of the public API. The catalog
// below is the source of docs/api/errors.md (TestErrorCatalogDocumented) and
// every code literal used in this package must be listed here
// (TestErrorCodesCatalogued). Feature workstreams add their specific codes
// (e.g. stack_name_taken) to this list in the same change that introduces
// them. Codes are never renamed or reused for a different meaning.
type ErrorCode struct {
	Code string
	// Status is the HTTP status the code is returned with.
	Status int
	// Retryable is the default retryable flag of the code.
	Retryable bool
	// Meaning is the one-line description published in docs/api/errors.md.
	Meaning string
	// Owner is the issue that introduced the code.
	Owner int
}

// ErrorCodes returns the catalog of stable error codes, grouped by status.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		{CodeBadRequest, http.StatusBadRequest, false, "The request is malformed (unparsable JSON, wrong content encoding).", 2},
		{CodeInvalidCode, http.StatusBadRequest, false, "The one-time code (invitation, password reset or owner recovery) is unknown, expired, revoked or already used. The response never says which.", 16},
		{CodeUnauthenticated, http.StatusUnauthorized, false, "No valid session cookie or API token; sign in again or send a valid bearer token.", 2},
		{CodeInvalidCredentials, http.StatusUnauthorized, false, "Sign-in, second factor or step-up failed: unknown account, wrong password or code, disabled account and bad passkey assertions all look alike.", 16},
		{CodeForbidden, http.StatusForbidden, false, "Authenticated, but the named capability is not granted for this resource. Returned only when the caller may know the resource exists; otherwise not_found.", 2},
		{CodeInsecureOrigin, http.StatusForbidden, false, "The request did not reach DockYard over HTTPS on DOCKYARD_PUBLIC_URL (first-run setup); the message explains how to fix the proxy or URL.", 16},
		{CodeCrossOriginRequest, http.StatusForbidden, false, "A browser sent an unsafe request from another origin (cross-site request forgery protection).", 16},
		{CodeStepUpRequired, http.StatusForbidden, false, "The change needs recent authentication; re-authenticate with POST /api/v1/auth/step-ups and retry.", 16},
		{CodeEnrollmentRequired, http.StatusForbidden, false, "The session may only enroll the sign-in factors the instance policy requires; finish enrollment first.", 16},
		{CodeEnrollmentExpired, http.StatusForbidden, false, "The grace period to enroll required sign-in factors has passed; ask the instance owner for a factor or password reset.", 16},
		{CodeSignInMethodNotAllowed, http.StatusForbidden, false, "The instance sign-in policy does not accept this sign-in method (for example a passkey when password and TOTP are required).", 16},
		{CodeNotFound, http.StatusNotFound, false, "The resource or route does not exist, or the caller may not know that it exists.", 2},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed, false, "The route exists but not with this method; see the Allow header.", 2},
		{CodeNotAcceptable, http.StatusNotAcceptable, false, "The Accept header excludes every media type the route can produce.", 2},
		{CodeConflict, http.StatusConflict, false, "Generic conflict with the current state. Routes prefer a specific 409 code.", 2},
		{CodeJobFinished, http.StatusConflict, false, "The job already reached a terminal state (for example a cancellation of a finished job).", 26},
		{CodeIdempotencyKeyReused, http.StatusConflict, false, "The Idempotency-Key was already used by this caller for a different request (different route, parameters or body).", 26},
		{CodeIdempotencyKeyInFlight, http.StatusConflict, true, "A request with the same Idempotency-Key is still being processed; retry after the Retry-After delay.", 4},
		{CodeSetupComplete, http.StatusConflict, false, "First-run setup already created the instance owner; sign in instead.", 16},
		{CodeUsernameTaken, http.StatusConflict, false, "Another account already uses this username.", 16},
		{CodeOwnerProtected, http.StatusConflict, false, "The instance owner cannot be disabled, deleted or reset through this route (use owner recovery).", 16},
		{CodeFactorRequired, http.StatusConflict, false, "Removing this factor would leave the account unable to satisfy the instance sign-in policy.", 16},
		{CodeTOTPAlreadyEnabled, http.StatusConflict, false, "TOTP is already enabled; remove it before enrolling a new secret.", 16},
		{CodeInvitationRedeemed, http.StatusConflict, false, "The invitation was already redeemed and can no longer be revoked.", 16},
		{CodeNoPendingFlow, http.StatusConflict, false, "No sign-in, TOTP enrollment or passkey ceremony is in progress in this session (or it expired); start again.", 16},
		{CodeEngineAlreadyEnrolled, http.StatusConflict, false, "Agent enrollment: the Docker Engine already has an active agent (one agent per Engine); enroll with intent replace:<agentId> to move to the new agent.", 3},
		{CodeEngineIdentityConflict, http.StatusConflict, false, "Agent enrollment: the Engine ID is already enrolled from another host (a cloned machine?); replace the agent, regenerate the clone's Engine ID or allow the duplicate Engine ID.", 3},
		{CodeEnvironmentArchived, http.StatusConflict, false, "The environment is archived: it cannot be edited, and enrolling its Engine needs intent reattach:<environmentId>.", 3},
		{CodeEnvironmentDetached, http.StatusConflict, false, "Agent enrollment: the Engine belongs to an environment whose agent was removed; enroll with intent reattach:<environmentId>.", 3},
		{CodeEngineMismatch, http.StatusConflict, false, "Agent enrollment: a replace or reattach enrollment was used for a different Docker Engine than its target's.", 3},
		{CodeEnrollmentTargetUnavailable, http.StatusConflict, false, "Agent enrollment: the agent to replace or the environment to re-attach is no longer in a state that allows it; create a new enrollment.", 3},
		{CodeAgentRevoked, http.StatusConflict, false, "The agent was removed or replaced; its credential cannot be rotated.", 3},
		{CodeGone, http.StatusGone, false, "The resource existed but was removed permanently (for example an expired invitation).", 2},
		{CodePreconditionFailed, http.StatusPreconditionFailed, false, "If-Match does not name the current revision. The response carries the current ETag; refetch, merge and retry.", 4},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge, false, "The request body exceeds the route's documented limit.", 2},
		{CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, false, "The Content-Type is not accepted by the route.", 2},
		{CodeValidationFailed, http.StatusUnprocessableEntity, false, "One or more inputs are invalid; details lists each field.", 2},
		{CodeVersionUnsupported, http.StatusUpgradeRequired, false, "Agent routes: the agent's protocol or version is outside the manager's window (same or previous minor release, never newer than the manager); upgrade as the message says.", 3},
		{CodePreconditionRequired, http.StatusPreconditionRequired, false, "The edit requires an If-Match header with the resource's current ETag.", 4},
		{CodeRateLimited, http.StatusTooManyRequests, true, "Too many requests; retry after the Retry-After delay.", 2},
		{CodeInternal, http.StatusInternalServerError, false, "Unexpected server error. The cause is logged under the request ID and never returned.", 2},
		{CodeNotImplemented, http.StatusNotImplemented, false, "The route is declared but this manager build does not implement it yet.", 2},
		{CodeJobKindUnavailable, http.StatusNotImplemented, false, "The operation would start a job kind whose executor this manager or agent does not provide yet.", 26},
		{CodeUnavailable, http.StatusServiceUnavailable, true, "A dependency (database, job engine, agent) is temporarily unavailable.", 2},
		{CodeNotReady, http.StatusServiceUnavailable, true, "Readiness check failed; details lists each failing check.", 2},
		{CodeTimeout, http.StatusGatewayTimeout, true, "The operation did not finish within its deadline (also 408 for slow request bodies).", 2},
	}
}

// LookupErrorCode returns the catalog entry of code.
func LookupErrorCode(code string) (ErrorCode, bool) {
	for _, c := range ErrorCodes() {
		if c.Code == code {
			return c, true
		}
	}
	return ErrorCode{}, false
}
