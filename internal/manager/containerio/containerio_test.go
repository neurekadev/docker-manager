package containerio_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentio "github.com/neurekadev/docker-manager/internal/agent/containerio"
	"github.com/neurekadev/docker-manager/internal/agent/containerio/ciotest"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/authztest"
	"github.com/neurekadev/docker-manager/internal/manager/containerio"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/streammux/muxtest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const env1 = "env-1"

// codedErr is an agent error frame as the hub returns it.
type codedErr struct{ code, msg string }

func (e codedErr) Error() string           { return e.code + ": " + e.msg }
func (e codedErr) ProtocolCode() string    { return e.code }
func (e codedErr) ProtocolMessage() string { return e.msg }

// loop is a fake session hub over the real agent service.
type loop struct {
	mu      sync.Mutex
	reqs    map[string]session.RequestHandler
	pipe    *muxtest.Pipe
	calls   []string
	offline bool
	watched string
	seen    chan struct{}
	// features are the capabilities features the agent announced.
	features map[string]bool
}

func (l *loop) EnvironmentHasFeature(env, feature string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return env == env1 && !l.offline && l.features[feature]
}

// watch makes the next request named name signal seen.
func (l *loop) watch(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.watched, l.seen = name, make(chan struct{})
}

func (l *loop) RequestEnvironment(ctx context.Context, env, name string, input any, _ time.Duration) (json.RawMessage, error) {
	l.mu.Lock()
	l.calls = append(l.calls, name)
	off := l.offline
	if l.watched == name {
		close(l.seen)
		l.watched = ""
	}
	l.mu.Unlock()
	if off || env != env1 {
		return nil, jobs.ErrAgentOffline
	}
	b, _ := json.Marshal(input)
	out, err := l.reqs[name](ctx, b)
	var he *session.HandlerError
	if errors.As(err, &he) {
		return nil, codedErr{he.Code, he.Message}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (l *loop) OpenStream(ctx context.Context, env, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error) {
	if env != env1 {
		return nil, jobs.ErrAgentOffline
	}
	return l.pipe.Open(ctx, kind, input, o)
}

func (l *loop) called(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		if c == name {
			n++
		}
	}
	return n
}

// agentsSvc and dockerSvc answer only what the log and exec routes use.
type agentsSvc struct{ api.AgentService }

func (agentsSvc) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	if id != env1 {
		return domain.Environment{}, domain.ErrEnvironmentNotFound
	}
	return domain.Environment{ID: env1, Name: "NAS", Status: domain.EnvironmentActive, Online: true}, nil
}

type dockerSvc struct{ api.DockerService }

func (dockerSvc) InspectContainer(_ context.Context, env, ref string) (protocol.ContainerDetails, error) {
	if env != env1 || (ref != "web" && ref != "web-id") {
		return protocol.ContainerDetails{}, &domain.DockerError{Code: domain.DockerNotFound, Message: "no such container"}
	}
	return protocol.ContainerDetails{ContainerSummary: protocol.ContainerSummary{ID: "web", Name: "web", State: "running"}}, nil
}

func (dockerSvc) StackIDs(context.Context, string) map[string]string { return nil }

type memAudit struct {
	mu  sync.Mutex
	evs []domain.AuditEvent
}

func (m *memAudit) Record(_ context.Context, ev domain.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, ev)
	return nil
}

func (m *memAudit) Records(context.Context, domain.AuditFilter) ([]domain.AuditRecord, error) {
	return nil, nil
}

