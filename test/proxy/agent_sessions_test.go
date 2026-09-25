//go:build integration

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentconfig "github.com/neurekadev/dockyard/internal/agent/config"
	agentio "github.com/neurekadev/dockyard/internal/agent/containerio"
	"github.com/neurekadev/dockyard/internal/agent/containerio/ciotest"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/enroll"
	agentjobs "github.com/neurekadev/dockyard/internal/agent/jobs"
	agentres "github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/state"
	"github.com/neurekadev/dockyard/internal/agent/transport"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestTLSProxyAgentSessions (#27, #3, #8, #23) runs the real agent
// enrollment, transport and session client (internal/agent/{enroll,
// transport,session}; the Engine is scripted: enginefake for inventory,
// ciotest for exec) against the managers of e2e/compose.yaml, through each
// example reverse proxy from deploy/ (Caddy, Traefik, nginx) with the
// proxies' configuration files unchanged:
//
//   - a remote agent enrolls through the public HTTPS origin (trusting the
//     E2E CA via DOCKYARD_MANAGER_CA_FILE) with the token the UI created
//     (e2e/tests/topology.spec.ts) or one created through the API, and its
//     session comes online; a request (container list) is served through it;
//   - an exec WebSocket through the proxy echoes stdin, survives idleness
//     beyond the proxies' 60 s read timeouts (the manager's pings) while the
//     idle agent session keeps its one connection, and a new terminal
//     attaches after the client's connection was dropped;
//   - the live SSE stream resumes with Last-Event-ID through the proxy after
//     a disconnect and replays what was missed;
//   - the agent session reconnects through the proxy after its TCP
//     connection is cut, and after an agent restart (stored credential);
//   - a co-located agent enrolls on the manager's internal plain-HTTP URL
//     only with DOCKYARD_MANAGER_ALLOW_HTTP, and is flagged.
//
// Needs the running E2E stack; the extended e2e job sets:
//
//	E2E_PROXY_STACK=1  enable the test (skipped otherwise)
//	E2E_CA_FILE        the stack's CA (default e2e/.e2e-ca.crt)
//	E2E_ENROLLMENT_TOKEN_DIR  tokens the UI spec left (<proxy>.token)
//
// Optional: E2E_PROXY_ORIGINS (name=publicURL=internalURL,...; an http
// public URL runs the same checks against test/devstack without a proxy),
// E2E_UI_OWNER / E2E_UI_PASSWORD (the owner ui.spec.ts creates),
// E2E_IDLE (idle period, default 70s).
func TestTLSProxyAgentSessions(t *testing.T) {
	if os.Getenv("E2E_PROXY_STACK") != "1" {
		t.Skip("needs the E2E stack of e2e/compose.yaml (E2E_PROXY_STACK=1)")
	}
	caFile := os.Getenv("E2E_CA_FILE")
	if caFile == "" {
		caFile = filepath.Join("..", "..", "e2e", ".e2e-ca.crt")
	}
	caFile, _ = filepath.Abs(caFile)
	caPEM, err := os.ReadFile(caFile)
	if err != nil && os.Getenv("E2E_CA_FILE") != "" {
		t.Fatalf("read the E2E CA: %v", err)
	}
	idle := 70 * time.Second
	if v := os.Getenv("E2E_IDLE"); v != "" {
		if idle, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range stackOrigins(t) {
		t.Run(o.name, func(t *testing.T) {
			t.Parallel()
			runProxy(t, o, caFile, caPEM, idle)
		})
	}
}

type stackOrigin struct{ name, public, internal string }

func stackOrigins(t *testing.T) []stackOrigin {
	spec := os.Getenv("E2E_PROXY_ORIGINS")
	if spec == "" {
		spec = "caddy=https://localhost:8443=http://127.0.0.1:18080," +
			"traefik=https://localhost:8444=http://127.0.0.1:18081," +
			"nginx=https://localhost:8445=http://127.0.0.1:18082"
	}
	var out []stackOrigin
	for _, part := range strings.Split(spec, ",") {
		f := strings.Split(part, "=")
		if len(f) != 3 {
			t.Fatalf("E2E_PROXY_ORIGINS: %q is not name=publicURL=internalURL", part)
		}
		out = append(out, stackOrigin{f[0], f[1], f[2]})
	}
	return out
}

func runProxy(t *testing.T, o stackOrigin, caFile string, caPEM []byte, idle time.Duration) {
	// Longer than testutil.Context: the idle checks alone take over a minute.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	api := newAPIClient(t, o.public, caPEM)
	api.signIn(ctx, t)
	var caps struct {
		ManagerVersion string `json:"managerVersion"`
	}
	api.json(ctx, t, http.MethodGet, "/api/v1/capabilities", nil, http.StatusOK, &caps)

	// Engine IDs are unique per run (one agent per Engine and manager).
	run := time.Now().UTC().Format("20060102150405")
	// ---- remote agent through the public origin
	token, envName := enrollmentToken(ctx, t, api, o.name)
	remoteVars := map[string]string{agentconfig.EnvManagerURL: o.public, agentconfig.EnvManagerCAFile: caFile}
	if strings.HasPrefix(o.public, "http://") { // a local run against test/devstack, without a proxy
		remoteVars = map[string]string{agentconfig.EnvManagerURL: o.public, agentconfig.EnvManagerAllowHTTP: "true"}
	}
	remote := enrollAgent(ctx, t, agentSpec{
		name: o.name + "-remote", vars: remoteVars,
		token: token, engineID: fmt.Sprintf("E2E0:%s:REMOTE:%s", strings.ToUpper(o.name), run), version: caps.ManagerVersion,
	})
	if envName != "" && remote.enrolled.EnvironmentName != envName {
		t.Errorf("enrolled as %q, the UI enrollment named %q", remote.enrolled.EnvironmentName, envName)
	}
	envID := remote.enrolled.EnvironmentID
	remote.start(ctx, t)
	api.waitOnline(ctx, t, envID, true)
	api.expectContainer(ctx, t, envID, "web")
	if dials := remote.tracker.dials(); dials != 1 {
		t.Fatalf("%d session connections before idling, want 1", dials)
	}

	// ---- exec WebSocket and agent session idle beyond the proxy timeout
	t.Run("exec WebSocket survives idleness and reattaches after a dropped connection", func(t *testing.T) {
		term := api.openTerminal(ctx, t, envID, "web")
		term.echo(ctx, t, "before idle")
		// The point of the test: stay idle longer than the proxy's read
		// timeout (60 s in deploy/).
		time.Sleep(idle)
		term.echo(ctx, t, "after idle")
		term.drop()
		again := api.openTerminal(ctx, t, envID, "web")
		again.echo(ctx, t, "reattached")
		again.close()
		// The idle agent session kept its one connection (heartbeats).
		if dials := remote.tracker.dials(); dials != 1 {
			t.Fatalf("the agent reconnected %d times while idle", dials-1)
		}
		api.waitOnline(ctx, t, envID, true)
	})

	// ---- live SSE stream: resume after a disconnect
	t.Run("live stream resumes with Last-Event-ID through the proxy", func(t *testing.T) {
		live := api.openLive(ctx, t, "", "settings")
		hello := live.next(ctx, t, "hello")
		first := api.renameInstance(ctx, t, "E2E "+o.name+" one")
		ev := live.until(ctx, t, "invalidate", func(e sseEvent) bool { return strings.Contains(e.data, `"settings"`) })
		live.close()
		_ = api.renameInstance(ctx, t, "E2E "+o.name+" two")
		resumed := api.openLive(ctx, t, ev.id, "settings")
		h := resumed.next(ctx, t, "hello")
		if !strings.Contains(h.data, `"resumed":true`) {
			t.Fatalf("hello after resume %s (first hello %s, first rename rev %d)", h.data, hello.data, first)
		}
		replayed := resumed.until(ctx, t, "invalidate", func(e sseEvent) bool { return strings.Contains(e.data, `"settings"`) })
		if replayed.id == ev.id {
			t.Fatalf("replayed the event already seen (%s)", ev.id)
		}
		resumed.close()
	})

	// ---- agent session reconnects: network cut and restart
	// The manager's view of the session comes from the live stream's
	// agent events (offline, then online again once reconciled).
	agentEvent := func(status string) func(sseEvent) bool {
		return func(e sseEvent) bool {
			return strings.Contains(e.data, `"environmentId":"`+envID+`"`) && strings.Contains(e.data, `"status":"`+status+`"`)
		}
	}
	t.Run("agent session reconnects through the proxy after a cut connection", func(t *testing.T) {
		live := api.openLive(ctx, t, "", "")
		defer live.close()
		live.next(ctx, t, "hello")
		before := remote.tracker.dials()
		remote.tracker.cut()
		live.until(ctx, t, "agent", agentEvent("offline"))
		live.until(ctx, t, "agent", agentEvent("online"))
		if remote.tracker.dials() <= before {
			t.Fatal("no new session connection after the cut")
		}
		api.waitOnline(ctx, t, envID, true)
		api.expectContainer(ctx, t, envID, "web")
	})
	t.Run("agent session reconnects after an agent restart", func(t *testing.T) {
		live := api.openLive(ctx, t, "", "")
		defer live.close()
		live.next(ctx, t, "hello")
		remote.stop()
		live.until(ctx, t, "agent", agentEvent("offline"))
		remote.start(ctx, t)
		live.until(ctx, t, "agent", agentEvent("online"))
		api.waitOnline(ctx, t, envID, true)
		api.expectContainer(ctx, t, envID, "web")
	})
	remote.stop()

	// ---- co-located agent on the internal URL
	t.Run("co-located agent on the internal URL needs the opt-in and is flagged", func(t *testing.T) {
		if _, err := agentconfig.Load(envconfig.Map(map[string]string{agentconfig.EnvManagerURL: o.internal}, nil)); err == nil ||
			!strings.Contains(err.Error(), agentconfig.EnvManagerAllowHTTP) {
			t.Fatalf("plain HTTP without the opt-in: %v", err)
		}
		var created struct {
			Token string `json:"token"`
		}
		api.json(ctx, t, http.MethodPost, "/api/v1/agent-enrollments", map[string]any{"environmentName": o.name + "-colocated"},
			http.StatusCreated, &created)
		local := enrollAgent(ctx, t, agentSpec{
			name: o.name + "-colocated", vars: map[string]string{agentconfig.EnvManagerURL: o.internal, agentconfig.EnvManagerAllowHTTP: "true"},
			token: created.Token, engineID: fmt.Sprintf("E2E0:%s:LOCAL:%s", strings.ToUpper(o.name), run), version: caps.ManagerVersion,
		})
		local.start(ctx, t)
		defer local.stop()
		api.waitOnline(ctx, t, local.enrolled.EnvironmentID, true)
		var sys struct {
			Transport struct {
				PlainHTTP bool `json:"plainHttp"`
			} `json:"transport"`
		}
		api.json(ctx, t, http.MethodGet, "/api/v1/environments/"+local.enrolled.EnvironmentID+"/system", nil, http.StatusOK, &sys)
		if !sys.Transport.PlainHTTP {
			t.Fatal("the co-located agent's plain-HTTP URL is not flagged")
		}
	})
}

// enrollmentToken is the token e2e/tests/topology.spec.ts created in the
// UI for this proxy (with its preset environment name), or a new one.
func enrollmentToken(ctx context.Context, t *testing.T, api *apiClient, proxy string) (token, envName string) {
	t.Helper()
	if dir := os.Getenv("E2E_ENROLLMENT_TOKEN_DIR"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, proxy+".token")); err == nil {
			var f struct{ Token, EnvironmentName string }
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("token file of %s: %v", proxy, err)
			}
			return f.Token, f.EnvironmentName
		}
		t.Logf("no UI enrollment token for %s; creating one through the API", proxy)
	}
	var created struct {
		Token string `json:"token"`
	}
	api.json(ctx, t, http.MethodPost, "/api/v1/agent-enrollments", map[string]any{"environmentName": proxy + "-remote"}, http.StatusCreated, &created)
	return created.Token, proxy + "-remote"
}

