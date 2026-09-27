package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Import by copy (#7): stack.import moves a discovered Compose project
// whose directory lies outside the stack roots into the stacks volume.
//
//  1. prepare: the project is read where it is, through the agent's
//     read-only import mount (storage.ImportDir): it must load, keep its
//     definition files inside its directory and bind nothing next to it
//     through a relative path (it would point elsewhere after the move);
//     every image its containers need must be on the host and the stacks
//     volume must have room. Nothing has changed yet.
//  2. stop_containers: the services that run are stopped in dependency
//     order (the start_containers compensation is journaled first), and
//     nothing of the project may still run: the copy is taken at rest, so
//     databases next to the Compose file are consistent.
//  3. copy_files: the whole directory is copied into a staging directory of
//     the stacks volume with the migration archive format (numeric owners,
//     permission and special bits, times, symlinks, hard links, FIFOs),
//     then extended attributes (ACLs, file capabilities); the copy is
//     compared entry by entry with the source, flushed to disk and renamed
//     to <project>. The remove_import_copy compensation removes it on any
//     failure until the project switches to it.
//  4. recreate: the project is loaded from the copy and its bind sources
//     checked against the original; then the project switches (journaled,
//     the copy's compensation released) and Compose recreates every
//     service that had containers from the copy (anonymous volumes are
//     inherited), without starting them.
//  5. start_containers: exactly the services that ran before start again,
//     dependencies first; a service whose containers still use the
//     original directory is never started.
//
// Docker Manager's own project (#32) is never stopped: it is copied while
// it runs (StackImportReport.Live) and nothing is recreated or started;
// its containers keep running from the original directory until the
// stack's next deploy recreates them from the copy (the agent's own
// service through the self-update helper). prepare refuses it when a
// service writes into the project directory (that data cannot be copied
// consistently while it runs).
//
// A build-only service (build section, no image) keeps the image it runs:
// the tool that built it may have named it otherwise than Compose does
// (Arcane), so recreate tags that image with Compose's name first.
//
// The original directory is only ever read. Before the switch a failure
// changes nothing (the copy is removed, what ran starts again from the
// original); after it the project lives in the stacks volume and a deploy
// finishes an interrupted import (no automatic rollback, #25).

// Import error classes (stable, #26).
const (
	classImportSourceUnavailable = "import_source_unavailable"
	classImportDirectoryExists   = "stack_directory_exists"
	classImportNotRelocatable    = "import_not_relocatable"
	classImportImageMissing      = "import_image_missing"
	classImportSourceChanged     = "import_source_changed"
	classImportNoSpace           = "insufficient_space"
	classImportShutdownFailed    = "shutdown_failed"
	classImportCopyFailed        = "copy_failed"
)

// Recovery guidance of import failures.
const (
	recoveryImportUnchanged = "Nothing changed: the copy (if any) was removed and the services that ran before were started " +
		"again from the original directory. Fix the cause and import the project again."
	recoveryImportSwitched = "The project now lives in the stacks volume (its original directory was only read and is " +
		"unchanged). Deploy the stack to finish; services whose containers still used the original directory were not started."
)

// importStagingPrefix names the staging directory of a copy in the
// stacks volume (never a valid project directory name).
const importStagingPrefix = ".docker-manager-import-"

// importProgressInterval is how often a running copy reports progress.
const importProgressInterval = 2 * time.Second

func importRefusal(class, format string, args ...any) error {
	return &stepError{class: class, recovery: recoveryImportUnchanged, err: fmt.Errorf(format, args...)}
}

// importResume is the start_containers compensation's record.
type importResume struct {
	Project    string   `json:"project"`
	WasRunning []string `json:"wasRunning"`
	// NewDir is the copy (host path): once a container of the project
	// uses it, only services whose containers all use it are started.
	NewDir string `json:"newDir"`
}

// importCopyRecord is the remove_import_copy compensation's record.
type importCopyRecord struct {
	// Root is the stacks volume's path; Staging and Dir are below it.
	Root    string `json:"root"`
	Staging string `json:"staging"`
	Dir     string `json:"dir"`
	// Dev and Ino identify the staging directory, also once renamed to
	// Dir: a directory Dir that is not it is never removed.
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

func (s *Service) importExecutor() jobexec.Executor {
	return jobexec.Executor{Kind: jobspec.StackImport, Steps: map[string]jobexec.StepFunc{
		"prepare":          classified(s.importPrepare),
		"stop_containers":  classified(s.importStop),
		"copy_files":       classified(s.importCopy),
		"recreate":         classified(s.importRecreate),
		"start_containers": classified(s.importStart),
	}, Compensations: map[string]jobexec.CompensationFunc{
		jobspec.CompStartContainers:  s.importCompStart,
		jobspec.CompRemoveImportCopy: s.importCompRemove,
	}}
}

func importInput(sc *jobexec.StepContext) (protocol.StackJobInput, error) {
	in, err := input(sc)
	if err != nil {
		return in, err
	}
	if in.Import == nil {
		return in, errors.New("stack.import needs the project's current directory")
	}
	if err := in.Import.Validate(); err != nil {
		return in, err
	}
	if in.Stack.Root != protocol.RootStacks || in.Stack.Dir != in.Stack.ProjectName {
		return in, errors.New("stack.import copies a project into the stacks volume directory named after it")
	}
	return in, nil
}

func stackOutput(sc *jobexec.StepContext) protocol.StackJobOutput {
	var o protocol.StackJobOutput
	if b := sc.Output(); len(b) > 0 {
		_ = json.Unmarshal(b, &o)
	}
	if o.Import == nil {
		o.Import = &protocol.StackImportReport{}
	}
	return o
}

// updateImport journals a change of the import report.
func updateImport(ctx context.Context, sc *jobexec.StepContext, fn func(o *protocol.StackJobOutput, r *protocol.StackImportReport)) error {
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		if o.Import == nil {
			o.Import = &protocol.StackImportReport{}
		}
		fn(o, o.Import)
	})
}

