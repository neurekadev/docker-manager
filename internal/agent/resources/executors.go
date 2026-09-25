package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/regauth"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protection"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Executors returns the job executors of the Docker resource kinds (#6).
func (s *Service) Executors() []jobexec.Executor {
	action := func(kind domain.JobKind, step string, fn func(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error) jobexec.Executor {
		return jobexec.Executor{Kind: kind, Steps: map[string]jobexec.StepFunc{step: s.containerStep(fn)}}
	}
	return []jobexec.Executor{
		{Kind: jobspec.ContainerCreate, Steps: map[string]jobexec.StepFunc{
			"create": s.createContainer, "connect_networks": s.connectNetworks, "start": s.startCreated}},
		action(jobspec.ContainerStart, "start", s.start),
		action(jobspec.ContainerStop, "stop", s.stop),
		action(jobspec.ContainerRestart, "restart", s.restart),
		action(jobspec.ContainerPause, "pause", s.pause),
		action(jobspec.ContainerUnpause, "unpause", s.unpause),
		action(jobspec.ContainerRemove, "remove", s.remove),
		{Kind: jobspec.ContainerUpdate, Steps: map[string]jobexec.StepFunc{"update": s.update}},
		{Kind: jobspec.ImagePull, Steps: map[string]jobexec.StepFunc{"pull": s.pull}},
		{Kind: jobspec.ImageRemove, Steps: map[string]jobexec.StepFunc{"remove": s.removeImage}},
		{Kind: jobspec.VolumeCreate, Steps: map[string]jobexec.StepFunc{"create": s.createVolume}},
		{Kind: jobspec.VolumeRemove, Steps: map[string]jobexec.StepFunc{"remove": s.removeVolume}},
		{Kind: jobspec.NetworkCreate, Steps: map[string]jobexec.StepFunc{"create": s.createNetwork}},
		{Kind: jobspec.NetworkRemove, Steps: map[string]jobexec.StepFunc{"remove": s.removeNetwork}},
	}
}

// decode strictly decodes a job input.
func decode[T any](sc *jobexec.StepContext) (T, error) {
	var in T
	dec := json.NewDecoder(bytes.NewReader(sc.Input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, refuse(ClassInvalidInput, "The manager sent an input this agent does not understand; upgrade the agent.",
			"malformed %s input", sc.Kind)
	}
	return in, nil
}

func invalid(err error) error {
	return &OpError{Class: ClassInvalidInput, Message: err.Error(), recovery: "Correct the input and run the operation again.", err: err}
}

// Containers.

func (s *Service) createContainer(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ContainerCreateInput](sc)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	spec := in.Spec
	if err := s.checkMounts(ctx, eng, spec.Mounts); err != nil {
		return err
	}
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, in.Ownership)
	es := engine.ContainerSpec{Name: spec.Name, Image: spec.Image, Cmd: spec.Command, Entrypoint: spec.Entrypoint, Env: spec.Env,
		Labels: labels, WorkingDir: spec.WorkingDir, User: spec.User, RestartPolicy: spec.RestartPolicy,
		Resources: engine.Resources{NanoCPUs: spec.Resources.NanoCPUs, CPUShares: spec.Resources.CPUShares, Memory: spec.Resources.Memory,
			MemorySwap: spec.Resources.MemorySwap, PidsLimit: spec.Resources.PidsLimit}}
	for _, p := range spec.Ports {
		es.Ports = append(es.Ports, engine.PortBinding{ContainerPort: p.ContainerPort, Protocol: p.Protocol, HostIP: p.HostIP, HostPort: p.HostPort})
	}
	for _, m := range spec.Mounts {
		es.Mounts = append(es.Mounts, engine.MountSpec{Type: m.Type, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	if len(spec.Networks) > 0 {
		es.NetworkMode, es.NetworkAliases = spec.Networks[0].Name, spec.Networks[0].Aliases
	}
	if h := spec.Healthcheck; h != nil {
		es.Healthcheck = &engine.HealthcheckSpec{Test: h.Test, Interval: h.Interval, Timeout: h.Timeout, StartPeriod: h.StartPeriod, Retries: h.Retries}
	}
	sc.Progress(ctx, 10, "creating container "+spec.Name)
	id, warnings, err := eng.CreateContainer(ctx, es)
	if err != nil {
		if engine.CodeOf(err) == engine.CodeConflict {
			return &OpError{Class: ClassNameTaken, Message: err.Error(), err: err,
				recovery: "Another container already uses this name. Choose another name or remove the other container first."}
		}
		return engineErr(err)
	}
	msg := "created " + shortID(id)
	if len(warnings) > 0 {
		msg += "; Engine warnings: " + strings.Join(warnings, "; ")
	}
	sc.Item(ctx, spec.Name, domain.ItemSucceeded, bound(msg))
	return nil
}

func (s *Service) connectNetworks(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ContainerCreateInput](sc)
	if err != nil {
		return err
	}
	if len(in.Spec.Networks) < 2 {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	d, err := eng.InspectContainer(ctx, in.Spec.Name)
	if err != nil {
		return engineErr(err)
	}
	for _, n := range in.Spec.Networks[1:] {
		if _, ok := d.Networks[n.Name]; ok {
			continue // connected by an earlier attempt
		}
		if err := eng.ConnectNetwork(ctx, n.Name, d.ID, n.Aliases...); err != nil {
			return engineErr(err)
		}
	}
	sc.Progress(ctx, 60, "connected to networks")
	return nil
}

func (s *Service) startCreated(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ContainerCreateInput](sc)
	if err != nil {
		return err
	}
	if !in.Start {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	d, err := eng.InspectContainer(ctx, in.Spec.Name)
	if err != nil {
		return engineErr(err)
	}
	if d.State.Running {
		return nil
	}
	if err := eng.StartContainer(ctx, d.ID); err != nil && engine.CodeOf(err) != engine.CodeNotModified {
		return engineErr(err)
	}
	sc.Progress(ctx, 100, "started")
	return nil
}

// containerStep resolves the container of an action input by the ID the
// manager resolved: a container recreated under the same name meanwhile is
// never touched.
func (s *Service) containerStep(fn func(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error) jobexec.StepFunc {
	return func(ctx context.Context, sc *jobexec.StepContext) error {
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
		d, err := eng.InspectContainer(ctx, in.ID)
		if err != nil {
			if engine.CodeOf(err) == engine.CodeNotFound && sc.Kind == jobspec.ContainerRemove {
				// Already removed (e.g. by an earlier attempt of this job).
				sc.Item(ctx, in.Name, domain.ItemSucceeded, "already removed")
				return nil
			}
			return engineErr(err)
		}
		if d.Name != in.Name {
			return refuse(ClassRecreated, "Refresh the inventory and run the operation on the current container.",
				"container %s is no longer named %s", shortID(in.ID), in.Name)
		}
		// DockYard's own containers (#32), whatever the manager decided.
		set, err := s.protected(ctx, eng)
		if err != nil {
			return engineErr(err)
		}
		if err := protection.Check(set.Container(d.ID), containerAction[sc.Kind], in.Confirmed); err != nil {
			return err
		}
		return fn(ctx, eng, sc, in, d)
	}
}

// containerAction maps the container kinds to protection actions.
var containerAction = map[domain.JobKind]protection.Action{
	jobspec.ContainerStart: protection.Start, jobspec.ContainerStop: protection.Stop, jobspec.ContainerRestart: protection.Restart,
	jobspec.ContainerPause: protection.Pause, jobspec.ContainerUnpause: protection.Unpause, jobspec.ContainerRemove: protection.Remove,
	jobspec.ContainerUpdate: protection.Update,
}

func timeoutOf(in protocol.ContainerActionInput) *time.Duration {
	if in.TimeoutSeconds == nil {
		return nil
	}
	d := time.Duration(*in.TimeoutSeconds) * time.Second
	return &d
}

func done(ctx context.Context, sc *jobexec.StepContext, name, msg string) error {
	sc.Item(ctx, name, domain.ItemSucceeded, msg)
	sc.Progress(ctx, 100, msg)
	return nil
}

func (s *Service) start(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if d.State.Running {
		return done(ctx, sc, in.Name, "already running")
	}
	if err := eng.StartContainer(ctx, d.ID); err != nil && engine.CodeOf(err) != engine.CodeNotModified {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "started")
}

func (s *Service) stop(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if !d.State.Running {
		return done(ctx, sc, in.Name, "already stopped")
	}
	if err := eng.StopContainer(ctx, d.ID, timeoutOf(in)); err != nil && engine.CodeOf(err) != engine.CodeNotModified {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "stopped")
}

func (s *Service) restart(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if err := eng.RestartContainer(ctx, d.ID, timeoutOf(in)); err != nil {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "restarted")
}

func (s *Service) pause(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if d.State.Paused {
		return done(ctx, sc, in.Name, "already paused")
	}
	if err := eng.PauseContainer(ctx, d.ID); err != nil {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "paused")
}

func (s *Service) unpause(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if !d.State.Paused {
		return done(ctx, sc, in.Name, "not paused")
	}
	if err := eng.UnpauseContainer(ctx, d.ID); err != nil {
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "unpaused")
}

// stackManaged refuses direct edits of a DockYard-managed stack's
// containers (#6): they would be undone or conflict with the next deploy.
func (s *Service) stackManaged(labels map[string]string, what string) error {
	if st := s.stackOf(labels); st != nil && st.Managed {
		return refuse(ClassStackManaged, "Change the stack's Compose definition and redeploy it, or use the stack's own operations.",
			"%s belongs to the DockYard-managed stack %q; direct changes would conflict with the stack", what, st.Project)
	}
	return nil
}

func (s *Service) remove(ctx context.Context, eng engine.Engine, sc *jobexec.StepContext, in protocol.ContainerActionInput, d engine.ContainerDetails) error {
	if err := s.stackManaged(d.Labels, "container "+in.Name); err != nil {
		return err
	}
	if d.State.Running && !in.Force {
		return refuse(string(engine.CodeConflict), "Stop the container first, or remove it with force.",
			"container %s is running", in.Name)
	}
	if err := eng.RemoveContainer(ctx, d.ID, engine.RemoveOptions{Force: in.Force, Volumes: in.RemoveVolumes}); err != nil {
		if engine.CodeOf(err) == engine.CodeNotFound {
			return done(ctx, sc, in.Name, "already removed")
		}
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "removed")
}

func (s *Service) update(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ContainerUpdateInput](sc)
	if err != nil {
		return err
	}
	if !protocol.ValidRestartPolicy(in.RestartPolicy) {
		return invalid(errors.New("invalid restart policy"))
	}
	if in.Resources != nil {
		if err := in.Resources.Validate("resources"); err != nil {
			return invalid(err)
		}
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	d, err := eng.InspectContainer(ctx, in.ID)
	if err != nil {
		return engineErr(err)
	}
	if d.Name != in.Name {
		return refuse(ClassRecreated, "Refresh the inventory and run the operation on the current container.",
			"container %s is no longer named %s", shortID(in.ID), in.Name)
	}
	set, err := s.protected(ctx, eng)
	if err != nil {
		return engineErr(err)
	}
	if err := protection.Check(set.Container(d.ID), protection.Update, false); err != nil {
		return err
	}
	if err := s.stackManaged(d.Labels, "container "+in.Name); err != nil {
		return err
	}
	u := engine.ContainerUpdate{RestartPolicy: in.RestartPolicy}
	if r := in.Resources; r != nil {
		u.Resources = &engine.Resources{NanoCPUs: r.NanoCPUs, CPUShares: r.CPUShares, Memory: r.Memory, MemorySwap: r.MemorySwap, PidsLimit: r.PidsLimit}
	}
	warnings, err := eng.UpdateContainer(ctx, d.ID, u)
	if err != nil {
		return engineErr(err)
	}
	msg := "updated"
	if len(warnings) > 0 {
		msg += "; Engine warnings: " + strings.Join(warnings, "; ")
	}
	return done(ctx, sc, in.Name, bound(msg))
}

// Images.

func (s *Service) pull(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ImagePullInput](sc)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	// The credential of the selected registry connection travels with this
	// attempt only (#19); a named connection without one never falls back
	// to an anonymous pull.
	auth, err := regauth.ForReference(sc.Secrets, in.Reference, len(in.RegistryConnections) > 0)
	if err != nil {
		return &OpError{Class: domain.ErrorCredentialUnavailable, Message: err.Error(), err: err,
			recovery: "The registry connection's credential did not reach the agent. Check the registry connection and run the pull again."}
	}
	o := engine.PullOptions{Platform: in.Platform, Auth: auth}
	p := newPullProgress(func(percent int, msg string) { sc.Progress(ctx, percent, msg) })
	o.Progress = p.add
	res, err := eng.PullImage(ctx, in.Reference, o)
	if err != nil {
		return engineErr(err)
	}
	msg := "pulled " + shortID(res.ImageID)
	if res.Digest != "" {
		msg += " (" + res.Digest + ")"
	}
	return done(ctx, sc, in.Reference, msg)
}

func (s *Service) removeImage(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.ImageRemoveInput](sc)
	if err != nil {
		return err
	}
	if !protocol.ValidImageID(in.Image) {
		return invalid(errors.New("image must be an image ID"))
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	im, err := eng.InspectImage(ctx, in.Image)
	if err != nil {
		if engine.CodeOf(err) == engine.CodeNotFound {
			return done(ctx, sc, in.Image, "already removed")
		}
		return engineErr(err)
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return engineErr(err)
	}
	if err := protection.Check(s.guard.Identify(ctx, eng, cs).Image(im.ID), protection.Remove, false); err != nil {
		return err
	}
	if users := usersByImage(cs)[im.ID]; len(users) > 0 {
		return refuse(ClassImageInUse, "Remove the containers using the image first.",
			"image %s is used by %d container(s): %s", shortID(im.ID), len(users), namesOf(users))
	}
	deleted, err := eng.RemoveImage(ctx, im.ID, in.Force, true)
	if err != nil {
		if engine.CodeOf(err) == engine.CodeNotFound {
			return done(ctx, sc, in.Image, "already removed")
		}
		return engineErr(err)
	}
	for _, d := range deleted {
		if d.Untagged != "" {
			sc.Item(ctx, d.Untagged, domain.ItemSucceeded, "untagged")
		}
	}
	return done(ctx, sc, in.Image, fmt.Sprintf("removed %s", shortID(im.ID)))
}

// Volumes.

func (s *Service) createVolume(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.VolumeCreateInput](sc)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	v, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: in.Name, Driver: in.Driver, DriverOpts: in.DriverOpts, Labels: in.Labels})
	if err != nil {
		return engineErr(err)
	}
	return done(ctx, sc, v.Name, "created")
}

func (s *Service) removeVolume(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.VolumeRemoveInput](sc)
	if err != nil {
		return err
	}
	if !protocol.ValidDockerName(in.Name) {
		return invalid(errors.New("invalid volume name"))
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	v, err := eng.InspectVolume(ctx, in.Name)
	if err != nil {
		if engine.CodeOf(err) == engine.CodeNotFound {
			return done(ctx, sc, in.Name, "already removed")
		}
		return engineErr(err)
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return engineErr(err)
	}
	if err := protection.Check(s.guard.Identify(ctx, eng, cs).Volume(v.Name, v.Labels), protection.Remove, false); err != nil {
		return err
	}
	if st := objectStack(v.Labels, s.managedProjects(cs)); st != nil && st.Managed {
		return refuse(ClassStackManaged, "Remove the volume from the stack's Compose definition (or remove the stack) instead.",
			"volume %s belongs to the DockYard-managed stack %q", in.Name, st.Project)
	}
	if users := usersByVolume(cs)[v.Name]; len(users) > 0 {
		return refuse(ClassVolumeInUse, "Remove the containers using the volume first.",
			"volume %s is used by %d container(s): %s", in.Name, len(users), namesOf(users))
	}
	if err := eng.RemoveVolume(ctx, v.Name, false); err != nil {
		switch engine.CodeOf(err) {
		case engine.CodeNotFound:
			return done(ctx, sc, in.Name, "already removed")
		case engine.CodeConflict:
			return &OpError{Class: ClassVolumeInUse, Message: err.Error(), err: err, recovery: "Remove the containers using the volume first."}
		}
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "removed")
}

// Networks.

func (s *Service) createNetwork(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.NetworkCreateInput](sc)
	if err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return invalid(err)
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	id, err := eng.CreateNetwork(ctx, engine.NetworkSpec{Name: in.Name, Driver: in.Driver, Internal: in.Internal, Attachable: in.Attachable,
		Labels: in.Labels, Options: in.Options})
	if err != nil {
		if engine.CodeOf(err) == engine.CodeConflict {
			return &OpError{Class: ClassNameTaken, Message: err.Error(), err: err,
				recovery: "A network with this name already exists. Check the network list before creating it again."}
		}
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "created "+shortID(id))
}

