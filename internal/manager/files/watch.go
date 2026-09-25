package files

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/agents"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// The manager side of the scoped file watcher (#23): Watcher decides what
// each environment's agent watches (files.watch) and turns the agent's
// reports into stack revisions.
//
//   - Watch set: every stack of the environment (so an external edit of
//     compose.yaml, an override or .env becomes a revision, #25 Q1) plus
//     the volumes with an open file view: held by a live stream's volume
//     filter (Hold) or touched by a volume listing (Touch, leased for
//     Lease). It is recomputed when stacks are created, removed or
//     changed and when leases change, pushed when it differs from what the
//     agent has, and pushed again after every (re)connect.
//   - External edits: file invalidations of a stack scope that touch its
//     definition are settled (1 s, so the file manager's own save records
//     its revision first) and reported to the stack service
//     (ExternalChange), which records a revision when the bytes differ.
//   - Missed notifications: after an fs sequence gap (a whole-environment
//     invalidation) every watched stack scope is rescanned (bounded) and
//     definition changes are recorded the same way.

// WatchedStacks is the stack service as the watcher uses it (#7 implements
// it).
type WatchedStacks interface {
	WatchScopes(ctx context.Context, environmentID string) ([]protocol.FileScope, error)
	ExternalChange(ctx context.Context, stackID string, paths []string, overflow bool) (*domain.StackRevision, error)
}

// WatchAgents is the session hub as the watcher uses it (*agents.Hub).
type WatchAgents interface {
	EnvironmentServes(environmentID, name string) bool
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	RescanEnvironment(ctx context.Context, environmentID string, p protocol.RescanPayload, timeout time.Duration) (protocol.RescanResult, error)
}

// Watcher defaults.
const (
	DefaultWatchLease  = 5 * time.Minute
	DefaultWatchSettle = time.Second
	// RescanEntries bounds a rescan after a sequence gap.
	RescanEntries = 200_000
	leaseCheck    = 30 * time.Second
)

// WatcherOptions configures a Watcher.
type WatcherOptions struct {
	Agents WatchAgents
	Stacks WatchedStacks
	Bus    *events.Bus
	Clock  clock.Clock
	Logger *slog.Logger
	// Lease keeps a touched volume watched (default 5 min); Settle
	// batches watch-set pushes and definition recording (default 1 s).
	Lease          time.Duration
	Settle         time.Duration
	RequestTimeout time.Duration
}

type pendingRecord struct {
	paths    map[string]bool
	overflow bool
}

// Watcher keeps agents watching the right scopes. Create it with
// NewWatcher, run it with Run.
type Watcher struct {
	opts WatcherOptions
	log  *slog.Logger
	clk  clock.Clock

	mu     sync.Mutex
	dirty  map[string]time.Time // env → when to recompute
	pushed map[string]string    // env → the watch set the agent has
	holds  map[string]map[string]int
	leases map[string]map[string]time.Time
	record map[string]*pendingRecord // stack ID → changes to settle
	recAt  map[string]time.Time
	envOf  map[string]string // stack ID → environment (from invalidations)
	gaps   map[string]time.Time
	wake   chan struct{}
	work   sync.WaitGroup
}

