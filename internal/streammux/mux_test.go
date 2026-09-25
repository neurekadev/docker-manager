package streammux

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/protocol"
)

// end is one session end: frames it sends are encoded, decoded and
// validated like on the wire and delivered in order to the peer.
type end struct {
	name   string
	mux    *Mux
	peer   *end
	ctrl   chan *protocol.Frame
	data   chan *protocol.Frame
	next   atomic.Uint64
	ctx    context.Context
	accept func(*Stream)
	// dataFrames counts delivered stream_data frames.
	dataFrames atomic.Int64
	// tamper may rewrite frames before delivery (protocol violations).
	tamper func(*protocol.Frame) *protocol.Frame
	wg     sync.WaitGroup
}

func (e *end) FrameID(prefix string) string {
	return e.name + "." + prefix + "." + strconv.FormatUint(e.next.Add(1), 10)
}

func (e *end) SendControl(f *protocol.Frame) error {
	if e.ctx.Err() != nil {
		return ErrSessionClosed
	}
	select {
	case e.ctrl <- f:
		return nil
	default:
		return errors.New("control queue full")
	}
}

func (e *end) SendData(ctx context.Context, f *protocol.Frame) error {
	select {
	case e.data <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ctx.Done():
		return ErrSessionClosed
	}
}

// pump delivers this end's frames to the peer (control first).
func (e *end) pump(t *testing.T) {
	defer e.wg.Done()
	for {
		var f *protocol.Frame
		select {
		case f = <-e.ctrl:
		default:
			select {
			case f = <-e.ctrl:
			case f = <-e.data:
			case <-e.ctx.Done():
				return
			}
		}
		if e.tamper != nil {
			if f = e.tamper(f); f == nil {
				continue
			}
		}
		b, err := protocol.Encode(f)
		if err != nil {
			t.Errorf("%s: encode %s: %v", e.name, f.Type, err)
			continue
		}
		g, err := protocol.Decode(b)
		if err == nil {
			err = protocol.ValidatePayload(g)
		}
		if err != nil {
			t.Errorf("%s: decode %s: %v", e.name, f.Type, err)
			continue
		}
		e.peer.receive(g)
	}
}

func (e *end) receive(f *protocol.Frame) {
	switch f.Type {
	case protocol.TypeStreamOpen:
		s, err := e.mux.Accept(e.ctx, f)
		if errors.Is(err, ErrLimit) {
			e.mux.Refuse(f.ID, protocol.CloseReasonLimit, protocol.CodeStreamLimit, "")
			return
		}
		if err != nil || e.accept == nil {
			e.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeUnsupportedStream, "")
			return
		}
		go e.accept(s)
	case protocol.TypeStreamData:
		e.dataFrames.Add(1)
		e.mux.Handle(f)
	default:
		e.mux.Handle(f)
	}
}

type pair struct {
	manager, agent *end
	cancel         context.CancelFunc
}

