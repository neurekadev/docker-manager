package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"golang.org/x/time/rate"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/server"
	"github.com/neurekadev/docker-manager/internal/manager/server/ws"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// AttemptLimits are per-secret attempt limits on top of the per-IP
// /agent/v1 guard: a leaked token cannot be hammered from many addresses,
// and a reconnect loop of one agent stays bounded. Zero fields use the
// defaults (enrollment: 6/min, burst 5; session: 10/min, burst 10).
type AttemptLimits struct {
	EnrollPerMinute  float64
	EnrollBurst      int
	SessionPerMinute float64
	SessionBurst     int
}

func (l AttemptLimits) withDefaults() AttemptLimits {
	if l.EnrollPerMinute <= 0 {
		l.EnrollPerMinute = 6
	}
	if l.EnrollBurst <= 0 {
		l.EnrollBurst = 5
	}
	if l.SessionPerMinute <= 0 {
		l.SessionPerMinute = 10
	}
	if l.SessionBurst <= 0 {
		l.SessionBurst = 10
	}
	return l
}

// maxLimitedSecrets bounds the per-secret limiter tables.
const maxLimitedSecrets = 10000

// Handler serves POST /agent/v1/enroll and GET /agent/v1/session. Mount it
// as server.Options.Agent: the server's agent guard (per-IP rate limit, body
// bound, pre-auth deadline) and credential separation run first. Every
// authentication failure is the same generic 401 (server.AgentFailure).
func (s *Service) Handler() http.Handler {
	l := s.opts.Attempts.withDefaults()
	h := &handler{svc: s,
		enrollLimit:  newKeyedLimiter(s.clk, l.EnrollPerMinute, l.EnrollBurst),
		sessionLimit: newKeyedLimiter(s.clk, l.SessionPerMinute, l.SessionBurst)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+protocol.EnrollPath, h.enroll)
	mux.HandleFunc("GET "+protocol.SessionPath, h.session)
	mux.HandleFunc(server.AgentBasePath+"/", func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, api.NotFound("no such agent route"))
	})
	return mux
}

type handler struct {
	svc          *Service
	enrollLimit  *keyedLimiter
	sessionLimit *keyedLimiter
}

// browserRequest refuses requests carrying an Origin header: every browser
// request does, agents never do (also blocks cross-site WebSocket
// hijacking).
func browserRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") == "" {
		return false
	}
	api.WriteError(w, r, api.Forbidden("browser requests are not accepted on agent routes"))
	return true
}

func clientIP(r *http.Request) string {
	info, _ := requestinfo.From(r.Context())
	if !info.ClientIP.IsValid() {
		return "unknown"
	}
	return info.ClientIP.String()
}

// MovedRetryAfter is the Retry-After (seconds) of agent requests refused
// because the manager moved to a new server.
const MovedRetryAfter = "60"

// moved refuses agent requests once the manager handed its state to a new
// server, or while it waits for a move's handoff
// (docs/internal/architecture/manager-move.md): 503 with
// Retry-After, before any credential check, so agents keep their
// credential and retry (never 401: they would delete it).
func (h *handler) moved(w http.ResponseWriter, r *http.Request) bool {
	if !h.svc.opts.MoveLock.AgentsRefused() {
		return false
	}
	msg := "Docker Manager moved to a new server; this manager no longer accepts agents (point the agent at the public address)"
	if h.svc.opts.MoveLock.Waiting() {
		msg = "this Docker Manager waits for a move from another server and accepts agents once the move is done"
	}
	api.WriteError(w, r, api.Unavailable(api.CodeUnavailable, msg).WithHeader("Retry-After", MovedRetryAfter))
	return true
}

// versionUnsupported answers 426 with an upgrade message.
func versionUnsupported(w http.ResponseWriter, r *http.Request, msg string) {
	api.WriteError(w, r, api.NewError(http.StatusUpgradeRequired, api.CodeVersionUnsupported, msg))
}

