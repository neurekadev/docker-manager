package observe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type recorder struct {
	mu        sync.Mutex
	published []protocol.EventPayload
	dropped   int
}

func (r *recorder) Publish(p protocol.EventPayload) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := p.Validate(); err != nil {
		panic(err)
	}
	r.published = append(r.published, p)
	return true
}

func (r *recorder) Drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped++
}

func ev(typ, action, actor string, at time.Time, attrs map[string]string) engine.Event {
	return engine.Event{Type: typ, Action: action, ActorID: actor, Attributes: attrs, Time: at}
}

func TestMapAllowlistsActionsAndAttributes(t *testing.T) {
	at := testutil.Epoch
	p, ok := Map(ev("container", "health_status: unhealthy", "abc123", at,
		map[string]string{"name": "web", "image": "nginx:1", "PASSWORD": "hunter2", "com.example.secret": "x"}))
	if !ok || p.ResourceID != "web" || p.Action != "health_status" || p.Attributes["health"] != "unhealthy" || p.Attributes["image"] != "nginx:1" ||
		len(p.Attributes) != 3 || p.Source != "engine" {
		t.Fatalf("%+v", p)
	}
	for _, e := range []engine.Event{
		ev("container", "exec_start: sh", "abc", at, nil),
		ev("container", "attach", "abc", at, nil),
		ev("volume", "mount", "data", at, nil),
		ev("plugin", "enable", "p", at, nil),
		ev("container", "start", "abc", time.Time{}, nil),
	} {
		if _, ok := Map(e); ok {
			t.Errorf("%s %s relayed", e.Type, e.Action)
		}
	}
	if p, ok := Map(ev("network", "connect", "n1", at, map[string]string{"name": "frontend", "container": "abc"})); !ok || p.ResourceID != "frontend" ||
		len(p.Attributes) != 1 {
		t.Fatalf("%+v", p)
	}
	if p, ok := Map(ev("volume", "destroy", "data", at, map[string]string{"driver": "local"})); !ok || p.ResourceID != "data" || p.Attributes != nil {
		t.Fatalf("%+v", p)
	}
}

func TestRelayCoalescesRateLimitsAndResumes(t *testing.T) {
	clk := testutil.FakeClock()
	at := testutil.Epoch
	rec := &recorder{}
	eng := &fakeEngine{events: []engine.Event{
		ev("container", "start", "a", at, map[string]string{"name": "web"}),
		// An immediate repeat of the same action is coalesced ...
		ev("container", "start", "a", at.Add(100*time.Millisecond), map[string]string{"name": "web"}),
		// ... but not after a different action.
		ev("container", "die", "a", at.Add(200*time.Millisecond), map[string]string{"name": "web", "exitCode": "0"}),
		ev("container", "start", "a", at.Add(300*time.Millisecond), map[string]string{"name": "web"}),
		// Another resource is independent.
		ev("container", "start", "b", at.Add(300*time.Millisecond), map[string]string{"name": "db"}),
		// Noise is never relayed.
		ev("container", "exec_die", "a", at.Add(400*time.Millisecond), nil),
		// After the window the same action is relayed again.
		ev("container", "start", "b", at.Add(2*time.Second), map[string]string{"name": "db"}),
	}, eventsErr: errors.New("stream broken"), eventsCalled: make(chan struct{}, 2)}
	r := NewEventRelay(EventOptions{Clock: clk, Logger: testutil.Logger(t), Engine: func() EngineAPI { return eng }, Publisher: rec, Rate: 1, Burst: 4})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	// The stream breaks after the events; the relay waits for its retry timer.
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	var got []string
	for _, p := range rec.published {
		got = append(got, p.ResourceID+":"+p.Action)
	}
	dropped := rec.dropped
	rec.mu.Unlock()
	// Burst 4: web start, web die, web start, db start; the second db start
	// is over the rate and becomes a sequence gap (Drop).
	if want := "[web:start web:die web:start db:start]"; fmtList(got) != want || dropped != 1 {
		t.Fatalf("published %v dropped %d, want %s and 1", got, dropped, want)
	}
	// Reconnect: the relay asks for events since the last one it saw, and
	// the replayed last event is not relayed twice.
	eng.mu.Lock()
	eng.eventsErr = nil
	eng.mu.Unlock()
	clk.Advance(time.Minute) // refill the bucket and fire the retry timer
	<-eng.eventsCalled       // the first stream
	<-eng.eventsCalled       // the second stream replayed
	cancel()
	<-done
	eng.mu.Lock()
	since := eng.since
	eng.mu.Unlock()
	if len(since) != 2 || !since[0].IsZero() || !since[1].Equal(at.Add(2*time.Second)) {
		t.Fatalf("since %v", since)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.published) != 4 {
		t.Fatalf("replay relayed again: %d events", len(rec.published))
	}
}

func fmtList(s []string) string {
	out := "["
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out + "]"
}
