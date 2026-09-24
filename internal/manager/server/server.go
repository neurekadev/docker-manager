// Package server assembles the manager's single HTTP handler:
//
//	/api/v1/*    public Huma API (internal/manager/api)
//	/agent/v1/*  private agent enrollment/session (reserved, #3)
//	/*           embedded SvelteKit PWA with deep-link fallback
//
// wrapped in request-ID, access-log, panic-recovery, security-header and
// no-store middleware. The manager serves plain HTTP; TLS terminates at the
// operator's reverse proxy (#27).
package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/manager/api"
)

// AgentBasePath prefixes the private agent routes.
const AgentBasePath = "/agent/v1"

// Options configures the handler.
type Options struct {
	Logger *slog.Logger
	Clock  clock.Clock
	// API are the dependencies of the public API operations.
	API api.Deps
	// UI is the embedded SvelteKit build (web.Assets()).
	UI fs.FS
	// TrustedProxies will gate X-Forwarded-* handling (#27). Parsed and
	// carried now; not yet used.
	TrustedProxies []netip.Prefix
}

// Server is the assembled handler plus the Huma API (for tests/tools).
type Server struct {
	Handler http.Handler
	API     huma.API
}

// New builds the handler.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		return nil, errors.New("server: logger is required")
	}
	if opts.UI == nil {
		return nil, errors.New("server: UI assets are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	ui, err := newSPA(opts.UI)
	if err != nil {
		return nil, err
	}

	apiMux := http.NewServeMux()
	humaAPI := api.New(apiMux, opts.API)

	mux := http.NewServeMux()
	mux.Handle(api.BasePath+"/", apiRouter(apiMux))
	mux.Handle(AgentBasePath+"/", agentPlaceholder())
	mux.Handle("/", ui)

	var h http.Handler = mux
	h = noStore(h)
	h = securityHeaders(contentSecurityPolicy(ui.scriptHashes), h)
	h = recoverPanics(h)
	h = accessLog(opts.Clock, h)
	h = withRequestID(opts.Logger, h)
	return &Server{Handler: h, API: humaAPI}, nil
}

// HTTPServer wraps a handler with DockYard's connection timeouts. There is
// deliberately no WriteTimeout: SSE and WebSocket streams are long-lived and
// enforce their own deadlines.
func HTTPServer(h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// apiRouter serves registered Huma routes and answers everything else under
// /api/v1 with a JSON 404 or 405 in the standard error shape (instead of
// falling through to the SPA).
func apiRouter(apiMux *http.ServeMux) http.Handler {
	probe := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := apiMux.Handler(r); pattern != "" {
			apiMux.ServeHTTP(w, r)
			return
		}
		var allowed []string
		for _, m := range probe {
			if m == r.Method {
				continue
			}
			alt := r.Clone(r.Context())
			alt.Method = m
			if _, pattern := apiMux.Handler(alt); pattern != "" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			for _, m := range allowed {
				w.Header().Add("Allow", m)
			}
			api.WriteError(w, r, api.NewError(http.StatusMethodNotAllowed, api.CodeMethodNotAllowed, "method not allowed"))
			return
		}
		api.WriteError(w, r, api.NotFound("no such API route"))
	})
}

// agentPlaceholder reserves /agent/v1 until enrollment and the session
// endpoint land.
//
// TODO(#3): POST /agent/v1/enroll and GET /agent/v1/session (WebSocket,
// subprotocol protocol.Version) with rate limits, generic failures, pre-auth
// timeouts and bounded frames (#27).
func agentPlaceholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, api.NotFound("agent protocol endpoint not available"))
	})
}