// allProfiles loads every service the project defines: containers of
// services behind a profile move with the project too.
func allProfiles(ref protocol.ProjectRef) protocol.ProjectRef {
	ref.Profiles = []string{"*"}
	return ref
}

func (s *Service) verifiedStorage() (*storage.Result, error) {
	var res *storage.Result
	if s.opts.Deps != nil {
		res = s.opts.Deps.Storage()
	}
	if res == nil || !res.StacksOK() {
		return nil, importRefusal("storage_unverified", "the stacks volume is not verified on this agent (see its storage diagnostics)")
	}
	return res, nil
}

// importSource returns where the agent reads the project's directory.
func (s *Service) importSource(in protocol.StackJobInput) (string, *storage.Result, error) {
	res, err := s.verifiedStorage()
	if err != nil {
		return "", nil, err
	}
	wd := in.Import.WorkingDir
	if within(wd, res.StacksDir) || within(res.StacksDir, wd) {
		return "", nil, importRefusal(classImportSourceUnavailable, "%s overlaps the stacks volume", wd)
	}
	p, ok := res.ImportSource(wd)
	if !ok {
		return "", nil, importRefusal(classImportSourceUnavailable,
			"%s is not visible to the agent: mount it, or a directory above it, into the agent below %s (read-only is enough)", wd, storage.ImportDir)
	}
	local := filepath.FromSlash(p)
	fi, err := os.Lstat(local)
	if err != nil || !fi.IsDir() {
		return "", nil, importRefusal(classImportSourceUnavailable, "%s is not a directory inside the agent (%s)", p, errOr(err, "not a directory"))
	}
	return local, res, nil
}

// ProjectDir resolves where a discovered project's files are on the host,
// and where the agent reads them, from its containers' working directory
// labels. A label is a host path when Compose ran on the host; a manager
// that runs Compose inside its own container (Arcane, Dockge, Portainer,
// ...) records its own path instead, e.g. /app/data/projects/<name>, which
// is translated through the mount of the container covering it (the
// manager's /app/data is a host directory or volume). Every label must
// lead to the same directory, readable through an import mount.
func ProjectDir(res *storage.Result, dirs []string, all []engine.Container) (host, local string, err error) {
	if len(dirs) == 0 {
		return "", "", errors.New("its containers carry no project directory label")
	}
	for _, d := range dirs {
		h, l, err := hostDir(res, d, all)
		if err != nil {
			return "", "", err
		}
		if host != "" && h != host {
			return "", "", fmt.Errorf("its containers were created from different directories (%s and %s)", host, h)
		}
		host, local = h, l
	}
	return host, local, nil
}

