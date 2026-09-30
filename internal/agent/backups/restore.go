package backups

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
	"sort"
	"strconv"
	"strings"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/stacks"
	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/humanize"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
)

// restore.run (#10):
//
//	prepare          resolve every target (the stack's project directory,
//	                 volume data directories, one file) from the snapshot's
//	                 paths to their current places, check space and the
//	                 containers involved; create a missing volume or
//	                 project directory (a fresh host).
//	stop_containers  record the affected containers' state, register the
//	                 restart compensation, stop them (Compose projects in
//	                 reverse dependency order); a failed stop aborts before
//	                 any data is touched.
//	restore_data     restore into a staging directory next to each target
//	                 (same filesystem), then swap: the target's entries move
//	                 to a rollback directory, the staged entries into place;
//	                 any failure moves the original entries back.
//	start_containers restart only what was running, dependencies first.
//
// A stack restore never touches volumes; a volume restore never touches
// the stack's definition; a full restore does both in one job. A paths
// restore puts selected files and directories back in place (a directory
// is made identical to the snapshot). Nothing is redeployed here: a full
// restore's redeploy is a stack.deploy the manager queues afterwards.

// Bounds of preview walks.
const (
	restoreListLimit = 100000
	restoreWalkLimit = 200000
)

type restoreTarget struct {
	protocol.RestoreTarget
	// dir is the current directory (project, volume data) or, for a file,
	// the file itself.
	current string
	volume  *protocol.RestoreVolume
	// root is the directory the target lies in now (the project
	// directory or the volume's data directory; file and dir targets).
	root string
	// selected: a path of a paths restore (a file, directory or symlink;
	// replaced as a whole).
	selected bool
}

type restorePlan struct {
	in      protocol.RestoreRunInput
	targets []restoreTarget
	// Containers to stop: Compose projects (running services) and
	// standalone containers (IDs), recorded before stopping.
	projects   map[string][]string
	standalone []string
	affected   []protocol.AffectedContainer
	conflicts  []string
	warnings   []string
	blocked    []string
}

