package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// Session is one established agent session (docs/internal/protocol/agent-v1.md,
// "Session"). Feature code uses it through Hub.Request/RequestEnvironment
// and Reconcilers.
type Session struct {
	hub     *Hub
	id      string
	p       AgentPrincipal
	conn    *websocket.Conn
	log     *slog.Logger
	started time.Time

	ctx    context.Context
	cancel context.CancelFunc
	// reqCtx carries the upgrade request's client IP and request ID (audit).
	reqCtx context.Context

	out       chan outFrame
	streamOut chan outFrame
	mux       *streammux.Mux
	activity  chan struct{}
	nextID    atomic.Uint64

	closeOnce sync.Once
	closing   atomic.Bool

	// lastSeen throttles the last-seen writes while the session lives
	// (set once established); bg tracks the watchdog and those writes.
	lastSeen *lastSeenThrottle
	bg       sync.WaitGroup

	// onlineMu orders the online transition (becomeOnline) against the
	// offline one (end), so the persisted state ends offline when a session
	// ends while it is being reported online.
	onlineMu sync.Mutex

	// slots bounds the request and rescan frames in flight to the
	// agent's limit (protocol.MaxConcurrentRequests): a request waits for
	// a slot instead of being refused as busy.
	slots chan struct{}

	mu      sync.Mutex
	pending map[string]chan requestResult
	// requests are the request names of the last capabilities frame.
	requests []string
	// features are the capabilities features the agent announced.
	features []string

	// Reader-goroutine state.
	seen          frameDedup
	eventSeq      protocol.SeqTracker
	fsSeq         protocol.SeqTracker
	haveCaps      bool
	reported      bool
	versionStatus string
}

type outFrame struct {
	f           *protocol.Frame
	closeCode   websocket.StatusCode
	closeReason string
}

type requestResult struct {
	out json.RawMessage
	err error
}

// ID returns the session ID.
func (s *Session) ID() string { return s.id }

// AgentID returns the session's agent.
func (s *Session) AgentID() string { return s.p.AgentID }

// EnvironmentID returns the session's environment.
func (s *Session) EnvironmentID() string { return s.p.EnvironmentID }

// Serves reports whether the agent advertised the named request in its
// last capabilities frame (reconcilers skip what an older agent lacks).
func (s *Session) Serves(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.requests, name)
}

// Context ends when the session ends.
func (s *Session) Context() context.Context { return s.ctx }

func (s *Session) frameID(prefix string) string {
	return prefix + "." + strconv.FormatUint(s.nextID.Add(1), 10)
}

// errSessionClosed means the frame could not be queued because the session
// is ending.
var errSessionClosed = fmt.Errorf("%w: session closed", jobs.ErrAgentOffline)

// send queues f for the writer. A full queue means the connection is stuck:
// the session is closed so the agent reconnects and reconciles.
func (s *Session) send(f *protocol.Frame) error {
	if f.RequestID != "" && !s.hasFeature(protocol.FeatureRequestID) {
		c := *f // an agent predating requestId would reject the field
		c.RequestID = ""
		f = &c
	}
	return s.queue(outFrame{f: f})
}

// hasFeature reports whether the agent announced a capabilities feature.
func (s *Session) hasFeature(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.features, name)
}

// streamQueue bounds queued stream data frames; senders wait for space
// (per-stream credit bounds what can be queued).
const streamQueue = 64

// FrameID implements streammux.Sender.
func (s *Session) FrameID(prefix string) string { return s.frameID(prefix) }

// SendControl implements streammux.Sender.
func (s *Session) SendControl(f *protocol.Frame) error { return s.send(f) }

// SendData implements streammux.Sender.
func (s *Session) SendData(ctx context.Context, f *protocol.Frame) error {
	if s.ctx.Err() != nil || s.closing.Load() {
		return errSessionClosed
	}
	select {
	case s.streamOut <- outFrame{f: f}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errSessionClosed
	}
}

