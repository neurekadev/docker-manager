package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Rename (#7): stack.rename moves a stack's Compose project to another
// project name. Compose names a project's volumes, networks and containers
// after the project, so a new name alone would start the stack with empty
// volumes; the rename carries the data over:
//
//  1. prepare: planRename (also compose.rename_preview) decides what moves
//     and refuses (Blockers) what it cannot do safely. Nothing has changed.
//  2. stop_containers: the services that run stop in dependency order, and
//     the containers outside the stack that mount a volume that moves
//     (the start_containers compensation is journaled first).
//  3. move: every named volume whose name follows the project moves to the
//     new project's name: a plain local volume by renaming its data
//     directory into a new volume (same disk, nothing is copied), a volume
//     with driver options by a new volume with the same options (its data
//     stays where they point). The project directory is renamed when it is
//     named after the project. The undo_rename compensation (journaled
//     first) moves everything back on any failure until the switch.
//  4. recreate: the switch (journaled): the outside containers are
//     recreated on the new volume names with their complete configuration,
//     the old project's containers and networks are removed, the project is
//     created (not started) under the new name from the current files, the
//     anonymous volumes' data moves into the new containers' ones and the
//     old volumes are removed.
//  5. start_containers: what ran before starts again, dependencies first.
//
// After the switch the stack lives under the new name even when a later
// step fails (the manager records it); a deploy finishes it (no automatic
// rollback, #25). A Compose file with a top-level name: pins the project
// name: such a stack is renamed by changing name: in the file, and the only
// rename allowed is to that name.

// Rename error classes (stable, #26).
const (
	classRenameRefused = "stack_rename_refused"
	// classProjectRenamed: a deploy found a top-level name: that differs
	// from the stack's project name.
	classProjectRenamed = "stack_project_renamed"
)

// Blocker and warning codes of a rename plan.
const (
	renameNameInFile      = "name_in_file"
	renameNameMismatch    = "name_mismatch"
	renameOwnProject      = "own_project"
	renameDirectoryExists = "directory_exists"
	renameProjectExists   = "project_exists"
	renameVolumeExists    = "volume_exists"
	renameVolumeForeign   = "volume_foreign"
	renameVolumeProtected = "volume_protected"
	renameVolumeHeld      = "volume_held"
	renameVolumeDriver    = "volume_not_movable"
	renameVolumeShared    = "volume_shared_with_project"
	renameNetworkInUse    = "network_in_use"
)

const (
	recoveryRenameUnchanged = "Nothing changed: what moved was moved back and what ran started again under the old name. " +
		"Fix the cause and rename again."
	recoveryRenameSwitched = "The stack now lives under its new name (its volumes and directory moved). Deploy the stack to finish; " +
		"the job's warnings list anything that was not moved or started."
)

// labelContainerNumber is Compose's replica number of a container.
const labelContainerNumber = "com.docker.compose.container-number"

func renameRefusal(format string, args ...any) error {
	return &stepError{class: classRenameRefused, recovery: recoveryRenameUnchanged, err: fmt.Errorf(format, args...)}
}

// renamedFailure classifies a failure after the switch.
func renamedFailure(err error) error {
	class := domain.ErrorStepFailed
	var ce jobexec.ClassedError
	if errors.As(classify(err), &ce) && ce.ErrorClass() != "" {
		class = ce.ErrorClass()
	}
	return &stepError{class: class, recovery: recoveryRenameSwitched, err: err}
}

// renameResume is the start_containers compensation's record.
type renameResume struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	WasRunning []string `json:"wasRunning"`
	// Outside are the names of the outside containers that ran.
	Outside []string `json:"outside,omitempty"`
}

// renameUndo is the undo_rename compensation's record.
type renameUndo struct {
	VolumesDir string                       `json:"volumesDir"`
	Volumes    []protocol.StackRenameVolume `json:"volumes"`
	FromDir    string                       `json:"fromDir"`
	ToDir      string                       `json:"toDir"`
}

func (s *Service) renameExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.StackRename, Steps: map[string]jobexec.StepFunc{
		"prepare":          classified(s.renamePrepare),
		"stop_containers":  classified(s.renameStop),
		"move":             classified(s.renameMove),
		"recreate":         classified(s.renameRecreate),
		"start_containers": classified(s.renameStart),
	}, Compensations: map[string]jobexec.CompensationFunc{
		jobspec.CompStartContainers: s.renameCompStart,
		jobspec.CompUndoRename:      s.renameCompUndo,
	}}
}