// ---------------------------------------------------------------- agent

type agentSpec struct {
	name, token, engineID, version string
	vars                           map[string]string
}

type testAgent struct {
	spec     agentSpec
	tr       *transport.Transport
	st       *state.Store
	fe       *enginefake.Engine
	exec     *ciotest.Engine
	enrolled protocol.EnrollResponse
	tracker  *connTracker
	log      *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// enrollAgent enrolls through the agent's real transport and enrollment
// client and stores the credential like the runtime does.
func enrollAgent(ctx context.Context, t *testing.T, spec agentSpec) *testAgent {
	t.Helper()
	cfg, err := agentconfig.Load(envconfig.Map(spec.vars, nil))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := transport.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	install, err := st.InstallID()
	if err != nil {
		t.Fatal(err)
	}
	fe := enginefake.New(spec.engineID)
	fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "busybox:1.36"}, true)
	exec := ciotest.New()
	// A shell that echoes its input (the terminal round trips).
	exec.SetProcess(func(stdin io.Reader, stdout, _ io.Writer) int {
		_, _ = io.Copy(stdout, stdin)
		return 0
	})
	id := fe.Identity()
	res, err := enroll.Enroll(ctx, tr.HTTPClient(), tr.URL(protocol.EnrollPath), spec.token, "dockyard-agent/e2e", protocol.EnrollRequest{
		Protocol: protocol.Version, AgentVersion: spec.version, InstallID: install, Hostname: spec.name,
		Engine: protocol.EngineInfo{ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion,
			MinAPIVersion: id.MinAPIVersion, OS: id.OS, Arch: id.Arch},
	})
	if err != nil {
		t.Fatalf("enroll %s: %v", spec.name, err)
	}
	if err := st.SaveCredential(state.Credential{AgentID: res.AgentID, EnvironmentID: res.EnvironmentID, EnvironmentName: res.EnvironmentName,
		Credential: res.Credential, ManagerURL: tr.URL("")}); err != nil {
		t.Fatal(err)
	}
	a := &testAgent{spec: spec, tr: tr, st: st, fe: fe, exec: exec, enrolled: res, tracker: &connTracker{}, log: testutil.Logger(t).With("agent", spec.name)}
	t.Cleanup(a.stop)
	return a
}

