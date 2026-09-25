package protect

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protection"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type journal struct{}

func (journal) Save(context.Context, *jobexec.State) error { return nil }

// TestGuardStacks (#32): the agent refuses stack jobs that would deploy,
// stop, restart or take down DockYard's own Compose project, whatever the
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
		if res := run(kind, "dockyard"); res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protection.CodeProtected {
			t.Errorf("%s of DockYard's project: %+v", kind, res)
		}
	}
	if res := run(jobspec.StackStart, "dockyard"); res.Outcome != jobexec.OutcomeSucceeded {
		t.Errorf("start: %+v", res)
	}
	if res := run(jobspec.StackStop, "shop"); res.Outcome != jobexec.OutcomeSucceeded {
		t.Errorf("stop another project: %+v", res)
	}
	if len(ran) != 2 || ran[0] != "start" || ran[1] != "stop" {
		t.Fatalf("steps that ran: %v", ran)
	}
}
