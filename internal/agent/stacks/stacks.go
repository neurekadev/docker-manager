// Package stacks is the agent side of Compose stacks (#7): the compose.*
// requests (validate, read, write, discover, services) and the executors of
// the stack.* job kinds (deploy, start, stop, restart, down, remove).
//
// Projects are addressed by protocol.ProjectRef and resolved against the
// verified stack roots of the #28 storage check; nothing outside them is
// read, written or deployed. Deploys always use the current on-disk bytes
// and report them (protocol.StackJobOutput.Sources) so the manager records
// the applied revision (#25 Q1); the agent never modifies definition files
// except through compose.write (stack creation and explicit revision
// restores requested by the manager). Start, stop and restart go through
// the shared dependency-aware lifecycle (internal/agent/lifecycle) on the
// deployed containers; deploy and down through the Compose SDK.
package stacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Composer is the subset of the Compose adapter the stack executors use
// (*compose.Adapter implements it; tests fake it).
type Composer interface {
	Load(ctx context.Context, spec compose.ProjectSpec) (*compose.Project, error)
	Up(ctx context.Context, p *compose.Project, o compose.UpOptions) error
	Pull(ctx context.Context, p *compose.Project, o compose.RunOptions) error
	Build(ctx context.Context, p *compose.Project, o compose.BuildOptions) error
	Down(ctx context.Context, name string, p *compose.Project, o compose.DownOptions) error
}

// Deps are the agent's live components; each may be nil while the Engine
// is not connected (requests and jobs then fail with engine_unavailable).
type Deps interface {
	Composer() Composer
	Engine() engine.Engine
	Storage() *storage.Result
}

// Options configures a Service.
type Options struct {
	Deps   Deps
	Clock  clock.Clock
	Logger *slog.Logger
	// WaitTimeout bounds each dependency-condition wait of start/restart
	// (default lifecycle.DefaultWaitTimeout).
	WaitTimeout time.Duration
	// BuildCancelPoll is how often a running build checks for cancellation
	// (default buildrun.DefaultCancelPoll).
	BuildCancelPoll time.Duration
}

// Service serves the compose.* requests and runs the stack.* jobs.
type Service struct {
	opts Options
	log  *slog.Logger
}

// New returns a Service.
func New(opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Service{opts: opts, log: opts.Logger}
}

// Requests returns the request handlers (runtime.Options.Requests).
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqComposeValidate: s.validate,
		protocol.ReqComposeRead:     s.read,
		protocol.ReqComposeWrite:    s.write,
		protocol.ReqComposeDiscover: s.discover,
		protocol.ReqComposeServices: s.services,
	}
}

var errUnavailable = &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: "the Docker Engine is not connected", Retryable: true}

func (s *Service) engine() (engine.Engine, error) {
	if s.opts.Deps == nil || s.opts.Deps.Engine() == nil {
		return nil, errUnavailable
	}
	return s.opts.Deps.Engine(), nil
}

func (s *Service) composer() (Composer, error) {
	if s.opts.Deps == nil || s.opts.Deps.Composer() == nil {
		return nil, errUnavailable
	}
	return s.opts.Deps.Composer(), nil
}

// resolve maps a stack reference to its absolute project directory inside a
// verified stack root. It refuses unverified roots, escapes and symlinks
// leading out of the root.
func (s *Service) resolve(ref protocol.ProjectRef) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
	}
	var res *storage.Result
	if s.opts.Deps != nil {
		res = s.opts.Deps.Storage()
	}
	if res == nil {
		return "", &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: "the storage layout has not been verified yet"}
	}
	var root string
	for _, r := range res.Roots {
		if !r.OK {
			continue
		}
		if (ref.Root == protocol.RootStacks && r.Kind == storage.KindStacks) ||
			(ref.Root == protocol.RootBind && r.Kind == storage.KindBind && r.Path == ref.RootPath) {
			root = r.Path
			break
		}
	}
	if root == "" {
		return "", &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: "the stack root is not a verified stack root of this agent"}
	}
	rootDir := filepath.FromSlash(root)
	dir := filepath.Join(rootDir, filepath.FromSlash(ref.Dir))
	if err := res.Allows(dir); err != nil {
		return "", &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: err.Error()}
	}
	if err := beneath(rootDir, dir); err != nil {
		return "", &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: err.Error()}
	}
	return dir, nil
}

