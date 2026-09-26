// Package jobstest provides an in-memory jobs.AgentDispatcher for tests of
// the job engine and of features that enqueue jobs.
package jobstest

import (
	"context"
	"sync"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Dispatcher queues frames per environment instead of sending them. Every
// frame is encoded and decoded on the way (like the real transport), so
// tests see exactly what an agent would receive.
type Dispatcher struct {
	mu     sync.Mutex
	online map[string]bool
	// attached environments accept frames even while not yet online (the
	// window in which the transport reconciles a reconnect).
	attached map[string]bool
	queue    map[string][]*protocol.Frame
	onSend   func(env string, f *protocol.Frame)
}

var _ jobs.AgentDispatcher = (*Dispatcher)(nil)

// New returns a dispatcher with every environment offline.
func New() *Dispatcher {
	return &Dispatcher{online: map[string]bool{}, attached: map[string]bool{}, queue: map[string][]*protocol.Frame{}}
}

// Connect attaches env and marks it online.
func (d *Dispatcher) Connect(env string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached[env], d.online[env] = true, true
}

// Attach accepts frames for env without reporting it online yet.
func (d *Dispatcher) Attach(env string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached[env] = true
}

// Disconnect marks env offline and drops its undelivered frames (they were
// in flight on the lost connection). It returns the dropped frames so tests
// can replay them later.
func (d *Dispatcher) Disconnect(env string) []*protocol.Frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached[env], d.online[env] = false, false
	dropped := d.queue[env]
	d.queue[env] = nil
	return dropped
}

// OnSend installs a hook called (outside the dispatcher lock) for every
// accepted frame, after it was queued.
func (d *Dispatcher) OnSend(fn func(env string, f *protocol.Frame)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onSend = fn
}

// Online implements jobs.AgentDispatcher.
func (d *Dispatcher) Online(env string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.online[env]
}

// Send implements jobs.AgentDispatcher.
func (d *Dispatcher) Send(_ context.Context, env string, f *protocol.Frame) error {
	b, err := protocol.Encode(f)
	if err != nil {
		return err
	}
	g, err := protocol.Decode(b)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if !d.attached[env] {
		d.mu.Unlock()
		return jobs.ErrAgentOffline
	}
	d.queue[env] = append(d.queue[env], g)
	hook := d.onSend
	d.mu.Unlock()
	if hook != nil {
		hook(env, g)
	}
	return nil
}

// Drain returns and removes the queued frames for env, in send order.
func (d *Dispatcher) Drain(env string) []*protocol.Frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.queue[env]
	d.queue[env] = nil
	return out
}

// Pending returns the number of queued frames for env.
func (d *Dispatcher) Pending(env string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue[env])
}
