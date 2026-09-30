package server

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
)

// AgentLimits hardens /agent/v1, which is publicly reachable on the shared
// origin (#27). The zero value means DefaultAgentLimits.
type AgentLimits struct {
	// RequestsPerSecond and Burst form a token bucket per client IP (IPv6
	// clients are grouped by /64). The client IP honors X-Forwarded-For
	// only from DOCKER_MANAGER_TRUSTED_PROXIES, so agents behind the proxy are
	// limited individually and spoofed headers cannot evade the limit.
	RequestsPerSecond float64
	Burst             int
	// MaxTrackedClients bounds the limiter table. When it is full and no
	// idle entry can be evicted, requests from new clients are rejected
	// with 429 (fail closed under address-rotation floods).
	MaxTrackedClients int
	// MaxBodyBytes bounds request bodies (enrollment payloads are small;
	// session traffic is WebSocket frames, bounded by the ws read limit).
	MaxBodyBytes int64
	// PreAuthTimeout bounds how long a client may take to send its request
	// body before the handler authenticates it. Handlers call EndPreAuth
	// after authenticating; ws.Accept clears it before upgrading.
	PreAuthTimeout time.Duration
}

// DefaultAgentLimits are the /agent/v1 defaults: 1 request/s per client IP
// with bursts of 30 (a reconnect storm after a manager restart stays well
// inside it), 64 KiB bodies, 10 s to send the body.
func DefaultAgentLimits() AgentLimits {
	return AgentLimits{RequestsPerSecond: 1, Burst: 30, MaxTrackedClients: 10000, MaxBodyBytes: 64 << 10, PreAuthTimeout: 10 * time.Second}
}

func (l AgentLimits) withDefaults() AgentLimits {
	d := DefaultAgentLimits()
	if l.RequestsPerSecond <= 0 {
		l.RequestsPerSecond = d.RequestsPerSecond
	}
	if l.Burst <= 0 {
		l.Burst = d.Burst
	}
	if l.MaxTrackedClients <= 0 {
		l.MaxTrackedClients = d.MaxTrackedClients
	}
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.PreAuthTimeout <= 0 {
		l.PreAuthTimeout = d.PreAuthTimeout
	}
	return l
}

// Generic /agent/v1 failure messages. They never say which check failed
// (unknown, expired, revoked or malformed credentials all look alike).
const (
	agentAuthFailed  = "agent authentication failed"
	agentRateLimited = "too many requests"
	agentTooLarge    = "request too large"
	agentBadRequest  = "invalid request"
)

// AgentFailure writes the generic /agent/v1 failure response for status
// (401, 400, 413, 408 or 429; anything else becomes 400). #3's enrollment
// and session handlers use it for every rejection so responses do not
// reveal whether a token existed, expired or was revoked.
func AgentFailure(w http.ResponseWriter, r *http.Request, status int) {
	var e *api.Error
	switch status {
	case http.StatusUnauthorized:
		e = api.Unauthenticated(agentAuthFailed)
	case http.StatusTooManyRequests:
		e = api.RateLimited(agentRateLimited)
	case http.StatusRequestEntityTooLarge:
		e = api.NewError(status, api.CodePayloadTooLarge, agentTooLarge)
	case http.StatusRequestTimeout:
		e = api.NewError(status, api.CodeTimeout, "request timed out")
	default:
		e = api.BadRequest(agentBadRequest)
	}
	api.WriteError(w, r, e)
}

// EndPreAuth clears the pre-authentication read deadline once a handler has
// authenticated the request (for streaming bodies after enrollment).
func EndPreAuth(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
}

// agentGuard applies AgentLimits to every /agent/v1 request. Its limiter
// table is shared by all handlers it wraps.
type agentGuard struct {
	limits AgentLimits
	clk    clock.Clock

	mu      sync.Mutex
	clients map[netip.Prefix]*rate.Limiter
}

