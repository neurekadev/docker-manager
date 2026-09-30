package protect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type journal struct{}

func (journal) Save(context.Context, *jobexec.State) error { return nil }

// TestGuardStacks (#32): the agent refuses stack jobs that would stop,
// restart or take down Docker Manager's own Compose project, whatever the
// manager sent; start and other projects run.
func TestGuardStacks(t *testing.T) {
	fe := enginefake.New("ENG")
	d := fe.Deploy(true)
	g := New(Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks})
	var ran []string
	step := func(name string) jobexec.StepFunc {
		return func(context.Context, *jobexec.StepContext) error { ran = append(ran, name); return nil }
	}
	execs := g.GuardStacks(func() engine.Engine { return fe }, []jobexec.Executor{
		{Kind: jobspec.StackStop, Steps: map[string]jobexec.StepFunc{"stop": step("stop")}},
		{Kind: jobspec.StackStart, Steps: map[string]jobexec.StepFunc{"start": step("start")}},
		{Kind: jobspec.StackDown, Steps: map[string]jobexec.StepFunc{"down": step("down")}},
	})
	run := func(kind domain.JobKind, project string) protocol.ResultPayload {
		t.Helper()
		var x jobexec.Executor
		for _, e := range execs {
			if e.Kind == kind {
				x = e
			}
		}
		in, _ := json.Marshal(protocol.StackJobInput{StackID: "s", Stack: protocol.ProjectRef{Root: "stacks", Dir: project, ProjectName: project}})
		res, err := jobexec.Run(testutil.Context(t), x, &jobexec.State{JobID: "j", Attempt: 1, FencingToken: 1, Kind: kind, Input: in},
			jobexec.Options{Journal: journal{}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, kind := range []domain.JobKind{jobspec.StackStop, jobspec.StackDown} {
		if res := run(kind, "docker-manager"); res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protection.CodeProtected {
			t.Errorf("%s of Docker Manager's project: %+v", kind, res)
		}
	}
	if res := run(jobspec.StackStart, "docker-manager"); res.Outcome != jobexec.OutcomeSucceeded {
		t.Errorf("start: %+v", res)
	}
	if res := run(jobspec.StackStop, "shop"); res.Outcome != jobexec.OutcomeSucceeded {
		t.Errorf("stop another project: %+v", res)
	}
	if len(ran) != 2 || ran[0] != "start" || ran[1] != "stop" {
		t.Fatalf("steps that ran: %v", ran)
	}
}

// TestGuardStacksLetsDockerManagerUpdateItself (#32 × #20): update.run and
// stack.deploy of Docker Manager's own Compose project are not wrapped: the
// stack executors hand the agent's own container to a helper container.
func TestGuardStacksLetsDockerManagerUpdateItself(t *testing.T) {
	fe := enginefake.New("ENG")
	d := fe.Deploy(true)
	g := New(Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks})
	var ran []string
	spec, _ := jobspec.Lookup(jobspec.UpdateRun)
	steps := map[string]jobexec.StepFunc{}
	for _, st := range spec.Steps {
		steps[st.Name] = func(context.Context, *jobexec.StepContext) error { ran = append(ran, st.Name); return nil }
	}
	execs := g.GuardStacks(func() engine.Engine { return fe }, []jobexec.Executor{{Kind: jobspec.UpdateRun, Steps: steps}})
	run := func(project string) protocol.ResultPayload {
		t.Helper()
		in, _ := json.Marshal(protocol.UpdateRunInput{PolicyID: "p", StackID: "s",
			Stack:    &protocol.ProjectRef{Root: "stacks", Dir: project, ProjectName: project},
			Services: []protocol.UpdateService{{Service: "web", Reference: "nginx:1.27", Digest: "sha256:" + strings.Repeat("a", 64)}}})
		res, err := jobexec.Run(testutil.Context(t), execs[0], &jobexec.State{JobID: "j-" + project, Attempt: 1, FencingToken: 1,
			Kind: jobspec.UpdateRun, Input: in}, jobexec.Options{Journal: journal{}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, project := range []string{"docker-manager", "shop"} {
		ran = nil
		if res := run(project); res.Outcome != jobexec.OutcomeSucceeded || len(ran) != len(spec.Steps) {
			t.Fatalf("update of %s: %+v (steps %v)", project, res, ran)
		}
	}
}
