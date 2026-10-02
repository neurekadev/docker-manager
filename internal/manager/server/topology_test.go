package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/server/ws"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// seen records what a handler observed.
type seen struct {
	mu      sync.Mutex
	calls   int
	info    requestinfo.Info
	reqID   string
	headers http.Header
}

func (s *seen) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.info, _ = requestinfo.From(r.Context())
	s.reqID = logging.RequestID(r.Context())
	s.headers = r.Header.Clone()
}

func (s *seen) get() (int, requestinfo.Info, string, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.info, s.reqID, s.headers
}

type topo struct {
	srv   *Server
	logs  *testutil.LogBuffer
	clk   *clock.Fake
	agent *seen
}

// newTopo builds a server trusting 10.0.0.0/24 and fd00::/64 as proxies,
// with a probe API operation (fake bearer/cookie authentication) and a
// probe agent handler.
func newTopo(t *testing.T, limits AgentLimits, agent http.Handler) *topo {
	t.Helper()
	logger, logs := testutil.CaptureLogger()
	clk := testutil.FakeClock()
	tp := &topo{logs: logs, clk: clk, agent: &seen{}}
	if agent == nil {
		agent = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tp.agent.record(r)
			if _, err := io.ReadAll(r.Body); err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					AgentFailure(w, r, http.StatusRequestEntityTooLarge)
					return
				}
				AgentFailure(w, r, http.StatusRequestTimeout)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	s, err := New(Options{
		Logger: logger, Clock: clk, UI: testUI,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("fd00::/64")},
		Agent:          agent, AgentLimits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	tp.srv = s
	api.Register(s.API, api.Operation{
		Operation:  huma.Operation{OperationID: "test-probe", Method: http.MethodGet, Path: api.BasePath + "/test/probe", Summary: "probe"},
		Capability: api.CapabilityAuthenticated, Scope: api.ScopeNone,
	}, func(ctx context.Context, in *struct {
		Authorization string `header:"Authorization"`
		Cookie        string `header:"Cookie"`
	}) (*struct{}, error) {
		// A fake authenticator accepting any bearer token or session cookie:
		// whatever reaches it would authenticate.
		if in.Authorization == "" && in.Cookie == "" {
			return nil, api.Unauthenticated("no credentials")
		}
		return nil, nil
	})
	return tp
}

func (tp *topo) do(method, target, remote string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	tp.srv.Handler.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) api.Error {
	t.Helper()
	var e api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return e
}

func TestForwardedHeadersOnlyFromTrustedProxies(t *testing.T) {
	tp := newTopo(t, AgentLimits{}, nil)
	spoof := []string{
		"X-Forwarded-For", "198.51.100.66", "X-Forwarded-Proto", "https",
		"X-Forwarded-Host", "evil.example", "Forwarded", "for=198.51.100.66", RequestIDHeader, "spoofed-id",
	}
	// Untrusted peer: everything ignored.
	tp.do(http.MethodGet, "http://docker.example.com/agent/v1/probe", "203.0.113.5:4000", spoof...)
	_, info, reqID, hdr := tp.agent.get()
	if info.ClientIP != netip.MustParseAddr("203.0.113.5") || info.TrustedPeer || info.Scheme != "http" || info.Host != "docker.example.com" {
		t.Fatalf("untrusted peer: %+v", info)
	}
	if reqID == "spoofed-id" {
		t.Fatal("request id from untrusted peer honored")
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded"} {
		if hdr.Get(h) != "" {
			t.Errorf("%s reached the handler", h)
		}
	}

	// Trusted proxy: client IP, scheme and host from the headers; a
	// client-prepended XFF entry is skipped; the proxy's request ID is kept.
	tp.do(http.MethodGet, "http://docker-manager:8080/agent/v1/probe", "10.0.0.2:4000",
		"X-Forwarded-For", "1.2.3.4, 198.51.100.7", "X-Forwarded-Proto", "https",
		"X-Forwarded-Host", "docker.example.com", RequestIDHeader, "proxy-req-1")
	_, info, reqID, hdr = tp.agent.get()
	if info.ClientIP != netip.MustParseAddr("198.51.100.7") || !info.TrustedPeer || info.Scheme != "https" || info.Host != "docker.example.com" {
		t.Fatalf("trusted proxy: %+v", info)
	}
	if reqID != "proxy-req-1" || hdr.Get("X-Forwarded-For") != "" {
		t.Fatalf("request id %q, XFF visible %q", reqID, hdr.Get("X-Forwarded-For"))
	}
	if !strings.Contains(tp.logs.String(), `"client_ip":"198.51.100.7"`) {
		t.Fatalf("access log lacks client_ip: %s", tp.logs.String())
	}
	// Malformed IDs are replaced even from a trusted proxy.
	rec := tp.do(http.MethodGet, "/api/v1/health", "10.0.0.2:1", RequestIDHeader, "bad id")
	if id := rec.Header().Get(RequestIDHeader); id == "bad id" || len(id) != 32 {
		t.Fatalf("request id %q", id)
	}
}

// TestCookiesNeverAuthenticateAgentRoutes: browser cookies are stripped on
// /agent/v1 and non-agent bearer credentials are refused there.
func TestCookiesNeverAuthenticateAgentRoutes(t *testing.T) {
	tp := newTopo(t, AgentLimits{}, nil)
	agentCred, err := authsep.NewAgentCredential()
	if err != nil {
		t.Fatal(err)
	}
	rec := tp.do(http.MethodGet, "/agent/v1/session", "203.0.113.5:1", "Cookie", "docker_manager_session=fake-session-cookie")
	calls, _, _, hdr := tp.agent.get()
	if calls != 1 || hdr.Get("Cookie") != "" || rec.Code != http.StatusNoContent {
		t.Fatalf("calls %d cookie %q status %d", calls, hdr.Get("Cookie"), rec.Code)
	}
	tp.do(http.MethodGet, "/agent/v1/session", "203.0.113.5:1", "Authorization", "Bearer "+agentCred, "Cookie", "a=b")
	calls, _, _, hdr = tp.agent.get()
	if calls != 2 || hdr.Get("Authorization") != "Bearer "+agentCred || hdr.Get("Cookie") != "" {
		t.Fatalf("agent credential: calls %d hdr %v", calls, hdr)
	}
	for _, auth := range []string{"Bearer dyt_fake-api-token", "Bearer random-token", "Basic YWRtaW46YWRtaW4=", "Bearer", "Bearer a b"} {
		rec = tp.do(http.MethodGet, "/agent/v1/session", "203.0.113.5:1", "Authorization", auth)
		e := decodeErr(t, rec)
		if rec.Code != http.StatusUnauthorized || e.Code != api.CodeUnauthenticated || e.Message != agentAuthFailed {
			t.Fatalf("%q: %d %+v", auth, rec.Code, e)
		}
	}
	if calls, _, _, _ := tp.agent.get(); calls != 2 {
		t.Fatalf("refused requests reached the handler: %d", calls)
	}
}

// TestAgentCredentialsNeverAuthenticateAPIRoutes: the fake API
// authenticator would accept any bearer token, but agent credentials and
// enrollment tokens are refused before it runs.
func TestAgentCredentialsNeverAuthenticateAPIRoutes(t *testing.T) {
	tp := newTopo(t, AgentLimits{}, nil)
	cred, _ := authsep.NewAgentCredential()
	enroll, _ := authsep.NewEnrollmentToken()
	for _, tok := range []string{cred, enroll} {
		rec := tp.do(http.MethodGet, "/api/v1/test/probe", "203.0.113.5:1", "Authorization", "Bearer "+tok)
		e := decodeErr(t, rec)
		if rec.Code != http.StatusUnauthorized || e.Message != "agent credentials cannot be used for the public API" {
			t.Fatalf("%.4s…: %d %+v", tok, rec.Code, e)
		}
		if strings.Contains(tp.logs.String(), tok) {
			t.Fatal("agent secret logged")
		}
	}
	// Other credentials still reach the API (and its authenticator).
	for _, hdr := range [][]string{{"Authorization", "Bearer dyt_fake-api-token"}, {"Cookie", "docker_manager_session=fake"}} {
		if rec := tp.do(http.MethodGet, "/api/v1/test/probe", "203.0.113.5:1", hdr...); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
			t.Fatalf("%v: %d %s", hdr, rec.Code, rec.Body.String())
		}
	}
}