// hostDir resolves one working directory label (see ProjectDir).
func hostDir(res *storage.Result, label string, all []engine.Container) (string, string, error) {
	label = path.Clean(filepath.ToSlash(label))
	if l, ok := visibleDir(res, label); ok {
		return label, l, nil
	}
	var found, locals []string
	for _, c := range all {
		for _, m := range c.Mounts {
			dst, src := path.Clean(filepath.ToSlash(m.Destination)), path.Clean(filepath.ToSlash(m.Source))
			if m.Destination == "" || m.Source == "" || dst == "/" || !within(dst, label) {
				continue
			}
			h := path.Join(src, strings.TrimPrefix(strings.TrimPrefix(label, dst), "/"))
			if l, ok := visibleDir(res, h); ok && !slices.Contains(found, h) {
				found, locals = append(found, h), append(locals, l)
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], locals[0], nil
	case 0:
		return "", "", fmt.Errorf("%s is not visible to the agent: mount it (or a directory above it) into the agent below %s "+
			"(read-only is enough) to import it by copy", label, storage.ImportDir)
	}
	return "", "", fmt.Errorf("%s leads to several host directories (%s)", label, strings.Join(found, ", "))
}

// visibleDir returns where the agent reads a host directory through an
// import mount, when it exists there.
func visibleDir(res *storage.Result, host string) (string, bool) {
	p, ok := res.ImportSource(host)
	if !ok {
		return "", false
	}
	local := filepath.FromSlash(p)
	if fi, err := os.Stat(local); err != nil || !fi.IsDir() {
		return "", false
	}
	return local, true
}

func errOr(err error, s string) string {
	if err != nil {
		return err.Error()
	}
	return s
}

// within reports whether p is root or below it (slash paths).
func within(root, p string) bool {
	root, p = path.Clean(filepath.ToSlash(root)), path.Clean(filepath.ToSlash(p))
	return p == root || root == "/" || strings.HasPrefix(p, root+"/")
}

func containerName(c engine.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

// switchedFailure classifies a failure after the switch with the
// switched import's guidance.
func switchedFailure(err error) error {
	class := domain.ErrorStepFailed
	var ce jobexec.ClassedError
	if errors.As(classify(err), &ce) && ce.ErrorClass() != "" {
		class = ce.ErrorClass()
	}
	return &stepError{class: class, recovery: recoveryImportSwitched, err: err}
}

func isRunning(c engine.Container) bool {
	return c.State == "running" || c.State == "restarting" || c.State == "paused"
}

// ownProject reports whether project is Docker Manager's own (#32).
func (s *Service) ownProject(ctx context.Context, eng engine.Engine, project string) (bool, error) {
	if s.opts.Guard == nil {
		return false, nil
	}
	all, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return false, err
	}
	return s.opts.Guard.Identify(ctx, eng, all).Project(project) != nil, nil
}

// errOwnStop refuses to stop Docker Manager's own project.
var errOwnStop = &protection.Refusal{Code: protection.CodeProtected, Action: protection.Stop,
	Reason: "refused to stop Docker Manager's own Compose project: it is imported while it runs"}

// importPrepare checks everything that can be checked before the project
// stops and records the services that run.
func (s *Service) importPrepare(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importInput(sc)
	if err != nil {
		return err
	}
	src, res, err := s.importSource(in)
	if err != nil {
		return err
	}
	dst, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	if o := stackOutput(sc); !o.Import.Copied {
		if _, err := os.Lstat(dst); err == nil {
			return importRefusal(classImportDirectoryExists, "the stacks volume already has a directory %s; nothing was overwritten", in.Stack.Dir)
		}
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	ref := allProfiles(in.Stack)
	p, err := compose.LoadProject(ctx, specOf(ref, src))
	if err != nil {
		return err
	}
	if p.Name != in.Stack.ProjectName {
		return importRefusal(classImportSourceChanged, "the Compose files in %s define project %q, not %q", in.Import.WorkingDir, p.Name, in.Stack.ProjectName)
	}
	if _, issues := definitionFiles(ctx, ref, src); len(issues) > 0 {
		return importRefusal(classImportNotRelocatable, "%s", issues[0].Message)
	}
	var warns []protocol.ComposeIssue
	for _, b := range p.Binds {
		switch {
		case within(src, b.Source):
		case outsideMount(res, src, b.Source):
			return importRefusal(classImportNotRelocatable,
				"service %s binds %s next to the project directory through a relative path: after the copy it would point "+
					"into the stacks volume instead. Move the data into the project directory, or use an absolute host path", b.Service, b.Source)
		case within(in.Import.WorkingDir, b.Source) && src != filepath.FromSlash(in.Import.WorkingDir):
			warns = append(warns, protocol.ComposeIssue{Code: protocol.IssueBindOutsideProject, Service: b.Service,
				Message: fmt.Sprintf("service %s binds %s by its absolute path: it keeps using the original directory, not the copy", b.Service, b.Source)})
		}
	}
	live, err := s.ownProject(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	if live {
		for _, b := range p.Binds {
			if within(src, b.Source) && !b.ReadOnly {
				return importRefusal(classImportNotRelocatable, "service %s writes to %s inside the project directory: Docker Manager's "+
					"own project is copied while it runs, and that data cannot be copied consistently. Move it into a named volume "+
					"(or bind it read-only), redeploy, then import again", b.Service, b.Source)
			}
		}
	}
	containers, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return importRefusal(classImportSourceChanged, "project %s has no containers on this Engine any more", in.Stack.ProjectName)
	}
	if err := checkProjectDir(ctx, eng, res, in.Import.WorkingDir, containers); err != nil {
		return err
	}
	diffs, created, err := driftOf(ctx, eng, p, src, in.Import.WorkingDir, containers)
	if err != nil {
		return err
	}
	if len(diffs) > 0 {
		return driftRefusal(diffs, editedAfter(p.DefinitionFiles, src, created))
	}
	defined := map[string]compose.ServiceInfo{}
	for _, svc := range p.Services {
		defined[svc.Name] = svc
	}
	var wasRunning []string
	checked := map[string]bool{}
	for _, c := range containers {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		if isRunning(c) && !slices.Contains(wasRunning, svc) {
			wasRunning = append(wasRunning, svc)
		}
		def, ok := defined[svc]
		if !ok {
			warns = appendOnce(warns, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Service: svc,
				Message: fmt.Sprintf("service %s has containers but is not in the Compose files: they keep using the original directory and are not started again", svc)})
			continue
		}
		if checked[svc] {
			continue
		}
		checked[svc] = true
		if exp, _ := p.Expected(svc); exp.Built && c.ImageID != "" {
			// A build-only service keeps the image it runs (recreate tags it).
			if _, err := eng.InspectImage(ctx, c.ImageID); engine.IsCode(err, engine.CodeNotFound) {
				return importRefusal(classImportImageMissing, "the image service %s runs is not on the host any more: build it first", svc)
			} else if err != nil {
				return err
			}
			continue
		}
		img, err := eng.InspectImage(ctx, def.Image)
		if engine.IsCode(err, engine.CodeNotFound) {
			return importRefusal(classImportImageMissing, "image %s of service %s is not on the host: pull or build it first", def.Image, svc)
		}
		if err != nil {
			return err
		}
		if c.ImageID != "" && img.ID != c.ImageID {
			warns = append(warns, protocol.ComposeIssue{Code: protocol.IssueInvalidProject, Service: svc,
				Message: fmt.Sprintf("service %s runs another image than %s names now: it is recreated with the current one", svc, def.Image)})
		}
	}
	slices.Sort(wasRunning)
	if err := checkSpace(ctx, src, res.StacksDir); err != nil {
		return err
	}
	before, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 5, fmt.Sprintf("checked %s: %d services, %d running", p.Name, len(p.Services), len(wasRunning)))
	return updateImport(ctx, sc, func(o *protocol.StackJobOutput, r *protocol.StackImportReport) {
		o.Before = before
		o.Warnings = warns
		r.WasRunning = wasRunning
		r.Live = live
	})
}

