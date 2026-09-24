package clock

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Fake is a deterministic Clock for tests. Time only moves when the test
// calls Advance or Set. Timers and tickers fire synchronously inside Advance,
// in deadline order; like the standard library, a ticker drops ticks when its
// buffered channel is full.
//
// Typical use:
//
//	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//	go worker(ctx, fc)             // worker calls fc.NewTicker(time.Minute)
//	fc.BlockUntilWaiters(ctx, 1)  // wait until the ticker exists
//	fc.Advance(time.Minute)        // fire it
type Fake struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []*fakeWaiter
	seq     uint64
}

// NewFake returns a Fake clock starting at start.
func NewFake(start time.Time) *Fake {
	f := &Fake{now: start}
	f.cond = sync.NewCond(&f.mu)
	return f
}

type fakeWaiter struct {
	clock  *Fake
	when   time.Time
	period time.Duration // >0 for tickers
	ch     chan time.Time
	active bool
	seq    uint64
}

// Now returns the fake current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since returns the fake time elapsed since t.
func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

// After returns a channel that receives once d has elapsed on the fake clock.
func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

// NewTimer creates a fake timer.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWaiter{clock: f, ch: make(chan time.Time, 1)}
	f.schedule(w, d)
	return fakeTimer{w}
}

// NewTicker creates a fake ticker. It panics for non-positive d, like time.NewTicker.
func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWaiter{clock: f, ch: make(chan time.Time, 1), period: d}
	f.schedule(w, d)
	return fakeTicker{w}
}

// schedule must be called with f.mu held.
func (f *Fake) schedule(w *fakeWaiter, d time.Duration) {
	f.seq++
	w.seq = f.seq
	w.when = f.now.Add(d)
	if !w.active {
		w.active = true
		f.waiters = append(f.waiters, w)
	}
	f.cond.Broadcast()
	if d <= 0 && w.period == 0 {
		f.fireDue()
	}
}

// unschedule must be called with f.mu held. It reports whether w was active.
func (f *Fake) unschedule(w *fakeWaiter) bool {
	if !w.active {
		return false
	}
	w.active = false
	for i, x := range f.waiters {
		if x == w {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			break
		}
	}
	f.cond.Broadcast()
	return true
}

// Advance moves the clock forward by d, firing due timers and tickers in order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanceTo(f.now.Add(d))
}

// Set moves the clock to t (which must not be before Now), firing due waiters.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Before(f.now) {
		panic("clock: Fake.Set cannot move time backwards")
	}
	f.advanceTo(t)
}

func (f *Fake) advanceTo(target time.Time) {
	for {
		next := f.nextDue(target)
		if next == nil {
			break
		}
		if next.when.After(f.now) {
			f.now = next.when
		}
		f.fire(next)
	}
	f.now = target
}

// nextDue returns the earliest waiter due at or before target.
func (f *Fake) nextDue(target time.Time) *fakeWaiter {
	due := make([]*fakeWaiter, 0, len(f.waiters))
	for _, w := range f.waiters {
		if !w.when.After(target) {
			due = append(due, w)
		}
	}
	if len(due) == 0 {
		return nil
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].when.Equal(due[j].when) {
			return due[i].seq < due[j].seq
		}
		return due[i].when.Before(due[j].when)
	})
	return due[0]
}

func (f *Fake) fireDue() {
	for {
		next := f.nextDue(f.now)
		if next == nil {
			return
		}
		f.fire(next)
	}
}

func (f *Fake) fire(w *fakeWaiter) {
	select {
	case w.ch <- f.now:
	default: // drop, like time.Ticker
	}
	if w.period > 0 {
		f.seq++
		w.seq = f.seq
		w.when = w.when.Add(w.period)
		return
	}
	f.unschedule(w)
}

// Waiters returns the number of active timers and tickers.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntilWaiters blocks until at least n timers/tickers are active or ctx
// is done. Use it to synchronize with a goroutine before calling Advance.
func (f *Fake) BlockUntilWaiters(ctx context.Context, n int) error {
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cond.Broadcast()
	})
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.waiters) < n {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.cond.Wait()
	}
	return nil
}

type fakeTimer struct{ w *fakeWaiter }

func (t fakeTimer) C() <-chan time.Time { return t.w.ch }

func (t fakeTimer) Stop() bool {
	f := t.w.clock
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unschedule(t.w)
}

func (t fakeTimer) Reset(d time.Duration) bool {
	f := t.w.clock
	f.mu.Lock()
	defer f.mu.Unlock()
	wasActive := t.w.active
	f.schedule(t.w, d)
	return wasActive
}

type fakeTicker struct{ w *fakeWaiter }

func (t fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t fakeTicker) Stop() {
	f := t.w.clock
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unschedule(t.w)
}

func (t fakeTicker) Reset(d time.Duration) {
	if d <= 0 {
		panic("clock: non-positive interval for Ticker.Reset")
	}
	f := t.w.clock
	f.mu.Lock()
	defer f.mu.Unlock()
	t.w.period = d
	f.schedule(t.w, d)
}