func (m *memAudit) find(action string) []domain.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.AuditEvent
	for _, e := range m.evs {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (m *memAudit) dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.evs)
	return string(b)
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock.Fake
	eng   *ciotest.Engine
	loop  *loop
	svc   *containerio.Service
	pol   *authztest.Policy
	audit *memAudit
	srv   *httptest.Server
	logs  *testutil.LogBuffer
	base  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: testutil.Context(t), clk: testutil.FakeClock(), eng: ciotest.New(), audit: &memAudit{}}
	logger, logs := testutil.CaptureLogger()
	f.logs = logs
	agent := agentio.New(agentio.Options{Engine: func() agentio.Engine { return f.eng }, Clock: f.clk, Logger: logger})
	hs := map[string]muxtest.Handler{}
	for k, h := range agent.Streams() {
		hs[k] = muxtest.Handler(h)
	}
	f.loop = &loop{reqs: agent.Requests(), pipe: muxtest.New(t, hs)}
	f.svc = containerio.New(containerio.Options{Agents: f.loop, Clock: f.clk, Logger: logger, PingInterval: 24 * time.Hour})
	t.Cleanup(f.svc.Close)
	f.pol = authztest.New().Owner("olga").
		Member("alice", "ops").Group("ops", "allow container.exec @env:"+env1, "allow container.logs.read @env:"+env1).
		Member("mia", "support").Group("support", "allow container.metrics.read @all", "allow container.restart @all", "allow container.details.read @all").
		Token("t-noexec", "alice", "allow container.logs.read @all", "allow container.restart @all").
		Token("t-exec", "alice", "allow container.exec @container:"+env1+"/web")
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Authorizer: f.pol, Agents: agentsSvc{}, Docker: dockerSvc{}, ContainerIO: f.svc, Clock: f.clk, Audit: f.audit,
		SSEHeartbeat: 15 * time.Second})
	f.srv = httptest.NewServer(authztest.Authenticate(mux))
	t.Cleanup(f.srv.Close)
	f.base = "/api/v1/environments/" + env1 + "/containers/web"
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", logs.String())
		}
	})
	return f
}

type resp struct {
	status int
	body   []byte
}

func (f *fixture) do(user, token, method, path string, body any) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(f.ctx, method, f.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(authztest.UserHeader, user)
	if token != "" {
		req.Header.Set(authztest.TokenHeader, token)
	}
	r, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, b}
}

func must(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
}

func entry(sec int, text string) engine.LogEntry {
	return ciotest.Entry(testutil.Epoch, sec, engine.Stdout, text)
}

// TestLogsAuthorizationAndTail: only container.logs.read opens logs:
// metrics, restart and details grants do not, tokens need it in their own
// scope, strangers do not learn the container exists.
func TestLogsAuthorizationAndTail(t *testing.T) {
	f := newFixture(t)
	f.eng.SetHistory(entry(1, "one\n"), entry(2, "two\n"), engine.LogEntry{Time: testutil.Epoch.Add(3 * time.Second), Stream: engine.Stderr, Data: []byte("bad \xff\n")})
	r := f.do("alice", "", http.MethodGet, f.base+"/logs?tail=10", nil)
	must(t, r, 200)
	var out api.ContainerLogs
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 3 || out.Lines[0].Line != "one" || out.Lines[2].Stream != "stderr" || out.Lines[2].Line != "bad �" {
		t.Fatalf("lines %+v", out.Lines)
	}
	must(t, f.do("alice", "", http.MethodGet, f.base+"/logs?stderr=false", nil), 200)
	must(t, f.do("mia", "", http.MethodGet, f.base+"/logs", nil), 403)
	must(t, f.do("mia", "", http.MethodGet, f.base+"/logs/stream", nil), 403)
	must(t, f.do("stranger", "", http.MethodGet, f.base+"/logs", nil), 404)
	must(t, f.do("alice", "t-exec", http.MethodGet, f.base+"/logs", nil), 403) // token without logs in its scope
	must(t, f.do("alice", "t-noexec", http.MethodGet, f.base+"/logs", nil), 200)
	must(t, f.do("alice", "", http.MethodGet, "/api/v1/environments/"+env1+"/containers/nope/logs", nil), 404)
	f.loop.mu.Lock()
	f.loop.offline = true
	f.loop.mu.Unlock()
	r = f.do("alice", "", http.MethodGet, f.base+"/logs", nil)
	if r.status != 503 || !strings.Contains(string(r.body), "environment_offline") {
		t.Fatalf("offline: %d %s", r.status, r.body)
	}
}

