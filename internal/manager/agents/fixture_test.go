package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/uptrace/bun"

	agentjobs "github.com/neurekadev/dockyard/internal/agent/jobs"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/state"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/server"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

const testManagerVersion = "1.4.0"

var testUI = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html><body>DockYard</body></html>")}}

// fixture is a real manager HTTP stack (server.New with the agent handler,
// the /agent/v1 guard and credential separation) over a migrated SQLite
// database, the job engine wired to the session hub, and a fake clock
// shared by manager and test agents: timers only fire when a test advances
// it, so nothing depends on wall-clock timing.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	clk   *clock.Fake
	db    *bun.DB
	bus   *events.Bus
	sub   *events.Subscription
	svc   *Service
	audit *audit.Log
	jobs  *jobs.Engine
	srv   *httptest.Server
	logs  *testutil.LogBuffer
}

type fixtureOptions struct {
	session        SessionOptions
	attempts       *AttemptLimits
	managerVersion string
}

func newFixture(t *testing.T, o ...fixtureOptions) *fixture {
	t.Helper()
	var opts fixtureOptions
	if len(o) > 0 {
		opts = o[0]
	}
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clk := testutil.FakeClock()
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: clk, Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := testutil.CaptureLogger()
	bus := events.New(clk)
	attempts := AttemptLimits{EnrollPerMinute: 6000, EnrollBurst: 1000, SessionPerMinute: 6000, SessionBurst: 1000}
	if opts.attempts != nil {
		attempts = *opts.attempts
	}
	managerVersion := testManagerVersion
	if opts.managerVersion != "" {
		managerVersion = opts.managerVersion
	}
	f := &fixture{t: t, ctx: ctx, clk: clk, db: db, bus: bus, sub: bus.Subscribe(4096, nil), logs: logs}
	pub, _ := url.Parse("https://docker.example.com")
	if f.audit, err = audit.New(audit.Options{DB: db, Clock: clk, Logger: logger}); err != nil {
		t.Fatal(err)
	}
	f.svc, err = New(Options{DB: db, Clock: clk, Logger: logger, Keyring: secrets.NewKeyring(key), Bus: bus, Audit: f.audit,
		ManagerVersion: managerVersion, PublicURL: pub, Session: opts.session, Attempts: attempts})
	if err != nil {
		t.Fatal(err)
	}
	allow := authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision {
		return authz.Allow("test")
	})
	f.jobs, err = jobs.New(jobs.Options{DB: db, Clock: clk, Logger: logger, Dispatcher: f.svc.Hub(), Authorizer: allow})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.jobs.Close)
	f.svc.AttachJobs(f.jobs)
	srv, err := server.New(server.Options{Logger: logger, Clock: clk, UI: testUI, Agent: f.svc.Handler(),
		AgentLimits: server.AgentLimits{RequestsPerSecond: 1000, Burst: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(srv.Handler)
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.svc.Hub().Shutdown(sctx)
		f.srv.Close()
		if t.Failed() {
			t.Logf("manager logs:\n%s", logs.String())
		}
	})
	return f
}

func (f *fixture) wsURL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + protocol.SessionPath
}

// createEnrollment creates an enrollment through the service.
func (f *fixture) createEnrollment(spec domain.EnrollmentSpec) domain.CreatedEnrollment {
	f.t.Helper()
	c, err := f.svc.CreateEnrollment(f.ctx, spec)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// enrollHTTP posts an enrollment request and returns the status and body.
func (f *fixture) enrollHTTP(token string, body any, hdr ...string) (int, []byte) {
	f.t.Helper()
	var b []byte
	switch v := body.(type) {
	case []byte:
		b = v
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			f.t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.srv.URL+protocol.EnrollPath, bytes.NewReader(b))
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func decodeErr(t *testing.T, b []byte) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("not an error body: %s", b)
	}
	return e
}