func renameInput(sc *jobexec.StepContext) (protocol.StackJobInput, error) {
	in, err := input(sc)
	if err != nil {
		return in, err
	}
	if in.Rename == nil {
		return in, errors.New("stack.rename needs the new project name")
	}
	return in, in.Rename.Validate(in.Stack)
}

// renameReport reads the journaled report (nil before prepare).
func renameReport(sc *jobexec.StepContext) *protocol.StackRenameReport {
	var o protocol.StackJobOutput
	if b := sc.Output(); len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	return o.Rename
}

func updateRename(ctx context.Context, sc *jobexec.StepContext, fn func(o *protocol.StackJobOutput, r *protocol.StackRenameReport)) error {
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		if o.Rename == nil {
			o.Rename = &protocol.StackRenameReport{}
		}
		fn(o, o.Rename)
	})
}

// renamed is the project reference after the rename.
func renamed(ref protocol.ProjectRef, r protocol.StackRename) protocol.ProjectRef {
	ref.ProjectName, ref.Dir = r.ProjectName, r.Dir
	return ref
}

// renamePreview serves compose.rename_preview.
func (s *Service) renamePreview(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.ComposeRenamePreviewInput](input)
	if err != nil {
		return nil, err
	}
	if err := in.Rename.Validate(in.Stack); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
	}
	plan, err := s.planRename(ctx, in.Stack, in.Rename, in.KeepVolumes)
	if err != nil {
		return nil, handlerError(err)
	}
	return plan, nil
}