// resolve maps an input to its targets and the containers involved.
func (s *Service) resolveRestore(ctx context.Context, in protocol.RestoreRunInput) (*restorePlan, error) {
	if err := in.Validate(); err != nil {
		return nil, backup.Refuse(domain.ErrorRejected, "invalid restore: "+err.Error(), "Start the restore again from the backup.")
	}
	eng, err := s.engine()
	if err != nil {
		return nil, classed(err)
	}
	res := s.storage()
	if res == nil {
		return nil, backup.Refuse(protocol.CodeForbiddenPath, "the storage layout has not been verified yet", "Wait for the agent to verify its storage and retry.")
	}
	p := &restorePlan{in: in, projects: map[string][]string{}}
	paths := slices.Clone(in.SnapshotPaths)
	var projectDir, projectSource string
	if in.Project != nil {
		dir, err := stacks.ResolveProjectDir(res, *in.Project)
		if err != nil {
			return nil, classed(err)
		}
		projectDir = dir
		projectSource = in.ProjectSource
		if projectSource == "" {
			projectSource = deriveProjectSource(paths, in.Project.Dir)
		}
	}
	all, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, classed(err)
	}
	var prot map[string]string
	if s.opts.Guard != nil {
		set := s.opts.Guard.Identify(ctx, eng, all)
		prot = map[string]string{}
		for _, c := range all {
			if pr := set.Container(c.ID); pr != nil {
				prot[c.ID] = pr.Reason
			}
		}
		for _, v := range in.Volumes {
			if pr := set.Volume(v.Name, nil); pr != nil {
				p.blocked = append(p.blocked, "volume "+v.Name+" is Docker Manager's own: "+pr.Reason)
			}
		}
	}
	addProject := func() error {
		if projectSource == "" {
			return backup.Refuse("snapshot_path_unknown", "the snapshot has no project directory for this stack",
				"Choose a snapshot of this stack.")
		}
		_, err := os.Stat(projectDir)
		p.targets = append(p.targets, restoreTarget{RestoreTarget: protocol.RestoreTarget{Kind: "project", Name: in.Project.ProjectName,
			Path: filepath.ToSlash(projectDir), Source: projectSource, Exists: err == nil, Create: errors.Is(err, fs.ErrNotExist)}, current: projectDir})
		p.involveProject(all, in.Project.ProjectName, prot)
		return nil
	}
	addVolumes := func() error {
		for i := range in.Volumes {
			v := in.Volumes[i]
			src := v.Source
			if src == "" {
				src = deriveVolumeSource(paths, v.Name)
			}
			if src == "" {
				return backup.Refuse("snapshot_path_unknown", "the snapshot does not contain volume "+v.Name, "Choose a snapshot that contains it.")
			}
			t := restoreTarget{RestoreTarget: protocol.RestoreTarget{Kind: "volume", Name: v.Name, Source: src}, volume: &v}
			vol, err := eng.InspectVolume(ctx, v.Name)
			switch {
			case engine.IsCode(err, engine.CodeNotFound):
				t.Create = true
				t.current = filepath.Join(filepath.FromSlash(res.VolumesDir), v.Name, "_data")
			case err != nil:
				return classed(err)
			default:
				if acc := res.AccessFor(vol); !acc.Supported {
					p.blocked = append(p.blocked, "volume "+v.Name+": "+acc.Reason)
				}
				t.Exists, t.current = true, osPath(vol.Mountpoint)
			}
			t.Path = filepath.ToSlash(t.current)
			p.targets = append(p.targets, t)
			p.involveVolume(all, v.Name, prot)
		}
		return nil
	}
	addPath := func(file string, anyType bool) error {
		t, err := s.resolveFile(ctx, eng, in, file, anyType, paths, projectDir, projectSource)
		if err != nil {
			return err
		}
		t.selected = anyType
		p.targets = append(p.targets, t)
		if t.volume != nil {
			p.involveVolume(all, t.volume.Name, prot)
		} else if in.Project != nil {
			p.involveProject(all, in.Project.ProjectName, prot)
		}
		return nil
	}
	switch in.Scope {
	case protocol.RestoreScopeStack:
		err = addProject()
	case protocol.RestoreScopeVolume:
		err = addVolumes()
	case protocol.RestoreScopeFull:
		if err = addProject(); err == nil {
			err = addVolumes()
		}
	case protocol.RestoreScopeFile:
		err = addPath(in.File, false)
	case protocol.RestoreScopePaths:
		for _, sp := range in.Paths {
			if err = addPath(sp, true); err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if !in.Shutdown && (len(p.projects) > 0 || len(p.standalone) > 0) {
		p.blocked = append(p.blocked, "containers using the data are running: restore with shutdown to stop them first (they are started again afterwards)")
	}
	sort.Strings(p.blocked)
	return p, nil
}

func deriveProjectSource(paths []string, dir string) string {
	base := path.Base(dir)
	for _, p := range paths {
		if path.Base(p) == base && !strings.Contains(p, "/volumes/") {
			return p
		}
	}
	return ""
}

func deriveVolumeSource(paths []string, name string) string {
	for _, p := range paths {
		if strings.HasSuffix(p, "/"+name+"/_data") {
			return p
		}
	}
	return ""
}

// resolveFile finds the root a file belongs to (the stack's project
// directory or one of the snapshot's volumes) and its current place. With
// anyType (paths restores) it may also be a directory or a symlink: its
// kind (file or dir) comes from the snapshot listing (count).
func (s *Service) resolveFile(ctx context.Context, eng engine.Engine, in protocol.RestoreRunInput, file string, anyType bool,
	paths []string, projectDir, projectSource string) (restoreTarget, error) {
	t := restoreTarget{RestoreTarget: protocol.RestoreTarget{Kind: "file", Source: file}}
	switch {
	case projectSource != "" && snapWithin(file, projectSource) && (file != projectSource || anyType):
		t.current = filepath.Join(projectDir, filepath.FromSlash(snapRel(file, projectSource)))
		t.Name, t.root = in.Project.ProjectName, projectDir
	default:
		for _, p := range volumeSources(paths, in.Volumes) {
			if !snapWithin(file, p) || (file == p && !anyType) {
				continue
			}
			name := path.Base(path.Dir(p))
			// The manager names the volume holding the data now (a stack
			// renamed since the snapshot, #7).
			if i := slices.IndexFunc(in.Volumes, func(v protocol.RestoreVolume) bool { return v.Source == p }); i >= 0 {
				name = in.Volumes[i].Name
			}
			vol, err := eng.InspectVolume(ctx, name)
			if err != nil {
				return t, backup.Refuse("target_missing", "volume "+name+" does not exist on this host", "Restore the whole volume instead.")
			}
			t.current = filepath.Join(osPath(vol.Mountpoint), filepath.FromSlash(snapRel(file, p)))
			t.Name, t.volume, t.root = name, &protocol.RestoreVolume{Name: name, Source: p}, osPath(vol.Mountpoint)
			break
		}
	}
	if t.current == "" {
		return t, backup.Refuse("path_not_restorable", file+" lies outside the stack's project directory and its volumes",
			"Download the file instead and put it in place yourself.")
	}
	// The parent must stay inside its root: no symlinked directories. A
	// selected root (the project directory or a volume) is swapped in
	// place like a full restore of it.
	if t.current == t.root {
		_, err := os.Stat(t.current)
		t.Kind, t.Exists, t.Path = "dir", err == nil, filepath.ToSlash(t.current)
		return t, nil
	}
	if parent, exists, err := realPath(filepath.Dir(t.current)); err != nil || (exists && !inside(parent, rootReal(t.root))) {
		return t, backup.Refuse("path_not_restorable", file+": its directory leads outside its root through a symlink", "Restore the whole directory.")
	}
	fi, err := os.Lstat(t.current)
	switch {
	case err == nil && anyType && !fi.Mode().IsRegular() && !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0:
		return t, backup.Refuse("path_not_restorable", file+" exists and is not a file, directory or symlink", "Deselect it and restore again.")
	case err == nil && !anyType && !fi.Mode().IsRegular():
		return t, backup.Refuse("path_not_restorable", "the path exists and is not a regular file", "Restore the directory instead.")
	case err == nil:
		t.Exists = true
	}
	t.Path = filepath.ToSlash(t.current)
	return t, nil
}

// volumeSources are the volume data directories a snapshot holds: the
// input's volume sources, then the snapshot paths ending in /_data.
func volumeSources(paths []string, vols []protocol.RestoreVolume) []string {
	var out []string
	for _, v := range vols {
		if v.Source != "" {
			out = append(out, v.Source)
		}
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "/_data") && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func rootReal(p string) string {
	if r, _, err := realPath(p); err == nil {
		return r
	}
	return p
}

// involveProject adds a Compose project's running services.
func (p *restorePlan) involveProject(all []engine.Container, project string, prot map[string]string) {
	for _, c := range all {
		if c.Labels[lifecycle.ComposeProjectLabel] != project || c.Labels[lifecycle.ComposeOneoffLabel] == "True" {
			continue
		}
		p.involve(c, prot)
	}
}

// involveVolume adds the containers mounting a volume (their whole
// Compose project for project members).
func (p *restorePlan) involveVolume(all []engine.Container, name string, prot map[string]string) {
	for _, c := range all {
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name != name {
				continue
			}
			if proj := c.Labels[lifecycle.ComposeProjectLabel]; proj != "" {
				p.involveProject(all, proj, prot)
			} else {
				p.involve(c, prot)
			}
		}
	}
}

func (p *restorePlan) involve(c engine.Container, prot map[string]string) {
	name := strings.TrimPrefix(firstName(c), "/")
	if slices.ContainsFunc(p.affected, func(a protocol.AffectedContainer) bool { return a.Name == name }) {
		return
	}
	running := c.State == "running" || c.State == "restarting" || c.State == "paused"
	a := protocol.AffectedContainer{Name: name, Project: c.Labels[lifecycle.ComposeProjectLabel], Service: c.Labels[lifecycle.ComposeServiceLabel], Running: running}
	if reason, ok := prot[c.ID]; ok {
		a.Protected = reason
		if running {
			p.blocked = append(p.blocked, "container "+name+" is Docker Manager's own and cannot be stopped for the restore: "+reason)
		}
	}
	p.affected = append(p.affected, a)
	if !running || a.Protected != "" {
		return
	}
	if a.Project != "" {
		if !slices.Contains(p.projects[a.Project], a.Service) {
			p.projects[a.Project] = append(p.projects[a.Project], a.Service)
		}
		return
	}
	p.standalone = append(p.standalone, c.ID)
}

// count fills a target's counts from the snapshot and the current tree.
func (s *Service) count(ctx context.Context, repo restic.Repo, snapshotID string, t *restoreTarget) error {
	l, err := repo.Ls(ctx, snapshotID, t.Source, true, restoreListLimit)
	var re *restic.Error
	if t.selected && errors.As(err, &re) && re.Code == restic.CodeSnapshotNotFound {
		return backup.Refuse("snapshot_path_unknown", t.Source+" is not in the backup", "Choose paths from the backup's contents.")
	}
	if err != nil {
		return err
	}
	t.Complete = !l.Truncated
	inSnap := map[string]bool{}
	owners := map[string]bool{}
	found := false
	for _, n := range l.Nodes {
		if !snapWithin(n.Path, t.Source) {
			continue
		}
		found = true
		rel := snapRel(n.Path, t.Source)
		inSnap[rel] = true
		// A selected path (paths restore) that is a directory in the
		// snapshot replaces the directory as a whole.
		if t.selected && t.Kind == "file" && (rel != "." || n.Type == "dir") {
			t.Kind = "dir"
		}
		if n.Type == "file" {
			t.Files++
			t.Bytes += n.Size
		}
		if len(owners) < 16 {
			owners[strconv.Itoa(n.UID)+":"+strconv.Itoa(n.GID)] = true
		}
	}
	for o := range owners {
		t.Owners = append(t.Owners, o)
	}
	sort.Strings(t.Owners)
	if t.selected && !found {
		return backup.Refuse("snapshot_path_unknown", t.Source+" is not in the backup", "Choose paths from the backup's contents.")
	}
	if t.Kind == "dir" && t.Exists {
		if fi, err := os.Lstat(t.current); err == nil && !fi.IsDir() {
			// A file (or link) now where the snapshot has a directory:
			// it is replaced whole.
			t.Overwritten = 1
			t.Added = t.Files
			t.FreeBytes = freeBytes(filepath.Dir(t.current))
			return nil
		}
	}
	if t.Kind == "file" {
		if t.Exists {
			t.Overwritten = 1
		} else {
			t.Added = 1
		}
	} else if t.Exists {
		seen := map[string]bool{}
		walked := 0
		_ = filepath.WalkDir(t.current, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == t.current {
				return nil
			}
			walked++
			if walked > restoreWalkLimit {
				t.Complete = false
				return filepath.SkipAll
			}
			rel, _ := filepath.Rel(t.current, p)
			rel = filepath.ToSlash(rel)
			seen[rel] = true
			if !d.Type().IsRegular() {
				return nil
			}
			if inSnap[rel] {
				t.Overwritten++
			} else {
				t.Removed++
			}
			return nil
		})
		for rel := range inSnap {
			if rel != "." && !seen[rel] {
				t.Added++
			}
		}
	} else {
		t.Added = t.Files
	}
	dir := t.current
	for dir != "" {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.FreeBytes = freeBytes(dir)
	return nil
}

func (s *Service) restorePreview(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.RestorePreviewInput](input)
	if err != nil {
		return nil, err
	}
	p, err := s.resolveRestore(ctx, in.Input)
	if err != nil {
		return nil, restoreHandlerError(err)
	}
	o, err := s.open(ctx, in.Input.Repository, in.Credential)
	if err != nil {
		return nil, err
	}
	out := protocol.RestorePreviewOutput{Targets: []protocol.RestoreTarget{}, Affected: p.affected, Conflicts: p.conflicts,
		Warnings: p.warnings, Blocked: p.blocked}
	if out.Affected == nil {
		out.Affected = []protocol.AffectedContainer{}
	}
	for i := range p.targets {
		t := &p.targets[i]
		if err := s.count(ctx, o.Repo, in.Input.SnapshotID, t); err != nil {
			return nil, handlerError(err)
		}
		if t.FreeBytes >= 0 && t.Bytes > t.FreeBytes {
			sz := humanize.Sizes(t.Bytes, t.FreeBytes)
			out.Blocked = append(out.Blocked, fmt.Sprintf("%s needs %s but only %s are free", t.Path, sz[0], sz[1]))
		}
		out.Targets = append(out.Targets, t.RestoreTarget)
	}
	switch {
	case in.Input.Scope == protocol.RestoreScopeStack:
		out.Warnings = append(out.Warnings, "The stack's volumes are not restored (restore them with scope volume); "+
			"deploy the stack afterwards to apply the restored definition.")
	case in.Input.Redeploy:
		out.Warnings = append(out.Warnings, "Afterwards the stack is deployed from the restored definition "+
			"(the services that were running before).")
	}
	return out, nil
}

