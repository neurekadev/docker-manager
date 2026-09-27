// Package containerio is the manager side of container logs and exec
// terminals (#8): bounded log tails and live log feeds relayed from the
// environment's agent, and exec sessions whose WebSocket (server/ws) is
// relayed to the agent's container.exec stream with short-lived one-use
// attach tickets, idle and maximum durations, permission re-checks, clean
// disconnects and audit records of the session's end. It implements
// api.ContainerIOService; the API authorizes (container.logs.read,
// api.AuthorizeExec). Log lines and terminal I/O are never logged or
// stored.
package containerio

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/server/ws"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
)

// Agents is the session hub as used here (*agents.Hub).
type Agents interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
}

// FeatureHub reports whether an environment's connected agent announced a
// capabilities feature (implemented by *agents.Hub; optional on Agents).
type FeatureHub interface {
	EnvironmentHasFeature(environmentID, feature string) bool
}

// Limits of exec sessions (docs/internal/api/streams.md).
type Limits struct {
	// AttachWindow: a session must be attached this soon (default 60 s).
	AttachWindow time.Duration
	// IdleTimeout ends a session without input or output (default 30 min).
	IdleTimeout time.Duration
	// MaxDuration ends any session (default 8 h).
	MaxDuration time.Duration
	// Recheck is how often an attached session re-checks container.exec
	// (default 15 s).
	Recheck time.Duration
	// PerPrincipal and PerContainer bound open sessions (default 4 and 8).
	PerPrincipal int
	PerContainer int
	// LogBuffer bounds the lines queued for a slow log client (default
	// 1024); more are dropped and counted.
	LogBuffer int
}

func (l Limits) withDefaults() Limits {
	if l.AttachWindow <= 0 {
		l.AttachWindow = 60 * time.Second
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = 30 * time.Minute
	}
	if l.MaxDuration <= 0 {
		l.MaxDuration = 8 * time.Hour
	}
	if l.Recheck <= 0 {
		l.Recheck = 15 * time.Second
	}
	if l.PerPrincipal <= 0 {
		l.PerPrincipal = 4
	}
	if l.PerContainer <= 0 {
		l.PerContainer = 8
	}
	if l.LogBuffer <= 0 {
		l.LogBuffer = 1024
	}
	return l
}

// Options configures the service.
type Options struct {
	Agents Agents
	Clock  clock.Clock
	Logger *slog.Logger
	// PublicURL is the manager's origin: cookie-authenticated upgrades must
	// come from it (browsers send Origin).
	PublicURL *url.URL
	// PingInterval is the WebSocket keep-alive (DOCKER_MANAGER_STREAM_HEARTBEAT).
	PingInterval time.Duration
	// RequestTimeout bounds agent requests (default 30 s).
	RequestTimeout time.Duration
	Limits         Limits
}

// Service implements api.ContainerIOService.
type Service struct {
	opts   Options
	limits Limits
	log    *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

var _ api.ContainerIOService = (*Service)(nil)

// New returns the service.
func New(o Options) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	return &Service{opts: o, limits: o.Limits.withDefaults(), log: o.Logger.With("component", "containerio"), sessions: map[string]*session{},
		stopCh: make(chan struct{})}
}

// Close ends every exec session (manager shutdown: close 1001) and waits.
func (s *Service) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stopCh)
	}
	all := make([]*session, 0, len(s.sessions))
	for _, x := range s.sessions {
		all = append(all, x)
	}
	s.mu.Unlock()
	for _, x := range all {
		x.end(websocket.StatusGoingAway, "manager shutting down")
	}
	s.wg.Wait()
}

// agentErr maps hub and agent errors to Docker errors.
func agentErr(err error) error {
	var ce protocol.CodedError
	var sce *streammux.CloseError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobs.ErrAgentOffline), errors.Is(err, streammux.ErrSessionClosed):
		return &domain.DockerError{Code: domain.DockerEnvironmentOffline, Message: "the environment's agent is not connected"}
	case errors.Is(err, protocol.ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return &domain.DockerError{Code: domain.DockerTimeout, Message: "the environment's agent did not answer in time"}
	case errors.As(err, &sce) && !sce.Local:
		return codeErr(sce.Code, sce.Message)
	case errors.As(err, &ce):
		return codeErr(ce.ProtocolCode(), ce.ProtocolMessage())
	}
	return err
}