// NewWatcher returns a Watcher.
func NewWatcher(o WatcherOptions) *Watcher {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Lease <= 0 {
		o.Lease = DefaultWatchLease
	}
	if o.Settle <= 0 {
		o.Settle = DefaultWatchSettle
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	return &Watcher{opts: o, log: o.Logger.With("component", "files.watch"), clk: o.Clock,
		dirty: map[string]time.Time{}, pushed: map[string]string{}, holds: map[string]map[string]int{},
		leases: map[string]map[string]time.Time{}, record: map[string]*pendingRecord{}, recAt: map[string]time.Time{},
		envOf: map[string]string{}, gaps: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

func (w *Watcher) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// markLocked schedules a recomputation of env's watch set.
func (w *Watcher) markLocked(env string) {
	if _, ok := w.dirty[env]; !ok {
		w.dirty[env] = w.clk.Now().Add(w.opts.Settle)
	}
}

// Hold keeps a volume watched until release is called (a live stream
// with a volume filter; the caller authorized volume.files.read).
func (w *Watcher) Hold(env, volume string) (release func()) {
	if !protocol.ValidVolumeName(volume) || env == "" {
		return func() {}
	}
	w.mu.Lock()
	if w.holds[env] == nil {
		w.holds[env] = map[string]int{}
	}
	w.holds[env][volume]++
	if w.holds[env][volume] == 1 {
		w.markLocked(env)
	}
	w.mu.Unlock()
	w.poke()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			if w.holds[env][volume]--; w.holds[env][volume] <= 0 {
				delete(w.holds[env], volume)
				// A lease keeps it briefly (a reconnecting stream).
				w.leaseLocked(env, volume)
			}
			w.mu.Unlock()
		})
	}
}

// Touch keeps a volume watched for the lease (a volume file listing).
func (w *Watcher) Touch(env, volume string) {
	if !protocol.ValidVolumeName(volume) || env == "" {
		return
	}
	w.mu.Lock()
	added := w.leaseLocked(env, volume)
	w.mu.Unlock()
	if added {
		w.poke()
	}
}

func (w *Watcher) leaseLocked(env, volume string) bool {
	if w.leases[env] == nil {
		w.leases[env] = map[string]time.Time{}
	}
	_, had := w.leases[env][volume]
	w.leases[env][volume] = w.clk.Now().Add(w.opts.Lease)
	if !had && w.holds[env][volume] == 0 {
		w.markLocked(env)
		return true
	}
	return false
}

// relevant selects the bus events the watcher reacts to.
func relevant(e events.Event) bool {
	switch e.Type {
	case events.EnvironmentOnline, events.EnvironmentOffline, events.StackCreated, events.StackRemoved, events.StackUpdated,
		events.FilesInvalidated, events.AgentCapabilitiesUpdate, events.EnvironmentResync:
		return true
	}
	return false
}

// Run follows the bus until ctx ends; background work finishes before it
// returns.
func (w *Watcher) Run(ctx context.Context) {
	sub := w.opts.Bus.Subscribe(1024, relevant)
	defer sub.Close()
	defer w.work.Wait()
	timer := w.clk.NewTimer(leaseCheck)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C():
			w.Handle(e)
		case <-w.wake:
		case <-timer.C():
		}
		w.Tick(ctx)
		timer.Reset(w.untilNext())
	}
}

// Handle records one bus event (Run calls it; tests may too).
func (w *Watcher) Handle(e events.Event) {
	now := w.clk.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	env := e.EnvironmentID
	switch e.Type {
	case events.EnvironmentOnline:
		// A (re)connected agent may have restarted: push the set again.
		delete(w.pushed, env)
		w.markLocked(env)
	case events.EnvironmentResync:
		if e.Attributes["reason"] == "reconnect" { // also when it never looked offline
			delete(w.pushed, env)
			w.markLocked(env)
		}
	case events.EnvironmentOffline:
		delete(w.pushed, env)
	case events.StackCreated, events.StackRemoved, events.StackUpdated, events.AgentCapabilitiesUpdate:
		// A capabilities change may bring (or lose) the verified stacks root.
		w.markLocked(env)
	case events.FilesInvalidated:
		if e.ResourceID == "*" {
			if _, ok := w.gaps[env]; !ok {
				w.gaps[env] = now.Add(w.opts.Settle)
			}
			return
		}
		if e.Attributes["scopeKind"] != protocol.ScopeStack {
			return
		}
		id := e.Attributes["scopeId"]
		p := w.record[id]
		if p == nil {
			p = &pendingRecord{paths: map[string]bool{}}
			w.record[id] = p
			w.recAt[id] = now.Add(w.opts.Settle)
		}
		w.envOf[id] = env
		if e.Overflow {
			p.overflow = true
		}
		for _, rel := range e.Paths {
			if len(p.paths) < protocol.MaxPaths {
				p.paths[rel] = true
			} else {
				p.overflow = true
			}
		}
	}
}