// beneath refuses a directory that exists but resolves (through symlinks)
// outside root.
func beneath(root, dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve stack root: %w", err)
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("the project directory resolves outside its stack root")
	}
	return nil
}

func specOf(ref protocol.ProjectRef, dir string) compose.ProjectSpec {
	return compose.ProjectSpec{Name: ref.ProjectName, Dir: dir, ConfigFiles: ref.ConfigFiles, EnvFiles: ref.EnvFiles, Profiles: ref.Profiles}
}

// handlerError maps adapter errors to protocol error codes.
func handlerError(err error) error {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he
	}
	switch engine.CodeOf(err) {
	case engine.CodeNotFound:
		return &session.HandlerError{Code: protocol.CodeNotFound, Message: err.Error()}
	case engine.CodeConflict:
		return &session.HandlerError{Code: protocol.CodeConflict, Message: err.Error()}
	case engine.CodeEngineUnavailable:
		return &session.HandlerError{Code: protocol.CodeEngineUnavailable, Message: err.Error(), Retryable: true}
	case engine.CodeTimeout:
		return &session.HandlerError{Code: protocol.CodeDeadlineExceeded, Message: err.Error()}
	case engine.CodeCanceled:
		return &session.HandlerError{Code: protocol.CodeCancelled, Message: err.Error()}
	}
	if strings.HasPrefix(string(engine.CodeOf(err)), "storage_") {
		return &session.HandlerError{Code: protocol.CodeForbiddenPath, Message: err.Error()}
	}
	return &session.HandlerError{Code: protocol.CodeEngineError, Message: err.Error()}
}

// definitionFiles lists a project's definition files (absolute paths
// inside dir, sorted) and warnings for definition files outside it. When
// the project does not load (broken YAML), it falls back to the Compose
// files and env files found by name, so a broken definition can still be
// read and recorded.
func definitionFiles(ctx context.Context, ref protocol.ProjectRef, dir string) ([]string, []protocol.ComposeIssue) {
	var files []string
	var issues []protocol.ComposeIssue
	if p, err := compose.LoadProject(ctx, specOf(ref, dir)); err == nil {
		files = p.DefinitionFiles
	} else {
		for _, name := range append(slices.Clone(compose.DefaultConfigFiles), "compose.override.yaml", "compose.override.yml",
			"docker-compose.override.yaml", "docker-compose.override.yml", ".env") {
			files = append(files, filepath.Join(dir, name))
		}
		for _, f := range append(slices.Clone(ref.ConfigFiles), ref.EnvFiles...) {
			files = append(files, filepath.Join(dir, filepath.FromSlash(f)))
		}
	}
	var out []string
	for _, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			issues = append(issues, protocol.ComposeIssue{Code: protocol.IssueBindOutsideProject,
				Message: fmt.Sprintf("definition file %s is outside the project directory and is not part of stack revisions or backups", f)})
			continue
		}
		out = append(out, f)
	}
	slices.Sort(out)
	return slices.Compact(out), issues
}

// errTooLarge marks a definition above the protocol bounds.
var errTooLarge = errors.New("definition too large")