// checkProjectDir makes sure the project's containers still come from
// host (every working directory label resolves to it) and that none binds
// data the copy would not hold: a bind below a label that is not the host
// directory itself (a manager's internal path Compose resolved a relative
// bind against, without translating it) lives elsewhere on the host.
func checkProjectDir(ctx context.Context, eng engine.Engine, res *storage.Result, host string, containers []engine.Container) error {
	all, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return err
	}
	var dirs []string
	for _, c := range containers {
		if wd := c.Labels[labelWorkingDir]; wd != "" && !slices.Contains(dirs, path.Clean(filepath.ToSlash(wd))) {
			dirs = append(dirs, path.Clean(filepath.ToSlash(wd)))
		}
	}
	got, _, err := ProjectDir(res, dirs, all)
	if err != nil {
		return importRefusal(classImportSourceChanged, "the project's directory cannot be resolved any more: %v", err)
	}
	if got != host {
		return importRefusal(classImportSourceChanged, "the project's containers now come from %s, not %s", got, host)
	}
	for _, c := range containers {
		wd := path.Clean(filepath.ToSlash(c.Labels[labelWorkingDir]))
		if c.Labels[labelWorkingDir] == "" || wd == host {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type == "bind" && within(wd, m.Source) {
				return importRefusal(classImportNotRelocatable, "container %s binds %s, a host path named after the directory its "+
					"manager saw (%s), not the project's files at %s: that data would not be copied. Move it into the project "+
					"directory and redeploy it from its manager first", containerName(c), m.Source, wd, host)
			}
		}
	}
	return nil
}

func appendOnce(list []protocol.ComposeIssue, i protocol.ComposeIssue) []protocol.ComposeIssue {
	if slices.Contains(list, i) {
		return list
	}
	return append(list, i)
}

// outsideMount reports whether a bind source lies in the import mount
// holding src but outside src: a relative path leaving the project
// directory (users write host paths; only a relative path resolves
// below the agent's own mount point).
func outsideMount(res *storage.Result, src, source string) bool {
	for _, m := range res.Imports {
		if within(m.Path, filepath.ToSlash(src)) {
			return within(m.Path, source) && !within(src, source)
		}
	}
	return false
}

