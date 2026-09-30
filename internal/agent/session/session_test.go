package session

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestBackoffFullJitter(t *testing.T) {
	b := Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: func() float64 { return 0.999999 }}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		got := b.Delay(i + 1)
		if got > w || got < w-time.Millisecond {
			t.Errorf("attempt %d: %v, want just below %v", i+1, got, w)
		}
	}
	b.Rand = func() float64 { return 0 }
	if b.Delay(5) != 0 {
		t.Fatal("full jitter must allow an immediate retry")
	}
	b.Rand = func() float64 { return 0.5 }
	if b.Delay(3) != 2*time.Second {
		t.Fatalf("half of 4s: %v", b.Delay(3))
	}
	d := DefaultBackoff()
	if d.Min != time.Second || d.Max != time.Minute || d.ResetAfter != time.Minute {
		t.Fatalf("default %+v", d)
	}
}

func TestRefusalClassification(t *testing.T) {
	for status, final := range map[int]bool{401: true, 426: true, 403: true, 429: false, 503: false, 500: false} {
		resp := &http.Response{StatusCode: status, Header: http.Header{}}
		err := refusal(resp, context.DeadlineExceeded)
		_, isStop := err.(*StopError)
		if isStop != final {
			t.Errorf("%d: %v (stop %v)", status, err, isStop)
		}
	}
	if s := (&StopError{HTTPStatus: 401}); !s.Unauthorized() {
		t.Fatal("401 not unauthorized")
	}
	if s := (&StopError{CloseCode: protocol.CloseReplaced}); s.Unauthorized() {
		t.Fatal("4409 is not a revocation")
	}
	for _, c := range []websocket.StatusCode{protocol.CloseRevoked, protocol.CloseUnauthorized} {
		if s := (&StopError{CloseCode: c}); !s.Unauthorized() {
			t.Errorf("%d not unauthorized", c)
		}
	}
	resp := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"9"}}}
	if ra, ok := refusal(resp, nil).(*retryAfter); !ok || ra.d != 9*time.Second {
		t.Fatalf("retry-after %v", refusal(resp, nil))
	}
}

