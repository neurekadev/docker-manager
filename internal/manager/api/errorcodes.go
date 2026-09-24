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
		{CodeUnauthenticated, http.StatusUnauthorized, false, "No valid session cookie or API token; sign in again or send a valid bearer token.", 2},
		{CodeForbidden, http.StatusForbidden, false, "Authenticated, but the named capability is not granted for this resource. Returned only when the caller may know the resource exists; otherwise not_found.", 2},
		{CodeNotFound, http.StatusNotFound, false, "The resource or route does not exist, or the caller may not know that it exists.", 2},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed, false, "The route exists but not with this method; see the Allow header.", 2},
		{CodeNotAcceptable, http.StatusNotAcceptable, false, "The Accept header excludes every media type the route can produce.", 2},
		{CodeConflict, http.StatusConflict, false, "Generic conflict with the current state. Routes prefer a specific 409 code.", 2},
		{CodeJobFinished, http.StatusConflict, false, "The job already reached a terminal state (for example a cancellation of a finished job).", 26},
		{CodeIdempotencyKeyReused, http.StatusConflict, false, "The Idempotency-Key was already used by this caller for a different request (different route, parameters or body).", 26},
		{CodeIdempotencyKeyInFlight, http.StatusConflict, true, "A request with the same Idempotency-Key is still being processed; retry after the Retry-After delay.", 4},
		{CodeGone, http.StatusGone, false, "The resource existed but was removed permanently (for example an expired invitation).", 2},
		{CodePreconditionFailed, http.StatusPreconditionFailed, false, "If-Match does not name the current revision. The response carries the current ETag; refetch, merge and retry.", 4},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge, false, "The request body exceeds the route's documented limit.", 2},
		{CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, false, "The Content-Type is not accepted by the route.", 2},
		{CodeValidationFailed, http.StatusUnprocessableEntity, false, "One or more inputs are invalid; details lists each field.", 2},
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