// OpenStream opens a byte stream of kind on this session
// (docs/internal/protocol/agent-v1.md, "Streams"). The stream is aborted when ctx
// ends; it fails with streammux.ErrSessionClosed when the session ends,
// and with a *streammux.CloseError carrying the agent's code
// (unsupported_stream, not_found, forbidden_path, ...) when the agent
// refuses or fails it.
func (s *Session) OpenStream(ctx context.Context, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	st, err := s.mux.Open(ctx, kind, input, o)
	if errors.Is(err, streammux.ErrSessionClosed) {
		return nil, errSessionClosed
	}
	return st, err
}

func (s *Session) queue(o outFrame) error {
	if s.ctx.Err() != nil || s.closing.Load() {
		return errSessionClosed
	}
	select {
	case s.out <- o:
		return nil
	default:
		s.log.Error("agent session send queue is full; closing the session", "queue", cap(s.out))
		s.closeWith(protocol.CloseInternal, "send queue full")
		return errSessionClosed
	}
}

// fail sends an error frame and then closes the session with code.
func (s *Session) fail(code websocket.StatusCode, errCode, message, correlationID string) {
	f, err := protocol.NewFrame(protocol.TypeError, s.frameID("e"), correlationID, protocol.JobRef{},
		protocol.ErrorPayload{Code: errCode, Message: message})
	if err != nil {
		s.closeWith(code, message)
		return
	}
	if s.queue(outFrame{f: f, closeCode: code, closeReason: message}) != nil {
		s.closeWith(code, message)
	}
}

// closeWith closes the connection with code (once). The reader notices and
// ends the session.
func (s *Session) closeWith(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		if len(reason) > 120 {
			reason = reason[:120]
		}
		s.log.Info("closing agent session", "close_code", int(code), "reason", reason)
		go func() { _ = s.conn.Close(code, reason) }()
	})
}

// Request sends a named request and waits for the agent's response.
func (s *Session) Request(ctx context.Context, name string, input any, timeout time.Duration) (json.RawMessage, error) {
	if timeout <= 0 {
		timeout = s.hub.opts.RequestTimeout
	}
	var raw json.RawMessage
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	b, err := json.Marshal(protocol.RequestPayload{Name: name, Input: raw})
	if err != nil {
		return nil, err
	}
	return s.call(ctx, timeout, func(deadline time.Time) *protocol.Frame {
		return &protocol.Frame{Type: protocol.TypeRequest, ID: s.frameID("q"), Deadline: &deadline, Payload: b,
			RequestID: protocol.RequestIDOrEmpty(logging.RequestID(ctx))}
	})
}

// Rescan asks the agent for a bounded reconciliation of one watched file
// scope (docs/internal/protocol/agent-v1.md, "fs_invalidation and rescan"). Errors
// are those of Request (unsupported_request from agents without a
// watcher, not_found for scopes it does not watch).
func (s *Session) Rescan(ctx context.Context, p protocol.RescanPayload, timeout time.Duration) (protocol.RescanResult, error) {
	if timeout <= 0 {
		timeout = s.hub.opts.RequestTimeout
	}
	b, err := json.Marshal(p)
	if err != nil {
		return protocol.RescanResult{}, err
	}
	out, err := s.call(ctx, timeout, func(deadline time.Time) *protocol.Frame {
		return &protocol.Frame{Type: protocol.TypeRescan, ID: s.frameID("rs"), Deadline: &deadline, Payload: b}
	})
	if err != nil {
		return protocol.RescanResult{}, err
	}
	var res protocol.RescanResult
	if err := json.Unmarshal(out, &res); err != nil {
		return protocol.RescanResult{}, fmt.Errorf("agents: decode rescan result: %w", err)
	}
	return res, nil
}

