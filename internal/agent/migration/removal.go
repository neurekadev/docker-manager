package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Executors returns the stack.remove_source executor: after the user
// confirmed a completed migration, the source project's containers and
// networks are removed (stopped in reverse dependency order first), then
// the migrated volumes and finally the project directory. Docker Manager's own
// project and volumes are refused (#32). Each step is idempotent: what is
// already gone is skipped.
func (s *Service) Executors() []jobexec.Executor {
	return []jobexec.Executor{{Kind: jobspec.StackRemoveSource, Steps: map[string]jobexec.StepFunc{
		"down":           s.removeDown,
		"remove_volumes": s.removeVolumes,
		"remove_files":   s.removeFiles,
	}}}
}

// classed is a step failure with its own job error class.
type classed struct {
	class, recovery string
	err             error
}

func (c *classed) Error() string      { return c.err.Error() }
func (c *classed) Unwrap() error      { return c.err }
func (c *classed) ErrorClass() string { return c.class }
func (c *classed) Recovery() string   { return c.recovery }

func removalInput(sc *jobexec.StepContext) (protocol.SourceRemovalInput, error) {
	var in protocol.SourceRemovalInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, fmt.Errorf("malformed stack.remove_source input: %w", err)
	}
	if err := in.Stack.Validate(); err != nil {
		return in, err
	}
	if err := protocol.ValidateJobLinked(in.MigrationID); err != nil {
		return in, err
	}
	for _, v := range in.Volumes {
		if !protocol.ValidDockerName(v) {
			return in, fmt.Errorf("invalid volume name %q", v)
		}
	}
	return in, nil
}

func (s *Service) updateRemoval(ctx context.Context, sc *jobexec.StepContext, fn func(o *protocol.SourceRemovalOutput)) error {
	var o protocol.SourceRemovalOutput
	if b := sc.Output(); len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	fn(&o)
	return sc.SetOutput(ctx, o)
}

func (s *Service) removeDown(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := removalInput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	if err := s.opts.Guard.CheckProject(ctx, eng, in.Stack.ProjectName, protection.Down); err != nil {
		return err
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	if len(list) > 0 {
		if g, err := lifecycle.GraphFromContainers(list); err == nil {
			rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
			if _, err := lifecycle.Stop(ctx, g, rt, nil, lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout}); err != nil {
				return err
			}
		}
	}
	// Every container of the project (one-off ones too).
	all, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{protocol.ComposeProjectLabel + "=" + in.Stack.ProjectName}})
	if err != nil {
		return err
	}
	var removed []string
	for _, c := range all {
		sc.Progress(ctx, -1, "removing container "+strings.TrimPrefix(firstName(c), "/"))
		if err := eng.RemoveContainer(ctx, c.ID, engine.RemoveOptions{Force: true}); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			return err
		}
		removed = append(removed, strings.TrimPrefix(firstName(c), "/"))
	}
	nets, err := eng.ListNetworks(ctx, protocol.ComposeProjectLabel+"="+in.Stack.ProjectName)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if err := eng.RemoveNetwork(ctx, n.ID); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			sc.Item(ctx, "network "+n.Name, domain.ItemSkipped, string(engine.CodeOf(err)))
		}
	}
	return s.updateRemoval(ctx, sc, func(o *protocol.SourceRemovalOutput) {
		for _, r := range removed {
			if !slices.Contains(o.RemovedContainers, r) {
				o.RemovedContainers = append(o.RemovedContainers, r)
			}
		}
	})
}

func (s *Service) removeVolumes(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := removalInput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	set, _, err := s.protectedSet(ctx, eng)
	if err != nil {
		return err
	}
	var removed []string
	for _, name := range in.Volumes {
		v, err := eng.InspectVolume(ctx, name)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if p := set.Volume(v.Name, v.Labels); p != nil {
			return protection.Check(p, protection.Remove, false)
		}
		if err := eng.RemoveVolume(ctx, name, false); err != nil {
			if engine.IsCode(err, engine.CodeConflict) {
				return &classed{class: "volume_in_use", err: err,
					recovery: "A container outside the migrated stack uses the volume; remove or change it, then confirm the removal again."}
			}
			return err
		}
		sc.Item(ctx, "volume "+name, domain.ItemSucceeded, "removed")
		removed = append(removed, name)
	}
	return s.updateRemoval(ctx, sc, func(o *protocol.SourceRemovalOutput) { o.RemovedVolumes = append(o.RemovedVolumes, removed...) })
}

func (s *Service) removeFiles(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := removalInput(sc)
	if err != nil {
		return err
	}
	res, err := s.storage()
	if err != nil {
		return err
	}
	root, err := stackRoot(res, in.Stack)
	if err != nil {
		return err
	}
	rfs, err := s.opts.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = rfs.Close() }()
	if _, err := rfs.Lstat(in.Stack.Dir); err != nil {
		return s.updateRemoval(ctx, sc, func(o *protocol.SourceRemovalOutput) {}) // already gone
	}
	if err := rfs.RemoveAll(in.Stack.Dir); err != nil {
		return err
	}
	sc.Item(ctx, "directory "+in.Stack.Dir, domain.ItemSucceeded, "removed")
	return s.updateRemoval(ctx, sc, func(o *protocol.SourceRemovalOutput) { o.RemovedDirectory = true })
}
