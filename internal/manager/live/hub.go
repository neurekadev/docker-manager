// Package live is the manager side of the live invalidation stream (#23,
// GET /api/v1/live/stream, docs/internal/api/streams.md "Live invalidation stream").
//
// The Hub consumes the in-process event bus (internal/manager/events):
// Docker events relayed by agents (#5), environment and agent status,
// jobs (#26, JobSource), stacks and revisions (#7), file-scope
// invalidations (#15/#23 watcher), metrics and inventory refreshes, and
// successful API mutations (policies, backups, registries, settings,
// permissions: events.ResourceChanged). It
//
//   - coalesces repeated events of one resource (same type, resource and
//     environment) within a window (250 ms; stored and live metrics 1 s
//     per environment): the first is sent at once, the rest are merged
//     (file paths united, beyond protocol.MaxPaths as overflow) and sent
//     once when the window ends, so a resource is announced at most 4
//     times a second;
//   - numbers every record with a strictly increasing sequence and keeps a
//     bounded replay log (the newest 10 000 records or 15 minutes) so a
//     reconnecting stream resumes from its cursor (`<epoch>.<seq>`; a
//     cursor of another manager process or older than the log gets a
//     reset). Live metrics (metrics.live, about one per environment and
//     second) are sent but not retained: a resumed stream refetches the
//     current values with its next one instead of pushing the replay
//     window out;
//   - turns losses into reset records: the Hub's own bus subscription
//     overflowing (reset gap for everyone) and environment resyncs (agent
//     reconnect or event gap: reset gap scoped to that environment);
//   - fans records out to subscribers through bounded queues without ever
//     blocking; a subscriber that falls behind loses its queue and gets a
//     reset overflow with a fresh cursor.
//
// Records carry bus events unfiltered: the stream handler filters every
// record per subscriber with the #17 rules (authz.EventVisible) and shapes
// it, so nothing a user may not see leaves the manager.
package live

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Version is the stream's schema version (the hello event's version).
const Version = "docker-manager.live/v1"

// Defaults (docs/internal/api/streams.md).
const (
	DefaultReplaySize      = 10_000
	DefaultReplayAge       = 15 * time.Minute
	DefaultCoalesce        = 250 * time.Millisecond
	DefaultMetricsCoalesce = time.Second
	DefaultQueue           = 512
	// MaxStreamsPerPrincipal bounds open live streams per user or token:
	// one per open browser tab, so the bound only stops runaway clients
	// (a stream is a queue and a goroutine); people keep many tabs open.
	MaxStreamsPerPrincipal = 32
	busBuffer              = 8192
)

// Reset reasons.
const (
	ResetServerRestart = "server_restart"
	ResetCursorExpired = "cursor_expired"
	ResetGap           = "gap"
	ResetOverflow      = "overflow"
)

// ErrTooManyStreams means the principal already has MaxStreamsPerPrincipal
// live streams open.
var ErrTooManyStreams = errors.New("live: too many live streams for this principal")

// Record is one position of the stream: a (possibly coalesced) bus event,
// or a reset.
type Record struct {
	Seq   uint64
	Event events.Event
	// Reset is the reason of a reset record. With Event set (an
	// environment resync) the reset covers that environment only.
	Reset string
}

// Options configures a Hub.
type Options struct {
	Bus    *events.Bus
	Clock  clock.Clock
	Logger *slog.Logger
	// ReplaySize and ReplayAge bound the replay log.
	ReplaySize int
	ReplayAge  time.Duration
	// Coalesce is the per-resource window; MetricsCoalesce the window of
	// metrics.sampled and metrics.live per environment (at least Coalesce).
	Coalesce        time.Duration
	MetricsCoalesce time.Duration
	// Queue bounds each subscriber's pending records.
	Queue int
	// MaxPerPrincipal bounds open streams per principal.
	MaxPerPrincipal int
}

// slot is the coalescing state of one resource.
type slot struct {
	lastEmit time.Time
	pending  *events.Event
	due      time.Time
}

// Hub coalesces, sequences, retains and fans out live records.
type Hub struct {
	opts  Options
	log   *slog.Logger
	clk   clock.Clock
	epoch string

	mu   sync.Mutex
	recs []Record
	last uint64
	// lost is the newest sequence dropped from the replay log: a cursor
	// below it cannot be resumed.
	lost   uint64
	subs   map[*Subscriber]struct{}
	counts map[string]int

	// Coalescing state (Run's goroutine only): nextDue is the earliest
	// pending window end, nextGC when idle slots are forgotten next.
	slots   map[string]*slot
	nextDue time.Time
	nextGC  time.Time
}

