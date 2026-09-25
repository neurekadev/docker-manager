// Package streammux implements the byte streams of the agent session
// protocol (docs/protocol/agent-v1.md, "Streams") for both ends of a
// session: stream_open, stream_data, stream_credit and stream_close frames
// multiplexed over the one session WebSocket, with per-stream credit-based
// flow control, sequence checks, size limits and transfer verification
// (bytes + SHA-256).
//
// Life cycle of a stream:
//
//   - The opener sends stream_open {kind, direction, input, windowBytes,
//     maxBytes}; the frame ID is the stream ID. windowBytes is the credit
//     the opener grants the peer for sending to it.
//   - The accepting side grants the opener credit with stream_credit when
//     the opener sends data (manager_to_agent, both).
//   - A data sender never has more than its granted credit in flight; the
//     receiver grants more as the application consumes bytes, so a slow
//     consumer slows the producer end to end and nothing is buffered
//     without bound.
//   - A side that finished sending sends stream_close {reason: eof, bytes,
//     sha256} (half close). The stream ends when both sides sent a close,
//     or at once when either side closes with another reason (cancelled,
//     error, timeout, limit): an abort.
//
// Protocol violations inside one stream (sequence gap, data beyond the
// granted credit or maxBytes) abort that stream only, never the session.
package streammux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"

	"github.com/neurekadev/dockyard/internal/protocol"
)

// Sender is the session end the mux writes frames to.
type Sender interface {
	// FrameID returns a new unique frame ID with the prefix.
	FrameID(prefix string) string
	// SendControl queues a small frame without blocking (credit, close,
	// open). An error means the session is gone.
	SendControl(f *protocol.Frame) error
	// SendData queues a stream_data frame (or the eof stream_close that
	// must follow a stream's data) on a FIFO queue, blocking while the
	// session's bounded stream queue is full (until ctx or the session
	// ends).
	SendData(ctx context.Context, f *protocol.Frame) error
}

// Errors returned by stream operations.
var (
	// ErrSessionClosed: the session ended; every stream fails with it.
	ErrSessionClosed = errors.New("streammux: session closed")
	// ErrLimit: too many open streams.
	ErrLimit = errors.New("streammux: too many open streams")
	// ErrClosed: the local side already closed its sending direction.
	ErrClosed = errors.New("streammux: stream closed for writing")
	// ErrPeerClosed: the peer of a one-way stream closed before all data
	// was sent (it needs no more; see Result).
	ErrPeerClosed = errors.New("streammux: the receiver closed the stream")
	// ErrDirection: this side may not send (or receive) on the stream.
	ErrDirection = errors.New("streammux: wrong stream direction")
	// ErrTooLarge: more data than the stream's maxBytes.
	ErrTooLarge = errors.New("streammux: stream exceeds its size limit")
	// ErrVerification: the received bytes or SHA-256 differ from the
	// sender's stream_close.
	ErrVerification = errors.New("streammux: transfer verification failed")
)

// CloseError is a stream ended by the peer (or locally) with a reason other
// than eof.
type CloseError struct {
	Reason  string
	Code    string
	Message string
	// Local is true when this side aborted the stream.
	Local bool
}

