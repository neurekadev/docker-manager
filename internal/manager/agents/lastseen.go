package agents

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// LastSeenRefresh is how often a live session persists the last-seen time
// of its agent and environment. Every inbound frame (at least one
// heartbeat per protocol.HeartbeatInterval, 15 s) proves the agent alive,
// but writing each one would cost a database write per agent every 15 s
// for a value shown to the minute: one write per 60 s (every fourth
// heartbeat) keeps "last seen" at most a minute old after a disconnect.
// The connect and disconnect transitions still write it at once.
const LastSeenRefresh = 60 * time.Second

// lastSeenWriteTimeout bounds one last-seen write (it never holds the
// session up; a slow database only delays the next refresh).
const lastSeenWriteTimeout = 5 * time.Second

// touchLastSeen persists that the session's agent was seen at at
// (store.TouchLastSeen). No event is published: being seen is not a change
// views must react to, and the connect/disconnect transitions publish
// their own events.
func (s *Service) touchLastSeen(ctx context.Context, p AgentPrincipal, sessionID string, at time.Time) error {
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := store.TouchLastSeen(ctx, tx, p.AgentID, sessionID, p.EnvironmentID, at)
		return err
	})
}

// lastSeenThrottle decides when a session persists its last-seen time: at
// most once per interval, never two writes at once. The session's
// watchdog calls due on inbound activity; the write runs on its own
// goroutine and reports back with done.
type lastSeenThrottle struct {
	interval time.Duration

	mu       sync.Mutex
	last     time.Time // last persisted (or scheduled) time
	inflight bool
	failing  bool // the last write failed (only the first failure warns)
}

func newLastSeenThrottle(interval time.Duration, start time.Time) *lastSeenThrottle {
	return &lastSeenThrottle{interval: interval, last: start}
}

// due reports whether activity at now should be persisted; when it
// returns true the caller must write and then call done.
func (t *lastSeenThrottle) due(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inflight || now.Sub(t.last) < t.interval {
		return false
	}
	t.inflight, t.last = true, now
	return true
}

// done ends a write started after due. It reports whether err should be
// logged as a warning: only the first failure of a series does (later
// ones are debug), and a success ends the series. A canceled write (the
// session ended) is not a failure.
func (t *lastSeenThrottle) done(err error) (warn bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight = false
	switch {
	case err == nil:
		t.failing = false
		return false
	case errors.Is(err, context.Canceled):
		return false
	}
	warn = !t.failing
	t.failing = true
	return warn
}

// noteSeen persists the session's last-seen time when the throttle says
// it is due. It never blocks the caller: the write runs on its own
// goroutine with a short deadline and stops with the session. It is
// called from the watchdog, which s.bg tracks too, so the s.bg.Add here
// never races serve's s.bg.Wait.
func (s *Session) noteSeen() {
	h := s.hub
	now := h.svc.now()
	if s.lastSeen == nil || !s.lastSeen.due(now) {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, lastSeenWriteTimeout)
		defer cancel()
		err := h.svc.touchLastSeen(ctx, s.p, s.id, now)
		if s.ctx.Err() != nil && err != nil {
			err = context.Canceled // the session ended meanwhile
		}
		switch warn := s.lastSeen.done(err); {
		case warn:
			s.log.Warn("cannot record the agent's last-seen time", "error", err)
		case err != nil && !errors.Is(err, context.Canceled):
			s.log.Debug("cannot record the agent's last-seen time", "error", err)
		}
	}()
}