// sse reads server-sent events from a response body.
type sse struct {
	sc *bufio.Scanner
}

type event struct{ id, name, data string }

func (s *sse) next(t *testing.T) event {
	t.Helper()
	var e event
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if e.name != "" || e.data != "" {
				return e
			}
		case strings.HasPrefix(line, "id: "):
			e.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			e.name = line[7:]
		case strings.HasPrefix(line, "data: "):
			e.data = line[6:]
		}
	}
	t.Fatalf("stream ended: %v", s.sc.Err())
	return e
}

func (f *fixture) openSSE(user, path string, header ...string) (*sse, func()) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+path, nil)
	req.Header.Set(authztest.UserHeader, user)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	r, err := f.srv.Client().Do(req) //nolint:bodyclose // closed by the returned stop function
	if err != nil {
		f.t.Fatal(err)
	}
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		f.t.Fatalf("SSE %d %s", r.StatusCode, b)
	}
	return &sse{sc: bufio.NewScanner(r.Body)}, func() { cancel(); _ = r.Body.Close() }
}

// TestLogStreamFollowsResumesAndEnds: the SSE stream relays the agent's
// lines with timestamp cursors, resumes after Last-Event-ID, ends with
// container_removed or permissions_changed.
func TestLogStreamFollowsResumesAndEnds(t *testing.T) {
	f := newFixture(t)
	f.eng.SetHistory(entry(1, "one\n"), entry(2, "two\n"))
	s, stop := f.openSSE("alice", f.base+"/logs/stream?tail=5")
	e := s.next(t)
	if e.name != "log" || e.id != testutil.Epoch.Add(time.Second).Format(time.RFC3339Nano) || !strings.Contains(e.data, `"line":"one"`) {
		t.Fatalf("first event %+v", e)
	}
	if e = s.next(t); !strings.Contains(e.data, `"line":"two"`) {
		t.Fatalf("second %+v", e)
	}
	f.eng.Emit(entry(3, "live\n"))
	if e = s.next(t); e.name != "log" || !strings.Contains(e.data, `"line":"live"`) {
		t.Fatalf("live %+v", e)
	}
	stop()

	// Reconnect after "two": the agent resumes at its timestamp.
	s, stop = f.openSSE("alice", f.base+"/logs/stream", "Last-Event-ID", testutil.Epoch.Add(2*time.Second).Format(time.RFC3339Nano))
	if e = s.next(t); !strings.Contains(e.data, `"line":"two"`) {
		t.Fatalf("resume %+v (the line at the cursor may repeat)", e)
	}
	if e = s.next(t); !strings.Contains(e.data, `"line":"live"`) {
		t.Fatalf("resume %+v", e)
	}
	// Losing container.logs.read ends the stream at the next heartbeat.
	f.pol.Group("ops", "allow container.exec @env:"+env1)
	if err := f.clk.BlockUntilWaiters(f.ctx, 2); err != nil { // heartbeat ticker, max-age timer
		t.Fatal(err)
	}
	f.clk.Advance(15 * time.Second)
	if e = s.next(t); e.name != "end" || !strings.Contains(e.data, "permissions_changed") {
		t.Fatalf("revoked %+v", e)
	}
	stop()
	f.pol.Group("ops", "allow container.exec @env:"+env1, "allow container.logs.read @env:"+env1)

	// Removal ends it.
	s, stop = f.openSSE("alice", f.base+"/logs/stream?tail=1")
	s.next(t)
	f.eng.Remove()
	if e = s.next(t); e.name != "end" || !strings.Contains(e.data, "container_removed") {
		t.Fatalf("removed %+v", e)
	}
	stop()
}

// wsURL returns the WebSocket URL of a path.
func (f *fixture) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + path
}

type execSession struct {
	ID        string   `json:"id"`
	StreamURL string   `json:"streamUrl"`
	Ticket    string   `json:"ticket"`
	Command   []string `json:"command"`
}