func (e *CloseError) Error() string {
	who := "peer"
	if e.Local {
		who = "local"
	}
	s := "streammux: stream " + e.Reason + " (" + who + ")"
	if e.Code != "" {
		s += ": " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Mux multiplexes the streams of one session end.
type Mux struct {
	send Sender
	max  int

	mu      sync.Mutex
	streams map[string]*Stream
	closed  error
}

// New returns a mux allowing at most maxStreams open streams (0:
// protocol.MaxStreams).
func New(send Sender, maxStreams int) *Mux {
	if maxStreams <= 0 {
		maxStreams = protocol.MaxStreams
	}
	return &Mux{send: send, max: maxStreams, streams: map[string]*Stream{}}
}

// OpenOptions tunes Open.
type OpenOptions struct {
	// JobID links the stream to a job (#35).
	JobID string
	// Window is the credit granted to the peer for data sent to this side
	// (0: protocol.StreamWindow).
	Window int64
	// MaxBytes bounds the stream's data in each direction (0: unbounded
	// beyond the kind's own limits).
	MaxBytes int64
}

// Open opens a stream of kind towards the peer. The stream fails (Read and
// Write return an error) when ctx ends.
func (m *Mux) Open(ctx context.Context, kind string, input any, o OpenOptions) (*Stream, error) {
	dir, ok := protocol.StreamDirection(kind)
	if !ok {
		return nil, fmt.Errorf("%w: %q", protocol.ErrUnsupportedStream, kind)
	}
	var raw json.RawMessage
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	window := o.Window
	if window <= 0 {
		window = protocol.StreamWindow
	}
	p := protocol.StreamOpenPayload{Kind: kind, Direction: dir, JobID: o.JobID, Input: raw, WindowBytes: window, MaxBytes: o.MaxBytes}
	f, err := protocol.NewFrame(protocol.TypeStreamOpen, m.send.FrameID("so"), "", protocol.JobRef{}, p)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidatePayload(f); err != nil {
		return nil, err
	}
	// The opener sends data for manager_to_agent (from the manager) and
	// both; it receives for agent_to_manager and both. Which side "opens"
	// is the manager in v1, so the direction names map to opener/peer.
	s := newStream(m, f.ID, p, dir == protocol.DirManagerToAgent || dir == protocol.DirBoth,
		dir == protocol.DirAgentToManager || dir == protocol.DirBoth)
	s.window = window
	s.granted = window // credit granted to the peer in the open frame
	if err := m.add(s); err != nil {
		return nil, err
	}
	if err := m.send.SendControl(f); err != nil {
		m.remove(s)
		return nil, ErrSessionClosed
	}
	s.watch(ctx)
	return s, nil
}

// Accept registers the peer's stream_open frame f (already validated) and
// grants the peer its initial credit when it sends data. The returned
// stream fails when ctx ends. ErrLimit when too many streams are open (the
// caller answers stream_close limit with Refuse).
func (m *Mux) Accept(ctx context.Context, f *protocol.Frame) (*Stream, error) {
	p, err := protocol.DecodePayload[protocol.StreamOpenPayload](f)
	if err != nil {
		return nil, err
	}
	dir := p.Direction
	s := newStream(m, f.ID, p, dir == protocol.DirAgentToManager || dir == protocol.DirBoth,
		dir == protocol.DirManagerToAgent || dir == protocol.DirBoth)
	s.credit = p.WindowBytes
	if s.credit <= 0 {
		s.credit = protocol.StreamWindow
	}
	s.window = protocol.StreamWindow
	if err := m.add(s); err != nil {
		return nil, err
	}
	if s.canRecv {
		s.mu.Lock()
		s.granted = s.window
		s.mu.Unlock()
		if err := s.sendCredit(s.window); err != nil {
			m.remove(s)
			return nil, ErrSessionClosed
		}
	}
	s.watch(ctx)
	return s, nil
}

// Refuse answers a stream_open the side will not serve with stream_close
// {reason, code} (unsupported_stream, stream_limit, ...).
func (m *Mux) Refuse(streamID, reason, code, message string) {
	f, err := protocol.NewFrame(protocol.TypeStreamClose, m.send.FrameID("sc"), streamID, protocol.JobRef{},
		protocol.StreamClosePayload{Reason: reason, Code: code, Message: message})
	if err == nil {
		_ = m.send.SendControl(f)
	}
}

func (m *Mux) add(s *Stream) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed != nil {
		return ErrSessionClosed
	}
	if _, dup := m.streams[s.id]; dup {
		return fmt.Errorf("%w: duplicate stream id", protocol.ErrInvalidFrame)
	}
	if len(m.streams) >= m.max {
		return ErrLimit
	}
	m.streams[s.id] = s
	return nil
}

func (m *Mux) remove(s *Stream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams[s.id] == s {
		delete(m.streams, s.id)
	}
}

func (m *Mux) get(id string) *Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[id]
}

// Len returns the number of open streams.
func (m *Mux) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

// Handle routes a validated stream_data, stream_credit or stream_close
// frame. Frames of unknown (finished) streams are ignored.
func (m *Mux) Handle(f *protocol.Frame) {
	s := m.get(f.CorrelationID)
	if s == nil {
		return
	}
	switch f.Type {
	case protocol.TypeStreamData:
		p, err := protocol.DecodePayload[protocol.StreamDataPayload](f)
		if err == nil {
			s.onData(p)
		}
	case protocol.TypeStreamCredit:
		p, err := protocol.DecodePayload[protocol.StreamCreditPayload](f)
		if err == nil {
			s.onCredit(p.Bytes)
		}
	case protocol.TypeStreamClose:
		p, err := protocol.DecodePayload[protocol.StreamClosePayload](f)
		if err == nil {
			s.onClose(p)
		}
	}
}