func codeErr(code, msg string) error {
	switch code {
	case protocol.CodeNotFound:
		return &domain.DockerError{Code: domain.DockerNotFound, Message: msg}
	case protocol.CodeConflict:
		return &domain.DockerError{Code: domain.DockerConflict, Message: msg}
	case protocol.CodeInvalidFrame, protocol.CodeInvalidArgument:
		return &domain.DockerError{Code: domain.DockerInvalid, Message: msg}
	case protocol.CodeEngineUnavailable:
		return &domain.DockerError{Code: domain.DockerEngineUnavailable, Message: "the agent cannot reach its Docker Engine"}
	case protocol.CodeUnsupportedRequest, protocol.CodeUnsupportedStream:
		return &domain.DockerError{Code: domain.DockerAgentUnsupported, Message: "the environment's agent does not support logs and terminals; upgrade it"}
	case protocol.CodeBusy, protocol.CodeStreamLimit:
		return &domain.DockerError{Code: domain.DockerBusy, Message: "the agent is busy; retry"}
	case protocol.CodeDeadlineExceeded:
		return &domain.DockerError{Code: domain.DockerTimeout, Message: "the Docker Engine did not answer in time"}
	case protocol.CodeCommandNotFound:
		return &domain.DockerError{Code: domain.DockerCommandNotFound, Message: msg}
	}
	return &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent failed (" + code + ")"}
}

