package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func TestLastSeenThrottle(t *testing.T) {
	start := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	th := newLastSeenThrottle(LastSeenRefresh, start)
	// Heartbeats within the interval of the connect write are not persisted.
	for _, d := range []time.Duration{0, 15 * time.Second, 30 * time.Second, LastSeenRefresh - time.Microsecond} {
		if th.due(start.Add(d)) {
			t.Fatalf("due %v after the connect write", d)
		}
	}
	at := start.Add(LastSeenRefresh)
	if !th.due(at) {
		t.Fatal("not due after the interval")
	}
	// Never two writes at once, even when the next one would be due.
	if th.due(at.Add(2 * LastSeenRefresh)) {
		t.Fatal("due while a write is in flight")
	}
	if th.done(nil) {
		t.Fatal("success warned")
	}
	// The interval counts from the last write.
	if th.due(at.Add(LastSeenRefresh - time.Second)) {
		t.Fatal("due within the interval of the last write")
	}
	at = at.Add(LastSeenRefresh)
	if !th.due(at) {
		t.Fatal("not due one interval after the last write")
	}

	// Only the first failure of a series warns; a success ends the series.
	boom := errors.New("database is locked")
	if !th.done(boom) {
		t.Fatal("first failure did not warn")
	}
	at = at.Add(LastSeenRefresh)
	if !th.due(at) {
		t.Fatal("a failed write blocks later ones")
	}
	if th.done(boom) {
		t.Fatal("second failure in a row warned")
	}
	at = at.Add(LastSeenRefresh)
	th.due(at)
	if th.done(context.Canceled) {
		t.Fatal("canceled write warned")
	}
	at = at.Add(LastSeenRefresh)
	th.due(at)
	th.done(nil)
	at = at.Add(LastSeenRefresh)
	th.due(at)
	if !th.done(boom) {
		t.Fatal("failure after a success did not warn")
	}
}

// connectedSession enrolls an agent, records sessionID as its session
// (as sessionStarted does) and returns an established-looking Session for
// noteSeen, without a connection.
func (f *fixture) connectedSession(engineID, sessionID string) (*Session, domain.Agent, *testutil.LogBuffer) {
	f.t.Helper()
	a := f.newAgent(engineID, "host-"+engineID)
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	ag, err := store.GetAgent(f.ctx, f.db, r.AgentID)
	if err != nil {
		f.t.Fatal(err)
	}
	now := f.svc.now()
	ag.SessionID, ag.LastSeenAt = sessionID, &now
	if err := store.UpdateAgent(f.ctx, f.db, &ag, 0); err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	f.t.Cleanup(cancel)
	logger, logs := testutil.CaptureLogger()
	s := &Session{hub: f.svc.Hub(), id: sessionID, ctx: ctx, cancel: cancel, log: logger,
		p:        AgentPrincipal{AgentID: r.AgentID, EnvironmentID: r.EnvironmentID, EngineID: engineID},
		lastSeen: newLastSeenThrottle(LastSeenRefresh, now)}
	return s, ag, logs
}

// drainEvents returns the bus events published so far.
func (f *fixture) drainEvents() []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-f.sub.C():
			out = append(out, e)
		default:
			return out
		}
	}
}

func (f *fixture) lastSeen(agentID, envID string) (agent, env *time.Time) {
	f.t.Helper()
	a, err := store.GetAgent(f.ctx, f.db, agentID)
	if err != nil {
		f.t.Fatal(err)
	}
	e, err := store.GetEnvironment(f.ctx, f.db, envID)
	if err != nil {
		f.t.Fatal(err)
	}
	return a.LastSeenAt, e.LastSeenAt
}

