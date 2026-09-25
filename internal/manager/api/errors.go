package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/logging"
)

// ErrorContentType is the media type of every error response. The body is a
// valid RFC 9457 problem document that uses only extension members (so
// "type" is implicitly "about:blank"); clients should parse it as the Error
// schema below. See docs/api/conventions.md.
const ErrorContentType = "application/problem+json"

// Error is the ONE JSON error shape returned by /api/v1 (and /agent/v1).
//
//	{"code":"not_found","message":"stack not found","details":[],"requestId":"…","retryable":false}
//
// Construct errors with the helpers in this file (NotFound, Invalid, ...) and
// return them from Huma handlers. Unexpected errors returned by handlers
// become 500 "internal" with the cause logged, never sent to the client.
type Error struct {
	status  int
	causes  []error
	headers http.Header

	Code      string        `json:"code" doc:"Stable snake_case error code; switch on this, not on message." example:"not_found"`
	Message   string        `json:"message" doc:"Human-readable summary. Not stable; do not parse." example:"stack not found"`
	Details   []ErrorDetail `json:"details" doc:"Per-field problems, empty when not applicable."`
	RequestID string        `json:"requestId" doc:"Request ID echoed in the X-Request-ID header and the manager logs." example:"4f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60"`
	Retryable bool          `json:"retryable" doc:"True when repeating the identical request later may succeed."`
}