func restoreHandlerError(err error) error {
	var r *backup.Refusal
	if errors.As(err, &r) {
		return &session.HandlerError{Code: r.Class, Message: r.Message}
	}
	return handlerError(err)
}

// --- the executor ---

func (s *Service) restoreInput(sc *jobexec.StepContext) (protocol.RestoreRunInput, error) {
	var in protocol.RestoreRunInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return in, backup.Refuse(domain.ErrorRejected, "invalid restore input", "Start the restore again from the backup.")
	}
	return in, nil
}

func (s *Service) restoreOutput(sc *jobexec.StepContext) protocol.RestoreRunOutput {
	var out protocol.RestoreRunOutput
	_ = json.Unmarshal(sc.Output(), &out)
	return out
}

func (s *Service) stepRestorePrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.restoreInput(sc)
	if err != nil {
		return err
	}
	p, err := s.resolveRestore(ctx, in)
	if err != nil {
		return err
	}
	if len(p.blocked) > 0 {
		return backup.Refuse("restore_blocked", strings.Join(p.blocked, "; "), "Resolve the blockers shown in the restore preview and restore again.")
	}
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	out := protocol.RestoreRunOutput{Scope: in.Scope, KeyGeneration: in.Repository.KeyGeneration}
	eng, err := s.engine()
	if err != nil {
		return classed(err)
	}
	for i := range p.targets {
		t := &p.targets[i]
		if err := s.count(ctx, o.Repo, in.SnapshotID, t); err != nil {
			return err
		}
		if t.FreeBytes >= 0 && t.Bytes > t.FreeBytes {
			sz := humanize.Sizes(t.Bytes, t.FreeBytes)
			return backup.Refuse("insufficient_space", fmt.Sprintf("%s needs %s, %s are free", t.Path, sz[0], sz[1]),
				"Free space on the target's filesystem and restore again.")
		}
		if t.Create {
			if err := s.createTarget(ctx, eng, t); err != nil {
				return err
			}
		}
		out.Targets = append(out.Targets, t.RestoreTarget)
	}
	return sc.SetOutput(ctx, out)
}

