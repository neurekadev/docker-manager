package backups

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// itemPlan is the resolved scope of one item.
type itemPlan struct {
	item protocol.BackupItem
	// project is the stack's Compose project name.
	project string
	dir     string
	sources []protocol.ScopeSource
	// paths are the included sources (OS paths, none inside another);
	// excludes the restic exclude patterns.
	paths    []string
	excludes []string
	volumes  []string
	// volumePaths maps each included volume to its data directory.
	volumePaths map[string]string
	// affected are the containers a shutdown would stop (and their order)
	// and conflicts what a shutdown cannot cover.
	affected  []protocol.AffectedContainer
	conflicts []string
	warnings  []string
	err       error

	// all is every container of the Engine and prot Docker Manager's own
	// objects among them (#32), read once per plan.
	all  []engine.Container
	prot *protect.Set
}

func (p itemPlan) preview() protocol.ScopePreviewItem {
	out := protocol.ScopePreviewItem{Item: p.item.Key(), Kind: p.item.Kind, StackID: p.item.StackID, Volume: p.item.Volume,
		Sources: p.sources, Excludes: p.excludes, Paths: p.paths, Volumes: p.volumes, Affected: p.affected,
		Conflicts: p.conflicts, Warnings: p.warnings}
	if out.Sources == nil {
		out.Sources = []protocol.ScopeSource{}
	}
	if p.err != nil {
		out.Error, out.ErrorClass = p.err.Error(), errorClass(p.err)
	}
	return out
}

func errorClass(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	var r *backup.Refusal
	if errors.As(err, &r) {
		return r.Class
	}
	return "invalid_scope"
}

func refuse(class, format string, args ...any) error {
	return &backup.Refusal{Class: class, Message: fmt.Sprintf(format, args...), Guidance: "Adjust the policy's selection and run the backup again."}
}

// forbiddenExternal are host paths never backed up as external binds.
var forbiddenExternal = []string{"/", "/proc", "/sys", "/dev", "/run", "/var/run", "/boot"}

// plan resolves an item's scope. repo is the destination (a local
// repository on this agent must not lie inside a source).
func (s *Service) plan(ctx context.Context, it protocol.BackupItem, repo *protocol.BackupRepositoryRef, shutdown bool) itemPlan {
	p := itemPlan{item: it}
	if err := it.Validate(); err != nil {
		p.err = refuse("invalid_scope", "%s", err.Error())
		return p
	}
	eng, err := s.engine()
	if err != nil {
		p.err = err
		return p
	}
	if p.all, err = eng.ListContainers(ctx, engine.ContainerFilter{All: true}); err != nil {
		p.err = &session.HandlerError{Code: protocol.CodeEngineError, Message: "the containers could not be listed"}
		return p
	}
	if s.opts.Guard != nil {
		p.prot = s.opts.Guard.Identify(ctx, eng, p.all)
	}
	switch it.Kind {
	case backup.MemberStack:
		s.planStack(ctx, eng, &p, shutdown)
	case backup.MemberVolume:
		s.planVolume(ctx, eng, &p)
	}
	if p.err == nil && repo != nil && repo.Destination.Kind == backup.KindLocal {
		rp := osPath(repo.Destination.Path)
		for _, src := range p.paths {
			if inside(rp, src) || inside(src, rp) {
				p.err = &backup.Refusal{Class: protocol.CodeRepositoryInsideSource,
					Message:  fmt.Sprintf("the backup repository %s lies inside (or contains) the source %s", repo.Destination.Path, src),
					Guidance: "Move the local repository outside every backed-up directory, or exclude that directory."}
				break
			}
		}
	}
	if p.err == nil && len(p.paths) == 0 {
		p.err = refuse("empty_scope", "nothing to back up for %s", it.Key())
	}
	return p
}

// allowlisted reports whether an external path is in the allowlist.
func (s *Service) allowlisted(p string) bool {
	for _, a := range s.opts.ExternalAllowlist {
		if inside(p, osPath(a)) {
			return true
		}
	}
	return false
}

