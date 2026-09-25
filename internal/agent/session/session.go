// Package session is the agent side of the manager session (GET
// /agent/v1/session, docs/protocol/agent-v1.md): it dials out with the
// agent's bearer credential over internal/agent/transport, sends hello,
// capabilities and the job report, keeps the session alive with
// heartbeats, feeds command/cancel/ack frames to the job runner
// (internal/agent/jobs), serves named requests and relays events and file
// invalidations with per-session sequence numbers. It reconnects with
// exponential backoff and full jitter, and stops for good on the
// non-retryable close codes (protocol.ReconnectAllowed).
//
// The agent never listens: everything here is outbound.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/agent/state"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// Backoff is the reconnect policy: exponential from Min to Max with full
// jitter, reset after a session stayed up for ResetAfter.
type Backoff struct {
	Min, Max   time.Duration
	ResetAfter time.Duration
	// Rand returns a value in [0, 1) (default math/rand/v2).
	Rand func() float64
}

// DefaultBackoff is the protocol's reconnect policy (1 s to 60 s, reset
// after 60 s of healthy session).
func DefaultBackoff() Backoff {
	return Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: rand.Float64}
}

// Delay returns the wait before reconnect attempt n (n >= 1).
func (b Backoff) Delay(n int) time.Duration {
	ceiling := b.Min
	for i := 1; i < n && ceiling < b.Max; i++ {
		ceiling *= 2
	}
	ceiling = min(ceiling, b.Max)
	r := b.Rand
	if r == nil {
		r = rand.Float64
	}
	return time.Duration(r() * float64(ceiling))
}

// Status is the connection state reported to the runtime (health file).
type Status struct {
	// State is connecting, connected (welcome received), online (the
	// manager reconciled the job report), disconnected (retrying) or
	// stopped (non-retryable; see Err).
	State     string
	SessionID string
	Err       error
}

// Connection states.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateOnline       = "online"
	StateDisconnected = "disconnected"
	StateStopped      = "stopped"
)

// StopError ends Run: the manager refused the session in a way that
// retrying with the same credential cannot fix.
type StopError struct {
	// CloseCode is the WebSocket close code (0 when the upgrade was refused).
	CloseCode websocket.StatusCode
	// HTTPStatus is the refused upgrade's status (0 after an upgrade).
	HTTPStatus int
	// Reason is the manager's message.
	Reason string
}

func (e *StopError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("the manager refused the session (HTTP %d): %s", e.HTTPStatus, e.Reason)
	}
	return fmt.Sprintf("the manager closed the session (%d): %s", e.CloseCode, e.Reason)
}

// Unauthorized reports whether the credential is no longer valid (revoked,
// removed, replaced or rotated out): the agent needs a new enrollment.
func (e *StopError) Unauthorized() bool {
	return e.HTTPStatus == http.StatusUnauthorized || e.CloseCode == protocol.CloseUnauthorized || e.CloseCode == protocol.CloseRevoked
}

// ErrNotConnected is returned by Send while no session is established.
var ErrNotConnected = errors.New("session: not connected to the manager")

// ErrNotEnrolled is returned by Run without a stored credential.
var ErrNotEnrolled = errors.New("session: the agent is not enrolled")

// RequestHandler serves a named request. It returns the output (encoded as
// JSON) or an error; *HandlerError chooses the protocol error code.
type RequestHandler func(ctx context.Context, input json.RawMessage) (any, error)

// StreamHandler serves a stream the manager opened (files.download,
// files.upload, container.logs, container.exec, ...). It reads and writes
// s and should finish it with s.CloseWrite or s.CloseWithResult; ctx ends
// when the stream or the session ends. A returned error aborts the stream
// (*HandlerError chooses the protocol code, others become internal).
type StreamHandler func(ctx context.Context, s *streammux.Stream) error

// HandlerError is a request failure with a protocol error code.
type HandlerError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *HandlerError) Error() string { return e.Code + ": " + e.Message }

// ProtocolCode returns the protocol error code.
func (e *HandlerError) ProtocolCode() string { return e.Code }

// JobRunner is the agent job runner (internal/agent/jobs.Runner).
type JobRunner interface {
	Report() protocol.JobReportPayload
	HandleFrame(ctx context.Context, f *protocol.Frame) error
}

