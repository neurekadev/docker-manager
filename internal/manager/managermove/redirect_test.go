package managermove

import (
	"context"
	"errors"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestRedirects: only the agents the move can place hear the new address
// (the new server's: its own manager; the one next to the old manager:
// the new server's address), with the generation raised by one; offline
// agents, agents without manager.redirect, refusals and timeouts are
// recorded with their class, never retried.
func TestRedirects(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	f.addEnvironment("env-elsewhere", "elsewhere", "")
	f.hub.setOnline("env-elsewhere", true)
	m, _ := f.create()
	f.enrollNewServer(m)
	m = f.svc.resolveTarget(f.ctx, f.move(m.ID))

	got := f.svc.sendRedirects(f.ctx, m)
	gen := f.inst.Generation + 1
	want := []domain.ManagerMoveRedirect{
		{EnvironmentID: "env-new", EnvironmentName: "192.168.1.20", Role: domain.RedirectNewServer, URL: NewServerManagerURL, Sent: true},
		{EnvironmentID: "env-old", EnvironmentName: "old-server", Role: domain.RedirectOldServer, URL: "http://192.168.1.20:8080", Sent: true},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("redirects %+v", got)
	}
	if _, ok := f.hub.redirects["env-elsewhere"]; ok {
		t.Fatal("an agent elsewhere was redirected")
	}
	if r := f.hub.redirects["env-old"]; r.Generation != gen {
		t.Fatalf("generation %d, want %d", r.Generation, gen)
	}

	for name, c := range map[string]struct {
		prepare func()
		class   string
	}{
		"offline":     {func() { f.hub.setOnline("env-old", false) }, RedirectOffline},
		"unsupported": {func() { f.hub.noRequest["env-old"] = true }, RedirectUnsupported},
		"refused":     {func() { f.hub.failWith["env-old"] = errors.New("error frame") }, RedirectRefused},
		"timeout":     {func() { f.hub.failWith["env-old"] = protocol.ErrRequestTimeout }, RedirectTimedOut},
		"deadline":    {func() { f.hub.failWith["env-old"] = context.DeadlineExceeded }, RedirectTimedOut},
	} {
		f.hub.online["env-old"], f.hub.noRequest, f.hub.failWith = true, map[string]bool{}, map[string]error{}
		c.prepare()
		got := f.svc.sendRedirects(f.ctx, m)
		if got[1].Sent || got[1].ErrorClass != c.class || !got[0].Sent {
			t.Errorf("%s: %+v", name, got)
		}
	}

	none := domain.ManagerMove{ID: "m"}
	if got := f.svc.sendRedirects(f.ctx, none); got == nil || len(got) != 0 {
		t.Fatalf("nothing to place: %+v", got)
	}
}