// planRename decides what a rename of ref moves. It changes nothing.
func (s *Service) planRename(ctx context.Context, ref protocol.ProjectRef, r protocol.StackRename, keep []string) (protocol.StackRenamePlan, error) {
	plan := protocol.StackRenamePlan{From: ref.ProjectName, To: r.ProjectName, FromDir: ref.Dir, ToDir: r.Dir}
	block := func(code, format string, args ...any) {
		plan.Blockers = append(plan.Blockers, protocol.ComposeIssue{Code: code, Message: fmt.Sprintf(format, args...)})
	}
	eng, err := s.engine()
	if err != nil {
		return plan, err
	}
	dir, err := s.resolve(ref)
	if err != nil {
		return plan, err
	}
	res := s.opts.Deps.Storage()
	all := allProfiles(ref)
	declared, err := compose.DeclaredName(ctx, specOf(all, dir))
	if err != nil {
		return plan, err
	}
	plan.DeclaredName = declared
	switch {
	case declared == ref.ProjectName:
		block(renameNameInFile, "its Compose file sets name: %s, which names the project: change name: in the file to rename it", declared)
	case declared != "" && declared != r.ProjectName:
		block(renameNameMismatch, "its Compose file sets name: %s: the project can only be renamed to %s", declared, declared)
	}
	if r.Dir != ref.Dir {
		to, err := ResolveProjectDir(res, renamed(ref, r))
		if err != nil {
			return plan, err
		}
		if _, err := os.Lstat(to); err == nil {
			block(renameDirectoryExists, "the directory %s already exists; nothing is overwritten", r.Dir)
		}
	}
	old, err := compose.LoadProject(ctx, specOf(all, dir))
	if err != nil {
		return plan, err
	}
	next, err := compose.LoadProject(ctx, specOf(renamed(all, r), dir))
	if err != nil {
		return plan, err
	}
	containers, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return plan, err
	}
	var set *protect.Set
	if s.opts.Guard != nil {
		set = s.opts.Guard.Identify(ctx, eng, containers)
		if set.Project(ref.ProjectName) != nil {
			block(renameOwnProject, "this is Docker Manager's own Compose project: it is never renamed")
		}
	}
	var mine []engine.Container
	for _, c := range containers {
		switch c.Labels[lifecycle.ComposeProjectLabel] {
		case ref.ProjectName:
			mine = append(mine, c)
			if svc := c.Labels[lifecycle.ComposeServiceLabel]; isRunning(c) && !slices.Contains(plan.Running, svc) {
				plan.Running = append(plan.Running, svc)
			}
		case r.ProjectName:
			if !slices.ContainsFunc(plan.Blockers, func(i protocol.ComposeIssue) bool { return i.Code == renameProjectExists }) {
				block(renameProjectExists, "containers of a Compose project named %s already exist on this Engine", r.ProjectName)
			}
		}
	}
	slices.Sort(plan.Running)

	// Named volumes whose name follows the project.
	newNames := map[string]string{}
	for _, v := range next.Resources().Volumes {
		newNames[v.Key] = v.Name
	}
	declaredNames := map[string]bool{}
	for _, v := range old.Resources().Volumes {
		declaredNames[v.Name] = true
		to := newNames[v.Key]
		if v.External || v.Name == "" || to == "" || to == v.Name {
			continue
		}
		rv := protocol.StackRenameVolume{Key: v.Key, Name: v.Name, NewName: to}
		if _, err := eng.InspectVolume(ctx, to); err == nil {
			block(renameVolumeExists, "a volume %s already exists; nothing is overwritten", to)
		} else if !engine.IsCode(err, engine.CodeNotFound) {
			return plan, err
		}
		vol, err := eng.InspectVolume(ctx, v.Name)
		switch {
		case engine.IsCode(err, engine.CodeNotFound):
			rv.Action = protocol.RenameVolumeAbsent
			plan.Volumes = append(plan.Volumes, rv)
			continue
		case err != nil:
			return plan, err
		}
		rv.Action = protocol.RenameVolumeMove
		switch {
		case vol.Labels[protocol.ComposeProjectLabel] != ref.ProjectName || vol.Labels[protocol.ComposeVolumeLabel] != v.Key:
			block(renameVolumeForeign, "volume %s was not created by this project; it is not renamed", v.Name)
		case slices.Contains(keep, v.Name):
			block(renameVolumeHeld, "volume %s is held by a migrated stack", v.Name)
		case set.Volume(v.Name, vol.Labels) != nil:
			block(renameVolumeProtected, "volume %s is Docker Manager's own", v.Name)
		case vol.Driver != "local":
			block(renameVolumeDriver, "volume %s uses the %s driver: its data cannot be moved to a new name", v.Name, vol.Driver)
		case len(vol.Options) > 0:
			rv.Action = protocol.RenameVolumeRecreate
		default:
			if why := movable(res, vol); why != "" {
				block(renameVolumeDriver, "volume %s cannot be moved: %s", v.Name, why)
			}
		}
		plan.Volumes = append(plan.Volumes, rv)
	}
	// Anonymous volumes of the stack's containers: their data moves into the
	// new containers' anonymous volumes.
	for _, c := range mine {
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" || declaredNames[m.Name] {
				continue
			}
			rv := protocol.StackRenameVolume{Name: m.Name, Service: c.Labels[lifecycle.ComposeServiceLabel],
				Number: c.Labels[labelContainerNumber], Target: m.Destination, Action: protocol.RenameVolumeMove}
			if vol, err := eng.InspectVolume(ctx, m.Name); err != nil {
				return plan, err
			} else if why := movable(res, vol); why != "" {
				block(renameVolumeDriver, "the anonymous volume of service %s at %s cannot be moved: %s", rv.Service, m.Destination, why)
			}
			plan.Volumes = append(plan.Volumes, rv)
		}
	}
	// Containers outside the stack that mount a volume that moves.
	moving := map[string]bool{}
	for _, v := range plan.Volumes {
		if v.Action != protocol.RenameVolumeAbsent {
			moving[v.Name] = true
		}
	}
	for _, c := range containers {
		if c.Labels[lifecycle.ComposeProjectLabel] == ref.ProjectName {
			continue
		}
		var uses []string
		for _, m := range c.Mounts {
			if m.Type == "volume" && moving[m.Name] && !slices.Contains(uses, m.Name) {
				uses = append(uses, m.Name)
			}
		}
		if len(uses) == 0 {
			continue
		}
		name := containerName(c)
		switch p := c.Labels[lifecycle.ComposeProjectLabel]; {
		case set.Container(c.ID) != nil:
			block(renameVolumeProtected, "Docker Manager's container %s uses volume %s", name, strings.Join(uses, ", "))
		case p != "":
			block(renameVolumeShared, "container %s of Compose project %s uses volume %s: that project's files name the volume, "+
				"so it would lose it. Change that project first", name, p, strings.Join(uses, ", "))
		default:
			plan.Containers = append(plan.Containers, protocol.StackRenameContainer{ID: c.ID, Name: name, Running: isRunning(c), Volumes: uses})
		}
	}
	// Networks of the project that outside containers are attached to
	// would block removing the old project.
	nets, err := eng.ListNetworks(ctx, protocol.ComposeProjectLabel+"="+ref.ProjectName)
	if err != nil {
		return plan, err
	}
	inProject := map[string]bool{}
	for _, c := range mine {
		inProject[c.ID] = true
	}
	for _, n := range nets {
		d, err := eng.InspectNetwork(ctx, n.ID)
		if err != nil {
			return plan, err
		}
		for id := range d.Containers {
			if !inProject[id] {
				block(renameNetworkInUse, "container %s outside the stack is attached to network %s: disconnect it first", shortID(id), n.Name)
			}
		}
	}
	return plan, nil
}

