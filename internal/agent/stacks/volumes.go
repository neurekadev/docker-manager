package stacks

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Removing a stack with its volumes (stack.remove with RemoveVolumes):
// the volumes the stack owns are determined before anything is taken
// down (its containers still show which anonymous volumes are theirs)
// and journaled; after compose down each one is checked again and
// removed one per call, never forced. Never the Compose SDK's down
// --volumes: it would skip the protection (#32), hold and in-use checks.

// classProjectUnreadable: the definition could not be read, so the
// stack's own volumes cannot be told apart from others.
const classProjectUnreadable = "project_unreadable"

// planVolumes records the volumes the stack owns (once per job; a resumed
// attempt keeps the first plan):
//   - named volumes its definition declares, except external ones;
//   - anonymous volumes mounted by its containers (not declared at all).
func (s *Service) planVolumes(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput, eng engine.Engine) error {
	var planned bool
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { planned = o.VolumesPlanned }); err != nil {
		return err
	}
	if planned {
		return nil
	}
	p, _, err := s.project(ctx, in)
	if err != nil {
		return &stepError{class: classProjectUnreadable, err: err,
			recovery: "Nothing was changed. Fix the stack's definition, or delete the stack without its volumes (they stay on the host)."}
	}
	declared := map[string]bool{} // every declared name, external ones included
	var vols []protocol.StackVolume
	for _, v := range p.Resources().Volumes {
		if v.Name == "" {
			continue
		}
		declared[v.Name] = true
		if !v.External {
			vols = append(vols, protocol.StackVolume{Name: v.Name, Key: v.Key, Status: protocol.StackVolumePending})
		}
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true,
		Labels: []string{protocol.ComposeProjectLabel + "=" + in.Stack.ProjectName}})
	if err != nil {
		return err
	}
	anon := map[string]bool{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" && !declared[m.Name] {
				anon[m.Name] = true
			}
		}
	}
	names := make([]string, 0, len(anon))
	for n := range anon {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		vols = append(vols, protocol.StackVolume{Name: n, Anonymous: true, Status: protocol.StackVolumePending})
	}
	return update(ctx, sc, func(o *protocol.StackJobOutput) { o.Volumes, o.VolumesPlanned = vols, true })
}

// removeVolumes removes the planned volumes that are still the stack's,
// unused and unprotected, after its containers are gone. Volumes kept on
// purpose are skipped items (the job still succeeds).
func (s *Service) removeVolumes(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput, eng engine.Engine) error {
	var vols []protocol.StackVolume
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { vols = slices.Clone(o.Volumes) }); err != nil {
		return err
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return err
	}
	users := map[string]int{}
	for _, c := range cs {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				users[m.Name]++
			}
		}
	}
	var set *protect.Set // nil-safe: no guard, nothing protected (tests)
	if s.opts.Guard != nil {
		set = s.opts.Guard.Identify(ctx, eng, cs)
	}
	for i := range vols {
		v := &vols[i]
		if v.Status != protocol.StackVolumePending {
			continue // done by an earlier attempt
		}
		reason, err := s.removeVolume(ctx, eng, in, *v, users[v.Name] > 0, set.Volume)
		if err != nil {
			return err
		}
		if reason == "" {
			v.Status = protocol.StackVolumeRemoved
			sc.Item(ctx, "volume "+v.Name, domain.ItemSucceeded, "removed")
		} else {
			v.Status, v.Reason = protocol.StackVolumeKept, reason
			sc.Item(ctx, "volume "+v.Name, domain.ItemSkipped, reason)
		}
		if err := update(ctx, sc, func(o *protocol.StackJobOutput) { o.Volumes = slices.Clone(vols) }); err != nil {
			return err
		}
	}
	return nil
}

// removeVolume checks one planned volume again and removes it; a non-empty
// reason says why it stays.
func (s *Service) removeVolume(ctx context.Context, eng engine.Engine, in protocol.StackJobInput, v protocol.StackVolume, used bool,
	protected func(name string, labels map[string]string) *protocol.Protection) (string, error) {
	vol, err := eng.InspectVolume(ctx, v.Name)
	if engine.IsCode(err, engine.CodeNotFound) {
		return "already removed", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case v.Anonymous && !hasLabel(vol.Labels, protocol.AnonymousVolumeLabel):
		return "not an anonymous volume of this stack", nil
	case !v.Anonymous && (vol.Labels[protocol.ComposeProjectLabel] != in.Stack.ProjectName || vol.Labels[protocol.ComposeVolumeLabel] != v.Key):
		return "not created by this stack", nil
	case slices.Contains(in.KeepVolumes, v.Name):
		return "held by a migrated stack", nil
	case used:
		return "in use by another container", nil
	}
	if p := protected(v.Name, vol.Labels); p != nil {
		return "protected: " + p.Reason, nil
	}
	err = eng.RemoveVolume(ctx, v.Name, false)
	switch {
	case err == nil:
		return "", nil
	case engine.IsCode(err, engine.CodeNotFound):
		return "already removed", nil
	case engine.IsCode(err, engine.CodeConflict):
		return "in use by another container", nil
	}
	return "", errors.Join(errors.New("removing volume "+v.Name), err)
}

func hasLabel(labels map[string]string, key string) bool {
	_, ok := labels[key]
	return ok
}
