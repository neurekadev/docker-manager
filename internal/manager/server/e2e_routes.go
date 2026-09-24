//go:build e2e

// Test-only routes for the proxy E2E suite (#27, e2e/). They exist only in
// binaries built with -tags e2e (e2e/compose.yaml builds the manager with
// GO_TAGS=e2e); release images never contain them (TestNoTestRoutesInReleaseBuild).
//
//	GET /api/v1/__e2e/request-info    how the manager saw the request (JSON)
//	GET /agent/v1/__e2e/request-info  the same through the /agent/v1 guard
//	GET /api/v1/__e2e/sse             SSE via server/sse: "ready", then
//	                                  ?idle_ms= of silence (heartbeats only),
//	                                  then "tick" 1..?n= every ?interval_ms=,
//	                                  then "done"
//	GET /agent/v1/__e2e/echo          WebSocket echo via server/ws behind the
//	                                  /agent/v1 guard; replies "echo:<msg>";
//	                                  with ?hello=1 it first sends the
//	                                  request info as JSON

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
	"github.com/neurekadev/dockyard/internal/manager/server/sse"
	"github.com/neurekadev/dockyard/internal/manager/server/ws"
)

func init() { testRoutes = e2eRoutes }

// e2eRequestInfo is the JSON body of the request-info routes.
type e2eRequestInfo struct {
	ClientIP      string `json:"clientIp"`
	Peer          string `json:"peer"`
	TrustedPeer   bool   `json:"trustedPeer"`
	Scheme        string `json:"scheme"`
	Host          string `json:"host"`
	RequestID     string `json:"requestId"`
	CookiePresent bool   `json:"cookiePresent"`
	// ForwardedHeaders lists X-Forwarded-* headers still visible to the
	// handler (always empty: the server strips them).
	ForwardedHeaders []string `json:"forwardedHeaders"`
	// SecureOrigin is the result of requestinfo.CheckSecureOrigin, as the
	// first-run setup (#16) will evaluate it: "ok" or the failure reason.
	SecureOrigin string `json:"secureOrigin"`
	HTTPVersion  string `json:"httpVersion"`
}

func describeRequest(r *http.Request, opts Options) e2eRequestInfo {
	info, _ := requestinfo.From(r.Context())
	out := e2eRequestInfo{
		ClientIP: addrString(info.ClientIP), Peer: addrString(info.Peer), TrustedPeer: info.TrustedPeer,
		Scheme: info.Scheme, Host: info.Host, RequestID: logging.RequestID(r.Context()),
		CookiePresent: r.Header.Get("Cookie") != "", ForwardedHeaders: []string{}, SecureOrigin: "ok",
		HTTPVersion: r.Proto,
	}
	for _, h := range []string{requestinfo.HeaderForwardedFor, requestinfo.HeaderForwardedProto, requestinfo.HeaderForwardedHost, requestinfo.HeaderForwarded} {
		if r.Header.Get(h) != "" {
			out.ForwardedHeaders = append(out.ForwardedHeaders, h)
		}
	}
	if err := requestinfo.CheckSecureOrigin(opts.PublicURL, opts.LocalDevelopment, info); err != nil {
		var ie *requestinfo.InsecureOriginError
		out.SecureOrigin = err.Error()
		if errors.As(err, &ie) {
			out.SecureOrigin = ie.Reason
		}
	}
	return out
}

func e2eRoutes(mux *http.ServeMux, opts Options) {
	requestInfo := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(describeRequest(r, opts))
	}
	mux.HandleFunc("GET /api/v1/__e2e/request-info", requestInfo)
	mux.HandleFunc("GET "+AgentBasePath+"/__e2e/request-info", requestInfo)

	mux.HandleFunc("GET /api/v1/__e2e/sse", func(w http.ResponseWriter, r *http.Request) {
		n := intParam(r, "n", 3, 1000)
		interval := time.Duration(intParam(r, "interval_ms", 100, 60_000)) * time.Millisecond
		idle := time.Duration(intParam(r, "idle_ms", 0, 300_000)) * time.Millisecond
		sse.SetHeaders(w.Header().Set)
		s := sse.NewWriter(w)
		ctx := r.Context()
		if s.Send(sse.Event{Name: "ready", Data: "0"}) != nil {
			return
		}
		hb := opts.Clock.NewTicker(opts.StreamHeartbeat)
		defer hb.Stop()
		wait := func(d time.Duration) bool {
			if d <= 0 {
				return true
			}
			t := opts.Clock.NewTimer(d)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return false
				case <-t.C():
					return true
				case <-hb.C():
					if s.Heartbeat() != nil {
						return false
					}
				}
			}
		}
		if !wait(idle) {
			return
		}
		for i := 1; i <= n; i++ {
			if !wait(interval) || s.Send(sse.Event{ID: strconv.Itoa(i), Name: "tick", Data: strconv.Itoa(i)}) != nil {
				return
			}
		}
		_ = s.Send(sse.Event{Name: "done", Data: strconv.Itoa(n)})
	})

	mux.HandleFunc("GET "+AgentBasePath+"/__e2e/echo", func(w http.ResponseWriter, r *http.Request) {
		hello := describeRequest(r, opts)
		c, err := ws.Accept(w, r, ws.Options{
			Subprotocols: []string{"dockyard.e2e"}, ReadLimit: 64 << 10,
			PingInterval: opts.StreamHeartbeat, Clock: opts.Clock,
		})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()
		go func() { _ = c.KeepAlive(ctx) }()
		if r.URL.Query().Get("hello") == "1" {
			b, _ := json.Marshal(hello)
			if c.Write(ctx, websocket.MessageText, b) != nil {
				return
			}
		}
		for {
			typ, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if c.Write(ctx, typ, append([]byte("echo:"), msg...)) != nil {
				return
			}
		}
	})
}

func intParam(r *http.Request, name string, def, maxVal int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < 0 {
		return def
	}
	return min(v, maxVal)
}
