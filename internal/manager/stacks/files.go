package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/files"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// The file manager's hooks (#15): stack scopes resolve to the stack's
// project directory, and saves of definition files record revisions.

// Systems reads an environment's agent and its last reported capabilities
// (*agents.Service implements it).
type Systems interface {
	EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error)
}

var (
	_ files.StackRoots     = (*Service)(nil)
	_ files.SourceObserver = (*Service)(nil)
)

// StackFileRoot resolves a stack's project directory to its absolute host
// path (identical inside the agent, #28): below the stacks volume's
// mountpoint the agent reported, or below the registered root.
func (s *Service) StackFileRoot(ctx context.Context, stackID string) (files.StackRoot, error) {
	st, err := store.GetStack(ctx, s.db, stackID)
	if errors.Is(err, domain.ErrStackNotFound) {
		return files.StackRoot{}, domain.ErrFileScopeNotFound
	}
	if err != nil {
		return files.StackRoot{}, err
	}
	root := st.RootPath
	if st.Root == domain.StackRootStacks {
		if root, err = s.stacksDir(ctx, st.EnvironmentID); err != nil {
			return files.StackRoot{}, err
		}
	}
	return files.StackRoot{EnvironmentID: st.EnvironmentID, Dir: path.Join(root, st.Dir)}, nil
}

// errNoStacksRoot: the environment's agent has not reported a verified
// stacks volume.
var errNoStacksRoot = &domain.StackError{Code: domain.StackErrRootUnavailable,
	Message: "the environment's agent has not reported a verified stacks volume (#28)"}

// stacksDir returns the stacks volume's host path from the environment's
// agent capabilities.
func (s *Service) stacksDir(ctx context.Context, environmentID string) (string, error) {
	if s.opts.Systems == nil {
		return "", errNoStacksRoot
	}
	sys, err := s.opts.Systems.EnvironmentSystem(ctx, environmentID)
	if err != nil {
		return "", err
	}
	if sys.Agent == nil || sys.Agent.Capabilities == "" {
		return "", errNoStacksRoot
	}
	var caps protocol.CapabilitiesPayload
	if err := json.Unmarshal([]byte(sys.Agent.Capabilities), &caps); err != nil {
		return "", errNoStacksRoot
	}
	for _, r := range caps.Roots {
		if r.Kind == protocol.RootStacks && path.IsAbs(r.Path) {
			return path.Clean(r.Path), nil
		}
	}
	return "", errNoStacksRoot
}

// sourceObserveTimeout bounds the background recording of a file-manager
// save.
const sourceObserveTimeout = time.Minute

// StackSourcesChanged records a revision (source file_manager) after the
// file manager changed definition files of a stack (#15). It returns at
// once; the definition is read from the agent in the background. It never
// deploys.
func (s *Service) StackSourcesChanged(ctx context.Context, stackID string, paths []string) {
	author, ok := authz.PrincipalFrom(ctx)
	if !ok {
		author = authz.Service()
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceObserveTimeout)
	go func() {
		defer cancel()
		if _, err := s.RecordObserved(bg, stackID, domain.RevisionFileManager, author); err != nil &&
			!errors.Is(err, domain.ErrStackNotFound) {
			s.log.Warn("could not record the stack's definition after a file-manager save", "stack_id", stackID,
				"paths", len(paths), "error", err)
		}
	}()
}
