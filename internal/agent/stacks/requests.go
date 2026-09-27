package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Compose CLI labels read by discovery.
const (
	labelWorkingDir  = "com.docker.compose.project.working_dir"
	labelConfigFiles = "com.docker.compose.project.config_files"
	labelEnvFile     = "com.docker.compose.project.environment_file"
)

func decode[T any](input json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed input: " + err.Error()}
	}
	return v, nil
}

// validate serves compose.validate: the on-disk definition, or the given
// files in memory as if they were in the project directory. Findings are
// the output; only infrastructure failures are errors.
func (s *Service) validate(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.ComposeValidateInput](input)
	if err != nil {
		return nil, err
	}
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return nil, err
	}
	spec := specOf(in.Stack, dir)
	if len(in.Files) > 0 {
		if err := protocol.ValidateSources(in.Files); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeTooLarge, Message: err.Error()}
		}
		spec.Content = map[string][]byte{}
		for _, f := range in.Files {
			spec.Content[f.Path] = f.Content
		}
		spec.SkipEnvFiles = true
	}
	return validateSpec(ctx, spec), nil
}

func validateSpec(ctx context.Context, spec compose.ProjectSpec) protocol.ComposeValidateOutput {
	out := protocol.ComposeValidateOutput{Errors: []protocol.ComposeIssue{}, Warnings: []protocol.ComposeIssue{},
		Services: []protocol.ComposeService{}, Binds: []protocol.ComposeBind{}}
	p, err := compose.LoadProject(ctx, spec)
	if err != nil {
		code := protocol.IssueInvalidProject
		if engine.CodeOf(err) == engine.CodeUnsupportedFeature {
			code = protocol.IssueUnsupportedFeature
		}
		msg := err.Error()
		var ee *engine.Error
		if errors.As(err, &ee) {
			msg = ee.Message
		}
		out.Errors = append(out.Errors, protocol.ComposeIssue{Code: code, Message: msg})
		return out
	}
	out.Valid = true
	out.ProjectName = p.Name
	out.Services = serviceInfos(p)
	out.Binds = binds(p, spec.Dir)
	out.Warnings = warnings(p, out.Binds)
	return out
}

// read serves compose.read: the definition files on disk.
func (s *Service) read(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.ComposeReadInput](input)
	if err != nil {
		return nil, err
	}
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return nil, err
	}
	return readDefinition(ctx, in.Stack, dir)
}

func readDefinition(ctx context.Context, ref protocol.ProjectRef, dir string) (protocol.ComposeReadOutput, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return protocol.ComposeReadOutput{Missing: true, Snapshot: protocol.NewSourceSnapshot(nil)}, nil
	}
	files, _ := definitionFiles(ctx, ref, dir)
	snap, err := readSources(dir, files)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return protocol.ComposeReadOutput{}, &session.HandlerError{Code: protocol.CodeTooLarge, Message: err.Error()}
		}
		return protocol.ComposeReadOutput{}, &session.HandlerError{Code: protocol.CodeInternal, Message: "could not read the definition files"}
	}
	out := protocol.ComposeReadOutput{Snapshot: snap, Missing: true}
	for _, f := range snap.Files {
		if slices.Contains(compose.DefaultConfigFiles, f.Path) || slices.Contains(ref.ConfigFiles, f.Path) {
			out.Missing = false
		}
	}
	return out, nil
}

