package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// maxConcurrentRequests bounds requests served at once per session.
const maxConcurrentRequests = 16

// writeTimeout bounds one frame write.
const writeTimeout = 10 * time.Second

// streamQueue bounds queued stream_data frames (they wait for space
// instead of closing the session; control frames have their own queue).
const streamQueue = 64

// conn is one established WebSocket session.
type conn struct {
	c      *Client
	ws     *websocket.Conn
	parent context.Context
	ctx    context.Context
	cancel context.CancelFunc

	out       chan outFrame
	streamOut chan outFrame
	mux       *streammux.Mux
	activity  chan struct{}
	ready     atomic.Bool
	closing   atomic.Bool
	once      sync.Once
	nextID    atomic.Uint64
	idBase    string
	reportID  string

	eventSeq atomic.Uint64
	fsSeq    atomic.Uint64

	sessionID    string
	hbStart      chan time.Duration
	hbTimeout    time.Duration
	seen         dedup
	lastError    atomic.Pointer[protocol.ErrorPayload]
	requestSlots chan struct{}
	wg           sync.WaitGroup
}

type outFrame struct {
	f         *protocol.Frame
	closeCode websocket.StatusCode
	reason    string
}

func (k *conn) frameID(prefix string) string {
	return prefix + "." + k.idBase + "." + strconv.FormatUint(k.nextID.Add(1), 10)
}

func (k *conn) send(f *protocol.Frame) error {
	return k.queue(outFrame{f: f})
}

// FrameID implements streammux.Sender.
func (k *conn) FrameID(prefix string) string { return k.frameID(prefix) }

// SendControl implements streammux.Sender.
func (k *conn) SendControl(f *protocol.Frame) error { return k.send(f) }

// SendData implements streammux.Sender: stream frames wait for queue space
// (flow control bounds them) instead of failing the session.
func (k *conn) SendData(ctx context.Context, f *protocol.Frame) error {
	if k.ctx.Err() != nil || k.closing.Load() {
		return ErrNotConnected
	}
	select {
	case k.streamOut <- outFrame{f: f}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-k.ctx.Done():
		return ErrNotConnected
	}
}

func (k *conn) queue(o outFrame) error {
	if k.ctx.Err() != nil || k.closing.Load() {
		return ErrNotConnected
	}
	select {
	case k.out <- o:
		return nil
	default:
		k.c.log.Error("session send queue is full; reconnecting")
		k.closeWith(protocol.CloseInternal, "send queue full")
		return ErrNotConnected
	}
}

func (k *conn) closeWith(code websocket.StatusCode, reason string) {
	k.once.Do(func() {
		k.closing.Store(true)
		go func() { _ = k.ws.Close(code, reason) }()
	})
}

// fail sends an error frame, then closes with code.
func (k *conn) fail(code websocket.StatusCode, errCode, msg, correlationID string) {
	f, err := protocol.NewFrame(protocol.TypeError, k.frameID("e"), correlationID, protocol.JobRef{}, protocol.ErrorPayload{Code: errCode, Message: msg})
	if err != nil || k.queue(outFrame{f: f, closeCode: code, reason: msg}) != nil {
		k.closeWith(code, msg)
	}
}