// checkSpace refuses a copy the stacks volume has no room for (with a 5%
// margin); an unmeasurable tree or filesystem is not refused here.
func checkSpace(ctx context.Context, src, stacksDir string) error {
	free := migration.FreeBytes(stacksDir)
	if free < 0 {
		return nil
	}
	fsys, err := migration.OSOpener(filepath.ToSlash(src))
	if err != nil {
		return importRefusal(classImportSourceUnavailable, "open %s: %v", src, err)
	}
	defer func() { _ = fsys.Close() }()
	_, bytes, _, truncated := migration.MeasureTree(ctx, fsys, migration.MaxEntries)
	if !truncated && bytes+bytes/20 > free {
		return importRefusal(classImportNoSpace, "the project directory holds %s but the stacks volume has %s free", humanBytes(bytes), humanBytes(free))
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (s *Service) importLifecycle(ctx context.Context, sc *jobexec.StepContext, in protocol.StackJobInput) lifecycle.Options {
	o := lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout}
	if sc != nil {
		o.Progress = func(service, msg string) { sc.Progress(ctx, -1, strings.TrimPrefix(service+": "+msg, ": ")) }
	}
	if in.TimeoutSeconds > 0 {
		d := time.Duration(in.TimeoutSeconds) * time.Second
		o.StopTimeout = &d
	}
	return o
}

// importStop stops the services that run (the compensation first) and
// makes sure nothing of the project runs any more.
func (s *Service) importStop(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importInput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	dst, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	o := stackOutput(sc)
	if o.Import.Live {
		return nil // Docker Manager's own project is copied while it runs
	}
	if own, err := s.ownProject(ctx, eng, in.Stack.ProjectName); err != nil {
		return err
	} else if own {
		return errOwnStop
	}
	if len(o.Import.WasRunning) > 0 {
		if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, importResume{Project: in.Stack.ProjectName,
			WasRunning: o.Import.WasRunning, NewDir: filepath.ToSlash(dst)}); err != nil {
			return err
		}
		list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
		if err != nil {
			return err
		}
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		sc.Progress(ctx, 10, "stopping "+strings.Join(o.Import.WasRunning, ", "))
		rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
		if _, err := lifecycle.Stop(ctx, g, rt, o.Import.WasRunning, s.importLifecycle(ctx, sc, in)); err != nil {
			return importRefusal(classImportShutdownFailed, "stopping the project failed: %v", err)
		}
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	for _, c := range list {
		if isRunning(c) {
			return importRefusal(classImportShutdownFailed, "container %s is still %s: the project is not copied while it runs", containerName(c), c.State)
		}
	}
	return nil
}

// countingFS counts the bytes read from regular files (copy progress).
type countingFS struct {
	migration.FS
	n *atomic.Int64
}

func (c countingFS) Open(name string) (io.ReadCloser, error) {
	f, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingReader{ReadCloser: f, n: c.n}, nil
}

type countingReader struct {
	io.ReadCloser
	n *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.n.Add(int64(n))
	return n, err
}

// importCopy copies the project directory into the stacks volume while
// the project is stopped: staging directory, verification, extended
// attributes, flush, rename.
func (s *Service) importCopy(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importInput(sc)
	if err != nil {
		return err
	}
	if stackOutput(sc).Import.Copied {
		return nil
	}
	src, res, err := s.importSource(in)
	if err != nil {
		return err
	}
	root, err := migration.OSOpener(res.StacksDir)
	if err != nil {
		return importRefusal(classImportCopyFailed, "open the stacks volume: %v", err)
	}
	defer func() { _ = root.Close() }()
	staging := importStagingPrefix + sc.JobID
	if !protocol.ValidRelativePath(staging) {
		return fmt.Errorf("invalid job ID %q", sc.JobID)
	}
	if err := root.RemoveAll(staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return importRefusal(classImportCopyFailed, "remove an earlier partial copy: %v", err)
	}
	if _, err := root.Lstat(in.Stack.Dir); err == nil {
		return importRefusal(classImportDirectoryExists, "the stacks volume already has a directory %s; nothing was overwritten", in.Stack.Dir)
	}
	if err := root.Mkdir(staging, 0o700); err != nil {
		return importRefusal(classImportCopyFailed, "create the staging directory: %v", err)
	}
	info, err := root.Lstat(staging)
	if err != nil {
		return importRefusal(classImportCopyFailed, "inspect the staging directory: %v", err)
	}
	if err := sc.AddCompensation(ctx, jobspec.CompRemoveImportCopy, importCopyRecord{Root: res.StacksDir, Staging: staging,
		Dir: in.Stack.Dir, Dev: info.Dev, Ino: info.Ino}); err != nil {
		return err
	}
	dstFS, err := root.Sub(staging)
	if err != nil {
		return importRefusal(classImportCopyFailed, "open the staging directory: %v", err)
	}
	defer func() { _ = dstFS.Close() }()
	srcFS, err := migration.OSOpener(filepath.ToSlash(src))
	if err != nil {
		return importRefusal(classImportSourceUnavailable, "open %s: %v", src, err)
	}
	defer func() { _ = srcFS.Close() }()

	_, total, _, _ := migration.MeasureTree(ctx, srcFS, migration.MaxEntries)
	var done atomic.Int64
	stop := s.copyProgress(ctx, sc, &done, total)
	limit := migration.FreeBytes(res.StacksDir)
	if limit < 0 {
		limit = 0
	}
	stats, err := migration.CopyTree(ctx, countingFS{FS: srcFS, n: &done}, dstFS, limit)
	stop()
	if err != nil {
		return importRefusal(classImportCopyFailed, "copying %s failed: %v", in.Import.WorkingDir, err)
	}
	sc.Progress(ctx, 70, "verifying the copy")
	if err := migration.VerifyTree(ctx, srcFS, dstFS); err != nil {
		return importRefusal(classImportCopyFailed, "the copy differs from the original: %v", err)
	}
	stagingDir := filepath.Join(filepath.FromSlash(res.StacksDir), staging)
	xs, err := migration.CopyXattrs(ctx, src, stagingDir)
	if err != nil {
		return importRefusal(classImportCopyFailed, "copying extended attributes failed: %v", err)
	}
	migration.Sync()
	if err := root.Rename(staging, in.Stack.Dir); err != nil {
		return importRefusal(classImportCopyFailed, "move the copy into place: %v", err)
	}
	migration.Sync()
	sc.Progress(ctx, 75, fmt.Sprintf("copied %d entries (%s)", stats.Entries, humanBytes(stats.Bytes)))
	return updateImport(ctx, sc, func(o *protocol.StackJobOutput, r *protocol.StackImportReport) {
		r.Copied, r.Entries, r.Bytes = true, stats.Entries, stats.Bytes
		r.Skipped, r.SkippedCount = stats.Skipped, stats.SkippedCount
		if stats.SkippedCount > 0 {
			o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: "copy_skipped",
				Message: fmt.Sprintf("%d sockets or device nodes were not copied (a program recreates sockets when it starts)", stats.SkippedCount)})
		}
		for _, f := range xs.Failed {
			o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: "xattr_not_copied",
				Message: "extended attribute not copied (the stacks volume refused it): " + f})
		}
	})
}