// Dialer opens the WebSocket (default websocket.Dial).
type Dialer func(ctx context.Context, url string, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error)

// Options configures a Client.
type Options struct {
	State  *state.Store
	Clock  clock.Clock
	Logger *slog.Logger
	// URL is the session WebSocket URL; DialOptions returns the dial
	// options (HTTP client with the transport's TLS trust, headers).
	URL         string
	DialOptions func(header http.Header) *websocket.DialOptions
	Dial        Dialer
	// AgentVersion is sent in hello; UserAgent on the upgrade.
	AgentVersion string
	UserAgent    string
	// Capabilities returns the current capabilities; ok is false while the
	// Engine identity is unknown (the client then waits).
	Capabilities func() (protocol.CapabilitiesPayload, bool)
	// Requests are the named request handlers served besides the built-in
	// agent.credential.rotate.
	Requests map[string]RequestHandler
	// Streams are the stream handlers by kind (protocol.StreamKinds); the
	// session advertises them in the capabilities' streams.
	Streams map[string]StreamHandler
	Backoff Backoff
	// WelcomeTimeout bounds the wait for welcome (default 30 s).
	WelcomeTimeout time.Duration
	// OnStatus observes connection state changes (must not block).
	OnStatus func(Status)
	// SendQueue bounds queued outbound frames (default 512).
	SendQueue int
}

// Client maintains the agent's session. Create it with New, attach the
// job runner with SetRunner, then call Run.
type Client struct {
	opts Options
	log  *slog.Logger

	runnerMu sync.RWMutex
	runner   JobRunner

	// sendMu orders the job report against runner sends (see report).
	sendMu  sync.Mutex
	current atomic.Pointer[conn]

	events *Relay[protocol.EventPayload]
	files  *Relay[protocol.FSInvalidationPayload]

	lastSession string
}

// New returns a Client.
func New(opts Options) *Client {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Dial == nil {
		opts.Dial = websocket.Dial
	}
	if opts.Backoff.Min <= 0 || opts.Backoff.Max < opts.Backoff.Min {
		d := DefaultBackoff()
		if opts.Backoff.Rand != nil {
			d.Rand = opts.Backoff.Rand
		}
		opts.Backoff = d
	}
	if opts.WelcomeTimeout <= 0 {
		opts.WelcomeTimeout = 30 * time.Second
	}
	if opts.SendQueue <= 0 {
		opts.SendQueue = 512
	}
	c := &Client{opts: opts, log: opts.Logger.With("component", "session")}
	c.events = &Relay[protocol.EventPayload]{c: c, typ: protocol.TypeEvent, seq: func(k *conn) *atomic.Uint64 { return &k.eventSeq },
		stamp: func(p *protocol.EventPayload, seq uint64) { p.Seq = seq }}
	c.files = &Relay[protocol.FSInvalidationPayload]{c: c, typ: protocol.TypeFSInvalidation, seq: func(k *conn) *atomic.Uint64 { return &k.fsSeq },
		stamp: func(p *protocol.FSInvalidationPayload, seq uint64) { p.Seq = seq }, prepare: prepareInvalidation}
	return c
}

// SetRunner attaches the job runner (it uses the Client as its Sender).
func (c *Client) SetRunner(r JobRunner) {
	c.runnerMu.Lock()
	defer c.runnerMu.Unlock()
	c.runner = r
}

func (c *Client) jobRunner() JobRunner {
	c.runnerMu.RLock()
	defer c.runnerMu.RUnlock()
	return c.runner
}

// Events relays Docker Engine and agent events (#5 publishes into it).
func (c *Client) Events() *Relay[protocol.EventPayload] { return c.events }

// FileInvalidations relays watched file-scope changes (#15/#23 publish).
func (c *Client) FileInvalidations() *Relay[protocol.FSInvalidationPayload] { return c.files }

// Connected reports whether a session is established.
func (c *Client) Connected() bool {
	k := c.current.Load()
	return k != nil && k.ready.Load()
}

// Send queues a frame on the established session (the job runner's
// Sender). ErrNotConnected while none is: the job report of the next
// session carries what could not be sent.
func (c *Client) Send(_ context.Context, f *protocol.Frame) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	k := c.current.Load()
	if k == nil || !k.ready.Load() {
		return ErrNotConnected
	}
	return k.send(f)
}