func (f *fixture) create(user, token string, body any) execSession {
	f.t.Helper()
	r := f.do(user, token, http.MethodPost, f.base+"/exec-sessions", body)
	must(f.t, r, 201)
	var s execSession
	if err := json.Unmarshal(r.body, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// attach dials the session's WebSocket; status is the HTTP status of a
// refused upgrade (0 without a response).
func (f *fixture) attach(user, token string, s execSession, ticket string) (*websocket.Conn, int, error) {
	h := http.Header{authztest.UserHeader: {user}}
	if token != "" {
		h.Set(authztest.TokenHeader, token)
	}
	c, r, err := websocket.Dial(f.ctx, f.wsURL(s.StreamURL), &websocket.DialOptions{HTTPHeader: h,
		Subprotocols: []string{api.ExecSubprotocol, api.ExecTicketPrefix + ticket}})
	status := 0
	if r != nil {
		status = r.StatusCode
		if r.Body != nil {
			_ = r.Body.Close()
		}
	}
	return c, status, err
}

func readUntilClose(t *testing.T, ctx context.Context, c *websocket.Conn) ([]byte, []string, websocket.StatusCode) {
	t.Helper()
	var out []byte
	var texts []string
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return out, texts, websocket.CloseStatus(err)
		}
		if typ == websocket.MessageBinary {
			out = append(out, b...)
		} else {
			texts = append(texts, string(b))
		}
	}
}