// serve runs the session: hello, welcome, capabilities, job report, then
// frames until the connection ends. It returns the reason it ended.
func (k *conn) serve(cred *state.Credential, installID string, caps protocol.CapabilitiesPayload) error {
	c := k.c
	defer func() {
		c.current.CompareAndSwap(k, nil)
		stopping := k.parent.Err() != nil
		k.cancel()
		k.mux.CloseAll() // stream handlers see the session end
		k.wg.Wait()
		if stopping && !k.closing.Load() {
			// The agent is stopping: tell the manager (orderly close) so it
			// marks the environment offline at once.
			_ = k.ws.Close(protocol.CloseNormal, "agent stopping")
		}
		_ = k.ws.CloseNow()
	}()
	k.idBase = newFrameID()
	k.requestSlots = make(chan struct{}, maxConcurrentRequests)
	k.hbStart = make(chan time.Duration, 1)
	k.wg.Add(1)
	go func() { defer k.wg.Done(); k.writeLoop() }()

	hello, err := protocol.NewFrame(protocol.TypeHello, k.frameID("hello"), "", protocol.JobRef{}, protocol.HelloPayload{
		Protocol: protocol.Version, AgentID: cred.AgentID, AgentVersion: c.opts.AgentVersion, InstallID: installID,
		EngineID: caps.Engine.ID, PreviousSessionID: c.lastSession,
	})
	if err != nil {
		k.closeWith(protocol.CloseInternal, "invalid hello")
		return err
	}
	if err := k.send(hello); err != nil {
		return err
	}
	welcome, err := k.awaitWelcome(hello.ID)
	if err != nil {
		return k.ended(err)
	}
	if accept := c.opts.AcceptWelcome; accept != nil {
		if err := accept(welcome); err != nil {
			// Refused before the read loop starts: nothing the manager
			// sends is handled. The close code is retryable, so Run
			// reconnects with backoff and the credential stays.
			k.closeWith(protocol.CloseManagerSuperseded, "manager generation superseded")
			_ = k.drain(err)
			return fmt.Errorf("refused the manager's session: %w", err)
		}
	}
	k.sessionID = welcome.SessionID
	c.lastSession = welcome.SessionID
	k.hbStart <- time.Duration(welcome.HeartbeatIntervalMs) * time.Millisecond
	k.hbTimeout = time.Duration(welcome.HeartbeatTimeoutMs) * time.Millisecond
	c.log.Info("manager session established", "session_id", welcome.SessionID, "environment_id", welcome.EnvironmentID,
		"manager_version", welcome.ManagerVersion, "agent_status", welcome.AgentStatus)
	if welcome.AgentStatus == protocol.VersionOutdated {
		c.log.Warn("this agent is one minor release behind the manager; upgrade it (manager first, then agents)",
			"manager_version", welcome.ManagerVersion, "agent_version", c.opts.AgentVersion)
	}
	c.current.Store(k)
	if err := k.startSession(); err != nil {
		return k.ended(err)
	}
	c.status(Status{State: StateConnected, SessionID: k.sessionID})
	k.wg.Add(1)
	go func() { defer k.wg.Done(); k.watchdog() }()
	return k.ended(k.readLoop())
}

// startSession sends the capabilities and the job report, and opens the
// session for runner sends. The report is built and queued under sendMu, so
// a job result journaled concurrently is either in the report or sent
// after it (a duplicate result is harmless: the manager reconciles).
func (k *conn) startSession() error {
	c := k.c
	capsFrame, ok := c.capabilitiesFrame(k)
	if !ok {
		return errors.New("capabilities unavailable")
	}
	if err := k.send(capsFrame); err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	report := protocol.JobReportPayload{Jobs: []protocol.JobReportEntry{}}
	if r := c.jobRunner(); r != nil {
		report = r.Report()
	}
	f, err := protocol.NewFrame(protocol.TypeJobReport, k.frameID("report"), "", protocol.JobRef{}, report)
	if err != nil {
		return err
	}
	k.reportID = f.ID
	if err := k.send(f); err != nil {
		return err
	}
	k.ready.Store(true)
	return nil
}

// awaitWelcome reads the welcome (or a refusal) within WelcomeTimeout.
func (k *conn) awaitWelcome(helloID string) (protocol.WelcomePayload, error) {
	c := k.c
	t := c.opts.Clock.NewTimer(c.opts.WelcomeTimeout)
	stop := make(chan struct{})
	go func() {
		select {
		case <-t.C():
			k.closeWith(protocol.CloseProtocolError, "welcome not received in time")
		case <-stop:
		case <-k.ctx.Done():
		}
	}()
	defer func() { close(stop); t.Stop() }()
	for {
		f, err := protocol.ReadFrame(k.ctx, k.ws)
		if err != nil {
			return protocol.WelcomePayload{}, err
		}
		if err := protocol.ValidatePayload(f); err != nil {
			k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid frame before welcome", f.ID)
			return protocol.WelcomePayload{}, err
		}
		switch f.Type {
		case protocol.TypeWelcome:
			if f.CorrelationID != helloID {
				k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "welcome does not answer hello", f.ID)
				return protocol.WelcomePayload{}, errors.New("welcome does not answer hello")
			}
			w, _ := protocol.DecodePayload[protocol.WelcomePayload](f)
			return w, nil
		case protocol.TypeError:
			p, _ := protocol.DecodePayload[protocol.ErrorPayload](f)
			k.lastError.Store(&p)
			c.log.Error("the manager refused the session", "code", p.Code, "message", p.Message)
		case protocol.TypeHeartbeat:
		default:
			k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "unexpected "+string(f.Type)+" before welcome", f.ID)
			return protocol.WelcomePayload{}, fmt.Errorf("unexpected %s before welcome", f.Type)
		}
	}
}