// busyRetries and busyBackoff bound the re-sends of a request the agent
// refused as busy (at its request limit; the first wait doubles each
// time), always within the request's timeout.
const (
	busyRetries = 4
	busyBackoff = 250 * time.Millisecond
)

// call sends the request-like frame build makes (with the request's
// deadline) within timeout: it waits for one of the agent's request slots
// first, and re-sends a frame the agent refused as busy. Re-sending is
// safe for every request, mutating ones included: the agent refuses a
// busy frame before running its handler.
func (s *Session) call(ctx context.Context, timeout time.Duration, build func(deadline time.Time) *protocol.Frame) (json.RawMessage, error) {
	clk := s.hub.svc.clk
	end := clk.Now().Add(timeout)
	for attempt := 0; ; attempt++ {
		if err := s.acquire(ctx, end); err != nil {
			return nil, err
		}
		left := end.Sub(clk.Now())
		if left <= 0 {
			<-s.slots
			return nil, ErrRequestTimeout
		}
		out, err := s.roundTrip(ctx, build(end.UTC()), left)
		<-s.slots
		var re *RequestError
		if !errors.As(err, &re) || re.Code != protocol.CodeBusy || !re.Retryable || attempt == busyRetries {
			return out, err
		}
		wait := busyBackoff << attempt
		if !clk.Now().Add(wait).Before(end) {
			return out, err
		}
		if werr := s.sleep(ctx, wait); werr != nil {
			return nil, werr
		}
	}
}

// acquire takes a request slot, waiting until end at most.
func (s *Session) acquire(ctx context.Context, end time.Time) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	default:
	}
	left := end.Sub(s.hub.svc.clk.Now())
	if left <= 0 {
		return ErrRequestTimeout
	}
	t := s.hub.svc.clk.NewTimer(left)
	defer t.Stop()
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-t.C():
		return ErrRequestTimeout
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errSessionClosed
	}
}

// sleep waits d, or until ctx or the session ends.
func (s *Session) sleep(ctx context.Context, d time.Duration) error {
	t := s.hub.svc.clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errSessionClosed
	}
}

// roundTrip sends a request-like frame and waits for its response or error.
func (s *Session) roundTrip(ctx context.Context, f *protocol.Frame, timeout time.Duration) (json.RawMessage, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if err := protocol.ValidatePayload(f); err != nil {
		return nil, err
	}
	ch := make(chan requestResult, 1)
	s.mu.Lock()
	if s.pending == nil {
		s.mu.Unlock()
		return nil, errSessionClosed
	}
	s.pending[f.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.pending != nil {
			delete(s.pending, f.ID)
		}
		s.mu.Unlock()
	}()
	if err := s.send(f); err != nil {
		return nil, err
	}
	t := s.hub.svc.clk.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-t.C():
		return nil, ErrRequestTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, errSessionClosed
	}
}

// serve runs an accepted connection until it ends.
func (h *Hub) serve(ctx context.Context, conn *websocket.Conn, p AgentPrincipal, clientIP string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Session{
		hub: h, id: ids.New(), p: p, conn: conn, started: h.svc.clk.Now().UTC(), ctx: ctx, cancel: cancel, reqCtx: ctx,
		out: make(chan outFrame, h.opts.SendQueue), streamOut: make(chan outFrame, streamQueue), activity: make(chan struct{}, 1),
		pending: map[string]chan requestResult{}, slots: make(chan struct{}, protocol.MaxConcurrentRequests),
	}
	s.mux = streammux.New(s, protocol.MaxStreams)
	s.log = h.log.With("agent_id", p.AgentID, "environment_id", p.EnvironmentID, "session_id", s.id, "client_ip", clientIP)
	conn.SetReadLimit(protocol.MaxFrameSize)
	if !h.track(s) {
		_ = conn.Close(protocol.CloseGoingAway, "manager shutting down")
		return
	}
	defer h.untrack(s)

	var writer sync.WaitGroup
	writer.Add(1)
	go func() { defer writer.Done(); s.writeLoop() }()
	defer func() {
		cancel()
		writer.Wait()
		_ = conn.CloseNow()
		s.end()
		s.bg.Wait() // watchdog and last-seen writes, canceled with the session
	}()

	hello, ok := s.handshake()
	if !ok {
		return
	}
	s.log.Info("agent session established", "agent_version", hello.AgentVersion, "version_status", s.versionStatus,
		"previous_session_id", hello.PreviousSessionID)
	// sessionStarted recorded the agent as seen now.
	s.lastSeen = newLastSeenThrottle(LastSeenRefresh, h.svc.now())
	s.bg.Add(1) // the watchdog starts the last-seen writes (bg.Add while it runs)
	go func() { defer s.bg.Done(); s.watchdog() }()
	s.readLoop()
}

