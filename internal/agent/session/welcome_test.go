package session_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/state"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// managerSession is what a fake manager saw of one agent session.
type managerSession struct {
	frames    []protocol.Type // frames received after hello
	closeCode websocket.StatusCode
	answered  bool // the request was answered
}

// runWelcome runs the agent session client against a fake manager (one
// httptest server on the loopback) that answers hello with a welcome of
// generation and sends an engine.info request right after it. The generation
// check is the production one: protect.Guard.AcceptGeneration over st.
// It returns what the manager saw, whether the request handler ran and the
// states the client reported.
func runWelcome(t *testing.T, st *state.Store, generation int64) (managerSession, bool, []string) {
	t.Helper()
	seen := make(chan managerSession, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Version}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		var ms managerSession
		defer func() { seen <- ms }()
		hello, err := protocol.ReadFrame(ctx, c)
		if err != nil {
			return
		}
		welcome, err := protocol.NewFrame(protocol.TypeWelcome, "w1", hello.ID, protocol.JobRef{}, protocol.WelcomePayload{
			SessionID: "s1", ManagerVersion: "1.0.0", EnvironmentID: "env1", AgentStatus: protocol.VersionCurrent,
			HeartbeatIntervalMs: 15000, HeartbeatTimeoutMs: 45000, Limits: protocol.DefaultLimits(), Generation: generation,
		})
		if err != nil {
			t.Error(err)
			return
		}
		deadline := testutil.Epoch.Add(time.Hour)
		input, _ := json.Marshal(protocol.RequestPayload{Name: protocol.ReqEngineInfo})
		req := &protocol.Frame{Type: protocol.TypeRequest, ID: "r1", Deadline: &deadline, Payload: input}
		if protocol.WriteFrame(ctx, c, welcome) != nil || protocol.WriteFrame(ctx, c, req) != nil {
			return
		}
		for {
			f, err := protocol.ReadFrame(ctx, c)
			if err != nil {
				ms.closeCode = websocket.CloseStatus(err)
				return
			}
			ms.frames = append(ms.frames, f.Type)
			if f.CorrelationID == "r1" {
				ms.answered = true
				_ = c.Close(protocol.CloseNormal, "done")
				return
			}
		}
	}))
	defer srv.Close()

	guard := protect.New(protect.Options{Logger: testutil.Logger(t)})
	guard.SetGenerations(st)
	var ran atomic.Bool
	var mu sync.Mutex
	var states []string
	c := session.New(session.Options{
		State: st, Clock: testutil.FakeClock(), Logger: testutil.Logger(t),
		URL:          "ws" + strings.TrimPrefix(srv.URL, "http"),
		DialOptions:  func(h http.Header) *websocket.DialOptions { return &websocket.DialOptions{HTTPHeader: h} },
		AgentVersion: "1.0.0", UserAgent: "docker-agent/test",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			return protocol.CapabilitiesPayload{AgentVersion: "1.0.0", Protocols: []string{protocol.Version},
				Engine: protocol.EngineInfo{ID: "ENG", APIVersion: "1.47"}}, true
		},
		Requests: map[string]session.RequestHandler{protocol.ReqEngineInfo: func(context.Context, json.RawMessage) (any, error) {
			ran.Store(true)
			return map[string]string{}, nil
		}},
		AcceptWelcome: func(w protocol.WelcomePayload) error { return guard.AcceptGeneration("", w.Generation) },
		OnStatus: func(s session.Status) {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, s.State)
		},
	})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	var ms managerSession
	select {
	case ms = <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	// The manager sees the close before the client reports its end: wait
	// until it settled (back in the reconnect backoff, or connected when
	// the manager was accepted and answered). The fake clock holds the
	// backoff: Run waits in it (it did not stop for good) until canceled.
	settled := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(states) > 0 && (states[len(states)-1] == session.StateDisconnected || ms.answered)
	}
	for end := time.Now().Add(10 * time.Second); !settled(); {
		if time.Now().After(end) {
			t.Fatal("the client did not settle after the session ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return ms, ran.Load(), states
}

func openState(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCredential(state.Credential{AgentID: "a1", EnvironmentID: "env1", Credential: "dya_c1_secret", ManagerURL: "https://m"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func generation(t *testing.T, st *state.Store) int64 {
	t.Helper()
	g, err := st.ManagerGeneration()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestWelcomeGenerationRefused (#35, manager moves): a welcome from a
// manager with a lower generation than recorded ends the session before
// anything else runs (no capabilities or job report, the request is never
// handled), closes with 4421, and the client goes back to its reconnect
// backoff (not stopped) with the credential and the record untouched.
func TestWelcomeGenerationRefused(t *testing.T) {
	st := openState(t)
	if err := st.SaveManagerGeneration(3); err != nil {
		t.Fatal(err)
	}
	for _, old := range []int64{0, 2} { // 0: a manager that predates moves (= 1)
		ms, ran, states := runWelcome(t, st, old)
		if ms.closeCode != protocol.CloseManagerSuperseded || len(ms.frames) != 0 || ms.answered {
			t.Fatalf("generation %d: manager saw %+v", old, ms)
		}
		if ran {
			t.Fatalf("generation %d: a request handler ran for a refused manager", old)
		}
		for _, s := range states {
			if s == session.StateConnected || s == session.StateStopped {
				t.Fatalf("generation %d: states %v", old, states)
			}
		}
		if len(states) == 0 || states[len(states)-1] != session.StateDisconnected {
			t.Fatalf("generation %d: not back in the reconnect backoff: %v", old, states)
		}
		if g := generation(t, st); g != 3 {
			t.Fatalf("generation %d: record %d", old, g)
		}
		if c, err := st.Credential(); err != nil || c == nil {
			t.Fatalf("credential lost: %v", err)
		}
	}
}

// TestWelcomeGenerationAccepted: a first welcome with generation 0 records
// 1; a higher generation is recorded and the session runs normally
// (capabilities, job report, the request answered).
func TestWelcomeGenerationAccepted(t *testing.T) {
	st := openState(t)
	ms, ran, _ := runWelcome(t, st, 0)
	if !ms.answered || !ran || generation(t, st) != 1 {
		t.Fatalf("generation 0: manager saw %+v, handler ran %v, record %d", ms, ran, generation(t, st))
	}
	ms, ran, _ = runWelcome(t, st, 4)
	if !ms.answered || !ran || generation(t, st) != 4 {
		t.Fatalf("generation 4: manager saw %+v, handler ran %v, record %d", ms, ran, generation(t, st))
	}
	if len(ms.frames) < 3 || ms.frames[0] != protocol.TypeCapabilities || ms.frames[1] != protocol.TypeJobReport {
		t.Fatalf("session frames %v", ms.frames)
	}
}
