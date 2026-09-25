package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/agent/watch"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestFileWatcherWiring (#23): with Files the agent serves files.watch and
// rescan through the scoped file service's resolution (a stack directory
// outside the verified roots is refused), and reports each root's watch
// mode in its capabilities.
func TestFileWatcherWiring(t *testing.T) {
	a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
		Geteuid: func() int { return 0 }, Files: true,
		NewNotifier: func() (watch.Notifier, error) { return nil, errors.New("disabled") }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testutil.Context(t)
	stacksDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(stacksDir, "shop")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.storage = &storage.Result{StacksDir: filepath.ToSlash(stacksDir), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(stacksDir), OK: true}}}
	a.mu.Unlock()

	in, _ := json.Marshal(protocol.FilesWatchInput{Scopes: []protocol.FileScope{
		{Kind: protocol.ScopeStack, ID: "s1", Dir: filepath.ToSlash(project)},
		{Kind: protocol.ScopeStack, ID: "s2", Dir: filepath.ToSlash(t.TempDir())}, // not in a verified root
	}})
	out, err := a.opts.Requests[protocol.ReqFilesWatch](ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(protocol.FilesWatchOutput)
	if len(res.Scopes) != 2 || res.Scopes[0].Mode != protocol.WatchPoll || res.Scopes[0].Reason != protocol.WatchReasonNotify ||
		res.Scopes[1].Mode != protocol.WatchUnavailable || res.Scopes[1].Reason != protocol.WatchReasonForbidden {
		t.Fatalf("watch set %+v", res)
	}
	if _, err := a.opts.Requests[protocol.ReqFilesWatch](ctx, json.RawMessage(`{"scopes":[{"kind":"stack","id":"x","dir":"relative"}]}`)); err == nil {
		t.Fatal("invalid scope accepted")
	}
	rs, err := a.rescan()(ctx, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: protocol.ScopeStack, ID: "s1"}, Path: ".", MaxEntries: 10})
	if err != nil || !rs.Truncated { // no baseline yet: the whole scope counts as changed
		t.Fatalf("rescan %+v %v", rs, err)
	}
	var he *session.HandlerError
	if _, err := a.rescan()(ctx, protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: protocol.ScopeStack, ID: "s2"}, Path: ".", MaxEntries: 10}); !errors.As(err, &he) || he.Code != protocol.CodeNotFound {
		t.Fatalf("rescan of an unwatched scope: %v", err)
	}
	if mode := a.rootWatchMode(filepath.ToSlash(stacksDir)); mode != protocol.WatchPoll {
		t.Fatalf("root watch mode %s", mode)
	}
}
