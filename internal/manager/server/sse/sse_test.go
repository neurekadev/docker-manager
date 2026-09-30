package sse

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestSetHeaders(t *testing.T) {
	h := http.Header{}
	SetHeaders(h.Set)
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-store" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", h)
	}
}

func TestEventEncoding(t *testing.T) {
	var buf bytes.Buffer
	s := NewWriter(&buf)
	if err := s.Send(Event{ID: "7", Name: "tick", Data: "a\nb\r\nc", Retry: 1500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := s.JSON("job", "", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(Event{Data: ""}); err != nil {
		t.Fatal(err)
	}
	if err := s.Comment("multi\nline"); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := s.Retry(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	want := "id: 7\nevent: tick\nretry: 1500\ndata: a\ndata: b\ndata: c\n\n" +
		"event: job\ndata: {\"n\":1}\n\n" +
		"data: \n\n" +
		": multi line\n\n" +
		": heartbeat\n\n" +
		"retry: 3000\n\n"
	if buf.String() != want {
		t.Fatalf("got %q\nwant %q", buf.String(), want)
	}
	for _, e := range []Event{{ID: "1\n2"}, {Name: "a\rb"}, {ID: "x\x00"}} {
		if err := s.Send(e); !errors.Is(err, ErrInvalidField) {
			t.Errorf("%+v: %v", e, err)
		}
	}
	if err := s.JSON("x", "", func() {}); err == nil {
		t.Fatal("unencodable value accepted")
	}
}

// TestEventsAreFlushedImmediately reads events over a real connection
// while the handler is still running: nothing waits in a buffer.
func TestEventsAreFlushedImmediately(t *testing.T) {
	ctx := testutil.Context(t)
	next := make(chan struct{})
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		SetHeaders(w.Header().Set)
		s := NewWriter(w)
		if err := s.Flush(); err != nil {
			t.Error(err)
			return
		}
		for i := range 3 {
			select {
			case <-next:
			case <-r.Context().Done():
				return
			}
			if i == 1 {
				_ = s.Heartbeat()
				continue
			}
			_ = s.Send(Event{Name: "n", Data: "x"})
		}
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", resp.Header)
	}
	r := bufio.NewReader(resp.Body)
	for _, want := range []string{"event: n", ": heartbeat", "event: n"} {
		next <- struct{}{}
		line, err := r.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != want {
			t.Fatalf("got %q (%v), want %q", line, err, want)
		}
		if _, err := r.ReadString('\n'); err != nil { // data line or blank
			t.Fatal(err)
		}
		if want == "event: n" {
			_, _ = r.ReadString('\n') // blank line
		}
	}
	<-done
}

func TestHeartbeatBounds(t *testing.T) {
	if DefaultHeartbeat < MinHeartbeat || DefaultHeartbeat > MaxHeartbeat || MaxHeartbeat >= 60*time.Second {
		t.Fatalf("heartbeat bounds %v %v %v", MinHeartbeat, DefaultHeartbeat, MaxHeartbeat)
	}
}