// TestExecSessionEndToEnd: create, attach with the one-use ticket, relay
// stdin/stdout, resize, exit code, close 1000 and the audited end; the
// ticket cannot be reused and a second attachment is refused.
func TestExecSessionEndToEnd(t *testing.T) {
	f := newFixture(t)
	const canary = "CANARY-TERMINAL-INPUT-77ab31"
	f.eng.SetProcess(func(stdin io.Reader, stdout, _ io.Writer) int {
		b := make([]byte, 64)
		n, _ := stdin.Read(b)
		_, _ = stdout.Write(bytes.ToUpper(b[:n]))
		_, _ = io.Copy(io.Discard, stdin)
		return 7
	})
	s := f.create("alice", "", map[string]any{"command": []string{"/bin/sh"}, "cols": 100, "rows": 30})
	if s.Ticket == "" || !strings.HasSuffix(s.StreamURL, "/exec-sessions/"+s.ID+"/stream") {
		t.Fatalf("session %+v", s)
	}
	if spec := f.eng.Execs()["exec-1"].Spec; !spec.Tty || spec.Width != 100 || spec.Height != 30 || spec.Cmd[0] != "/bin/sh" {
		t.Fatalf("exec spec %+v", spec)
	}
	// A wrong ticket, another user and no ticket are refused before the upgrade.
	for _, try := range []struct{ user, ticket string }{{"alice", "wrong"}, {"olga", s.Ticket}, {"alice", ""}} {
		_, status, err := f.attach(try.user, "", s, try.ticket)
		if err == nil || status != http.StatusNotFound {
			t.Fatalf("attach %+v: %v %d", try, err, status)
		}
	}
	c, _, err := f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subprotocol() != api.ExecSubprotocol {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	// A second attachment while attached: 4409.
	c2, _, err := f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := readUntilClose(t, f.ctx, c2); code != 4409 {
		t.Fatalf("second attach closed with %d", code)
	}
	if err := c.Write(f.ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(f.ctx, websocket.MessageBinary, append([]byte{0}, canary...)); err != nil {
		t.Fatal(err)
	}
	// Close stdin through the API (the process then exits).
	var out []byte
	typ, b, err := c.Read(f.ctx)
	if err != nil || typ != websocket.MessageBinary || b[0] != 1 {
		t.Fatalf("output %v %q %v", typ, b, err)
	}
	out = append(out, b[1:]...)
	must(t, f.do("alice", "", http.MethodDelete, f.base+"/exec-sessions/"+s.ID, nil), 204)
	_, _, code := readUntilClose(t, f.ctx, c)
	if string(out) != strings.ToUpper(canary) || code != websocket.StatusNormalClosure {
		t.Fatalf("output %q close %d", out, code)
	}
	if r := f.eng.Resized(); len(r) != 2 || r[0] != 40 || r[1] != 120 {
		t.Fatalf("resize %v", r)
	}
	// The session is gone; its ticket is dead.
	if _, status, err := f.attach("alice", "", s, s.Ticket); err == nil || status != http.StatusNotFound {
		t.Fatalf("reattach: %v", err)
	}
	ends := f.audit.find("container.exec.end")
	if len(ends) != 1 || ends[0].Actor.UserID != "alice" || ends[0].Details["sessionId"] != s.ID {
		t.Fatalf("audit end %+v", ends)
	}
	if len(f.audit.find("container.exec")) != 2 { // create and delete (automatic)
		t.Fatalf("audit %s", f.audit.dump())
	}
	if strings.Contains(f.logs.String(), canary) || strings.Contains(f.audit.dump(), canary) ||
		strings.Contains(f.logs.String(), strings.ToUpper(canary)) {
		t.Fatal("terminal I/O reached the logs or the audit trail")
	}
}

// TestExecFailedUpgradeKeepsTheTicket: the session is claimed before the
// WebSocket upgrade (so an attachment racing with a successful one is
// always refused); an upgrade that fails gives the claim and the one-use
// ticket back.
func TestExecFailedUpgradeKeepsTheTicket(t *testing.T) {
	f := newFixture(t)
	f.eng.SetProcess(func(stdin io.Reader, stdout, _ io.Writer) int {
		_, _ = stdout.Write([]byte("hi"))
		return 0
	})
	s := f.create("alice", "", map[string]any{})
	// A plain request: the upgrade fails before any connection exists.
	w := httptest.NewRecorder()
	a := api.ExecAttach{Principal: authz.Principal{Kind: authz.KindUser, UserID: "alice"}, EnvironmentID: env1,
		ContainerID: "web", SessionID: s.ID, Ticket: s.Ticket, Allowed: func(context.Context) bool { return true }}
	f.svc.Attach(f.ctx, w, httptest.NewRequest(http.MethodGet, s.StreamURL, nil), a)
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("status %d, want 426", w.Code)
	}
	if err := f.svc.CheckAttach(f.ctx, a); err != nil {
		t.Fatalf("the ticket was used up by the failed upgrade: %v", err)
	}
	c, _, err := f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatalf("attach after the failed upgrade: %v", err)
	}
	if out, _, code := readUntilClose(t, f.ctx, c); !strings.Contains(string(out), "hi") || code != websocket.StatusNormalClosure {
		t.Fatalf("output %q close %d", out, code)
	}
}

// TestExecExitAndMissingShell: the exit code arrives as a message before a
// normal close; a command missing from the image ends with a clear error
// and close 4422.
func TestExecExitAndMissingShell(t *testing.T) {
	f := newFixture(t)
	f.eng.SetProcess(func(stdin io.Reader, stdout, _ io.Writer) int {
		_, _ = stdout.Write([]byte("hi"))
		return 3
	})
	s := f.create("alice", "", map[string]any{})
	c, _, err := f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	out, texts, code := readUntilClose(t, f.ctx, c)
	if string(out) != "\x01hi" && string(out) != "hi" {
		t.Fatalf("output %q", out)
	}
	if code != websocket.StatusNormalClosure || len(texts) != 1 || texts[0] != `{"type":"exit","code":3}` {
		t.Fatalf("close %d texts %v", code, texts)
	}
	if ends := f.audit.find("container.exec.end"); len(ends) != 1 || ends[0].Details["exitCode"] != 3 {
		t.Fatalf("audit %+v", ends)
	}

	f.eng.SetProcess(func(_ io.Reader, stdout, _ io.Writer) int {
		_, _ = stdout.Write([]byte(`OCI runtime exec failed: exec: "/bin/sh": executable file not found in $PATH`))
		return 127
	})
	s = f.create("alice", "", map[string]any{"command": []string{"/bin/sh"}})
	c, _, err = f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	_, texts, code = readUntilClose(t, f.ctx, c)
	if code != 4422 || len(texts) != 1 || !strings.Contains(texts[0], "command_not_found") {
		t.Fatalf("missing shell: close %d texts %v", code, texts)
	}
}