// handshake reads hello (within HelloTimeout), checks it and answers
// welcome. ok is false when the session was refused (closed).
func (s *Session) handshake() (protocol.HelloPayload, bool) {
	h := s.hub
	timer := h.svc.clk.NewTimer(h.opts.HelloTimeout)
	stop := make(chan struct{})
	go func() {
		select {
		case <-timer.C():
			s.closeWith(protocol.CloseProtocolError, "hello not received in time")
		case <-stop:
		case <-s.ctx.Done():
		}
	}()
	f, err := protocol.ReadFrame(s.ctx, s.conn)
	close(stop)
	timer.Stop()
	if err != nil {
		if errors.Is(err, protocol.ErrInvalidFrame) || errors.Is(err, protocol.ErrFrameTooLarge) {
			s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "the first frame must be a valid hello", "")
		}
		s.drain()
		return protocol.HelloPayload{}, false
	}
	if f.Type != protocol.TypeHello {
		s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "the first frame must be hello", f.ID)
		s.drain()
		return protocol.HelloPayload{}, false
	}
	hello, err := protocol.DecodePayload[protocol.HelloPayload](f)
	if err == nil && hello.Protocol != protocol.Version {
		s.fail(protocol.CloseVersionUnsupported, protocol.CodeVersionUnsupported,
			fmt.Sprintf("protocol %q is not supported; this manager speaks %s", hello.Protocol, protocol.Version), f.ID)
		s.drain()
		return hello, false
	}
	if err == nil {
		err = hello.Validate()
	}
	if err != nil {
		s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid hello: "+err.Error(), f.ID)
		s.drain()
		return hello, false
	}
	s.seen.add(f.ID)
	switch {
	case hello.AgentID != s.p.AgentID || hello.InstallID != s.p.InstallID:
		s.log.Warn("agent hello does not match its credential", "hello_agent_id", hello.AgentID)
		s.auditRefusal("identity_mismatch")
		s.fail(protocol.CloseUnauthorized, protocol.CodeUnauthorized, "the hello identity does not match the credential", f.ID)
		s.drain()
		return hello, false
	case hello.EngineID != s.p.EngineID:
		s.log.Warn("agent now controls a different Docker Engine", "enrolled_engine_id", s.p.EngineID, "engine_id", hello.EngineID)
		s.auditRefusal("engine_mismatch")
		s.fail(protocol.CloseRevoked, protocol.CodeConflict, fmt.Sprintf("this agent was enrolled for Docker Engine %s but now controls %s; "+
			"enroll it again with an enrollment of intent replace or new", s.p.EngineID, hello.EngineID), f.ID)
		s.drain()
		return hello, false
	}
	status, err := protocol.CheckAgentVersion(h.svc.opts.ManagerVersion, hello.AgentVersion)
	if err != nil {
		s.log.Warn("agent version outside the supported window", "agent_version", hello.AgentVersion, "error", err)
		s.auditRefusal("version_unsupported")
		s.fail(protocol.CloseVersionUnsupported, protocol.CodeVersionUnsupported, err.Error(), f.ID)
		s.drain()
		return hello, false
	}
	s.versionStatus = status
	if err := h.svc.sessionStarted(s.ctx, s.p, s.id, hello, status); err != nil {
		s.log.Warn("cannot start agent session", "error", err)
		s.fail(protocol.CloseRevoked, protocol.CodeUnauthorized, "the agent is no longer enrolled", f.ID)
		s.drain()
		return hello, false
	}
	if err := h.attach(s); err != nil {
		code := protocol.CloseGoingAway
		if errors.Is(err, errMoved) {
			code = websocket.StatusServiceRestart
		}
		s.closeWith(code, err.Error())
		s.drain()
		return hello, false
	}
	// A revocation committed after sessionStarted kicked nobody (the
	// session was not attached yet): check again now that it is.
	if !h.svc.stillActive(s.ctx, s.p) {
		s.fail(protocol.CloseRevoked, protocol.CodeUnauthorized, "the agent is no longer enrolled", f.ID)
		s.drain()
		return hello, false
	}
	welcome, err := protocol.NewFrame(protocol.TypeWelcome, s.frameID("w"), f.ID, protocol.JobRef{}, protocol.WelcomePayload{
		SessionID: s.id, ManagerVersion: h.svc.opts.ManagerVersion, EnvironmentID: s.p.EnvironmentID, AgentStatus: status,
		HeartbeatIntervalMs: h.opts.HeartbeatInterval.Milliseconds(), HeartbeatTimeoutMs: h.opts.HeartbeatTimeout.Milliseconds(),
		Limits:     protocol.DefaultLimits(),
		Generation: h.svc.opts.Generation,
	})
	if err == nil {
		err = s.send(welcome)
	}
	if err != nil {
		s.closeWith(protocol.CloseInternal, "cannot send welcome")
		s.drain()
		return hello, false
	}
	return hello, true
}

