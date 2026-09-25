package protect

import (
	"context"
	"encoding/json"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protection"
)

// stackActions are the stack job kinds that must not touch DockYard's own
// Compose project (#32): deploy, stop, restart, down and remove. Start is
// harmless.
var stackActions = map[domain.JobKind]protection.Action{
	jobspec.StackDeploy: protection.Deploy, jobspec.StackUpdate: protection.Deploy, jobspec.StackStop: protection.Stop,
	jobspec.StackRestart: protection.Restart, jobspec.StackDown: protection.Down, jobspec.StackRemove: protection.Down,
}

// GuardStacks wraps every step of the stack executors (#7) so the agent
// refuses to deploy, stop, restart or take down DockYard's own Compose
// project, whatever the manager sent. eng returns the connected Engine.
func (g *Guard) GuardStacks(eng func() engine.Engine, execs []jobexec.Executor) []jobexec.Executor {
	out := make([]jobexec.Executor, 0, len(execs))
	for _, x := range execs {
		action, ok := stackActions[x.Kind]
		if !ok {
			out = append(out, x)
			continue
		}
		steps := make(map[string]jobexec.StepFunc, len(x.Steps))
		for name, fn := range x.Steps {
			steps[name] = g.stackStep(eng, action, fn)
		}
		x.Steps = steps
		out = append(out, x)
	}
	return out
}

func (g *Guard) stackStep(eng func() engine.Engine, action protection.Action, fn jobexec.StepFunc) jobexec.StepFunc {
	return func(ctx context.Context, sc *jobexec.StepContext) error {
		var in struct {
			Stack struct {
				ProjectName string `json:"projectName"`
			} `json:"stack"`
		}
		e := eng()
		if e == nil || json.Unmarshal(sc.Input, &in) != nil || in.Stack.ProjectName == "" {
			return fn(ctx, sc) // the step reports the missing Engine or bad input itself
		}
		if err := g.CheckProject(ctx, e, in.Stack.ProjectName, action); err != nil {
			return err
		}
		return fn(ctx, sc)
	}
}

// CheckProject refuses action on DockYard's own Compose project.
func (g *Guard) CheckProject(ctx context.Context, e engine.Engine, project string, action protection.Action) error {
	cs, err := e.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return err
	}
	return protection.Check(g.Identify(ctx, e, cs).Project(project), action, false)
}