// waitEvent reads bus events until one of type typ (for resource id, if
// set) arrives.
func (f *fixture) waitEvent(typ, id string) events.Event {
	f.t.Helper()
	for {
		select {
		case e := <-f.sub.C():
			if e.Type == typ && (id == "" || e.ResourceID == id) {
				return e
			}
		case <-f.ctx.Done():
			f.t.Fatalf("event %s %s did not arrive", typ, id)
		}
	}
}

// engineInfo is the fake Docker Engine identity of test agents.
func engineInfo(id string) protocol.EngineInfo {
	return protocol.EngineInfo{ID: id, Version: "28.5.2", APIVersion: "1.51", MinAPIVersion: "1.24", OS: "linux", Arch: "amd64"}
}

// testAgent is a real agent session client (internal/agent/session) with
// its state directory and job runner, like the agent binary wires them.
type testAgent struct {
	t        *testing.T
	f        *fixture
	engineID string
	hostname string
	version  string
	store    *state.Store
	install  string
	client   *session.Client
	runner   *agentjobs.Runner
	statuses chan session.Status
	cancel   context.CancelFunc
	done     chan error
	execs    []jobexec.Executor
	// streams are the agent's stream handlers (#15, #8).
	streams map[string]session.StreamHandler
	// redial, when set, is waited for before every dial after the first
	// (holds reconnects).
	redial chan struct{}
	dials  atomic.Int32
}

func (f *fixture) newAgent(engineID, hostname string, execs ...jobexec.Executor) *testAgent {
	f.t.Helper()
	st, err := state.Open(f.t.TempDir())
	if err != nil {
		f.t.Fatal(err)
	}
	install, err := st.InstallID()
	if err != nil {
		f.t.Fatal(err)
	}
	return &testAgent{t: f.t, f: f, engineID: engineID, hostname: hostname, version: testManagerVersion, store: st, install: install, execs: execs}
}

func (a *testAgent) enrollRequest() protocol.EnrollRequest {
	return protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: a.version, InstallID: a.install,
		Engine: engineInfo(a.engineID), Hostname: a.hostname}
}

// enroll exchanges token over HTTP and stores the credential.
func (a *testAgent) enroll(token string) protocol.EnrollResponse {
	a.t.Helper()
	code, body := a.f.enrollHTTP(token, a.enrollRequest())
	if code != http.StatusCreated {
		a.t.Fatalf("enroll: %d %s", code, body)
	}
	var resp protocol.EnrollResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		a.t.Fatal(err)
	}
	if err := resp.Validate(); err != nil {
		a.t.Fatal(err)
	}
	if err := a.store.SaveCredential(state.Credential{AgentID: resp.AgentID, EnvironmentID: resp.EnvironmentID,
		Credential: resp.Credential, ManagerURL: a.f.srv.URL}); err != nil {
		a.t.Fatal(err)
	}
	return resp
}

func (a *testAgent) capabilities() (protocol.CapabilitiesPayload, bool) {
	cmds := []string{}
	for _, e := range a.execs {
		cmds = append(cmds, string(e.Kind))
	}
	return protocol.CapabilitiesPayload{AgentVersion: a.version, Protocols: []string{protocol.Version}, OS: "linux", Arch: "amd64",
		Engine: engineInfo(a.engineID), Commands: cmds, Requests: []string{}, Streams: []string{},
		Transport: protocol.TransportInfo{ManagerURL: a.f.srv.URL, PlainHTTP: true}}, true
}