func TestAgentRateLimitPerClientIP(t *testing.T) {
	tp := newTopo(t, AgentLimits{RequestsPerSecond: 1, Burst: 3, MaxTrackedClients: 3}, nil)
	get := func(remote string, hdr ...string) int {
		return tp.do(http.MethodGet, "/agent/v1/enroll", remote, hdr...).Code
	}
	for i := range 3 {
		if c := get("203.0.113.5:1"); c != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	rec := tp.do(http.MethodGet, "/agent/v1/enroll", "203.0.113.5:2")
	e := decodeErr(t, rec)
	if rec.Code != http.StatusTooManyRequests || e.Code != api.CodeRateLimited || !e.Retryable || rec.Header().Get("Retry-After") != "1" ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("over limit: %d %+v %v", rec.Code, e, rec.Header())
	}
	// Spoofed XFF from an untrusted peer does not open a fresh bucket.
	if c := get("203.0.113.5:3", "X-Forwarded-For", "198.51.100.1"); c != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF evaded the limit: %d", c)
	}
	// Behind the trusted proxy each client has its own bucket.
	if c := get("10.0.0.2:1", "X-Forwarded-For", "198.51.100.1"); c != http.StatusNoContent {
		t.Fatalf("client behind proxy: %d", c)
	}
	// Tokens refill with (fake) time.
	tp.clk.Advance(time.Second)
	if c := get("203.0.113.5:1"); c != http.StatusNoContent {
		t.Fatalf("after refill: %d", c)
	}
	if c := get("10.0.0.2:1", "X-Forwarded-For", "198.51.100.1"); c != http.StatusNoContent { // keeps its bucket busy
		t.Fatalf("client behind proxy: %d", c)
	}
	// IPv6 clients share a /64 bucket. The table holds 3 entries: two are
	// in use; the third (2001:db8:1::/64) takes the last slot.
	for i := range 3 {
		if c := get(fmt.Sprintf("[2001:db8:1::%d]:1", i+1)); c != http.StatusNoContent {
			t.Fatalf("ipv6 %d: %d", i, c)
		}
	}
	if c := get("[2001:db8:1::99]:1"); c != http.StatusTooManyRequests {
		t.Fatalf("same /64: %d", c)
	}
	// Table full: a new client still gets a bucket (a flood of new
	// addresses never locks clients out); the bucket that is full again
	// soonest (198.51.100.1's, one token used) is forgotten...
	if c := get("192.0.2.200:1"); c != http.StatusNoContent {
		t.Fatalf("full table: %d", c)
	}
	// ...while the busy /64 keeps its exhausted bucket.
	if c := get("[2001:db8:1::5]:1"); c != http.StatusTooManyRequests {
		t.Fatalf("recently seen /64 lost its bucket: %d", c)
	}
	// Public API routes are not affected by the agent limiter.
	for range 10 {
		if c := tp.do(http.MethodGet, "/api/v1/health", "192.0.2.200:1").Code; c != http.StatusOK {
			t.Fatalf("api: %d", c)
		}
	}
}