// TestExecShellSelection (#8): a shell is resolved by agents announcing
// exec.shell (the first existing path; 422 command_not_found when none
// does); older agents get the shell's most common path; shell and command
// are mutually exclusive; the response names the argv started.
func TestExecShellSelection(t *testing.T) {
	f := newFixture(t)
	lastCmd := func() []string {
		t.Helper()
		xs := f.eng.Execs()
		return xs["exec-"+strconv.Itoa(len(xs))].Spec.Cmd
	}

	// An agent without the feature: the legacy path, no lookup.
	s := f.create("alice", "", map[string]any{"shell": "bash"})
	if got := lastCmd(); len(got) != 1 || got[0] != "/bin/bash" || !slices.Equal(s.Command, got) {
		t.Fatalf("legacy bash: exec %v, response %v", got, s.Command)
	}
	s = f.create("alice", "", map[string]any{})
	if got := lastCmd(); len(got) != 1 || got[0] != "/bin/sh" || !slices.Equal(s.Command, got) {
		t.Fatalf("legacy default: exec %v, response %v", got, s.Command)
	}
	if n := len(f.eng.Stats()); n != 0 {
		t.Fatalf("legacy agent looked up %d paths", n)
	}
	must(t, f.do("alice", "", http.MethodDelete, f.base+"/exec-sessions/"+s.ID, nil), 204)

	// An agent resolving shells: auto picks Bash wherever it lives.
	f.loop.mu.Lock()
	f.loop.features = map[string]bool{protocol.FeatureExecShell: true}
	f.loop.mu.Unlock()
	f.eng.SetPaths("/usr/bin/bash", "/bin/sh")
	s = f.create("alice", "", map[string]any{})
	if got := lastCmd(); len(got) != 1 || got[0] != "/usr/bin/bash" || !slices.Equal(s.Command, got) {
		t.Fatalf("auto: exec %v, response %v", got, s.Command)
	}
	must(t, f.do("alice", "", http.MethodDelete, f.base+"/exec-sessions/"+s.ID, nil), 204)
	s = f.create("alice", "", map[string]any{"shell": "sh"})
	if !slices.Equal(s.Command, []string{"/bin/sh"}) {
		t.Fatalf("sh: response %v", s.Command)
	}
	must(t, f.do("alice", "", http.MethodDelete, f.base+"/exec-sessions/"+s.ID, nil), 204)

	// No zsh in the container: 422 command_not_found, nothing created.
	n := len(f.eng.Execs())
	r := f.do("alice", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{"shell": "zsh"})
	if r.status != 422 || !strings.Contains(string(r.body), "command_not_found") {
		t.Fatalf("missing zsh: %d %s", r.status, r.body)
	}
	if len(f.eng.Execs()) != n {
		t.Fatal("a missing shell created an exec instance")
	}

	// shell and command together, or an unknown shell: 422 before the agent.
	calls := f.loop.called(protocol.ReqContainerExecCreate)
	r = f.do("alice", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{"shell": "bash", "command": []string{"/bin/sh"}})
	if r.status != 422 || !strings.Contains(string(r.body), "body.shell") {
		t.Fatalf("shell and command: %d %s", r.status, r.body)
	}
	must(t, f.do("alice", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{"shell": "fish"}), 422)
	if f.loop.called(protocol.ReqContainerExecCreate) != calls {
		t.Fatal("invalid bodies reached the agent")
	}
}

