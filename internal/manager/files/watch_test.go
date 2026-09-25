package files_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/agents"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/files"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// watchAgents records files.watch requests and answers rescans.
type watchAgents struct {
	mu          sync.Mutex
	sets        [][]string
	rescans     []protocol.RescanPayload
	offline     bool
	unsupported bool
	changed     []string
}

func (a *watchAgents) RequestEnvironment(_ context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.offline {
		return nil, jobs.ErrAgentOffline
	}
	if a.unsupported {
		return nil, &agents.RequestError{Code: protocol.CodeUnsupportedRequest}
	}
	in := input.(protocol.FilesWatchInput)
	var set []string
	for _, s := range in.Scopes {
		set = append(set, s.Kind+":"+s.ID)
	}
	a.sets = append(a.sets, set)
	return json.RawMessage(`{"scopes":[],"watchLimit":8192,"watchesUsed":0}`), nil
}

func (a *watchAgents) RescanEnvironment(_ context.Context, env string, p protocol.RescanPayload, _ time.Duration) (protocol.RescanResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rescans = append(a.rescans, p)
	return protocol.RescanResult{Scope: p.Scope, Path: p.Path, Changed: a.changed}, nil
}

func (a *watchAgents) pushes() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.sets)
}

// watchStacks lists stack scopes and records external changes.
type watchStacks struct {
	mu      sync.Mutex
	ids     []string
	changes []string
}

func (s *watchStacks) WatchScopes(context.Context, string) ([]protocol.FileScope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.FileScope
	for _, id := range s.ids {
		out = append(out, protocol.FileScope{Kind: protocol.ScopeStack, ID: id, Dir: "/stacks/" + id})
	}
	return out, nil
}

func (s *watchStacks) ExternalChange(_ context.Context, id string, paths []string, overflow bool) (*domain.StackRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := id + ":"
	for _, p := range paths {
		c += p + ","
	}
	if overflow {
		c += "<overflow>"
	}
	s.changes = append(s.changes, c)
	return nil, nil
}

func (s *watchStacks) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.changes)
}

type watchHarness struct {
	t      *testing.T
	ctx    context.Context
	w      *files.Watcher
	agents *watchAgents
	stacks *watchStacks
	clk    interface{ Advance(time.Duration) }
}

func newWatchHarness(t *testing.T) *watchHarness {
	clk := testutil.FakeClock()
	h := &watchHarness{t: t, ctx: testutil.Context(t), agents: &watchAgents{}, stacks: &watchStacks{ids: []string{"s1"}}, clk: clk}
	h.w = files.NewWatcher(files.WatcherOptions{Agents: h.agents, Stacks: h.stacks, Bus: events.New(clk), Clock: clk, Logger: testutil.Logger(t)})
	return h
}

// settle advances past the settle delay and runs what is due.
func (h *watchHarness) settle() {
	h.clk.Advance(files.DefaultWatchSettle)
	h.w.Tick(h.ctx)
	h.w.Wait()
}

var online = events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: envID, EnvironmentID: envID}

