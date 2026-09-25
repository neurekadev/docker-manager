package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authsep"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// enrolledRaw enrolls a fake installation and returns its credential and
// the hello payload it must send.
func (f *fixture) enrolledRaw(engineID string) (protocol.EnrollResponse, protocol.HelloPayload) {
	f.t.Helper()
	a := f.newAgent(engineID, "host-"+engineID)
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	return r, protocol.HelloPayload{Protocol: protocol.Version, AgentID: r.AgentID, AgentVersion: testManagerVersion,
		InstallID: a.install, EngineID: engineID}
}

func caps(engineID string) protocol.CapabilitiesPayload {
	return protocol.CapabilitiesPayload{AgentVersion: testManagerVersion, Protocols: []string{protocol.Version}, OS: "linux", Arch: "amd64",
		Engine: engineInfo(engineID), Commands: []string{}, Requests: []string{}, Streams: []string{},
		Transport: protocol.TransportInfo{ManagerURL: "https://docker.example.com"}}
}

// handshake completes hello/welcome/capabilities/job_report on a raw
// session and returns the welcome.
func (r *rawSession) handshake(hello protocol.HelloPayload) protocol.WelcomePayload {
	r.t.Helper()
	hid := r.send(protocol.TypeHello, "", hello)
	f, err := r.read()
	if err != nil || f.Type != protocol.TypeWelcome || f.CorrelationID != hid {
		r.t.Fatalf("welcome: %+v %v", f, err)
	}
	w, err := protocol.DecodePayload[protocol.WelcomePayload](f)
	if err != nil || w.Validate() != nil {
		r.t.Fatalf("welcome payload %+v %v", w, err)
	}
	r.send(protocol.TypeCapabilities, "", caps(hello.EngineID))
	rid := r.send(protocol.TypeJobReport, "", protocol.JobReportPayload{Jobs: []protocol.JobReportEntry{}})
	ack, err := r.read()
	if err != nil || ack.Type != protocol.TypeAck || ack.CorrelationID != rid {
		r.t.Fatalf("report ack: %+v %v", ack, err)
	}
	return w
}