// ErrorDetail describes one problem with a specific input location.
type ErrorDetail struct {
	Field   string `json:"field" doc:"Input location, e.g. body.name, query.limit, path.stackId, header.If-Match." example:"body.name"`
	Message string `json:"message" doc:"What is wrong with the value." example:"expected length >= 1"`
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// GetStatus implements huma.StatusError.
func (e *Error) GetStatus() int { return e.status }

// Unwrap exposes internal causes to errors.Is/As (never serialized).
func (e *Error) Unwrap() []error { return e.causes }

// GetHeaders implements huma.HeadersError: response headers sent with the
// error (ETag on 412, Retry-After on 409 in-flight/429/503, Allow on 405).
func (e *Error) GetHeaders() http.Header {
	if e.headers == nil {
		e.headers = http.Header{}
	}
	return e.headers
}

// WithHeader sets a response header sent with the error.
func (e *Error) WithHeader(name, value string) *Error {
	e.GetHeaders().Set(name, value)
	return e
}

// ContentType implements huma.ContentTypeFilter.
func (e *Error) ContentType(ct string) string {
	if ct == "application/json" || ct == "" {
		return ErrorContentType
	}
	return ct
}

// NewError builds an error with an explicit code. Prefer the helpers below.
func NewError(status int, code, message string, details ...ErrorDetail) *Error {
	if details == nil {
		details = []ErrorDetail{}
	}
	return &Error{status: status, Code: code, Message: message, Details: details, Retryable: defaultRetryable(status)}
}

// WithCause attaches internal causes that are logged but never returned.
func (e *Error) WithCause(errs ...error) *Error {
	e.causes = append(e.causes, errs...)
	return e
}

// WithRetryable overrides the retryable flag.
func (e *Error) WithRetryable(r bool) *Error {
	e.Retryable = r
	return e
}

// Stable error codes. Add new codes here; never rename an existing one.
const (
	CodeBadRequest           = "bad_request"
	CodeUnauthenticated      = "unauthenticated"
	CodeForbidden            = "forbidden"
	CodeNotFound             = "not_found"
	CodeMethodNotAllowed     = "method_not_allowed"
	CodeNotAcceptable        = "not_acceptable"
	CodeConflict             = "conflict"
	CodeGone                 = "gone"
	CodePreconditionFailed   = "precondition_failed"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeValidationFailed     = "validation_failed"
	CodePreconditionRequired = "precondition_required"
	CodeRateLimited          = "rate_limited"
	CodeInternal             = "internal"
	CodeNotImplemented       = "not_implemented"
	CodeUnavailable          = "unavailable"
	CodeNotReady             = "not_ready"
	CodeTimeout              = "timeout"

	// Idempotency (#4, #26).
	CodeIdempotencyKeyReused   = "idempotency_key_reused"
	CodeIdempotencyKeyInFlight = "idempotency_key_in_flight"

	// Jobs (#26).
	CodeJobFinished        = "job_finished"
	CodeJobKindUnavailable = "job_kind_unavailable"

	// Identity (#16).
	CodeInvalidCredentials     = "invalid_credentials" //nolint:gosec // G101: an error code, not a credential
	CodeInvalidCode            = "invalid_code"
	CodeInsecureOrigin         = "insecure_origin"
	CodeCrossOriginRequest     = "cross_origin_request"
	CodeStepUpRequired         = "step_up_required"
	CodeEnrollmentRequired     = "enrollment_required"
	CodeEnrollmentExpired      = "enrollment_expired"
	CodeSignInMethodNotAllowed = "sign_in_method_not_allowed"
	CodeSetupComplete          = "setup_complete"
	CodeUsernameTaken          = "username_taken"
	CodeOwnerProtected         = "owner_protected"
	CodeFactorRequired         = "factor_required"
	CodeTOTPAlreadyEnabled     = "totp_already_enabled"
	CodeInvitationRedeemed     = "invitation_redeemed"
	CodeNoPendingFlow          = "no_pending_flow"
	// Agents, enrollment and environments (#3).
	CodeVersionUnsupported          = "version_unsupported"
	CodeEngineAlreadyEnrolled       = "engine_already_enrolled"
	CodeEngineIdentityConflict      = "engine_identity_conflict"
	CodeEnvironmentArchived         = "environment_archived"
	CodeEnvironmentDetached         = "environment_detached"
	CodeEngineMismatch              = "engine_mismatch"
	CodeEnrollmentTargetUnavailable = "enrollment_target_unavailable"
	CodeAgentRevoked                = "agent_revoked"

	// Authorization (#17).
	CodeGroupNameTaken        = "group_name_taken"
	CodeDefaultGroupProtected = "default_group_protected"
	CodeGroupNotEmpty         = "group_not_empty"

	// API tokens (#31).
	CodeAPITokenNotAllowed = "api_token_not_allowed" //nolint:gosec // G101: an error code, not a credential
	CodeAPITokensDisabled  = "api_tokens_disabled"   //nolint:gosec // G101: an error code, not a credential

	// Registry connections (#19).
	CodeRegistryNameTaken           = "registry_connection_name_taken"
	CodeAmbiguousRegistryConnection = "ambiguous_registry_connection"
	CodeRegistryConnectionRevoked   = "registry_connection_revoked"
)

// CodeForStatus returns the default code for an HTTP status.
func CodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthenticated
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusNotAcceptable:
		return CodeNotAcceptable
	case http.StatusConflict:
		return CodeConflict
	case http.StatusGone:
		return CodeGone
	case http.StatusPreconditionFailed:
		return CodePreconditionFailed
	case http.StatusRequestEntityTooLarge:
		return CodePayloadTooLarge
	case http.StatusUnsupportedMediaType:
		return CodeUnsupportedMediaType
	case http.StatusUnprocessableEntity:
		return CodeValidationFailed
	case http.StatusPreconditionRequired:
		return CodePreconditionRequired
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusNotImplemented:
		return CodeNotImplemented
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return CodeUnavailable
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return CodeTimeout
	}
	if status >= 500 {
		return CodeInternal
	}
	return CodeBadRequest
}

func defaultRetryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return true
	}
	return false
}

// Helpers for handlers.

// BadRequest is a 400 for malformed requests.
func BadRequest(msg string) *Error { return NewError(http.StatusBadRequest, CodeBadRequest, msg) }

// Unauthenticated is a 401.
func Unauthenticated(msg string) *Error {
	return NewError(http.StatusUnauthorized, CodeUnauthenticated, msg)
}

// Forbidden is a 403. Use NotFound instead when existence must not leak.
func Forbidden(msg string) *Error { return NewError(http.StatusForbidden, CodeForbidden, msg) }

// NotFound is a 404.
func NotFound(msg string) *Error { return NewError(http.StatusNotFound, CodeNotFound, msg) }

