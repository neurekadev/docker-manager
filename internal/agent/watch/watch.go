// Package watch is the agent's scoped filesystem watcher (#23): it watches
// exactly the file scopes the manager declared (files.watch: stack project
// directories and local named-volume roots, resolved and verified by the
// scoped file service, #15/#28) and reports changed root-relative paths as
// fs_invalidation frames. It never watches or reports anything outside a
// scope root and never follows a symlink out of one.
//
//   - inotify mode: one kernel watch per directory (fsnotify), registered
//     while walking the tree (a directory's watch is added before its
//     entries are read). New directories are added as they appear;
//     removed or renamed directories drop their watches (the new name
//     arrives as a create). Changes are debounced (200 ms) and coalesced
//     per path; more than protocol.MaxPaths paths become overflow.
//   - Watch-limit accounting: every scope's watches count against one
//     budget (MaxWatches, default half the kernel's
//     fs.inotify.max_user_watches). A scope that does not fit is polled.
//   - Poll mode (watch limit reached, remote filesystem such as NFS/CIFS,
//     kernel notifications unavailable): bounded reconciliation scans at
//     PollInterval (30 s, well within #25 Q5's 60 s) compare a per-directory
//     hash of names, sizes, modification times and modes and report the
//     directories whose content changed.
//   - inotify scopes are reconciled too, at SafetyInterval (10 min) and at
//     once after a kernel queue overflow, so missed notifications heal.
//
// File contents are never read, logged or sent; names are only sent to the
// manager, which filters them per user (docs/internal/api/streams.md).
package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Defaults (docs/internal/support-matrix.md, "File watching").
const (
	DefaultDebounce       = 200 * time.Millisecond
	DefaultPollInterval   = 30 * time.Second
	DefaultSafetyInterval = 10 * time.Minute
	// DefaultMaxScanEntries bounds one reconciliation scan of a scope.
	DefaultMaxScanEntries = 200_000
	// MaxRescanEntries bounds a manager rescan request.
	MaxRescanEntries = 200_000
)

// Resolver resolves a scope to its verified, symlink-free root directory
// (absolute OS path) with the file service's checks
// (files.Service.ScopeDir). Errors are *session.HandlerError.
type Resolver func(ctx context.Context, scope protocol.FileScope) (string, error)

// Options configures a Watcher.
type Options struct {
	Resolve Resolver
	// Invalidate publishes an fs_invalidation (the session relay). It must
	// not block.
	Invalidate func(protocol.FSInvalidationPayload)
	Clock      clock.Clock
	Logger     *slog.Logger
	// NewNotifier opens the kernel notifier (default NewFSNotifier); an
	// error polls every scope (notify_unavailable).
	NewNotifier func() (Notifier, error)
	// MaxWatches is the kernel watch budget (default DefaultMaxWatches()).
	MaxWatches int
	// Remote reports whether a directory is on a filesystem kernel
	// notifications cannot observe (default: statfs magic on Linux).
	Remote         func(dir string) bool
	Debounce       time.Duration
	PollInterval   time.Duration
	SafetyInterval time.Duration
	MaxScanEntries int
	// Scanned is called after each registration or reconciliation scan of
	// a scope (diagnostics, tests). It must not block.
	Scanned func(protocol.ScopeRef)
}

// scope is one watched scope.
type scope struct {
	ref  protocol.ScopeRef
	spec protocol.FileScope
	// dir is the resolved root (absolute OS path).
	dir    string
	mode   string
	reason string
	// watches are the directories (OS paths) this scope watches.
	watches map[string]bool
	// registered: the initial walk (watches and baseline) finished.
	registered bool

	snapshot  map[string]uint64
	entries   int
	truncated bool

	pending  map[string]bool
	overflow bool
	flushAt  time.Time
	nextScan time.Time
	queued   bool
	removed  bool
}

// Watcher watches the declared scopes. Create it with New, call Run.
type Watcher struct {
	opts Options
	log  *slog.Logger
	clk  clock.Clock

	notifier Notifier
	notifyOK bool

	mu     sync.Mutex
	scopes map[protocol.ScopeRef]*scope
	// owners maps a watched directory to the scopes watching it (nested
	// scopes share kernel watches, reference-counted here).
	owners map[string][]*scope
	used   int

	wake  chan struct{}
	scanQ chan protocol.ScopeRef
}

