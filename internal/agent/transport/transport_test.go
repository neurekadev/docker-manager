package transport

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/agent/config"
	"github.com/neurekadev/docker-manager/internal/envconfig"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// tlsManager is an httptest TLS server standing in for the manager (or a
// TLS-terminating proxy in front of it) with a private CA.
func tlsManager(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/session":
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"docker-manager.agent/v1"}})
			if err != nil {
				return
			}
			defer func() { _ = c.CloseNow() }()
			typ, msg, err := c.Read(r.Context())
			if err == nil {
				_ = c.Write(r.Context(), typ, append([]byte(r.Header.Get("Authorization")+"|"), msg...))
			}
		case "/redirect":
			http.Redirect(w, r, "http://"+r.Host+"/agent/v1/enroll", http.StatusTemporaryRedirect)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, caFile
}

func load(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	cfg, err := config.Load(envconfig.Map(vars, nil))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestHTTPSWithCustomCA(t *testing.T) {
	srv, caFile := tlsManager(t)
	tr, err := New(load(t, map[string]string{config.EnvManagerURL: srv.URL, config.EnvManagerCAFile: caFile}))
	if err != nil {
		t.Fatal(err)
	}
	if info := tr.Info(); info.PlainHTTP || !info.CustomCA || info.Flagged() || info.ManagerURL != srv.URL || info.Validate() != nil {
		t.Fatalf("info %+v", info)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodGet, tr.URL("/agent/v1/enroll"), nil)
	resp, err := tr.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Fatalf("status %d tls %v", resp.StatusCode, resp.TLS != nil)
	}

	// The session WebSocket uses the same TLS trust.
	if u := tr.WebSocketURL("/agent/v1/session"); !strings.HasPrefix(u, "wss://") {
		t.Fatalf("ws url %s", u)
	}
	c, _, err := websocket.Dial(ctx(t), tr.WebSocketURL("/agent/v1/session"), //nolint:bodyclose // the connection owns the upgrade response
		tr.DialOptions(http.Header{"Authorization": {"Bearer dya_fake"}}, "docker-manager.agent/v1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	if c.Subprotocol() != "docker-manager.agent/v1" {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	if err := c.Write(ctx(t), websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.Read(ctx(t)); err != nil || string(msg) != "Bearer dya_fake|hello" {
		t.Fatalf("echo %q %v", msg, err)
	}
}

func TestHTTPSRejectsUnknownCA(t *testing.T) {
	srv, _ := tlsManager(t)
	tr, err := New(load(t, map[string]string{config.EnvManagerURL: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodGet, tr.URL("/"), nil)
	resp, err := tr.HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("unexpected error %v", err)
	}
	if _, _, err := websocket.Dial(ctx(t), tr.WebSocketURL("/agent/v1/session"), tr.DialOptions(nil)); err == nil { //nolint:bodyclose // the connection owns the upgrade response
		t.Fatal("untrusted certificate accepted for the WebSocket")
	}
}

func TestRedirectsAreRefused(t *testing.T) {
	srv, caFile := tlsManager(t)
	tr, err := New(load(t, map[string]string{config.EnvManagerURL: srv.URL, config.EnvManagerCAFile: caFile}))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodPost, tr.URL("/redirect"), strings.NewReader("token"))
	resp, err := tr.HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect followed: %v", err)
	}
}

