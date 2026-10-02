// Package server assembles the manager's single HTTP handler:
//
//	/api/v1/*    public Huma API (internal/manager/api)
//	/agent/v1/*  private agent enrollment/session (Options.Agent, #3)
//	/*           embedded SvelteKit PWA with deep-link fallback
//
// wrapped in request-info (trusted proxies), request-ID, access-log,
// panic-recovery, security-header, no-store and route-boundary middleware
// (the /agent/v1 guard and the cookie/agent-credential separation). The
// manager serves plain HTTP; TLS terminates at the operator's reverse proxy
// (#27, docs/internal/deployment.md).
package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/server/sse"
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
	// TrustedProxies are the reverse proxies whose X-Forwarded-For/Proto/Host
	// headers are honored (DOCKER_MANAGER_TRUSTED_PROXIES, #27).
	TrustedProxies []netip.Prefix
	// PublicURL and LocalDevelopment describe DOCKER_MANAGER_PUBLIC_URL (for the
	// secure-origin check, requestinfo.CheckSecureOrigin).
	PublicURL        *url.URL
	LocalDevelopment bool
	// StreamHeartbeat is the SSE heartbeat and WebSocket ping interval
	// (DOCKER_MANAGER_STREAM_HEARTBEAT; default sse.DefaultHeartbeat).
	StreamHeartbeat time.Duration
	// Agent serves /agent/v1/* (enrollment and session, #3). Nil answers
	// every agent route with 404. It always runs behind the agent guard
	// (AgentLimits) and the credential separation.
	Agent http.Handler
	// AgentLimits hardens /agent/v1 (zero: DefaultAgentLimits).
	AgentLimits AgentLimits
	// Auth wraps every /api/v1 request: session loading, CSRF protection
	// and principals (internal/manager/auth, #16). Nil leaves the API
	// unauthenticated (every non-public route answers 401).
	Auth func(http.Handler) http.Handler
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
	if opts.StreamHeartbeat <= 0 {
		opts.StreamHeartbeat = sse.DefaultHeartbeat
	}
	ui, err := newSPA(opts.UI)
	if err != nil {
		return nil, err
	}

	apiMux := http.NewServeMux()
	humaAPI := api.New(apiMux, opts.API)

	var apiHandler = apiRouter(apiMux)
	if opts.Auth != nil {
		apiHandler = opts.Auth(apiHandler)
	}
	mux := http.NewServeMux()
	mux.Handle(api.BasePath+"/", apiHandler)
	agent := opts.Agent
	if agent == nil {
		agent = agentPlaceholder()
	}
	mux.Handle(AgentBasePath+"/", agent)
	mux.Handle("/", ui)

	var h http.Handler = mux
	h = routeBoundaries(newAgentGuard(opts.AgentLimits, opts.Clock), h)
	h = noStore(h)
	h = securityHeaders(contentSecurityPolicy(ui.scriptHashes), opts.PublicURL != nil && opts.PublicURL.Scheme == "https", h)
	h = recoverPanics(h)
	h = accessLog(opts.Clock, h)
	h = withRequestID(opts.Logger, h)
	h = withRequestInfo(requestinfo.NewResolver(opts.TrustedProxies), h)
	return &Server{Handler: h, API: humaAPI}, nil
}

// HTTPServer wraps a handler with Docker Manager's connection timeouts. There is
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
// TODO(#3): POST /agent/v1/enroll and GET /agent/v1/session (WebSocket via
// server/ws with subprotocol protocol.Version and KeepAlive), passed as
// Options.Agent. The guard (rate limits, body bound, pre-auth deadline) and
// the credential separation already apply; reject with AgentFailure.
func agentPlaceholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, api.NotFound("agent protocol endpoint not available"))
	})
}