// TestLastSeenRefreshedWhileConnected: activity of a live session
// persists the agent's and environment's last-seen time at most once per
// LastSeenRefresh, without touching revisions or publishing events.
func TestLastSeenRefreshedWhileConnected(t *testing.T) {
	f := newFixture(t)
	s, ag, _ := f.connectedSession("ENG-A", "session-1")
	f.drainEvents() // enrollment
	env0, err := store.GetEnvironment(f.ctx, f.db, s.p.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	connected := *ag.LastSeenAt

	// Heartbeats within the interval: nothing written.
	f.clk.Advance(30 * time.Second)
	s.noteSeen()
	s.bg.Wait()
	if a, _ := f.lastSeen(s.p.AgentID, s.p.EnvironmentID); !a.Equal(connected) {
		t.Fatalf("agent last seen %v, want the connect time %v", a, connected)
	}

	f.clk.Advance(LastSeenRefresh - 30*time.Second)
	want := f.svc.now()
	s.noteSeen()
	s.bg.Wait()
	a, e := f.lastSeen(s.p.AgentID, s.p.EnvironmentID)
	if a == nil || !a.Equal(want) || e == nil || !e.Equal(want) {
		t.Fatalf("last seen agent %v environment %v, want %v", a, e, want)
	}
	// Throttled again right after the write.
	f.clk.Advance(15 * time.Second)
	s.noteSeen()
	s.bg.Wait()
	if a, _ := f.lastSeen(s.p.AgentID, s.p.EnvironmentID); !a.Equal(want) {
		t.Fatalf("written again within the interval: %v", a)
	}

	env1, _ := store.GetEnvironment(f.ctx, f.db, s.p.EnvironmentID)
	ag1, _ := store.GetAgent(f.ctx, f.db, s.p.AgentID)
	if env1.Revision != env0.Revision || !env1.UpdatedAt.Equal(env0.UpdatedAt) || ag1.Revision != ag.Revision || !ag1.UpdatedAt.Equal(ag.UpdatedAt) {
		t.Fatal("being seen changed a revision or updated_at")
	}
	for _, ev := range f.drainEvents() {
		if ev.ResourceID == s.p.AgentID || ev.EnvironmentID == s.p.EnvironmentID {
			t.Fatalf("event published for a last-seen refresh: %+v", ev)
		}
	}
}

// TestLastSeenStopsWithTheSession: a session that is no longer the agent's
// (replaced or ended) never writes, and times never move backwards.
func TestLastSeenStopsWithTheSession(t *testing.T) {
	f := newFixture(t)
	s, _, logs := f.connectedSession("ENG-A", "session-1")
	f.clk.Advance(LastSeenRefresh)
	before, beforeEnv := f.lastSeen(s.p.AgentID, s.p.EnvironmentID)

	// Another session took over: the old one's refresh is a no-op.
	ag, _ := store.GetAgent(f.ctx, f.db, s.p.AgentID)
	ag.SessionID = "session-2"
	if err := store.UpdateAgent(f.ctx, f.db, &ag, 0); err != nil {
		t.Fatal(err)
	}
	s.noteSeen()
	s.bg.Wait()
	if a, e := f.lastSeen(s.p.AgentID, s.p.EnvironmentID); !equalTime(a, before) || !equalTime(e, beforeEnv) {
		t.Fatalf("stale session wrote last seen: agent %v environment %v", a, e)
	}

	// An older time never overwrites a newer one (disconnect raced a refresh).
	later := f.svc.now().Add(time.Hour)
	if _, err := store.TouchLastSeen(f.ctx, f.db, s.p.AgentID, "session-2", s.p.EnvironmentID, later); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.TouchLastSeen(f.ctx, f.db, s.p.AgentID, "session-2", s.p.EnvironmentID, f.svc.now()); err != nil || !ok {
		t.Fatalf("touch: %v %v", ok, err)
	}
	if a, e := f.lastSeen(s.p.AgentID, s.p.EnvironmentID); !a.Equal(later) || !e.Equal(later) {
		t.Fatalf("last seen moved backwards: agent %v environment %v", a, e)
	}

	// An ended session writes nothing and logs no failure.
	s.cancel()
	f.clk.Advance(2 * time.Hour) // past later: a write would show
	ag, _ = store.GetAgent(f.ctx, f.db, s.p.AgentID)
	ag.SessionID = s.id
	if err := store.UpdateAgent(f.ctx, f.db, &ag, 0); err != nil {
		t.Fatal(err)
	}
	s.noteSeen()
	s.bg.Wait()
	if a, _ := f.lastSeen(s.p.AgentID, s.p.EnvironmentID); !a.Equal(later) {
		t.Fatalf("ended session wrote last seen: %v", a)
	}
	if strings.Contains(logs.String(), "last-seen") {
		t.Fatalf("last-seen failure logged:\n%s", logs.String())
	}
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
