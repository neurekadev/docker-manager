package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/config"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/agent/transport"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// redirectAgent builds an agent over stateDir with cfg and prepares what
// Run prepares before the first session: the state store and the
// transport resolved from DOCKER_AGENT_MANAGER_URL and manager.json.
func redirectAgent(t *testing.T, cfg config.Config) (*Agent, *testutil.LogBuffer) {
	t.Helper()
	log, logs := testutil.CaptureLogger()
	a, err := New(Options{Config: cfg, Logger: log, Clock: testutil.FakeClock(), Geteuid: func() int { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	if a.store, err = state.Open(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	a.guard.SetGenerations(a.store)
	configured, err := transport.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.setTransport(a.resolveTransport(configured))
	return a, logs
}

func redirect(t *testing.T, a *Agent, rawURL string, generation int64) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(protocol.ManagerRedirectInput{URL: rawURL, Generation: generation})
	return a.opts.Requests[protocol.ReqManagerRedirect](testutil.Context(t), raw)
}

// follow sends a redirect that must be followed.
func follow(t *testing.T, a *Agent, rawURL string, generation int64) {
	t.Helper()
	_, err := redirect(t, a, rawURL, generation)
	var end *session.EndSessionError
	if !errors.As(err, &end) || end.Err != nil {
		t.Fatalf("redirect to %s (generation %d) not followed: %v", rawURL, generation, err)
	}
}

func managerState(t *testing.T, a *Agent) state.ManagerState {
	t.Helper()
	ms, err := a.store.ManagerState()
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func sessionURL(a *Agent) string {
	u, _ := a.sessionTarget(http.Header{})
	return u
}

// TestManagerRedirectFollowed (#35, manager moves): a valid
// manager.redirect is written to manager.json (address and raised
// generation) before the answer, the answer is a response (not an error)
// that ends the session with a reconnectable close code, and the next
// dial goes to the new address, over plain http although
// DOCKER_AGENT_MANAGER_ALLOW_HTTP is not set. The credential stays.
func TestManagerRedirectFollowed(t *testing.T) {
	dir := t.TempDir()
	a, logs := redirectAgent(t, testConfig(dir))
	cred := state.Credential{AgentID: "a1", EnvironmentID: "e1", Credential: "dya_c1_secret", ManagerURL: "https://docker.example.com"}
	if err := a.store.SaveCredential(cred); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveManagerGeneration(1); err != nil {
		t.Fatal(err)
	}
	if u := sessionURL(a); u != "wss://docker.example.com"+protocol.SessionPath {
		t.Fatalf("before the redirect: %s", u)
	}

	out, err := redirect(t, a, "http://192.0.2.10:8080/", 2)
	var end *session.EndSessionError
	if out != nil || !errors.As(err, &end) || end.Err != nil || end.Output == nil {
		t.Fatalf("answer %+v %v", out, err)
	}
	if end.Code != protocol.CloseGoingAway || !protocol.ReconnectAllowed(end.Code) {
		t.Fatalf("close code %d", end.Code)
	}
	ms := managerState(t, a)
	if ms.Generation != 2 || ms.Redirect == nil || ms.Redirect.URL != "http://192.0.2.10:8080" ||
		ms.Redirect.Replaces != "https://docker.example.com" || !ms.Redirect.At.Equal(testutil.Epoch) {
		t.Fatalf("manager state %+v %+v", ms, ms.Redirect)
	}
	if u := sessionURL(a); u != "ws://192.0.2.10:8080"+protocol.SessionPath {
		t.Fatalf("next dial %s", u)
	}
	a.mu.RLock()
	ti := a.tinfo
	a.mu.RUnlock()
	if ti.ManagerURL != "http://192.0.2.10:8080" || !ti.PlainHTTP || ti.Validate() != nil {
		t.Fatalf("capabilities transport %+v", ti)
	}
	if c, err := a.store.Credential(); err != nil || c == nil || *c != cred {
		t.Fatalf("credential changed: %+v %v", c, err)
	}
	if err := a.writeHealthFile(testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if h := readHealth(t, dir); h.ManagerURL != "http://192.0.2.10:8080" || h.ManagerURLSource != ManagerURLFromMove {
		t.Fatalf("health %+v", h)
	}
	// The old manager is refused from now on (its generation is lower).
	if err := a.guard.AcceptGeneration("", 1); err == nil {
		t.Fatal("the old manager's generation is still accepted")
	}
	if !strings.Contains(logs.String(), "following it to its new address") || strings.Contains(logs.String(), "dya_") {
		t.Fatalf("log: %s", logs.String())
	}

	// The same redirect again (the answer was lost) is answered the same
	// way; a later move raises it further.
	follow(t, a, "http://192.0.2.10:8080", 2)
	follow(t, a, "https://Docker.Example.com", 3)
	if ms := managerState(t, a); ms.Generation != 3 || ms.Redirect.URL != "https://docker.example.com" ||
		ms.Redirect.Replaces != "https://docker.example.com" {
		t.Fatalf("after the second move %+v %+v", ms, ms.Redirect)
	}
	if u := sessionURL(a); u != "wss://docker.example.com"+protocol.SessionPath {
		t.Fatalf("https redirect dial %s", u)
	}
}

// TestManagerRedirectRefused: malformed input, a bad address or a
// generation that is not newer are answered with an error (the session
// stays) and change nothing.
func TestManagerRedirectRefused(t *testing.T) {
	a, _ := redirectAgent(t, testConfig(t.TempDir()))
	follow(t, a, "http://192.0.2.10:8080", 3)
	before := managerState(t, a)
	check := func(name string, err error, code string) {
		t.Helper()
		var end *session.EndSessionError
		var he *session.HandlerError
		if errors.As(err, &end) || !errors.As(err, &he) || he.Code != code {
			t.Errorf("%s: %v (want %s)", name, err, code)
		}
		if ms := managerState(t, a); ms.Generation != before.Generation || *ms.Redirect != *before.Redirect {
			t.Errorf("%s changed the state: %+v %+v", name, ms, ms.Redirect)
		}
		if u := sessionURL(a); u != "ws://192.0.2.10:8080"+protocol.SessionPath {
			t.Errorf("%s changed the dialed address: %s", name, u)
		}
	}
	_, err := a.opts.Requests[protocol.ReqManagerRedirect](testutil.Context(t), json.RawMessage(`{"url":`))
	check("malformed", err, protocol.CodeInvalidFrame)
	for _, g := range []int64{0, -1} {
		_, err := redirect(t, a, "http://192.0.2.20:8080", g)
		check(fmt.Sprintf("generation %d", g), err, protocol.CodeInvalidArgument)
	}
	for _, bad := range []string{"", "ftp://m", "http://user:pw@m:8080", "http://m:8080/agent", "http://m:8080?x", "//m:8080", "m:8080"} {
		_, err := redirect(t, a, bad, 4)
		check("url "+bad, err, protocol.CodeInvalidArgument)
	}
	// Equal (with another address) or lower generation: conflict.
	_, err = redirect(t, a, "http://192.0.2.20:8080", 3)
	check("equal generation", err, protocol.CodeConflict)
	_, err = redirect(t, a, "http://192.0.2.20:8080", 2)
	check("lower generation", err, protocol.CodeConflict)
}

// TestManagerRedirectAfterRestart: a restarted agent dials the persisted
// address while DOCKER_AGENT_MANAGER_URL is unchanged; once the operator
// changes the variable, its value wins and the redirect is forgotten (the
// generation stays).
func TestManagerRedirectAfterRestart(t *testing.T) {
	dir := t.TempDir()
	a, _ := redirectAgent(t, testConfig(dir))
	follow(t, a, "http://192.0.2.10:8080", 2)

	b, logs := redirectAgent(t, testConfig(dir))
	if u := sessionURL(b); u != "ws://192.0.2.10:8080"+protocol.SessionPath || !b.currentTransport().Redirected() {
		t.Fatalf("restart dials %s", u)
	}
	if strings.Contains(logs.String(), "forgetting") {
		t.Fatalf("redirect dropped: %s", logs.String())
	}

	changed := testConfig(dir)
	changed.ManagerURL = &url.URL{Scheme: "https", Host: "docker.example.org"}
	c, logs := redirectAgent(t, changed)
	if u := sessionURL(c); u != "wss://docker.example.org"+protocol.SessionPath || c.currentTransport().Redirected() {
		t.Fatalf("changed configuration dials %s", u)
	}
	if ms := managerState(t, c); ms.Redirect != nil || ms.Generation != 2 {
		t.Fatalf("after the change %+v %+v", ms, ms.Redirect)
	}
	if !strings.Contains(logs.String(), "DOCKER_AGENT_MANAGER_URL was changed") {
		t.Fatalf("log: %s", logs.String())
	}
	if err := c.writeHealthFile(testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if h := readHealth(t, dir); h.ManagerURL != "https://docker.example.org" || h.ManagerURLSource != ManagerURLFromConfig {
		t.Fatalf("health %+v", h)
	}
}