// CloseAll fails every open stream (the session ended) and refuses new ones.
func (m *Mux) CloseAll() {
	m.mu.Lock()
	m.closed = ErrSessionClosed
	all := make([]*Stream, 0, len(m.streams))
	for _, s := range m.streams {
		all = append(all, s)
	}
	m.streams = map[string]*Stream{}
	m.mu.Unlock()
	for _, s := range all {
		s.fail(ErrSessionClosed)
	}
}

// Stream is one byte stream. Read and Write may be used from different
// goroutines; each of them from one goroutine at a time.
type Stream struct {
	m       *Mux
	id      string
	open    protocol.StreamOpenPayload
	canSend bool
	canRecv bool

	// ctx ends when the stream ends (pending data sends give up).
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	wake   chan struct{} // closed and replaced on every state change
	done   chan struct{}
	failed error // terminal error (abort or session end)

	// Sending side.
	credit      int64
	sendSeq     uint64
	sent        int64
	sendHash    hash.Hash
	localClosed bool

	// Receiving side.
	window      int64 // credit granted per refill
	granted     int64 // total credit granted to the peer
	received    int64
	recvSeq     uint64
	recvHash    hash.Hash
	buf         [][]byte
	channels    []string
	consumed    int64 // consumed since the last grant
	remoteClose *protocol.StreamClosePayload
}

func newStream(m *Mux, id string, open protocol.StreamOpenPayload, canSend, canRecv bool) *Stream {
	ctx, cancel := context.WithCancel(context.Background())
	return &Stream{m: m, id: id, open: open, canSend: canSend, canRecv: canRecv, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}), done: make(chan struct{}), sendHash: sha256.New(), recvHash: sha256.New()}
}

// ID returns the stream ID (the stream_open frame ID).
func (s *Stream) ID() string { return s.id }

// Kind returns the stream kind.
func (s *Stream) Kind() string { return s.open.Kind }

// Input returns the stream_open input.
func (s *Stream) Input() json.RawMessage { return s.open.Input }

// JobID returns the linked job, if any.
func (s *Stream) JobID() string { return s.open.JobID }

// MaxBytes returns the opener's size bound (0: none).
func (s *Stream) MaxBytes() int64 { return s.open.MaxBytes }

// Done is closed when the stream ended (both sides closed, or aborted).
func (s *Stream) Done() <-chan struct{} { return s.done }

// Err returns the terminal error of an aborted stream (nil otherwise).
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// watch aborts the stream when ctx ends.
func (s *Stream) watch(ctx context.Context) {
	if ctx == nil || ctx.Done() == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			s.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "")
		case <-s.done:
		}
	}()
}

// signal wakes waiters; call with mu held.
func (s *Stream) signal() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// finishLocked ends the stream; call with mu held.
func (s *Stream) finishLocked(err error) {
	select {
	case <-s.done:
		return
	default:
	}
	if err != nil && s.failed == nil {
		s.failed = err
	}
	close(s.done)
	s.cancel()
	s.signal()
}

func (s *Stream) fail(err error) {
	s.mu.Lock()
	s.finishLocked(err)
	s.mu.Unlock()
}

// maybeEnd ends the stream once both sides closed; call with mu held.
func (s *Stream) maybeEndLocked() {
	if s.localClosed && s.remoteClose != nil {
		s.m.remove(s) // lock order: stream, then mux (the mux never locks a stream)
		s.finishLocked(nil)
	}
}

// Abort ends the stream at once for both sides with reason (cancelled,
// error, timeout, limit) and an optional protocol error code. Pending and
// later Read/Write calls fail. Aborting a finished stream does nothing.
func (s *Stream) Abort(reason, code, message string) {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
	}
	if reason == protocol.CloseReasonError && code == "" {
		code = protocol.CodeInternal
	}
	s.localClosed = true
	s.m.remove(s)
	s.finishLocked(&CloseError{Reason: reason, Code: code, Message: message, Local: true})
	s.mu.Unlock()
	_ = s.sendClose(protocol.StreamClosePayload{Reason: reason, Code: code, Message: bound(message)})
}