func (s *Service) removeNetwork(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := decode[protocol.NetworkRemoveInput](sc)
	if err != nil {
		return err
	}
	if !protocol.ValidDockerName(in.Name) || in.ID == "" {
		return invalid(errors.New("the input names no network"))
	}
	eng, err := s.engine()
	if err != nil {
		return engineErr(err)
	}
	n, err := eng.InspectNetwork(ctx, in.ID)
	if err != nil {
		if engine.CodeOf(err) == engine.CodeNotFound {
			return done(ctx, sc, in.Name, "already removed")
		}
		return engineErr(err)
	}
	if n.Name != in.Name {
		return refuse(ClassRecreated, "Refresh the inventory and run the operation on the current network.",
			"network %s is no longer named %s", shortID(in.ID), in.Name)
	}
	if isBuiltin(n.Name) {
		return refuse(ClassNetworkBuiltin, "Predefined networks cannot be removed.", "%s is a predefined network", n.Name)
	}
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return engineErr(err)
	}
	if err := protection.Check(s.guard.Identify(ctx, eng, cs).Network(n.ID, n.Name, n.Labels), protection.Remove, false); err != nil {
		return err
	}
	if st := objectStack(n.Labels, s.managedProjects(cs)); st != nil && st.Managed {
		return refuse(ClassStackManaged, "Remove the network from the stack's Compose definition (or remove the stack) instead.",
			"network %s belongs to the DockYard-managed stack %q", in.Name, st.Project)
	}
	if len(n.Containers) > 0 {
		return refuse(ClassNetworkInUse, "Disconnect or remove the attached containers first.",
			"network %s has %d attached container(s)", in.Name, len(n.Containers))
	}
	if err := eng.RemoveNetwork(ctx, n.ID); err != nil {
		switch engine.CodeOf(err) {
		case engine.CodeNotFound:
			return done(ctx, sc, in.Name, "already removed")
		case engine.CodeConflict:
			return &OpError{Class: ClassNetworkInUse, Message: err.Error(), err: err, recovery: "Disconnect or remove the attached containers first."}
		}
		return engineErr(err)
	}
	return done(ctx, sc, in.Name, "removed")
}