func (s *Service) planStack(ctx context.Context, eng engine.Engine, p *itemPlan, shutdown bool) {
	it := p.item
	dir, err := stacks.ResolveProjectDir(s.storage(), *it.Project)
	if err != nil {
		p.err = err
		return
	}
	var loader Loader
	if s.opts.Loader != nil {
		loader = s.opts.Loader()
	}
	if loader == nil {
		p.err = errEngineUnavailable
		return
	}
	ref := *it.Project
	proj, err := loader.Load(ctx, compose.ProjectSpec{Name: ref.ProjectName, Dir: dir, ConfigFiles: ref.ConfigFiles,
		EnvFiles: ref.EnvFiles, Profiles: ref.Profiles})
	if err != nil {
		p.err = &session.HandlerError{Code: protocol.CodeInvalidArgument, Message: "the Compose project could not be loaded: " + err.Error()}
		return
	}
	p.project, p.dir = proj.Name, dir
	realDir, _, err := realPath(dir)
	if err != nil {
		p.err = err
		return
	}
	p.sources = append(p.sources, protocol.ScopeSource{Kind: protocol.SourceProject, Path: filepath.ToSlash(dir), State: protocol.SourceIncluded,
		Reason: "Compose files, .env and the workspace, including relative bind sources inside the project directory"})
	p.paths = append(p.paths, dir)
	// Path exclusions are relative to the project directory.
	for _, rel := range it.Rules.PathExcludes {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if !inside(abs, dir) || abs == dir {
			p.err = refuse("invalid_scope", "path exclude %q leaves the project directory", rel)
			return
		}
		p.excludes = append(p.excludes, abs)
	}
	excluded := func(path string) bool {
		for _, e := range p.excludes {
			if inside(path, e) {
				return true
			}
		}
		return false
	}
	optedIn := map[string]bool{}
	for _, e := range it.Rules.ExternalPaths {
		optedIn[osPath(e)] = true
	}
	seenBinds := map[string]bool{}
	addExternal := func(src protocol.ScopeSource, path string) {
		rp, exists, err := realPath(path)
		switch {
		case err != nil:
			src.State, src.Reason = protocol.SourceBlocked, "cannot resolve the path"
		case !exists:
			src.State, src.Reason = protocol.SourceExcluded, "the path does not exist"
		case slices.ContainsFunc(forbiddenExternal, func(f string) bool { return rp == osPath(f) }) || rp == filepath.VolumeName(rp)+string(filepath.Separator):
			src.State, src.Reason = protocol.SourceBlocked, "system paths are never backed up"
		case !optedIn[path] && !optedIn[rp]:
			src.State, src.Reason = protocol.SourceRequiresOpt, "outside the project directory: requires an explicit opt-in in the policy"
		case !s.allowlisted(rp):
			src.State, src.Reason = protocol.SourceBlocked, "opted in, but not allowed by this agent's DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST"
		case s.insideDockerRoot(rp):
			src.State, src.Reason = protocol.SourceBlocked, "inside Docker's data root: select the volume instead"
		default:
			src.State, src.Reason = protocol.SourceIncluded, "external path opted in and allowlisted"
			p.paths = append(p.paths, rp)
		}
		p.sources = append(p.sources, src)
	}
	for _, b := range proj.Binds {
		src := osPath(b.Source)
		key := b.Service + "\x00" + src
		if seenBinds[key] {
			continue
		}
		seenBinds[key] = true
		ss := protocol.ScopeSource{Kind: protocol.SourceBind, Path: filepath.ToSlash(src), Service: b.Service}
		if inside(src, dir) {
			rp, exists, err := realPath(src)
			switch {
			case err != nil:
				ss.State, ss.Reason = protocol.SourceBlocked, "cannot resolve the path"
			case exists && !inside(rp, realDir):
				ss.State, ss.Reason = protocol.SourceBlocked, "a symlink leads outside the project directory; it is not followed"
			case excluded(src):
				ss.State, ss.Reason = protocol.SourceExcluded, "excluded by a path rule"
			case !exists:
				ss.State, ss.Reason = protocol.SourceExcluded, "the path does not exist"
			default:
				ss.State, ss.Reason = protocol.SourceIncluded, "relative bind source inside the project directory"
			}
			p.sources = append(p.sources, ss)
			continue
		}
		ss.Kind = protocol.SourceExternal
		addExternal(ss, src)
		delete(optedIn, src)
	}
	for e := range optedIn {
		// Opted-in paths that no bind names (e.g. data a service reaches
		// another way) are allowed on the same terms.
		if !slices.Contains(p.paths, e) {
			addExternal(protocol.ScopeSource{Kind: protocol.SourceExternal, Path: filepath.ToSlash(e)}, e)
		}
	}
	s.planStackVolumes(ctx, eng, p, proj)
	if shutdown {
		s.planShutdown(ctx, eng, p)
	}
	p.paths = dedupe(p.paths)
	sort.Strings(p.excludes)
}