func TestSessionUpgradeRefusals(t *testing.T) {
	f := newFixture(t)
	r, _ := f.enrolledRaw("ENG-A")
	enroll := f.createEnrollment(domain.EnrollmentSpec{}).Token
	forged, _ := authsep.MintAgentCredential("0190a6e0-0000-7000-8000-00000000beef")
	for name, c := range map[string]struct {
		cred   string
		sub    []string
		origin string
		status int
	}{
		"no credential":               {"", nil, "", http.StatusUnauthorized},
		"unknown credential":          {forged.Token, nil, "", http.StatusUnauthorized},
		"enrollment token":            {enroll, nil, "", http.StatusUnauthorized},
		"wrong secret":                {wrongSecret(t, r.Credential), nil, "", http.StatusUnauthorized},
		"no subprotocol":              {r.Credential, []string{"other"}, "", http.StatusUpgradeRequired},
		"browser (Origin) with a key": {r.Credential, nil, "https://docker.example.com", http.StatusForbidden},
	} {
		h := http.Header{}
		if c.cred != "" {
			h.Set("Authorization", "Bearer "+c.cred)
		}
		if c.origin != "" {
			h.Set("Origin", c.origin)
		}
		sub := c.sub
		if sub == nil {
			sub = []string{protocol.Version}
		}
		conn, resp, err := websocket.Dial(f.ctx, f.wsURL(), &websocket.DialOptions{HTTPHeader: h, Subprotocols: sub})
		if conn != nil {
			_ = conn.CloseNow()
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		if err == nil || code != c.status {
			t.Errorf("%s: %d %v, want %d", name, code, err, c.status)
		}
	}
	if strings.Contains(f.logs.String(), strings.SplitN(strings.TrimPrefix(r.Credential, "dya_"), "_", 2)[1]) {
		t.Fatal("credential secret logged")
	}
}

func wrongSecret(t *testing.T, cred string) string {
	t.Helper()
	id, _, ok := authsep.ParseAgentCredential(cred)
	if !ok {
		t.Fatal("bad credential")
	}
	m, _ := authsep.MintAgentCredential(id)
	return m.Token
}

func TestHandshakeRefusals(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	cases := []struct {
		name  string
		first func(*rawSession)
		code  websocket.StatusCode
	}{
		{"first frame not hello", func(s *rawSession) { s.send(protocol.TypeHeartbeat, "", nil) }, protocol.CloseProtocolError},
		{"protocol mismatch", func(s *rawSession) {
			h := hello
			h.Protocol = "dockyard.agent/v2"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseVersionUnsupported},
		{"agent too old", func(s *rawSession) {
			h := hello
			h.AgentVersion = "1.2.0"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseVersionUnsupported},
		{"agent newer than manager", func(s *rawSession) {
			h := hello
			h.AgentVersion = "2.0.0"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseVersionUnsupported},
		{"hello for another agent", func(s *rawSession) {
			h := hello
			h.AgentID = "0190a6e0-0000-7000-8000-000000000999"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseUnauthorized},
		{"hello from another install", func(s *rawSession) {
			h := hello
			h.InstallID = "0190a6e0-0000-7000-8000-000000000998"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseUnauthorized},
		{"another engine", func(s *rawSession) {
			h := hello
			h.EngineID = "ENG-B"
			s.send(protocol.TypeHello, "", h)
		}, protocol.CloseRevoked},
		{"binary message", func(s *rawSession) {
			_ = s.c.Write(f.ctx, websocket.MessageBinary, []byte("{}"))
		}, protocol.CloseProtocolError},
		{"unknown field", func(s *rawSession) {
			_ = s.c.Write(f.ctx, websocket.MessageText, []byte(`{"type":"hello","id":"h1","payload":{},"extra":1}`))
		}, protocol.CloseProtocolError},
	}
	for _, c := range cases {
		s := f.raw(r.Credential)
		c.first(s)
		if got := s.closeCode(); got != c.code {
			t.Errorf("%s: close %d, want %d", c.name, got, c.code)
		}
	}
	// The refusals changed nothing: the agent can still connect.
	s := f.raw(r.Credential)
	w := s.handshake(hello)
	if w.EnvironmentID != r.EnvironmentID || w.AgentStatus != protocol.VersionCurrent {
		t.Fatalf("welcome %+v", w)
	}
	f.waitOnline(r.EnvironmentID)
}

// TestHelloTimeout: a connection that never says hello is closed with 4400
// after HelloTimeout (fake clock).
func TestHelloTimeout(t *testing.T) {
	f := newFixture(t)
	r, _ := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	if err := f.clk.BlockUntilWaiters(f.ctx, 2); err != nil { // hello timer + heartbeat ticker
		t.Fatal(err)
	}
	f.clk.Advance(protocol.HelloTimeout)
	if got := s.closeCode(); got != protocol.CloseProtocolError {
		t.Fatalf("close %d", got)
	}
}

// TestHeartbeatTimeout: a silent agent is closed with 4408, and the
// environment goes offline (persisted and published).
func TestHeartbeatTimeout(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	if err := f.clk.BlockUntilWaiters(f.ctx, 2); err != nil { // heartbeat ticker + watchdog
		t.Fatal(err)
	}
	f.clk.Advance(protocol.HeartbeatTimeout)
	if got := s.closeCode(); got != protocol.CloseHeartbeatTimeout {
		t.Fatalf("close %d", got)
	}
	f.waitEvent(events.EnvironmentOffline, r.EnvironmentID)
	env, _ := f.svc.GetEnvironment(f.ctx, r.EnvironmentID)
	if env.Online || env.ConnectionChangedAt == nil {
		t.Fatalf("environment %+v", env)
	}
}

// TestNewerSessionReplacesOlder: a second session with the same credential
// closes the first with 4409 (which the agent treats as final).
func TestNewerSessionReplacesOlder(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s1 := f.raw(r.Credential)
	s1.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	s2 := f.raw(r.Credential)
	s2.handshake(hello)
	if got := s1.closeCode(); got != protocol.CloseReplaced {
		t.Fatalf("old session closed with %d", got)
	}
	// The environment never went offline (no transition event); the new
	// session was reconciled (resync) and is the online one.
	f.waitEvent(events.EnvironmentResync, r.EnvironmentID) // first session
	f.waitEvent(events.EnvironmentResync, r.EnvironmentID) // second session
	if !f.svc.Hub().Online(r.EnvironmentID) {
		t.Fatal("not online after the takeover")
	}
	if f.svc.Hub().EnvironmentSession(r.EnvironmentID) == nil || len(f.svc.Hub().Sessions()) != 1 {
		t.Fatalf("sessions %+v", f.svc.Hub().Sessions())
	}
}

// TestOnlineOnlyAfterReconciliation: the environment is reported online
// only after the job report was reconciled and every reconciler ran.
func TestOnlineOnlyAfterReconciliation(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	entered := make(chan string, 1)
	f.svc.Hub().AddReconciler(func(ctx context.Context, s *Session) error {
		entered <- s.EnvironmentID()
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	hid := s.send(protocol.TypeHello, "", hello)
	if w, err := s.read(); err != nil || w.CorrelationID != hid {
		t.Fatal(err)
	}
	s.send(protocol.TypeCapabilities, "", caps("ENG-A"))
	if f.svc.Hub().Online(r.EnvironmentID) {
		t.Fatal("online before the job report")
	}
	s.send(protocol.TypeJobReport, "", protocol.JobReportPayload{Jobs: []protocol.JobReportEntry{}})
	if env := <-entered; env != r.EnvironmentID {
		t.Fatalf("reconciler for %s", env)
	}
	if f.svc.Hub().Online(r.EnvironmentID) {
		t.Fatal("online while reconciling")
	}
	close(release)
	f.waitOnline(r.EnvironmentID)
	resync := f.waitEvent(events.EnvironmentResync, r.EnvironmentID)
	if resync.Attributes["reason"] != "reconnect" {
		t.Fatalf("resync %+v", resync)
	}
}

func TestSessionRejectsProtocolViolations(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	for name, fn := range map[string]func(*rawSession){
		"job report before capabilities": func(s *rawSession) {
			s.send(protocol.TypeHello, "", hello)
			_, _ = s.read()
			s.send(protocol.TypeJobReport, "", protocol.JobReportPayload{Jobs: []protocol.JobReportEntry{}})
		},
		"manager-only frame": func(s *rawSession) {
			s.handshake(hello)
			s.send(protocol.TypeRescan, "", protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: "stack", ID: "s"}, Path: ".", MaxEntries: 1})
		},
		"invalid payload": func(s *rawSession) {
			s.handshake(hello)
			s.send(protocol.TypeEvent, "", map[string]any{"source": "shell", "type": "container", "action": "x", "at": time.Now(), "seq": 1})
		},
		"capabilities for another engine": func(s *rawSession) {
			s.send(protocol.TypeHello, "", hello)
			_, _ = s.read()
			s.send(protocol.TypeCapabilities, "", caps("ENG-OTHER"))
		},
	} {
		s := f.raw(r.Credential)
		fn(s)
		got := s.closeCode()
		want := protocol.CloseProtocolError
		if name == "capabilities for another engine" {
			want = protocol.CloseRevoked
		}
		if got != want {
			t.Errorf("%s: close %d, want %d", name, got, want)
		}
	}
}

// TestRequestsAndUnsupportedStreams: named requests round-trip through a
// real agent; unknown requests fail with unsupported_request; streams the
// manager does not serve yet are refused.
func TestRequestsAndUnsupportedStreams(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)

	type result struct {
		out json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := f.svc.Hub().RequestEnvironment(f.ctx, r.EnvironmentID, protocol.ReqEngineInfo, map[string]string{"q": "1"}, 0)
		done <- result{out, err}
	}()
	q, err := s.read()
	if err != nil || q.Type != protocol.TypeRequest || q.Deadline == nil {
		t.Fatalf("request %+v %v", q, err)
	}
	p, _ := protocol.DecodePayload[protocol.RequestPayload](q)
	if p.Name != protocol.ReqEngineInfo || string(p.Input) != `{"q":"1"}` {
		t.Fatalf("request payload %+v", p)
	}
	s.send(protocol.TypeResponse, q.ID, protocol.ResponsePayload{Output: json.RawMessage(`{"ok":true}`)})
	if res := <-done; res.err != nil || string(res.out) != `{"ok":true}` {
		t.Fatalf("response %s %v", res.out, res.err)
	}

	go func() {
		out, err := f.svc.Hub().Request(f.ctx, r.AgentID, protocol.ReqContainerList, nil, 0)
		done <- result{out, err}
	}()
	q, _ = s.read()
	s.send(protocol.TypeError, q.ID, protocol.ErrorPayload{Code: protocol.CodeUnsupportedRequest, Message: "no"})
	var re *RequestError
	if res := <-done; !errors.As(res.err, &re) || re.Code != protocol.CodeUnsupportedRequest {
		t.Fatalf("error response %v", res.err)
	}

	// Timeout (fake clock) and offline.
	go func() {
		_, err := f.svc.Hub().Request(f.ctx, r.AgentID, protocol.ReqContainerList, nil, time.Second)
		done <- result{err: err}
	}()
	if _, err := s.read(); err != nil {
		t.Fatal(err)
	}
	if err := f.clk.BlockUntilWaiters(f.ctx, 3); err != nil { // ticker, watchdog, request timer
		t.Fatal(err)
	}
	f.clk.Advance(time.Second)
	if res := <-done; !errors.Is(res.err, ErrRequestTimeout) {
		t.Fatalf("timeout: %v", res.err)
	}
	if _, err := f.svc.Hub().Request(f.ctx, "0190a6e0-0000-7000-8000-000000000123", protocol.ReqEngineInfo, nil, 0); !errors.Is(err, jobs.ErrAgentOffline) {
		t.Fatalf("offline: %v", err)
	}

	// An agent-opened stream is refused (no manager-side streams yet).
	sid := s.send(protocol.TypeStreamOpen, "", protocol.StreamOpenPayload{Kind: protocol.StreamContainerLogs, Direction: protocol.DirAgentToManager})
	cl, err := s.read()
	if err != nil || cl.Type != protocol.TypeStreamClose || cl.CorrelationID != sid {
		t.Fatalf("stream close %+v %v", cl, err)
	}
	// A duplicated frame ID is dropped (no second answer).
	s.send(protocol.TypeHeartbeat, "", nil)
	hb := &protocol.Frame{Type: protocol.TypeHeartbeat, ID: "t.heartbeat.dup"}
	for range 2 {
		if err := protocol.WriteFrame(f.ctx, s.c, hb); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEventAndInvalidationRelay: events and file invalidations are
// published on the bus; duplicates are dropped and a sequence gap makes the
// manager resynchronize (whole environment / all file scopes).
func TestEventAndInvalidationRelay(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	f.waitEvent(events.EnvironmentResync, r.EnvironmentID)
	at := f.clk.Now()
	ev := func(seq uint64, action string) protocol.EventPayload {
		return protocol.EventPayload{Source: "engine", Type: "container", Action: action, ResourceID: "c1", At: at, Seq: seq,
			Attributes: map[string]string{"name": "web-1", "env.SECRET": "canary"}}
	}
	s.send(protocol.TypeEvent, "", ev(1, "start"))
	s.send(protocol.TypeEvent, "", ev(1, "start")) // duplicate seq (retransmission with a new frame ID)
	s.send(protocol.TypeEvent, "", ev(4, "die"))   // gap: 2 and 3 were dropped by the agent
	got := f.waitEvent(events.DockerEvent, "c1")
	if got.Attributes["action"] != "start" || got.Attributes["name"] != "web-1" || got.Attributes["env.SECRET"] != "" || got.EnvironmentID != r.EnvironmentID {
		t.Fatalf("relayed event %+v", got)
	}
	gap := f.waitEvent(events.EnvironmentResync, r.EnvironmentID)
	if gap.Attributes["reason"] != "event_gap" {
		t.Fatalf("gap event %+v", gap)
	}
	if next := f.waitEvent(events.DockerEvent, "c1"); next.Attributes["action"] != "die" {
		t.Fatalf("after gap %+v (the duplicate must have been dropped)", next)
	}

	inv := func(seq uint64, paths ...string) protocol.FSInvalidationPayload {
		return protocol.FSInvalidationPayload{Scope: protocol.ScopeRef{Kind: "stack", ID: "shop"}, Paths: paths, Overflow: len(paths) == 0, At: at, Seq: seq}
	}
	s.send(protocol.TypeFSInvalidation, "", inv(1, "compose.yaml"))
	s.send(protocol.TypeFSInvalidation, "", inv(3))
	first := f.waitEvent(events.FilesInvalidated, "stack:shop")
	if len(first.Paths) != 1 || first.Paths[0] != "compose.yaml" || first.Overflow {
		t.Fatalf("invalidation %+v", first)
	}
	all := f.waitEvent(events.FilesInvalidated, "*")
	if !all.Overflow || all.Attributes["reason"] != "sequence_gap" {
		t.Fatalf("gap invalidation %+v", all)
	}
	if over := f.waitEvent(events.FilesInvalidated, "stack:shop"); !over.Overflow {
		t.Fatalf("overflow invalidation %+v", over)
	}
}

// TestSessionAttemptLimit: repeated session attempts with one credential
// are rate limited (429) independently of the client address.
func TestSessionAttemptLimit(t *testing.T) {
	f := newFixture(t, fixtureOptions{attempts: &AttemptLimits{SessionPerMinute: 1, SessionBurst: 2, EnrollPerMinute: 60, EnrollBurst: 60}})
	r, _ := f.enrolledRaw("ENG-A")
	codes := []int{}
	for range 3 {
		codes = append(codes, f.upgradeStatus(r.Credential))
	}
	if codes[0] != http.StatusSwitchingProtocols || codes[1] != http.StatusSwitchingProtocols || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes %v", codes)
	}
}

// TestAgentStopsOnNonRetryableRefusal: the real agent client stops for good
// on 401 at the upgrade and on 4426, and reconnects after retryable closes.
func TestAgentStopsOnNonRetryableRefusal(t *testing.T) {
	f := newFixture(t)
	a := f.newAgent("ENG-A", "host-a")
	r := a.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	a.version = "1.2.0" // outside the window
	a.start()
	err := <-a.done
	a.cancel = nil
	var stop *session.StopError
	if !errors.As(err, &stop) || stop.CloseCode != protocol.CloseVersionUnsupported || !strings.Contains(stop.Reason, "upgrade the agent") {
		t.Fatalf("version mismatch: %v", err)
	}
	if stop.Unauthorized() {
		t.Fatal("version refusal treated as revoked credential")
	}

	a.version = testManagerVersion
	a.start()
	f.waitOnline(r.EnvironmentID)
	f.waitEvent(events.EnvironmentResync, r.EnvironmentID) // first session online
	a.waitState(session.StateOnline)
	// A retryable close (1011) makes it reconnect and come back online.
	// The new session may attach before the old one's teardown ran; then
	// it takes over without an offline/online transition (and without
	// environment.online), so wait for the new session's resync instead.
	f.svc.Hub().Session(r.AgentID).closeWith(protocol.CloseInternal, "test")
	a.waitState(session.StateDisconnected)
	f.waitEvent(events.EnvironmentResync, r.EnvironmentID) // reconnected session online
	if !f.svc.Hub().Online(r.EnvironmentID) {
		t.Fatal("environment not online after the reconnect")
	}
	a.waitState(session.StateOnline)

	if _, err := f.svc.RemoveAgent(f.ctx, r.AgentID, mustAgent(t, f, r.AgentID).Revision); err != nil {
		t.Fatal(err)
	}
	err = <-a.done
	a.cancel = nil
	if !errors.As(err, &stop) || !stop.Unauthorized() {
		t.Fatalf("revoked: %v", err)
	}
	// Next start: the upgrade itself is refused with 401 and the client stops.
	a.start()
	err = <-a.done
	a.cancel = nil
	if !errors.As(err, &stop) || stop.HTTPStatus != http.StatusUnauthorized || !stop.Unauthorized() {
		t.Fatalf("revoked upgrade: %v", err)
	}
}

// TestAgentVersionWindow (#34): with manager 1.4.0, an N-1 agent (1.3.x)
// enrolls, connects and works, flagged outdated; an older agent (1.2.x)
// and a newer one (1.5.x) are refused with close 4426 and a message saying
// what to upgrade, and do not retry.
func TestAgentVersionWindow(t *testing.T) {
	f := newFixture(t)

	// N-1: accepted, outdated.
	old := f.newAgent("ENG-N1", "host-n1")
	old.version = "1.3.7"
	r := old.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
	old.start()
	f.waitOnline(r.EnvironmentID)
	old.waitState(session.StateOnline)
	a, err := f.svc.GetAgent(f.ctx, r.AgentID)
	if err != nil || a.Version != "1.3.7" || a.VersionStatus != protocol.VersionOutdated {
		t.Fatalf("N-1 agent %+v %v", a, err)
	}
	if st, msg := protocol.AgentCompatibility(testManagerVersion, a.Version); st != protocol.VersionOutdated || msg == "" {
		t.Fatalf("N-1 compatibility %s %q", st, msg)
	}

	for _, c := range []struct{ version, says string }{
		{"1.2.9", "upgrade the agent"},
		{"1.5.0", "upgrade the manager first"},
	} {
		// Enrollment refuses it before anything is created.
		ag := f.newAgent("ENG-"+c.version, "host-"+c.version)
		ag.version = c.version
		code, body := f.enrollHTTP(f.createEnrollment(domain.EnrollmentSpec{}).Token, ag.enrollRequest())
		if code != http.StatusUpgradeRequired || !strings.Contains(string(body), c.says) {
			t.Errorf("%s enrollment: %d %s", c.version, code, body)
		}
		// An enrolled agent whose version left the window is refused at
		// the handshake.
		enrolled := f.newAgent("ENG-X"+c.version, "host-x"+c.version)
		er := enrolled.enroll(f.createEnrollment(domain.EnrollmentSpec{}).Token)
		enrolled.version = c.version
		enrolled.start()
		err := <-enrolled.done
		enrolled.cancel = nil
		var stop *session.StopError
		if !errors.As(err, &stop) || stop.CloseCode != protocol.CloseVersionUnsupported || !strings.Contains(stop.Reason, c.says) ||
			protocol.ReconnectAllowed(stop.CloseCode) || stop.Unauthorized() {
			t.Errorf("%s handshake: %v", c.version, err)
		}
		if f.svc.Hub().Online(er.EnvironmentID) {
			t.Errorf("%s: environment online", c.version)
		}
	}
}

// TestRescanRoundTrip (#3, #23): the manager's rescan frame carries a
// deadline and is answered like a request: the agent's RescanResult, or an
// error frame (agents without a watcher answer unsupported_request).
func TestRescanRoundTrip(t *testing.T) {
	f := newFixture(t)
	r, hello := f.enrolledRaw("ENG-A")
	s := f.raw(r.Credential)
	s.handshake(hello)
	f.waitOnline(r.EnvironmentID)
	type result struct {
		res protocol.RescanResult
		err error
	}
	done := make(chan result, 1)
	ask := protocol.RescanPayload{Scope: protocol.ScopeRef{Kind: "stack", ID: "shop"}, Path: ".", MaxEntries: 100, Reason: "sequence_gap"}
	go func() {
		res, err := f.svc.Hub().RescanEnvironment(f.ctx, r.EnvironmentID, ask, 0)
		done <- result{res, err}
	}()
	q, err := s.read()
	if err != nil || q.Type != protocol.TypeRescan || q.Deadline == nil {
		t.Fatalf("rescan frame %+v %v", q, err)
	}
	if p, _ := protocol.DecodePayload[protocol.RescanPayload](q); p != ask {
		t.Fatalf("rescan payload %+v", p)
	}
	s.send(protocol.TypeResponse, q.ID, protocol.ResponsePayload{Output: json.RawMessage(`{"scope":{"kind":"stack","id":"shop"},"path":".","entries":3,"truncated":false,"changed":["config"]}`)})
	if got := <-done; got.err != nil || got.res.Entries != 3 || len(got.res.Changed) != 1 || got.res.Changed[0] != "config" {
		t.Fatalf("rescan result %+v %v", got.res, got.err)
	}
	go func() {
		res, err := f.svc.Hub().RescanEnvironment(f.ctx, r.EnvironmentID, ask, 0)
		done <- result{res, err}
	}()
	q, _ = s.read()
	s.send(protocol.TypeError, q.ID, protocol.ErrorPayload{Code: protocol.CodeUnsupportedRequest, Message: "this agent watches no file scopes"})
	var re *RequestError
	if got := <-done; !errors.As(got.err, &re) || re.Code != protocol.CodeUnsupportedRequest {
		t.Fatalf("unsupported rescan %v", got.err)
	}
	if _, err := f.svc.Hub().RescanEnvironment(f.ctx, "0190a6e0-0000-7000-8000-000000000999", ask, 0); !errors.Is(err, jobs.ErrAgentOffline) {
		t.Fatalf("offline rescan %v", err)
	}
}