// movable returns why a local volume's data cannot be moved ("" when it
// can): it must live in Docker's volume directory, which the agent mounts.
func movable(res *storage.Result, v engine.Volume) string {
	if res == nil {
		return "the storage layout has not been verified yet"
	}
	if acc := res.AccessFor(v); !acc.Supported {
		return acc.Reason
	}
	if path.Clean(filepath.ToSlash(v.Mountpoint)) != path.Join(res.VolumesDir, v.Name, "_data") {
		return "its data is not in Docker's volume directory"
	}
	return ""
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func blockerText(p protocol.StackRenamePlan) string {
	msgs := make([]string, 0, len(p.Blockers))
	for _, b := range p.Blockers {
		msgs = append(msgs, b.Message)
	}
	return strings.Join(msgs, "; ")
}

// renamePrepare plans the rename and records it; blockers refuse it.
func (s *Service) renamePrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := renameInput(sc)
	if err != nil {
		return err
	}
	if r := renameReport(sc); r != nil && r.Stopped {
		return nil // a resumed attempt keeps its plan
	}
	plan, err := s.planRename(ctx, in.Stack, *in.Rename, in.KeepVolumes)
	if err != nil {
		return err
	}
	if len(plan.Blockers) > 0 {
		return renameRefusal("%s", blockerText(plan))
	}
	eng, _ := s.engine()
	before, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 5, fmt.Sprintf("renaming %s to %s: %d volumes, %d containers outside the stack", plan.From, plan.To,
		len(plan.Volumes), len(plan.Containers)))
	return updateRename(ctx, sc, func(o *protocol.StackJobOutput, r *protocol.StackRenameReport) {
		o.Before = before
		r.StackRenamePlan = plan
	})
}

func (s *Service) renameLifecycle(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput) lifecycle.Options {
	return s.importLifecycle(ctx, sc, in)
}

// renameStop stops what runs (the compensation first) and makes sure
// nothing that uses a volume that moves still runs.
func (s *Service) renameStop(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := renameInput(sc)
	if err != nil {
		return err
	}
	r := renameReport(sc)
	if r == nil {
		return errors.New("the rename was not planned")
	}
	if r.Stopped {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	var outside []string
	for _, c := range r.Containers {
		if c.Running {
			outside = append(outside, c.Name)
		}
	}
	if len(r.Running) > 0 || len(outside) > 0 {
		if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, renameResume{From: r.From, To: r.To,
			WasRunning: r.Running, Outside: outside}); err != nil {
			return err
		}
	}
	opts := s.renameLifecycle(ctx, sc, in)
	if len(r.Running) > 0 {
		list, err := lifecycle.ProjectContainers(ctx, eng, r.From)
		if err != nil {
			return err
		}
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		sc.Progress(ctx, 10, "stopping "+strings.Join(r.Running, ", "))
		rt := lifecycle.EngineRuntime{Engine: eng, Project: r.From, Clock: s.opts.Clock}
		if _, err := lifecycle.Stop(ctx, g, rt, r.Running, opts); err != nil {
			return renameRefusal("stopping the stack failed: %v", err)
		}
	}
	for _, c := range r.Containers {
		if !c.Running {
			continue
		}
		sc.Progress(ctx, 15, "stopping "+c.Name+" (outside the stack)")
		if err := eng.StopContainer(ctx, c.ID, opts.StopTimeout); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
			return renameRefusal("stopping container %s failed: %v", c.Name, err)
		}
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, r.From)
	if err != nil {
		return err
	}
	for _, c := range list {
		if isRunning(c) {
			return renameRefusal("container %s is still %s: the stack is not renamed while it runs", containerName(c), c.State)
		}
	}
	for _, c := range r.Containers {
		if d, err := eng.InspectContainer(ctx, c.ID); err == nil && d.State.Running {
			return renameRefusal("container %s is still running: its volumes are not moved while it runs", c.Name)
		}
	}
	return updateRename(ctx, sc, func(_ *protocol.StackJobOutput, r *protocol.StackRenameReport) { r.Stopped = true })
}

// projectDirs returns the project directory now and after the rename.
func (s *Service) projectDirs(in protocol.StackJobInput) (from, to string, err error) {
	if from, err = s.resolve(in.Stack); err != nil {
		return "", "", err
	}
	if to, err = s.resolve(renamed(in.Stack, *in.Rename)); err != nil {
		return "", "", err
	}
	return from, to, nil
}