// New returns a Hub; Run feeds it from the bus.
func New(o Options) *Hub {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ReplaySize <= 0 {
		o.ReplaySize = DefaultReplaySize
	}
	if o.ReplayAge <= 0 {
		o.ReplayAge = DefaultReplayAge
	}
	if o.Coalesce <= 0 {
		o.Coalesce = DefaultCoalesce
	}
	if o.MetricsCoalesce <= 0 {
		o.MetricsCoalesce = DefaultMetricsCoalesce
	}
	if o.Queue <= 0 {
		o.Queue = DefaultQueue
	}
	if o.MaxPerPrincipal <= 0 {
		o.MaxPerPrincipal = MaxStreamsPerPrincipal
	}
	id := strings.ReplaceAll(ids.New(), "-", "")
	return &Hub{opts: o, log: o.Logger.With("component", "live"), clk: o.Clock, epoch: id[len(id)-8:],
		subs: map[*Subscriber]struct{}{}, counts: map[string]int{}, slots: map[string]*slot{}}
}

// Epoch is the cursor prefix of this manager process.
func (h *Hub) Epoch() string { return h.epoch }

// Run consumes the bus until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.consume(ctx, h.opts.Bus.Subscribe(busBuffer, nil))
}

func (h *Hub) consume(ctx context.Context, sub *events.Subscription) {
	defer sub.Close()
	timer := h.clk.NewTimer(time.Hour)
	defer timer.Stop()
	var dropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C():
			if d := sub.Dropped(); d != dropped {
				dropped = d
				h.log.Warn("live stream hub fell behind the event bus; streams reset", "dropped", d)
				h.append(Record{Reset: ResetGap})
			}
			h.Offer(e)
		case <-timer.C():
		}
		timer.Reset(h.Flush())
	}
}

// coalesceKey identifies the resource an event invalidates.
func coalesceKey(e events.Event) string {
	return e.Type + "\x00" + e.ResourceType + "\x00" + e.ResourceID + "\x00" + e.EnvironmentID
}

// Offer coalesces one bus event (Run calls it; tests may too): the first
// event of a resource in a window becomes a record at once, later ones are
// merged and flushed when the window ends (Flush).
func (h *Hub) Offer(e events.Event) {
	if e.Type == events.EnvironmentResync {
		h.append(Record{Reset: ResetGap, Event: e})
		return
	}
	now := h.clk.Now()
	window := h.opts.Coalesce
	if isMetrics(e.Type) {
		window = h.opts.MetricsCoalesce
	}
	k := coalesceKey(e)
	s := h.slots[k]
	if s == nil {
		s = &slot{}
		h.slots[k] = s
	}
	if s.pending == nil && !now.Before(s.lastEmit.Add(window)) {
		s.lastEmit = now
		h.append(Record{Event: e})
		return
	}
	if s.pending == nil {
		s.pending = &e
		s.due = s.lastEmit.Add(window)
		if h.nextDue.IsZero() || s.due.Before(h.nextDue) {
			h.nextDue = s.due
		}
		return
	}
	merged := merge(*s.pending, e)
	s.pending = &merged
}

// merge folds a later event of the same resource into an earlier pending
// one: the later state wins, file paths and batch members are united.
func merge(old, e events.Event) events.Event {
	out := e
	switch {
	case old.Overflow || e.Overflow:
		out.Overflow, out.Paths = true, nil
	case len(old.Paths) > 0 || len(e.Paths) > 0:
		paths := append(slices.Clone(old.Paths), e.Paths...)
		slices.Sort(paths)
		out.Paths = slices.Compact(paths)
		if len(out.Paths) > protocol.MaxPaths {
			out.Overflow, out.Paths = true, nil
		}
	}
	if len(old.Members) > 0 {
		members := append(slices.Clone(old.Members), e.Members...)
		slices.Sort(members)
		out.Members = slices.Compact(members)
	}
	if old.Attributes["host"] == "true" && isMetrics(out.Type) {
		attrs := map[string]string{}
		for k, v := range out.Attributes {
			attrs[k] = v
		}
		attrs["host"] = "true"
		out.Attributes = attrs
	}
	return out
}

// Flush appends the pending events whose window ended, forgets idle
// slots and returns the time until the next window ends. It only walks the
// slots when something is due, so a flood of events stays O(1) each.
func (h *Hub) Flush() time.Duration {
	now := h.clk.Now()
	if (h.nextDue.IsZero() || now.Before(h.nextDue)) && now.Before(h.nextGC) {
		return untilEarliest(now, h.nextDue, h.nextGC)
	}
	h.nextDue = time.Time{}
	for k, s := range h.slots {
		switch {
		case s.pending != nil && !now.Before(s.due):
			h.append(Record{Event: *s.pending})
			s.pending, s.lastEmit = nil, now
		case s.pending != nil:
			if h.nextDue.IsZero() || s.due.Before(h.nextDue) {
				h.nextDue = s.due
			}
		case now.Sub(s.lastEmit) > h.idle():
			delete(h.slots, k)
		}
	}
	h.nextGC = now.Add(h.idle())
	return untilEarliest(now, h.nextDue, h.nextGC)
}