// start runs the session client until the test ends (or stop).
func (a *testAgent) start() {
	a.t.Helper()
	ctx, cancel := context.WithCancel(a.f.ctx)
	a.cancel = cancel
	a.statuses = make(chan session.Status, 256)
	a.client = session.New(session.Options{
		State: a.store, Clock: a.f.clk, Logger: testutil.Logger(a.t), URL: a.f.wsURL(),
		DialOptions: func(h http.Header) *websocket.DialOptions { return &websocket.DialOptions{HTTPHeader: h} },
		Dial: func(ctx context.Context, u string, o *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
			if a.dials.Add(1) > 1 && a.redial != nil {
				select {
				case <-a.redial:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			}
			return websocket.Dial(ctx, u, o)
		},
		AgentVersion: a.version, UserAgent: "dockyard-agent/test", Capabilities: a.capabilities, Streams: a.streams,
		Backoff: session.Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: func() float64 { return 0 }},
		OnStatus: func(s session.Status) {
			select {
			case a.statuses <- s:
			default:
			}
		},
	})
	var err error
	a.runner, err = agentjobs.New(ctx, agentjobs.Options{StateDir: a.store.Dir(), Clock: a.f.clk, Logger: testutil.Logger(a.t),
		Sender: a.client, Executors: a.execs})
	if err != nil {
		a.t.Fatal(err)
	}
	a.client.SetRunner(a.runner)
	a.done = make(chan error, 1)
	go func() { a.done <- a.client.Run(ctx) }()
	a.t.Cleanup(a.stop)
}

func (a *testAgent) stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	<-a.done
	a.runner.Wait()
	a.cancel = nil
}

// waitState waits for a session status.
func (a *testAgent) waitState(want string) session.Status {
	a.t.Helper()
	for {
		select {
		case s := <-a.statuses:
			if s.State == want {
				return s
			}
		case <-a.f.ctx.Done():
			a.t.Fatalf("agent did not reach state %s", want)
		}
	}
}

// waitOnline starts nothing; it waits until the manager reports env online.
func (f *fixture) waitOnline(envID string) {
	f.t.Helper()
	f.waitEvent(events.EnvironmentOnline, envID)
	if !f.svc.Hub().Online(envID) {
		f.t.Fatalf("environment %s not online in the hub", envID)
	}
}

// dialRaw opens a session WebSocket with credential (no agent logic).
func (f *fixture) dialRaw(credential string, subprotocols ...string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if credential != "" {
		h.Set("Authorization", "Bearer "+credential)
	}
	if subprotocols == nil {
		subprotocols = []string{protocol.Version}
	}
	return websocket.Dial(f.ctx, f.wsURL(), &websocket.DialOptions{HTTPHeader: h, Subprotocols: subprotocols})
}

// upgradeStatus dials a session with credential and returns the HTTP
// status of the upgrade (101 on success; the connection is closed).
func (f *fixture) upgradeStatus(credential string) int {
	f.t.Helper()
	c, resp, _ := f.dialRaw(credential)
	if c != nil {
		_ = c.CloseNow()
	}
	if resp == nil {
		return 0
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	return resp.StatusCode
}

// rawSession is a hand-driven session for handshake tests.
type rawSession struct {
	t  *testing.T
	c  *websocket.Conn
	n  int
	fx *fixture
}

func (f *fixture) raw(credential string) *rawSession {
	f.t.Helper()
	c, resp, err := f.dialRaw(credential)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		f.t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(protocol.MaxFrameSize)
	f.t.Cleanup(func() { _ = c.CloseNow() })
	return &rawSession{t: f.t, c: c, fx: f}
}

func (r *rawSession) send(typ protocol.Type, correlation string, payload any) string {
	r.t.Helper()
	r.n++
	id := "t." + string(typ) + "." + strconv.Itoa(r.n)
	f, err := protocol.NewFrame(typ, id, correlation, protocol.JobRef{}, payload)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := protocol.WriteFrame(r.fx.ctx, r.c, f); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// read returns the next non-heartbeat frame, or the close error.
func (r *rawSession) read() (*protocol.Frame, error) {
	for {
		f, err := protocol.ReadFrame(r.fx.ctx, r.c)
		if err != nil {
			return nil, err
		}
		if f.Type != protocol.TypeHeartbeat {
			return f, nil
		}
	}
}

// closeCode reads until the connection closes and returns the code.
func (r *rawSession) closeCode() websocket.StatusCode {
	r.t.Helper()
	for {
		_, err := r.read()
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}