// execEngine serves exec from ciotest for the fake Engine's containers.
type execEngine struct {
	*ciotest.Engine
	fe *enginefake.Engine
}

func (e execEngine) InspectContainer(ctx context.Context, id string) (engine.ContainerDetails, error) {
	return e.fe.InspectContainer(ctx, id)
}

// start runs the session client (a fresh one each time, as after an agent
// restart) with the stored credential.
func (a *testAgent) start(ctx context.Context, t *testing.T) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	clk := clock.Real()
	res := agentres.New(agentres.Options{Engine: func() engine.Engine { return a.fe }, Logger: a.log})
	cio := agentio.New(agentio.Options{Engine: func() agentio.Engine { return execEngine{a.exec, a.fe} }, Clock: clk, Logger: a.log})
	requests := res.Requests()
	maps.Copy(requests, cio.Requests())
	id := a.fe.Identity()
	info := protocol.EngineInfo{ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion, MinAPIVersion: id.MinAPIVersion,
		OS: id.OS, Arch: id.Arch}
	httpClient := a.tracker.wrap(a.tr.HTTPClient())
	client := session.New(session.Options{
		State: a.st, Clock: clk, Logger: a.log, URL: a.tr.WebSocketURL(protocol.SessionPath),
		DialOptions: func(h http.Header) *websocket.DialOptions {
			o := a.tr.DialOptions(h, protocol.Version)
			o.HTTPClient = httpClient
			return o
		},
		AgentVersion: a.spec.version, UserAgent: "dockyard-agent/e2e",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			return protocol.CapabilitiesPayload{AgentVersion: a.spec.version, Protocols: []string{protocol.Version}, OS: "linux", Arch: id.Arch,
				Engine: info, Commands: []string{}, Requests: []string{}, Streams: []string{}, Transport: a.tr.Info()}, true
		},
		Requests: requests,
		Streams:  cio.Streams(),
		Backoff:  session.Backoff{Min: 200 * time.Millisecond, Max: 2 * time.Second, ResetAfter: time.Minute},
	})
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	runner, err := agentjobs.New(runCtx, agentjobs.Options{StateDir: a.st.Dir(), Clock: clk, Logger: a.log, Sender: client})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client.SetRunner(runner)
	a.cancel, a.done = cancel, make(chan struct{})
	done := a.done
	go func() {
		defer close(done)
		_ = client.Run(runCtx)
		runner.Wait()
	}()
}

