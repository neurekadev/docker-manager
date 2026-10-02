package managermove

import (
	"net/netip"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const movedURL = "http://192.168.1.20:8080"

func transportOf(u string) protocol.TransportInfo {
	return protocol.TransportInfo{ManagerURL: u, PlainHTTP: u[:5] == "http:"}
}

// TestSecureStep: the agent next to the old manager that still dials the
// move's plain-HTTP address is sent the public URL once a request reached
// it there (and only if it accepts that redirect); an agent whose
// credential crossed in clear is rotated once its session is HTTPS or on
// the new server's Docker network; nothing else is touched.
func TestSecureStep(t *testing.T) {
	now := testutil.Epoch
	oldServer := domain.ManagerMoveRedirect{EnvironmentID: "env-old", Role: domain.RedirectOldServer, URL: movedURL, Sent: true}
	newServer := domain.ManagerMoveRedirect{EnvironmentID: "env-new", Role: domain.RedirectNewServer, URL: NewServerManagerURL, Sent: true}
	returned, rotated := oldServer, newServer
	returned.ReturnedAt = &now
	rotated.RotatedAt = &now
	notSent := oldServer
	notSent.Sent, notSent.ErrorClass = false, RedirectOffline
	newNotSent := newServer
	newNotSent.Sent = false
	for name, c := range map[string]struct {
		r                  domain.ManagerMoveRedirect
		url                string
		reached, canReturn bool
		want               secureAction
	}{
		"old server on the move's address, public URL reached": {oldServer, movedURL, true, true, secureReturn},
		"public URL not reached yet":                           {oldServer, movedURL, false, true, secureNone},
		"older agent":                                          {oldServer, movedURL, true, false, secureNone},
		"already sent the public URL":                          {returned, movedURL, true, true, secureNone},
		"old server over https":                                {returned, publicURL, true, true, secureRotate},
		"old server set to https by hand":                      {oldServer, publicURL, false, false, secureRotate},
		"old server on another plain-HTTP address":             {oldServer, "http://192.168.1.99:8080", true, true, secureNone},
		"redirect not sent: its credential never crossed":      {notSent, publicURL, true, true, secureNone},
		"new server on its Docker network":                     {newServer, NewServerManagerURL, false, false, secureRotate},
		"new server without the redirect, set by hand":         {newNotSent, NewServerManagerURL, false, false, secureRotate},
		"new server still on the old server's address":         {newServer, "http://192.168.1.10:8080", true, true, secureNone},
		"new server already rotated":                           {rotated, NewServerManagerURL, true, true, secureNone},
	} {
		if got := secureStep(c.r, transportOf(c.url), c.reached, c.canReturn); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// arrivedFixture stores an arrived move whose old manager redirected the
// new server's agent (env-new) and the agent next to it (env-old), both
// connected here over the addresses the move gave.
func arrivedFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t, testutil.FakeClock(), nil)
	now := f.clk.Now().UTC()
	for _, env := range []string{"env-new", "env-old"} {
		f.addEnvironment(env, env, "")
		agentID := "agent-" + env[4:]
		if _, err := f.db.ExecContext(f.ctx, "UPDATE environments SET agent_id = ? WHERE id = ?", agentID, env); err != nil {
			t.Fatal(err)
		}
		f.hub.setOnline(env, true)
	}
	f.setTransport("env-new", NewServerManagerURL)
	f.setTransport("env-old", movedURL)
	m := domain.ManagerMove{ID: "move-arrived", State: domain.MoveArrived, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now, ArrivedAt: &now,
		SourceURL: "http://192.168.1.10:8080", SourceEnvironmentID: "env-old", TargetEnvironmentID: "env-new", UpdatedAt: now,
		Redirects: []domain.ManagerMoveRedirect{
			{EnvironmentID: "env-new", Role: domain.RedirectNewServer, URL: NewServerManagerURL, Sent: true},
			{EnvironmentID: "env-old", Role: domain.RedirectOldServer, URL: movedURL, Sent: true},
		}}
	if err := store.InsertManagerMove(f.ctx, f.db, &m, ""); err != nil {
		t.Fatal(err)
	}
	return f, m.ID
}

// setTransport makes env's agent report that it dials u.
func (f *fixture) setTransport(env, u string) {
	f.enroll.mu.Lock()
	defer f.enroll.mu.Unlock()
	agentID := "agent-" + env[4:]
	caps := `{"transport":{"managerUrl":"` + u + `","plainHttp":` + map[bool]string{true: "true", false: "false"}[u[:5] == "http:"] + `}}`
	f.enroll.agents[agentID] = domain.Agent{ID: agentID, EnvironmentID: env, Status: domain.AgentActive, Capabilities: caps}
}

func (f *fixture) rotated() []string {
	f.enroll.mu.Lock()
	defer f.enroll.mu.Unlock()
	return slices.Clone(f.enroll.rotated)
}

func (f *fixture) redirectOf(id, env string) domain.ManagerMoveRedirect {
	f.t.Helper()
	for _, r := range f.move(id).Redirects {
		if r.EnvironmentID == env {
			return r
		}
	}
	f.t.Fatalf("no redirect for %s", env)
	return domain.ManagerMoveRedirect{}
}

// TestBackToHTTPS: after the arrival the new server's agent (on its
// Docker network) gets a new credential at once; the agent next to the
// old manager is sent the public URL at this manager's generation only
// after a request reached the manager over HTTPS at that URL, once, and
// gets a new credential when it is back over HTTPS. Each step happens
// once.
func TestBackToHTTPS(t *testing.T) {
	f, id := arrivedFixture(t)

	f.svc.secureDue(f.ctx)
	if got := f.rotated(); !slices.Equal(got, []string{"agent-new"}) {
		t.Fatalf("rotated %v", got)
	}
	if r := f.redirectOf(id, "env-new"); r.RotatedAt == nil {
		t.Fatalf("new server's rotation not recorded: %+v", r)
	}
	if _, ok := f.hub.redirects["env-old"]; ok {
		t.Fatal("the old server's agent was redirected before the public URL reached this manager")
	}

	// The owner's dashboard over plain HTTP proves nothing; over HTTPS at
	// the public URL it does.
	plain := requestinfo.With(f.ctx, requestinfo.Info{Scheme: "http", Host: "192.168.1.20:8080", ClientIP: netip.MustParseAddr("192.168.1.5")})
	if _, err := f.svc.Current(plain); err != nil {
		t.Fatal(err)
	}
	if f.svc.publicReached.Load() {
		t.Fatal("a plain-HTTP request counted as the public URL")
	}
	public := requestinfo.With(f.ctx, requestinfo.Info{Scheme: "https", Host: "docker.example.com", ClientIP: netip.MustParseAddr("192.168.1.5"),
		TrustedPeer: true})
	if _, err := f.svc.Current(public); err != nil {
		t.Fatal(err)
	}
	if !f.svc.publicReached.Load() {
		t.Fatal("the HTTPS request at the public URL was not noted")
	}

	f.svc.secureDue(f.ctx)
	want := protocol.ManagerRedirectInput{URL: publicURL, Generation: max(f.inst.Generation, 1)}
	if got := f.hub.redirects["env-old"]; got != want {
		t.Fatalf("redirect %+v, want %+v", got, want)
	}
	if r := f.redirectOf(id, "env-old"); r.ReturnedAt == nil || r.RotatedAt != nil {
		t.Fatalf("old server's redirect %+v", r)
	}
	// Not again while the agent has not reconnected yet, and no rotation
	// over plain HTTP.
	delete(f.hub.redirects, "env-old")
	f.svc.secureDue(f.ctx)
	if _, ok := f.hub.redirects["env-old"]; ok || len(f.rotated()) != 1 {
		t.Fatalf("repeated: redirects %v, rotated %v", f.hub.redirects, f.rotated())
	}

	// Back over HTTPS: its credential is rotated, once.
	f.setTransport("env-old", publicURL)
	f.svc.secureDue(f.ctx)
	f.svc.secureDue(f.ctx)
	if got := f.rotated(); !slices.Equal(got, []string{"agent-new", "agent-old"}) {
		t.Fatalf("rotated %v", got)
	}
	if r := f.redirectOf(id, "env-old"); r.RotatedAt == nil || r.ReturnedAt == nil {
		t.Fatalf("old server's redirect %+v", r)
	}
	if m := f.move(id); m.State != domain.MoveArrived || m.SourceURL != "http://192.168.1.10:8080" {
		t.Fatalf("move changed beyond its redirects: %+v", m)
	}
}

// TestBackToHTTPSNotApplicable: offline agents wait, an agent without the
// feature keeps the move's address, and a plain-http public URL (local
// development) changes nothing at all.
func TestBackToHTTPSNotApplicable(t *testing.T) {
	f, id := arrivedFixture(t)
	f.svc.publicReached.Store(true)
	f.hub.setOnline("env-new", false)
	f.hub.oldAgent["env-old"] = true
	f.svc.secureDue(f.ctx)
	if len(f.rotated()) != 0 || len(f.hub.redirects) != 0 {
		t.Fatalf("rotated %v, redirects %v", f.rotated(), f.hub.redirects)
	}
	if r := f.redirectOf(id, "env-old"); r.ReturnedAt != nil {
		t.Fatalf("recorded %+v", r)
	}

	dev, _ := url.Parse("http://localhost:8080")
	f.svc.opts.PublicURL = dev
	f.hub.setOnline("env-new", true)
	f.hub.oldAgent = map[string]bool{}
	f.svc.secureDue(f.ctx)
	if len(f.rotated()) != 0 || len(f.hub.redirects) != 0 {
		t.Fatalf("local development: rotated %v, redirects %v", f.rotated(), f.hub.redirects)
	}
}