// TestWatchSetFollowsStacksAndOpenVolumes (#23): the agent is told to
// watch every stack plus the volumes with an open view (a live stream's
// volume filter holds it, a listing leases it for 5 minutes); an unchanged
// set is not re-sent, but it is after every reconnect.
func TestWatchSetFollowsStacksAndOpenVolumes(t *testing.T) {
	h := newWatchHarness(t)
	h.w.Handle(online)
	h.settle()
	if got := h.agents.pushes(); len(got) != 1 || !slices.Equal(got[0], []string{"stack:s1"}) {
		t.Fatalf("initial set %v", got)
	}
	release := h.w.Hold(envID, "pgdata")
	h.w.Hold("", "ignored")()
	h.w.Hold(envID, "../escape")()
	h.settle()
	if got := h.agents.pushes(); len(got) != 2 || !slices.Equal(got[1], []string{"stack:s1", "volume:pgdata"}) {
		t.Fatalf("held volume %v", got)
	}
	// A stack update that does not change the set sends nothing.
	h.w.Handle(events.Event{Type: events.StackUpdated, ResourceType: events.ResourceStack, ResourceID: "s1", EnvironmentID: envID})
	h.settle()
	if n := len(h.agents.pushes()); n != 2 {
		t.Fatalf("unchanged set re-sent (%d)", n)
	}
	// A new stack is added.
	h.stacks.mu.Lock()
	h.stacks.ids = append(h.stacks.ids, "s2")
	h.stacks.mu.Unlock()
	h.w.Handle(events.Event{Type: events.StackCreated, ResourceType: events.ResourceStack, ResourceID: "s2", EnvironmentID: envID})
	h.settle()
	if got := h.agents.pushes(); !slices.Equal(got[len(got)-1], []string{"stack:s1", "stack:s2", "volume:pgdata"}) {
		t.Fatalf("new stack %v", got)
	}
	// Released: the lease keeps it 5 minutes, then it is dropped.
	release()
	release()
	h.w.Touch(envID, "cache") // a listing of another volume
	h.settle()
	if got := h.agents.pushes(); !slices.Equal(got[len(got)-1], []string{"stack:s1", "stack:s2", "volume:cache", "volume:pgdata"}) {
		t.Fatalf("leased %v", got)
	}
	h.clk.Advance(files.DefaultWatchLease)
	h.w.Tick(h.ctx)
	h.settle()
	if got := h.agents.pushes(); !slices.Equal(got[len(got)-1], []string{"stack:s1", "stack:s2"}) {
		t.Fatalf("after the lease %v", got)
	}
	// Reconnect: sent again although unchanged.
	n := len(h.agents.pushes())
	h.w.Handle(events.Event{Type: events.EnvironmentOffline, EnvironmentID: envID})
	h.w.Handle(online)
	h.settle()
	if len(h.agents.pushes()) != n+1 {
		t.Fatal("not re-sent after reconnect")
	}
	// Offline agents are retried when they come online; agents without a
	// watcher are not asked again.
	h.agents.unsupported = true
	h.w.Handle(online)
	h.settle()
	h.w.Handle(events.Event{Type: events.StackUpdated, EnvironmentID: envID})
	h.settle()
	if len(h.agents.pushes()) != n+1 {
		t.Fatal("unsupported agent asked again")
	}
}

// TestExternalChangesSettleAndGapsRescan: invalidations of a stack scope
// are settled (merged within 1 s) before the stack service checks them;
// volume scopes never record revisions; a sequence gap rescans every stack
// scope and reports what changed.
func TestExternalChangesSettleAndGapsRescan(t *testing.T) {
	h := newWatchHarness(t)
	inv := func(kind, id string, overflow bool, paths ...string) events.Event {
		return events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: kind + ":" + id,
			EnvironmentID: envID, Paths: paths, Overflow: overflow, Attributes: map[string]string{"scopeKind": kind, "scopeId": id}}
	}
	h.w.Handle(inv("stack", "s1", false, "compose.yaml"))
	h.w.Handle(inv("stack", "s1", false, ".env", "compose.yaml"))
	h.w.Handle(inv("volume", "pgdata", false, "base/1"))
	h.w.Tick(h.ctx)
	h.w.Wait()
	if got := h.stacks.recorded(); len(got) != 0 {
		t.Fatalf("recorded before settling: %v", got)
	}
	h.settle()
	if got := h.stacks.recorded(); !slices.Equal(got, []string{"s1:.env,compose.yaml,"}) {
		t.Fatalf("settled %v", got)
	}
	h.w.Handle(inv("stack", "s1", true))
	h.settle()
	if got := h.stacks.recorded(); got[len(got)-1] != "s1:<overflow>" {
		t.Fatalf("overflow %v", got)
	}
	// A sequence gap: rescan every stack scope.
	h.agents.changed = []string{"."}
	h.w.Handle(events.Event{Type: events.FilesInvalidated, ResourceType: events.ResourceFileScope, ResourceID: "*", EnvironmentID: envID, Overflow: true})
	h.settle()
	if len(h.agents.rescans) != 1 || h.agents.rescans[0].Scope.ID != "s1" || h.agents.rescans[0].Reason != "sequence_gap" || h.agents.rescans[0].MaxEntries != files.RescanEntries {
		t.Fatalf("rescans %+v", h.agents.rescans)
	}
	if got := h.stacks.recorded(); got[len(got)-1] != "s1:.," {
		t.Fatalf("after the gap %v", got)
	}
}