func (h *handler) enroll(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(withUserAgent(r.Context(), r.UserAgent()))
	log := logging.FromContext(r.Context()).With("client_ip", clientIP(r))
	if browserRequest(w, r) {
		return
	}
	if h.moved(w, r) {
		return
	}
	tok, ok := authsep.BearerToken(r.Header)
	id, _, valid := authsep.ParseEnrollmentToken(tok)
	if !ok || !valid {
		log.Warn("agent enrollment refused: malformed or missing token")
		server.AgentFailure(w, r, http.StatusUnauthorized)
		return
	}
	if !h.enrollLimit.allow(id) {
		w.Header().Set("Retry-After", "10")
		server.AgentFailure(w, r, http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			server.AgentFailure(w, r, http.StatusRequestEntityTooLarge)
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			server.AgentFailure(w, r, http.StatusRequestTimeout)
		default:
			server.AgentFailure(w, r, http.StatusBadRequest)
		}
		return
	}
	server.EndPreAuth(w)
	req, err := protocol.DecodeEnrollRequest(body)
	if err != nil {
		api.WriteError(w, r, api.Invalid("invalid enrollment request", api.Field("body", err.Error())))
		return
	}
	if req.Protocol != protocol.Version {
		versionUnsupported(w, r, "protocol "+strings.TrimSpace(req.Protocol)+" is not supported; this manager speaks "+protocol.Version+"; upgrade the agent")
		return
	}
	if err := req.Validate(); err != nil {
		api.WriteError(w, r, api.Invalid("invalid enrollment request", api.Field("body", err.Error())))
		return
	}
	resp, err := h.svc.Enroll(r.Context(), tok, req)
	var conflict *domain.EnrollConflict
	var verr *VersionError
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrEnrollmentInvalid):
		log.Warn("agent enrollment refused: invalid token", "enrollment_id", id)
		server.AgentFailure(w, r, http.StatusUnauthorized)
		return
	case errors.As(err, &verr):
		log.Warn("agent enrollment refused: version outside the window", "agent_version", req.AgentVersion)
		versionUnsupported(w, r, verr.Error())
		return
	case errors.As(err, &conflict):
		api.WriteError(w, r, api.Conflict(conflict.Rejection.Code, conflict.Rejection.Message))
		return
	default:
		api.WriteError(w, r, api.Internal(err))
		return
	}
	log.Info("agent enrollment succeeded", "agent_id", resp.AgentID, "environment_id", resp.EnvironmentID)
	b, err := json.Marshal(resp)
	if err != nil {
		api.WriteError(w, r, api.Internal(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(b)
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(withUserAgent(r.Context(), r.UserAgent()))
	log := logging.FromContext(r.Context()).With("client_ip", clientIP(r))
	if browserRequest(w, r) {
		return
	}
	if h.moved(w, r) {
		return
	}
	tok, ok := authsep.BearerToken(r.Header)
	id, _, valid := authsep.ParseAgentCredential(tok)
	if !ok || !valid {
		server.AgentFailure(w, r, http.StatusUnauthorized)
		return
	}
	if !h.sessionLimit.allow(id) {
		w.Header().Set("Retry-After", "6")
		server.AgentFailure(w, r, http.StatusTooManyRequests)
		return
	}
	if !offersSubprotocol(r, protocol.Version) {
		versionUnsupported(w, r, "the "+protocol.Version+" WebSocket subprotocol was not offered; upgrade the agent")
		return
	}
	p, err := h.svc.Authenticate(r.Context(), tok)
	switch {
	case errors.Is(err, domain.ErrCredentialInvalid):
		log.Warn("agent session refused: invalid credential", "credential_id", id)
		server.AgentFailure(w, r, http.StatusUnauthorized)
		return
	case err != nil:
		api.WriteError(w, r, api.Internal(err))
		return
	}
	conn, err := ws.Accept(w, r, ws.Options{Subprotocols: []string{protocol.Version}, RequireSubprotocol: true,
		ReadLimit: protocol.MaxFrameSize, Clock: h.svc.clk})
	if err != nil {
		log.Info("agent session upgrade failed", "agent_id", p.AgentID, "error", err)
		return
	}
	// The session outlives nothing but the connection; manager shutdown
	// closes it through Hub.Shutdown.
	h.svc.hub.serve(context.WithoutCancel(r.Context()), conn.Conn, p, clientIP(r))
}

func offersSubprotocol(r *http.Request, want string) bool {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		if slices.ContainsFunc(strings.Split(v, ","), func(s string) bool { return strings.TrimSpace(s) == want }) {
			return true
		}
	}
	return false
}

// keyedLimiter is a bounded table of token buckets keyed by secret ID.
type keyedLimiter struct {
	clk   clock.Clock
	limit rate.Limit
	burst int

	mu sync.Mutex
	m  map[string]*rate.Limiter
}

func newKeyedLimiter(clk clock.Clock, perMinute float64, burst int) *keyedLimiter {
	return &keyedLimiter{clk: clk, limit: rate.Limit(perMinute / 60), burst: burst, m: map[string]*rate.Limiter{}}
}

func (k *keyedLimiter) allow(key string) bool {
	now := k.clk.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	l, ok := k.m[key]
	if !ok {
		if len(k.m) >= maxLimitedSecrets {
			for key, l := range k.m {
				if l.TokensAt(now) >= float64(k.burst) {
					delete(k.m, key)
				}
			}
		}
		if len(k.m) >= maxLimitedSecrets {
			// Fail open: the per-IP /agent/v1 guard still applies, and a
			// flood of made-up IDs must not lock out real agents.
			return true
		}
		l = rate.NewLimiter(k.limit, k.burst)
		k.m[key] = l
	}
	return l.AllowN(now, 1)
}