// ended turns a read error into the session's outcome: a StopError for
// non-retryable close codes, otherwise the error (reconnect).
func (k *conn) ended(err error) error {
	code := websocket.CloseStatus(err)
	if code == -1 {
		return err
	}
	reason := ""
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		reason = ce.Reason
	}
	if p := k.lastError.Load(); p != nil {
		reason = p.Message
	}
	if !protocol.ReconnectAllowed(code) {
		return &StopError{CloseCode: code, Reason: reason}
	}
	return fmt.Errorf("session closed (%d %s): %w", code, reason, err)
}

func (k *conn) writeLoop() {
	c := k.c
	var hb interface {
		C() <-chan time.Time
		Stop()
	}
	var hbC <-chan time.Time
	defer func() {
		if hb != nil {
			hb.Stop()
		}
	}()
	var seq uint64
	for {
		var o outFrame
		// Control frames (job traffic, requests, credits) go before queued
		// stream data.
		select {
		case o = <-k.out:
		default:
			select {
			case <-k.ctx.Done():
				return
			case d := <-k.hbStart:
				// The heartbeat interval is known once welcome arrived.
				if hb == nil && d > 0 {
					t := c.opts.Clock.NewTicker(d)
					hb, hbC = t, t.C()
				}
				continue
			case o = <-k.out:
			case o = <-k.streamOut:
			case <-hbC:
				seq++
				f, err := protocol.NewFrame(protocol.TypeHeartbeat, k.frameID("hb"), "", protocol.JobRef{},
					protocol.HeartbeatPayload{Seq: seq, SentAt: c.opts.Clock.Now().UTC()})
				if err != nil {
					continue
				}
				o = outFrame{f: f}
			}
		}
		b, err := protocol.Encode(o.f)
		if err != nil {
			c.log.Error("dropping an invalid outbound frame", "type", o.f.Type, "error", err)
			continue
		}
		wctx, cancel := context.WithTimeout(k.ctx, writeTimeout)
		err = k.ws.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			if k.ctx.Err() == nil && !k.closing.Load() {
				k.closeWith(protocol.CloseInternal, "write failed")
			}
			return
		}
		if o.closeCode != 0 {
			k.closeWith(o.closeCode, o.reason)
		}
	}
}

// watchdog closes the session with 4408 when the manager was silent for the
// heartbeat timeout.
func (k *conn) watchdog() {
	c := k.c
	t := c.opts.Clock.NewTimer(k.hbTimeout)
	defer t.Stop()
	for {
		select {
		case <-k.ctx.Done():
			return
		case <-k.activity:
			t.Reset(k.hbTimeout)
		case <-t.C():
			c.log.Warn("no frame from the manager within the heartbeat timeout; reconnecting", "timeout", k.hbTimeout.String())
			k.closeWith(protocol.CloseHeartbeatTimeout, "heartbeat timeout")
			return
		}
	}
}

func (k *conn) readLoop() error {
	c := k.c
	for {
		f, err := protocol.ReadFrame(k.ctx, k.ws)
		if err != nil {
			if errors.Is(err, protocol.ErrInvalidFrame) || errors.Is(err, protocol.ErrFrameTooLarge) {
				c.log.Warn("invalid frame from the manager", "error", err)
				k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid frame", "")
				return k.drain(err)
			}
			return err
		}
		select {
		case k.activity <- struct{}{}:
		default:
		}
		if !k.seen.add(f.ID) {
			continue
		}
		if err := protocol.ValidatePayload(f); err != nil {
			c.log.Warn("invalid frame payload from the manager", "type", f.Type, "error", err)
			k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "invalid "+string(f.Type)+" payload", f.ID)
			return k.drain(err)
		}
		if err := k.handle(f); err != nil {
			return k.drain(err)
		}
	}
}

// drain waits for the close handshake after this side started closing.
func (k *conn) drain(cause error) error {
	for {
		if _, _, err := k.ws.Read(k.ctx); err != nil {
			if websocket.CloseStatus(err) != -1 {
				return err
			}
			return cause
		}
	}
}