// CapabilitiesChanged resends the capabilities on a live session.
func (c *Client) CapabilitiesChanged() {
	k := c.current.Load()
	if k == nil || !k.ready.Load() {
		return
	}
	if f, ok := c.capabilitiesFrame(k); ok {
		_ = k.send(f)
	}
}

func (c *Client) status(st Status) {
	if c.opts.OnStatus != nil {
		c.opts.OnStatus(st)
	}
}

// Run keeps a session up until ctx ends (nil) or the manager refuses the
// credential or version for good (*StopError). ErrNotEnrolled without a
// stored credential.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		cred, err := c.opts.State.Credential()
		if err != nil {
			return err
		}
		if cred == nil {
			return ErrNotEnrolled
		}
		c.status(Status{State: StateConnecting})
		started := c.opts.Clock.Now()
		err = c.runOnce(ctx, cred)
		var stop *StopError
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.As(err, &stop):
			c.log.Error("the manager refused this agent; not reconnecting", "close_code", int(stop.CloseCode),
				"http_status", stop.HTTPStatus, "reason", stop.Reason)
			c.status(Status{State: StateStopped, Err: stop})
			return stop
		}
		if c.opts.Clock.Since(started) >= c.opts.Backoff.ResetAfter {
			attempt = 0
		}
		attempt++
		delay := c.opts.Backoff.Delay(attempt)
		var ra *retryAfter
		if errors.As(err, &ra) && ra.d > delay {
			delay = ra.d
		}
		c.log.Info("manager session ended; reconnecting", "error", errString(err), "attempt", attempt, "delay", delay.String())
		c.status(Status{State: StateDisconnected, Err: err})
		t := c.opts.Clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type retryAfter struct {
	d   time.Duration
	err error
}

func (r *retryAfter) Error() string { return r.err.Error() }
func (r *retryAfter) Unwrap() error { return r.err }

// runOnce dials, handshakes and serves one session until it ends.
func (c *Client) runOnce(ctx context.Context, cred *state.Credential) error {
	caps, ok := c.opts.Capabilities()
	if !ok {
		return errors.New("the Docker Engine identity is not known yet")
	}
	installID, err := c.opts.State.InstallID()
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cred.Credential)
	h.Set("User-Agent", c.opts.UserAgent)
	dopts := c.opts.DialOptions(h)
	dopts.Subprotocols = []string{protocol.Version}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, resp, err := c.opts.Dial(dctx, c.opts.URL, dopts)
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return refusal(resp, err)
	}
	if ws.Subprotocol() != protocol.Version {
		_ = ws.Close(protocol.CloseVersionUnsupported, "subprotocol not negotiated")
		return &StopError{CloseCode: protocol.CloseVersionUnsupported, Reason: "the manager did not negotiate " + protocol.Version + "; upgrade the manager"}
	}
	ws.SetReadLimit(protocol.MaxFrameSize)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	k := &conn{c: c, ws: ws, parent: ctx, ctx: sctx, cancel: scancel, out: make(chan outFrame, c.opts.SendQueue),
		streamOut: make(chan outFrame, streamQueue), activity: make(chan struct{}, 1)}
	k.mux = streammux.New(k, protocol.MaxStreams)
	return k.serve(cred, installID, caps)
}

// refusal classifies a failed upgrade.
func refusal(resp *http.Response, err error) error {
	if resp == nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &StopError{HTTPStatus: resp.StatusCode, Reason: "the agent credential is not valid (revoked, removed or rotated out); enroll the agent again"}
	case http.StatusUpgradeRequired:
		return &StopError{HTTPStatus: resp.StatusCode, Reason: "the manager does not support this agent's protocol version; upgrade the agent (or the manager first)"}
	case http.StatusForbidden:
		return &StopError{HTTPStatus: resp.StatusCode, Reason: "the manager refused the connection (forbidden)"}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		d := 5 * time.Second
		if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s > 0 {
			d = time.Duration(s) * time.Second
		}
		return &retryAfter{d: d, err: fmt.Errorf("manager answered HTTP %d", resp.StatusCode)}
	}
	return fmt.Errorf("session upgrade failed (HTTP %d): %w", resp.StatusCode, err)
}