// auditRefusal records a session refused after authentication.
func (s *Session) auditRefusal(class string) {
	s.hub.svc.record(s.reqCtx, domain.AuditEvent{Action: AuditSessionDenied, Actor: audit.AgentActor(s.p.AgentID),
		Outcome: domain.AuditDenied, ErrorClass: class, EnvironmentID: s.p.EnvironmentID, Targets: agentTargets(s.p.AgentID, s.p.EnvironmentID)})
}

// drain reads until the connection is closed (so the close handshake
// completes after a refusal).
func (s *Session) drain() {
	for {
		if _, _, err := s.conn.Read(s.ctx); err != nil {
			return
		}
	}
}

// writeLoop is the only writer: queued frames in order, then a heartbeat
// whenever HeartbeatInterval passed.
func (s *Session) writeLoop() {
	h := s.hub
	hb := h.svc.clk.NewTicker(h.opts.HeartbeatInterval)
	defer hb.Stop()
	var hbSeq uint64
	for {
		var o outFrame
		// Control frames (jobs, requests, credits) go before stream data.
		select {
		case o = <-s.out:
		default:
			select {
			case <-s.ctx.Done():
				return
			case o = <-s.out:
			case o = <-s.streamOut:
			case <-hb.C():
				hbSeq++
				f, err := protocol.NewFrame(protocol.TypeHeartbeat, s.frameID("h"), "", protocol.JobRef{},
					protocol.HeartbeatPayload{Seq: hbSeq, SentAt: h.svc.clk.Now().UTC()})
				if err != nil {
					continue
				}
				o = outFrame{f: f}
			}
		}
		if err := s.write(o.f); err != nil {
			if s.ctx.Err() == nil && !s.closing.Load() {
				s.log.Warn("agent session write failed", "error", err)
				s.closeWith(protocol.CloseInternal, "write failed")
			}
			return
		}
		if o.closeCode != 0 {
			s.closeWith(o.closeCode, o.closeReason)
		}
	}
}