// Logs returns a bounded tail of a container's output.
func (s *Service) Logs(ctx context.Context, env string, in protocol.ContainerLogsInput) (protocol.ContainerLogsOutput, error) {
	var out protocol.ContainerLogsOutput
	if s.opts.Agents == nil {
		return out, agentErr(jobs.ErrAgentOffline)
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqContainerLogs, in, s.opts.RequestTimeout)
	if err != nil {
		return out, agentErr(err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent answered with malformed logs"}
	}
	return out, nil
}

// FollowLogs opens the agent's container.logs stream and decodes it into a
// bounded queue: a slow client loses lines (counted), never the agent's
// throughput or the manager's memory.
func (s *Service) FollowLogs(ctx context.Context, env string, in protocol.ContainerLogsInput) (api.LogFeed, error) {
	if s.opts.Agents == nil {
		return nil, agentErr(jobs.ErrAgentOffline)
	}
	fctx, cancel := context.WithCancel(ctx)
	st, err := s.opts.Agents.OpenStream(fctx, env, protocol.StreamContainerLogs, in, streammux.OpenOptions{})
	if err != nil {
		cancel()
		return nil, agentErr(err)
	}
	f := &feed{st: st, cancel: cancel, ch: make(chan protocol.LogLine, s.limits.LogBuffer), done: make(chan struct{})}
	go f.pump()
	return f, nil
}

// feed decodes NDJSON log records into a bounded channel.
type feed struct {
	st     *streammux.Stream
	cancel context.CancelFunc
	ch     chan protocol.LogLine
	done   chan struct{}

	mu      sync.Mutex
	dropped int
	err     error
}

// maxRecord bounds one NDJSON record (a 16 KiB line, base64, framing).
const maxRecord = 64 << 10

func (f *feed) pump() {
	defer close(f.done)
	sc := bufio.NewScanner(f.st)
	sc.Buffer(make([]byte, 0, 64<<10), maxRecord)
	for sc.Scan() {
		var l protocol.LogLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		select {
		case f.ch <- l:
		default:
			f.mu.Lock()
			f.dropped++
			f.mu.Unlock()
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	var ce *streammux.CloseError
	if errors.As(err, &ce) && ce.Code == protocol.CodeNotFound {
		err = domain.ErrContainerGone
	} else if !errors.Is(err, io.EOF) {
		err = agentErr(err)
	} else {
		err = domain.ErrContainerGone
	}
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// Next returns the next line, a dropped count (after the lines queued
// before the drop), or the feed's end.
func (f *feed) Next(ctx context.Context) (api.LogEvent, error) {
	select {
	case l := <-f.ch:
		return api.LogEvent{Line: l}, nil
	default:
	}
	f.mu.Lock()
	if n := f.dropped; n > 0 {
		f.dropped = 0
		f.mu.Unlock()
		return api.LogEvent{Dropped: n}, nil
	}
	f.mu.Unlock()
	select {
	case l := <-f.ch:
		return api.LogEvent{Line: l}, nil
	case <-ctx.Done():
		return api.LogEvent{}, ctx.Err()
	case <-f.done:
		select {
		case l := <-f.ch:
			return api.LogEvent{Line: l}, nil
		default:
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if n := f.dropped; n > 0 {
			f.dropped = 0
			return api.LogEvent{Dropped: n}, nil
		}
		return api.LogEvent{}, f.err
	}
}

// Close ends the feed and the agent's stream.
func (f *feed) Close() {
	f.cancel()
	f.st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
}

// session is one exec session.
type session struct {
	s          *Service
	id         string
	env        string
	container  string
	execID     string
	principal  authz.Principal
	res        authz.Resource
	ticketHash [32]byte
	created    time.Time
	expires    time.Time
	tty        bool

	mu       sync.Mutex
	attached bool
	ended    bool
	stop     func(code websocket.StatusCode, reason string)
	allowed  func(ctx context.Context) bool
}

func (x *session) end(code websocket.StatusCode, reason string) {
	x.mu.Lock()
	stop := x.stop
	x.mu.Unlock()
	if stop != nil {
		stop(code, reason)
		return
	}
	x.s.forget(x, true)
}

// samePrincipal: the session belongs to this caller (the same user,
// through a session or one of their tokens).
func samePrincipal(a, b authz.Principal) bool {
	return a.Kind == b.Kind && a.UserID == b.UserID && a.TokenID == b.TokenID
}

// CreateExec creates an exec instance on the agent and a session with a
// one-use attach ticket.
func (s *Service) CreateExec(ctx context.Context, req api.ExecRequest) (api.ExecSession, error) {
	if s.opts.Agents == nil {
		return api.ExecSession{}, agentErr(jobs.ErrAgentOffline)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return api.ExecSession{}, &domain.DockerError{Code: domain.DockerBusy, Message: "the manager is shutting down"}
	}
	perPrincipal, perContainer := 0, 0
	for _, x := range s.sessions {
		if x.principal.UserID == req.Principal.UserID {
			perPrincipal++
		}
		if x.env == req.EnvironmentID && x.container == req.Input.ContainerID {
			perContainer++
		}
	}
	s.mu.Unlock()
	if perPrincipal >= s.limits.PerPrincipal || perContainer >= s.limits.PerContainer {
		return api.ExecSession{}, domain.ErrExecSessionLimit
	}
	in := s.execInput(req.EnvironmentID, req.Input)
	raw, err := s.opts.Agents.RequestEnvironment(ctx, req.EnvironmentID, protocol.ReqContainerExecCreate, in, s.opts.RequestTimeout)
	if err != nil {
		return api.ExecSession{}, agentErr(err)
	}
	var out protocol.ExecCreateOutput
	if err := json.Unmarshal(raw, &out); err != nil || out.ExecID == "" {
		return api.ExecSession{}, &domain.DockerError{Code: domain.DockerEngineError, Message: "the agent answered with a malformed exec"}
	}
	var tb [32]byte
	if _, err := rand.Read(tb[:]); err != nil {
		return api.ExecSession{}, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(tb[:])
	now := s.opts.Clock.Now().UTC()
	x := &session{s: s, id: ids.New(), env: req.EnvironmentID, container: req.Input.ContainerID, execID: out.ExecID,
		principal: req.Principal, res: req.Container, ticketHash: sha256.Sum256([]byte(ticket)), created: now,
		expires: now.Add(s.limits.AttachWindow), tty: req.Input.Tty}
	s.mu.Lock()
	s.sessions[x.id] = x
	s.wg.Add(1)
	s.mu.Unlock()
	// Forget the session if nobody attaches in time.
	go func() {
		defer s.wg.Done()
		t := s.opts.Clock.NewTimer(s.limits.AttachWindow)
		defer t.Stop()
		select {
		case <-t.C():
		case <-s.stopCh:
		}
		x.mu.Lock()
		attached := x.attached
		x.mu.Unlock()
		if !attached {
			s.forget(x, true)
		}
	}()
	cmd := out.Cmd
	if len(cmd) == 0 {
		cmd = in.Cmd // an agent without FeatureExecShell runs what it was sent
	}
	return api.ExecSession{ID: x.id, Ticket: ticket, ExpiresAt: x.expires, Command: cmd}, nil
}

// execInput adapts an exec input to the environment's agent: a shell goes
// only to agents announcing FeatureExecShell (receivers reject unknown
// fields); older ones get the shell's most common path as the command.
func (s *Service) execInput(environmentID string, in protocol.ExecCreateInput) protocol.ExecCreateInput {
	if in.Shell == "" {
		return in
	}
	if fh, ok := s.opts.Agents.(FeatureHub); ok && fh.EnvironmentHasFeature(environmentID, protocol.FeatureExecShell) {
		return in
	}
	if cmd, ok := protocol.LegacyShellCommand(in.Shell); ok {
		in.Cmd, in.Shell = cmd, ""
	}
	return in
}

// forget removes a session; closeStdin also asks the agent to end the
// exec's input (best effort).
func (s *Service) forget(x *session, closeStdin bool) {
	x.mu.Lock()
	already := x.ended
	x.ended = true
	x.mu.Unlock()
	s.mu.Lock()
	if s.sessions[x.id] == x {
		delete(s.sessions, x.id)
	}
	s.mu.Unlock()
	if already || !closeStdin || s.opts.Agents == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = s.opts.Agents.RequestEnvironment(ctx, x.env, protocol.ReqContainerExecDelete, protocol.ExecDeleteInput{ExecID: x.execID}, 10*time.Second)
}

// lookup finds the caller's session of the container.
func (s *Service) lookup(a api.ExecAttach) (*session, error) {
	s.mu.Lock()
	x := s.sessions[a.SessionID]
	s.mu.Unlock()
	if x == nil || x.env != a.EnvironmentID || x.container != a.ContainerID || !samePrincipal(x.principal, a.Principal) {
		return nil, domain.ErrExecSessionNotFound
	}
	return x, nil
}

// CheckAttach validates an attach before the upgrade: the caller's own
// unexpired session and its ticket.
func (s *Service) CheckAttach(_ context.Context, a api.ExecAttach) error {
	x, err := s.lookup(a)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(a.Ticket))
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ended || (!x.attached && (subtle.ConstantTimeCompare(h[:], x.ticketHash[:]) != 1 || !s.opts.Clock.Now().Before(x.expires))) {
		return domain.ErrExecSessionNotFound
	}
	return nil
}

// DeleteExec detaches the caller's session and closes the process's stdin.
func (s *Service) DeleteExec(_ context.Context, a api.ExecAttach) error {
	x, err := s.lookup(a)
	if err != nil {
		return err
	}
	x.end(websocket.StatusNormalClosure, "session closed")
	return nil
}

// Close codes of exec WebSockets (docs/internal/api/streams.md).
const (
	closeSessionExpired   websocket.StatusCode = 4401
	closeRevoked          websocket.StatusCode = 4403
	closeUnknown          websocket.StatusCode = 4404
	closeIdle             websocket.StatusCode = 4408
	closeAlreadyAttached  websocket.StatusCode = 4409
	closeCommandNotFound  websocket.StatusCode = 4422
	closeEnvironmentGone  websocket.StatusCode = 4503
	maxClientMessageBytes                      = 64<<10 + 1
)

// serverMessage is a text message to the client.
type serverMessage struct {
	Type    string `json:"type"`
	Code    any    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// clientMessage is a text message from the client.
type clientMessage struct {
	Type string `json:"type"`
	Cols uint   `json:"cols"`
	Rows uint   `json:"rows"`
}

// Attach upgrades the request and relays the session until it ends. The
// session is claimed before the upgrade: a client that sees its upgrade
// succeed owns the session, and any attachment racing with it (even one
// dialed right after the upgrade response) is refused with 4409. A failed
// upgrade gives the claim (and the ticket) back.
func (s *Service) Attach(ctx context.Context, w http.ResponseWriter, r *http.Request, a api.ExecAttach) {
	var patterns []string
	if s.opts.PublicURL != nil {
		patterns = []string{s.opts.PublicURL.Host}
	}
	var refuse websocket.StatusCode
	var refusal string
	x, err := s.lookup(a)
	if err != nil {
		refuse, refusal = closeUnknown, "exec session unknown or ended"
	}
	var rctx context.Context
	var cancel context.CancelCauseFunc
	var once sync.Once
	var endCode websocket.StatusCode
	var endReason string
	var ticketHash [32]byte
	if x != nil {
		h := sha256.Sum256([]byte(a.Ticket))
		x.mu.Lock()
		switch {
		case x.ended || !s.opts.Clock.Now().Before(x.expires) && !x.attached:
			refuse, refusal = closeUnknown, "exec session expired or ended"
		case x.attached:
			refuse, refusal = closeAlreadyAttached, "exec session already attached"
		case subtle.ConstantTimeCompare(h[:], x.ticketHash[:]) != 1:
			refuse, refusal = closeUnknown, "invalid attach ticket"
		default:
			ticketHash = x.ticketHash
			x.attached = true
			x.ticketHash = [32]byte{} // one use
			x.allowed = a.Allowed
			rctx, cancel = context.WithCancelCause(ctx)
			x.stop = func(code websocket.StatusCode, reason string) {
				once.Do(func() { endCode, endReason = code, reason })
				cancel(errStopped)
			}
		}
		x.mu.Unlock()
	}
	c, err := ws.Accept(w, r, ws.Options{Subprotocols: []string{api.ExecSubprotocol}, RequireSubprotocol: true, OriginPatterns: patterns,
		ReadLimit: maxClientMessageBytes, PingInterval: s.opts.PingInterval, Clock: s.opts.Clock})
	if err != nil {
		if refuse == 0 {
			// The response was written without an upgrade: nobody attached.
			// A session stopped meanwhile (deleted) ends; otherwise the
			// client may attach again with its ticket.
			x.mu.Lock()
			stopped := rctx.Err() != nil
			if !x.ended && !stopped {
				x.attached, x.ticketHash, x.stop = false, ticketHash, nil
			}
			x.mu.Unlock()
			cancel(nil)
			if stopped {
				s.forget(x, true)
			}
		}
		return
	}
	defer func() { _ = c.CloseNow() }()
	if refuse != 0 {
		_ = c.Close(refuse, refusal)
		return
	}
	started := s.opts.Clock.Now()
	// WebSocket reads and writes use their own context: cancelling the
	// context of a coder/websocket operation drops the connection without a
	// close frame, so it ends only after the close handshake.
	ioCtx, ioCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer ioCancel()
	exitCode, code, reason := s.relay(rctx, ioCtx, c, x)
	if endCode != 0 {
		code, reason = endCode, endReason
	}
	cancel(nil)
	s.forget(x, true)
	details := map[string]any{"sessionId": x.id, "reason": reason, "closeCode": int(code),
		"durationSeconds": int(s.opts.Clock.Since(started).Seconds())}
	if exitCode != nil {
		details["exitCode"] = *exitCode
	}
	_ = audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{Action: "container.exec.end", Actor: audit.ActorFor(x.principal),
		EnvironmentID: x.env, Targets: []domain.AuditTarget{{Type: "container", ID: x.res.ID, EnvironmentID: x.env}},
		Outcome: domain.AuditSuccess, Details: details})
	if code != 0 {
		_ = c.Close(code, reason)
	}
}

var errStopped = errors.New("containerio: session stopped")

// relay connects the WebSocket to the agent's exec stream. It returns the
// process exit code (when it exited) and the close code and reason.
func (s *Service) relay(ctx, ioCtx context.Context, c *ws.Conn, x *session) (*int, websocket.StatusCode, string) {
	st, err := s.opts.Agents.OpenStream(ctx, x.env, protocol.StreamContainerExec, protocol.ExecStreamInput{ExecID: x.execID}, streammux.OpenOptions{})
	if err != nil {
		return nil, closeEnvironmentGone, "environment offline"
	}
	defer st.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
	activity := make(chan struct{}, 1)
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	type result struct {
		exit   *int
		code   websocket.StatusCode
		reason string
	}
	done := make(chan result, 3)
	go func() { _ = c.KeepAlive(ioCtx) }()
	// Client -> agent.
	go func() {
		for {
			typ, b, err := c.Read(ioCtx)
			if err != nil {
				switch {
				case websocket.CloseStatus(err) == websocket.StatusMessageTooBig:
					done <- result{code: websocket.StatusMessageTooBig, reason: "message too big"}
				case ctx.Err() != nil:
					done <- result{}
				default:
					done <- result{code: websocket.StatusNormalClosure, reason: "client closed"}
				}
				return
			}
			touch()
			switch typ {
			case websocket.MessageBinary:
				if len(b) == 0 || b[0] != 0 {
					done <- result{code: websocket.StatusPolicyViolation, reason: "binary messages carry stdin (first byte 0)"}
					return
				}
				if _, err := st.WriteChannel("stdin", b[1:]); err != nil {
					done <- result{}
					return
				}
			case websocket.MessageText:
				var m clientMessage
				if json.Unmarshal(b, &m) != nil || m.Type != "resize" || m.Cols == 0 || m.Rows == 0 || m.Cols > 1000 || m.Rows > 1000 {
					done <- result{code: websocket.StatusPolicyViolation, reason: "unknown message"}
					return
				}
				if x.tty {
					rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					_, _ = s.opts.Agents.RequestEnvironment(rctx, x.env, protocol.ReqContainerExecResize,
						protocol.ExecResizeInput{ExecID: x.execID, Cols: m.Cols, Rows: m.Rows}, 10*time.Second)
					cancel()
				}
			}
		}
	}()
	// Agent -> client.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, ch, err := st.ReadChannel(buf)
			if n > 0 {
				touch()
				prefix := byte(1)
				if ch == protocol.LogStderr {
					prefix = 2
				}
				msg := append([]byte{prefix}, buf[:n]...)
				if werr := c.Write(ioCtx, websocket.MessageBinary, msg); werr != nil {
					done <- result{}
					return
				}
			}
			if err == nil {
				continue
			}
			var ce *streammux.CloseError
			switch {
			case errors.Is(err, io.EOF):
				var exit *int
				if rc := st.RemoteClose(); rc != nil && rc.ExitCode != nil {
					exit = rc.ExitCode
				}
				code := 0
				if exit != nil {
					code = *exit
				}
				_ = c.Write(ioCtx, websocket.MessageText, mustJSON(serverMessage{Type: "exit", Code: code}))
				done <- result{exit: exit, code: websocket.StatusNormalClosure, reason: "process exited"}
			case errors.As(err, &ce) && !ce.Local && ce.Code == protocol.CodeNotFound:
				_ = c.Write(ioCtx, websocket.MessageText, mustJSON(serverMessage{Type: "error", Code: "command_not_found",
					Message: "the command was not found in the container (the image may have no shell); choose another command"}))
				done <- result{code: closeCommandNotFound, reason: "command not found"}
			case errors.Is(err, streammux.ErrSessionClosed):
				_ = c.Write(ioCtx, websocket.MessageText, mustJSON(serverMessage{Type: "error", Code: "environment_offline",
					Message: "the environment's agent disconnected"}))
				done <- result{code: closeEnvironmentGone, reason: "environment offline"}
			case ctx.Err() != nil:
				done <- result{}
			default:
				_ = c.Write(ioCtx, websocket.MessageText, mustJSON(serverMessage{Type: "error", Code: "internal", Message: "the terminal failed"}))
				done <- result{code: websocket.StatusInternalError, reason: "terminal failed"}
			}
			return
		}
	}()
	clk := s.opts.Clock
	idle := clk.NewTimer(s.limits.IdleTimeout)
	defer idle.Stop()
	maxDur := clk.NewTimer(s.limits.MaxDuration)
	defer maxDur.Stop()
	recheck := clk.NewTicker(s.limits.Recheck)
	defer recheck.Stop()
	for {
		select {
		case r := <-done:
			if r.code == websocket.StatusNormalClosure && r.reason == "client closed" {
				_ = st.CloseWrite()
			}
			return r.exit, r.code, r.reason
		case <-activity:
			idle.Reset(s.limits.IdleTimeout)
		case <-idle.C():
			return nil, closeIdle, "idle timeout"
		case <-maxDur.C():
			return nil, closeIdle, "maximum session duration"
		case <-recheck.C():
			if x.res.Type != "" && !s.allowed(ctx, x) {
				return nil, closeRevoked, "container.exec revoked"
			}
		case <-ctx.Done():
			switch authz.CloseReason(ctx) {
			case "session_expired":
				return nil, closeSessionExpired, "session expired"
			case "permissions_changed":
				return nil, closeRevoked, "permissions changed"
			}
			if errors.Is(context.Cause(ctx), errStopped) {
				return nil, 0, ""
			}
			return nil, websocket.StatusGoingAway, "going away"
		}
	}
}

// allowed re-checks container.exec for an attached session.
func (s *Service) allowed(ctx context.Context, x *session) bool {
	x.mu.Lock()
	f := x.allowed
	x.mu.Unlock()
	return f == nil || f(ctx)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