func (a *testAgent) stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		a.tracker.cut()
	}
}

// connTracker counts and cuts the agent's session connections (a network
// failure between the agent and the proxy).
type connTracker struct {
	mu    sync.Mutex
	n     int
	conns []net.Conn
}

func (c *connTracker) wrap(base *http.Client) *http.Client {
	tr := base.Transport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			c.mu.Lock()
			c.n++
			c.conns = append(c.conns, conn)
			c.mu.Unlock()
		}
		return conn, err
	}
	return &http.Client{Transport: tr, CheckRedirect: base.CheckRedirect}
}

func (c *connTracker) dials() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *connTracker) cut() {
	c.mu.Lock()
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// ---------------------------------------------------------------- API

type apiClient struct {
	base   string
	client *http.Client
	owner  string
	pass   string
}

func newAPIClient(t *testing.T, base string, caPEM []byte) *apiClient {
	t.Helper()
	roots := x509.NewCertPool()
	if strings.HasPrefix(base, "https://") && !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("E2E CA: no certificates (set E2E_CA_FILE)")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := http.DefaultTransport.(*http.Transport)
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	owner, pass := os.Getenv("E2E_UI_OWNER"), os.Getenv("E2E_UI_PASSWORD")
	if owner == "" {
		owner, pass = "owner", "dockyard-e2e-owner-passphrase"
	}
	return &apiClient{base: base, client: &http.Client{Transport: tr, Jar: jar, Timeout: 60 * time.Second}, owner: owner, pass: pass}
}

// do sends a request as a browser on the public origin would (Origin for
// the cross-site protection of cookie-authenticated unsafe methods).
func (c *apiClient) do(ctx context.Context, t *testing.T, method, path string, body any, header http.Header) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", c.base)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (c *apiClient) json(ctx context.Context, t *testing.T, method, path string, body any, want int, out any) http.Header {
	t.Helper()
	resp := c.do(ctx, t, method, path, body, nil)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return resp.Header
}