// Conflict is a 409 with a specific code (e.g. "stack_name_taken").
func Conflict(code, msg string) *Error { return NewError(http.StatusConflict, code, msg) }

// PreconditionFailed is a 412 for stale If-Match/ETag revisions. Prefer
// CheckIfMatch, which also returns the current ETag.
func PreconditionFailed(msg string, details ...ErrorDetail) *Error {
	return NewError(http.StatusPreconditionFailed, CodePreconditionFailed, msg, details...)
}

// PreconditionRequired is a 428 for edits sent without If-Match.
func PreconditionRequired(msg string, details ...ErrorDetail) *Error {
	return NewError(http.StatusPreconditionRequired, CodePreconditionRequired, msg, details...)
}

// Invalid is a 422 with per-field details.
func Invalid(msg string, details ...ErrorDetail) *Error {
	return NewError(http.StatusUnprocessableEntity, CodeValidationFailed, msg, details...)
}

// Field builds an ErrorDetail.
func Field(field, msg string) ErrorDetail { return ErrorDetail{Field: field, Message: msg} }

// RateLimited is a retryable 429.
func RateLimited(msg string) *Error {
	return NewError(http.StatusTooManyRequests, CodeRateLimited, msg)
}

// Unavailable is a retryable 503.
func Unavailable(code, msg string) *Error {
	return NewError(http.StatusServiceUnavailable, code, msg)
}

// Internal is a 500 whose causes are logged, not returned.
func Internal(causes ...error) *Error {
	return NewError(http.StatusInternalServerError, CodeInternal, "internal server error").WithCause(causes...)
}

// fromHuma converts Huma's built-in errors (validation, parsing, 404/405 ...)
// into Error. Details carrying huma.ErrorDetail map to field details; for
// 5xx, details are treated as internal causes.
func fromHuma(status int, msg string, errs ...error) *Error {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	e := NewError(status, CodeForStatus(status), msg)
	if status >= 500 {
		e.Message = "internal server error"
		if status == http.StatusServiceUnavailable || status == http.StatusNotImplemented {
			e.Message = msg
		}
		return e.WithCause(errs...)
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var d huma.ErrorDetailer
		if errors.As(err, &d) {
			hd := d.ErrorDetail()
			e.Details = append(e.Details, ErrorDetail{Field: hd.Location, Message: hd.Message})
			continue
		}
		e.Details = append(e.Details, ErrorDetail{Message: err.Error()})
	}
	if e.Message == "" {
		e.Message = strings.ToLower(http.StatusText(status))
	}
	return e
}

func init() {
	// DockYard always returns lists as [] (never null); document them so.
	huma.DefaultArrayNullable = false
	// Replace Huma's RFC 9457 ErrorModel with DockYard's Error everywhere,
	// including request validation and content negotiation failures.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return fromHuma(status, msg, errs...)
	}
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return fromHuma(status, msg, errs...)
	}
}

// errorTransformer stamps the request ID onto every Error Huma writes and
// logs internal causes of 5xx responses.
func errorTransformer(ctx huma.Context, _ string, v any) (any, error) {
	e, ok := v.(*Error)
	if !ok {
		return v, nil
	}
	finalize(ctx.Context(), e)
	return e, nil
}

// finalize fills the request ID and logs server-side failures.
func finalize(ctx context.Context, e *Error) {
	if e.RequestID == "" {
		e.RequestID = logging.RequestID(ctx)
	}
	if e.Details == nil {
		e.Details = []ErrorDetail{}
	}
	if e.status >= 500 {
		logging.FromContext(ctx).LogAttrs(ctx, slog.LevelError, "request failed",
			slog.Int("status", e.status), slog.String("code", e.Code), slog.Any("error", errors.Join(e.causes...)))
	}
}

// WriteError writes e as a response outside Huma (router fallbacks, panic
// recovery, the /agent/v1 placeholder).
func WriteError(w http.ResponseWriter, r *http.Request, e *Error) {
	finalize(r.Context(), e)
	for k, vs := range e.headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", ErrorContentType)
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(e)
}