// renameMove moves the named volumes and the project directory (undone
// by undo_rename until the switch).
func (s *Service) renameMove(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := renameInput(sc)
	if err != nil {
		return err
	}
	r := renameReport(sc)
	if r == nil || !r.Stopped {
		return errors.New("the stack was not stopped")
	}
	if r.Switched {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	from, to, err := s.projectDirs(in)
	if err != nil {
		return err
	}
	res := s.opts.Deps.Storage()
	volumesDir := ""
	if res != nil {
		volumesDir = res.VolumesDir
	}
	var named []protocol.StackRenameVolume
	for _, v := range r.Volumes {
		if v.Key != "" && v.Action != protocol.RenameVolumeAbsent {
			named = append(named, v)
		}
	}
	if err := sc.AddCompensation(ctx, jobspec.CompUndoRename, renameUndo{VolumesDir: volumesDir, Volumes: named,
		FromDir: filepath.ToSlash(from), ToDir: filepath.ToSlash(to)}); err != nil {
		return err
	}
	cur := from
	if r.DirMoved {
		cur = to
	}
	next, err := compose.LoadProject(ctx, specOf(renamed(allProfiles(in.Stack), *in.Rename), cur))
	if err != nil {
		return renameRefusal("the project does not load under its new name: %v", err)
	}
	for i := range r.Volumes {
		v := r.Volumes[i]
		if v.Key == "" || v.Action == protocol.RenameVolumeAbsent || v.Done {
			continue
		}
		sc.Progress(ctx, 30, "moving volume "+v.Name+" to "+v.NewName)
		if err := s.moveNamedVolume(ctx, eng, next, volumesDir, v); err != nil {
			return renameRefusal("moving volume %s to %s failed: %v", v.Name, v.NewName, err)
		}
		if err := updateRename(ctx, sc, func(_ *protocol.StackJobOutput, rep *protocol.StackRenameReport) {
			rep.Volumes[i].Done = true
		}); err != nil {
			return err
		}
		sc.Item(ctx, "volume "+v.Name, domain.ItemSucceeded, "moved to "+v.NewName)
	}
	if from != to && !r.DirMoved {
		sc.Progress(ctx, 45, "renaming the project directory to "+in.Rename.Dir)
		if err := moveDir(from, to); err != nil {
			return renameRefusal("renaming the project directory failed: %v", err)
		}
		if err := updateRename(ctx, sc, func(_ *protocol.StackJobOutput, rep *protocol.StackRenameReport) { rep.DirMoved = true }); err != nil {
			return err
		}
	}
	return nil
}

// moveNamedVolume creates v.NewName exactly as Compose would for the
// renamed project (so Compose adopts it unchanged) and moves the data into
// it. Idempotent.
func (s *Service) moveNamedVolume(ctx context.Context, eng engine.Engine, next *compose.Project, volumesDir string, v protocol.StackRenameVolume) error {
	spec, err := next.VolumeSpec(v.Key)
	if err != nil {
		return err
	}
	if spec.Name != v.NewName {
		return fmt.Errorf("the renamed project names volume %s %s, not %s", v.Key, spec.Name, v.NewName)
	}
	created, err := eng.CreateVolume(ctx, spec)
	if err != nil {
		return err
	}
	if v.Action == protocol.RenameVolumeRecreate {
		return nil
	}
	if path.Clean(filepath.ToSlash(created.Mountpoint)) != path.Join(volumesDir, v.NewName, "_data") {
		return fmt.Errorf("the new volume's data is not in Docker's volume directory (%s)", created.Mountpoint)
	}
	return moveVolumeData(volumesDir, v.Name, v.NewName, false)
}

// moveVolumeData moves the data directory of volume from into volume to,
// below Docker's volume directory (never through a symlink). to's own data
// directory must be empty unless replace is set (a fresh anonymous volume
// Docker filled from the image). A repeated call finds it done.
func moveVolumeData(volumesDir, from, to string, replace bool) error {
	if volumesDir == "" {
		return errors.New("the Docker volume directory is not known")
	}
	for _, n := range []string{from, to} {
		if !protocol.ValidRelativePath(n) || strings.Contains(n, "/") || n == "." {
			return fmt.Errorf("invalid volume name %q", n)
		}
	}
	root, err := os.OpenRoot(filepath.FromSlash(volumesDir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	src, dst := path.Join(from, "_data"), path.Join(to, "_data")
	if _, err := root.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		if _, derr := root.Lstat(dst); derr == nil {
			return nil // moved by an earlier attempt
		}
		return fmt.Errorf("volume %s has no data directory", from)
	} else if err != nil {
		return err
	}
	if replace {
		if err := root.RemoveAll(dst); err != nil {
			return err
		}
	} else if err := root.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the data directory of volume %s is not empty: %w", to, err)
	}
	return root.Rename(src, dst)
}

// moveDir renames a project directory (same parent). A repeated call finds
// it done.
func moveDir(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		if _, terr := os.Lstat(to); terr == nil {
			return nil
		}
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	return os.Rename(from, to)
}

// renameRecreate switches the project: outside containers onto the new
// volume names, the old project removed, the new one created (not
// started), anonymous volume data moved, old volumes removed.
func (s *Service) renameRecreate(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := renameInput(sc)
	if err != nil {
		return err
	}
	r := renameReport(sc)
	if r == nil || !r.Stopped {
		return errors.New("the stack was not stopped")
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	_, dst, err := s.projectDirs(in)
	if err != nil {
		return err
	}
	next := renamed(in.Stack, *in.Rename)
	all, err := c.Load(ctx, specOf(allProfiles(next), dst))
	if err != nil {
		return err
	}
	had := map[string]bool{}
	var before []protocol.ServiceState
	if err := update(ctx, sc, func(o *protocol.StackJobOutput) { before = o.Before }); err != nil {
		return err
	}
	for _, st := range before {
		if st.Containers > 0 {
			had[st.Service] = true
		}
	}
	var services []string
	for _, svc := range all.Services {
		if had[svc.Name] {
			services = append(services, svc.Name)
		}
	}
	if !r.Switched {
		// Built images keep running: tag them with the new project's names
		// while the old containers still show which image they run.
		if err := keepBuiltImages(ctx, eng, all, r.From, services); err != nil {
			return err
		}
		if err := updateRename(ctx, sc, func(_ *protocol.StackJobOutput, rep *protocol.StackRenameReport) { rep.Switched = true }); err != nil {
			return err
		}
		if err := sc.ReleaseCompensation(ctx, jobspec.CompUndoRename); err != nil {
			return err
		}
	}
	var warns []protocol.ComposeIssue
	volMap := map[string]string{}
	for _, v := range r.Volumes {
		if v.Key != "" && v.Done {
			volMap[v.Name] = v.NewName
		}
	}
	for i, oc := range r.Containers {
		if oc.NewID != "" {
			continue
		}
		sc.Progress(ctx, 55, "recreating "+oc.Name+" on the new volume names")
		id, err := s.recreateOutside(ctx, eng, oc, volMap)
		if err != nil {
			return renamedFailure(fmt.Errorf("recreating container %s failed: %w", oc.Name, err))
		}
		if err := updateRename(ctx, sc, func(_ *protocol.StackJobOutput, rep *protocol.StackRenameReport) {
			rep.Containers[i].NewID = id
		}); err != nil {
			return err
		}
		sc.Item(ctx, "container "+oc.Name, domain.ItemSucceeded, "recreated on the new volume names")
	}
	var timeout *time.Duration
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		timeout = &d
	}
	if list, err := lifecycle.ProjectContainers(ctx, eng, r.From); err != nil {
		return renamedFailure(err)
	} else if len(list) > 0 {
		sc.Progress(ctx, 65, "removing the containers of project "+r.From)
		if err := c.Down(ctx, r.From, nil, compose.DownOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc)},
			RemoveOrphans: true, Timeout: timeout}); err != nil {
			return renamedFailure(err)
		}
	}
	p, snap, err := s.loadSnapshot(ctx, c, protocol.StackJobInput{Stack: next}, dst)
	if err != nil {
		return renamedFailure(err)
	}
	if len(services) > 0 {
		sc.Progress(ctx, 75, "creating "+strings.Join(services, ", ")+" as project "+r.To)
		if err := c.Create(ctx, all, compose.CreateOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc)},
			Services: services, StopTimeout: timeout, ForceRecreate: true}); err != nil {
			return renamedFailure(err)
		}
	}
	if err := s.moveAnonymous(ctx, sc, eng, r); err != nil {
		return renamedFailure(err)
	}
	// The anonymous volumes a down left behind follow the project (#276).
	if err := s.opts.DownVolumes.Rename(r.From, r.To); err != nil {
		s.log.Warn("could not move the recorded anonymous volumes to the new project name", "from", r.From, "to", r.To, "error", err)
	}
	// The old volumes are empty (moved) or replaced (recreated) now.
	for _, v := range renameReport(sc).Volumes {
		if !v.Done || v.NewName == v.Name {
			continue
		}
		if err := eng.RemoveVolume(ctx, v.Name, false); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			warns = append(warns, protocol.ComposeIssue{Code: "volume_kept",
				Message: fmt.Sprintf("the old volume %s could not be removed (%v); its data moved to %s", v.Name, err, v.NewName)})
		}
	}
	bs := binds(p, dst)
	images := appliedImages(ctx, eng, p)
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		src := snap
		if inlineSize(src) > protocol.MaxInlineSources {
			for i := range src.Files {
				src.Files[i].Content = nil
			}
			src.ContentOmitted = true
		}
		o.Sources = &src
		o.Services = serviceInfos(p)
		o.Images = images
		o.Binds = bs
		o.Warnings = append(append(o.Warnings, warnings(p, bs)...), warns...)
	})
}

