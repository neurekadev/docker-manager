package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"slices"
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
	_ files.WatchedStacks  = (*Service)(nil)
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

// HostPath is the stack's project directory on the host (#22: the stack
// header shows the logical location "environment · stack" with this path
// on hover). It needs the agent's last reported stacks root for stacks in
// the stacks volume.
func (s *Service) HostPath(ctx context.Context, stackID string) (string, error) {
	r, err := s.StackFileRoot(ctx, stackID)
	if err != nil {
		return "", err
	}
	return r.Dir, nil
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
		if r.Kind == protocol.RootStacks && protocol.IsAbsHostPath(r.Path) {
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

// WatchScopes returns the file scopes of an environment's stacks for the
// agent's watch set (#23): every stack whose project directory resolves
// (stacks in the stacks volume need the agent's verified stacks root).
func (s *Service) WatchScopes(ctx context.Context, environmentID string) ([]protocol.FileScope, error) {
	list, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: environmentID})
	if err != nil {
		return nil, err
	}
	var stacksRoot string
	var out []protocol.FileScope
	for _, st := range list {
		root := st.RootPath
		if st.Root == domain.StackRootStacks {
			if stacksRoot == "" {
				if stacksRoot, err = s.stacksDir(ctx, environmentID); err != nil {
					return nil, nil // no verified stacks root yet: nothing to watch
				}
			}
			root = stacksRoot
		}
		if root == "" {
			continue
		}
		out = append(out, protocol.FileScope{Kind: protocol.ScopeStack, ID: st.ID, Dir: path.Join(root, st.Dir)})
	}
	return out, nil
}

// ExternalChange is the file watcher's hook (#23, #25 Q1): when changed
// paths reported for a stack's project directory touch its definition
// (a Compose, override or env file, a directory holding one, or the whole
// scope), the definition is read from disk and recorded as a revision
// (source external) when it differs, which marks undeployed changes. It
// never deploys. It returns the new revision, or nil.
func (s *Service) ExternalChange(ctx context.Context, stackID string, paths []string, overflow bool) (*domain.StackRevision, error) {
	root, err := s.Root(ctx, stackID)
	if err != nil {
		return nil, err
	}
	st, err := store.GetStack(ctx, s.db, stackID)
	if err != nil {
		return nil, err
	}
	if !overflow && !touchesDefinition(st, root.DefinitionFiles, paths) {
		return nil, nil
	}
	return s.RecordObserved(ctx, stackID, domain.RevisionExternal, authz.Service())
}

// touchesDefinition reports whether a changed path is a definition file,
// or a directory (reconciliation reports directories) that holds one.
func touchesDefinition(st domain.Stack, known []string, paths []string) bool {
	defs := append(append(append(slices.Clone(known), definitionNames...), st.ConfigFiles...), st.EnvFiles...)
	for _, p := range paths {
		if p == "." || IsDefinitionFile(st, known, p) {
			return true
		}
		for _, d := range defs {
			if path.Dir(d) == p {
				return true
			}
		}
	}
	return false
}