func newPair(t *testing.T, accept func(*Stream)) *pair {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mk := func(name string) *end {
		e := &end{name: name, ctrl: make(chan *protocol.Frame, 1024), data: make(chan *protocol.Frame, 8), ctx: ctx}
		e.mux = New(e, 4)
		return e
	}
	p := &pair{manager: mk("m"), agent: mk("a"), cancel: cancel}
	p.manager.peer, p.agent.peer = p.agent, p.manager
	p.agent.accept = accept
	for _, e := range []*end{p.manager, p.agent} {
		e.wg.Add(1)
		go e.pump(t)
	}
	t.Cleanup(func() {
		cancel()
		p.manager.mux.CloseAll()
		p.agent.mux.CloseAll()
		p.manager.wg.Wait()
		p.agent.wg.Wait()
	})
	return p
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDownloadStreamTransfersAndVerifies(t *testing.T) {
	payload := randomBytes(t, 5<<20+123)
	agentStream := make(chan *Stream, 1)
	p := newPair(t, func(s *Stream) {
		agentStream <- s
		if _, err := s.Write(payload); err != nil {
			t.Errorf("agent write: %v", err)
			return
		}
		if err := s.CloseWrite(); err != nil {
			t.Errorf("agent close: %v", err)
		}
	})
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, map[string]string{"path": "a"}, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if s.Err() != nil {
		t.Fatalf("stream error %v", s.Err())
	}
	a := <-agentStream
	<-a.Done()
	if a.Err() != nil || p.manager.mux.Len() != 0 || p.agent.mux.Len() != 0 {
		t.Fatalf("agent stream %v, open streams %d/%d", a.Err(), p.manager.mux.Len(), p.agent.mux.Len())
	}
}

// A receiver that does not read bounds what the sender can push: at most
// the window is in flight, the rest waits for credit.
func TestFlowControlBoundsBufferedData(t *testing.T) {
	var sent atomic.Int64
	windowSent := make(chan *Stream, 1)
	writerDone := make(chan struct{})
	p := newPair(t, func(s *Stream) {
		defer close(writerDone)
		chunk := make([]byte, 64<<10)
		for range 64 { // 4 MiB, four windows
			n, err := s.Write(chunk)
			if sent.Add(int64(n)) == protocol.StreamWindow {
				windowSent <- s
			}
			if err != nil {
				return
			}
		}
		_ = s.CloseWrite()
	})
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Without reading, the agent can send exactly one window: its credit
	// is exhausted and the next Write waits. (A sender ignoring credit is
	// cut off by the receiver's check, which would fail the copy below.)
	a := <-windowSent
	a.mu.Lock()
	credit := a.credit
	a.mu.Unlock()
	if credit != 0 {
		t.Fatalf("credit left after a full window without reads: %d", credit)
	}
	n, err := io.Copy(io.Discard, s)
	if err != nil || n != 4<<20 {
		t.Fatalf("copied %d, %v", n, err)
	}
	<-writerDone
}

func TestUploadStreamWithResult(t *testing.T) {
	payload := randomBytes(t, 3<<20)
	var received []byte
	p := newPair(t, func(s *Stream) {
		b, err := io.ReadAll(s)
		if err != nil {
			s.Abort(protocol.CloseReasonError, protocol.CodeInternal, err.Error())
			return
		}
		received = b
		if err := s.CloseWithResult(map[string]any{"name": "x.bin", "size": len(b)}); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesUpload, nil, OpenOptions{MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	res, err := s.Result(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.Size != len(payload) || out.Name != "x.bin" {
		t.Fatalf("result %s %v", res, err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("payload differs")
	}
	<-s.Done()
}

func TestMaxBytesEnforcedOnBothSides(t *testing.T) {
	gotErr := make(chan error, 1)
	p := newPair(t, func(s *Stream) {
		_, err := io.ReadAll(s)
		gotErr <- err
	})
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesUpload, nil, OpenOptions{MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(make([]byte, 1001)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("sender: %v, want ErrTooLarge", err)
	}
	s.Abort(protocol.CloseReasonLimit, protocol.CodeTooLarge, "")
	var ce *CloseError
	if err := <-gotErr; !errors.As(err, &ce) || ce.Reason != protocol.CloseReasonLimit {
		t.Fatalf("receiver: %v", err)
	}

	// A sender that ignores the limit is cut off by the receiver.
	p2 := newPair(t, func(s *Stream) {
		_, err := io.ReadAll(s)
		gotErr <- err
	})
	p2.manager.tamper = func(f *protocol.Frame) *protocol.Frame {
		if f.Type == protocol.TypeStreamOpen {
			op, _ := protocol.DecodePayload[protocol.StreamOpenPayload](f)
			op.MaxBytes = 10
			g, _ := protocol.NewFrame(f.Type, f.ID, "", protocol.JobRef{}, op)
			return g
		}
		return f
	}
	s2, err := p2.manager.mux.Open(testCtx(t), protocol.StreamFilesUpload, nil, OpenOptions{MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s2.Write(make([]byte, 100))
	if err := <-gotErr; !errors.As(err, &ce) || ce.Code != protocol.CodeTooLarge {
		t.Fatalf("receiver: %v", err)
	}
	<-s2.Done()
	var ce2 *CloseError
	if !errors.As(s2.Err(), &ce2) || ce2.Code != protocol.CodeTooLarge {
		t.Fatalf("sender sees %v", s2.Err())
	}
}

func TestAbortPropagatesAndSessionEndFailsStreams(t *testing.T) {
	started := make(chan *Stream, 1)
	p := newPair(t, func(s *Stream) { started <- s })
	ctx, cancel := context.WithCancel(testCtx(t))
	s, err := p.manager.mux.Open(ctx, protocol.StreamFilesDownload, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agentSide := <-started
	cancel() // the HTTP client went away
	<-agentSide.Done()
	var ce *CloseError
	if !errors.As(agentSide.Err(), &ce) || ce.Reason != protocol.CloseReasonCancelled || ce.Local {
		t.Fatalf("agent side: %v", agentSide.Err())
	}
	if _, err := agentSide.Write([]byte("x")); err == nil {
		t.Fatal("write after abort succeeded")
	}
	<-s.Done()

	s2, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	p.manager.mux.CloseAll()
	if _, err := s2.Read(make([]byte, 1)); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("read after session end: %v", err)
	}
	if _, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("open after session end: %v", err)
	}
}

func TestStreamLimitAndViolations(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	p := newPair(t, func(s *Stream) { <-hold })
	var streams []*Stream
	for range 4 {
		s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, s)
	}
	if _, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("fifth stream: %v", err)
	}
	streams[0].Abort(protocol.CloseReasonCancelled, "", "")
	if _, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{}); err != nil {
		t.Fatalf("after abort: %v", err)
	}

	// A sequence gap aborts the stream, not the session.
	p2 := newPair(t, func(s *Stream) {
		_, _ = s.Write([]byte("one"))
		_, _ = s.Write([]byte("two"))
		_ = s.CloseWrite()
	})
	var n atomic.Int32
	p2.agent.tamper = func(f *protocol.Frame) *protocol.Frame {
		if f.Type == protocol.TypeStreamData && n.Add(1) == 1 {
			return nil // drop the first chunk
		}
		return f
	}
	s, err := p2.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(s)
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != protocol.CodeInvalidFrame {
		t.Fatalf("gap: %v", err)
	}
	if _, err := p2.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{}); err != nil {
		t.Fatalf("the session survives a stream violation: %v", err)
	}
}

func TestVerificationDetectsTampering(t *testing.T) {
	p := newPair(t, func(s *Stream) {
		_, _ = s.Write([]byte("hello"))
		_ = s.CloseWrite()
	})
	p.agent.tamper = func(f *protocol.Frame) *protocol.Frame {
		if f.Type == protocol.TypeStreamData {
			d, _ := protocol.DecodePayload[protocol.StreamDataPayload](f)
			d.Data = []byte("HELLO")
			g, _ := protocol.NewFrame(f.Type, f.ID, f.CorrelationID, protocol.JobRef{}, d)
			return g
		}
		return f
	}
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamFilesDownload, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(s); !errors.Is(err, ErrVerification) {
		t.Fatalf("got %v, want ErrVerification", err)
	}
}

// Bidirectional streams (exec) carry channels and close each direction
// independently.
func TestBidirectionalChannels(t *testing.T) {
	p := newPair(t, func(s *Stream) {
		buf := make([]byte, 16)
		for {
			n, ch, err := s.ReadChannel(buf)
			if err != nil {
				_, _ = s.WriteChannel("stderr", []byte("bye"))
				_ = s.CloseWrite()
				return
			}
			if ch != "stdin" {
				t.Errorf("channel %q", ch)
			}
			_, _ = s.WriteChannel("stdout", bytes.ToUpper(buf[:n]))
		}
	})
	s, err := p.manager.mux.Open(testCtx(t), protocol.StreamContainerExec, nil, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteChannel("stdin", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, ch, err := s.ReadChannel(buf)
	if err != nil || ch != "stdout" || string(buf[:n]) != "ABC" {
		t.Fatalf("%q %q %v", buf[:n], ch, err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	n, ch, err = s.ReadChannel(buf)
	if err != nil || ch != "stderr" || string(buf[:n]) != "bye" {
		t.Fatalf("%q %q %v", buf[:n], ch, err)
	}
	if _, _, err := s.ReadChannel(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	<-s.Done()
}