// TestExecAuthorizationBoundaries: container.exec is needed; metrics,
// restart and details grants never open a terminal; API tokens need
// container.exec in their own scope (#31); strangers get 404.
func TestExecAuthorizationBoundaries(t *testing.T) {
	f := newFixture(t)
	must(t, f.do("mia", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{}), 403)
	must(t, f.do("stranger", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{}), 404)
	must(t, f.do("alice", "t-noexec", http.MethodPost, f.base+"/exec-sessions", map[string]any{}), 403)
	// Refusals never reach the agent.
	if n := f.loop.called(protocol.ReqContainerExecCreate); n != 0 {
		t.Fatalf("refused requests created %d agent execs", n)
	}
	s := f.create("alice", "t-exec", map[string]any{})
	if n := f.loop.called(protocol.ReqContainerExecCreate); n != 1 {
		t.Fatalf("agent exec creations %d, want 1", n)
	}
	// Only the same principal attaches: the user's cookie session is not
	// the token.
	if _, status, err := f.attach("alice", "", s, s.Ticket); err == nil || status != http.StatusNotFound {
		t.Fatalf("other principal attach: %v", err)
	}
	c, _, err := f.attach("alice", "t-exec", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	must(t, f.do("mia", "", http.MethodDelete, f.base+"/exec-sessions/"+s.ID, nil), 403)
	must(t, f.do("alice", "", http.MethodDelete, f.base+"/exec-sessions/nope", nil), 404)
	// A stopped container: 409.
	f.eng.SetRunning(false)
	r := f.do("alice", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{})
	if r.status != 409 {
		t.Fatalf("stopped container: %d %s", r.status, r.body)
	}
}

// TestExecLimitsTimeoutsAndCleanup: attach window, idle timeout,
// permission revocation, per-user limit and client disconnect cleanup.
func TestExecLimitsTimeoutsAndCleanup(t *testing.T) {
	f := newFixture(t)
	running := make(chan struct{}, 8)
	f.eng.SetProcess(func(stdin io.Reader, _, _ io.Writer) int {
		running <- struct{}{}
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	})
	// Not attached within 60 s: gone (and the agent's exec is released).
	s := f.create("alice", "", map[string]any{})
	if err := f.clk.BlockUntilWaiters(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(61 * time.Second)
	if _, status, err := f.attach("alice", "", s, s.Ticket); err == nil || status != http.StatusNotFound {
		t.Fatalf("expired attach: %v", err)
	}

	// Idle timeout: 4408.
	s = f.create("alice", "", map[string]any{})
	c, _, err := f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	<-running
	// Waiters: attach-window timer, idle timer, max-duration timer,
	// recheck ticker, keep-alive ticker.
	if err := f.clk.BlockUntilWaiters(f.ctx, 5); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(30 * time.Minute)
	if _, _, code := readUntilClose(t, f.ctx, c); code != 4408 {
		t.Fatalf("idle close %d", code)
	}

	// Revoked container.exec: 4403 at the next re-check.
	s = f.create("alice", "", map[string]any{})
	c, _, err = f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	<-running
	if err := f.clk.BlockUntilWaiters(f.ctx, 5); err != nil {
		t.Fatal(err)
	}
	f.pol.Group("ops", "allow container.logs.read @env:"+env1)
	f.clk.Advance(15 * time.Second)
	if _, _, code := readUntilClose(t, f.ctx, c); code != 4403 {
		t.Fatalf("revoked close %d", code)
	}
	f.pol.Group("ops", "allow container.exec @env:"+env1, "allow container.logs.read @env:"+env1)

	// The client disconnects: the session ends, the agent closes stdin
	// (the process sees end of input and exits).
	s = f.create("alice", "", map[string]any{})
	c, _, err = f.attach("alice", "", s, s.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	<-running
	f.loop.watch(protocol.ReqContainerExecDelete)
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	select {
	case <-f.loop.seen:
	case <-f.ctx.Done():
		t.Fatal("agent exec not released after the disconnect")
	}

	// At most 4 open sessions per user.
	for range 4 {
		f.create("alice", "", map[string]any{})
	}
	if r := f.do("alice", "", http.MethodPost, f.base+"/exec-sessions", map[string]any{}); r.status != 429 {
		t.Fatalf("5th session: %d %s", r.status, r.body)
	}
}