func (k *conn) handle(f *protocol.Frame) error {
	c := k.c
	switch f.Type {
	case protocol.TypeHeartbeat:
		return nil
	case protocol.TypeCommand, protocol.TypeCancel, protocol.TypeAck:
		if f.Type == protocol.TypeAck && f.CorrelationID == k.reportID {
			c.log.Info("the manager reconciled this agent's jobs; environment online", "session_id", k.sessionID)
			c.status(Status{State: StateOnline, SessionID: k.sessionID})
		}
		r := c.jobRunner()
		if r == nil {
			if f.Type == protocol.TypeCommand {
				ack, err := protocol.NewFrame(protocol.TypeAck, k.frameID("ack"), f.ID, f.Ref(),
					protocol.AckPayload{Code: protocol.AckUnsupportedKind, Message: "this agent runs no jobs"})
				if err == nil {
					return ignoreClosed(k.send(ack))
				}
			}
			return nil
		}
		if err := r.HandleFrame(k.ctx, f); err != nil && k.ctx.Err() == nil {
			c.log.Warn("job frame failed", "type", f.Type, "job_id", f.JobID, "error", err)
		}
		return nil
	case protocol.TypeRequest:
		k.serveRequest(f)
		return nil
	case protocol.TypeRescan:
		k.serveRescan(f)
		return nil
	case protocol.TypeStreamOpen:
		k.serveStream(f)
		return nil
	case protocol.TypeStreamData, protocol.TypeStreamCredit, protocol.TypeStreamClose:
		k.mux.Handle(f)
		return nil
	case protocol.TypeError:
		p, _ := protocol.DecodePayload[protocol.ErrorPayload](f)
		if f.CorrelationID == "" {
			k.lastError.Store(&p)
			c.log.Error("the manager reported a session error", "code", p.Code, "message", p.Message)
		} else {
			c.log.Warn("the manager rejected a frame", "code", p.Code, "message", p.Message, "correlation_id", f.CorrelationID)
		}
		return nil
	}
	k.fail(protocol.CloseProtocolError, protocol.CodeInvalidFrame, "frame type "+string(f.Type)+" is not sent by managers", f.ID)
	return fmt.Errorf("unexpected %s frame from the manager", f.Type)
}

func ignoreClosed(err error) error {
	if errors.Is(err, ErrNotConnected) {
		return nil
	}
	return err
}

// serveRequest runs a named request on its own goroutine (bounded) and
// answers response or error.
func (k *conn) serveRequest(f *protocol.Frame) {
	c := k.c
	p, _ := protocol.DecodePayload[protocol.RequestPayload](f)
	h := c.handler(p.Name)
	if h == nil {
		_ = k.reply(f.ID, nil, &HandlerError{Code: protocol.CodeUnsupportedRequest, Message: p.Name + " is not served by this agent"})
		return
	}
	k.run(f, p.Name, func(ctx context.Context) (any, error) { return h(ctx, p.Input) })
}

// serveRescan answers a rescan frame (#23) like a request: bounded
// concurrency, the frame's deadline, response or error.
func (k *conn) serveRescan(f *protocol.Frame) {
	h := k.c.opts.Rescan
	if h == nil {
		_ = k.reply(f.ID, nil, &HandlerError{Code: protocol.CodeUnsupportedRequest, Message: "this agent watches no file scopes"})
		return
	}
	p, _ := protocol.DecodePayload[protocol.RescanPayload](f)
	k.run(f, "rescan", func(ctx context.Context) (any, error) { return h(ctx, p) })
}

// run serves a request-like frame on its own goroutine (at most 16 at once
// per session) and answers response or error.
func (k *conn) run(f *protocol.Frame, name string, h func(ctx context.Context) (any, error)) {
	c := k.c
	if f.Deadline != nil && !c.opts.Clock.Now().Before(*f.Deadline) {
		_ = k.reply(f.ID, nil, &HandlerError{Code: protocol.CodeDeadlineExceeded, Message: "the request deadline passed before it arrived"})
		return
	}
	select {
	case k.requestSlots <- struct{}{}:
	default:
		_ = k.reply(f.ID, nil, &HandlerError{Code: protocol.CodeBusy, Message: "too many concurrent requests", Retryable: true})
		return
	}
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		defer func() { <-k.requestSlots }()
		ctx, cancel := context.WithCancel(requestContext(k.ctx, c.log, f.RequestID, "request", name))
		defer cancel()
		var timer interface{ Stop() bool }
		if f.Deadline != nil {
			t := c.opts.Clock.NewTimer(f.Deadline.Sub(c.opts.Clock.Now()))
			timer = t
			go func() {
				select {
				case <-t.C():
					cancel()
				case <-ctx.Done():
				}
			}()
		}
		out, err := h(ctx)
		if timer != nil {
			timer.Stop()
		}
		if err != nil && errors.Is(ctx.Err(), context.Canceled) && k.ctx.Err() == nil {
			err = &HandlerError{Code: protocol.CodeDeadlineExceeded, Message: "the request deadline passed"}
		}
		var end *EndSessionError
		if errors.As(err, &end) {
			if end.Err == nil {
				k.replyAndClose(f.ID, end.Output, nil, end.closeCode(), end.Reason)
			} else {
				k.replyAndClose(f.ID, nil, err, end.closeCode(), end.Reason)
			}
			return
		}
		_ = k.reply(f.ID, out, err)
	}()
}