// untilNext is the time until the next due work (at most leaseCheck).
func (w *Watcher) untilNext() time.Duration {
	now := w.clk.Now()
	next := now.Add(leaseCheck)
	w.mu.Lock()
	for _, m := range []map[string]time.Time{w.dirty, w.recAt, w.gaps} {
		for _, t := range m {
			if t.Before(next) {
				next = t
			}
		}
	}
	w.mu.Unlock()
	return max(next.Sub(now), time.Millisecond)
}

// Tick runs what is due: lease expiry, watch-set pushes, definition
// recording and gap rescans (in the background; Wait waits for them).
func (w *Watcher) Tick(ctx context.Context) {
	now := w.clk.Now()
	w.mu.Lock()
	for env, vols := range w.leases {
		for v, until := range vols {
			if !now.Before(until) {
				delete(vols, v)
				if w.holds[env][v] == 0 {
					w.markLocked(env)
				}
			}
		}
	}
	var envs []string
	for env, at := range w.dirty {
		if !now.Before(at) {
			envs = append(envs, env)
			delete(w.dirty, env)
		}
	}
	type rec struct {
		id, env  string
		paths    []string
		overflow bool
	}
	var recs []rec
	for id, at := range w.recAt {
		if now.Before(at) {
			continue
		}
		p := w.record[id]
		r := rec{id: id, env: w.envOf[id], overflow: p.overflow}
		for rel := range p.paths {
			r.paths = append(r.paths, rel)
		}
		slices.Sort(r.paths)
		recs = append(recs, r)
		delete(w.record, id)
		delete(w.recAt, id)
		delete(w.envOf, id)
	}
	var gaps []string
	for env, at := range w.gaps {
		if !now.Before(at) {
			gaps = append(gaps, env)
			delete(w.gaps, env)
		}
	}
	w.mu.Unlock()
	if len(envs)+len(recs)+len(gaps) == 0 {
		return
	}
	w.work.Add(1)
	go func() {
		defer w.work.Done()
		for _, env := range envs {
			w.push(ctx, env)
		}
		for _, r := range recs {
			w.external(ctx, r.id, r.paths, r.overflow)
		}
		for _, env := range gaps {
			w.rescan(ctx, env)
		}
	}()
}

// Wait waits for the background work Tick started.
func (w *Watcher) Wait() { w.work.Wait() }

// scopes computes env's watch set.
func (w *Watcher) scopes(ctx context.Context, env string) ([]protocol.FileScope, error) {
	var out []protocol.FileScope
	if w.opts.Stacks != nil {
		st, err := w.opts.Stacks.WatchScopes(ctx, env)
		if err != nil {
			return nil, err
		}
		out = append(out, st...)
	}
	now := w.clk.Now()
	w.mu.Lock()
	vols := map[string]bool{}
	for v := range w.holds[env] {
		vols[v] = true
	}
	for v, until := range w.leases[env] {
		if now.Before(until) {
			vols[v] = true
		}
	}
	w.mu.Unlock()
	for v := range vols {
		out = append(out, protocol.FileScope{Kind: protocol.ScopeVolume, ID: v})
	}
	slices.SortFunc(out, func(a, b protocol.FileScope) int {
		return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID)
	})
	if len(out) > protocol.MaxWatchScopes {
		w.log.Warn("too many file scopes to watch; the rest are refreshed only when listed", "environment_id", env, "scopes", len(out))
		out = out[:protocol.MaxWatchScopes]
	}
	return out, nil
}

func setKey(scopes []protocol.FileScope) string {
	var b strings.Builder
	for _, s := range scopes {
		b.WriteString(s.Kind + ":" + s.ID + ":" + s.Dir + "\n")
	}
	return b.String()
}