// recreateOutside replaces a container outside the stack by a clone that
// mounts the new volume names: the old one is renamed aside, the clone
// created under its name, then the old one removed. Idempotent.
func (s *Service) recreateOutside(ctx context.Context, eng engine.Engine, oc protocol.StackRenameContainer, volumes map[string]string) (string, error) {
	cl, ok := eng.(engine.Cloner)
	if !ok {
		return "", errors.New("this Engine cannot recreate containers")
	}
	if cur, err := eng.InspectContainer(ctx, oc.Name); err == nil && cur.ID != oc.ID {
		// The clone exists (an earlier attempt).
		if err := eng.RemoveContainer(ctx, oc.ID, engine.RemoveOptions{Force: true}); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			return "", err
		}
		return cur.ID, nil
	}
	aside := oc.Name + protocol.RenameAsideInfix + shortID(oc.ID)
	if err := eng.RenameContainer(ctx, oc.ID, aside); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
		return "", err
	}
	// The clone carries Docker Manager's labels under their current keys.
	id, err := cl.CloneContainer(ctx, oc.ID, engine.CloneOptions{Name: oc.Name, Volumes: volumes, RenameLabels: protocol.LegacyLabelRenames()})
	if err != nil {
		if rerr := eng.RenameContainer(context.WithoutCancel(ctx), oc.ID, oc.Name); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return "", err
	}
	if err := eng.RemoveContainer(ctx, oc.ID, engine.RemoveOptions{Force: true}); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
		return "", err
	}
	return id, nil
}