// signIn signs in as the E2E owner, completing first-run setup with it
// when ui.spec.ts has not.
func (c *apiClient) signIn(ctx context.Context, t *testing.T) {
	t.Helper()
	var status struct {
		SetupComplete bool `json:"setupComplete"`
	}
	c.json(ctx, t, http.MethodGet, "/api/v1/setup/status", nil, http.StatusOK, &status)
	if !status.SetupComplete {
		resp := c.do(ctx, t, http.MethodPost, "/api/v1/setup/owner", map[string]any{"username": c.owner, "password": c.pass}, nil)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
			t.Fatalf("setup: %d", resp.StatusCode)
		}
	}
	var s struct {
		State string `json:"state"`
	}
	c.json(ctx, t, http.MethodPost, "/api/v1/auth/session", map[string]any{"username": c.owner, "password": c.pass}, http.StatusOK, &s)
	if s.State != "authenticated" {
		t.Fatalf("sign-in state %q (the E2E owner must sign in with a password alone)", s.State)
	}
}

// waitOnline polls the environment until its online flag is want (agent
// sessions and their reconciliation are asynchronous).
func (c *apiClient) waitOnline(ctx context.Context, t *testing.T, envID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		var env struct {
			Online bool `json:"online"`
		}
		c.json(ctx, t, http.MethodGet, "/api/v1/environments/"+envID, nil, http.StatusOK, &env)
		if env.Online == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("environment %s online=%v, want %v", envID, env.Online, want)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// expectContainer lists the environment's containers: an agent request
// answered through the session.
func (c *apiClient) expectContainer(ctx context.Context, t *testing.T, envID, name string) {
	t.Helper()
	var page struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	c.json(ctx, t, http.MethodGet, "/api/v1/environments/"+envID+"/containers", nil, http.StatusOK, &page)
	for _, it := range page.Items {
		if it.Name == name {
			return
		}
	}
	t.Fatalf("container %s not listed: %+v", name, page.Items)
}

// renameInstance changes the instance name (an audited settings change
// the live stream announces) and returns the new revision.
func (c *apiClient) renameInstance(ctx context.Context, t *testing.T, name string) int64 {
	t.Helper()
	var s struct {
		Revision int64 `json:"revision"`
	}
	h := c.json(ctx, t, http.MethodGet, "/api/v1/settings", nil, http.StatusOK, &s)
	resp := c.do(ctx, t, http.MethodPatch, "/api/v1/settings", map[string]any{"name": name}, http.Header{"If-Match": {h.Get("ETag")}})
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &s) != nil {
		t.Fatalf("rename: %d %s", resp.StatusCode, b)
	}
	return s.Revision
}