// write serves compose.write: create a new project directory or replace
// definition files when the current definition has the expected hash.
func (s *Service) write(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.ComposeWriteInput](input)
	if err != nil {
		return nil, err
	}
	dir, err := s.resolve(in.Stack)
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateSources(in.Files); err != nil {
		return nil, &session.HandlerError{Code: protocol.CodeTooLarge, Message: err.Error()}
	}
	if len(in.Files) == 0 {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "no files to write"}
	}
	switch in.Mode {
	case protocol.WriteCreate:
		if len(in.Remove) > 0 || in.ExpectHash != "" {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "create takes no expected hash or removals"}
		}
		if err := createDir(dir); err != nil {
			return nil, err
		}
	case protocol.WriteReplace:
		cur, err := readDefinition(ctx, in.Stack, dir)
		if err != nil {
			return nil, err
		}
		if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
			return nil, &session.HandlerError{Code: protocol.CodeNotFound, Message: "the project directory does not exist"}
		}
		if cur.Snapshot.Hash != in.ExpectHash {
			return nil, &session.HandlerError{Code: protocol.CodeConflict, Message: "the definition on disk changed since it was read"}
		}
		for _, r := range in.Remove {
			if !slices.ContainsFunc(cur.Snapshot.Files, func(f protocol.SourceFile) bool { return f.Path == r }) {
				return nil, &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: fmt.Sprintf("%s is not a definition file of the project", r)}
			}
		}
	default:
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: fmt.Sprintf("unknown write mode %q", in.Mode)}
	}
	for _, f := range in.Files {
		if err := writeFile(dir, f); err != nil {
			return nil, err
		}
	}
	for _, r := range in.Remove {
		if slices.ContainsFunc(in.Files, func(f protocol.SourceFile) bool { return f.Path == r }) {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(r))
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, &session.HandlerError{Code: protocol.CodeInternal, Message: "could not remove " + r}
		}
	}
	out, err := readDefinition(ctx, in.Stack, dir)
	if err != nil {
		return nil, err
	}
	s.log.Info("stack definition written", "project", in.Stack.ProjectName, "mode", in.Mode, "files", len(in.Files),
		"removed", len(in.Remove), "hash", out.Snapshot.Hash)
	return protocol.ComposeWriteOutput{Snapshot: out.Snapshot}, nil
}

// createDir creates a new, empty project directory. An existing directory
// with any content is never reused: nothing is overwritten silently.
func createDir(dir string) error {
	if st, err := os.Lstat(dir); err == nil {
		if !st.IsDir() {
			return &session.HandlerError{Code: protocol.CodeConflict, Message: "the project path exists and is not a directory"}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not read the project directory"}
		}
		if len(entries) > 0 {
			return &session.HandlerError{Code: protocol.CodeConflict, Message: "the project directory already exists and is not empty"}
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // project directories are read by the Engine and users' containers
		return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not create the project directory"}
	}
	return nil
}

// writeFile atomically writes one definition file inside dir (temp file,
// fsync, rename). It never writes through a symlink or outside dir. Env
// files get mode 0600, everything else 0644.
func writeFile(dir string, f protocol.SourceFile) error {
	target := filepath.Join(dir, filepath.FromSlash(f.Path))
	parent := filepath.Dir(target)
	if parent != dir {
		// The deepest existing ancestor must resolve inside the project
		// before anything is created below it, and the parent after.
		existing := parent
		for existing != dir {
			if _, err := os.Lstat(existing); err == nil {
				break
			}
			existing = filepath.Dir(existing)
		}
		if existing != dir {
			if err := beneath(dir, existing); err != nil {
				return &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: f.Path + ": " + err.Error()}
			}
		}
		if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // see createDir
			return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not create " + path.Dir(f.Path)}
		}
		if err := beneath(dir, parent); err != nil {
			return &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: f.Path + ": " + err.Error()}
		}
	}
	if st, err := os.Lstat(target); err == nil && (st.Mode()&os.ModeSymlink != 0 || st.IsDir()) {
		return &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: f.Path + " is a symlink or directory; not overwritten"}
	}
	mode := os.FileMode(0o644)
	if base := path.Base(f.Path); strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		mode = 0o600
	}
	tmp, err := os.CreateTemp(parent, ".docker-manager-*.tmp")
	if err != nil {
		return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not write " + f.Path}
	}
	_, werr := tmp.Write(f.Content)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr, os.Chmod(tmp.Name(), mode)); err != nil {
		_ = os.Remove(tmp.Name())
		return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not write " + f.Path}
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return &session.HandlerError{Code: protocol.CodeInternal, Message: "could not write " + f.Path}
	}
	return nil
}

// discover serves compose.discover: every Compose project the Engine knows
// from container labels, with whether it can be adopted in place.
func (s *Service) discover(ctx context.Context, input json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(input)) > 0 && string(bytes.TrimSpace(input)) != "null" && string(bytes.TrimSpace(input)) != "{}" {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "compose.discover takes no input"}
	}
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	// Every container: another project's manager container (Arcane,
	// Dockge, ...) tells where a project's files are on the host.
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, handlerError(err)
	}
	var res *storage.Result
	if s.opts.Deps != nil {
		res = s.opts.Deps.Storage()
	}
	return discoverProjects(list, res), nil
}