// moveAnonymous moves the data of the old containers' anonymous volumes
// into the anonymous volumes Compose gave the new containers (same
// service, replica number and mount target).
func (s *Service) moveAnonymous(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, r *protocol.StackRenameReport) error {
	pending := slices.ContainsFunc(r.Volumes, func(v protocol.StackRenameVolume) bool { return v.Key == "" && !v.Done })
	if !pending {
		return nil
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, r.To)
	if err != nil {
		return err
	}
	volumesDir := ""
	if res := s.opts.Deps.Storage(); res != nil {
		volumesDir = res.VolumesDir
	}
	for i, v := range r.Volumes {
		if v.Key != "" || v.Done {
			continue
		}
		target := ""
		for _, c := range list {
			if c.Labels[lifecycle.ComposeServiceLabel] != v.Service || c.Labels[labelContainerNumber] != v.Number {
				continue
			}
			for _, m := range c.Mounts {
				if m.Type == "volume" && m.Destination == v.Target {
					target = m.Name
				}
			}
		}
		if target == "" {
			sc.Item(ctx, "volume "+v.Name, domain.ItemSkipped, "kept: no new container of service "+v.Service+" mounts "+v.Target)
			continue
		}
		if target != v.Name {
			if err := moveVolumeData(volumesDir, v.Name, target, true); err != nil {
				return fmt.Errorf("moving the anonymous volume of service %s at %s: %w", v.Service, v.Target, err)
			}
		}
		if err := updateRename(ctx, sc, func(_ *protocol.StackJobOutput, rep *protocol.StackRenameReport) {
			rep.Volumes[i].NewName, rep.Volumes[i].Done = target, true
		}); err != nil {
			return err
		}
		r.Volumes[i].NewName, r.Volumes[i].Done = target, true
	}
	return nil
}