func (s *Session) write(f *protocol.Frame) error {
	b, err := protocol.Encode(f)
	if err != nil {
		s.log.Error("dropping an invalid outbound frame", "type", f.Type, "error", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.hub.opts.WriteTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// watchdog closes the session with 4408 when nothing arrived for
// HeartbeatTimeout. Inbound activity also refreshes the persisted
// last-seen time (throttled to LastSeenRefresh, off the read loop).
func (s *Session) watchdog() {
	h := s.hub
	t := h.svc.clk.NewTimer(h.opts.HeartbeatTimeout)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.activity:
			t.Reset(h.opts.HeartbeatTimeout)
			s.noteSeen()
		case <-t.C():
			s.log.Warn("no frame from the agent within the heartbeat timeout", "timeout", h.opts.HeartbeatTimeout)
			s.closeWith(protocol.CloseHeartbeatTimeout, "heartbeat timeout")
			return
		}
	}
}

func (s *Session) alive() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

// readLoop handles frames until the connection ends.
func (s *Session) readLoop() {
	for {
		f, err := protocol.ReadFrame(s.ctx, s.conn)
		if err != nil {
			if errors.Is(err, protocol.ErrInvalidFrame) || errors.Is(err, protocol.ErrFrameTooLarge) {
				s.log.Warn("invalid frame from agent", "error", err)
				s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid frame", "")
				s.drain()
				return
			}
			if st := websocket.CloseStatus(err); st == protocol.CloseManagerSuperseded {
				s.log.Warn("an agent refused this manager: it follows a newer generation of this instance (Docker Manager was moved to "+
					"another server); point the agent at the manager it follows, or remove it here", "close_code", int(st), "generation", s.hub.svc.opts.Generation)
			} else if st != -1 {
				s.log.Info("agent closed the session", "close_code", int(st))
			} else if s.ctx.Err() == nil && !s.closing.Load() {
				s.log.Info("agent session connection lost", "error", err)
			}
			return
		}
		s.alive()
		if !s.seen.add(f.ID) {
			continue // retransmission: never duplicate work
		}
		if err := protocol.ValidatePayload(f); err != nil {
			s.log.Warn("invalid frame payload from agent", "type", f.Type, "error", err)
			s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid "+string(f.Type)+" payload", f.ID)
			s.drain()
			return
		}
		if !s.handle(f) {
			s.drain()
			return
		}
	}
}

// handle routes one inbound frame. It returns false when the session must
// end (it was closed).
func (s *Session) handle(f *protocol.Frame) bool {
	h := s.hub
	switch f.Type {
	case protocol.TypeHeartbeat:
		return true
	case protocol.TypeCapabilities:
		c, _ := protocol.DecodePayload[protocol.CapabilitiesPayload](f)
		if c.Engine.ID != s.p.EngineID {
			s.fail(protocol.CloseRevoked, protocol.CodeConflict, "the agent now controls a different Docker Engine; enroll it again", f.ID)
			return false
		}
		if err := h.svc.capabilitiesReported(s.ctx, s.p, c); err != nil {
			s.log.Error("cannot store agent capabilities", "error", err)
			s.closeWith(protocol.CloseInternal, "cannot store capabilities")
			return false
		}
		s.haveCaps = true
		s.mu.Lock()
		s.requests = slices.Clone(c.Requests)
		s.features = slices.Clone(c.Features)
		s.mu.Unlock()
		return true
	case protocol.TypeJobReport:
		if !s.haveCaps || s.reported {
			s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "job_report must follow capabilities, once per session", f.ID)
			return false
		}
		s.reported = true
		if !s.jobFrame(f) {
			return false
		}
		// Reconcilers issue requests whose responses this reader must
		// deliver, so they run on their own goroutine.
		go s.becomeOnline()
		return true
	case protocol.TypeAck, protocol.TypeProgress, protocol.TypeResult:
		return s.jobFrame(f)
	case protocol.TypeResponse, protocol.TypeError:
		if f.CorrelationID == "" {
			p, _ := protocol.DecodePayload[protocol.ErrorPayload](f)
			s.log.Warn("agent reported a session error", "code", p.Code, "message", p.Message)
			return true
		}
		s.resolve(f)
		return true
	case protocol.TypeEvent:
		s.relayEvent(f)
		return true
	case protocol.TypeFSInvalidation:
		s.relayInvalidation(f)
		return true
	case protocol.TypeStreamOpen:
		// In v1 only the manager opens streams: refuse politely.
		s.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeUnsupportedStream, "agents do not open streams in v1")
		return true
	case protocol.TypeStreamData, protocol.TypeStreamCredit, protocol.TypeStreamClose:
		s.mux.Handle(f)
		return true
	}
	s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "frame type "+string(f.Type)+" is not sent by agents", f.ID)
	return false
}