// New returns a Watcher. The kernel notifier is opened here; when it
// cannot be, every scope is polled.
func New(o Options) *Watcher {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.NewNotifier == nil {
		o.NewNotifier = NewFSNotifier
	}
	if o.MaxWatches <= 0 {
		o.MaxWatches = DefaultMaxWatches()
	}
	if o.Remote == nil {
		o.Remote = RemoteFilesystem
	}
	if o.Debounce <= 0 {
		o.Debounce = DefaultDebounce
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.SafetyInterval <= 0 {
		o.SafetyInterval = DefaultSafetyInterval
	}
	if o.MaxScanEntries <= 0 {
		o.MaxScanEntries = DefaultMaxScanEntries
	}
	w := &Watcher{opts: o, log: o.Logger.With("component", "watch"), clk: o.Clock,
		scopes: map[protocol.ScopeRef]*scope{}, owners: map[string][]*scope{},
		wake: make(chan struct{}, 1), scanQ: make(chan protocol.ScopeRef, protocol.MaxWatchScopes)}
	n, err := o.NewNotifier()
	if err != nil {
		w.log.Warn("kernel file notifications are unavailable; watched scopes are polled", "error", err)
	} else {
		w.notifier, w.notifyOK = n, true
	}
	return w
}

// Notifying reports whether kernel notifications are available (else
// every scope is polled).
func (w *Watcher) Notifying() bool { return w.notifyOK }

// Close releases the kernel notifier.
func (w *Watcher) Close() error {
	if w.notifier != nil {
		return w.notifier.Close()
	}
	return nil
}

func (w *Watcher) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes notifications, debounced flushes and reconciliation scans
// until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	var events <-chan Event
	var errs <-chan error
	if w.notifier != nil {
		events, errs = w.notifier.Events(), w.notifier.Errors()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.scanLoop(ctx)
	}()
	defer func() { <-done }()
	timer := w.clk.NewTimer(w.untilNext())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			w.onEvent(e)
		case err := <-errs:
			w.onError(err)
		case <-w.wake:
		case <-timer.C():
		}
		w.tick()
		timer.Reset(w.untilNext())
	}
}

// scanLoop runs queued registrations and reconciliations one at a time.
func (w *Watcher) scanLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ref := <-w.scanQ:
			w.reconcile(ctx, ref)
			if w.opts.Scanned != nil {
				w.opts.Scanned(ref)
			}
			w.poke()
		}
	}
}

// untilNext is the time until the next flush or scan is due (at most the
// poll interval).
func (w *Watcher) untilNext() time.Duration {
	now := w.clk.Now()
	next := now.Add(w.opts.PollInterval)
	w.mu.Lock()
	for _, s := range w.scopes {
		if !s.flushAt.IsZero() && s.flushAt.Before(next) {
			next = s.flushAt
		}
		if !s.queued && !s.nextScan.IsZero() && s.nextScan.Before(next) {
			next = s.nextScan
		}
	}
	w.mu.Unlock()
	return max(next.Sub(now), time.Millisecond)
}

// tick publishes due invalidations and queues due scans.
func (w *Watcher) tick() {
	now := w.clk.Now()
	var out []protocol.FSInvalidationPayload
	w.mu.Lock()
	for _, s := range w.scopes {
		if !s.flushAt.IsZero() && !now.Before(s.flushAt) {
			out = append(out, s.takePending(now))
		}
		if !s.queued && !s.nextScan.IsZero() && !now.Before(s.nextScan) {
			select {
			case w.scanQ <- s.ref:
				s.queued = true
			default: // the queue holds every scope at most once; cannot fill
			}
		}
	}
	w.mu.Unlock()
	for _, p := range out {
		w.opts.Invalidate(p)
	}
}

// takePending builds the scope's invalidation and clears it (w.mu held).
func (s *scope) takePending(now time.Time) protocol.FSInvalidationPayload {
	p := protocol.FSInvalidationPayload{Scope: s.ref, At: now.UTC(), Overflow: s.overflow}
	if !s.overflow {
		for rel := range s.pending {
			p.Paths = append(p.Paths, rel)
		}
		slices.Sort(p.Paths)
	}
	if len(p.Paths) == 0 {
		p.Overflow = true
	}
	s.pending, s.overflow, s.flushAt = nil, false, time.Time{}
	return p
}

