package watch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// fakeNotifier records the watched directories; tests feed events to the
// watcher directly.
type fakeNotifier struct {
	mu      sync.Mutex
	watched map[string]bool
	addErr  func(dir string) error
	events  chan Event
	errs    chan error
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{watched: map[string]bool{}, events: make(chan Event, 64), errs: make(chan error, 4)}
}

func (f *fakeNotifier) Add(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		if err := f.addErr(dir); err != nil {
			return err
		}
	}
	f.watched[dir] = true
	return nil
}

func (f *fakeNotifier) Remove(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watched, dir)
	return nil
}

func (f *fakeNotifier) Events() <-chan Event { return f.events }
func (f *fakeNotifier) Errors() <-chan error { return f.errs }
func (f *fakeNotifier) Close() error         { return nil }

func (f *fakeNotifier) dirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for d := range f.watched {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// harness drives a Watcher without its Run loop: deterministic time.
type harness struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock.Fake
	w     *Watcher
	n     *fakeNotifier
	root  string
	mu    sync.Mutex
	inval []protocol.FSInvalidationPayload
}

var stackRef = protocol.ScopeRef{Kind: protocol.ScopeStack, ID: "s1"}

func newHarness(t *testing.T, notify bool, opts ...func(*Options)) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: testutil.Context(t), clk: testutil.FakeClock(), n: newFakeNotifier(), root: root}
	o := Options{
		Clock: h.clk, Logger: testutil.Logger(t),
		Resolve: func(_ context.Context, sc protocol.FileScope) (string, error) {
			if sc.ID == "missing" {
				return "", &session.HandlerError{Code: protocol.CodeNotFound, Message: "no"}
			}
			return filepath.FromSlash(sc.Dir), nil
		},
		Invalidate: func(p protocol.FSInvalidationPayload) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.inval = append(h.inval, p)
		},
		NewNotifier: func() (Notifier, error) {
			if !notify {
				return nil, errors.New("inotify disabled for the test")
			}
			return h.n, nil
		},
		MaxWatches: 1000,
	}
	for _, fn := range opts {
		fn(&o)
	}
	h.w = New(o)
	return h
}

func (h *harness) scope() protocol.FileScope {
	return protocol.FileScope{Kind: protocol.ScopeStack, ID: "s1", Dir: filepath.ToSlash(h.root)}
}