func discoverProjects(list []engine.Container, res *storage.Result) protocol.ComposeDiscoverOutput {
	type acc struct {
		p        protocol.DiscoveredProject
		services map[string]*protocol.DiscoveredService
		// dirs are the working directories of all its containers.
		dirs []string
	}
	projects := map[string]*acc{}
	var names []string
	for _, c := range list {
		name := c.Labels[lifecycle.ComposeProjectLabel]
		if name == "" || c.Labels[lifecycle.ComposeOneoffLabel] == "True" {
			continue
		}
		a, ok := projects[name]
		if !ok {
			a = &acc{p: protocol.DiscoveredProject{Name: name}, services: map[string]*protocol.DiscoveredService{}}
			projects[name] = a
			names = append(names, name)
		}
		if wd := c.Labels[labelWorkingDir]; wd != "" {
			wd = path.Clean(filepath.ToSlash(wd))
			if a.p.WorkingDir == "" {
				a.p.WorkingDir = wd
				a.p.ConfigFiles = splitLabel(c.Labels[labelConfigFiles])
				a.p.EnvFiles = splitLabel(c.Labels[labelEnvFile])
			}
			if !slices.Contains(a.dirs, wd) {
				a.dirs = append(a.dirs, wd)
			}
		}
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		ds, ok := a.services[svc]
		if !ok {
			ds = &protocol.DiscoveredService{Name: svc, Image: c.Image}
			a.services[svc] = ds
		}
		ds.Containers++
		if c.State == "running" {
			ds.Running++
		}
	}
	slices.Sort(names)
	out := protocol.ComposeDiscoverOutput{Projects: []protocol.DiscoveredProject{}}
	for _, n := range names {
		a := projects[n]
		a.p.Services = []protocol.DiscoveredService{}
		var svcs []string
		for k := range a.services {
			svcs = append(svcs, k)
		}
		slices.Sort(svcs)
		for _, k := range svcs {
			a.p.Services = append(a.p.Services, *a.services[k])
		}
		if outside := locate(&a.p, res); outside {
			importable(&a.p, res, a.dirs, list)
		}
		out.Projects = append(out.Projects, a.p)
	}
	return out
}

func splitLabel(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, path.Clean(filepath.ToSlash(p)))
		}
	}
	return out
}

// locate decides whether a discovered project can be adopted in place: its
// working directory must be below a verified stack root (not the root
// itself) and its Compose and env files inside the working directory. It
// reports outside when the directory lies outside every stack root (the
// project may still be importable by copy).
func locate(p *protocol.DiscoveredProject, res *storage.Result) (outside bool) {
	switch {
	case p.WorkingDir == "":
		p.Reason = "the containers carry no project directory label; import it with an explicit Compose source"
		return false
	case res == nil:
		p.Reason = "the agent's storage layout has not been verified"
		return false
	}
	for _, r := range res.Roots {
		if !r.OK || (r.Kind != storage.KindStacks && r.Kind != storage.KindBind) {
			continue
		}
		root := path.Clean(filepath.ToSlash(r.Path))
		if !strings.HasPrefix(p.WorkingDir, root+"/") {
			continue
		}
		p.Dir = strings.TrimPrefix(p.WorkingDir, root+"/")
		p.Root = protocol.RootStacks
		if r.Kind == storage.KindBind {
			p.Root, p.RootPath = protocol.RootBind, root
		}
		for _, f := range append(slices.Clone(p.ConfigFiles), p.EnvFiles...) {
			if !strings.HasPrefix(f, p.WorkingDir+"/") {
				p.Reason = fmt.Sprintf("%s is outside the project directory; import it with an explicit Compose source", f)
				return false
			}
		}
		if len(p.ConfigFiles) == 0 {
			p.Reason = "the containers carry no Compose file label; import it with an explicit Compose source"
			return false
		}
		p.Adoptable = true
		return false
	}
	p.Reason = "the project directory is outside the stacks volume and the registered stack roots"
	return true
}