func untilEarliest(now, due, gc time.Time) time.Duration {
	next := gc
	if !due.IsZero() && due.Before(next) {
		next = due
	}
	return max(next.Sub(now), time.Millisecond)
}

// isMetrics reports the event types coalesced per environment over
// MetricsCoalesce.
func isMetrics(eventType string) bool {
	return eventType == events.MetricsSampled || eventType == events.MetricsLive
}

// idle is how long a slot without pending events is kept (the longest
// window, so it still throttles the next event).
func (h *Hub) idle() time.Duration { return max(h.opts.Coalesce, h.opts.MetricsCoalesce) }

// append sequences, retains and fans out one record. Live metrics are not
// retained for replay (see the package comment).
func (h *Hub) append(r Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last++
	r.Seq = h.last
	if r.Event.At.IsZero() {
		r.Event.At = h.clk.Now().UTC()
	}
	if r.Event.Type != events.MetricsLive || r.Reset != "" {
		h.recs = append(h.recs, r)
	}
	cutoff := h.clk.Now().Add(-h.opts.ReplayAge)
	drop := max(len(h.recs)-h.opts.ReplaySize, 0)
	for drop < len(h.recs)-1 && h.recs[drop].Event.At.Before(cutoff) {
		drop++
	}
	if drop > 0 {
		h.lost = h.recs[drop-1].Seq
		h.recs = append(h.recs[:0:0], h.recs[drop:]...)
	}
	for s := range h.subs {
		select {
		case s.ch <- r:
		default:
			s.overflow = true
		}
	}
}

func (h *Hub) cursor(seq uint64) string { return h.epoch + "." + strconv.FormatUint(seq, 10) }

// Cursor formats a record's position.
func (h *Hub) Cursor(r Record) string { return h.cursor(r.Seq) }

// Subscription is the start of a live stream.
type Subscription struct {
	// Cursor is the position after Replay (the hello cursor).
	Cursor string
	// Resumed: lastEventID was accepted; Replay holds what was missed.
	Resumed bool
	Replay  []Record
	// Reset is set when lastEventID cannot be resumed (server_restart,
	// cursor_expired): the client refetches everything.
	Reset string
	Sub   *Subscriber
}

// Subscribe registers a subscriber for principal (its key bounds the
// open streams) and returns what to replay after lastEventID ("" for a
// fresh stream).
func (h *Hub) Subscribe(principal, lastEventID string) (Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.counts[principal] >= h.opts.MaxPerPrincipal {
		return Subscription{}, ErrTooManyStreams
	}
	h.counts[principal]++
	s := &Subscriber{h: h, principal: principal, ch: make(chan Record, h.opts.Queue)}
	h.subs[s] = struct{}{}
	sub := Subscription{Cursor: h.cursor(h.last), Sub: s}
	if lastEventID == "" {
		return sub, nil
	}
	epoch, seqText, ok := strings.Cut(lastEventID, ".")
	seq, err := strconv.ParseUint(seqText, 10, 64)
	switch {
	case !ok || err != nil || epoch != h.epoch:
		sub.Reset = ResetServerRestart
	case seq > h.last || seq < h.lost:
		sub.Reset = ResetCursorExpired
	default:
		sub.Resumed = true
		for _, r := range h.recs {
			if r.Seq > seq {
				sub.Replay = append(sub.Replay, r)
			}
		}
	}
	return sub, nil
}

// Subscriber receives new records.
type Subscriber struct {
	h         *Hub
	principal string
	ch        chan Record
	overflow  bool
	closed    bool
}

// C delivers records in order.
func (s *Subscriber) C() <-chan Record { return s.ch }

// Overflowed reports (and clears) a queue overflow: records were lost. It
// drains the queue and returns the cursor to continue from.
func (s *Subscriber) Overflowed() (string, bool) {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if !s.overflow {
		return "", false
	}
	s.overflow = false
	for {
		select {
		case <-s.ch:
		default:
			return s.h.cursor(s.h.last), true
		}
	}
}

// Close unregisters the subscriber.
func (s *Subscriber) Close() {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.h.subs, s)
	if s.h.counts[s.principal]--; s.h.counts[s.principal] <= 0 {
		delete(s.h.counts, s.principal)
	}
}

// Subscribers returns the number of open subscribers (diagnostics, tests).
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
