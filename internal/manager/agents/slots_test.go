package agents

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
)

// frames reads a raw session's frames on their own goroutine (a read
// with a deadline would close the connection).
func (r *rawSession) frames() <-chan *protocol.Frame {
	ch := make(chan *protocol.Frame, 64)
	go func() {
		defer close(ch)
		for {
			f, err := r.read()
			if err != nil {
				return
			}
			ch <- f
		}
	}()
	return ch
}

// next returns the next frame within a second of real time.
func next(t *testing.T, ch <-chan *protocol.Frame) *protocol.Frame {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("the session closed")
		}
		return f
	case <-time.After(time.Second):
		t.Fatal("no frame")
	}
	return nil
}

// TestBusyRequestsAreSentAgain: a request the agent refused as busy (a
// retryable busy: its request limit) is sent again after a backoff and
// succeeds; a busy that is not retryable (another limit) is returned.
func TestBusyRequestsAreSentAgain(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	frames := s.frames()

	type result struct {
		out json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := f.svc.Hub().RequestEnvironment(f.ctx, r.EnvironmentID, protocol.ReqContainerList, nil, 0)
		done <- result{out, err}
	}()
	q := next(t, frames)
	s.send(protocol.TypeError, q.ID, protocol.ErrorPayload{Code: protocol.CodeBusy, Message: "too many concurrent requests", Retryable: true})
	var again *protocol.Frame
	for again == nil { // advance the fake clock until the backoff ends and the request is sent again
		if err := f.clk.BlockUntilWaiters(f.ctx, 3); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(busyBackoff)
		select {
		case again = <-frames:
		case <-time.After(50 * time.Millisecond):
		}
	}
	if again.Type != protocol.TypeRequest || again.ID == q.ID {
		t.Fatalf("re-sent frame %+v (first %s)", again, q.ID)
	}
	if p, _ := protocol.DecodePayload[protocol.RequestPayload](again); p.Name != protocol.ReqContainerList {
		t.Fatalf("re-sent request %+v", p)
	}
	s.send(protocol.TypeResponse, again.ID, protocol.ResponsePayload{Output: json.RawMessage(`[]`)})
	if res := <-done; res.err != nil || string(res.out) != `[]` {
		t.Fatalf("after a busy reply: %s %v", res.out, res.err)
	}

	go func() {
		_, err := f.svc.Hub().RequestEnvironment(f.ctx, r.EnvironmentID, protocol.ReqContainerList, nil, 0)
		done <- result{err: err}
	}()
	q = next(t, frames)
	s.send(protocol.TypeError, q.ID, protocol.ErrorPayload{Code: protocol.CodeBusy, Message: "too many exec sessions on this agent"})
	var re *RequestError
	if res := <-done; !errors.As(res.err, &re) || re.Code != protocol.CodeBusy {
		t.Fatalf("a busy reply that is not retryable: %v", res.err)
	}
}

// TestRequestsWaitForAnAgentSlot: at most protocol.MaxConcurrentRequests
// requests are in flight on a session; the next one is sent once an
// answer frees a slot.
func TestRequestsWaitForAnAgentSlot(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	frames := s.frames()

	n := protocol.MaxConcurrentRequests + 1
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := f.svc.Hub().RequestEnvironment(f.ctx, r.EnvironmentID, protocol.ReqContainerList, nil, 0)
			errs <- err
		}()
	}
	var sent []*protocol.Frame
	for range protocol.MaxConcurrentRequests {
		sent = append(sent, next(t, frames))
	}
	select {
	case q := <-frames:
		t.Fatalf("a request beyond the agent's limit was sent: %+v", q)
	case <-time.After(100 * time.Millisecond):
	}
	s.send(protocol.TypeResponse, sent[0].ID, protocol.ResponsePayload{Output: json.RawMessage(`[]`)})
	sent = append(sent[1:], next(t, frames))
	for _, q := range sent {
		if q.Type != protocol.TypeRequest {
			t.Fatalf("frame %+v", q)
		}
		s.send(protocol.TypeResponse, q.ID, protocol.ResponsePayload{Output: json.RawMessage(`[]`)})
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
