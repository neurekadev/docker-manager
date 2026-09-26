package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentio "code.neureka.dev/docker-manager/docker-manager/internal/agent/containerio"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	agentjobs "code.neureka.dev/docker-manager/docker-manager/internal/agent/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/observe"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	agentprune "code.neureka.dev/docker-manager/docker-manager/internal/agent/prune"
	agentres "code.neureka.dev/docker-manager/docker-manager/internal/agent/resources"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/state"
	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// A real agent for the Docker resource tests (#6): the agent session client
// (internal/agent/session) and job runner with the agent's resource
// handlers and executors over an in-memory fake Engine, enrolled through
// /agent/v1/enroll and connected through /agent/v1/session of the test
// manager. Everything between the public API and the Engine adapter is the
// production code.

const stacksRoot = "/var/lib/docker/volumes/docker-manager_stacks/_data"

type testAgent struct {
	env    string
	engine *enginefake.Engine
	cancel context.CancelFunc
	done   chan error
	runner *agentjobs.Runner
	// relayDone is closed when the Docker event relay stopped (nil: none).
	relayDone chan struct{}
}

// connectAgent enrolls a new environment named name for fe and runs its
// agent until the test ends; it returns once the environment is online.
func (e *env) connectAgent(name string, fe *enginefake.Engine) *testAgent {
	e.t.Helper()
	return e.connectGuardedAgent(name, fe, nil)
}

// connectGuardedAgent is connectAgent with the agent's self-protection
// guard (#32; nil: identification by labels only).
func (e *env) connectGuardedAgent(name string, fe *enginefake.Engine, guard *protect.Guard) *testAgent {
	e.t.Helper()
	return e.connectAgentWith(name, fe, guard, false)
}

// connectObservedAgent is connectAgent with the agent's Docker event relay
// (#5, internal/agent/observe) on the session, as the runtime wires it: the
// fake Engine's events of every operation reach the manager's bus.
func (e *env) connectObservedAgent(name string, fe *enginefake.Engine) *testAgent {
	e.t.Helper()
	return e.connectAgentWith(name, fe, nil, true)
}

func (e *env) connectAgentWith(name string, fe *enginefake.Engine, guard *protect.Guard, relay bool) *testAgent {
	e.t.Helper()
	return e.connectAgentParts(name, fe, agentParts{guard: guard, relay: relay})
}

// agentParts extends a test agent: the self-protection guard, the Docker
// event relay and extra request handlers, stream handlers and executors
// (e.g. migrations, #35).
type agentParts struct {
	guard     *protect.Guard
	relay     bool
	requests  map[string]session.RequestHandler
	streams   map[string]session.StreamHandler
	executors []jobexec.Executor
}