// mark records a changed path of s (w.mu held).
func (w *Watcher) mark(s *scope, rel string, now time.Time) {
	if s.flushAt.IsZero() {
		s.flushAt = now.Add(w.opts.Debounce)
	}
	if s.overflow {
		return
	}
	if rel == "" {
		s.overflow, s.pending = true, nil
		return
	}
	if s.pending == nil {
		s.pending = map[string]bool{}
	}
	s.pending[rel] = true
	if len(s.pending) > protocol.MaxPaths {
		s.overflow, s.pending = true, nil
	}
}

// relPath maps an absolute path to s's root-relative slash path; ok is
// false outside the root.
func (s *scope) relPath(p string) (string, bool) {
	rel, err := filepath.Rel(s.dir, p)
	if err != nil || filepath.IsAbs(rel) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") || !protocol.ValidRelativePath(rel) {
		return "", false
	}
	return rel, true
}

// onEvent handles one kernel notification.
func (w *Watcher) onEvent(e Event) {
	now := w.clk.Now()
	parent := filepath.Dir(e.Path)
	w.mu.Lock()
	targets := append(slices.Clone(w.owners[parent]), w.owners[e.Path]...)
	var newDirs []*scope
	for _, s := range uniq(targets) {
		if s.removed || s.mode != protocol.WatchInotify {
			continue
		}
		if e.Path == s.dir {
			// The root itself was removed, renamed or changed: re-resolve.
			if e.Op&(OpRemove|OpRename) != 0 {
				w.dropWatchesLocked(s, s.dir)
				s.registered = false
				s.nextScan = now
			}
			w.mark(s, "", now)
			continue
		}
		rel, ok := s.relPath(e.Path)
		if !ok {
			continue
		}
		w.mark(s, rel, now)
		if e.Op&(OpRemove|OpRename) != 0 && s.watches[e.Path] {
			w.dropWatchesLocked(s, e.Path)
		}
		if e.Op&OpCreate != 0 && !s.watches[e.Path] {
			newDirs = append(newDirs, s)
		}
	}
	w.mu.Unlock()
	if len(newDirs) == 0 {
		return
	}
	// A new directory (or one moved in): watch it and everything below it.
	fi, err := os.Lstat(e.Path)
	if err != nil || !fi.IsDir() {
		return
	}
	for _, s := range newDirs {
		w.addSubtree(s, e.Path)
	}
}

func uniq(in []*scope) []*scope {
	slices.SortFunc(in, func(a, b *scope) int { return strings.Compare(a.ref.Kind+":"+a.ref.ID, b.ref.Kind+":"+b.ref.ID) })
	return slices.Compact(in)
}

// addSubtree watches a new directory below s's root and reports what is
// already in it.
func (w *Watcher) addSubtree(s *scope, dir string) {
	rel, ok := s.relPath(dir)
	if !ok {
		return
	}
	_, err := walkTree(s.dir, rel, w.opts.MaxScanEntries, func(abs, r string) error {
		if !w.watchDir(s, abs) {
			return errStopWalk
		}
		w.mu.Lock()
		w.mark(s, r, w.clk.Now())
		w.mu.Unlock()
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log.Debug("could not watch a new directory", "scope", s.ref.Kind, "error", err)
	}
}

// watchDir adds a kernel watch for dir to s, within the budget. When the
// budget is exhausted, s switches to poll mode and false is returned.
func (w *Watcher) watchDir(s *scope, dir string) bool {
	w.mu.Lock()
	if s.removed || s.mode != protocol.WatchInotify {
		w.mu.Unlock()
		return false
	}
	if s.watches[dir] {
		w.mu.Unlock()
		return true
	}
	shared := len(w.owners[dir]) > 0
	if !shared && w.used >= w.opts.MaxWatches {
		w.toPollLocked(s, protocol.WatchReasonLimit)
		w.mu.Unlock()
		w.log.Warn("file watch limit reached; the scope is polled instead", "scope", s.ref.Kind, "limit", w.opts.MaxWatches)
		return false
	}
	if !shared {
		w.used++
	}
	s.watches[dir] = true
	w.owners[dir] = append(w.owners[dir], s)
	w.mu.Unlock()
	if !shared {
		err := errNotRealDir
		if realDir(dir) {
			err = w.notifier.Add(dir)
			// The directory must still be the same real directory after the
			// watch was added (inotify follows a symlink swapped in).
			if err == nil && !realDir(dir) {
				_ = w.notifier.Remove(dir)
				err = errNotRealDir
			}
		}
		if err != nil {
			w.mu.Lock()
			w.releaseLocked(s, dir)
			if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errNotRealDir) {
				// ENOSPC: the kernel's own limit (shared with other
				// processes) is reached before ours.
				w.toPollLocked(s, protocol.WatchReasonLimit)
			}
			w.mu.Unlock()
			return false
		}
	}
	return true
}

