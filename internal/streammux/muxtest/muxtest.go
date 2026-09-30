// Package muxtest connects two stream multiplexers in memory, for tests of
// stream handlers (agent side) and stream consumers (manager side) without
// a WebSocket session. Frames are encoded, decoded and validated like on
// the wire and delivered in order. Import it from tests only.
package muxtest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
)

// Handler serves an accepted stream; a returned error aborts it with the
// error's code (Coder) or internal.
type Handler func(ctx context.Context, s *streammux.Stream) error

// Coder is implemented by errors carrying a protocol error code.
type Coder interface{ ProtocolCode() string }

type end struct {
	name   string
	mux    *streammux.Mux
	peer   *end
	ctrl   chan *protocol.Frame
	data   chan *protocol.Frame
	next   atomic.Uint64
	ctx    context.Context
	accept func(*protocol.Frame)
}

func (e *end) FrameID(prefix string) string {
	return e.name + "." + prefix + "." + strconv.FormatUint(e.next.Add(1), 10)
}

func (e *end) SendControl(f *protocol.Frame) error {
	select {
	case <-e.ctx.Done():
		return streammux.ErrSessionClosed
	case e.ctrl <- f:
		return nil
	}
}

func (e *end) SendData(ctx context.Context, f *protocol.Frame) error {
	select {
	case e.data <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ctx.Done():
		return streammux.ErrSessionClosed
	}
}

func (e *end) pump(t testing.TB, wg *sync.WaitGroup) {
	defer wg.Done()
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
		b, err := protocol.Encode(f)
		var g *protocol.Frame
		if err == nil {
			g, err = protocol.Decode(b)
		}
		if err == nil {
			err = protocol.ValidatePayload(g)
		}
		if err != nil {
			t.Errorf("muxtest: invalid %s frame: %v", f.Type, err)
			continue
		}
		if g.Type == protocol.TypeStreamOpen {
			e.peer.accept(g)
			continue
		}
		e.peer.mux.Handle(g)
	}
}

// Pipe is a connected pair: Open opens streams on the "manager" end whose
// handlers run on the "agent" end.
type Pipe struct {
	manager *end
	cancel  context.CancelFunc
}

// New connects a manager end to an agent end serving handlers by kind.
// Unknown kinds are refused with unsupported_stream. Everything stops when
// the test ends.
func New(t testing.TB, handlers map[string]Handler) *Pipe {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	mk := func(name string) *end {
		e := &end{name: name, ctrl: make(chan *protocol.Frame, 4096), data: make(chan *protocol.Frame, 16), ctx: ctx}
		e.mux = streammux.New(e, protocol.MaxStreams)
		return e
	}
	m, a := mk("m"), mk("a")
	m.peer, a.peer = a, m
	m.accept = func(f *protocol.Frame) {
		m.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeUnsupportedStream, "")
	}
	var handlerWG sync.WaitGroup
	a.accept = func(f *protocol.Frame) {
		p, _ := protocol.DecodePayload[protocol.StreamOpenPayload](f)
		h := handlers[p.Kind]
		if h == nil {
			a.mux.Refuse(f.ID, protocol.CloseReasonError, protocol.CodeUnsupportedStream, "")
			return
		}
		s, err := a.mux.Accept(ctx, f)
		if err != nil {
			a.mux.Refuse(f.ID, protocol.CloseReasonLimit, protocol.CodeStreamLimit, "")
			return
		}
		handlerWG.Add(1)
		go func() {
			defer handlerWG.Done()
			hctx, hcancel := context.WithCancel(ctx)
			defer hcancel()
			go func() {
				select {
				case <-s.Done():
					hcancel()
				case <-hctx.Done():
				}
			}()
			if err := h(hctx, s); err != nil {
				code := protocol.CodeInternal
				var c Coder
				if errors.As(err, &c) {
					code = c.ProtocolCode()
				}
				s.Abort(protocol.CloseReasonError, code, err.Error())
				return
			}
			_ = s.CloseWrite()
		}()
	}
	var pumps sync.WaitGroup
	pumps.Add(2)
	go m.pump(t, &pumps)
	go a.pump(t, &pumps)
	t.Cleanup(func() {
		cancel()
		m.mux.CloseAll()
		a.mux.CloseAll()
		pumps.Wait()
		handlerWG.Wait()
	})
	return &Pipe{manager: m, cancel: cancel}
}

// Open opens a stream from the manager end.
func (p *Pipe) Open(ctx context.Context, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	return p.manager.mux.Open(ctx, kind, input, o)
}

// Close ends the pipe like a session ending: every stream fails.
func (p *Pipe) Close() {
	p.cancel()
	p.manager.mux.CloseAll()
}
