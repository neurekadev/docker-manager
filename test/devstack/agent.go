package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	agentio "github.com/neurekadev/dockyard/internal/agent/containerio"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	agentjobs "github.com/neurekadev/dockyard/internal/agent/jobs"
	"github.com/neurekadev/dockyard/internal/agent/observe"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	agentprune "github.com/neurekadev/dockyard/internal/agent/prune"
	agentres "github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/state"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/app"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// devAgent is a real agent (session client, job runner, the production
// resource, prune and container I/O handlers) over a fake Engine.
type devAgent struct {
	name   string
	env    string
	engine *devEngine
	cancel context.CancelFunc
	done   chan struct{}
}

// connectAgent enrolls a new environment through /agent/v1/enroll and
// runs its agent session until stop.
func connectAgent(ctx context.Context, m *app.Manager, base, stateDir string, h *homelabHost, log *slog.Logger) (*devAgent, error) {
	created, err := m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: h.name})
	if err != nil {
		return nil, err
	}
	st, err := state.Open(stateDir)
	if err != nil {
		return nil, err
	}
	install, err := st.InstallID()
	if err != nil {
		return nil, err
	}
	fe := h.engine
	id := fe.Identity()
	info := protocol.EngineInfo{ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion, MinAPIVersion: id.MinAPIVersion,
		OS: id.OS, Arch: id.Arch}
	body, _ := json.Marshal(protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: buildinfo.Get().Version, InstallID: install,
		Engine: info, Hostname: h.hostname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+protocol.EnrollPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var er protocol.EnrollResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(b, &er) != nil {
		return nil, fmt.Errorf("enroll %s: %d %s", h.name, resp.StatusCode, b)
	}
	if err := st.SaveCredential(state.Credential{AgentID: er.AgentID, EnvironmentID: er.EnvironmentID, Credential: er.Credential,
		ManagerURL: base}); err != nil {
		return nil, err
	}

	clk := clock.Real()
	alog := log.With("agent", h.name)
	managed := func(dir string) bool { return strings.HasPrefix(dir, h.stacksDir+"/") }
	// The guard knows the agent's own container and the stacks volume, as
	// the runtime's self-inspection would (#32).
	guard := protect.New(protect.Options{SelfContainerID: h.selfContainer, StacksVolume: h.stacksVolume, Logger: alog})
	res := agentres.New(agentres.Options{Engine: func() engine.Engine { return fe }, Logger: alog, ManagedStackDir: managed, Guard: guard})
	pr := agentprune.New(agentprune.Options{Engine: func() engine.Engine { return fe }, Clock: clk, Logger: alog, ManagedStackDir: managed,
		Guard: guard})
	cio := agentio.New(agentio.Options{Engine: func() agentio.Engine { return fe }, Clock: clk, Logger: alog})
	sim := newSimulation(h, clk)
	var sessionClient atomic.Pointer[session.Client]
	fs := newFileServing(h, clk, alog, &sessionClient)

	execs := append(res.Executors(), pr.Executor())
	execs = append(execs, fs.executors()...)
	requests := res.Requests()
	maps.Copy(requests, pr.Requests())
	maps.Copy(requests, cio.Requests())
	maps.Copy(requests, sim.requests())
	maps.Copy(requests, fs.requests())
	streams := cio.Streams()
	maps.Copy(streams, fs.streams())

	a := &devAgent{name: h.name, env: er.EnvironmentID, engine: fe, done: make(chan struct{})}
	client := session.New(session.Options{
		State: st, Clock: clk, Logger: alog, URL: "ws" + strings.TrimPrefix(base, "http") + protocol.SessionPath,
		DialOptions: func(hd http.Header) *websocket.DialOptions {
			return &websocket.DialOptions{HTTPHeader: hd, Subprotocols: []string{protocol.Version}}
		},
		AgentVersion: buildinfo.Get().Version, UserAgent: "dockyard-agent/devstack",
		Capabilities: func() (protocol.CapabilitiesPayload, bool) {
			cmds := make([]string, 0, len(execs))
			for _, x := range execs {
				cmds = append(cmds, string(x.Kind))
			}
			return protocol.CapabilitiesPayload{AgentVersion: buildinfo.Get().Version, Protocols: []string{protocol.Version}, OS: "linux",
				Arch: id.Arch, Engine: info, Commands: cmds, Requests: []string{}, Streams: []string{},
				Roots:     fs.roots,
				Transport: protocol.TransportInfo{ManagerURL: base, PlainHTTP: true}}, true
		},
		Requests: requests,
		Streams:  streams,
		Rescan:   fs.watcher.Rescan,
		Backoff:  session.Backoff{Min: time.Second, Max: 10 * time.Second, ResetAfter: time.Minute, Rand: func() float64 { return 0.5 }},
	})
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.cancel = cancel
	runner, err := agentjobs.New(runCtx, agentjobs.Options{StateDir: st.Dir(), Clock: clk, Logger: alog, Sender: client, Executors: execs})
	if err != nil {
		cancel()
		return nil, err
	}
	client.SetRunner(runner)
	sessionClient.Store(client)
	relay := observe.NewEventRelay(observe.EventOptions{Engine: func() observe.EngineAPI { return fe }, Publisher: client.Events(),
		Clock: clk, Logger: alog})
	go func() {
		defer close(a.done)
		go relay.Run(runCtx)
		go sim.run(runCtx)
		go fs.watcher.Run(runCtx)
		_ = client.Run(runCtx)
		runner.Wait()
		_ = fs.watcher.Close()
	}()
	return a, nil
}

func (a *devAgent) stop() {
	if a.cancel != nil {
		a.cancel()
		<-a.done
		a.cancel = nil
	}
}

// devEngine is the fake Engine plus simulated container logs (the fake
// itself does not produce any).
type devEngine struct {
	*enginefake.Engine
	logs func(container string) []string
}

// Logs emits the container's simulated lines (the tail), then, when
// following, one line every few seconds until ctx ends.
func (e *devEngine) Logs(ctx context.Context, id string, o engine.LogOptions, fn func(engine.LogEntry) error) error {
	c, ok := e.Container(id)
	if !ok {
		return enginefake.Err("container.logs", engine.CodeNotFound, "no such container: %s", id)
	}
	lines := e.logs(c.Details.Name)
	if len(lines) == 0 {
		lines = []string{"ready"}
	}
	now := time.Now().UTC()
	tail := lines
	if o.Tail > 0 && o.Tail < len(tail) {
		tail = tail[len(tail)-o.Tail:]
	}
	for i, l := range tail {
		at := now.Add(-time.Duration(len(tail)-i) * 7 * time.Second)
		if err := fn(engine.LogEntry{Stream: engine.Stdout, Time: at, Data: []byte(l + "\n")}); err != nil {
			return err
		}
	}
	if !o.Follow || !c.Details.State.Running {
		return nil
	}
	t := time.NewTicker(4 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case at := <-t.C:
			if err := fn(engine.LogEntry{Stream: engine.Stdout, Time: at.UTC(), Data: []byte(lines[i%len(lines)] + "\n")}); err != nil {
				return err
			}
		}
	}
}