// TestRelaySequenceAndGaps: relayed events are numbered per session;
// a congested queue drops an item but consumes its number, so the manager
// sees a gap; without a session nothing is relayed.
func TestRelaySequenceAndGaps(t *testing.T) {
	c := New(Options{Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	ev := protocol.EventPayload{Source: "engine", Type: "container", Action: "start", At: testutil.Epoch}
	if c.Events().Publish(ev) {
		t.Fatal("relayed without a session")
	}
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	k := &conn{c: c, ctx: ctx, cancel: cancel, out: make(chan outFrame, 2), idBase: "b"}
	k.ready.Store(true)
	c.current.Store(k)

	if !c.Events().Publish(ev) {
		t.Fatal("first event not queued")
	}
	if c.Events().Publish(ev) { // queue at half capacity: dropped, seq 2 consumed
		t.Fatal("event queued past the relay share of the queue")
	}
	first := <-k.out
	if !c.Events().Publish(ev) {
		t.Fatal("third event not queued")
	}
	third := <-k.out
	seqOf := func(o outFrame) uint64 {
		p, err := protocol.DecodePayload[protocol.EventPayload](o.f)
		if err != nil {
			t.Fatal(err)
		}
		return p.Seq
	}
	if seqOf(first) != 1 || seqOf(third) != 3 {
		t.Fatalf("seqs %d %d, want 1 3", seqOf(first), seqOf(third))
	}
	var tr protocol.SeqTracker
	tr.Observe(seqOf(first))
	if res, missed := tr.Observe(seqOf(third)); res != protocol.SeqGap || missed != 1 {
		t.Fatalf("manager view: %v %d", res, missed)
	}
	// Drop (the producer discarded an item, e.g. over its rate) consumes
	// a number without sending: the manager sees another gap.
	c.Events().Drop()
	if !c.Events().Publish(ev) {
		t.Fatal("event after a drop not queued")
	}
	if s := seqOf(<-k.out); s != 5 {
		t.Fatalf("seq after a drop %d, want 5", s)
	}

	// File invalidations have their own counter; too many paths become an
	// overflow of the whole scope.
	paths := make([]string, protocol.MaxPaths+1)
	for i := range paths {
		paths[i] = "f"
	}
	if !c.FileInvalidations().Publish(protocol.FSInvalidationPayload{Scope: protocol.ScopeRef{Kind: "stack", ID: "s"}, Paths: paths, At: testutil.Epoch}) {
		t.Fatal("invalidation not queued")
	}
	p, err := protocol.DecodePayload[protocol.FSInvalidationPayload]((<-k.out).f)
	if err != nil || p.Seq != 1 || !p.Overflow || len(p.Paths) != 0 {
		t.Fatalf("invalidation %+v %v", p, err)
	}
	// Invalid items are never sent.
	if c.FileInvalidations().Publish(protocol.FSInvalidationPayload{Scope: protocol.ScopeRef{Kind: "stack", ID: "s"}, Paths: []string{"../etc"}, At: testutil.Epoch}) {
		t.Fatal("escaping path relayed")
	}
}

// TestEndSessionError: a handler refusing the manager for good answers the
// request with its error frame and closes the session after that frame
// with a code the agent reconnects after (never one that deletes the
// credential or idles the agent); the close is classified as retryable.
func TestEndSessionError(t *testing.T) {
	c := New(Options{Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	k := &conn{c: c, ctx: ctx, cancel: cancel, out: make(chan outFrame, 4), idBase: "b", requestSlots: make(chan struct{}, 1)}
	f := &protocol.Frame{Type: protocol.TypeRequest, ID: "req-1"}
	serve := func(err error) outFrame {
		t.Helper()
		k.run(f, protocol.ReqManagerIdentity, func(context.Context) (any, error) { return nil, err })
		k.wg.Wait()
		select {
		case o := <-k.out:
			return o
		default:
			t.Fatal("no answer queued")
			return outFrame{}
		}
	}
	for code, want := range map[websocket.StatusCode]websocket.StatusCode{
		protocol.CloseManagerSuperseded: protocol.CloseManagerSuperseded,
		protocol.CloseRevoked:           protocol.CloseInternal, // would delete the credential
		protocol.CloseReplaced:          protocol.CloseInternal, // would idle the agent
		0:                               protocol.CloseInternal,
	} {
		o := serve(&EndSessionError{Err: &HandlerError{Code: protocol.CodeConflict, Message: "older manager"}, Code: code, Reason: "superseded"})
		p, err := protocol.DecodePayload[protocol.ErrorPayload](o.f)
		if o.f.Type != protocol.TypeError || o.f.CorrelationID != "req-1" || err != nil || p.Code != protocol.CodeConflict || p.Message != "older manager" {
			t.Fatalf("code %d: answer %+v %+v %v", code, o.f, p, err)
		}
		if o.closeCode != want || o.reason != "superseded" {
			t.Fatalf("code %d: closes with %d %q, want %d", code, o.closeCode, o.reason, want)
		}
	}
	// Without Err the request is answered with Output (a response), then
	// the session closes (manager.redirect).
	o := serve(&EndSessionError{Output: map[string]int{"n": 1}, Code: protocol.CloseGoingAway, Reason: "moving"})
	rp, err := protocol.DecodePayload[protocol.ResponsePayload](o.f)
	if o.f.Type != protocol.TypeResponse || o.f.CorrelationID != "req-1" || err != nil || string(rp.Output) != `{"n":1}` {
		t.Fatalf("answer with output %+v %s %v", o.f, rp.Output, err)
	}
	if o.closeCode != protocol.CloseGoingAway || o.reason != "moving" {
		t.Fatalf("output answer closes with %d %q", o.closeCode, o.reason)
	}
	// An ordinary handler error keeps the session.
	if o := serve(&HandlerError{Code: protocol.CodeNotFound, Message: "x"}); o.closeCode != 0 {
		t.Fatalf("ordinary error closes the session with %d", o.closeCode)
	}

	// The echoed close is not a StopError: Run reconnects with backoff and
	// the control loop keeps the credential.
	res := (&conn{}).ended(websocket.CloseError{Code: protocol.CloseManagerSuperseded, Reason: "superseded"})
	var stop *StopError
	if res == nil || errors.As(res, &stop) || !protocol.ReconnectAllowed(protocol.CloseManagerSuperseded) {
		t.Fatalf("superseded close classified as %v", res)
	}
}
