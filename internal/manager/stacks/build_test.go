package stacks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/buildrun"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// Stack builds (#33) through the manager: POST /stacks/{id}/builds'
// service call enqueues stack.build with credential IDs only, the job
// engine resolves them into the command, the agent's executor builds
// (scripted Compose adapter) with streamed progress reported back as job
// events, and cancellation reaches the running build.

const buildYAML = `services:
  web:
    build:
      context: ./web
      args:
        API_KEY: ${API_KEY}
    depends_on: [db]
  db:
    image: registry.example:5000/db:16
`

// buildArg is the value of a build argument (a secret the user chose to
// pass as one): it may reach BuildKit, never the job input or events.
const buildArg = "Canary-build-arg-value-7f3a"

// commands dispatches pending jobs and acknowledges their commands.
func (h *harness) commands() []*protocol.Frame {
	h.t.Helper()
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	var out []*protocol.Frame
	for _, f := range h.disp.Drain(env) {
		if f.Type == protocol.TypeCommand {
			h.frame(protocol.TypeAck, f, protocol.AckPayload{Accepted: true})
			out = append(out, f)
		}
	}
	return out
}

// frameReporter sends an attempt's progress to the job engine like the
// agent's session does.
type frameReporter struct {
	h   *harness
	cmd *protocol.Frame
	mu  sync.Mutex
	n   int
}

func (r *frameReporter) Progress(ctx context.Context, _ *jobexec.State, p protocol.ProgressPayload) {
	r.mu.Lock()
	r.n++
	id := fmt.Sprintf("progress-%s-%d", r.cmd.ID, r.n)
	r.mu.Unlock()
	f, err := protocol.NewFrame(protocol.TypeProgress, id, r.cmd.ID, r.cmd.Ref(), p)
	if err == nil {
		_, err = r.h.eng.HandleAgentFrame(ctx, env, f)
	}
	if err != nil {
		r.h.t.Errorf("progress frame: %v", err)
	}
}

// execute runs one command with the agent's executors (progress reported,
// cancellation read from the job) and returns its result; the caller
// sends it with h.frame (not from another goroutine).
func (h *harness) execute(f *protocol.Frame) protocol.ResultPayload {
	cmd, err := protocol.DecodePayload[protocol.CommandPayload](f)
	if err != nil {
		h.t.Error(err)
		return protocol.ResultPayload{}
	}
	var exec jobexec.Executor
	for _, x := range h.agent.Executors() {
		if string(x.Kind) == cmd.Kind {
			exec = x
		}
	}
	st := &jobexec.State{JobID: f.JobID, Attempt: f.Attempt, FencingToken: f.FencingToken, Kind: exec.Kind, Input: cmd.Input, Secrets: cmd.Secrets}
	res, err := jobexec.Run(h.ctx, exec, st, jobexec.Options{Journal: nopJournal{}, Reporter: &frameReporter{h: h, cmd: f},
		CancelRequested: func() bool {
			j, err := h.eng.Get(context.Background(), f.JobID)
			return err == nil && j.CancelRequested
		}})
	if err != nil {
		h.t.Error(err)
	}
	return res
}

func (h *harness) events(jobID string) string {
	h.t.Helper()
	evs, err := h.eng.Events(h.ctx, jobID, 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := json.Marshal(evs)
	return string(b)
}

func stackInput(t *testing.T, j domain.Job) protocol.StackJobInput {
	t.Helper()
	var in protocol.StackJobInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestStackBuildJobOnTheManager(t *testing.T) {
	h := newHarness(t)
	set := canary.New()
	regID := "reg-build-canary-0001"
	set.Register(canary.RegistryCredential, "base-image registry password", "pw-"+regID) // what the harness resolves regID to
	set.Register(canary.EnvValue, "build argument", buildArg)
	h.regs.build = []string{regID}
	st := h.create("shop", buildYAML, "API_KEY="+buildArg+"\n")
	h.comp.buildLog = []string{"logging in with pw-" + regID}

	j, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{Services: []string{"web"}, IdempotencyKey: "b-1"},
		domain.StackBuildOptions{NoCache: true, Pull: true, TimeoutSeconds: 7200})
	if err != nil {
		t.Fatal(err)
	}
	in := stackInput(t, j)
	if j.Kind != jobspec.StackBuild || len(j.Targets) != 1 || j.Targets[0] != (domain.JobTarget{Type: domain.TargetStack, ID: st.ID}) ||
		!in.NoCache || !in.PullBase || in.BuildTimeoutSeconds != 7200 || !slices.Equal(in.Services, []string{"web"}) ||
		!slices.Equal(in.RegistryConnections, []string{regID}) || in.Stack.ProjectName != "shop" {
		t.Fatalf("job %+v input %+v", j, in)
	}
	set.AssertClean(t, "job input", j.Input)
	// Idempotent replay.
	if again, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{Services: []string{"web"}, IdempotencyKey: "b-1"},
		domain.StackBuildOptions{NoCache: true, Pull: true, TimeoutSeconds: 7200}); err != nil || again.ID != j.ID {
		t.Fatalf("replay %+v %v", again, err)
	}

	cmds := h.commands()
	if len(cmds) != 1 {
		t.Fatalf("commands %d", len(cmds))
	}
	res := h.execute(cmds[0])
	h.frame(protocol.TypeResult, cmds[0], res)
	done := h.job(j.ID)
	if done.State != domain.JobSucceeded {
		t.Fatalf("job %+v result %+v", done, res)
	}
	// The build got the resolved credential and the request's options.
	b := h.comp.Builds()
	if len(b) != 1 || !b[0].NoCache || !b[0].PullBase || b[0].MissingOnly || !slices.Equal(b[0].Services, []string{"web"}) ||
		len(b[0].Auth) != 1 || string(b[0].Auth[0].Password) != "pw-"+regID {
		t.Fatalf("builds %+v", b)
	}
	// Progress and output reached the job's events, scrubbed.
	evs := h.events(j.ID)
	if !strings.Contains(evs, "shop-web: [2/2] RUN make") || !strings.Contains(evs, "logging in with [redacted]") {
		t.Errorf("events %s", evs)
	}
	// The built images are the job's items (image reference -> image ID).
	if len(done.Items) != 1 || done.Items[0].Name != "shop-web" || done.Items[0].Message != "sha256:built-web" {
		t.Errorf("items %+v", done.Items)
	}
	var out protocol.StackJobOutput
	if err := json.Unmarshal(res.Output, &out); err != nil || len(out.Built) != 1 || out.Built[0].Image != "shop-web" {
		t.Errorf("output %s %v", res.Output, err)
	}
	set.AssertClean(t, "job events", evs)
	set.AssertClean(t, "job", done)

	// A build changes no deployment state.
	got := h.get(st.ID)
	if got.Status != domain.StackUndeployed || got.Applied != nil || got.LastJobKind != jobspec.StackBuild {
		t.Errorf("stack after build %+v", got)
	}
}