// releaseLocked forgets s's watch of dir (and removes the kernel watch
// when no other scope uses it).
func (w *Watcher) releaseLocked(s *scope, dir string) {
	if !s.watches[dir] {
		return
	}
	delete(s.watches, dir)
	owners := slices.DeleteFunc(w.owners[dir], func(o *scope) bool { return o == s })
	if len(owners) > 0 {
		w.owners[dir] = owners
		return
	}
	delete(w.owners, dir)
	w.used--
	if w.notifier != nil {
		_ = w.notifier.Remove(dir) // already gone for deleted directories
	}
}

// dropWatchesLocked releases s's watches of dir and everything below it.
func (w *Watcher) dropWatchesLocked(s *scope, dir string) {
	prefix := dir + string(filepath.Separator)
	for d := range s.watches {
		if d == dir || strings.HasPrefix(d, prefix) {
			w.releaseLocked(s, d)
		}
	}
}

// toPollLocked switches s to poll mode, releasing its kernel watches.
func (w *Watcher) toPollLocked(s *scope, reason string) {
	for d := range s.watches {
		w.releaseLocked(s, d)
	}
	s.mode, s.reason = protocol.WatchPoll, reason
	s.nextScan = w.clk.Now().Add(w.opts.PollInterval)
	w.mark(s, "", w.clk.Now()) // changes may have been missed while switching
}

// onError handles a notifier error: a queue overflow reconciles every
// inotify scope at once.
func (w *Watcher) onError(err error) {
	if !errors.Is(err, ErrOverflow) {
		w.log.Warn("kernel file notification error", "error", err)
		return
	}
	w.log.Warn("kernel file notifications overflowed; reconciling every watched scope")
	now := w.clk.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.scopes {
		if s.mode == protocol.WatchInotify {
			s.nextScan = now
		}
	}
}

// SetScopes replaces the watch set (files.watch). New scopes are resolved
// now and registered in the background; removed scopes stop at once.
func (w *Watcher) SetScopes(ctx context.Context, in protocol.FilesWatchInput) protocol.FilesWatchOutput {
	want := map[protocol.ScopeRef]protocol.FileScope{}
	for _, sc := range in.Scopes {
		want[protocol.ScopeRef{Kind: sc.Kind, ID: sc.ID}] = sc
	}
	// Resolve outside the lock (volume scopes ask the Engine).
	type resolved struct {
		dir string
		err error
	}
	res := map[protocol.ScopeRef]resolved{}
	for ref, sc := range want {
		w.mu.Lock()
		cur := w.scopes[ref]
		same := cur != nil && cur.spec == sc
		w.mu.Unlock()
		if same {
			continue
		}
		dir, err := w.opts.Resolve(ctx, sc)
		res[ref] = resolved{dir: dir, err: err}
	}
	now := w.clk.Now()
	w.mu.Lock()
	for ref, s := range w.scopes {
		if _, ok := want[ref]; !ok {
			w.removeLocked(s)
		}
	}
	for ref, sc := range want {
		r, changed := res[ref]
		if !changed {
			continue
		}
		if old := w.scopes[ref]; old != nil {
			w.removeLocked(old)
		}
		s := &scope{ref: ref, spec: sc, watches: map[string]bool{}, nextScan: now}
		switch {
		case r.err != nil:
			s.mode, s.reason = protocol.WatchUnavailable, reasonFor(r.err)
			s.nextScan = now.Add(w.opts.PollInterval) // retried: the directory may appear
		case !w.notifyOK:
			s.dir, s.mode, s.reason = r.dir, protocol.WatchPoll, protocol.WatchReasonNotify
		case w.opts.Remote(r.dir):
			s.dir, s.mode, s.reason = r.dir, protocol.WatchPoll, protocol.WatchReasonRemote
		default:
			s.dir, s.mode = r.dir, protocol.WatchInotify
		}
		w.scopes[ref] = s
	}
	out := w.statusLocked(in.Scopes)
	w.mu.Unlock()
	w.poke()
	return out
}

