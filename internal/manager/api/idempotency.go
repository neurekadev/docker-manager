package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
)

// Idempotency keys for dangerous non-job operations (#4).
//
// Operations that start jobs (202 + job) declare IdempotencyJob and hand
// the key to the job engine (jobs.Request.IdempotencyKey, #26); the engine
// returns the existing job for a repeated request. Every other dangerous
// retry (for example creating an enrollment token, an invitation or an API
// token, rotating a credential) declares IdempotencyStored: Register then
// installs the middleware below, which
//
//   - scopes keys to (principal, operation): two callers or two operations
//     never share a key;
//   - fingerprints the request (method, path, query, Content-Type, body);
//   - reserves the key before the handler runs, so a concurrent duplicate
//     gets 409 idempotency_key_in_flight (retryable, Retry-After);
//   - stores the first 2xx response (status, Content-Type/Location/ETag,
//     body sealed at rest by the store) for domain.IdempotencyTTL and replays
//     it with Idempotent-Replayed: true;
//   - releases the key when the handler answers non-2xx (the request had no
//     effect, so a retry runs again);
//   - answers 409 idempotency_key_reused when the key comes back with a
//     different fingerprint.
//
// Requests without the header, and unauthenticated requests (the handler
// answers 401), bypass the store.

// IdempotencyKeyParam is embedded in inputs of operations safe to retry.
// Declare Operation.Idempotency (stored or job) together with it.
type IdempotencyKeyParam struct {
	IdempotencyKey string `header:"Idempotency-Key" minLength:"1" maxLength:"128" pattern:"^[A-Za-z0-9._:-]+$" doc:"Client-generated key (for example a UUID) making retries of this request safe for 24 hours. Scoped to the caller and the operation."`
}

// IdempotencyStore persists keyed responses (internal/manager/idempotency).
type IdempotencyStore interface {
	// Begin reserves r. It returns (nil, nil) when the caller must run the
	// request and then Complete or Release it, the stored response when r
	// was completed before with the same fingerprint, or
	// domain.ErrIdempotencyMismatch / domain.ErrIdempotencyInFlight.
	Begin(ctx context.Context, r domain.IdempotencyReservation) (*domain.IdempotentResponse, error)
	// Complete stores the response of a reserved request.
	Complete(ctx context.Context, r domain.IdempotencyReservation, resp domain.IdempotentResponse) error
	// Release drops an unfinished reservation.
	Release(ctx context.Context, r domain.IdempotencyReservation) error
}

// HeaderIdempotentReplayed is set on responses replayed for a repeated Idempotency-Key.
const HeaderIdempotentReplayed = "Idempotent-Replayed"

// maxStoredResponse bounds a stored response body; larger responses are not
// replayable (the reservation is released and a retry runs again).
const maxStoredResponse = 1 << 20

var idempotencyKeyRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// replayHeaders are the response headers stored for a replay.
var replayHeaders = []string{"Content-Type", "Location", "ETag"}

type depsKey struct{}

// withDeps is an API-wide middleware exposing Deps to operation middlewares
// and the audit recorder to every handler (audit.Record, #30).
func withDeps(deps Deps) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		c := context.WithValue(ctx.Context(), depsKey{}, deps)
		if deps.Audit != nil {
			c = audit.WithRecorder(c, deps.Audit)
		}
		next(huma.WithContext(ctx, c))
	}
}

func depsFrom(ctx context.Context) (Deps, bool) {
	d, ok := ctx.Value(depsKey{}).(Deps)
	return d, ok
}