// readSources reads the definition files that exist (inside dir) into a
// snapshot. Files are read with a size bound and never through a symlink
// that leaves dir.
func readSources(dir string, files []string) (protocol.SourceSnapshot, error) {
	var out []protocol.SourceFile
	total := 0
	for _, f := range files {
		st, err := os.Lstat(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return protocol.SourceSnapshot{}, err
		}
		if st.IsDir() {
			continue
		}
		// Symlinked files or directories must not lead out of the project.
		if err := beneath(dir, f); err != nil {
			return protocol.SourceSnapshot{}, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		b, err := readBounded(f, protocol.MaxSourceFile)
		if err != nil {
			return protocol.SourceSnapshot{}, err
		}
		if total += len(b); total > protocol.MaxSourceTotal || len(out) >= protocol.MaxSourceFiles {
			return protocol.SourceSnapshot{}, fmt.Errorf("%w: more than %d bytes or %d files", errTooLarge, protocol.MaxSourceTotal, protocol.MaxSourceFiles)
		}
		rel, _ := filepath.Rel(dir, f)
		out = append(out, protocol.SourceFile{Path: filepath.ToSlash(rel), Content: b})
	}
	return protocol.NewSourceSnapshot(out), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a definition file inside the resolved project directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", errTooLarge, filepath.Base(path), limit)
	}
	return b, nil
}

// binds converts the project's binds, relative to dir.
func binds(p *compose.Project, dir string) []protocol.ComposeBind {
	out := []protocol.ComposeBind{}
	for _, b := range p.Binds {
		cb := protocol.ComposeBind{Service: b.Service, Source: filepath.ToSlash(b.Source), Target: b.Target, ReadOnly: b.ReadOnly}
		rel, err := filepath.Rel(dir, b.Source)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			cb.RelPath = filepath.ToSlash(rel)
		} else {
			cb.External = true
		}
		out = append(out, cb)
	}
	return out
}

// serviceInfos converts the project's services.
func serviceInfos(p *compose.Project) []protocol.ComposeService {
	out := make([]protocol.ComposeService, 0, len(p.Services))
	for _, s := range p.Services {
		cs := protocol.ComposeService{Name: s.Name, Image: s.Image, Build: s.Build, Profiles: s.Profiles,
			Description: s.Description, Icon: s.Icon}
		for _, d := range s.DependsOn {
			cs.DependsOn = append(cs.DependsOn, protocol.ComposeDependency{Service: d.Service, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
		}
		out = append(out, cs)
	}
	return out
}

// warnings converts load warnings and flags external binds (#10).
func warnings(p *compose.Project, bs []protocol.ComposeBind) []protocol.ComposeIssue {
	out := []protocol.ComposeIssue{}
	for _, w := range p.Warnings {
		code := protocol.IssueInvalidProject
		if strings.Contains(w, compose.ObsoleteVersionWarning) {
			code = protocol.IssueObsoleteVersion
		}
		out = append(out, protocol.ComposeIssue{Code: code, Message: w})
	}
	for _, b := range bs {
		if b.External {
			out = append(out, protocol.ComposeIssue{Code: protocol.IssueBindOutsideProject, Service: b.Service,
				Message: fmt.Sprintf("service %s binds %s from outside the project directory: stack backups include it only with an explicit opt-in", b.Service, b.Source)})
		}
	}
	return out
}

// serviceStates summarizes the project's containers per service.
func serviceStates(ctx context.Context, eng engine.Engine, project string) ([]protocol.ServiceState, error) {
	list, err := lifecycle.ProjectContainers(ctx, eng, project)
	if err != nil {
		return nil, err
	}
	by := map[string]*protocol.ServiceState{}
	var names []string
	for _, c := range list {
		svc := c.Labels[lifecycle.ComposeServiceLabel]
		st, ok := by[svc]
		if !ok {
			st = &protocol.ServiceState{Service: svc}
			by[svc] = st
			names = append(names, svc)
		}
		st.Containers++
		if c.State == "running" {
			st.Running++
		}
		if c.ImageID != "" && !slices.Contains(st.ImageIDs, c.ImageID) {
			st.ImageIDs = append(st.ImageIDs, c.ImageID)
		}
	}
	slices.Sort(names)
	out := make([]protocol.ServiceState, 0, len(names))
	for _, n := range names {
		out = append(out, *by[n])
	}
	return out, nil
}