// reasonFor maps a resolution failure to a watch reason.
func reasonFor(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		switch he.Code {
		case protocol.CodeUnsupportedVolume:
			return protocol.WatchReasonUnsupported
		case protocol.CodeForbiddenPath:
			return protocol.WatchReasonForbidden
		}
	}
	return protocol.WatchReasonNotFound
}

func (w *Watcher) removeLocked(s *scope) {
	s.removed = true
	for d := range s.watches {
		w.releaseLocked(s, d)
	}
	delete(w.scopes, s.ref)
}

// statusLocked reports the watch state of scopes (in request order).
func (w *Watcher) statusLocked(order []protocol.FileScope) protocol.FilesWatchOutput {
	out := protocol.FilesWatchOutput{Scopes: []protocol.WatchStatus{}, WatchLimit: w.opts.MaxWatches, WatchesUsed: w.used}
	for _, sc := range order {
		s := w.scopes[protocol.ScopeRef{Kind: sc.Kind, ID: sc.ID}]
		if s == nil {
			continue
		}
		st := protocol.WatchStatus{Scope: s.ref, Mode: s.mode, Watches: len(s.watches), Entries: s.entries, Reason: s.reason}
		if st.Reason == "" && s.truncated {
			st.Reason = protocol.WatchReasonScanTruncate
		}
		out.Scopes = append(out.Scopes, st)
	}
	return out
}