// ---------------------------------------------------------------- exec

type terminal struct {
	c   *websocket.Conn
	out chan []byte
}

func (c *apiClient) openTerminal(ctx context.Context, t *testing.T, envID, container string) *terminal {
	t.Helper()
	var s struct {
		StreamURL   string `json:"streamUrl"`
		Subprotocol string `json:"subprotocol"`
		Ticket      string `json:"ticket"`
	}
	c.json(ctx, t, http.MethodPost, "/api/v1/environments/"+envID+"/containers/"+container+"/exec-sessions",
		map[string]any{"command": []string{"/bin/cat"}, "tty": false}, http.StatusCreated, &s)
	u := "ws" + strings.TrimPrefix(c.base, "http") + s.StreamURL
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c.client, HTTPHeader: http.Header{"Origin": {c.base}},
		Subprotocols: []string{s.Subprotocol, "dockyard.ticket." + s.Ticket}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("attach through the proxy: %v", err)
	}
	if conn.Subprotocol() != s.Subprotocol {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	term := &terminal{c: conn, out: make(chan []byte, 64)}
	// Keep reading so control frames (the manager's pings) are answered
	// while the terminal is idle.
	go func() {
		defer close(term.out)
		for {
			typ, b, err := conn.Read(context.WithoutCancel(ctx))
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary && len(b) > 0 {
				term.out <- b
			}
		}
	}()
	return term
}

func (m *terminal) echo(ctx context.Context, t *testing.T, text string) {
	t.Helper()
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.c.Write(wctx, websocket.MessageBinary, append([]byte{0}, text+"\n"...)); err != nil {
		t.Fatalf("stdin: %v", err)
	}
	var got []byte
	for !bytes.Contains(got, []byte(text)) {
		select {
		case b, ok := <-m.out:
			if !ok {
				t.Fatalf("terminal closed before echoing %q (got %q)", text, got)
			}
			if b[0] != 1 {
				t.Fatalf("output channel %d", b[0])
			}
			got = append(got, b[1:]...)
		case <-wctx.Done():
			t.Fatalf("no echo of %q through the proxy (got %q)", text, got)
		}
	}
}

// drop ends the connection without a close handshake (a lost network).
func (m *terminal) drop() { _ = m.c.CloseNow() }

func (m *terminal) close() { _ = m.c.Close(websocket.StatusNormalClosure, "") }

// ---------------------------------------------------------------- SSE

type sseEvent struct{ event, id, data string }

type sseStream struct {
	resp   *http.Response
	events chan sseEvent
}

// openLive opens GET /live/stream (topics "" = all) through the proxy.
func (c *apiClient) openLive(ctx context.Context, t *testing.T, lastEventID, topics string) *sseStream {
	t.Helper()
	h := http.Header{"Accept": {"text/event-stream"}}
	if lastEventID != "" {
		h.Set("Last-Event-ID", lastEventID)
	}
	path := "/api/v1/live/stream"
	if topics != "" {
		path += "?topics=" + topics
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h
	// No client timeout on a stream.
	resp, err := (&http.Client{Transport: c.client.Transport, Jar: c.client.Jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("live stream: %d %s", resp.StatusCode, b)
	}
	s := &sseStream{resp: resp, events: make(chan sseEvent, 64)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event != "" || ev.data != "" {
					s.events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return s
}

func (s *sseStream) next(ctx context.Context, t *testing.T, event string) sseEvent {
	t.Helper()
	return s.until(ctx, t, event, func(sseEvent) bool { return true })
}

// until returns the first event of the type that matches; the proxies must
// deliver it without buffering (well within the wait).
func (s *sseStream) until(ctx context.Context, t *testing.T, event string, match func(sseEvent) bool) sseEvent {
	t.Helper()
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var seen []string
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				t.Fatalf("stream ended waiting for %s (saw %v)", event, seen)
			}
			if ev.event == event && match(ev) {
				return ev
			}
			seen = append(seen, ev.event)
		case <-wctx.Done():
			t.Fatalf("no %s event through the proxy (saw %v)", event, seen)
		}
	}
}

func (s *sseStream) close() { _ = s.resp.Body.Close() }
