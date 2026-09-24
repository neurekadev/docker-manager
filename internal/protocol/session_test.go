package protocol

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
)

// TestProtocolDocListsCommands: the allowed-command table lists exactly the
// agent-executed job kinds of the #26 catalog with their capabilities.
func TestProtocolDocListsCommands(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol", "agent-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	agentKinds := map[string]bool{}
	for _, s := range jobspec.Catalog() {
		if s.Executor != domain.ExecutorAgent {
			continue
		}
		agentKinds[string(s.Kind)] = true
		row := "| `" + string(s.Kind) + "` | command | `" + s.Capability + "` |"
		if !strings.Contains(doc, row) {
			t.Errorf("docs/protocol/agent-v1.md lacks the row %q", row)
		}
	}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_.]+)` \\| command \\|").FindAllStringSubmatch(doc, -1) {
		if !agentKinds[m[1]] {
			t.Errorf("doc lists %q as an agent command, but it is not an agent-executed job kind", m[1])
		}
	}
}

func frameWith(t *testing.T, typ Type, payload any) *Frame {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	f := &Frame{Type: typ, ID: "f1", Payload: b}
	if needsCorrelation[typ] {
		f.CorrelationID = "c1"
	}
	switch typ {
	case TypeRequest:
		f.Deadline = &deadline
	case TypeCommand:
		f.JobID, f.Attempt, f.FencingToken, f.Deadline = "j1", 1, 1, &deadline
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("envelope %s: %v", typ, err)
	}
	return f
}

func validHello() HelloPayload {
	return HelloPayload{Protocol: Version, AgentID: "a1", AgentVersion: "1.4.0", InstallID: "i1", EngineID: "ABCD:EFGH"}
}

func validWelcome() WelcomePayload {
	return WelcomePayload{SessionID: "s1", ManagerVersion: "1.4.0", EnvironmentID: "e1", AgentStatus: VersionCurrent,
		HeartbeatIntervalMs: HeartbeatInterval.Milliseconds(), HeartbeatTimeoutMs: HeartbeatTimeout.Milliseconds(), Limits: DefaultLimits()}
}

func validCaps() CapabilitiesPayload {
	return CapabilitiesPayload{AgentVersion: "1.4.0", Protocols: []string{Version}, OS: "linux", Arch: "amd64",
		Engine:   EngineInfo{ID: "ABCD", Version: "28.5.2", APIVersion: "1.51", OS: "linux", Arch: "amd64"},
		Commands: []string{"stack.deploy"}, Requests: []string{ReqEngineInfo}, Streams: []string{StreamContainerLogs},
		Roots: []Root{{Kind: "stacks", Path: "/var/lib/docker/volumes/dockyard_stacks/_data", Watch: "inotify"}}}
}

func TestValidPayloads(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	exit := 0
	ok := []struct {
		typ Type
		p   any
	}{
		{TypeHello, validHello()},
		{TypeWelcome, validWelcome()},
		{TypeCapabilities, validCaps()},
		{TypeHeartbeat, HeartbeatPayload{Seq: 1, SentAt: now}},
		{TypeEvent, EventPayload{Source: "engine", Type: "container", Action: "die", ResourceID: "abc", At: now, Seq: 9}},
		{TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s1"}, Paths: []string{"compose.yaml", "config/app.env"}, At: now, Seq: 2}},
		{TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "volume", ID: "data"}, Overflow: true, At: now, Seq: 3}},
		{TypeRescan, RescanPayload{Scope: ScopeRef{Kind: "volume", ID: "data"}, Path: ".", MaxEntries: 10000}},
		{TypeRequest, RequestPayload{Name: ReqFilesList, Input: json.RawMessage(`{"path":"a"}`)}},
		{TypeResponse, ResponsePayload{Output: json.RawMessage(`{"entries":[]}`)}},
		{TypeStreamOpen, StreamOpenPayload{Kind: StreamMigrationSend, Direction: DirAgentToManager, JobID: "j1", MaxBytes: 1 << 30}},
		{TypeStreamOpen, StreamOpenPayload{Kind: StreamContainerExec, Direction: DirBoth}},
		{TypeStreamData, StreamDataPayload{Seq: 1, Data: []byte("hello"), Channel: "stdout"}},
		{TypeStreamClose, StreamClosePayload{Reason: CloseReasonEOF, Bytes: 5, ExitCode: &exit}},
		{TypeStreamClose, StreamClosePayload{Reason: CloseReasonError, Code: CodeForbiddenPath}},
		{TypeStreamCredit, StreamCreditPayload{Bytes: StreamWindow}},
		{TypeCancel, CancelPayload{Reason: "user"}},
		{TypeError, ErrorPayload{Code: CodeUnsupportedRequest, Message: "no"}},
		{TypeCommand, CommandPayload{Kind: "stack.deploy"}},
		{TypeAck, AckPayload{Accepted: true}},
		{TypeProgress, ProgressPayload{Percent: -1}},
		{TypeResult, ResultPayload{Outcome: "partial"}},
		{TypeJobReport, JobReportPayload{Jobs: []JobReportEntry{{JobID: "j", Status: ReportRunning}, {JobID: "k", Status: ReportFinished, Result: &ResultPayload{Outcome: "failed"}}}}},
	}
	for _, c := range ok {
		f := frameWith(t, c.typ, c.p)
		if err := ValidatePayload(f); err != nil {
			t.Errorf("%s %+v: %v", c.typ, c.p, err)
		}
	}
	// Optional payloads may be absent.
	for _, typ := range []Type{TypeHeartbeat, TypeCancel, TypeResponse} {
		f := &Frame{Type: typ, ID: "x", CorrelationID: "c"}
		if err := ValidatePayload(f); err != nil {
			t.Errorf("%s without payload: %v", typ, err)
		}
	}
}

func TestInvalidPayloads(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	mut := func(f func(*HelloPayload)) HelloPayload { h := validHello(); f(&h); return h }
	wmut := func(f func(*WelcomePayload)) WelcomePayload { w := validWelcome(); f(&w); return w }
	cmut := func(f func(*CapabilitiesPayload)) CapabilitiesPayload { c := validCaps(); f(&c); return c }
	bad := map[string]struct {
		typ Type
		p   any
	}{
		"hello wrong protocol":    {TypeHello, mut(func(h *HelloPayload) { h.Protocol = "dockyard.agent/v2" })},
		"hello bad agent id":      {TypeHello, mut(func(h *HelloPayload) { h.AgentID = "a b" })},
		"hello empty version":     {TypeHello, mut(func(h *HelloPayload) { h.AgentVersion = "" })},
		"welcome bad status":      {TypeWelcome, wmut(func(w *WelcomePayload) { w.AgentStatus = "ancient" })},
		"welcome heartbeat order": {TypeWelcome, wmut(func(w *WelcomePayload) { w.HeartbeatTimeoutMs = w.HeartbeatIntervalMs })},
		"welcome frame too big":   {TypeWelcome, wmut(func(w *WelcomePayload) { w.Limits.MaxFrameBytes = MaxFrameSize + 1 })},
		"caps without protocol":   {TypeCapabilities, cmut(func(c *CapabilitiesPayload) { c.Protocols = []string{"v0"} })},
		"caps unknown request":    {TypeCapabilities, cmut(func(c *CapabilitiesPayload) { c.Requests = []string{"host.shell"} })},
		"caps unknown stream":     {TypeCapabilities, cmut(func(c *CapabilitiesPayload) { c.Streams = []string{"host.pty"} })},
		"caps relative root":      {TypeCapabilities, cmut(func(c *CapabilitiesPayload) { c.Roots[0].Path = "stacks" })},
		"caps no engine":          {TypeCapabilities, cmut(func(c *CapabilitiesPayload) { c.Engine = EngineInfo{} })},
		"event unknown source":    {TypeEvent, EventPayload{Source: "docker", Type: "container", Action: "die", At: now}},
		"event no time":           {TypeEvent, EventPayload{Source: "engine", Type: "container", Action: "die"}},
		"fs escape":               {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, Paths: []string{"../etc/passwd"}, At: now}},
		"fs absolute":             {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, Paths: []string{"/etc/passwd"}, At: now}},
		"fs unclean":              {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, Paths: []string{"a/./b"}, At: now}},
		"fs backslash":            {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, Paths: []string{`a\..\..\b`}, At: now}},
		"fs empty":                {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, At: now}},
		"fs too many":             {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "stack", ID: "s"}, Paths: slices.Repeat([]string{"a"}, MaxPaths+1), At: now}},
		"fs bad scope":            {TypeFSInvalidation, FSInvalidationPayload{Scope: ScopeRef{Kind: "host", ID: "/"}, Paths: []string{"a"}, At: now}},
		"rescan escape":           {TypeRescan, RescanPayload{Scope: ScopeRef{Kind: "volume", ID: "v"}, Path: "..", MaxEntries: 1}},
		"rescan unbounded":        {TypeRescan, RescanPayload{Scope: ScopeRef{Kind: "volume", ID: "v"}, Path: "."}},
		"request shell":           {TypeRequest, RequestPayload{Name: "host.shell"}},
		"request docker proxy":    {TypeRequest, RequestPayload{Name: "docker.api"}},
		"stream unknown":          {TypeStreamOpen, StreamOpenPayload{Kind: "host.pty", Direction: DirBoth}},
		"stream wrong direction":  {TypeStreamOpen, StreamOpenPayload{Kind: StreamFilesUpload, Direction: DirAgentToManager}},
		"stream negative window":  {TypeStreamOpen, StreamOpenPayload{Kind: StreamFilesUpload, Direction: DirManagerToAgent, WindowBytes: -1}},
		"chunk too big":           {TypeStreamData, StreamDataPayload{Seq: 1, Data: make([]byte, MaxChunk+1)}},
		"chunk bad channel":       {TypeStreamData, StreamDataPayload{Seq: 1, Channel: "tty0"}},
		"close unknown reason":    {TypeStreamClose, StreamClosePayload{Reason: "bored"}},
		"close error no code":     {TypeStreamClose, StreamClosePayload{Reason: CloseReasonError}},
		"credit zero":             {TypeStreamCredit, StreamCreditPayload{}},
		"credit huge":             {TypeStreamCredit, StreamCreditPayload{Bytes: 1 << 40}},
		"error unknown code":      {TypeError, ErrorPayload{Code: "oops"}},
		"command bad kind":        {TypeCommand, CommandPayload{Kind: "rm -rf"}},
		"progress over 100":       {TypeProgress, ProgressPayload{Percent: 101}},
		"result unknown outcome":  {TypeResult, ResultPayload{Outcome: "meh"}},
		"report bad status":       {TypeJobReport, JobReportPayload{Jobs: []JobReportEntry{{JobID: "j", Status: "lost"}}}},
		"report finished no res":  {TypeJobReport, JobReportPayload{Jobs: []JobReportEntry{{JobID: "j", Status: ReportFinished}}}},
	}
	for name, c := range bad {
		f := frameWith(t, c.typ, c.p)
		if err := ValidatePayload(f); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Unknown payload fields are rejected; required job payloads must exist.
	f := &Frame{Type: TypeHello, ID: "h", Payload: json.RawMessage(`{"protocol":"dockyard.agent/v1","shell":"sh"}`)}
	if err := ValidatePayload(f); !errors.Is(err, ErrInvalidFrame) {
		t.Errorf("unknown hello field: %v", err)
	}
	if err := ValidatePayload(&Frame{Type: TypeResult, ID: "r", CorrelationID: "c"}); err == nil {
		t.Error("result without payload accepted")
	}
	if !errors.Is(RequestPayload{Name: "x.y"}.Validate(), ErrUnsupportedRequest) || !errors.Is(StreamOpenPayload{Kind: "x"}.Validate(), ErrUnsupportedStream) {
		t.Error("sentinel errors")
	}
}

func TestRelativePaths(t *testing.T) {
	for _, p := range []string{".", "a", "a/b", "a/.hidden", "..a", "a/..b"} {
		if !ValidRelativePath(p) {
			t.Errorf("%q rejected", p)
		}
	}
	for _, p := range []string{"", "/", "/a", "..", "../a", "a/../..", "a/", "a//b", "./a", "a\\b", "a\x00", strings.Repeat("a", 4097)} {
		if ValidRelativePath(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestNameCatalogs(t *testing.T) {
	names := RequestNames()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] || !nameRE.MatchString(n) {
			t.Errorf("request name %q duplicated or malformed", n)
		}
		seen[n] = true
	}
	for _, m := range mutatingRequests {
		if !seen[m] || !IsMutatingRequest(m) {
			t.Errorf("mutating request %q not in the catalog", m)
		}
	}
	if IsMutatingRequest(ReqEngineInfo) {
		t.Error("engine.info is not mutating")
	}
	for _, k := range StreamKinds() {
		if d, ok := StreamDirection(k); !ok || (d != DirAgentToManager && d != DirManagerToAgent && d != DirBoth) {
			t.Errorf("stream %q direction %q", k, d)
		}
	}
	if _, ok := StreamDirection("nope"); ok {
		t.Error("unknown stream has a direction")
	}
	if len(ErrorCodes()) != len(errorCodes) {
		t.Error("ErrorCodes")
	}
	// Nothing in the catalogs is a generic passthrough.
	for _, n := range append(names, StreamKinds()...) {
		for _, bad := range []string{"shell", "exec_host", "docker.api", "proxy", "raw"} {
			if strings.Contains(n, bad) {
				t.Errorf("%q looks like a passthrough", n)
			}
		}
	}
}

func TestCheckAgentVersion(t *testing.T) {
	cases := []struct {
		manager, agent, want string
	}{
		{"1.4.0", "1.4.0", VersionCurrent},
		{"1.4.2", "1.4.0", VersionCurrent},
		{"1.4.0", "1.4.3", VersionCurrent},
		{"1.4.0", "1.3.9", VersionOutdated},
		{"1.4.0-rc.1", "1.3.0+abc", VersionOutdated},
		{"0.0.0-edge", "0.0.0-edge", VersionCurrent},
		{"1.4.0", "1.2.9", ""},
		{"1.4.0", "1.5.0", ""},
		{"2.0.0", "1.9.0", ""},
		{"1.4.0", "dev", ""},
		{"1.4.0", "1.04.0", ""},
		{"1.0.0", "0.9.0", ""},
	}
	for _, c := range cases {
		got, err := CheckAgentVersion(c.manager, c.agent)
		if c.want == "" {
			if !errors.Is(err, ErrVersionUnsupported) || got != "" {
				t.Errorf("%s vs %s: %q %v, want unsupported", c.manager, c.agent, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s vs %s: %q %v, want %s", c.manager, c.agent, got, err, c.want)
		}
	}
	_, err := CheckAgentVersion("1.4.0", "1.5.0")
	if err == nil || !strings.Contains(err.Error(), "upgrade the manager first") {
		t.Errorf("message %v", err)
	}
}

func TestCloseCodes(t *testing.T) {
	for code, want := range map[websocket.StatusCode]bool{
		CloseNormal: true, CloseGoingAway: true, CloseInternal: true, CloseTooLarge: true, CloseProtocolError: true,
		CloseHeartbeatTimeout: true, CloseUnauthorized: false, CloseRevoked: false, CloseReplaced: false, CloseVersionUnsupported: false,
	} {
		if ReconnectAllowed(code) != want {
			t.Errorf("ReconnectAllowed(%d) != %v", code, want)
		}
	}
}

// TestProtocolDocListsNames keeps docs/protocol/agent-v1.md in sync with
// the code: every frame type, request name, stream kind, error code and
// close code is documented, and every documented request or stream exists.
func TestProtocolDocListsNames(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol", "agent-v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	var want []string
	for _, typ := range Types() {
		want = append(want, "`"+string(typ)+"`")
	}
	for _, n := range append(append(RequestNames(), StreamKinds()...), ErrorCodes()...) {
		want = append(want, "`"+n+"`")
	}
	for _, c := range []websocket.StatusCode{CloseNormal, CloseGoingAway, CloseTooLarge, CloseInternal, CloseProtocolError,
		CloseUnauthorized, CloseRevoked, CloseHeartbeatTimeout, CloseReplaced, CloseVersionUnsupported} {
		want = append(want, "| "+strconv.Itoa(int(c))+" |")
	}
	for _, w := range want {
		if !strings.Contains(doc, w) {
			t.Errorf("docs/protocol/agent-v1.md does not mention %s", w)
		}
	}
	// Rows of the request and stream tables must name real entries.
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_.]+)` \\| (request|stream) \\|").FindAllStringSubmatch(doc, -1) {
		switch m[2] {
		case "request":
			if !slices.Contains(RequestNames(), m[1]) {
				t.Errorf("doc lists unknown request %q", m[1])
			}
		case "stream":
			if _, ok := StreamDirection(m[1]); !ok {
				t.Errorf("doc lists unknown stream %q", m[1])
			}
		}
	}
}