func TestPlainHTTPRequiresOptInAndIsFlagged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	if _, err := config.Load(envconfig.Map(map[string]string{config.EnvManagerURL: srv.URL}, nil)); err == nil ||
		!strings.Contains(err.Error(), config.EnvManagerAllowHTTP) {
		t.Fatalf("plain http without opt-in: %v", err)
	}
	tr, err := New(load(t, map[string]string{config.EnvManagerURL: srv.URL, config.EnvManagerAllowHTTP: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if info := tr.Info(); !info.PlainHTTP || !info.Flagged() || info.Validate() != nil {
		t.Fatalf("plain http not flagged: %+v", info)
	}
	if u := tr.WebSocketURL("/agent/v1/session"); !strings.HasPrefix(u, "ws://") {
		t.Fatalf("ws url %s", u)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodGet, tr.URL("/"), nil)
	resp, err := tr.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestCABundleValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, content := range map[string]string{
		"empty.pem":   "",
		"garbage.pem": "not a certificate",
		"key.pem":     "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"broken.pem":  "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	} {
		_, err := config.Load(envconfig.Map(map[string]string{
			config.EnvManagerURL: "https://docker.example.com", config.EnvManagerCAFile: write(name, content),
		}, nil))
		if err == nil || !strings.Contains(err.Error(), config.EnvManagerCAFile) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := config.Load(envconfig.Map(map[string]string{
		config.EnvManagerURL: "https://docker.example.com", config.EnvManagerCAFile: filepath.Join(dir, "missing.pem"),
	}, nil))
	if err == nil {
		t.Error("missing CA file accepted")
	}
}

// TestRedirectedAllowsPlainHTTPOnlyForTheRedirect (#35, manager moves): the
// address of a manager.redirect may be plain http without
// DOCKER_AGENT_MANAGER_ALLOW_HTTP (it is flagged like any http URL);
// DOCKER_AGENT_MANAGER_URL itself still requires the opt-in.
func TestRedirectedAllowsPlainHTTPOnlyForTheRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	cfg := load(t, map[string]string{config.EnvManagerURL: "https://docker.example.com"})
	if _, err := config.Load(envconfig.Map(map[string]string{config.EnvManagerURL: srv.URL}, nil)); err == nil {
		t.Fatal("plain-HTTP DOCKER_AGENT_MANAGER_URL accepted without the opt-in")
	}
	tr, err := NewRedirected(cfg, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Redirected() || tr.URL("/x") != srv.URL+"/x" {
		t.Fatalf("redirected %v url %s", tr.Redirected(), tr.URL("/x"))
	}
	if info := tr.Info(); !info.PlainHTTP || !info.Flagged() || info.ManagerURL != srv.URL || info.Validate() != nil {
		t.Fatalf("info %+v", info)
	}
	if u := tr.WebSocketURL("/agent/v1/session"); u != "ws"+strings.TrimPrefix(srv.URL, "http")+"/agent/v1/session" {
		t.Fatalf("ws url %s", u)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodGet, tr.URL("/"), nil)
	resp, err := tr.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	plain, err := New(cfg)
	if err != nil || plain.Redirected() {
		t.Fatalf("configured transport: %v", err)
	}

	for _, bad := range []string{"", "ftp://m:21", "http://", "http://user:secret@m:8080", "http://m:8080/path", "http://m:8080?x=1",
		"http://m:8080?", "http://m:8080#f", "m:8080", "http://m:port", "https://" + strings.Repeat("a", config.MaxRedirectURLLen)} {
		_, err := NewRedirected(cfg, bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error echoes credentials: %v", bad, err)
		}
	}
}

// TestRedirectedHTTPSKeepsCustomCA: an https redirect keeps the configured
// CA bundle and is not flagged.
func TestRedirectedHTTPSKeepsCustomCA(t *testing.T) {
	srv, caFile := tlsManager(t)
	cfg := load(t, map[string]string{config.EnvManagerURL: "https://docker.example.com", config.EnvManagerCAFile: caFile})
	tr, err := NewRedirected(cfg, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if info := tr.Info(); info.PlainHTTP || !info.CustomCA || info.Flagged() || info.ManagerURL != srv.URL {
		t.Fatalf("info %+v", info)
	}
	if u := tr.WebSocketURL("/agent/v1/session"); !strings.HasPrefix(u, "wss://") {
		t.Fatalf("ws url %s", u)
	}
	req, _ := http.NewRequestWithContext(ctx(t), http.MethodGet, tr.URL("/"), nil)
	resp, err := tr.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("private CA not trusted for the redirect: %v", err)
	}
	_ = resp.Body.Close()
}