func isBuiltin(name string) bool {
	for _, b := range protocol.BuiltinNetworks {
		if b == name {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// checkMounts refuses mounts that would expose DockYard's own data (#32):
// its volumes (manager data, agent state, stacks, backup repositories) and
// bind mounts of the Engine's data root, a directory inside it or one of
// its ancestors (every volume lives there).
func (s *Service) checkMounts(ctx context.Context, eng engine.Engine, mounts []protocol.MountSpec) error {
	if len(mounts) == 0 {
		return nil
	}
	set, err := s.protected(ctx, eng)
	if err != nil {
		return engineErr(err)
	}
	for _, m := range mounts {
		switch m.Type {
		case "volume":
			if m.Source == "" {
				continue
			}
			if err := protection.Check(set.Volume(m.Source, nil), protection.Mount, false); err != nil {
				return err
			}
		case "bind":
			if root := set.DockerRootDir; root != "" && (pathWithin(m.Source, root) || pathWithin(root, m.Source)) {
				return &protection.Refusal{Code: protection.CodeProtected, Action: protection.Mount,
					Reason: "refused to bind " + m.Source + ": it is or contains the Docker data root, which holds DockYard's own volumes"}
			}
		}
	}
	return nil
}

// pathWithin reports whether p is dir or below it (clean absolute paths).
func pathWithin(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}