func bound(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// Write sends p as stream_data frames, waiting for credit. It fails once the
// stream was aborted or closed for writing.
func (s *Stream) Write(p []byte) (int, error) {
	return s.WriteChannel("", p)
}

// WriteChannel is Write with a channel (stdout/stderr/stdin, exec streams).
func (s *Stream) WriteChannel(channel string, p []byte) (int, error) {
	if !s.canSend {
		return 0, ErrDirection
	}
	written := 0
	for len(p) > 0 {
		s.mu.Lock()
		for {
			if s.failed != nil {
				err := s.failed
				s.mu.Unlock()
				return written, err
			}
			if s.localClosed {
				s.mu.Unlock()
				return written, ErrClosed
			}
			if s.remoteClose != nil && !s.canRecv {
				// The receiver finished early (e.g. an upload it skipped):
				// it reads no more; its result is in Result.
				s.mu.Unlock()
				return written, ErrPeerClosed
			}
			if s.credit > 0 {
				break
			}
			w := s.wake
			s.mu.Unlock()
			<-w
			s.mu.Lock()
		}
		n := int64(len(p))
		n = min(n, s.credit, protocol.MaxChunk)
		if limit := s.open.MaxBytes; limit > 0 && s.sent+n > limit {
			s.mu.Unlock()
			return written, ErrTooLarge
		}
		s.credit -= n
		s.sendSeq++
		seq := s.sendSeq
		s.sent += n
		chunk := append([]byte(nil), p[:n]...)
		s.sendHash.Write(chunk)
		s.mu.Unlock()

		f, err := protocol.NewFrame(protocol.TypeStreamData, s.m.send.FrameID("sd"), s.id, protocol.JobRef{},
			protocol.StreamDataPayload{Seq: seq, Data: chunk, Channel: channel})
		if err != nil {
			return written, err
		}
		if err := s.m.send.SendData(s.ctx, f); err != nil {
			if e := s.Err(); e != nil {
				return written, e
			}
			s.fail(ErrSessionClosed)
			return written, ErrSessionClosed
		}
		written += int(n)
		p = p[n:]
	}
	return written, nil
}

// CloseWrite ends this side's sending direction with stream_close {eof,
// bytes, sha256}. Streams this side only receives on are closed too (the
// peer's close then ends the stream).
func (s *Stream) CloseWrite() error {
	return s.closeWith(nil, nil)
}

// CloseWriteExit is CloseWrite carrying a process exit code (exec).
func (s *Stream) CloseWriteExit(code int) error {
	return s.closeWith(nil, &code)
}

// CloseWithResult is CloseWrite carrying a kind-specific result (e.g. the
// entry written by an upload).
func (s *Stream) CloseWithResult(result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.closeWith(b, nil)
}

func (s *Stream) closeWith(result json.RawMessage, exitCode *int) error {
	s.mu.Lock()
	if s.failed != nil {
		err := s.failed
		s.mu.Unlock()
		return err
	}
	if s.localClosed {
		s.mu.Unlock()
		return nil
	}
	s.localClosed = true
	p := protocol.StreamClosePayload{Reason: protocol.CloseReasonEOF, Bytes: s.sent, Result: result, ExitCode: exitCode}
	if s.canSend {
		p.SHA256 = hex.EncodeToString(s.sendHash.Sum(nil))
	}
	s.maybeEndLocked()
	s.signal()
	s.mu.Unlock()
	// The eof close travels behind this side's data frames (same queue),
	// so the peer sees every chunk before it.
	f, err := protocol.NewFrame(protocol.TypeStreamClose, s.m.send.FrameID("sc"), s.id, protocol.JobRef{}, p)
	if err != nil {
		return err
	}
	if err := s.m.send.SendData(context.Background(), f); err != nil {
		s.fail(ErrSessionClosed)
		return ErrSessionClosed
	}
	return nil
}

// sendClose sends an abort close on the control queue (it overtakes queued
// data, which the peer then discards).
func (s *Stream) sendClose(p protocol.StreamClosePayload) error {
	f, err := protocol.NewFrame(protocol.TypeStreamClose, s.m.send.FrameID("sc"), s.id, protocol.JobRef{}, p)
	if err != nil {
		return err
	}
	if err := s.m.send.SendControl(f); err != nil {
		s.fail(ErrSessionClosed)
		return ErrSessionClosed
	}
	return nil
}

func (s *Stream) sendCredit(n int64) error {
	f, err := protocol.NewFrame(protocol.TypeStreamCredit, s.m.send.FrameID("scr"), s.id, protocol.JobRef{},
		protocol.StreamCreditPayload{Bytes: n})
	if err != nil {
		return err
	}
	return s.m.send.SendControl(f)
}

// Read reads received data. It returns io.EOF after the peer's eof close
// once everything was read and verified (ErrVerification on a mismatch).
func (s *Stream) Read(p []byte) (int, error) {
	n, _, err := s.ReadChannel(p)
	return n, err
}

// ReadChannel is Read that also reports the chunk's channel. One call
// never mixes channels.
func (s *Stream) ReadChannel(p []byte) (int, string, error) {
	if !s.canRecv {
		return 0, "", ErrDirection
	}
	if len(p) == 0 {
		return 0, "", nil
	}
	s.mu.Lock()
	for len(s.buf) == 0 {
		if s.failed != nil {
			err := s.failed
			s.mu.Unlock()
			return 0, "", err
		}
		if rc := s.remoteClose; rc != nil {
			err := s.verifyLocked(rc)
			s.mu.Unlock()
			return 0, "", err
		}
		w := s.wake
		s.mu.Unlock()
		<-w
		s.mu.Lock()
	}
	ch := s.channels[0]
	n := copy(p, s.buf[0])
	if n == len(s.buf[0]) {
		s.buf, s.channels = s.buf[1:], s.channels[1:]
	} else {
		s.buf[0] = s.buf[0][n:]
	}
	s.consumed += int64(n)
	var grant int64
	if s.remoteClose == nil && s.failed == nil && s.consumed >= s.window/4 {
		grant = s.consumed
		s.consumed = 0
		s.granted += grant
	}
	s.mu.Unlock()
	if grant > 0 {
		if err := s.sendCredit(grant); err != nil {
			s.fail(ErrSessionClosed)
		}
	}
	return n, ch, nil
}

func (s *Stream) verifyLocked(rc *protocol.StreamClosePayload) error {
	if rc.Bytes != s.received {
		return fmt.Errorf("%w: received %d bytes, sender reported %d", ErrVerification, s.received, rc.Bytes)
	}
	if rc.SHA256 != "" && rc.SHA256 != hex.EncodeToString(s.recvHash.Sum(nil)) {
		return fmt.Errorf("%w: SHA-256 mismatch", ErrVerification)
	}
	return io.EOF
}

// Result waits until the peer closed the stream and returns its close
// payload's result (nil when it sent none). An aborted stream returns its
// error.
func (s *Stream) Result(ctx context.Context) (json.RawMessage, error) {
	s.mu.Lock()
	for s.failed != nil || s.remoteClose == nil {
		if s.failed != nil {
			err := s.failed
			s.mu.Unlock()
			return nil, err
		}
		w := s.wake
		s.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
	}
	res := s.remoteClose.Result
	s.mu.Unlock()
	return res, nil
}

// RemoteClose returns the peer's close payload once received.
func (s *Stream) RemoteClose() *protocol.StreamClosePayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteClose == nil {
		return nil
	}
	c := *s.remoteClose
	return &c
}