func newAgentGuard(limits AgentLimits, clk clock.Clock) *agentGuard {
	return &agentGuard{limits: limits.withDefaults(), clk: clk, clients: map[netip.Prefix]*rate.Limiter{}}
}

func (g *agentGuard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, next) })
}

func (g *agentGuard) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	info, _ := requestinfo.From(r.Context())
	if !g.allow(info.ClientIP) {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(1/g.limits.RequestsPerSecond))))
		logging.FromContext(r.Context()).Warn("agent endpoint rate limit exceeded", slog.String("client_ip", addrString(info.ClientIP)))
		AgentFailure(w, r, http.StatusTooManyRequests)
		return
	}
	if r.ContentLength > g.limits.MaxBodyBytes {
		AgentFailure(w, r, http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, g.limits.MaxBodyBytes)
	// Connection deadlines are absolute wall-clock times, so this uses
	// time.Now rather than the injectable clock.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(g.limits.PreAuthTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		logging.FromContext(r.Context()).Debug("cannot set pre-auth read deadline", slog.Any("error", err))
	}
	next.ServeHTTP(w, r)
}

// allow takes one token for the client's bucket.
func (g *agentGuard) allow(ip netip.Addr) bool {
	key := clientKey(ip)
	now := g.clk.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	lim, ok := g.clients[key]
	if !ok {
		if len(g.clients) >= g.limits.MaxTrackedClients {
			g.evictIdle(now)
		}
		if len(g.clients) >= g.limits.MaxTrackedClients {
			return false
		}
		lim = rate.NewLimiter(rate.Limit(g.limits.RequestsPerSecond), g.limits.Burst)
		g.clients[key] = lim
	}
	return lim.AllowN(now, 1)
}

// evictIdle drops buckets that have fully refilled: forgetting them changes
// nothing for their clients.
func (g *agentGuard) evictIdle(now time.Time) {
	for k, lim := range g.clients {
		if lim.TokensAt(now) >= float64(g.limits.Burst) {
			delete(g.clients, k)
		}
	}
}

// clientKey groups IPv6 clients by /64 (one site or host can rotate through
// its whole /64); IPv4 clients are keyed individually. Requests without a
// parsable client address share one bucket.
func clientKey(ip netip.Addr) netip.Prefix {
	if !ip.IsValid() {
		return netip.Prefix{}
	}
	ip = ip.Unmap()
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, _ := ip.Prefix(bits)
	return p
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return "unknown"
	}
	return a.String()
}

// routeBoundaries applies the per-area rules of the shared origin by path,
// in front of every handler registered under /api/v1 or /agent/v1:
//
//   - /agent/v1: the agentGuard (rate limit, body bound, pre-auth
//     deadline), then the credential separation: the Cookie header is
//     removed, so browser sessions can never authenticate an agent route,
//     and any Authorization that is not an agent bearer credential or
//     enrollment token (authsep) is refused generically;
//   - /api/v1: agent credentials and enrollment tokens are refused before
//     any handler (or session/API-token middleware, #16/#31) sees them.
func routeBoundaries(guard *agentGuard, next http.Handler) http.Handler {
	agent := guard.wrap(agentSeparation(next))
	public := apiSeparation(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isAgentPath(r.URL.Path):
			agent.ServeHTTP(w, r)
		case isAPIPath(r.URL.Path):
			public.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func agentSeparation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		if r.Header.Get("Authorization") != "" {
			tok, ok := authsep.BearerToken(r.Header)
			if !ok || authsep.Classify(tok) != authsep.KindAgent {
				// Basic auth, API tokens (#31) or garbage: never agent credentials.
				AgentFailure(w, r, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func apiSeparation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := authsep.BearerToken(r.Header); ok && authsep.Classify(tok) == authsep.KindAgent {
			info, _ := requestinfo.From(r.Context())
			logging.FromContext(r.Context()).Warn("agent credential presented to the public API; refused",
				slog.String("client_ip", addrString(info.ClientIP)))
			api.WriteError(w, r, api.Unauthenticated("agent credentials cannot be used for the public API"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