// jobFrame hands a job frame to the engine and sends its replies.
func (s *Session) jobFrame(f *protocol.Frame) bool {
	j := s.hub.jobEngine()
	if j == nil {
		s.log.Error("job frame received but no job engine is attached", "type", f.Type)
		return true
	}
	replies, err := j.HandleAgentFrame(s.ctx, s.p.EnvironmentID, f)
	if err != nil {
		if errors.Is(err, protocol.ErrInvalidFrame) {
			s.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid "+string(f.Type)+" frame", f.ID)
			return false
		}
		// A database problem: reconnecting re-runs the reconciliation.
		s.log.Error("job engine could not process an agent frame", "type", f.Type, "job_id", f.JobID, "error", err)
		s.closeWith(protocol.CloseInternal, "job frame processing failed")
		return false
	}
	for _, r := range replies {
		if err := s.send(r); err != nil {
			return false
		}
	}
	return true
}

// becomeOnline runs the reconcilers and reports the environment online.
func (s *Session) becomeOnline() {
	h := s.hub
	for _, r := range h.reconcilerList() {
		if err := r(s.ctx, s); err != nil {
			if s.ctx.Err() == nil {
				s.log.Error("reconciliation after reconnect failed; closing the session", "error", err)
				s.closeWith(protocol.CloseInternal, "reconciliation failed")
			}
			return
		}
	}
	s.onlineMu.Lock()
	if s.ctx.Err() != nil || s.closing.Load() || !h.markOnline(s) {
		s.onlineMu.Unlock()
		return
	}
	h.svc.setOnline(s.ctx, s.p, true)
	s.onlineMu.Unlock()
	// The environment's history while offline is unknown: consumers
	// refetch its inventory (#23).
	h.svc.publish(events.Event{Type: events.EnvironmentResync, ResourceType: events.ResourceEnvironment,
		ResourceID: s.p.EnvironmentID, EnvironmentID: s.p.EnvironmentID, Attributes: map[string]string{"reason": "reconnect"}})
	if j := h.jobEngine(); j != nil {
		j.Wake()
	}
	// A rotation requested while the agent was offline is delivered now.
	ctx, cancel := context.WithTimeout(s.ctx, RotationTimeout)
	defer cancel()
	h.svc.deliverRotation(ctx, s.p.AgentID)
}

// resolve completes a pending request.
func (s *Session) resolve(f *protocol.Frame) {
	s.mu.Lock()
	ch := s.pending[f.CorrelationID]
	if ch != nil {
		delete(s.pending, f.CorrelationID)
	}
	s.mu.Unlock()
	if ch == nil {
		return // late or unknown answer
	}
	if f.Type == protocol.TypeError {
		p, _ := protocol.DecodePayload[protocol.ErrorPayload](f)
		ch <- requestResult{err: &RequestError{Code: p.Code, Message: p.Message, Retryable: p.Retryable}}
		return
	}
	var out json.RawMessage
	if len(f.Payload) > 0 {
		p, _ := protocol.DecodePayload[protocol.ResponsePayload](f)
		out = p.Output
	}
	ch <- requestResult{out: out}
}