// createTarget creates a missing volume (with Compose's labels for a
// stack volume) or project directory.
func (s *Service) createTarget(ctx context.Context, eng engine.Engine, t *restoreTarget) error {
	switch t.Kind {
	case "volume":
		labels := map[string]string{}
		if t.volume.ComposeProject != "" {
			labels[lifecycle.ComposeProjectLabel] = t.volume.ComposeProject
			labels["com.docker.compose.volume"] = t.volume.ComposeKey
		}
		v, err := eng.CreateVolume(ctx, engine.VolumeSpec{Name: t.Name, Labels: labels})
		if err != nil {
			return classed(err)
		}
		t.current, t.Path = osPath(v.Mountpoint), v.Mountpoint
		return os.MkdirAll(t.current, 0o755) //nolint:gosec // a volume's data directory is world-readable like Docker creates it
	case "project":
		return os.MkdirAll(t.current, 0o750)
	}
	return nil
}

func (s *Service) stepRestoreStop(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.restoreInput(sc)
	if err != nil {
		return err
	}
	p, err := s.resolveRestore(ctx, in)
	if err != nil {
		return err
	}
	if len(p.projects) == 0 && len(p.standalone) == 0 {
		return nil
	}
	eng, err := s.engine()
	if err != nil {
		return classed(err)
	}
	out := s.restoreOutput(sc)
	rep := &protocol.ShutdownReport{}
	var rec shutdownRecord
	type stop struct {
		graph   *lifecycle.Graph
		project string
		running []string
	}
	var stops []stop
	projects := make([]string, 0, len(p.projects))
	for proj := range p.projects {
		projects = append(projects, proj)
	}
	sort.Strings(projects)
	for _, proj := range projects {
		containers, err := lifecycle.ProjectContainers(ctx, eng, proj)
		if err != nil {
			return classed(err)
		}
		g, err := lifecycle.GraphFromContainers(containers)
		if err != nil {
			return err
		}
		running := p.projects[proj]
		for _, svc := range g.Services() {
			rep.PreState = append(rep.PreState, protocol.ShutdownServiceState{Project: proj, Service: svc, Running: slices.Contains(running, svc)})
		}
		rec.Projects = append(rec.Projects, projectState{Project: proj, WasRunning: running})
		stops = append(stops, stop{graph: g, project: proj, running: running})
	}
	rec.Containers = p.standalone
	for _, id := range p.standalone {
		rep.PreState = append(rep.PreState, protocol.ShutdownServiceState{Service: id, Running: true})
	}
	out.Shutdown = rep
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, rec); err != nil {
		return err
	}
	for _, st := range stops {
		r, err := lifecycle.Stop(ctx, st.graph, s.projectRuntime(eng, st.project), st.running, s.lifecycleOpts(ctx, sc))
		for _, svc := range r.Stopped {
			rep.Stopped = append(rep.Stopped, st.project+"/"+svc)
		}
		if err != nil {
			_ = sc.SetOutput(ctx, out)
			return backup.Refuse("shutdown_failed", fmt.Sprintf("stopping %s failed: %v", st.project, err),
				"Nothing was restored. The containers that were running are started again; check them and restore again.")
		}
	}
	for _, id := range p.standalone {
		if err := eng.StopContainer(ctx, id, nil); err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			_ = sc.SetOutput(ctx, out)
			return backup.Refuse("shutdown_failed", "stopping a container failed: "+err.Error(),
				"Nothing was restored. The containers that were running are started again; check them and restore again.")
		}
		rep.Stopped = append(rep.Stopped, id)
	}
	return sc.SetOutput(ctx, out)
}

