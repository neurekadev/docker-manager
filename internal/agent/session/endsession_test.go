package session_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestEndSessionWithOutputAndTarget (#35, manager moves): a handler that
// returns an EndSessionError without Err answers the request with its
// Output (a response frame, not an error), then the session closes with
// the handler's reconnectable code; the client reconnects and asks Target
// again, so the next dial goes to the address the handler switched to.
func TestEndSessionWithOutputAndTarget(t *testing.T) {
	type seen struct {
		response  *protocol.Frame
		closeCode websocket.StatusCode
	}
	first := make(chan seen, 1)
	oldManager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Version}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		var s seen
		defer func() {
			select {
			case first <- s:
			default:
			}
		}()
		hello, err := protocol.ReadFrame(ctx, c)
		if err != nil {
			return
		}
		welcome, _ := protocol.NewFrame(protocol.TypeWelcome, "w1", hello.ID, protocol.JobRef{}, protocol.WelcomePayload{
			SessionID: "s1", ManagerVersion: "1.0.0", EnvironmentID: "env1", AgentStatus: protocol.VersionCurrent,
			HeartbeatIntervalMs: 15000, HeartbeatTimeoutMs: 45000, Limits: protocol.DefaultLimits(),
		})
		deadline := testutil.Epoch.Add(time.Hour)
		input, _ := json.Marshal(protocol.RequestPayload{Name: protocol.ReqManagerRedirect})
		req := &protocol.Frame{Type: protocol.TypeRequest, ID: "r1", Deadline: &deadline, Payload: input}
		if protocol.WriteFrame(ctx, c, welcome) != nil || protocol.WriteFrame(ctx, c, req) != nil {
			return
		}
		for {
			f, err := protocol.ReadFrame(ctx, c)
			if err != nil {
				s.closeCode = websocket.CloseStatus(err)
				return
			}
			if f.CorrelationID == "r1" {
				s.response = f
			}
		}
	}))
	defer oldManager.Close()
	reached := make(chan string, 1)
	newManager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Version}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		if _, err := protocol.ReadFrame(r.Context(), c); err == nil {
			select {
			case reached <- r.Header.Get("Authorization"):
			default:
			}
		}
		_ = c.Close(protocol.CloseNormal, "done")
	}))
	defer newManager.Close()

	var switched atomic.Bool
	var dials atomic.Int32
	st := openState(t)
	c := session.New(session.Options{
		State: st, Clock: testutil.FakeClock(), Logger: testutil.Logger(t),
		Target: func(h http.Header) (string, *websocket.DialOptions) {
			dials.Add(1)
			base := oldManager.URL
			if switched.Load() {
				base = newManager.URL
			}
			return "ws" + strings.TrimPrefix(base, "http"), &websocket.DialOptions{HTTPHeader: h}
		},
		AgentVersion: "1.0.0", UserAgent: "docker-agent/test",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			return protocol.CapabilitiesPayload{AgentVersion: "1.0.0", Protocols: []string{protocol.Version},
				Engine: protocol.EngineInfo{ID: "ENG", APIVersion: "1.47"}}, true
		},
		Requests: map[string]session.RequestHandler{protocol.ReqManagerRedirect: func(context.Context, json.RawMessage) (any, error) {
			switched.Store(true)
			return nil, &session.EndSessionError{Output: map[string]bool{"followed": true}, Code: protocol.CloseGoingAway, Reason: "moving"}
		}},
		Backoff: session.Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: func() float64 { return 0 }},
	})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	var s seen
	select {
	case s = <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("the first session did not end")
	}
	if s.response == nil || s.response.Type != protocol.TypeResponse {
		t.Fatalf("answer %+v", s.response)
	}
	p, err := protocol.DecodePayload[protocol.ResponsePayload](s.response)
	if err != nil || string(p.Output) != `{"followed":true}` {
		t.Fatalf("output %s %v", p.Output, err)
	}
	if s.closeCode != protocol.CloseGoingAway {
		t.Fatalf("close code %d", s.closeCode)
	}
	select {
	case auth := <-reached:
		if auth != "Bearer dya_c1_secret" {
			t.Fatalf("the new address got %q", auth)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the client did not dial the new address")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dials.Load() < 2 {
		t.Fatalf("dials %d", dials.Load())
	}
	if cred, err := st.Credential(); err != nil || cred == nil {
		t.Fatalf("credential lost: %v", err)
	}
}