// capabilitiesFrame builds the capabilities frame with the built-in
// requests added.
func (c *Client) capabilitiesFrame(k *conn) (*protocol.Frame, bool) {
	caps, ok := c.opts.Capabilities()
	if !ok {
		return nil, false
	}
	for _, name := range c.requestNames() {
		if !slices.Contains(caps.Requests, name) {
			caps.Requests = append(caps.Requests, name)
		}
	}
	slices.Sort(caps.Requests)
	for kind := range c.opts.Streams {
		if !slices.Contains(caps.Streams, kind) {
			caps.Streams = append(caps.Streams, kind)
		}
	}
	slices.Sort(caps.Streams)
	f, err := protocol.NewFrame(protocol.TypeCapabilities, k.frameID("c"), "", protocol.JobRef{}, caps)
	if err != nil {
		c.log.Error("cannot build the capabilities frame", "error", err)
		return nil, false
	}
	return f, true
}

func (c *Client) requestNames() []string {
	names := []string{protocol.ReqAgentCredentialRotate}
	for n := range c.opts.Requests {
		names = append(names, n)
	}
	return names
}

func (c *Client) handler(name string) RequestHandler {
	if name == protocol.ReqAgentCredentialRotate {
		return c.rotateCredential
	}
	return c.opts.Requests[name]
}

// rotateCredential persists the manager's new credential atomically before
// confirming; the manager revokes the old one only after this answer.
func (c *Client) rotateCredential(_ context.Context, input json.RawMessage) (any, error) {
	var in protocol.CredentialRotateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, &HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed rotation input"}
	}
	if err := c.opts.State.ReplaceCredential(in.Credential); err != nil {
		c.log.Error("could not persist the rotated credential; keeping the old one", "error", err)
		return nil, &HandlerError{Code: protocol.CodeInternal, Message: "could not persist the credential", Retryable: true}
	}
	c.log.Info("agent credential rotated and persisted")
	return protocol.CredentialRotateOutput{Persisted: true}, nil
}

// Relay forwards events or file invalidations on the live session with
// per-session sequence numbers (docs/protocol/agent-v1.md, "Sequence
// numbers and gaps"). Publish never blocks: without a session the item is
// dropped (the manager resynchronizes after every reconnect); when the send
// queue is congested it is dropped but still consumes its number, so the
// manager sees the gap and resynchronizes.
type Relay[T any] struct {
	c       *Client
	typ     protocol.Type
	seq     func(*conn) *atomic.Uint64
	stamp   func(*T, uint64)
	prepare func(*T)
}

// Publish relays one item. It reports whether it was queued.
func (r *Relay[T]) Publish(p T) bool {
	k := r.c.current.Load()
	if k == nil || !k.ready.Load() {
		return false
	}
	if r.prepare != nil {
		r.prepare(&p)
	}
	r.stamp(&p, r.seq(k).Add(1))
	f, err := protocol.NewFrame(r.typ, k.frameID(string(r.typ[:2])), "", protocol.JobRef{}, p)
	if err == nil {
		err = protocol.ValidatePayload(f)
	}
	if err != nil {
		r.c.log.Warn("dropping an invalid relayed frame", "type", r.typ, "error", err)
		return false
	}
	// Keep half of the queue for job and request traffic.
	if len(k.out) >= cap(k.out)/2 {
		return false
	}
	return k.send(f) == nil
}

// Drop consumes a sequence number without sending anything: the producer
// discarded items (rate limit, overflow), so the manager sees a gap and
// resynchronizes. Without a session it does nothing (every reconnect
// resynchronizes anyway).
func (r *Relay[T]) Drop() {
	if k := r.c.current.Load(); k != nil && k.ready.Load() {
		r.seq(k).Add(1)
	}
}

// prepareInvalidation bounds an invalidation: more than MaxPaths paths
// become an overflow of the whole scope.
func prepareInvalidation(p *protocol.FSInvalidationPayload) {
	if len(p.Paths) > protocol.MaxPaths {
		p.Paths, p.Overflow = nil, true
	}
	if len(p.Paths) == 0 {
		p.Overflow = true
	}
}

// newFrameID returns a unique frame ID prefix for this process.
func newFrameID() string { return ids.New() }