func (s *Service) stepRestoreData(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := s.restoreInput(sc)
	if err != nil {
		return err
	}
	p, err := s.resolveRestore(ctx, in)
	if err != nil {
		return err
	}
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	out := s.restoreOutput(sc)
	tag := restoreTag(sc.JobID)
	type done struct {
		t        restoreTarget
		rollback string
	}
	var swapped []done
	undo := func() {
		for i := len(swapped) - 1; i >= 0; i-- {
			d := swapped[i]
			if d.t.Kind == "file" || (d.t.selected && d.t.current != d.t.root) {
				if !d.t.Exists {
					_ = os.RemoveAll(d.t.current) // added by this restore
				}
				_ = rollbackFile(d.t.current, d.rollback)
				continue
			}
			_ = rollbackDir(d.t.current, d.rollback)
		}
	}
	for i := range p.targets {
		t := p.targets[i]
		if t.Kind == "volume" && t.Create {
			// Created in prepare: its current place is the mountpoint now.
			if v, err := s.opts.Engine().InspectVolume(ctx, t.Name); err == nil {
				t.current = osPath(v.Mountpoint)
			}
		}
		// Files and selected paths are replaced whole (a selected directory
		// becomes identical to the snapshot); project and volume roots keep
		// their directory and swap its entries.
		whole := t.Kind == "file" || (t.selected && t.current != t.root)
		parent := filepath.Dir(t.current)
		staging := filepath.Join(parent, ".docker-manager-restore-"+tag)
		rollback := filepath.Join(parent, ".docker-manager-rollback-"+tag)
		if whole {
			rollback = filepath.Join(parent, ".docker-manager-rollback-"+tag+"-"+filepath.Base(t.current))
		}
		_ = os.RemoveAll(staging)
		sc.Progress(ctx, 30+50*i/len(p.targets), "restoring "+t.Path)
		sum, err := o.Repo.Restore(ctx, restic.RestoreRequest{SnapshotID: in.SnapshotID, Target: staging, Include: []string{t.Source},
			Overwrite: "always"})
		if err != nil {
			_ = os.RemoveAll(staging)
			undo()
			return err
		}
		staged := stagedPath(staging, t.Source)
		if whole {
			err = swapFile(t.current, staged, rollback)
		} else {
			err = swapDir(t.current, staged, rollback)
		}
		_ = os.RemoveAll(staging)
		if err != nil {
			undo()
			return backup.Refuse("restore_failed", "putting the restored data in place failed: "+err.Error(),
				"The original data was moved back. Check the target's filesystem and restore again.")
		}
		swapped = append(swapped, done{t: t, rollback: rollback})
		if len(out.Targets) == len(p.targets) {
			out.Targets[i].FilesChanged = sum.FilesRestored
			out.Targets[i].Path = filepath.ToSlash(t.current)
		}
	}
	// Everything is in place: drop the rollback copies.
	for _, d := range swapped {
		_ = os.RemoveAll(d.rollback)
	}
	out.RedeploySuggested = in.Scope == protocol.RestoreScopeStack || (in.Scope == protocol.RestoreScopeFull && !in.Redeploy)
	return sc.SetOutput(ctx, out)
}

