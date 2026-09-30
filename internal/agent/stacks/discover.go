package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Folder discovery (#7): Compose projects that have no containers (never
// started, or after `docker compose down`) are found through their Compose
// files, in two places:
//
//   - each direct subfolder of a verified stack root (the stacks volume or
//     a registered bind root): adoptable in place, like a project whose
//     containers' labels point there;
//   - an import mount (at or below /import) and up to importScanDepth
//     folder levels below it: importable by copy with the same checks as a
//     project with containers.
//
// The project name is the one Compose resolves (the top-level name: or
// the folder's name, normalized); a folder whose files do not load is
// listed with the reason and cannot be imported. A folder whose project
// name or directory a project with containers has already is skipped (the
// project with containers has everything). Hidden folders are skipped,
// symlinks never followed and a folder holding a Compose file is never
// descended into. The walk is bounded (maxFolderDirs folders read,
// maxFolderProjects projects).

// Folder discovery bounds.
const (
	importScanDepth   = 2
	maxFolderDirs     = 2000
	maxFolderProjects = 200
)

// folderCandidate is a folder holding a Compose file.
type folderCandidate struct {
	// local is where the agent reads it; host its host path (slash form).
	local, host string
	// inRoot: a direct subfolder dir of the stack root (kind, rootPath).
	inRoot   bool
	kind     string
	rootPath string
	dir      string
}

// folderScan is what discoverFolders found.
type folderScan struct {
	projects []protocol.DiscoveredProject
	// named are the volume names each loaded project's Compose file
	// resolves to (named volumes, `name:` and external ones), by project.
	named map[string][]string
	// truncated: a bound stopped the walk.
	truncated bool
}

// folderWalker reads folders within the bounds.
type folderWalker struct {
	dirs      int
	truncated bool
	visited   map[string]bool
	// mounts are the import mounts' local paths: each is walked on its own
	// (its host path differs from the folder of the mount above it).
	mounts map[string]bool
}

// subdirs lists the subfolders of dir by name: no hidden ones, no
// symlinks (a DirEntry of a symlink is not a directory).
func (w *folderWalker) subdirs(dir string) []string {
	if w.dirs >= maxFolderDirs {
		w.truncated = true
		return nil
	}
	w.dirs++
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// walkImport walks an import mount breadth first down to importScanDepth
// levels and hands every folder holding a Compose file to add (never
// descending into it); add returns false to stop.
func (w *folderWalker) walkImport(m storage.ImportMount, add func(local, host string) bool) {
	type item struct {
		local, host string
		depth       int
	}
	queue := []item{{local: filepath.FromSlash(path.Clean(m.Path)), host: path.Clean(filepath.ToSlash(m.HostPath))}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if w.visited[it.local] {
			continue
		}
		w.visited[it.local] = true
		if fi, err := os.Lstat(it.local); err != nil || !fi.IsDir() {
			continue
		}
		if hasComposeFile(it.local) {
			if !add(it.local, it.host) {
				return
			}
			continue
		}
		if it.depth >= importScanDepth {
			continue
		}
		for _, name := range w.subdirs(it.local) {
			local := filepath.Join(it.local, name)
			if w.mounts[filepath.ToSlash(local)] {
				continue
			}
			queue = append(queue, item{local: local, host: path.Join(it.host, name), depth: it.depth + 1})
		}
	}
}

// hasComposeFile reports whether dir holds one of Compose's default files.
func hasComposeFile(dir string) bool {
	for _, n := range compose.DefaultConfigFiles {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// discoverFolders finds the containerless projects (see above). known are
// the projects found through containers.
func discoverFolders(ctx context.Context, res *storage.Result, known []protocol.DiscoveredProject) folderScan {
	scan := folderScan{named: map[string][]string{}}
	if res == nil {
		return scan
	}
	w := &folderWalker{visited: map[string]bool{}, mounts: map[string]bool{}}
	for _, m := range res.Imports {
		w.mounts[path.Clean(filepath.ToSlash(m.Path))] = true
	}
	var cands []folderCandidate
	full := func() bool {
		if len(cands) >= maxFolderProjects {
			w.truncated = true
			return true
		}
		return false
	}
	for _, r := range res.Roots {
		if !r.OK || (r.Kind != storage.KindStacks && r.Kind != storage.KindBind) {
			continue
		}
		root := path.Clean(filepath.ToSlash(r.Path))
		for _, name := range w.subdirs(filepath.FromSlash(root)) {
			local := filepath.Join(filepath.FromSlash(root), name)
			w.visited[local] = true
			if !hasComposeFile(local) {
				continue
			}
			if full() {
				break
			}
			cands = append(cands, folderCandidate{local: local, host: root + "/" + name, inRoot: true, kind: r.Kind, rootPath: root, dir: name})
		}
	}
	for _, m := range res.Imports {
		if full() {
			break
		}
		w.walkImport(m, func(local, host string) bool {
			if inStackRoot(res, host) {
				return true // listed by the stack roots (in place), or the stacks volume itself
			}
			cands = append(cands, folderCandidate{local: local, host: host})
			return !full()
		})
	}
	scan.truncated = w.truncated

	knownName, knownDir := map[string]bool{}, map[string]bool{}
	for _, k := range known {
		knownName[k.Name] = true
		for _, d := range []string{k.WorkingDir, k.SourceDir} {
			if d != "" {
				knownDir[d] = true
			}
		}
	}
	at := map[string]int{}
	for _, c := range cands {
		if knownDir[c.host] {
			continue
		}
		f, ok := loadFolder(ctx, c)
		if !ok || knownName[f.p.Name] {
			continue
		}
		if i, dup := at[f.p.Name]; dup {
			first := &scan.projects[i]
			first.Adoptable, first.Copyable, first.SourceDir, first.Root, first.RootPath, first.Dir = false, false, "", "", "", ""
			first.Reason = fmt.Sprintf("several folders hold a Compose project named %s (%s and %s): give all but one of them "+
				"another project name (a top-level name: in the Compose file), then refresh", f.p.Name, first.WorkingDir, c.host)
			continue
		}
		if f.loaded {
			if c.inRoot {
				inPlace(&f.p, c)
			} else {
				copyableFolder(&f.p, res, c, f.defs)
			}
		}
		at[f.p.Name] = len(scan.projects)
		scan.projects = append(scan.projects, f.p)
		scan.named[f.p.Name] = f.volumes
	}
	return scan
}

// inStackRoot reports whether a host directory lies in a verified stack
// root (the stacks volume or a registered bind root).
func inStackRoot(res *storage.Result, host string) bool {
	for _, r := range res.Roots {
		if r.OK && (r.Kind == storage.KindStacks || r.Kind == storage.KindBind) && within(r.Path, host) {
			return true
		}
	}
	return false
}

// loadedFolder is a folder's project as Compose loads it.
type loadedFolder struct {
	p      protocol.DiscoveredProject
	loaded bool
	// defs are its definition files (absolute, where the agent reads them).
	defs    []string
	volumes []string
}

// loadFolder loads the project of a folder with every profile. ok is
// false when no project name can be derived (the folder is not listed).
func loadFolder(ctx context.Context, c folderCandidate) (loadedFolder, bool) {
	f := loadedFolder{p: protocol.DiscoveredProject{WorkingDir: c.host, Containerless: true, Services: []protocol.DiscoveredService{}}}
	spec := compose.ProjectSpec{Dir: c.local, Profiles: []string{"*"}}
	name, derr := compose.DeclaredName(ctx, spec)
	if name == "" {
		name = compose.NormalizeProjectName(path.Base(c.host))
	}
	if name == "" {
		return f, false
	}
	f.p.Name = name
	if derr != nil {
		f.p.Reason = unloadable(derr)
		return f, true
	}
	spec.Name = name
	p, err := compose.LoadProject(ctx, spec)
	if err != nil {
		f.p.Reason = unloadable(err)
		return f, true
	}
	f.loaded, f.defs = true, p.DefinitionFiles
	for _, svc := range p.Services {
		f.p.Services = append(f.p.Services, protocol.DiscoveredService{Name: svc.Name, Image: svc.Image})
	}
	for _, v := range p.Volumes {
		if v.Name != "" && !slices.Contains(f.volumes, v.Name) {
			f.volumes = append(f.volumes, v.Name)
		}
	}
	return f, true
}

func unloadable(err error) string {
	msg := err.Error()
	var ee *engine.Error
	if errors.As(err, &ee) {
		msg = ee.Message
	}
	return fmt.Sprintf("its Compose files do not load (%s): fix them, then refresh", msg)
}

// inPlace makes a folder of a stack root adoptable in place.
func inPlace(p *protocol.DiscoveredProject, c folderCandidate) {
	ref := protocol.ProjectRef{Root: protocol.RootStacks, Dir: c.dir, ProjectName: p.Name}
	if c.kind == storage.KindBind {
		ref.Root, ref.RootPath = protocol.RootBind, c.rootPath
	}
	if err := ref.Validate(); err != nil {
		p.Reason = fmt.Sprintf("the folder %s cannot become a stack (%v); rename it or set a valid top-level name: in its Compose file", c.host, err)
		return
	}
	p.Root, p.RootPath, p.Dir = ref.Root, ref.RootPath, ref.Dir
	p.Adoptable = true
}

// copyableFolder decides whether a folder below an import mount can be
// imported by copy (the checks of importable, from the folder itself).
func copyableFolder(p *protocol.DiscoveredProject, res *storage.Result, c folderCandidate, defs []string) {
	const outside = "the folder is outside the stacks volume and the registered stack roots"
	for _, f := range defs {
		if !within(filepath.ToSlash(c.local), filepath.ToSlash(f)) {
			p.Reason = fmt.Sprintf("%s and its definition file %s is outside it; import it with an explicit Compose source", outside, path.Base(filepath.ToSlash(f)))
			return
		}
	}
	switch {
	case !res.StacksOK():
		p.Reason = outside + " and the stacks volume is not verified (see the agent's storage diagnostics)"
		return
	case !protocol.ValidProjectName(p.Name):
		p.Reason = outside + " and its project name cannot name a directory of the stacks volume; set a valid top-level name: in its Compose file"
		return
	case within(c.host, res.StacksDir) || within(res.StacksDir, c.host):
		p.Reason = outside + " and overlaps the stacks volume"
		return
	}
	if _, ok := visibleDir(res, c.host); !ok {
		p.Reason = fmt.Sprintf("%s and the agent cannot read %s through its import mounts", outside, c.host)
		return
	}
	if _, err := os.Lstat(filepath.Join(filepath.FromSlash(res.StacksDir), p.Name)); err == nil {
		p.Reason = fmt.Sprintf("%s and the stacks volume already has a directory %s: move that away to import the project by copy", outside, p.Name)
		return
	}
	p.Copyable, p.SourceDir = true, c.host
	p.Reason = outside + ": import it by copy (its whole folder moves into the stacks volume)"
}

// projectVolumes sets every project's Volumes: the existing volumes labeled
// with its project name, those its containers mount and, for a loaded
// containerless project, those its Compose file names (named). Anonymous
// volumes and Docker Manager's own (own) are left out.
func projectVolumes(projects []protocol.DiscoveredProject, containers []engine.Container, vols []engine.Volume,
	named map[string][]string, own *protect.Set) {
	byName := map[string]engine.Volume{}
	labeled := map[string][]string{}
	for _, v := range vols {
		byName[v.Name] = v
		if p := v.Labels[lifecycle.ComposeProjectLabel]; p != "" {
			labeled[p] = append(labeled[p], v.Name)
		}
	}
	mounted := map[string][]string{}
	for _, c := range containers {
		p := c.Labels[lifecycle.ComposeProjectLabel]
		if p == "" {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				mounted[p] = append(mounted[p], m.Name)
			}
		}
	}
	for i := range projects {
		p := &projects[i]
		names := append(slices.Clone(labeled[p.Name]), mounted[p.Name]...)
		for _, n := range named[p.Name] {
			if _, ok := byName[n]; ok {
				names = append(names, n)
			}
		}
		var out []string
		for _, n := range names {
			if protocol.AnonymousVolumeName(n) || own.Volume(n, byName[n].Labels) != nil {
				continue
			}
			out = append(out, n)
		}
		slices.Sort(out)
		out = slices.Compact(out)
		if len(out) > protocol.MaxDiscoveredVolumes {
			out = out[:protocol.MaxDiscoveredVolumes]
		}
		p.Volumes = out
	}
}