func (h *harness) path(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h *harness) mkdir(rel string) {
	h.t.Helper()
	if err := os.MkdirAll(h.path(rel), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) write(rel, content string) {
	h.t.Helper()
	if err := os.WriteFile(h.path(rel), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// register declares the scope and runs its registration walk.
func (h *harness) register() protocol.FilesWatchOutput {
	h.t.Helper()
	out := h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{h.scope()}})
	h.drainScans()
	return out
}

// drainScans runs the queued scans synchronously.
func (h *harness) drainScans() {
	h.w.tick()
	for {
		select {
		case ref := <-h.w.scanQ:
			h.w.reconcile(h.ctx, ref)
		default:
			return
		}
	}
}

// flush advances past the debounce and returns what was published.
func (h *harness) flush() []protocol.FSInvalidationPayload {
	h.clk.Advance(DefaultDebounce)
	h.w.tick()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.inval
	h.inval = nil
	return out
}

func (h *harness) event(rel string, op Op) { h.w.onEvent(Event{Path: h.path(rel), Op: op}) }

func paths(ps []protocol.FSInvalidationPayload) []string {
	var out []string
	for _, p := range ps {
		if p.Overflow {
			out = append(out, "<overflow>")
		}
		out = append(out, p.Paths...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestInotifyCreateEditRenameDelete (#23): a scope in inotify mode watches
// every directory, and create/edit/rename/delete notifications become
// debounced invalidations of root-relative paths; new directories are
// watched, renamed and deleted ones released.
func TestInotifyCreateEditRenameDelete(t *testing.T) {
	h := newHarness(t, true)
	h.mkdir("config/nested")
	h.write("compose.yaml", "services: {}\n")
	out := h.register()
	if len(out.Scopes) != 1 || out.Scopes[0].Mode != protocol.WatchInotify {
		t.Fatalf("watch set %+v", out)
	}
	want := []string{h.root, h.path("config"), h.path("config/nested")}
	slices.Sort(want)
	if got := h.n.dirs(); !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
	if st := h.w.Status(); st.WatchesUsed != 3 || st.Scopes[0].Watches != 3 {
		t.Fatalf("accounting %+v", st)
	}
	if got := h.flush(); len(got) != 0 {
		t.Fatalf("registration published %v", got)
	}

	// Edit: several notifications for one file within the debounce window
	// are one invalidation.
	h.write("compose.yaml", "services: {web: {image: nginx}}\n")
	h.event("compose.yaml", OpWrite)
	h.clk.Advance(DefaultDebounce / 2)
	h.w.tick()
	h.event("compose.yaml", OpWrite|OpChmod)
	h.mu.Lock()
	early := len(h.inval)
	h.mu.Unlock()
	if early != 0 {
		t.Fatal("published before the debounce elapsed")
	}
	got := h.flush()
	if len(got) != 1 || !slices.Equal(got[0].Paths, []string{"compose.yaml"}) || got[0].Overflow || got[0].Scope != stackRef {
		t.Fatalf("edit invalidation %+v", got)
	}

	// Create a directory with content: it is watched and reported.
	h.mkdir("data/deep")
	h.write("data/deep/file.txt", "x")
	h.event("data", OpCreate)
	if !slices.Contains(h.n.dirs(), h.path("data/deep")) {
		t.Fatalf("new directory not watched: %v", h.n.dirs())
	}
	if p := paths(h.flush()); !slices.Equal(p, []string{"data", "data/deep"}) {
		t.Fatalf("create invalidation %v", p)
	}

	// Rename a directory: the old name's watches are released, the new
	// name (a create) is watched.
	if err := os.Rename(h.path("data"), h.path("moved")); err != nil {
		t.Fatal(err)
	}
	h.event("data", OpRename)
	h.event("moved", OpCreate)
	dirs := h.n.dirs()
	if slices.Contains(dirs, h.path("data")) || slices.Contains(dirs, h.path("data/deep")) || !slices.Contains(dirs, h.path("moved/deep")) {
		t.Fatalf("watches after rename: %v", dirs)
	}
	if p := paths(h.flush()); !slices.Equal(p, []string{"data", "moved", "moved/deep"}) {
		t.Fatalf("rename invalidation %v", p)
	}

	// Delete a file and a directory.
	if err := os.RemoveAll(h.path("config/nested")); err != nil {
		t.Fatal(err)
	}
	h.event("config/nested", OpRemove)
	if err := os.Remove(h.path("compose.yaml")); err != nil {
		t.Fatal(err)
	}
	h.event("compose.yaml", OpRemove)
	if slices.Contains(h.n.dirs(), h.path("config/nested")) {
		t.Fatal("deleted directory still watched")
	}
	if p := paths(h.flush()); !slices.Equal(p, []string{"compose.yaml", "config/nested"}) {
		t.Fatalf("delete invalidation %v", p)
	}
	if st := h.w.Status(); st.WatchesUsed != len(h.n.dirs()) {
		t.Fatalf("accounting %d, kernel %d", st.WatchesUsed, len(h.n.dirs()))
	}
	// Notifications outside any watched directory are ignored.
	h.w.onEvent(Event{Path: filepath.Join(filepath.Dir(h.root), "elsewhere.txt"), Op: OpCreate})
	if got := h.flush(); len(got) != 0 {
		t.Fatalf("outside notification published %v", got)
	}
}

// TestDebounceOverflow: more than protocol.MaxPaths changed paths in one
// window are reported as one overflow invalidation without paths.
func TestDebounceOverflow(t *testing.T) {
	h := newHarness(t, true)
	h.register()
	for i := range protocol.MaxPaths + 1 {
		h.event("f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Duration(i).String(), OpCreate)
	}
	got := h.flush()
	if len(got) != 1 || !got[0].Overflow || len(got[0].Paths) != 0 {
		t.Fatalf("overflow invalidation %+v", got)
	}
	if err := got[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestMissedEventReconciliation (#23, #25 Q5): with kernel notifications
// disabled the scope is polled, and a change nobody reported is found by
// the next reconciliation scan within the poll interval (≤ 60 s).
func TestMissedEventReconciliation(t *testing.T) {
	if DefaultPollInterval > 60*time.Second {
		t.Fatal("poll interval exceeds the 60 s reconciliation target")
	}
	h := newHarness(t, false)
	h.mkdir("a/b")
	h.write("a/b/c.txt", "one")
	out := h.register()
	if out.Scopes[0].Mode != protocol.WatchPoll || out.Scopes[0].Reason != protocol.WatchReasonNotify {
		t.Fatalf("watch set %+v", out)
	}
	h.flush()
	// Nothing changed: the next scan reports nothing.
	h.clk.Advance(DefaultPollInterval)
	h.drainScans()
	if got := h.flush(); len(got) != 0 {
		t.Fatalf("unchanged scope reported %v", got)
	}
	// Change size and add a directory, without any notification.
	h.write("a/b/c.txt", "one two")
	h.mkdir("a/new")
	h.clk.Advance(DefaultPollInterval - DefaultDebounce)
	h.drainScans()
	if p := paths(h.flush()); !slices.Equal(p, []string{"a", "a/b", "a/new"}) {
		t.Fatalf("reconciled invalidation %v", p)
	}
	// Removal is found too.
	if err := os.RemoveAll(h.path("a/b")); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(DefaultPollInterval)
	h.drainScans()
	if p := paths(h.flush()); !slices.Equal(p, []string{"a", "a/b"}) {
		t.Fatalf("removal invalidation %v", p)
	}
}

// TestInotifyOverflowReconciles: a kernel queue overflow reconciles
// inotify scopes at once, so notifications the kernel dropped are found.
func TestInotifyOverflowReconciles(t *testing.T) {
	h := newHarness(t, true)
	h.mkdir("dir")
	h.register()
	h.write("dir/lost.txt", "dropped by the kernel")
	h.w.onError(ErrOverflow)
	h.drainScans()
	if p := paths(h.flush()); !slices.Equal(p, []string{"dir"}) {
		t.Fatalf("overflow reconciliation %v", p)
	}
}

// TestWatchLimitFallsBackToPolling: a scope that does not fit the watch
// budget is polled and holds no kernel watches; the kernel's own ENOSPC
// does the same.
func TestWatchLimitFallsBackToPolling(t *testing.T) {
	h := newHarness(t, true, func(o *Options) { o.MaxWatches = 2 })
	h.mkdir("a")
	h.mkdir("b")
	h.register()
	st := h.w.Status()
	if st.Scopes[0].Mode != protocol.WatchPoll || st.Scopes[0].Reason != protocol.WatchReasonLimit || st.WatchesUsed != 0 || len(h.n.dirs()) != 0 {
		t.Fatalf("status %+v, kernel %v", st, h.n.dirs())
	}
	// Still reconciled.
	h.flush()
	h.write("a/x", "1")
	h.clk.Advance(DefaultPollInterval)
	h.drainScans()
	if p := paths(h.flush()); !slices.Equal(p, []string{"a"}) {
		t.Fatalf("polled invalidation %v", p)
	}

	k := newHarness(t, true)
	k.n.addErr = func(string) error { return errors.New("no space left on device") }
	k.register()
	if st := k.w.Status(); st.Scopes[0].Mode != protocol.WatchPoll || st.Scopes[0].Reason != protocol.WatchReasonLimit {
		t.Fatalf("ENOSPC status %+v", st)
	}
}

// TestSymlinkEscapeNotFollowed: a symlink to a directory outside the root
// is neither watched, nor scanned, nor rescanned through.
func TestSymlinkEscapeNotFollowed(t *testing.T) {
	h := newHarness(t, true)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "secret", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir(t, outside, h.path("escape"))
	h.mkdir("inside")
	h.register()
	for _, d := range h.n.dirs() {
		if !strings.HasPrefix(d, h.root) {
			t.Fatalf("watched outside the root: %s", d)
		}
	}
	h.w.mu.Lock()
	for rel := range h.w.scopes[stackRef].snapshot {
		if strings.HasPrefix(rel, "escape") {
			t.Errorf("scanned through the symlink: %s", rel)
		}
	}
	h.w.mu.Unlock()
	// A symlink created later is reported but not descended.
	linkDir(t, filepath.Join(outside, "secret"), h.path("inside/link"))
	h.event("inside/link", OpCreate)
	for _, d := range h.n.dirs() {
		if !strings.HasPrefix(d, h.root) {
			t.Fatalf("watched outside the root: %s", d)
		}
	}
	if p := paths(h.flush()); !slices.Equal(p, []string{"inside/link"}) {
		t.Fatalf("symlink create %v", p)
	}
	// A rescan through the symlink does not walk the target.
	res, err := h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: "escape/secret", MaxEntries: 100})
	if err != nil || res.Entries != 0 || len(res.Changed) != 0 {
		t.Fatalf("rescan through symlink %+v %v", res, err)
	}
	// A directory swapped for a symlink is not watched (checked right
	// before and after adding the kernel watch).
	if realDir(h.path("escape")) || !realDir(h.path("inside")) {
		t.Fatal("realDir")
	}
}

// linkDir makes link point to the directory target: a symlink, or on
// Windows without the symlink privilege a directory junction (a reparse
// point os.Lstat does not report as a directory either).
func linkDir(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); jerr != nil {
		t.Fatalf("symlink: %v; junction: %v %s", err, jerr, out)
	}
}

// TestRescan: the manager's rescan scans a subtree, reports what changed
// since the baseline and updates it; unknown scopes are not_found.
func TestRescan(t *testing.T) {
	h := newHarness(t, true)
	h.mkdir("x/y")
	h.mkdir("z")
	h.register()
	h.write("x/y/new.txt", "n")
	h.write("z/other.txt", "o")
	res, err := h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: "x", MaxEntries: 100, Reason: "sequence_gap"})
	if err != nil || !slices.Equal(res.Changed, []string{"x/y"}) || res.Truncated || res.Entries != 2 {
		t.Fatalf("rescan %+v %v", res, err)
	}
	// The baseline was updated: a second rescan reports nothing.
	res, err = h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: "x", MaxEntries: 100})
	if err != nil || len(res.Changed) != 0 {
		t.Fatalf("second rescan %+v %v", res, err)
	}
	// The whole scope, bounded.
	res, err = h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: ".", MaxEntries: 2})
	if err != nil || !res.Truncated {
		t.Fatalf("bounded rescan %+v %v", res, err)
	}
	res, err = h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: ".", MaxEntries: 100})
	if err != nil || !slices.Equal(res.Changed, []string{"z"}) {
		t.Fatalf("full rescan %+v %v", res, err)
	}
	// A vanished subtree reports its parent.
	res, err = h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: stackRef, Path: "x/gone", MaxEntries: 100})
	if err != nil || !slices.Equal(res.Changed, []string{"x"}) {
		t.Fatalf("vanished rescan %+v %v", res, err)
	}
	var he *session.HandlerError
	if _, err := h.w.Rescan(h.ctx, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: "volume", ID: "v"}, Path: ".", MaxEntries: 1}); !errors.As(err, &he) || he.Code != protocol.CodeNotFound {
		t.Fatalf("unknown scope %v", err)
	}
}

