package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

type serverResult struct {
	conn *Conn
	err  error
}

// newServer accepts one connection per request and hands it to the test.
func newServer(t *testing.T, opts Options) (*httptest.Server, <-chan serverResult) {
	t.Helper()
	ch := make(chan serverResult, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, opts)
		ch <- serverResult{c, err}
		if c != nil {
			<-r.Context().Done() // keep the handler alive; the test owns c
		}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func TestAcceptNegotiatesSubprotocol(t *testing.T) {
	ctx := testutil.Context(t)
	srv, ch := newServer(t, Options{Subprotocols: []string{"docker-manager.agent/v1"}, RequireSubprotocol: true})
	c, resp, err := websocket.Dial(ctx, wsURL(srv), &websocket.DialOptions{Subprotocols: []string{"docker-manager.agent/v1"}}) //nolint:bodyclose // the connection owns the upgrade response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	if resp.Header.Get("Sec-WebSocket-Protocol") != "docker-manager.agent/v1" || c.Subprotocol() != "docker-manager.agent/v1" {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	res := <-ch
	if res.err != nil || res.conn.Subprotocol() != "docker-manager.agent/v1" {
		t.Fatalf("server: %+v", res)
	}
	_ = res.conn.CloseNow()

	// Without the required subprotocol the server closes with 1008.
	c2, _, err := websocket.Dial(ctx, wsURL(srv), nil) //nolint:bodyclose // the connection owns the upgrade response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.CloseNow() }()
	if _, _, err := c2.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("client read %v", err)
	}
	if res := <-ch; !errors.Is(res.err, ErrSubprotocol) {
		t.Fatalf("server err %v", res.err)
	}
}

func TestAcceptRejectsPlainHTTPAndForeignOrigins(t *testing.T) {
	ctx := testutil.Context(t)
	srv, ch := newServer(t, Options{OriginPatterns: []string{"docker.example.com"}})

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var e struct{ Code string }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired || e.Code != "upgrade_required" || !strings.Contains(resp.Header.Get("Content-Type"), "problem+json") {
		t.Fatalf("plain GET: %d %q", resp.StatusCode, e.Code)
	}
	if res := <-ch; !errors.Is(res.err, ErrNotWebSocket) {
		t.Fatalf("server err %v", res.err)
	}

	_, resp, err = websocket.Dial(ctx, wsURL(srv), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}) //nolint:bodyclose // closed below
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", err, resp)
	}
	_ = resp.Body.Close()
	<-ch

	for _, origin := range []string{"https://docker.example.com", srv.URL} {
		c, _, err := websocket.Dial(ctx, wsURL(srv), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}}) //nolint:bodyclose // the connection owns the upgrade response
		if err != nil {
			t.Fatalf("origin %s: %v", origin, err)
		}
		_ = c.CloseNow()
		if res := <-ch; res.err != nil {
			t.Fatal(res.err)
		} else {
			_ = res.conn.CloseNow()
		}
	}
}

func TestReadLimitIsBounded(t *testing.T) {
	ctx := testutil.Context(t)
	srv, ch := newServer(t, Options{ReadLimit: 16})
	c, _, err := websocket.Dial(ctx, wsURL(srv), nil) //nolint:bodyclose // the connection owns the upgrade response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	res := <-ch
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer func() { _ = res.conn.CloseNow() }()
	if err := c.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 17))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := res.conn.Read(ctx); err == nil || !strings.Contains(err.Error(), "message too big") {
		t.Fatalf("oversized message: %v", err)
	}
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("client sees %v", err)
	}
}

// TestKeepAlivePingsAndDropsDeadPeers drives KeepAlive with the fake clock:
// a reading peer answers every ping; a peer that stops reading is closed
// after PongTimeout.
func TestKeepAlivePingsAndDropsDeadPeers(t *testing.T) {
	ctx := testutil.Context(t)
	clk := testutil.FakeClock()
	const interval = 15 * time.Second
	srv, ch := newServer(t, Options{PingInterval: interval, PongTimeout: 5 * time.Second, Clock: clk})

	pings := make(chan struct{}, 4)
	c, _, err := websocket.Dial(ctx, wsURL(srv), &websocket.DialOptions{OnPingReceived: func(context.Context, []byte) bool { //nolint:bodyclose // the connection owns the upgrade response
		pings <- struct{}{}
		return true
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	res := <-ch
	if res.err != nil {
		t.Fatal(res.err)
	}
	srvConn := res.conn
	// Pongs reach KeepAlive only through a concurrent server-side reader;
	// the client reads so its library answers pings.
	go func() { _, _, _ = srvConn.Read(ctx) }()
	go func() { _, _, _ = c.Read(ctx) }()

	pinged := make(chan error, 4)
	srvConn.afterPing = func(err error) { pinged <- err }
	kaCtx, stop := context.WithCancel(ctx)
	kaDone := make(chan error, 1)
	go func() { kaDone <- srvConn.KeepAlive(kaCtx) }()

	for i := range 2 {
		if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(interval)
		select {
		case <-pings:
		case <-ctx.Done():
			t.Fatalf("ping %d not received", i)
		}
		if err := <-pinged; err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	stop()
	if err := <-kaDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("KeepAlive after cancel: %v", err)
	}

	// A peer that never answers: a second connection whose client does not read.
	c2, _, err := websocket.Dial(ctx, wsURL(srv), nil) //nolint:bodyclose // the connection owns the upgrade response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.CloseNow() }()
	res = <-ch
	if res.err != nil {
		t.Fatal(res.err)
	}
	dead := res.conn
	go func() { _, _, _ = dead.Read(ctx) }()
	go func() { kaDone <- dead.KeepAlive(ctx) }()
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(interval) // ping sent, pong timer armed
	if err := clk.BlockUntilWaiters(ctx, 2); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	select {
	case err := <-kaDone:
		if !errors.Is(err, ErrPongTimeout) {
			t.Fatalf("KeepAlive on dead peer: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("dead peer was not dropped")
	}
}
