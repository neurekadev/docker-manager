package observe

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
)

// Journal limits (docs/internal/api/streams.md, "Environment and stack events").
const (
	DefaultJournalSize   = 1000
	DefaultJournalAge    = 15 * time.Minute
	DefaultListenerQueue = 256
	busBuffer            = 4096
)

// Reset reasons of the environment event stream.
const (
	ResetServerRestart = "server_restart"
	ResetCursorExpired = "cursor_expired"
	ResetGap           = "gap"
	ResetOverflow      = "overflow"
)

// JournalOptions configures a Journal.
type JournalOptions struct {
	Bus    *events.Bus
	Clock  clock.Clock
	Logger *slog.Logger
	// Size and MaxAge bound each environment's replay log.
	Size   int
	MaxAge time.Duration
	// ListenerQueue bounds each stream's pending entries.
	ListenerQueue int
}

// Entry is one journaled event with its per-environment sequence number.
// Reset entries mark a loss (the manager's bus overflowed): streams send a
// reset and clients refetch.
type Entry struct {
	Seq   uint64
	Event events.Event
	Reset string
}

// Journal keeps a bounded, per-environment log of the bus events the
// environment event stream relays (Docker events, environment status,
// metric and inventory invalidations) so a reconnecting stream can replay
// what it missed.
type Journal struct {
	opts  JournalOptions
	log   *slog.Logger
	epoch string

	mu   sync.Mutex
	envs map[string]*envLog
}

type envLog struct {
	entries   []Entry
	last      uint64
	listeners map[*Listener]struct{}
}

// NewJournal returns a Journal; Run feeds it.
func NewJournal(opts JournalOptions) *Journal {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Size <= 0 {
		opts.Size = DefaultJournalSize
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = DefaultJournalAge
	}
	if opts.ListenerQueue <= 0 {
		opts.ListenerQueue = DefaultListenerQueue
	}
	id := strings.ReplaceAll(ids.New(), "-", "")
	return &Journal{opts: opts, log: opts.Logger, epoch: id[len(id)-8:], envs: map[string]*envLog{}}
}

// journaled selects the bus events of the environment stream.
func journaled(e events.Event) bool {
	switch e.Type {
	case events.DockerEvent, events.MetricsSampled, events.InventoryUpdated,
		events.EnvironmentOnline, events.EnvironmentOffline, events.EnvironmentResync, events.EnvironmentUpdated,
		events.EnvironmentArchived, events.EnvironmentReattached:
		return envOf(e) != ""
	}
	return false
}

func envOf(e events.Event) string {
	if e.EnvironmentID != "" {
		return e.EnvironmentID
	}
	if e.ResourceType == events.ResourceEnvironment {
		return e.ResourceID
	}
	return ""
}

// Run consumes the bus until ctx ends. When the journal's own bus
// subscription overflows, every environment gets a reset entry.
func (j *Journal) Run(ctx context.Context) {
	j.consume(ctx, j.opts.Bus.Subscribe(busBuffer, journaled))
}

func (j *Journal) consume(ctx context.Context, sub *events.Subscription) {
	defer sub.Close()
	var dropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C():
			if d := sub.Dropped(); d != dropped {
				dropped = d
				j.log.Warn("environment event journal fell behind; streams reset", "dropped", d)
				j.resetAll(ResetGap)
			}
			j.Append(e)
		}
	}
}

// Append journals an event (Run calls it; tests may too).
func (j *Journal) Append(e events.Event) {
	env := envOf(e)
	if env == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLocked(env, Entry{Event: e})
}

func (j *Journal) resetAll(reason string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for env := range j.envs {
		j.appendLocked(env, Entry{Reset: reason})
	}
}

func (j *Journal) logOf(env string) *envLog {
	lg := j.envs[env]
	if lg == nil {
		lg = &envLog{listeners: map[*Listener]struct{}{}}
		j.envs[env] = lg
	}
	return lg
}

func (j *Journal) appendLocked(env string, en Entry) {
	lg := j.logOf(env)
	lg.last++
	en.Seq = lg.last
	lg.entries = append(lg.entries, en)
	cutoff := j.opts.Clock.Now().Add(-j.opts.MaxAge)
	drop := max(len(lg.entries)-j.opts.Size, 0)
	for drop < len(lg.entries)-1 && lg.entries[drop].Reset == "" && lg.entries[drop].Event.At.Before(cutoff) {
		drop++
	}
	if drop > 0 {
		lg.entries = append(lg.entries[:0:0], lg.entries[drop:]...)
	}
	for l := range lg.listeners {
		select {
		case l.ch <- en:
		default:
			l.overflow = true
		}
	}
}

// Cursor formats a stream position: the journal epoch (a manager restart
// invalidates cursors) and the sequence number.
func (j *Journal) cursor(seq uint64) string { return j.epoch + "." + strconv.FormatUint(seq, 10) }

// Subscription is the start of an environment stream.
type Subscription struct {
	// Cursor is the position after Replay.
	Cursor string
	// Replay are the retained entries after the requested cursor.
	Replay []Entry
	// Reset is set when the requested cursor cannot be resumed
	// (server_restart, cursor_expired): the client refetches.
	Reset    string
	Listener *Listener
}

// Subscribe registers a listener for an environment and returns what to
// replay after lastEventID ("" for a fresh stream: no replay).
func (j *Journal) Subscribe(env, lastEventID string) Subscription {
	j.mu.Lock()
	defer j.mu.Unlock()
	lg := j.logOf(env)
	l := &Listener{j: j, env: env, ch: make(chan Entry, j.opts.ListenerQueue)}
	lg.listeners[l] = struct{}{}
	sub := Subscription{Cursor: j.cursor(lg.last), Listener: l}
	if lastEventID == "" {
		return sub
	}
	epoch, seqText, ok := strings.Cut(lastEventID, ".")
	seq, err := strconv.ParseUint(seqText, 10, 64)
	first := lg.last + 1
	if len(lg.entries) > 0 {
		first = lg.entries[0].Seq
	}
	switch {
	case !ok || err != nil || epoch != j.epoch:
		sub.Reset = ResetServerRestart
	case seq > lg.last || seq+1 < first:
		sub.Reset = ResetCursorExpired
	default:
		for _, en := range lg.entries {
			if en.Seq > seq {
				sub.Replay = append(sub.Replay, en)
			}
		}
	}
	return sub
}

// Listener receives an environment's new entries.
type Listener struct {
	j        *Journal
	env      string
	ch       chan Entry
	overflow bool
}

// C delivers entries in order.
func (l *Listener) C() <-chan Entry { return l.ch }

// Overflowed reports (and clears) a queue overflow: entries were lost.
// It drains the queue and returns the cursor to continue from, so the
// stream sends a reset and carries on with newer entries.
func (l *Listener) Overflowed() (string, bool) {
	l.j.mu.Lock()
	defer l.j.mu.Unlock()
	if !l.overflow {
		return "", false
	}
	l.overflow = false
drain:
	for {
		select {
		case <-l.ch:
		default:
			break drain
		}
	}
	return l.j.cursor(l.j.logOf(l.env).last), true
}

// Cursor formats an entry's position.
func (l *Listener) Cursor(e Entry) string { return l.j.cursor(e.Seq) }

// Close unregisters the listener.
func (l *Listener) Close() {
	l.j.mu.Lock()
	defer l.j.mu.Unlock()
	if lg := l.j.envs[l.env]; lg != nil {
		delete(lg.listeners, l)
	}
}

// Epoch returns the journal epoch (cursor prefix).
func (j *Journal) Epoch() string { return j.epoch }