func TestAgentBodyBound(t *testing.T) {
	tp := newTopo(t, AgentLimits{MaxBodyBytes: 8}, nil)
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", strings.NewReader(strings.Repeat("x", 9)))
	rec := httptest.NewRecorder()
	tp.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || decodeErr(t, rec).Message != agentTooLarge {
		t.Fatalf("declared length: %d %s", rec.Code, rec.Body.String())
	}
	if calls, _, _, _ := tp.agent.get(); calls != 0 {
		t.Fatal("oversized request reached the handler")
	}
	// Unknown length (chunked): the handler's read fails at the bound.
	req = httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", io.MultiReader(strings.NewReader(strings.Repeat("x", 9))))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	tp.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked: %d", rec.Code)
	}
}

// TestAgentPreAuthTimeout uses an already-expired pre-auth deadline, so the
// outcome is deterministic without waiting: a client that has not sent its
// whole body in time is cut off, and a WebSocket upgrade clears the
// deadline so the session is unaffected.
func TestAgentPreAuthTimeout(t *testing.T) {
	ctx := testutil.Context(t)
	readErr := make(chan error, 1)
	tp := newTopo(t, AgentLimits{PreAuthTimeout: time.Nanosecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws.IsUpgrade(r) {
			c, err := ws.Accept(w, r, ws.Options{})
			if err != nil {
				return
			}
			defer func() { _ = c.CloseNow() }()
			typ, msg, err := c.Read(ctx)
			if err == nil {
				err = c.Write(ctx, typ, append([]byte("echo:"), msg...))
			}
			readErr <- err
			return
		}
		_, err := io.ReadAll(r.Body)
		readErr <- err
		AgentFailure(w, r, http.StatusRequestTimeout)
	}))
	srv := httptest.NewServer(tp.srv.Handler)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "POST /agent/v1/enroll HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\npartial")
	select {
	case err := <-readErr:
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("slow body: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("slow body was not cut off")
	}

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/agent/v1/session", nil) //nolint:bodyclose // the connection owns the upgrade response
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	if err := c.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := <-readErr; err != nil {
		t.Fatalf("session read after upgrade: %v", err)
	}
	if _, msg, err := c.Read(ctx); err != nil || string(msg) != "echo:hi" {
		t.Fatalf("echo %q %v", msg, err)
	}
}

func TestAgentFailureIsGeneric(t *testing.T) {
	for status, msg := range map[int]string{
		http.StatusUnauthorized: agentAuthFailed, http.StatusTooManyRequests: agentRateLimited,
		http.StatusRequestEntityTooLarge: agentTooLarge, http.StatusTeapot: agentBadRequest, http.StatusRequestTimeout: "request timed out",
	} {
		rec := httptest.NewRecorder()
		AgentFailure(rec, httptest.NewRequest(http.MethodGet, "/agent/v1/x", nil), status)
		e := decodeErr(t, rec)
		want := status
		if status == http.StatusTeapot {
			want = http.StatusBadRequest
		}
		if rec.Code != want || e.Message != msg || len(e.Details) != 0 {
			t.Errorf("%d: %d %+v", status, rec.Code, e)
		}
	}
}