// importable decides whether a project outside the stack roots can be
// imported by copying its directory into the stacks volume (stack.import):
// its directory, resolved on the host from its containers' labels
// (ProjectDir, also through a manager container's mounts), must be
// visible through an import mount, its Compose files must be there and
// the stacks volume must not have a directory of that name yet.
func importable(p *protocol.DiscoveredProject, res *storage.Result, dirs []string, all []engine.Container) {
	const outside = "the project directory is outside the stacks volume and the registered stack roots"
	for _, f := range append(slices.Clone(p.ConfigFiles), p.EnvFiles...) {
		if !strings.HasPrefix(f, p.WorkingDir+"/") {
			p.Reason = fmt.Sprintf("%s and %s is outside it; import it with an explicit Compose source", outside, f)
			return
		}
	}
	if len(p.ConfigFiles) == 0 {
		p.Reason = outside + " and the containers carry no Compose file label; import it with an explicit Compose source"
		return
	}
	host, src, err := ProjectDir(res, dirs, all)
	if err != nil {
		p.Reason = fmt.Sprintf("%s: %v", outside, err)
		return
	}
	for _, f := range p.ConfigFiles {
		if fi, err := os.Stat(filepath.Join(src, filepath.FromSlash(strings.TrimPrefix(f, p.WorkingDir+"/")))); err != nil || !fi.Mode().IsRegular() {
			p.Reason = fmt.Sprintf("%s and its Compose file %s is not in %s", outside, path.Base(f), host)
			return
		}
	}
	switch {
	case !res.StacksOK():
		p.Reason = outside + " and the stacks volume is not verified (see the agent's storage diagnostics)"
		return
	case !protocol.ValidProjectName(p.Name):
		p.Reason = outside + " and its project name cannot name a directory of the stacks volume; import it with an explicit Compose source"
		return
	case within(host, res.StacksDir) || within(res.StacksDir, host):
		p.Reason = outside + " and overlaps the stacks volume"
		return
	}
	if _, err := os.Lstat(filepath.Join(filepath.FromSlash(res.StacksDir), p.Name)); err == nil {
		p.Reason = fmt.Sprintf("%s and the stacks volume already has a directory %s: move that away to import the project by copy", outside, p.Name)
		return
	}
	p.Copyable, p.SourceDir = true, host
	p.Reason = outside + ": import it by copy (its whole directory moves into the stacks volume)"
}

// services serves compose.services: the project's containers as seen on
// the Engine (live state for drift detection and the services view).
func (s *Service) services(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := decode[protocol.ComposeServicesInput](input)
	if err != nil {
		return nil, err
	}
	if !protocol.ValidProjectName(in.ProjectName) {
		return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "invalid project name"}
	}
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	list, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true, Labels: []string{lifecycle.ComposeProjectLabel + "=" + in.ProjectName}})
	if err != nil {
		return nil, handlerError(err)
	}
	out := protocol.ComposeServicesOutput{Containers: []protocol.StackContainer{}}
	for _, c := range list {
		d, err := eng.InspectContainer(ctx, c.ID)
		if engine.IsCode(err, engine.CodeNotFound) {
			continue // removed meanwhile
		}
		if err != nil {
			return nil, handlerError(err)
		}
		out.Containers = append(out.Containers, stackContainer(c, d))
	}
	slices.SortFunc(out.Containers, func(a, b protocol.StackContainer) int {
		return strings.Compare(a.Service+"/"+a.Name, b.Service+"/"+b.Name)
	})
	return out, nil
}

func stackContainer(c engine.Container, d engine.ContainerDetails) protocol.StackContainer {
	sc := protocol.StackContainer{
		ID: d.ID, Name: d.Name, Service: c.Labels[lifecycle.ComposeServiceLabel], Image: d.Image, ImageID: d.ImageID,
		State: d.State.Status, ExitCode: d.State.ExitCode, OneOff: c.Labels[lifecycle.ComposeOneoffLabel] == "True",
		RestartPolicy: d.RestartPolicy, ConfigHash: c.Labels["com.docker.compose.config-hash"], CreatedAt: d.Created.UTC(),
		Resources: protocol.ContainerResources{NanoCPUs: d.Resources.NanoCPUs, CPUShares: d.Resources.CPUShares,
			Memory: d.Resources.Memory, PidsLimit: d.Resources.PidsLimit},
	}
	if d.State.Health != nil && d.State.Health.Status != "none" {
		sc.Health = d.State.Health.Status
	}
	if !d.State.StartedAt.IsZero() {
		t := d.State.StartedAt.UTC()
		sc.StartedAt = &t
	}
	ports := d.Ports
	if len(ports) == 0 {
		ports = c.Ports
	}
	for _, p := range ports {
		sc.Ports = append(sc.Ports, protocol.PortMapping{PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, HostIP: p.HostIP, Protocol: p.Protocol})
	}
	for _, n := range slices.Sorted(maps.Keys(d.Networks)) {
		ep := d.Networks[n]
		sc.Networks = append(sc.Networks, protocol.ContainerNetwork{Name: n, NetworkID: ep.NetworkID, IPAddress: ep.IPAddress, IPv6Address: ep.IPv6Address})
	}
	return sc
}