func (s *Service) insideDockerRoot(p string) bool {
	res := s.storage()
	return res != nil && res.DockerRootDir != "" && inside(p, osPath(res.DockerRootDir))
}

// planStackVolumes selects a stack's named (and optionally anonymous)
// volumes.
func (s *Service) planStackVolumes(ctx context.Context, eng engine.Engine, p *itemPlan, proj *compose.Project) {
	it := p.item
	type vol struct{ key, name, service string }
	var named []vol
	seen := map[string]bool{}
	for _, v := range proj.Volumes {
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		named = append(named, vol{v.Key, v.Name, v.Service})
	}
	matches := func(list []string, v vol) bool { return slices.Contains(list, v.key) || slices.Contains(list, v.name) }
	for _, v := range named {
		ss := protocol.ScopeSource{Kind: protocol.SourceVolume, Name: v.name, Service: v.service}
		switch {
		case matches(it.Rules.VolumeExclude, v):
			ss.State, ss.Reason = protocol.SourceExcluded, "excluded by the policy"
		case len(it.Rules.VolumeInclude) > 0 && !matches(it.Rules.VolumeInclude, v):
			ss.State, ss.Reason = protocol.SourceExcluded, "not in the policy's volume selection"
		default:
			s.includeVolume(ctx, eng, p, &ss, v.name)
		}
		p.sources = append(p.sources, ss)
	}
	// Anonymous volumes only exist on containers.
	containers, err := lifecycle.ProjectContainers(ctx, eng, p.project)
	if err != nil {
		p.warnings = append(p.warnings, "the project's containers could not be listed: "+err.Error())
		return
	}
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			ss := protocol.ScopeSource{Kind: protocol.SourceAnonymous, Name: m.Name, Service: c.Labels[lifecycle.ComposeServiceLabel]}
			if !it.Rules.AnonymousVolumes {
				ss.State, ss.Reason = protocol.SourceExcluded, "anonymous volumes are off (enable them in the policy)"
			} else {
				s.includeVolume(ctx, eng, p, &ss, m.Name)
			}
			p.sources = append(p.sources, ss)
		}
	}
}

// includeVolume adds a volume's data directory when it is supported and
// not Docker Manager's own.
func (s *Service) includeVolume(ctx context.Context, eng engine.Engine, p *itemPlan, ss *protocol.ScopeSource, name string) {
	v, err := eng.InspectVolume(ctx, name)
	if engine.IsCode(err, engine.CodeNotFound) {
		ss.State, ss.Reason = protocol.SourceExcluded, "the volume does not exist (not created yet)"
		return
	}
	if err != nil {
		ss.State, ss.Reason = protocol.SourceBlocked, "the volume could not be inspected"
		return
	}
	ss.Path = filepath.ToSlash(osPath(v.Mountpoint))
	if p.prot != nil {
		if prot := p.prot.Volume(v.Name, v.Labels); prot != nil {
			ss.State, ss.Reason = protocol.SourceExcluded, "Docker Manager's own volume: "+prot.Reason
			return
		}
	}
	if res := s.storage(); res != nil {
		if acc := res.AccessFor(v); !acc.Supported {
			ss.State, ss.Reason = protocol.SourceBlocked, acc.Reason
			return
		}
	} else {
		ss.State, ss.Reason = protocol.SourceBlocked, "the storage layout has not been verified yet"
		return
	}
	rp, exists, err := realPath(osPath(v.Mountpoint))
	if err != nil || !exists || !inside(rp, osPath(v.Mountpoint)) {
		ss.State, ss.Reason = protocol.SourceBlocked, "the volume's data directory is missing or a symlink"
		return
	}
	ss.State, ss.Reason = protocol.SourceIncluded, "named volume"
	if ss.Kind == protocol.SourceAnonymous {
		ss.Reason = "anonymous volume"
	}
	p.paths = append(p.paths, rp)
	p.volumes = append(p.volumes, v.Name)
	if p.volumePaths == nil {
		p.volumePaths = map[string]string{}
	}
	p.volumePaths[v.Name] = snapPath(rp)
}

