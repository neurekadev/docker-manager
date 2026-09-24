package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestEchoFixture(t *testing.T) {
	srv := httptest.NewServer(handler())
	t.Cleanup(srv.Close)
	ctx := t.Context()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse?n=2&interval_ms=1", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q", ct)
	}
	var events []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if e, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			events = append(events, e)
		}
	}
	if got := strings.Join(events, ","); got != "ready,tick,tick,done" {
		t.Errorf("events = %s", got)
	}

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil) //nolint:bodyclose // the library owns the handshake response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	for _, m := range []string{"a", "b"} {
		if err := c.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
		_, got, err := c.Read(ctx)
		if err != nil || string(got) != "echo:"+m {
			t.Errorf("echo %s = %q, %v", m, got, err)
		}
	}

	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/healthz", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "ok" {
		t.Errorf("healthz = %q", b)
	}
}
