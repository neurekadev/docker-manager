package stacks_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/watch"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/events"
	mfiles "github.com/neurekadev/dockyard/internal/manager/files"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/stacks"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestExternalComposeEditRecordsRevision (#23, #25 Q1) runs the whole
// chain on the real filesystem: the agent's watcher (fsnotify) sees
// compose.yaml edited outside DockYard, the invalidation reaches the
// manager's bus, the manager's file watcher settles it and the stack
// service records a revision (source external) and marks undeployed
// changes. A change outside the definition records nothing and does not
// even read the definition.
func TestExternalComposeEditRecordsRevision(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	before := len(revisions(t, h, st.ID))
	project, err := filepath.EvalSymlinks(h.path(st.Dir))
	if err != nil {
		t.Fatal(err)
	}

	// Agent side: the real watcher and kernel notifications. Its
	// invalidations are relayed to the bus like the session relay does.
	scanned := make(chan struct{}, 4)
	aw := watch.New(watch.Options{
		Clock: clock.Real(), Logger: testutil.Logger(t),
		Resolve: func(context.Context, protocol.FileScope) (string, error) { return project, nil },
		Invalidate: func(p protocol.FSInvalidationPayload) {
			h.bus.Publish(events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope,
				ResourceID: p.Scope.Kind + ":" + p.Scope.ID, EnvironmentID: env, Paths: p.Paths, Overflow: p.Overflow,
				Attributes: map[string]string{"scopeKind": p.Scope.Kind, "scopeId": p.Scope.ID}})
		},
		Scanned: func(protocol.ScopeRef) { scanned <- struct{}{} },
	})
	t.Cleanup(func() { _ = aw.Close() })
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() { defer close(done); aw.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	out := aw.SetScopes(ctx, protocol.FilesWatchInput{Scopes: []protocol.FileScope{{Kind: protocol.ScopeStack, ID: st.ID, Dir: filepath.ToSlash(project)}}})
	if out.Scopes[0].Mode != protocol.WatchInotify {
		t.Fatalf("watch set %+v", out)
	}
	<-scanned // registered: watches and baseline

	// Manager side: the file watcher (fake clock) over the real stack
	// service.
	fw := mfiles.NewWatcher(mfiles.WatcherOptions{Stacks: h.svc, Bus: h.bus, Clock: h.clk, Logger: testutil.Logger(t)})
	sub := h.bus.Subscribe(64, func(e events.Event) bool { return e.Type == events.FilesInvalidated })
	defer sub.Close()
	next := func(want string) events.Event {
		t.Helper()
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case e := <-sub.C():
				if slices.Contains(e.Paths, want) || e.Overflow {
					return e
				}
			case <-timeout.C:
				t.Fatalf("no invalidation for %s", want)
			}
		}
	}
	settle := func(e events.Event) {
		fw.Handle(e)
		h.clk.Advance(mfiles.DefaultWatchSettle)
		fw.Tick(h.ctx)
		fw.Wait()
	}

	// A file outside the definition: invalidated, nothing recorded.
	h.agents.mu.Lock()
	h.agents.requests = nil
	h.agents.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(project, "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	settle(next("data"))
	if n := len(revisions(t, h, st.ID)); n != before {
		t.Fatalf("a data directory recorded a revision (%d -> %d)", before, n)
	}
	h.agents.mu.Lock()
	reads := slices.Contains(h.agents.requests, protocol.ReqComposeRead)
	h.agents.mu.Unlock()
	if reads {
		t.Fatal("the definition was read for a change outside it")
	}

	// compose.yaml edited outside DockYard (an editor on the host).
	if err := os.WriteFile(filepath.Join(project, "compose.yaml"), []byte(shopYAML+"# edited on the host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settle(next("compose.yaml"))
	revs := revisions(t, h, st.ID)
	if len(revs) != before+1 || revs[0].Source != domain.RevisionExternal {
		t.Fatalf("revisions %d -> %d, newest %+v", before, len(revs), revs[0])
	}
	if !h.get(st.ID).UndeployedChanges() {
		t.Fatal("the external edit is not an undeployed change")
	}
}

// TestExternalChangeAndWatchScopes: ExternalChange records only changes
// touching the definition (files, their directories, the whole scope) and
// never twice for the same bytes; WatchScopes lists every stack's project
// directory below the reported stacks root.
func TestExternalChangeAndWatchScopes(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	before := len(revisions(t, h, st.ID))
	if rev, err := h.svc.ExternalChange(h.ctx, st.ID, []string{"data/db.sqlite", "data"}, false); err != nil || rev != nil {
		t.Fatalf("non-definition change %+v %v", rev, err)
	}
	h.write("DB_TAG=17\n", "shop", ".env")
	rev, err := h.svc.ExternalChange(h.ctx, st.ID, []string{".env"}, false)
	if err != nil || rev == nil || rev.Source != domain.RevisionExternal {
		t.Fatalf("env change %+v %v", rev, err)
	}
	// The same bytes again (a reconciliation reporting the root) record
	// nothing new.
	if rev, err := h.svc.ExternalChange(h.ctx, st.ID, []string{"."}, false); err != nil || rev != nil {
		t.Fatalf("unchanged %+v %v", rev, err)
	}
	if n := len(revisions(t, h, st.ID)); n != before+1 {
		t.Fatalf("revisions %d -> %d", before, n)
	}

	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	caps := `{"roots":[{"kind":"stacks","path":"/var/lib/docker/volumes/dockyard_stacks/_data","watch":"inotify"}]}`
	svc, err := stacks.New(stacks.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(t), Keyring: secrets.NewKeyring(key),
		Agents: h.agents, Environments: fakeEnvironments{h.agents}, Jobs: h.eng, Bus: h.bus, Systems: fakeSystems{caps}})
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := svc.WatchScopes(h.ctx, env)
	if err != nil || len(scopes) != 1 || scopes[0].ID != st.ID || scopes[0].Dir != "/var/lib/docker/volumes/dockyard_stacks/_data/shop" {
		t.Fatalf("watch scopes %+v %v", scopes, err)
	}
	// Without a verified stacks root there is nothing to watch yet.
	if scopes, err := h.svc.WatchScopes(h.ctx, env); err != nil || len(scopes) != 0 {
		t.Fatalf("no root %+v %v", scopes, err)
	}
}