func restoreTag(jobID string) string {
	id := strings.ReplaceAll(jobID, "-", "")
	if len(id) > 12 {
		id = id[len(id)-12:]
	}
	return id
}

// swapDir replaces target's entries with staged's: target's entries move
// to rollback first; on any failure the original entries come back.
func swapDir(target, staged, rollback string) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(rollback, 0o700); err != nil {
		return err
	}
	old, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, e := range old {
		if err := os.Rename(filepath.Join(target, e.Name()), filepath.Join(rollback, e.Name())); err != nil {
			_ = rollbackDir(target, rollback)
			return err
		}
	}
	entries, err := os.ReadDir(staged)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = rollbackDir(target, rollback)
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(staged, e.Name()), filepath.Join(target, e.Name())); err != nil {
			_ = rollbackDir(target, rollback)
			return err
		}
	}
	if fi, err := os.Stat(staged); err == nil {
		_ = os.Chmod(target, fi.Mode().Perm())
		copyOwner(fi, target)
	}
	return nil
}

// rollbackDir removes what was put into target and moves the original
// entries back from rollback.
func rollbackDir(target, rollback string) error {
	back, err := os.ReadDir(rollback)
	if err != nil {
		return err
	}
	cur, _ := os.ReadDir(target)
	for _, e := range cur {
		_ = os.RemoveAll(filepath.Join(target, e.Name()))
	}
	var errs []error
	for _, e := range back {
		errs = append(errs, os.Rename(filepath.Join(rollback, e.Name()), filepath.Join(target, e.Name())))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return os.Remove(rollback)
}

// swapFile replaces one file, keeping the original as rollback.
func swapFile(target, staged, rollback string) error {
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, rollback); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		_ = rollbackFile(target, rollback)
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		_ = rollbackFile(target, rollback)
		return err
	}
	return nil
}

func rollbackFile(target, rollback string) error {
	if _, err := os.Lstat(rollback); err != nil {
		return nil
	}
	_ = os.RemoveAll(target)
	return os.Rename(rollback, target)
}

func (s *Service) stepRestoreStart(ctx context.Context, sc *jobexec.StepContext) error {
	out := s.restoreOutput(sc)
	if out.Shutdown == nil {
		return nil
	}
	rec := recordOf(out.Shutdown)
	for _, st := range out.Shutdown.PreState {
		if st.Project == "" && st.Running {
			rec.Containers = append(rec.Containers, st.Service)
		}
	}
	restarted, err := s.resume(ctx, sc, rec)
	out.Shutdown.Restarted = append(out.Shutdown.Restarted, restarted...)
	if err != nil {
		out.Shutdown.RestartError = err.Error()
		_ = sc.SetOutput(ctx, out)
		return backup.Refuse("restart_failed", "restarting the stopped containers failed: "+err.Error(),
			"The data was restored. Start the affected containers manually (only those that were running before).")
	}
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	return sc.ReleaseCompensation(ctx, jobspec.CompStartContainers)
}