// copyProgress reports the copied bytes until the returned stop is called.
func (s *Service) copyProgress(ctx context.Context, sc *jobexec.StepContext, done *atomic.Int64, total int64) func() {
	quit := make(chan struct{})
	finished := make(chan struct{})
	t := s.opts.Clock.NewTicker(importProgressInterval)
	go func() {
		defer close(finished)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C():
				n := done.Load()
				pct := -1
				if total > 0 {
					pct = 15 + int(55*min(n, total)/total)
				}
				sc.Progress(ctx, pct, fmt.Sprintf("copying: %s of %s", humanBytes(n), humanBytes(total)))
			}
		}
	}()
	return func() {
		close(quit)
		<-finished
	}
}

// importRecreate switches the project to the copy: Compose recreates the
// containers of every service that had some, from the copy's exact bytes,
// without starting them.
func (s *Service) importRecreate(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importInput(sc)
	if err != nil {
		return err
	}
	o := stackOutput(sc)
	if !o.Import.Copied {
		return errors.New("the project directory was not copied")
	}
	c, err := s.composer()
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	dst, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	plain := protocol.StackJobInput{Stack: in.Stack}
	p, snap, err := s.loadSnapshot(ctx, c, plain, dst)
	if err != nil {
		return err
	}
	all, err := c.Load(ctx, specOf(allProfiles(in.Stack), dst))
	if err != nil {
		return err
	}
	if !o.Import.Switched {
		src, _, err := s.importSource(in)
		if err != nil {
			return err
		}
		orig, err := compose.LoadProject(ctx, specOf(allProfiles(in.Stack), src))
		if err != nil {
			return err
		}
		if err := relocated(orig, all, src, dst); err != nil {
			return importRefusal(classImportNotRelocatable, "%v", err)
		}
		// The switch: from here on the project lives in the copy.
		if err := updateImport(ctx, sc, func(_ *protocol.StackJobOutput, r *protocol.StackImportReport) { r.Switched = true }); err != nil {
			return err
		}
		if err := sc.ReleaseCompensation(ctx, jobspec.CompRemoveImportCopy); err != nil {
			return err
		}
	}
	had := map[string]bool{}
	for _, st := range o.Before {
		if st.Containers > 0 {
			had[st.Service] = true
		}
	}
	var services []string
	for _, svc := range all.Services {
		if had[svc.Name] && !o.Import.Live {
			services = append(services, svc.Name)
		}
	}
	if err := keepBuiltImages(ctx, eng, all, in.Stack.ProjectName, services); err != nil {
		return switchedFailure(err)
	}
	if len(services) > 0 {
		sc.Progress(ctx, 80, "recreating "+strings.Join(services, ", ")+" from the copy")
		var timeout *time.Duration
		if in.TimeoutSeconds > 0 {
			d := time.Duration(in.TimeoutSeconds) * time.Second
			timeout = &d
		}
		if err := c.Create(ctx, all, compose.CreateOptions{RunOptions: compose.RunOptions{Events: s.progress(ctx, sc)},
			Services: services, StopTimeout: timeout, ForceRecreate: true}); err != nil {
			return switchedFailure(err)
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
		o.Warnings = append(o.Warnings, warnings(p, bs)...)
	})
}

