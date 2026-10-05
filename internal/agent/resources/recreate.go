package resources

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// recreate replaces a standalone container with a new one from its whole
// configuration (Compose's --force-recreate for a container outside any
// Compose project): the old one is renamed aside, the new one created
// under its name from the image its reference names on the host now (never
// pulled), then the old one stopped and removed (its anonymous volumes
// stay: the new one mounts them by name) and the new one started when the
// old one ran (also paused or restarting). A failed create puts the old
// container back. The labels are copied, so a saved recreate specification
// (automatic updates) stays attached. Idempotent: a repeated attempt finds
// the container set aside and the new one; the result output keeps
// whether the old one ran.
func (s *Service) recreate(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ContainerActionInput](sc)
	if err != nil {
		return err
	}
	if in.ID == "" || !protocol.ValidDockerName(in.Name) {
		return invalid(errors.New("the input names no container"))
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	cl, ok := eng.(engine.Cloner)
	if !ok {
		return engineErr(&engine.Error{Op: "container.clone", Code: engine.CodeUnsupported, Message: "this Engine cannot recreate containers"})
	}
	var out protocol.ContainerRecreateOutput
	recorded := len(sc.Output()) > 0 && json.Unmarshal(sc.Output(), &out) == nil
	old, err := eng.InspectContainer(ctx, in.ID)
	if engine.CodeOf(err) == engine.CodeNotFound && out.ContainerID != "" {
		// An earlier attempt removed the old container: only the start is left.
		return s.startRecreated(ctx, eng, sc, in, out)
	}
	if err != nil {
		return engineErr(err)
	}
	aside := in.Name + protocol.RecreateAsideInfix + shortID(in.ID)
	if old.Name != in.Name && old.Name != aside {
		return refuse(ClassRecreated, "Refresh the inventory and run the operation on the current container.",
			"container %s is no longer named %s", shortID(in.ID), in.Name)
	}
	if err := s.recreatable(ctx, eng, in.Name, old); err != nil {
		return err
	}
	if !recorded {
		out = protocol.ContainerRecreateOutput{WasRunning: old.State.Running}
		if err := sc.SetOutput(ctx, out); err != nil {
			return err
		}
	}
	// The new container of an earlier attempt holds the name already.
	if out.ContainerID == "" && old.Name == aside {
		cur, err := eng.InspectContainer(ctx, in.Name)
		switch {
		case err == nil:
			out.ContainerID = cur.ID
		case engine.CodeOf(err) != engine.CodeNotFound:
			return engineErr(err)
		}
	}
	if out.ContainerID == "" {
		if old.Name != aside {
			if err := eng.RenameContainer(ctx, old.ID, aside); err != nil {
				return engineErr(err)
			}
		}
		sc.Progress(ctx, 20, "creating the new "+in.Name)
		// Docker Manager's labels go under their current keys.
		id, err := cl.CloneContainer(ctx, old.ID, engine.CloneOptions{Name: in.Name, RenameLabels: protocol.LegacyLabelRenames(), CurrentImage: true})
		if err != nil {
			// Put the old container back (it still runs if it ran).
			if rerr := eng.RenameContainer(context.WithoutCancel(ctx), old.ID, in.Name); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return engineErr(err)
		}
		out.ContainerID = id
		if err := sc.SetOutput(ctx, out); err != nil {
			return err
		}
	}
	// The old container goes; its anonymous volumes stay for the new one.
	if old.State.Running {
		sc.Progress(ctx, 50, "stopping the old "+in.Name)
		if err := eng.StopContainer(ctx, old.ID, timeoutOf(in)); err != nil && engine.CodeOf(err) != engine.CodeNotModified &&
			engine.CodeOf(err) != engine.CodeNotFound {
			return engineErr(err)
		}
	}
	if err := eng.RemoveContainer(ctx, old.ID, engine.RemoveOptions{Force: true}); err != nil && !s.removedAlready(ctx, eng, old.ID, err) {
		return engineErr(err)
	}
	return s.startRecreated(ctx, eng, sc, in, out)
}

// Waiting for an AutoRemove container to go (removedAlready).
const (
	selfRemoveTimeout = 30 * time.Second
	selfRemovePoll    = 200 * time.Millisecond
)

// removedAlready reports whether a failed removal of the old container
// only met the container going away by itself: gone already, or (an
// AutoRemove, --rm, container removing itself once stopped) the Engine
// refusing a removal already in progress, in which case it waits, at most
// selfRemoveTimeout, until the container is gone. A container left dead
// or still there is not removed.
func (s *Service) removedAlready(ctx context.Context, eng engine.Engine, id string, err error) bool {
	switch engine.CodeOf(err) {
	case engine.CodeNotFound:
		return true
	case engine.CodeConflict:
	default:
		return false
	}
	deadline := s.clock.NewTimer(selfRemoveTimeout)
	defer deadline.Stop()
	for {
		d, ierr := eng.InspectContainer(ctx, id)
		if engine.CodeOf(ierr) == engine.CodeNotFound {
			return true
		}
		if ierr != nil || d.State.Status != "removing" {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C():
			return false
		case <-s.clock.After(selfRemovePoll):
		}
	}
}

// startRecreated starts the new container when the old one ran.
func (s *Service) startRecreated(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput,
	out protocol.ContainerRecreateOutput) error {
	if !out.WasRunning {
		return done(ctx, sc, in.Name, "recreated; not started, like the old container")
	}
	sc.Progress(ctx, 80, "starting "+in.Name)
	if err := eng.StartContainer(ctx, out.ContainerID); err != nil && engine.CodeOf(err) != engine.CodeNotModified {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "recreated and started")
}

// recreatable refuses what container.recreate never replaces, whatever the
// manager decided: Docker Manager's own containers (#32), the containers of
// a Compose project (their project recreates them), Docker Manager's and
// Compose's temporary containers and a container being removed.
func (s *Service) recreatable(ctx context.Context, eng engine.Engine, name string, d engine.ContainerDetails) error {
	set, err := s.protected(ctx, eng)
	if err != nil {
		return engineErr(err)
	}
	if err := protection.Check(set.Container(d.ID), containerAction[jobspec.ContainerRecreate], false); err != nil {
		return err
	}
	if err := s.stackManaged(d.Labels, "container "+name); err != nil {
		return err
	}
	if st := s.stackOf(d.Labels); st != nil {
		return refuse(ClassComposeProject, "Import the project as a stack to manage it in Docker Manager, or recreate it with Compose.",
			"container %s belongs to the Compose project %q, which Docker Manager does not manage", name, st.Project)
	}
	if protocol.IsHelperContainer(name, d.Labels) {
		return refuse(string(engine.CodeConflict), "Leave it to the operation that created it, or remove it.",
			"container %s is a temporary container of Docker Manager or Compose", name)
	}
	if d.State.Status == "removing" {
		return refuse(string(engine.CodeConflict), "Wait until the removal finished.", "container %s is being removed", name)
	}
	return nil
}
