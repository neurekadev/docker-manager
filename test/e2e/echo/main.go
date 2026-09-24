// Command echo is the E2E stream fixture (#29): it sits behind the TLS
// proxy next to the manager (e2e/compose.yaml, route /__e2e/echo/*) so the
// Playwright SSE and WebSocket helpers can be verified through the proxy
// before product streams exist.
//
//	GET /healthz   200 "ok"
//	GET /sse       text/event-stream: "ready", then "tick" 1..n (?n=, default
//	               3) one per ?interval_ms= (default 100), then "done"
//	GET /ws        WebSocket echo: every message is sent back prefixed "echo:"
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

func main() {
	addr := ":8080"
	if v := os.Getenv("ECHO_ADDR"); v != "" {
		addr = v
	}
	srv := &http.Server{Addr: addr, Handler: handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("echo fixture listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("echo fixture", "err", err)
		os.Exit(1)
	}
}

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /sse", sse)
	mux.HandleFunc("GET /ws", ws)
	return mux
}

func intParam(r *http.Request, name string, def, maxVal int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < 0 {
		return def
	}
	return min(v, maxVal)
}

func sse(w http.ResponseWriter, r *http.Request) {
	n := intParam(r, "n", 3, 1000)
	interval := time.Duration(intParam(r, "interval_ms", 100, 10000)) * time.Millisecond
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	send := func(event, data string) bool {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("ready", "0") {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for i := 1; i <= n; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
		if !send("tick", strconv.Itoa(i)) {
			return
		}
	}
	send("done", strconv.Itoa(n))
}

func ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(64 << 10)
	for {
		typ, msg, err := c.Read(r.Context())
		if err != nil {
			return
		}
		if err := c.Write(r.Context(), typ, append([]byte("echo:"), msg...)); err != nil {
			return
		}
	}
}