// TestSetScopesReplacesTheWatchSet: scopes not declared any more release
// their watches; unresolvable scopes are unavailable and retried.
func TestSetScopesReplacesTheWatchSet(t *testing.T) {
	h := newHarness(t, true)
	h.mkdir("a")
	h.register()
	missing := protocol.FileScope{Kind: protocol.ScopeStack, ID: "missing", Dir: filepath.ToSlash(h.root) + "/nope"}
	out := h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{missing}})
	if len(out.Scopes) != 1 || out.Scopes[0].Mode != protocol.WatchUnavailable || out.Scopes[0].Reason != protocol.WatchReasonNotFound {
		t.Fatalf("watch set %+v", out)
	}
	if len(h.n.dirs()) != 0 || out.WatchesUsed != 0 {
		t.Fatalf("watches not released: %v", h.n.dirs())
	}
	// Declaring the same set again keeps the state.
	h.register()
	again := h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{h.scope()}})
	if again.Scopes[0].Watches != 2 {
		t.Fatalf("redeclared scope lost its watches: %+v", again)
	}
}

// TestNestedScopesShareWatches: two scopes watching the same directories
// share kernel watches (reference-counted).
func TestNestedScopesShareWatches(t *testing.T) {
	h := newHarness(t, true)
	h.mkdir("app/data")
	outer := h.scope()
	inner := protocol.FileScope{Kind: protocol.ScopeStack, ID: "s2", Dir: filepath.ToSlash(h.path("app"))}
	h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{outer, inner}})
	h.drainScans()
	if st := h.w.Status(); st.WatchesUsed != 3 {
		t.Fatalf("shared watches counted twice: %+v", st)
	}
	h.event("app/data/f", OpCreate)
	got := h.flush()
	if len(got) != 2 {
		t.Fatalf("both scopes should report: %+v", got)
	}
	h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{outer}})
	if st := h.w.Status(); st.WatchesUsed != 3 || len(h.n.dirs()) != 3 {
		t.Fatalf("outer lost shared watches: %+v %v", st, h.n.dirs())
	}
}

// TestRootReplacedIsReresolved: the root removed out from under a scope is
// reported as a whole-scope invalidation and re-resolved.
func TestRootReplacedIsReresolved(t *testing.T) {
	h := newHarness(t, true)
	sub := filepath.Join(h.root, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := protocol.FileScope{Kind: protocol.ScopeStack, ID: "s1", Dir: filepath.ToSlash(sub)}
	h.w.SetScopes(h.ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{sc}})
	h.drainScans()
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	h.w.onEvent(Event{Path: sub, Op: OpRemove})
	got := h.flush()
	if len(got) != 1 || !got[0].Overflow {
		t.Fatalf("root removal %+v", got)
	}
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h.drainScans()
	if st := h.w.Status(); st.Scopes[0].Mode != protocol.WatchInotify || st.Scopes[0].Watches != 1 {
		t.Fatalf("re-registration %+v", st)
	}
}
