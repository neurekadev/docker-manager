package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Code is a stable, machine-readable error class. Codes are part of the
// agent's contract with the manager (job results, capability frames) and
// must never change meaning.
type Code string

// Error codes.
const (
	CodeNotFound              Code = "not_found"
	CodeConflict              Code = "conflict"
	CodeInvalidArgument       Code = "invalid_argument"
	CodeUnauthorized          Code = "unauthorized"
	CodeForbidden             Code = "forbidden"
	CodeRateLimited           Code = "rate_limited"
	CodeUnsupportedAPIVersion Code = "unsupported_api_version"
	CodeUnsupported           Code = "unsupported"
	CodeEngineUnavailable     Code = "engine_unavailable"
	CodeTimeout               Code = "timeout"
	CodeCanceled              Code = "canceled"
	CodeNotModified           Code = "not_modified"
	CodeBuildFailed           Code = "build_failed"
	CodeRegistryUnavailable   Code = "registry_unavailable"
	CodeEngineError           Code = "engine_error"
	// Compose adapter codes (internal/agent/compose).
	CodeInvalidProject     Code = "invalid_project"
	CodeUnsupportedFeature Code = "unsupported_compose_feature"
	CodeDependencyFailed   Code = "dependency_failed"
)

// Error is the only error type returned by this package. Message never
// contains credentials: registry auth is passed out of band and SDK
// messages do not echo it.
type Error struct {
	Code Code
	// Op is the adapter operation, e.g. "container.start".
	Op      string
	Message string
	err     error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return e.Message
	}
	return e.Op + ": " + e.Message
}

// Unwrap returns the underlying SDK error.
func (e *Error) Unwrap() error { return e.err }

// Is matches another *Error with the same code, so callers can write
// errors.Is(err, engine.ErrNotFound).
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) && t.Op == "" && t.Message == "" {
		return t.Code == e.Code
	}
	return false
}

// Sentinels for errors.Is.
var (
	ErrNotFound              = &Error{Code: CodeNotFound}
	ErrConflict              = &Error{Code: CodeConflict}
	ErrUnauthorized          = &Error{Code: CodeUnauthorized}
	ErrRateLimited           = &Error{Code: CodeRateLimited}
	ErrUnsupportedAPIVersion = &Error{Code: CodeUnsupportedAPIVersion}
	ErrEngineUnavailable     = &Error{Code: CodeEngineUnavailable}
)

// CodeOf returns the code of err, CodeEngineError for foreign errors and ""
// for nil.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeEngineError
}

func newError(op string, code Code, format string, args ...any) *Error {
	return &Error{Op: op, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Errorf returns an *Error; for the Compose adapter, which shares this
// error model.
func Errorf(op string, code Code, format string, args ...any) *Error {
	return newError(op, code, format, args...)
}

// Wrap maps an SDK error (Moby errdefs, context, connection failures,
// registry messages) to an *Error; *Error values pass through unchanged.
func Wrap(op string, err error) error { return wrap(op, err) }

// WrapCode wraps err with an explicit code.
func WrapCode(op string, code Code, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Code: code, Message: err.Error(), err: err}
}

// wrap maps an SDK error to an *Error. It is the single place SDK error
// semantics are interpreted.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return &Error{Op: op, Code: classify(err), Message: err.Error(), err: err}
}

func classify(err error) Code {
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	case client.IsErrConnectionFailed(err):
		return CodeEngineUnavailable
	}
	// Registry failures arrive as messages in pull/build streams (often
	// without a usable status code), so the text is checked before the
	// errdefs classes, which may be a generic "unknown"/"system" error.
	if c := classifyRegistryMessage(err.Error()); c != "" {
		return c
	}
	// The Engine answers 403 for a network that still has containers; for
	// callers that is a conflict like a volume in use.
	if strings.Contains(err.Error(), "has active endpoints") {
		return CodeConflict
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return CodeNotFound
	case cerrdefs.IsConflict(err), cerrdefs.IsAlreadyExists(err):
		return CodeConflict
	case cerrdefs.IsUnauthorized(err):
		return CodeUnauthorized
	case cerrdefs.IsPermissionDenied(err):
		return CodeForbidden
	case cerrdefs.IsInvalidArgument(err):
		return CodeInvalidArgument
	case cerrdefs.IsNotImplemented(err):
		return CodeUnsupported
	case cerrdefs.IsUnavailable(err):
		return CodeEngineUnavailable
	case cerrdefs.IsNotModified(err):
		return CodeNotModified
	case cerrdefs.IsResourceExhausted(err):
		return CodeRateLimited
	case cerrdefs.IsDeadlineExceeded(err):
		return CodeTimeout
	case cerrdefs.IsCanceled(err):
		return CodeCanceled
	}
	return CodeEngineError
}

// classifyRegistryMessage recognizes registry errors relayed by the Engine
// (distribution error codes and HTTP status texts).
func classifyRegistryMessage(msg string) Code {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "toomanyrequests"), strings.Contains(m, "429 too many requests"),
		strings.Contains(m, "rate limit"):
		return CodeRateLimited
	case strings.Contains(m, "no basic auth credentials"), strings.Contains(m, "unauthorized: "),
		strings.Contains(m, "401 unauthorized"), strings.Contains(m, "authentication required"),
		strings.Contains(m, "pull access denied"), strings.Contains(m, "incorrect username or password"):
		return CodeUnauthorized
	case strings.Contains(m, "denied: "), strings.Contains(m, "403 forbidden"):
		return CodeForbidden
	case strings.Contains(m, "manifest unknown"), strings.Contains(m, "name unknown"),
		strings.Contains(m, "repository does not exist"):
		return CodeNotFound
	case strings.Contains(m, "502 bad gateway"), strings.Contains(m, "503 service unavailable"),
		strings.Contains(m, "504 gateway timeout"):
		return CodeRegistryUnavailable
	}
	return ""
}