// connectAgentParts is connectAgent with extra parts.
func (e *env) connectAgentParts(name string, fe *enginefake.Engine, parts agentParts) *testAgent {
	t := e.t
	t.Helper()
	guard, relay := parts.guard, parts.relay
	ctx := testutil.Context(t)
	sub := e.m.Events().Subscribe(256, func(ev events.Event) bool { return ev.Type == events.EnvironmentOnline })
	defer sub.Close()
	created, err := e.m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: name})
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	install, err := st.InstallID()
	if err != nil {
		t.Fatal(err)
	}
	id := fe.Identity()
	info := protocol.EngineInfo{ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion, MinAPIVersion: id.MinAPIVersion, OS: id.OS, Arch: id.Arch}
	body, _ := json.Marshal(protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: buildinfo.Get().Version, InstallID: install,
		Engine: info, Hostname: "host-" + name})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+protocol.EnrollPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var er protocol.EnrollResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(b, &er) != nil {
		t.Fatalf("enroll %s: %d %s", name, resp.StatusCode, b)
	}
	if err := st.SaveCredential(state.Credential{AgentID: er.AgentID, EnvironmentID: er.EnvironmentID, Credential: er.Credential,
		ManagerURL: e.srv.URL}); err != nil {
		t.Fatal(err)
	}

	res := agentres.New(agentres.Options{Engine: func() engine.Engine { return fe }, Logger: testutil.Logger(t), Guard: guard,
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	// Prune policies (#14), as the runtime wires them.
	pr := agentprune.New(agentprune.Options{Engine: func() engine.Engine { return fe }, Guard: guard, Clock: e.clk, Logger: testutil.Logger(t),
		ManagedStackDir: func(dir string) bool { return strings.HasPrefix(dir, stacksRoot+"/") }})
	execs := append(append(res.Executors(), pr.Executor()), parts.executors...)
	// Container logs and exec sessions (#8), as the runtime wires them.
	cio := agentio.New(agentio.Options{Engine: func() agentio.Engine { return fe }, Clock: e.clk, Logger: testutil.Logger(t)})
	requests := res.Requests()
	for k, v := range cio.Requests() {
		requests[k] = v
	}
	for k, v := range pr.Requests() {
		requests[k] = v
	}
	maps.Copy(requests, parts.requests)
	streams := cio.Streams()
	maps.Copy(streams, parts.streams)
	a := &testAgent{env: er.EnvironmentID, engine: fe, done: make(chan error, 1)}
	client := session.New(session.Options{
		State: st, Clock: e.clk, Logger: testutil.Logger(t), URL: "ws" + strings.TrimPrefix(e.srv.URL, "http") + protocol.SessionPath,
		DialOptions: func(h http.Header) *websocket.DialOptions {
			return &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{protocol.Version}}
		},
		AgentVersion: buildinfo.Get().Version, UserAgent: "docker-agent/test",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			cmds := make([]string, 0, len(execs))
			for _, x := range execs {
				cmds = append(cmds, string(x.Kind))
			}
			return protocol.CapabilitiesPayload{AgentVersion: buildinfo.Get().Version, Protocols: []string{protocol.Version}, OS: "linux",
				Arch: "amd64", Engine: info, Commands: cmds, Requests: []string{}, Streams: []string{},
				Transport: protocol.TransportInfo{ManagerURL: e.srv.URL, PlainHTTP: true}}, true
		},
		Requests: requests,
		Streams:  streams,
		Backoff:  session.Backoff{Min: time.Second, Max: time.Minute, ResetAfter: time.Minute, Rand: func() float64 { return 0 }},
	})
	runCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.runner, err = agentjobs.New(runCtx, agentjobs.Options{StateDir: st.Dir(), Clock: e.clk, Logger: testutil.Logger(t), Sender: client, Executors: execs})
	if err != nil {
		t.Fatal(err)
	}
	client.SetRunner(a.runner)
	go func() { a.done <- client.Run(runCtx) }()
	if relay {
		a.relayDone = make(chan struct{})
		r := observe.NewEventRelay(observe.EventOptions{Engine: func() observe.EngineAPI { return fe }, Publisher: client.Events(),
			Clock: e.clk, Logger: testutil.Logger(t)})
		go func() {
			defer close(a.relayDone)
			r.Run(runCtx)
		}()
	}
	t.Cleanup(a.stop)
	for {
		select {
		case ev := <-sub.C():
			if ev.ResourceID == er.EnvironmentID || ev.EnvironmentID == er.EnvironmentID {
				return a
			}
		case <-ctx.Done():
			t.Fatalf("environment %s did not come online", name)
		}
	}
}

// stop disconnects the agent (the environment goes offline).
func (a *testAgent) stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	<-a.done
	a.runner.Wait()
	if a.relayDone != nil {
		<-a.relayDone
	}
	a.cancel = nil
}

// runJob dispatches pending jobs and waits until job id is terminal.
func (e *env) runJob(id string) domain.Job {
	t := e.t
	t.Helper()
	ctx := testutil.Context(t)
	ch, cancel := e.m.Jobs().Subscribe(id)
	defer cancel()
	if err := e.m.Jobs().DispatchPending(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		j, err := e.m.Jobs().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State.Terminal() {
			return j
		}
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("job %s stuck in %s (blocked reason %q, by %q)", id, j.State, j.BlockedReason, j.BlockedBy)
		}
	}
}

// jobOf decodes the job of a 202 response.
func jobOf(t *testing.T, r response) string {
	t.Helper()
	var j struct {
		ID string `json:"id"`
	}
	r.json(t, &j)
	if j.ID == "" || r.header.Get("Location") != "/api/v1/jobs/"+j.ID {
		t.Fatalf("not a job response: %v %s", r.header, r.body)
	}
	return j.ID
}