// eventAttributes are the Docker event attributes relayed onto the bus.
var eventAttributes = map[string]bool{"name": true, "image": true, "exitCode": true, "health": true, "signal": true}

func (s *Session) relayEvent(f *protocol.Frame) {
	p, _ := protocol.DecodePayload[protocol.EventPayload](f)
	res, missed := s.eventSeq.Observe(p.Seq)
	switch res {
	case protocol.SeqDuplicate:
		return
	case protocol.SeqGap:
		s.log.Info("agent event sequence gap; resynchronizing", "missed", missed)
		s.hub.svc.publish(events.Event{Type: events.EnvironmentResync, ResourceType: events.ResourceEnvironment,
			ResourceID: s.p.EnvironmentID, EnvironmentID: s.p.EnvironmentID, Attributes: map[string]string{"reason": "event_gap"}})
	}
	attrs := map[string]string{"source": p.Source, "type": p.Type, "action": p.Action}
	for k, v := range p.Attributes {
		if eventAttributes[k] && len(v) <= 256 {
			attrs[k] = v
		}
	}
	s.hub.svc.publish(events.Event{Type: events.DockerEvent, ResourceType: p.Type, ResourceID: p.ResourceID,
		EnvironmentID: s.p.EnvironmentID, At: p.At.UTC(), Attributes: attrs})
}

func (s *Session) relayInvalidation(f *protocol.Frame) {
	p, _ := protocol.DecodePayload[protocol.FSInvalidationPayload](f)
	res, missed := s.fsSeq.Observe(p.Seq)
	switch res {
	case protocol.SeqDuplicate:
		return
	case protocol.SeqGap:
		// Unknown scopes changed: invalidate every file scope of the
		// environment (the #23 consumer rescans what is open).
		s.log.Info("agent file invalidation sequence gap; invalidating all file scopes", "missed", missed)
		s.hub.svc.publish(events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "*",
			EnvironmentID: s.p.EnvironmentID, Overflow: true, Attributes: map[string]string{"reason": "sequence_gap"}})
	}
	s.hub.svc.publish(events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope,
		ResourceID: p.Scope.Kind + ":" + p.Scope.ID, EnvironmentID: s.p.EnvironmentID, At: p.At.UTC(), Paths: p.Paths,
		Overflow: p.Overflow || len(p.Paths) == 0, Attributes: map[string]string{"scopeKind": p.Scope.Kind, "scopeId": p.Scope.ID}})
}

// end cleans up after the connection ended.
func (s *Session) end() {
	s.closing.Store(true)
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- requestResult{err: errSessionClosed}
	}
	s.mux.CloseAll()
	h := s.hub
	s.onlineMu.Lock()
	current, wasOnline := h.detach(s)
	if current && s.versionStatus != "" { // established
		// Record the transition even if the environment never became online
		// (last seen, session cleared).
		h.svc.setOnline(context.WithoutCancel(s.ctx), s.p, false)
	}
	s.onlineMu.Unlock()
	if !current {
		return
	}
	if j := h.jobEngine(); j != nil {
		j.AgentDisconnected(s.p.EnvironmentID)
	}
	s.log.Info("agent session ended", "was_online", wasOnline)
}

// frameDedup remembers the last 1024 inbound frame IDs.
type frameDedup struct {
	ring [1024]string
	set  map[string]struct{}
	pos  int
}

// add records id and reports whether it is new.
func (d *frameDedup) add(id string) bool {
	if d.set == nil {
		d.set = make(map[string]struct{}, len(d.ring))
	}
	if _, dup := d.set[id]; dup {
		return false
	}
	if old := d.ring[d.pos]; old != "" {
		delete(d.set, old)
	}
	d.ring[d.pos] = id
	d.set[id] = struct{}{}
	d.pos = (d.pos + 1) % len(d.ring)
	return true
}