// keepBuiltImages tags the image each build-only service among services
// runs with Compose's name for it, so the recreated containers run the
// very same image (no rebuild; the tool that built it may have named it
// otherwise).
func keepBuiltImages(ctx context.Context, eng engine.Engine, p *compose.Project, project string, services []string) error {
	list, err := lifecycle.ProjectContainers(ctx, eng, project)
	if err != nil {
		return err
	}
	for _, svc := range services {
		exp, ok := p.Expected(svc)
		if !ok || !exp.Built {
			continue
		}
		i := slices.IndexFunc(list, func(c engine.Container) bool {
			return c.Labels[lifecycle.ComposeServiceLabel] == svc && c.ImageID != ""
		})
		if i < 0 {
			continue
		}
		if img, err := eng.InspectImage(ctx, exp.Image); err == nil && img.ID == list[i].ImageID {
			continue
		} else if err != nil && !engine.IsCode(err, engine.CodeNotFound) {
			return err
		}
		if err := eng.TagImage(ctx, list[i].ImageID, exp.Image); err != nil {
			return fmt.Errorf("tag the image service %s runs as %s: %w", svc, exp.Image, err)
		}
	}
	return nil
}

// relocated checks that every bind source of the copy is the original's
// moved along with the project directory (inside it) or the same absolute
// path (outside it).
func relocated(orig, moved *compose.Project, src, dst string) error {
	type key struct{ service, target string }
	at := map[key]string{}
	for _, b := range moved.Binds {
		at[key{b.Service, b.Target}] = filepath.ToSlash(b.Source)
	}
	srcSlash, dstSlash := filepath.ToSlash(src), filepath.ToSlash(dst)
	for _, b := range orig.Binds {
		was := filepath.ToSlash(b.Source)
		want := was
		if within(srcSlash, was) {
			want = path.Join(dstSlash, strings.TrimPrefix(strings.TrimPrefix(was, srcSlash), "/"))
		}
		got, ok := at[key{b.Service, b.Target}]
		if !ok {
			return fmt.Errorf("service %s lost its bind mount at %s in the copy", b.Service, b.Target)
		}
		if got != want {
			return fmt.Errorf("service %s binds %s through a relative path outside the project directory: from the copy it "+
				"would be %s. Move the data into the project directory, or use an absolute host path", b.Service, was, got)
		}
	}
	return nil
}

// importStartSet splits the services that ran before into those to start
// and those kept stopped: once the project uses the copy (a container
// runs from newDir), a service is started only when all its containers do.
func importStartSet(list []engine.Container, wasRunning []string, newDir string) (start, kept []string) {
	newDir = path.Clean(newDir)
	switched := false
	dirs := map[string][]string{}
	for _, c := range list {
		wd := path.Clean(filepath.ToSlash(c.Labels[labelWorkingDir]))
		switched = switched || wd == newDir
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		dirs[svc] = append(dirs[svc], wd)
	}
	if !switched {
		return slices.Clone(wasRunning), nil
	}
	for _, svc := range wasRunning {
		ds := dirs[svc]
		if len(ds) > 0 && !slices.ContainsFunc(ds, func(d string) bool { return d != newDir }) {
			start = append(start, svc)
		} else {
			kept = append(kept, svc)
		}
	}
	return start, kept
}

