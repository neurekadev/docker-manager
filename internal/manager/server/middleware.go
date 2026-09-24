package server

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
)

// RequestIDHeader carries the request ID in requests and responses.
const RequestIDHeader = "X-Request-ID"

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// newRequestID returns 16 random bytes as hex.
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// withRequestInfo resolves the request's client IP, scheme and host
// (honoring X-Forwarded-* only from DOCKYARD_TRUSTED_PROXIES), stores them
// in the context (requestinfo.From / requestinfo.ClientIP) and strips the
// forwarding headers so no handler can read client-supplied values. It is
// the outermost middleware.
func withRequestInfo(res *requestinfo.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := res.Resolve(r)
		for _, h := range []string{requestinfo.HeaderForwardedFor, requestinfo.HeaderForwardedProto,
			requestinfo.HeaderForwardedHost, requestinfo.HeaderForwarded} {
			r.Header.Del(h)
		}
		next.ServeHTTP(w, r.WithContext(requestinfo.With(r.Context(), info)))
	})
}

// withRequestID assigns every request an ID, echoes it in the response and
// stores it plus a request-scoped logger in the context. An inbound
// X-Request-ID is kept only when it is well-formed and the direct peer is a
// trusted proxy (so the proxy's ID correlates both logs); anyone else gets a
// fresh ID.
func withRequestID(base *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, _ := requestinfo.From(r.Context())
		id := r.Header.Get(RequestIDHeader)
		if !info.TrustedPeer || !requestIDRE.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := logging.WithRequestID(r.Context(), id)
		ctx = logging.IntoContext(ctx, base.With(slog.String("request_id", id)))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// accessLog logs one line per request. It logs the path only, never the
// query string (it may carry cursors or future tokens) or headers. remote is
// the direct peer (the proxy); client_ip the resolved client.
func accessLog(clk clock.Clock, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := clk.Now()
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if r.URL.Path == api.BasePath+"/health" || r.URL.Path == api.BasePath+"/health/ready" {
			level = slog.LevelDebug // container health checks would flood the log
		}
		if rec.status >= 500 {
			level = slog.LevelError
		}
		logging.FromContext(r.Context()).LogAttrs(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.statusOrOK()),
			slog.Int64("bytes", rec.bytes),
			slog.Duration("duration", clk.Since(start)),
			slog.String("remote", r.RemoteAddr),
			slog.String("client_ip", addrString(requestinfo.ClientIP(r.Context()))),
		)
	})
}

// recoverPanics turns a handler panic into a 500 in the standard error shape
// (for API routes) and logs the stack. http.ErrAbortHandler is re-panicked.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := w.(*responseRecorder)
		if !ok {
			rec = &responseRecorder{ResponseWriter: w}
		}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, isErr := v.(error); isErr && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			logging.FromContext(r.Context()).Error("panic serving request",
				slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
			if rec.wroteHeader {
				return // too late to change the response
			}
			if isAPIPath(r.URL.Path) {
				api.WriteError(rec, r, api.Internal())
				return
			}
			http.Error(rec, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(rec, r)
	})
}

// securityHeaders sets the baseline browser hardening headers.
func securityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// noStore marks every API and agent response as uncacheable.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) || isAgentPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func isAPIPath(p string) bool   { return p == api.BasePath || strings.HasPrefix(p, api.BasePath+"/") }
func isAgentPath(p string) bool { return p == AgentBasePath || strings.HasPrefix(p, AgentBasePath+"/") }

// responseRecorder captures status and size while staying transparent to
// http.ResponseController (Flush, Hijack, deadlines) via Unwrap.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *responseRecorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush supports streaming responses (SSE).
func (r *responseRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// Hijack supports WebSocket upgrades.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil && !r.wroteHeader {
		r.status = http.StatusSwitchingProtocols
		r.wroteHeader = true
	}
	return conn, rw, err
}