// Status reports the current watch set (diagnostics, tests).
func (w *Watcher) Status() protocol.FilesWatchOutput {
	w.mu.Lock()
	defer w.mu.Unlock()
	var order []protocol.FileScope
	for _, s := range w.scopes {
		order = append(order, s.spec)
	}
	slices.SortFunc(order, func(a, b protocol.FileScope) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	return w.statusLocked(order)
}

// reconcile registers a new scope (watches and baseline) or scans it and
// reports the directories that changed since the last scan.
func (w *Watcher) reconcile(ctx context.Context, ref protocol.ScopeRef) {
	w.mu.Lock()
	s := w.scopes[ref]
	if s == nil || s.removed {
		w.mu.Unlock()
		return
	}
	s.queued = false
	spec, dir, registered, mode := s.spec, s.dir, s.registered, s.mode
	w.mu.Unlock()

	if !registered || mode == protocol.WatchUnavailable {
		// (Re-)resolve: the root may have been replaced, removed or
		// created since.
		d, err := w.opts.Resolve(ctx, spec)
		w.mu.Lock()
		if s.removed {
			w.mu.Unlock()
			return
		}
		now := w.clk.Now()
		if err != nil {
			if s.mode != protocol.WatchUnavailable {
				w.mark(s, "", now)
			}
			for d := range s.watches {
				w.releaseLocked(s, d)
			}
			s.mode, s.reason, s.dir, s.registered = protocol.WatchUnavailable, reasonFor(err), "", false
			s.nextScan = now.Add(w.opts.PollInterval)
			w.mu.Unlock()
			return
		}
		if s.mode == protocol.WatchUnavailable || d != s.dir {
			if s.dir != "" && d != s.dir {
				w.mark(s, "", now)
			}
			for old := range s.watches {
				w.releaseLocked(s, old)
			}
			s.dir, s.snapshot = d, nil
			switch {
			case !w.notifyOK:
				s.mode, s.reason = protocol.WatchPoll, protocol.WatchReasonNotify
			case w.opts.Remote(d):
				s.mode, s.reason = protocol.WatchPoll, protocol.WatchReasonRemote
			default:
				s.mode, s.reason = protocol.WatchInotify, ""
			}
		}
		dir, mode = s.dir, s.mode
		w.mu.Unlock()
	}

	var visit visitDir
	if !registered && mode == protocol.WatchInotify {
		visit = func(abs, _ string) error {
			if !w.watchDir(s, abs) {
				return errStopWalk // switched to poll: finish as a plain scan below
			}
			return nil
		}
	}
	sc, err := walkTree(dir, ".", w.opts.MaxScanEntries, visit)
	if visit != nil {
		w.mu.Lock()
		switched := s.mode != protocol.WatchInotify
		w.mu.Unlock()
		if switched {
			sc, err = walkTree(dir, ".", w.opts.MaxScanEntries, nil)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.removed {
		return
	}
	now := w.clk.Now()
	if err != nil {
		// The root vanished or became unreadable: re-resolve next time.
		w.mark(s, "", now)
		s.registered = false
		s.nextScan = now.Add(w.opts.PollInterval)
		return
	}
	if s.snapshot != nil {
		changed := diffScans(s.snapshot, sc, ".")
		for _, rel := range changed {
			w.mark(s, rel, now)
		}
	}
	s.snapshot, s.entries, s.truncated, s.registered = sc.dirs, sc.entries, sc.truncated, true
	if s.mode == protocol.WatchInotify {
		s.nextScan = now.Add(w.opts.SafetyInterval)
	} else {
		s.nextScan = now.Add(w.opts.PollInterval)
	}
}

// Rescan answers the manager's rescan: a bounded scan of one watched
// scope's subtree, reporting the directories that changed since the last
// scan (and updating the baseline). Unknown scopes are not_found.
func (w *Watcher) Rescan(_ context.Context, p protocol.RescanPayload) (protocol.RescanResult, error) {
	w.mu.Lock()
	s := w.scopes[p.Scope]
	var dir string
	if s != nil && s.mode != protocol.WatchUnavailable {
		dir = s.dir
	}
	w.mu.Unlock()
	if dir == "" {
		return protocol.RescanResult{}, &session.HandlerError{Code: protocol.CodeNotFound, Message: "the scope is not watched"}
	}
	limit := min(p.MaxEntries, MaxRescanEntries)
	sc, err := walkTree(dir, p.Path, limit, nil)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return protocol.RescanResult{Scope: p.Scope, Path: p.Path, Changed: []string{parentOf(p.Path)}}, nil
	case errors.Is(err, fs.ErrInvalid):
		// A file or a symlink: nothing below it to scan (never followed).
		return protocol.RescanResult{Scope: p.Scope, Path: p.Path}, nil
	case err != nil:
		return protocol.RescanResult{}, &session.HandlerError{Code: protocol.CodeInternal, Message: "the scope could not be scanned"}
	}
	res := protocol.RescanResult{Scope: p.Scope, Path: p.Path, Entries: sc.entries, Truncated: sc.truncated}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.removed {
		return res, nil
	}
	if s.snapshot == nil {
		res.Truncated = true // no baseline yet: everything may have changed
	} else {
		res.Changed = diffScans(s.snapshot, sc, p.Path)
		slices.Sort(res.Changed)
		if len(res.Changed) > protocol.MaxPaths {
			res.Changed, res.Truncated = nil, true
		}
		for rel := range s.snapshot {
			if _, seen := sc.dirs[rel]; under(rel, p.Path) && (seen || !sc.truncated) {
				delete(s.snapshot, rel)
			}
		}
		for rel, h := range sc.dirs {
			s.snapshot[rel] = h
		}
	}
	return res, nil
}

// errNotRealDir: a path is no longer a real directory (replaced by a
// symlink or a file, or removed): it is not watched.
var errNotRealDir = errors.New("watch: not a real directory")

// realDir reports whether abs is a directory reached without any symlink
// (abs is built from a symlink-free root, so it equals its resolution).
func realDir(abs string) bool {
	fi, err := os.Lstat(abs)
	if err != nil || !fi.IsDir() {
		return false
	}
	r, err := filepath.EvalSymlinks(abs)
	return err == nil && r == abs
}

func parentOf(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return "."
}