func TestStackBuildRegistryConnections(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", buildYAML, "API_KEY=x\n")
	h.regs.build = []string{"reg-hub"}
	h.regs.usable = map[string]bool{"reg-named": true}
	// Named connections replace the host-wide defaults and must be usable.
	j, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackBuildOptions{RegistryIDs: []string{"reg-named", "reg-named"}})
	if err != nil || !slices.Equal(stackInput(t, j).RegistryConnections, []string{"reg-named"}) {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackBuildOptions{RegistryIDs: []string{"reg-gone"}}); !errors.Is(err, domain.ErrRegistryConnectionNotFound) {
		t.Errorf("unknown connection: %v", err)
	}
	var ie *domain.InputError
	if _, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackBuildOptions{TimeoutSeconds: 6*3600 + 1}); !errors.As(err, &ie) {
		t.Errorf("timeout out of range: %v", err)
	}
	// A deploy of a stack with build sections offers the build connections
	// to base images next to the image selections.
	h.regs.byHost["registry.example:5000/db"] = "reg-db"
	j, err = h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{BuildTimeoutSeconds: 900})
	if in := stackInput(t, j); err != nil || !slices.Equal(in.RegistryConnections, []string{"reg-db", "reg-hub"}) || in.BuildTimeoutSeconds != 900 {
		t.Errorf("deploy %+v %v", stackInput(t, j), err)
	}
	// Without build sections nothing is added.
	plain := h.create("plain", "services:\n  db:\n    image: registry.example:5000/db:16\n", "")
	j, err = h.svc.Deploy(h.ctx, alice, plain, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil || !slices.Equal(stackInput(t, j).RegistryConnections, []string{"reg-db"}) {
		t.Errorf("plain deploy %+v %v", stackInput(t, j), err)
	}
}

func TestDeployPullsBaseImagesOnlyWithPullAndBuild(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", buildYAML, "API_KEY=x\n")
	for _, c := range []struct {
		o    domain.StackDeployOptions
		want bool
	}{
		{domain.StackDeployOptions{}, false},
		{domain.StackDeployOptions{Build: true}, false},
		{domain.StackDeployOptions{Pull: "always"}, false},
		{domain.StackDeployOptions{Pull: "missing", Build: true}, false},
		{domain.StackDeployOptions{Pull: "always", Build: true}, true},
	} {
		j, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, c.o)
		if err != nil {
			t.Fatalf("%+v: %v", c.o, err)
		}
		if in := stackInput(t, j); in.PullBase != c.want || in.Pull != c.o.Pull || in.Build != c.o.Build {
			t.Errorf("%+v: input %+v, want pullBase %v", c.o, in, c.want)
		}
	}
}

func TestStackBuildCancelsMidBuildThroughTheJobEngine(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", buildYAML, "API_KEY=x\n")
	j := h.deploy(st)
	h.run()
	if h.job(j.ID).State != domain.JobSucceeded {
		t.Fatalf("deploy %+v", h.job(j.ID))
	}
	before := h.get(st.ID)

	h.comp.blockBuild = true
	h.comp.started = make(chan struct{}, 1)
	b, err := h.svc.Build(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cmds := h.commands()
	results := make(chan protocol.ResultPayload, 1)
	go func() { results <- h.execute(cmds[0]) }()
	<-h.comp.started
	if _, err := h.eng.Cancel(h.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.clk.BlockUntilWaiters(h.ctx, 2); err != nil { // the build's cancellation poll and timeout
		t.Fatal(err)
	}
	h.clk.Advance(buildrun.DefaultCancelPoll)
	res := <-results
	h.frame(protocol.TypeResult, cmds[0], res)
	got := h.job(b.ID)
	if got.State != domain.JobCancelled || res.Outcome != jobexec.OutcomeCancelled {
		t.Fatalf("job %+v result %+v", got, res)
	}
	// The deployed stack is untouched.
	after := h.get(st.ID)
	if after.Status != before.Status || after.Applied.ID != before.Applied.ID || !slices.Equal(h.comp.Calls(), []string{"build:shop", "up:shop", "build:shop"}) {
		t.Errorf("stack %+v calls %v", after, h.comp.Calls())
	}
}
