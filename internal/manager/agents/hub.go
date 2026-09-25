package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// JobEngine is the job engine as seen by the session hub
// (internal/manager/jobs.Engine implements it).
type JobEngine interface {
	HandleAgentFrame(ctx context.Context, environmentID string, f *protocol.Frame) ([]*protocol.Frame, error)
	AgentDisconnected(environmentID string)
	Wake()
}

// Reconciler runs after an agent's job_report was reconciled and before its
// environment is reported online (#23: "reconciliation before claiming live
// state is current"). Inventory owners (#5, #6, #7) register one to re-read
// what the agent's events would have told them while it was offline.
// Returning an error closes the session (the agent reconnects).
type Reconciler func(ctx context.Context, s *Session) error

// SessionOptions tunes the session protocol. Zero values use the protocol
// constants (docs/protocol/agent-v1.md).
type SessionOptions struct {
	HelloTimeout      time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	// WriteTimeout bounds one frame write (network safety net).
	WriteTimeout time.Duration
	// SendQueue bounds the frames waiting for the writer; a full queue
	// closes the session (1011) so its state is reconciled on reconnect.
	SendQueue int
	// RequestTimeout is the default deadline of Request.
	RequestTimeout time.Duration
}

func (o SessionOptions) withDefaults() SessionOptions {
	if o.HelloTimeout <= 0 {
		o.HelloTimeout = protocol.HelloTimeout
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = protocol.HeartbeatInterval
	}
	if o.HeartbeatTimeout <= o.HeartbeatInterval {
		o.HeartbeatTimeout = max(protocol.HeartbeatTimeout, 2*o.HeartbeatInterval)
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.SendQueue <= 0 {
		o.SendQueue = 256
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	return o
}

// Hub tracks the live agent sessions (one per agent and environment). It
// implements jobs.AgentDispatcher: Send delivers frames in call order to
// the environment's session; Online is true only after the session's
// job_report was reconciled and every Reconciler ran.
type Hub struct {
	svc  *Service
	opts SessionOptions
	log  *slog.Logger

	mu      sync.Mutex
	jobs    JobEngine
	byAgent map[string]*Session
	byEnv   map[string]*Session
	// conns are all connections being served, also before hello.
	conns       map[*Session]struct{}
	online      map[string]bool
	reconcilers []Reconciler
	shutdown    bool
	wg          sync.WaitGroup
}

var _ jobs.AgentDispatcher = (*Hub)(nil)

func newHub(svc *Service, opts SessionOptions) *Hub {
	return &Hub{svc: svc, opts: opts, log: svc.log.With("component", "agent-sessions"),
		byAgent: map[string]*Session{}, byEnv: map[string]*Session{}, online: map[string]bool{}, conns: map[*Session]struct{}{}}
}

func (h *Hub) attachJobs(j JobEngine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobs = j
}

func (h *Hub) jobEngine() JobEngine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.jobs
}

// AddReconciler registers a reconciler (before serving).
func (h *Hub) AddReconciler(r Reconciler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reconcilers = append(h.reconcilers, r)
}

// Online implements jobs.AgentDispatcher.
func (h *Hub) Online(environmentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online[environmentID]
}

// Send implements jobs.AgentDispatcher: it queues f on the environment's
// session (also while it is still reconciling). jobs.ErrAgentOffline when
// no session exists.
func (h *Hub) Send(_ context.Context, environmentID string, f *protocol.Frame) error {
	h.mu.Lock()
	s := h.byEnv[environmentID]
	h.mu.Unlock()
	if s == nil {
		return jobs.ErrAgentOffline
	}
	return s.send(f)
}

// Session returns the live session of an agent (nil when offline).
func (h *Hub) Session(agentID string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byAgent[agentID]
}

// EnvironmentSession returns the live session of an environment.
func (h *Hub) EnvironmentSession(environmentID string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byEnv[environmentID]
}

// ErrRequestTimeout means the agent did not answer a request in time. It
// is protocol.ErrRequestTimeout, so packages that must not import this one
// (it imports the API) can match it too.
var ErrRequestTimeout = protocol.ErrRequestTimeout

// RequestError is an error frame the agent answered a request with.
type RequestError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *RequestError) Error() string { return "agent: " + e.Code + ": " + e.Message }

// AgentCode returns the protocol error code (lets packages that do not
// import agents classify the error).
func (e *RequestError) AgentCode() string { return e.Code }

// ProtocolCode returns the error frame code (protocol.CodedError).
func (e *RequestError) ProtocolCode() string { return e.Code }

// ProtocolMessage returns the error frame message (protocol.CodedError).
func (e *RequestError) ProtocolMessage() string { return e.Message }