func (s *Service) planVolume(ctx context.Context, eng engine.Engine, p *itemPlan) {
	it := p.item
	ss := protocol.ScopeSource{Kind: protocol.SourceVolume, Name: it.Volume}
	s.includeVolume(ctx, eng, p, &ss, it.Volume)
	p.sources = append(p.sources, ss)
	if ss.State != protocol.SourceIncluded {
		p.err = refuse("volume_unavailable", "volume %s cannot be backed up: %s", it.Volume, ss.Reason)
		return
	}
	root := osPath(ss.Path)
	for _, rel := range it.Rules.PathExcludes {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if !inside(abs, root) || abs == root {
			p.err = refuse("invalid_scope", "path exclude %q leaves the volume", rel)
			return
		}
		p.excludes = append(p.excludes, abs)
	}
	// Containers using a standalone volume are never stopped: they are
	// surfaced (the backup is crash-consistent for them).
	for _, c := range p.all {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == it.Volume && c.State == "running" {
				p.conflicts = append(p.conflicts, fmt.Sprintf("container %s uses the volume while it is backed up (not stopped; crash-consistent)",
					strings.TrimPrefix(firstName(c), "/")))
			}
		}
	}
	sort.Strings(p.excludes)
}

func firstName(c engine.Container) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.ID
}

// planShutdown lists the containers a shutdown stops (stop order) and the
// conflicts it cannot resolve: included volumes used by containers outside
// the project, which are never stopped silently.
func (s *Service) planShutdown(ctx context.Context, eng engine.Engine, p *itemPlan) {
	containers, err := lifecycle.ProjectContainers(ctx, eng, p.project)
	if err != nil {
		p.warnings = append(p.warnings, "the project's containers could not be listed: "+err.Error())
		return
	}
	g, err := lifecycle.GraphFromContainers(containers)
	if err != nil {
		p.warnings = append(p.warnings, "the dependency graph could not be read: "+err.Error())
		return
	}
	prot := map[string]string{}
	if p.prot != nil {
		for _, c := range containers {
			if pr := p.prot.Container(c.ID); pr != nil {
				prot[c.ID] = pr.Reason
			}
		}
	}
	running := map[string]bool{}
	for _, c := range containers {
		if c.State == "running" || c.State == "restarting" || c.State == "paused" {
			running[c.Labels[lifecycle.ComposeServiceLabel]] = true
		}
	}
	var runningServices []string
	for svc := range running {
		runningServices = append(runningServices, svc)
	}
	order := map[string]int{}
	if len(prot) > 0 {
		// Docker Manager's own project is never stopped (#32): the run backs the
		// whole project up live, so no container of it gets a stop order.
		p.conflicts = append(p.conflicts, fmt.Sprintf("stack %s contains Docker Manager's own containers; it is backed up live", p.project))
	} else {
		for i, svc := range g.StopOrder(runningServices) {
			order[svc] = i + 1
		}
	}
	for _, c := range containers {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		a := protocol.AffectedContainer{Name: strings.TrimPrefix(firstName(c), "/"), Project: p.project, Service: svc,
			Running: c.State == "running"}
		if reason, ok := prot[c.ID]; ok {
			a.Protected = reason
		} else if running[svc] {
			a.StopOrder = order[svc]
		}
		p.affected = append(p.affected, a)
	}
	sort.SliceStable(p.affected, func(i, j int) bool {
		a, b := p.affected[i], p.affected[j]
		if (a.StopOrder == 0) != (b.StopOrder == 0) {
			return a.StopOrder != 0
		}
		if a.StopOrder != b.StopOrder {
			return a.StopOrder < b.StopOrder
		}
		return a.Name < b.Name
	})
	// Shared volumes: containers of other projects (or unmanaged ones)
	// mounting an included volume keep running.
	for _, c := range p.all {
		if c.Labels[lifecycle.ComposeProjectLabel] == p.project {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" && slices.Contains(p.volumes, m.Name) && c.State == "running" {
				p.conflicts = append(p.conflicts, fmt.Sprintf("volume %s is also used by container %s outside the stack; it is not stopped", m.Name,
					strings.TrimPrefix(firstName(c), "/")))
			}
		}
	}
	sort.Strings(p.conflicts)
}

// dedupe drops paths inside other paths (restic would store them twice).
func dedupe(paths []string) []string {
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		if slices.ContainsFunc(out, func(o string) bool { return inside(p, o) }) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// estimate walks the included paths (not following symlinks, skipping
// excluded ones) within budget entries.
func estimate(ctx context.Context, paths, excludes []string, budget int) (bytes, files int64, complete bool) {
	complete = true
	n := 0
	for _, root := range paths {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				complete = false
				return filepath.SkipAll
			}
			n++
			if n > budget {
				complete = false
				return filepath.SkipAll
			}
			for _, e := range excludes {
				if inside(p, e) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if d.Type().IsRegular() {
				if fi, err := d.Info(); err == nil {
					bytes += fi.Size()
					files++
				}
			}
			return nil
		})
	}
	return bytes, files, complete
}