// renameStart starts what ran before: the stack's services under the new
// name (dependencies first) and the recreated outside containers.
func (s *Service) renameStart(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := renameInput(sc)
	if err != nil {
		return err
	}
	r := renameReport(sc)
	if r == nil || !r.Switched {
		return errors.New("the project did not switch")
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	var rerr error
	if len(r.Running) > 0 {
		list, err := lifecycle.ProjectContainers(ctx, eng, r.To)
		if err != nil {
			return err
		}
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		sc.Progress(ctx, 90, "starting "+strings.Join(r.Running, ", "))
		rt := lifecycle.EngineRuntime{Engine: eng, Project: r.To, Clock: s.opts.Clock}
		rep, err := lifecycle.Resume(ctx, g, rt, r.Running, s.renameLifecycle(ctx, sc, in))
		for _, svc := range rep.Started {
			sc.Item(ctx, svc, domain.ItemSucceeded, "started as project "+r.To)
		}
		rerr = err
	}
	for _, oc := range r.Containers {
		if !oc.Running || oc.NewID == "" {
			continue
		}
		if err := eng.StartContainer(ctx, oc.NewID); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
			rerr = errors.Join(rerr, fmt.Errorf("starting container %s: %w", oc.Name, err))
		}
	}
	after, aerr := serviceStates(ctx, eng, r.To)
	if uerr := update(ctx, sc, func(o *protocol.StackJobOutput) { o.After = after }); uerr != nil && rerr == nil {
		rerr = uerr
	}
	if rerr != nil {
		return renamedFailure(rerr)
	}
	if aerr != nil {
		return aerr
	}
	return sc.ReleaseCompensation(ctx, jobspec.CompStartContainers)
}

// renameCompStart starts what ran before the rename again (compensation):
// under the new name once its containers exist, else under the old one;
// outside containers by name (a recreated one keeps its name).
func (s *Service) renameCompStart(ctx context.Context, args json.RawMessage) error {
	var a renameResume
	if err := json.Unmarshal(args, &a); err != nil {
		return err
	}
	if !protocol.ValidProjectName(a.From) || !protocol.ValidProjectName(a.To) {
		return fmt.Errorf("invalid projects %q/%q", a.From, a.To)
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	project := a.To
	list, err := lifecycle.ProjectContainers(ctx, eng, a.To)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		project = a.From
		if list, err = lifecycle.ProjectContainers(ctx, eng, a.From); err != nil {
			return err
		}
	}
	var errs []error
	if len(list) > 0 && len(a.WasRunning) > 0 {
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		rt := lifecycle.EngineRuntime{Engine: eng, Project: project, Clock: s.opts.Clock}
		if _, err := lifecycle.Resume(ctx, g, rt, a.WasRunning, lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout}); err != nil {
			errs = append(errs, err)
		}
	}
	for _, name := range a.Outside {
		if err := eng.StartContainer(ctx, name); err != nil && !engine.IsCode(err, engine.CodeNotModified) {
			errs = append(errs, fmt.Errorf("start %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// renameCompUndo moves the volumes and the project directory back before
// the project switched (compensation). Each part is checked first, so a
// part that never moved is left alone.
func (s *Service) renameCompUndo(ctx context.Context, args json.RawMessage) error {
	var a renameUndo
	if err := json.Unmarshal(args, &a); err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	var errs []error
	if a.FromDir != "" && a.ToDir != "" && a.FromDir != a.ToDir {
		from, to := filepath.FromSlash(a.FromDir), filepath.FromSlash(a.ToDir)
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Lstat(to); err == nil {
				if err := os.Rename(to, from); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	for _, v := range a.Volumes {
		if v.Action == protocol.RenameVolumeMove {
			if err := moveVolumeData(a.VolumesDir, v.NewName, v.Name, false); err != nil && !isMissingData(err) {
				errs = append(errs, fmt.Errorf("move volume %s back: %w", v.Name, err))
				continue
			}
		}
		if err := eng.RemoveVolume(ctx, v.NewName, false); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			errs = append(errs, fmt.Errorf("remove volume %s: %w", v.NewName, err))
		}
	}
	return errors.Join(errs...)
}

// isMissingData reports a move whose source has no data directory (the
// volume never moved).
func isMissingData(err error) bool {
	return err != nil && strings.Contains(err.Error(), "has no data directory")
}

// checkDeclaredName refuses a deploy whose Compose files set a top-level
// name: other than the stack's project name: deploying would leave the
// running project behind and start one with empty volumes. The stack is
// renamed to it instead.
func checkDeclaredName(ctx context.Context, ref protocol.ProjectRef, dir string) error {
	declared, err := compose.DeclaredName(ctx, specOf(ref, dir))
	if err != nil || declared == "" || declared == ref.ProjectName {
		return err
	}
	return &stepError{class: classProjectRenamed, err: fmt.Errorf("the Compose file sets name: %s, but the stack runs as project %s", declared, ref.ProjectName),
		recovery: fmt.Sprintf("Nothing changed. Rename the stack to %s to move its containers and volumes to that name, or change name: back to %s.",
			declared, ref.ProjectName)}
}