// importStart starts exactly the services that ran before (dependencies
// first), never one that still uses the original directory.
func (s *Service) importStart(ctx context.Context, sc *jobexec.StepContext) error {
	in, err := importInput(sc)
	if err != nil {
		return err
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	dst, err := s.resolve(in.Stack)
	if err != nil {
		return err
	}
	o := stackOutput(sc)
	if o.Import.Live {
		return s.importLiveDone(ctx, sc, eng, in, o)
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	start, kept := importStartSet(list, o.Import.WasRunning, filepath.ToSlash(dst))
	var rerr error
	if len(start) > 0 {
		g, err := lifecycle.GraphFromContainers(list)
		if err != nil {
			return err
		}
		sc.Progress(ctx, 90, "starting "+strings.Join(start, ", "))
		rt := lifecycle.EngineRuntime{Engine: eng, Project: in.Stack.ProjectName, Clock: s.opts.Clock}
		rep, err := lifecycle.Resume(ctx, g, rt, start, s.importLifecycle(ctx, sc, in))
		for _, svc := range rep.Started {
			sc.Item(ctx, svc, domain.ItemSucceeded, "started from the copy")
		}
		rerr = err
	}
	for _, svc := range kept {
		sc.Item(ctx, svc, domain.ItemSkipped, "kept stopped: its containers still use the original directory")
	}
	after, aerr := serviceStates(ctx, eng, in.Stack.ProjectName)
	if uerr := update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.After = after
		for _, svc := range kept {
			o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: "kept_stopped", Service: svc,
				Message: fmt.Sprintf("service %s was not started: its containers still use the original directory", svc)})
		}
	}); uerr != nil && rerr == nil {
		rerr = uerr
	}
	if rerr != nil {
		return switchedFailure(rerr)
	}
	if aerr != nil {
		return aerr
	}
	return sc.ReleaseCompensation(ctx, jobspec.CompStartContainers)
}

// importLiveDone ends the import of Docker Manager's own project: nothing
// stopped, so nothing starts; its containers keep running from the
// original directory until the stack's next deploy.
func (s *Service) importLiveDone(ctx context.Context, sc *jobexec.StepContext, eng engine.Engine, in protocol.StackJobInput,
	o protocol.StackJobOutput) error {
	for _, svc := range o.Import.WasRunning {
		sc.Item(ctx, svc, domain.ItemSkipped, "kept running from the original directory until the next deploy")
	}
	after, err := serviceStates(ctx, eng, in.Stack.ProjectName)
	if err != nil {
		return err
	}
	return update(ctx, sc, func(o *protocol.StackJobOutput) {
		o.After = after
		o.Warnings = append(o.Warnings, protocol.ComposeIssue{Code: "kept_running",
			Message: "Docker Manager's own project keeps running from its original directory; deploy the stack to move it onto the copy"})
	})
}

// importCompStart starts what ran before the import again (compensation):
// from the original directory before the switch, afterwards only services
// already recreated from the copy.
func (s *Service) importCompStart(ctx context.Context, args json.RawMessage) error {
	var a importResume
	if err := json.Unmarshal(args, &a); err != nil {
		return err
	}
	if !protocol.ValidProjectName(a.Project) {
		return fmt.Errorf("invalid project %q", a.Project)
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	list, err := lifecycle.ProjectContainers(ctx, eng, a.Project)
	if err != nil {
		return err
	}
	start, kept := importStartSet(list, a.WasRunning, a.NewDir)
	if len(kept) > 0 {
		s.log.Warn("import: services kept stopped, their containers still use the original directory", "project", a.Project, "services", kept)
	}
	if len(start) == 0 {
		return nil
	}
	g, err := lifecycle.GraphFromContainers(list)
	if err != nil {
		return err
	}
	rt := lifecycle.EngineRuntime{Engine: eng, Project: a.Project, Clock: s.opts.Clock}
	_, err = lifecycle.Resume(ctx, g, rt, start, lifecycle.Options{Clock: s.opts.Clock, WaitTimeout: s.opts.WaitTimeout})
	return err
}

// importCompRemove removes the copy while the project does not use it
// (compensation): the staging directory, and the renamed copy only when
// it is still the directory this job created.
func (s *Service) importCompRemove(_ context.Context, args json.RawMessage) error {
	var a importCopyRecord
	if err := json.Unmarshal(args, &a); err != nil {
		return err
	}
	if !strings.HasPrefix(a.Staging, importStagingPrefix) || !protocol.ValidRelativePath(a.Staging) ||
		!protocol.ValidProjectName(a.Dir) {
		return fmt.Errorf("invalid copy record %q/%q", a.Staging, a.Dir)
	}
	root, err := migration.OSOpener(a.Root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(a.Staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	info, err := root.Lstat(a.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode.IsDir() || a.Ino == 0 || info.Dev != a.Dev || info.Ino != a.Ino {
		return nil // not the copy this job made
	}
	return root.RemoveAll(a.Dir)
}
