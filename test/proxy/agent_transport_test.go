//go:build integration

// Package proxy holds Docker-backed topology tests (#27) run by the extended
// e2e job (-run '^TestTLSProxy'). The browser-side proxy tests are the
// Playwright specs in e2e/tests/proxy.spec.ts.
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	agentconfig "github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/transport"
	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/manager/authsep"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
	"github.com/neurekadev/dockyard/internal/manager/server"
	"github.com/neurekadev/dockyard/internal/manager/server/ws"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestTLSProxyAgentTransport runs the manager's HTTP stack in the test
// process behind a real TLS-terminating proxy (Caddy with a private CA) and
// connects agents the two supported ways: a remote agent through the public
// HTTPS origin, trusting the private CA via DOCKYARD_MANAGER_CA_FILE, and a
// co-located agent on the internal plain-HTTP URL with the explicit opt-in.
// The session handler here is a stand-in that authenticates the bearer
// credential and echoes one frame (transport only); the real manager
// session behind each proxy of deploy/ is TestTLSProxyAgentSessions.
func TestTLSProxyAgentTransport(t *testing.T) {
	ctx := testutil.Context(t)
	cred, err := authsep.NewAgentCredential()
	if err != nil {
		t.Fatal(err)
	}
	session := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != server.AgentBasePath+"/session" {
			server.AgentFailure(w, r, http.StatusBadRequest)
			return
		}
		if tok, ok := authsep.BearerToken(r.Header); !ok || tok != cred {
			server.AgentFailure(w, r, http.StatusUnauthorized)
			return
		}
		info, _ := requestinfo.From(r.Context())
		c, err := ws.Accept(w, r, ws.Options{Subprotocols: []string{protocol.Version}, RequireSubprotocol: true})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		_, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		reply := fmt.Sprintf("%s|%s|%t", msg, info.Scheme, info.TrustedPeer)
		_ = c.Write(ctx, websocket.MessageText, []byte(reply))
	})
	private := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("127.0.0.0/8"),
	}
	srv, err := server.New(server.Options{
		Logger: testutil.Logger(t),
		UI:     fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>DockYard</title>")}},
		Agent:  session, TrustedProxies: private,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := httptest.NewServer(srv.Handler)
	t.Cleanup(manager.Close)
	_, portStr, _ := net.SplitHostPort(manager.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	proxy := testharness.StartTLSProxy(t, testharness.TLSProxyOptions{
		Upstream:        fmt.Sprintf("host.testcontainers.internal:%d", port),
		HostAccessPorts: []int{port},
	})
	caFile := filepath.Join(t.TempDir(), "proxy-ca.pem")
	if err := os.WriteFile(caFile, proxy.RootCAPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	dial := func(t *testing.T, vars map[string]string, token string) (string, error) {
		t.Helper()
		cfg, err := agentconfig.Load(envconfig.Map(vars, nil))
		if err != nil {
			return "", err
		}
		tr, err := transport.New(cfg)
		if err != nil {
			return "", err
		}
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(dctx, tr.WebSocketURL(server.AgentBasePath+"/session"), //nolint:bodyclose // the connection owns the upgrade response
			tr.DialOptions(http.Header{"Authorization": {"Bearer " + token}}, protocol.Version))
		if err != nil {
			return "", err
		}
		defer func() { _ = c.CloseNow() }()
		if err := c.Write(dctx, websocket.MessageText, []byte("hello")); err != nil {
			return "", err
		}
		_, msg, err := c.Read(dctx)
		return string(msg), err
	}

	t.Run("remote agent through the public origin with a private CA", func(t *testing.T) {
		got, err := dial(t, map[string]string{agentconfig.EnvManagerURL: proxy.URL, agentconfig.EnvManagerCAFile: caFile}, cred)
		if err != nil {
			t.Fatal(err)
		}
		// The manager saw https via the trusted proxy.
		if got != "hello|https|true" {
			t.Fatalf("reply %q", got)
		}
	})
	t.Run("remote agent without the CA is refused by certificate validation", func(t *testing.T) {
		_, err := dial(t, map[string]string{agentconfig.EnvManagerURL: proxy.URL}, cred)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrong credential gets the generic failure through the proxy", func(t *testing.T) {
		_, err := dial(t, map[string]string{agentconfig.EnvManagerURL: proxy.URL, agentconfig.EnvManagerCAFile: caFile}, "dya_wrong")
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("co-located agent on the internal URL needs the opt-in", func(t *testing.T) {
		if _, err := dial(t, map[string]string{agentconfig.EnvManagerURL: manager.URL}, cred); err == nil ||
			!strings.Contains(err.Error(), agentconfig.EnvManagerAllowHTTP) {
			t.Fatalf("plain http without opt-in: %v", err)
		}
		got, err := dial(t, map[string]string{agentconfig.EnvManagerURL: manager.URL, agentconfig.EnvManagerAllowHTTP: "true"}, cred)
		if err != nil {
			t.Fatal(err)
		}
		if got != "hello|http|true" { // 127.0.0.1 is in the trusted list here, but sends no X-Forwarded-Proto
			t.Fatalf("reply %q", got)
		}
	})
	t.Run("agent credential cannot call the public API through the proxy", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+cred)
		resp, err := proxy.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
}