// push sends env's watch set when the agent's differs.
func (w *Watcher) push(ctx context.Context, env string) {
	scopes, err := w.scopes(ctx, env)
	if err != nil {
		w.log.Warn("could not compute the file watch set", "environment_id", env, "error", err)
		return
	}
	key := setKey(scopes)
	w.mu.Lock()
	same := w.pushed[env] == key
	_, known := w.pushed[env]
	w.mu.Unlock()
	if known && same {
		return
	}
	if scopes == nil {
		scopes = []protocol.FileScope{}
	}
	if !w.opts.Agents.EnvironmentServes(env, protocol.ReqFilesWatch) {
		// Offline (pushed again when it comes online), or an agent without
		// a watcher: an older agent would close the session on an unknown
		// request.
		w.mu.Lock()
		w.pushed[env] = key
		w.mu.Unlock()
		return
	}
	raw, err := w.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqFilesWatch, protocol.FilesWatchInput{Scopes: scopes}, w.opts.RequestTimeout)
	var re *agents.RequestError
	switch {
	case errors.Is(err, jobs.ErrAgentOffline):
		return // pushed again when it comes online
	case errors.As(err, &re) && re.Code == protocol.CodeUnsupportedRequest:
		w.mu.Lock()
		w.pushed[env] = key // an agent without a watcher: do not retry
		w.mu.Unlock()
		return
	case err != nil:
		w.log.Warn("could not send the file watch set to the agent", "environment_id", env, "error", err)
		w.mu.Lock()
		w.markLocked(env)
		w.mu.Unlock()
		return
	}
	var out protocol.FilesWatchOutput
	if json.Unmarshal(raw, &out) == nil {
		polled := 0
		for _, s := range out.Scopes {
			if s.Mode != protocol.WatchInotify {
				polled++
			}
		}
		w.log.Info("file watch set updated", "environment_id", env, "scopes", len(scopes), "polled_or_unavailable", polled,
			"watches_used", out.WatchesUsed, "watch_limit", out.WatchLimit)
	}
	w.mu.Lock()
	w.pushed[env] = key
	w.mu.Unlock()
}

// external reports settled changes of a stack scope to the stack service.
func (w *Watcher) external(ctx context.Context, stackID string, paths []string, overflow bool) {
	if w.opts.Stacks == nil {
		return
	}
	rev, err := w.opts.Stacks.ExternalChange(ctx, stackID, paths, overflow)
	switch {
	case errors.Is(err, domain.ErrStackNotFound), errors.Is(err, jobs.ErrAgentOffline):
	case err != nil:
		w.log.Warn("could not record an external change of a stack's definition", "stack_id", stackID, "error", err)
	case rev != nil:
		w.log.Info("recorded an external edit of a stack's definition", "stack_id", stackID, "revision", rev.Seq)
	}
}

// rescan reconciles every watched stack scope of env after a sequence gap.
func (w *Watcher) rescan(ctx context.Context, env string) {
	if w.opts.Stacks == nil {
		return
	}
	scopes, err := w.opts.Stacks.WatchScopes(ctx, env)
	if err != nil {
		return
	}
	watching := w.opts.Agents.EnvironmentServes(env, protocol.ReqFilesWatch)
	for _, sc := range scopes {
		if !watching {
			w.external(ctx, sc.ID, nil, true) // no watcher to ask: read the definition
			continue
		}
		res, err := w.opts.Agents.RescanEnvironment(ctx, env, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: sc.Kind, ID: sc.ID},
			Path: ".", MaxEntries: RescanEntries, Reason: "sequence_gap"}, w.opts.RequestTimeout)
		var re *agents.RequestError
		switch {
		case errors.Is(err, jobs.ErrAgentOffline):
			return
		case errors.As(err, &re) && re.Code == protocol.CodeUnsupportedRequest:
			w.external(ctx, sc.ID, nil, true) // no watcher: read the definition
		case err != nil:
			w.log.Warn("file scope rescan failed", "stack_id", sc.ID, "error", err)
			w.external(ctx, sc.ID, nil, true)
		default:
			w.external(ctx, sc.ID, res.Changed, res.Truncated)
		}
	}
}
