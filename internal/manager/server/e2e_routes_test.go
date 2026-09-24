//go:build e2e

package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestE2ERoutes checks the test-only routes the Playwright proxy suite
// relies on (run by the extended e2e job with -tags e2e).
func TestE2ERoutes(t *testing.T) {
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	pub, _ := url.Parse("https://localhost:8443")
	logger, _ := testutil.CaptureLogger()
	s, err := New(Options{
		Logger: logger, Clock: clk, UI: testUI, PublicURL: pub, StreamHeartbeat: 2 * time.Second,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler)
	defer srv.Close()

	get := func(path string, hdr ...string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	var info e2eRequestInfo
	resp := get("/api/v1/__e2e/request-info", "X-Forwarded-For", "198.51.100.3", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "localhost:8443", "Cookie", "a=b")
	_ = json.NewDecoder(resp.Body).Decode(&info)
	_ = resp.Body.Close()
	if info.ClientIP != "198.51.100.3" || info.Scheme != "https" || info.SecureOrigin != "ok" || !info.CookiePresent || len(info.ForwardedHeaders) != 0 {
		t.Fatalf("api request info %+v", info)
	}
	resp = get("/agent/v1/__e2e/request-info", "Cookie", "a=b")
	_ = json.NewDecoder(resp.Body).Decode(&info)
	_ = resp.Body.Close()
	if info.CookiePresent || info.SecureOrigin != "request_not_https" {
		t.Fatalf("agent request info %+v", info)
	}

	// SSE: ready, heartbeats while idle (fake clock), ticks, done.
	resp = get("/api/v1/__e2e/sse?n=1&interval_ms=10&idle_ms=5000")
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("headers %v", resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() string {
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, "data:") && !strings.HasPrefix(l, "id:") {
				return l
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return ""
	}
	if l := next(); l != "event: ready" {
		t.Fatalf("first %q", l)
	}
	for range 2 {
		if err := clk.BlockUntilWaiters(ctx, 2); err != nil { // heartbeat ticker + idle timer
			t.Fatal(err)
		}
		clk.Advance(2 * time.Second)
		if l := next(); l != ": heartbeat" {
			t.Fatalf("got %q, want heartbeat", l)
		}
	}
	clk.Advance(time.Second) // idle over
	if err := clk.BlockUntilWaiters(ctx, 2); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Millisecond)
	for _, want := range []string{"event: tick", "event: done"} {
		if l := next(); l != want {
			t.Fatalf("got %q, want %q", l, want)
		}
	}

	// WebSocket echo with hello.
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/agent/v1/__e2e/echo?hello=1", //nolint:bodyclose // the connection owns the upgrade response
		&websocket.DialOptions{Subprotocols: []string{"dockyard.e2e"}, HTTPHeader: http.Header{"Cookie": {"a=b"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	_, hello, err := c.Read(ctx)
	if err != nil || json.Unmarshal(hello, &info) != nil || info.CookiePresent {
		t.Fatalf("hello %s %v", hello, err)
	}
	if err := c.Write(ctx, websocket.MessageText, []byte("ping-1")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.Read(ctx); err != nil || string(msg) != "echo:ping-1" {
		t.Fatalf("echo %q %v", msg, err)
	}
}
