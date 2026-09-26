package stacks_test

import (
	"context"
	"errors"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

type fakeSystems struct{ caps string }

func (f fakeSystems) EnvironmentSystem(_ context.Context, id string) (domain.EnvironmentSystem, error) {
	if id != env {
		return domain.EnvironmentSystem{}, domain.ErrEnvironmentNotFound
	}
	return domain.EnvironmentSystem{Environment: domain.Environment{ID: env}, Agent: &domain.Agent{ID: "ag", Capabilities: f.caps}}, nil
}

// TestFileManagerHooks (#15): stack file scopes resolve to the project
// directory below the stacks volume the agent reported, and a
// file-manager save of a definition file records a revision.
func TestFileManagerHooks(t *testing.T) {
	h := newHarness(t)
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	caps := `{"roots":[{"kind":"volumes","path":"/var/lib/docker/volumes","watch":"inotify"},` +
		`{"kind":"stacks","path":"/var/lib/docker/volumes/docker-manager_stacks/_data","watch":"inotify"}]}`
	svc, err := stacks.New(stacks.Options{DB: h.db, Clock: h.clk, Logger: testutil.Logger(t), Keyring: secrets.NewKeyring(key),
		Agents: h.agents, Environments: fakeEnvironments{h.agents}, Jobs: h.eng, Bus: h.bus, Systems: fakeSystems{caps}})
	if err != nil {
		t.Fatal(err)
	}
	st := h.create("shop", shopYAML, shopEnv)
	root, err := svc.StackFileRoot(h.ctx, st.ID)
	if err != nil || root.EnvironmentID != env || root.Dir != "/var/lib/docker/volumes/docker-manager_stacks/_data/shop" {
		t.Fatalf("root %+v %v", root, err)
	}
	if _, err := svc.StackFileRoot(h.ctx, "nope"); !errors.Is(err, domain.ErrFileScopeNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
	// The stack header's host path (#22) is the same directory.
	if p, err := svc.HostPath(h.ctx, st.ID); err != nil || p != root.Dir {
		t.Errorf("host path %q %v", p, err)
	}
	if _, err := h.svc.HostPath(h.ctx, st.ID); stackErrCode(err) != domain.StackErrRootUnavailable {
		t.Errorf("host path without systems: %v", err)
	}
	// Without reported capabilities the root is unavailable.
	if _, err := h.svc.StackFileRoot(h.ctx, st.ID); stackErrCode(err) != domain.StackErrRootUnavailable {
		t.Errorf("no systems: %v", err)
	}

	sub := h.bus.Subscribe(0, func(e events.Event) bool { return e.Type == events.StackRevisionRecorded })
	defer sub.Close()
	h.write(shopYAML+"# edited in the file manager\n", "shop", "compose.yaml")
	ctx, err := authz.WithPrincipal(h.ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	svc.StackSourcesChanged(ctx, st.ID, []string{"compose.yaml"})
	select {
	case e := <-sub.C():
		if e.ResourceID != st.ID || e.Attributes["source"] != string(domain.RevisionFileManager) {
			t.Errorf("event %+v", e)
		}
	case <-h.ctx.Done():
		t.Fatal("no revision recorded")
	}
	revs := revisions(t, h, st.ID)
	if revs[0].Source != domain.RevisionFileManager || revs[0].AuthorUserID != "alice" || !h.get(st.ID).UndeployedChanges() {
		t.Errorf("revision %+v", revs[0])
	}
}