// RequestFingerprint hashes the parts of a request that make it "the same
// request" for idempotency.
func RequestFingerprint(method, path, rawQuery, contentType string, body []byte) string {
	h := sha256.New()
	for _, part := range []string{method, path, canonicalQuery(rawQuery), contentType} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalQuery(raw string) string {
	v, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	return v.Encode() // sorted by key
}

func idempotencyMiddleware(operationID string, maxBody int64) func(huma.Context, func(huma.Context)) {
	if maxBody <= 0 {
		maxBody = 1 << 20 // Huma's default MaxBodyBytes
	}
	return func(ctx huma.Context, next func(huma.Context)) {
		key := ctx.Header("Idempotency-Key")
		if key == "" || !idempotencyKeyRE.MatchString(key) {
			next(ctx) // absent: no idempotency; malformed: Huma answers 422
			return
		}
		p, ok := authz.PrincipalFrom(ctx.Context())
		if !ok {
			next(ctx) // the handler answers 401
			return
		}
		deps, _ := depsFrom(ctx.Context())
		if deps.Idempotency == nil {
			writeHumaError(ctx, Unavailable(CodeUnavailable, "idempotency keys are not available on this manager"))
			return
		}
		// Same body read timeout Huma applies (5 s default), so a slow client
		// cannot hold the connection while the body is buffered here.
		if op := ctx.Operation(); op != nil && op.BodyReadTimeout > 0 {
			_ = ctx.SetReadDeadline(time.Now().Add(op.BodyReadTimeout)) // network deadline: wall clock by design
		}
		body, err := io.ReadAll(io.LimitReader(ctx.BodyReader(), maxBody+1))
		_ = ctx.SetReadDeadline(time.Time{})
		if err != nil {
			writeHumaError(ctx, BadRequest("cannot read request body"))
			return
		}
		inner := &capture{humaCtx: ctx, body: body}
		if int64(len(body)) > maxBody {
			next(inner) // Huma answers 413; nothing reserved
			return
		}
		u := ctx.URL()
		r := domain.IdempotencyReservation{
			Scope:       p.Key() + " " + operationID,
			Key:         key,
			RequestHash: RequestFingerprint(ctx.Method(), u.Path, u.RawQuery, ctx.Header("Content-Type"), body),
		}
		stored, err := deps.Idempotency.Begin(ctx.Context(), r)
		switch {
		case errors.Is(err, domain.ErrIdempotencyMismatch):
			writeHumaError(ctx, Conflict(CodeIdempotencyKeyReused, "the Idempotency-Key was already used for a different request"))
			return
		case errors.Is(err, domain.ErrIdempotencyInFlight):
			writeHumaError(ctx, Conflict(CodeIdempotencyKeyInFlight, "a request with this Idempotency-Key is still in progress").
				WithRetryable(true).WithHeader("Retry-After", "1"))
			return
		case err != nil:
			writeHumaError(ctx, Internal(err))
			return
		case stored != nil:
			replay(ctx, stored)
			return
		}

		completed := false
		defer func() {
			if completed {
				return
			}
			// Handler failed, panicked or produced an unstorable response:
			// the key is free again. Use a context that survives a client
			// disconnect so the reservation does not linger for the lease.
			if err := deps.Idempotency.Release(context.WithoutCancel(ctx.Context()), r); err != nil {
				logging.FromContext(ctx.Context()).Error("release idempotency key", "operation", operationID, "error", err)
			}
		}()
		next(inner)
		status := inner.Status()
		if status < 200 || status > 299 || inner.overflow {
			return
		}
		resp := domain.IdempotentResponse{Status: status, Header: map[string][]string{}, Body: inner.out.Bytes()}
		for _, h := range replayHeaders {
			if v := inner.header.Values(h); len(v) > 0 {
				resp.Header[h] = v
			}
		}
		if err := deps.Idempotency.Complete(context.WithoutCancel(ctx.Context()), r, resp); err != nil {
			logging.FromContext(ctx.Context()).Error("store idempotent response", "operation", operationID, "error", err)
			return
		}
		completed = true
	}
}

func replay(ctx huma.Context, resp *domain.IdempotentResponse) {
	for k, vs := range resp.Header {
		for i, v := range vs {
			if i == 0 {
				ctx.SetHeader(k, v)
			} else {
				ctx.AppendHeader(k, v)
			}
		}
	}
	ctx.SetHeader(HeaderIdempotentReplayed, "true")
	ctx.SetStatus(resp.Status)
	_, _ = ctx.BodyWriter().Write(resp.Body)
}

// writeHumaError writes e from a middleware (outside the handler's error
// path), with the request ID stamped and headers applied.
func writeHumaError(ctx huma.Context, e *Error) {
	finalize(ctx.Context(), e)
	for k, vs := range e.headers {
		for _, v := range vs {
			ctx.AppendHeader(k, v)
		}
	}
	ctx.SetHeader("Content-Type", ErrorContentType)
	ctx.SetStatus(e.status)
	b, _ := json.Marshal(e)
	_, _ = ctx.BodyWriter().Write(b)
}

// humaCtx lets capture embed huma.Context without its field name clashing
// with the Context() method.
type humaCtx huma.Context

// capture replays the buffered request body and records the response.
type capture struct {
	humaCtx
	body     []byte
	status   int
	header   http.Header
	out      bytes.Buffer
	overflow bool
}

func (c *capture) BodyReader() io.Reader { return bytes.NewReader(c.body) }

func (c *capture) SetStatus(code int) {
	c.status = code
	c.humaCtx.SetStatus(code)
}

func (c *capture) Status() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

func (c *capture) SetHeader(name, value string) {
	c.hdr().Set(name, value)
	c.humaCtx.SetHeader(name, value)
}

func (c *capture) AppendHeader(name, value string) {
	c.hdr().Add(name, value)
	c.humaCtx.AppendHeader(name, value)
}

func (c *capture) hdr() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *capture) BodyWriter() io.Writer { return captureWriter{c} }

type captureWriter struct{ c *capture }

func (w captureWriter) Write(b []byte) (int, error) {
	if !w.c.overflow {
		if w.c.out.Len()+len(b) > maxStoredResponse {
			w.c.overflow = true
			w.c.out.Reset()
		} else {
			w.c.out.Write(b)
		}
	}
	return w.c.humaCtx.BodyWriter().Write(b)
}
