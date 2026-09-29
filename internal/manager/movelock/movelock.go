// Package movelock is the lock a manager takes while it moves to a new
// server (docs/internal/architecture/manager-move.md): one shared level,
// consulted by the API (read-only), the job engine (no new jobs, no
// dispatch), the scheduler (fires nothing) and the agent handler (agents
// refused). The move service (internal/manager/managermove) sets it from
// the state of the current move, also at start, so a restarted old
// manager stays locked.
//
// Every method is safe on a nil *Lock (never locked), so components built
// without one (focused tests) behave normally.
package movelock

import (
	"sync"
	"sync/atomic"
)

// Level is how far the lock reaches.
type Level int32

// Levels, in increasing strength.
const (
	// Open: nothing is locked (no move, or an open, cancelled or expired one).
	Open Level = iota
	// ReadOnly: a new manager asked for the handoff (draining): the API
	// refuses changes, no job starts, the scheduler fires nothing; agents
	// stay connected so running jobs can finish.
	ReadOnly
	// AgentsRefused: the state was copied out (handed off, confirmed):
	// read-only, and agents are refused and disconnected.
	AgentsRefused
)

// String names the level (logs).
func (l Level) String() string {
	switch l {
	case ReadOnly:
		return "read_only"
	case AgentsRefused:
		return "agents_refused"
	}
	return "open"
}

// Lock is the shared move lock. The zero value is open.
type Lock struct {
	level atomic.Int32

	mu        sync.Mutex
	listeners []func(Level)
}

// New returns an open lock.
func New() *Lock { return &Lock{} }

// Level returns the current level.
func (l *Lock) Level() Level {
	if l == nil {
		return Open
	}
	return Level(l.level.Load())
}

// ReadOnly reports whether changes are refused (ReadOnly or stronger).
func (l *Lock) ReadOnly() bool { return l.Level() >= ReadOnly }

// AgentsRefused reports whether agents are refused.
func (l *Lock) AgentsRefused() bool { return l.Level() >= AgentsRefused }

// Set changes the level and, when it changed, calls every listener with
// the new level (in registration order, on the caller's goroutine).
func (l *Lock) Set(v Level) {
	if l == nil {
		return
	}
	l.mu.Lock()
	old := Level(l.level.Swap(int32(v)))
	listeners := append([]func(Level){}, l.listeners...)
	l.mu.Unlock()
	if old == v {
		return
	}
	for _, fn := range listeners {
		fn(v)
	}
}

// OnChange registers fn, called after every level change. Listeners must
// not block (the agent hub closes its sessions asynchronously).
func (l *Lock) OnChange(fn func(Level)) {
	if l == nil || fn == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listeners = append(l.listeners, fn)
}