// Request sends a named request to the agent's live session and waits for
// its response (timeout 0: SessionOptions.RequestTimeout). Errors:
// jobs.ErrAgentOffline (no session, or the session ended), *RequestError,
// ErrRequestTimeout, or ctx's error. Mutating requests are never re-sent.
func (h *Hub) Request(ctx context.Context, agentID, name string, input any, timeout time.Duration) (json.RawMessage, error) {
	s := h.Session(agentID)
	if s == nil {
		return nil, jobs.ErrAgentOffline
	}
	return s.Request(ctx, name, input, timeout)
}

// RequestEnvironment is Request addressed by environment.
func (h *Hub) RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error) {
	s := h.EnvironmentSession(environmentID)
	if s == nil {
		return nil, jobs.ErrAgentOffline
	}
	return s.Request(ctx, name, input, timeout)
}

// OpenStream opens a byte stream (files.download, files.upload,
// container.logs, container.exec, ...) on the environment's live session.
// jobs.ErrAgentOffline without a session. See Session.OpenStream.
func (h *Hub) OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	s := h.EnvironmentSession(environmentID)
	if s == nil {
		return nil, jobs.ErrAgentOffline
	}
	return s.OpenStream(ctx, kind, input, o)
}

// SessionInfo describes a live session (diagnostics).
type SessionInfo struct {
	SessionID     string
	AgentID       string
	EnvironmentID string
	Online        bool
	StartedAt     time.Time
}

// Sessions lists the live sessions ordered by agent ID.
func (h *Hub) Sessions() []SessionInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionInfo, 0, len(h.byAgent))
	for _, s := range h.byAgent {
		out = append(out, SessionInfo{SessionID: s.id, AgentID: s.p.AgentID, EnvironmentID: s.p.EnvironmentID,
			Online: h.online[s.p.EnvironmentID], StartedAt: s.started})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// kick closes an agent's live session with code (revocation: 4403).
func (h *Hub) kick(agentID string, code websocket.StatusCode, reason string) {
	if s := h.Session(agentID); s != nil {
		s.closeWith(code, reason)
	}
}

// Shutdown closes every session with 1001 (going away; agents reconnect
// with backoff) and waits up to ctx for them to end. New sessions are
// refused afterwards. http.Server.Shutdown does not close hijacked
// WebSocket connections, so the manager calls this first.
func (h *Hub) Shutdown(ctx context.Context) {
	h.mu.Lock()
	h.shutdown = true
	sessions := make([]*Session, 0, len(h.conns))
	for s := range h.conns {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.closeWith(protocol.CloseGoingAway, "manager shutting down")
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// track registers a connection being served (false while shutting down).
func (h *Hub) track(s *Session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shutdown {
		return false
	}
	h.conns[s] = struct{}{}
	h.wg.Add(1)
	return true
}

func (h *Hub) untrack(s *Session) {
	h.mu.Lock()
	delete(h.conns, s)
	h.mu.Unlock()
	h.wg.Done()
}

// attach registers s as its agent's session, closing an older one (4409).
func (h *Hub) attach(s *Session) error {
	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		return errors.New("manager is shutting down")
	}
	old := h.byAgent[s.p.AgentID]
	if o := h.byEnv[s.p.EnvironmentID]; o != nil && o != old {
		// Another agent's session for the same environment (it was
		// replaced; its revocation kick is in flight).
		defer o.closeWith(protocol.CloseRevoked, "replaced by another agent")
		delete(h.byAgent, o.p.AgentID)
	}
	h.byAgent[s.p.AgentID] = s
	h.byEnv[s.p.EnvironmentID] = s
	delete(h.online, s.p.EnvironmentID)
	h.mu.Unlock()
	if old != nil {
		h.log.Warn("a newer session of the same agent took over; closing the older one", "agent_id", s.p.AgentID,
			"old_session_id", old.id, "session_id", s.id)
		old.closeWith(protocol.CloseReplaced, "a newer session with the same credential took over")
	}
	return nil
}

// markOnline reports the environment online if s is still its session.
func (h *Hub) markOnline(s *Session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byEnv[s.p.EnvironmentID] != s {
		return false
	}
	h.online[s.p.EnvironmentID] = true
	return true
}

// detach removes s if it is still current and reports whether it was, and
// whether the environment had been online.
func (h *Hub) detach(s *Session) (current, wasOnline bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byAgent[s.p.AgentID] == s {
		delete(h.byAgent, s.p.AgentID)
	}
	if h.byEnv[s.p.EnvironmentID] != s {
		return false, false
	}
	delete(h.byEnv, s.p.EnvironmentID)
	wasOnline = h.online[s.p.EnvironmentID]
	delete(h.online, s.p.EnvironmentID)
	return true, wasOnline
}

func (h *Hub) reconcilerList() []Reconciler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Reconciler(nil), h.reconcilers...)
}

func (h *Hub) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("agents.Hub{sessions: %d}", len(h.byAgent))
}