// serveStream accepts a stream the manager opened and runs its handler on
// its own goroutine (at most protocol.MaxStreams at once). Unknown kinds
// are refused with unsupported_stream, a full table with stream_limit.
func (k *conn) serveStream(f *protocol.Frame) {
	c := k.c
	p, _ := protocol.DecodePayload[protocol.StreamOpenPayload](f)
	h := c.opts.Streams[p.Kind]
	if h == nil {
		k.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeUnsupportedStream, p.Kind+" is not served by this agent")
		return
	}
	s, err := k.mux.Accept(k.ctx, f)
	switch {
	case errors.Is(err, streammux.ErrLimit):
		k.mux.Refuse(f.ID, protocol.CloseReasonLimit, protocol.CodeStreamLimit, "too many open streams")
		return
	case err != nil:
		k.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeInvalidFrame, "invalid stream_open")
		return
	}
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		ctx, cancel := context.WithCancel(requestContext(k.ctx, c.log, f.RequestID, "stream", p.Kind))
		defer cancel()
		go func() {
			select {
			case <-s.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		err := h(ctx, s)
		if err != nil {
			var he *HandlerError
			if !errors.As(err, &he) {
				if s.Err() == nil {
					logging.FromContext(ctx).Error("stream failed", "kind", p.Kind, "error", err)
				}
				he = &HandlerError{Code: protocol.CodeInternal, Message: "internal agent error"}
			}
			s.Abort(protocol.CloseReasonError, he.Code, he.Message)
			return
		}
		_ = s.CloseWrite()
	}()
}

// requestContext carries the manager's request ID (frame requestId, #34)
// to a request or stream handler: logging.RequestID(ctx) returns it and
// logging.FromContext(ctx) logs with request_id (and the request or stream
// name). Without an ID the session logger is used as is.
func requestContext(ctx context.Context, log *slog.Logger, requestID, what, name string) context.Context {
	l := log.With(what, name)
	if requestID != "" {
		ctx = logging.WithRequestID(ctx, requestID)
		l = l.With("request_id", requestID)
	}
	return logging.IntoContext(ctx, l)
}

// reply answers a request (or rescan) with response or error.
func (k *conn) reply(correlationID string, out any, err error) error {
	f, ferr := k.replyFrame(correlationID, out, err)
	if ferr != nil {
		return ferr
	}
	return k.send(f)
}

// replyAndClose answers a request with out or the error err, then closes
// the session with code once the answer is written (EndSessionError). code
// always allows reconnecting, so Run reconnects with backoff and the
// credential stays.
func (k *conn) replyAndClose(correlationID string, out any, err error, code websocket.StatusCode, reason string) {
	f, ferr := k.replyFrame(correlationID, out, err)
	if ferr != nil || k.queue(outFrame{f: f, closeCode: code, reason: reason}) != nil {
		k.closeWith(code, reason)
	}
}

// replyFrame builds the response or error frame answering a request.
func (k *conn) replyFrame(correlationID string, out any, err error) (*protocol.Frame, error) {
	if err != nil {
		var he *HandlerError
		if !errors.As(err, &he) {
			k.c.log.Error("request failed", "error", err)
			he = &HandlerError{Code: protocol.CodeInternal, Message: "internal agent error"}
		}
		return protocol.NewFrame(protocol.TypeError, k.frameID("e"), correlationID, protocol.JobRef{},
			protocol.ErrorPayload{Code: he.Code, Message: he.Message, Retryable: he.Retryable})
	}
	var payload protocol.ResponsePayload
	if out != nil {
		b, merr := json.Marshal(out)
		if merr != nil {
			return nil, merr
		}
		payload.Output = b
	}
	return protocol.NewFrame(protocol.TypeResponse, k.frameID("r"), correlationID, protocol.JobRef{}, payload)
}

// dedup remembers the last 1024 inbound frame IDs.
type dedup struct {
	ring [1024]string
	set  map[string]struct{}
	pos  int
}

func (d *dedup) add(id string) bool {
	if d.set == nil {
		d.set = make(map[string]struct{}, len(d.ring))
	}
	if _, ok := d.set[id]; ok {
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
