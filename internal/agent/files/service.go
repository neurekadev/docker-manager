// Package files is the agent's scoped file service (#15): the shared file
// tree service (internal/fsroot) over the two agent roots, a stack's
// project directory (in a verified stack root, #28) or a local Docker
// volume's data directory (storage.Result.AccessFor; non-local drivers and
// Docker Manager's own volumes are refused), plus the session wiring: the
// files.* requests, the files.download/files.upload streams and the files.*
// job executors. Containment and the operations themselves are described
// in internal/fsroot; stack scopes follow no symlink at all
// (fsroot.Options.NoFollow).
//
// File contents and names are never logged.
package files

import (
	"context"
	"log/slog"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/fsroot"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Engine is the part of the Engine adapter the service needs.
type Engine interface {
	InspectVolume(ctx context.Context, name string) (engine.Volume, error)
	ListContainers(ctx context.Context, f engine.ContainerFilter) ([]engine.Container, error)
}

// Limits bound the service's work (fsroot.Limits).
type Limits = fsroot.Limits

// Options configures the service.
type Options struct {
	// Engine returns the connected Engine (nil while disconnected).
	Engine func() Engine
	// Storage returns the #28 layout check (nil before it ran).
	Storage func() *storage.Result
	Clock   clock.Clock
	Logger  *slog.Logger
	Limits  Limits
	// Invalidate is called with the paths Docker Manager changed in a scope, so
	// other open views refresh (the runtime relays it as fs_invalidation,
	// #23). Must not block.
	Invalidate func(protocol.FSInvalidationPayload)
}

// Service serves scoped file operations on the agent's stack and volume
// roots.
type Service struct {
	*fsroot.Service
	opts Options
}

// New returns the service.
func New(o Options) *Service {
	s := &Service{opts: o}
	s.Service = fsroot.New(fsroot.Options{
		Resolve: s.resolve, Clock: o.Clock, Logger: o.Logger, Limits: o.Limits,
		Kinds: fsroot.AgentKinds, Invalidate: o.Invalidate,
		// No symlink is followed in a stack's project directory: an in-root
		// link would give a Compose source a second name and bypass the
		// manager's stack.definition.* checks (protocol.FeatureStackFilesNoFollow).
		NoFollow: []string{protocol.ScopeStack},
	})
	return s
}

var fail = fsroot.Fail

// resolve returns the verified root directory of a stack or volume scope.
func (s *Service) resolve(ctx context.Context, scope protocol.FileScope) (string, error) {
	st := (*storage.Result)(nil)
	if s.opts.Storage != nil {
		st = s.opts.Storage()
	}
	if st == nil {
		return "", fail(protocol.CodeUnsupportedVolume, "the storage layout has not been verified yet")
	}
	var dir string
	switch scope.Kind {
	case protocol.ScopeStack:
		if err := st.Allows(scope.Dir); err != nil {
			return "", fail(protocol.CodeForbiddenPath, "the stack's project directory is not in a verified stack root")
		}
		dir = scope.Dir
	case protocol.ScopeVolume:
		d, err := s.volumeDir(ctx, st, scope.ID)
		if err != nil {
			return "", err
		}
		dir = d
	default:
		return "", fail(protocol.CodeInvalidFrame, "the agent serves stack and volume scopes only")
	}
	local := filepath.FromSlash(dir)
	resolved, err := filepath.EvalSymlinks(local)
	if err != nil {
		return "", fail(protocol.CodeNotFound, "the %s's directory does not exist", scope.Kind)
	}
	// The directory itself (not only the given name) must be inside a
	// verified root: a project directory that is a symlink elsewhere is
	// refused.
	switch scope.Kind {
	case protocol.ScopeStack:
		if st.Allows(filepath.ToSlash(resolved)) != nil {
			return "", fail(protocol.CodeForbiddenPath, "the stack's project directory resolves outside the verified stack roots")
		}
	case protocol.ScopeVolume:
		if !within(filepath.ToSlash(resolved), st.VolumesDir) && !within(filepath.ToSlash(resolved), evalOr(st.VolumesDir)) {
			return "", fail(protocol.CodeUnsupportedVolume, "the volume's data directory resolves outside Docker's volume directory")
		}
	}
	return resolved, nil
}

func evalOr(p string) string {
	r, err := filepath.EvalSymlinks(filepath.FromSlash(p))
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

// within reports whether p is root or below it (slash paths).
func within(p, root string) bool {
	p, root = path.Clean(p), path.Clean(root)
	return root != "." && (p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/"))
}

// volumeDir returns the data directory of a supported local volume.
func (s *Service) volumeDir(ctx context.Context, st *storage.Result, name string) (string, error) {
	var eng Engine
	if s.opts.Engine != nil {
		eng = s.opts.Engine()
	}
	if eng == nil {
		return "", fail(protocol.CodeEngineUnavailable, "the Docker Engine is not connected")
	}
	v, err := eng.InspectVolume(ctx, name)
	if err != nil {
		if engine.IsCode(err, engine.CodeNotFound) {
			return "", fail(protocol.CodeNotFound, "volume %s does not exist", name)
		}
		return "", fail(protocol.CodeEngineUnavailable, "cannot inspect the volume")
	}
	if acc := st.AccessFor(v); !acc.Supported {
		return "", fail(protocol.CodeUnsupportedVolume, "%s", acc.Reason)
	}
	mp := path.Clean(filepath.ToSlash(v.Mountpoint))
	if st.StacksDir != "" && (within(mp, st.StacksDir) || within(st.StacksDir, mp)) {
		return "", fail(protocol.CodeUnsupportedVolume, "the stacks volume is browsed per stack (stack files), not as a volume")
	}
	protected, err := s.protectedVolumes(ctx, eng)
	if err != nil {
		return "", fail(protocol.CodeEngineUnavailable, "cannot list Docker Manager's own containers")
	}
	if slices.Contains(protected, v.Name) {
		return "", fail(protocol.CodeUnsupportedVolume, "volume %s holds Docker Manager's own data and is not served by the file manager", name)
	}
	return mp, nil
}

// protectedVolumes lists the volumes mounted by Docker Manager's own
// containers (manager data, agent state): containers carrying
// protocol.LabelRole, under its current or legacy key.
func (s *Service) protectedVolumes(ctx context.Context, eng Engine) ([]string, error) {
	cs, err := eng.ListContainers(ctx, engine.ContainerFilter{All: true})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cs {
		if _, ours := protocol.LookupLabel(c.Labels, protocol.LabelRole); !ours {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				out = append(out, m.Name)
			}
		}
	}
	return out, nil
}