// violation aborts the stream after a protocol violation by the peer.
func (s *Stream) violation(code, message string) {
	s.Abort(protocol.CloseReasonError, code, message)
}

func (s *Stream) onData(p protocol.StreamDataPayload) {
	s.mu.Lock()
	if s.failed != nil || s.remoteClose != nil {
		s.mu.Unlock()
		return
	}
	if !s.canRecv {
		s.mu.Unlock()
		s.violation(protocol.CodeInvalidFrame, "data on a stream the sender may not write")
		return
	}
	if p.Seq != s.recvSeq+1 {
		s.mu.Unlock()
		s.violation(protocol.CodeInvalidFrame, "stream_data sequence gap")
		return
	}
	n := int64(len(p.Data))
	if s.received+n > s.granted {
		s.mu.Unlock()
		s.violation(protocol.CodeInvalidFrame, "stream_data beyond the granted credit")
		return
	}
	if limit := s.open.MaxBytes; limit > 0 && s.received+n > limit {
		s.mu.Unlock()
		s.violation(protocol.CodeTooLarge, "stream exceeds its size limit")
		return
	}
	s.recvSeq = p.Seq
	s.received += n
	s.recvHash.Write(p.Data)
	if n > 0 {
		s.buf = append(s.buf, p.Data)
		s.channels = append(s.channels, p.Channel)
	}
	s.signal()
	s.mu.Unlock()
}

func (s *Stream) onCredit(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	s.credit += n
	s.signal()
}

func (s *Stream) onClose(p protocol.StreamClosePayload) {
	s.mu.Lock()
	if s.failed != nil {
		s.mu.Unlock()
		return
	}
	if p.Reason != protocol.CloseReasonEOF {
		// An abort also after the peer's eof (e.g. it gave up waiting for
		// this side's result).
		s.localClosed = true
		s.remoteClose = &p
		s.m.remove(s)
		s.finishLocked(&CloseError{Reason: p.Reason, Code: p.Code, Message: p.Message})
		s.mu.Unlock()
		return
	}
	if s.remoteClose != nil {
		s.mu.Unlock()
		return // duplicate eof
	}
	s.remoteClose = &p
	s.maybeEndLocked()
	s.signal()
	s.mu.Unlock()
}
