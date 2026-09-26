package resources

import (
	"context"
	"encoding/json"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/permissions"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// The resource graph for authorization (#17). Containers, volumes and
// networks of a Compose project that is a Docker Manager stack (#7) live in that
// stack (containers also in its service), so stack-scoped rules apply to
// them. Handlers pass the parents they know from the agent's answer
// (Parents); the Locators serve checks without one — the job engine's
// request/dispatch checks of container, volume and network targets — from
// the stack membership last seen in this environment. Locators never call
// the agent.

// maxCache bounds the membership cache (entries are small; the v1 scale
// target is 1000 containers in total).
const maxCache = 20000

func (s *Service) remember(typ, env, name string, st *protocol.StackRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cacheKey{typ, env, name}
	if st == nil {
		delete(s.stack, k)
		return
	}
	if len(s.stack) >= maxCache {
		clear(s.stack)
	}
	s.stack[k] = *st
}

// Parents returns the authorization parents of a container, volume or
// network with Compose membership st: its service and stack when the
// project is a Docker Manager stack, none otherwise (never nil, so no Locator is
// consulted).
func (s *Service) Parents(ctx context.Context, typ, env string, st *protocol.StackRef) []authz.ResourceRef {
	out := []authz.ResourceRef{}
	if st == nil {
		return out
	}
	stackID, ok := s.StackIDs(ctx, env)[st.Project]
	if !ok {
		return out
	}
	if typ == catalog.TypeContainer && st.Service != "" {
		out = append(out, authz.ResourceRef{Type: catalog.TypeService, ID: authz.ServiceID(stackID, st.Service)})
	}
	return append(out, authz.ResourceRef{Type: catalog.TypeStack, ID: stackID})
}

// Locator returns the permission Locator of a Docker resource type
// (container, volume or network).
func (s *Service) Locator(typ string) permissions.Locator {
	return permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		if ref.EnvironmentID == "" {
			return permissions.Location{}, nil
		}
		s.mu.Lock()
		st, ok := s.stack[cacheKey{typ, ref.EnvironmentID, ref.ID}]
		s.mu.Unlock()
		if !ok {
			// Unknown here: the built-in rule places it in its environment
			// without parents.
			return permissions.Location{}, nil
		}
		return permissions.Location{Found: true, EnvironmentID: ref.EnvironmentID, Parents: s.Parents(ctx, typ, ref.EnvironmentID, &st)}, nil
	})
}

// Reconcile runs after an environment's agent (re)connected (agents
// reconciler): it refreshes the stack membership cache from the container
// list and drops recreate specifications whose container no longer exists
// and whose create job has ended. It never fails the reconnect: an agent
// that cannot list containers (Engine down, older agent) is skipped.
func (s *Service) Reconcile(ctx context.Context, env string, list func(ctx context.Context) ([]protocol.ContainerSummary, error)) {
	cs, err := list(ctx)
	if err != nil {
		s.log.Debug("container inventory not available after reconnect", "environment_id", env, "error", err)
		return
	}
	labels := map[string]bool{}
	for _, c := range cs {
		s.remember(catalog.TypeContainer, env, c.Name, c.Stack)
		if id := c.Labels[protocol.LabelSpec]; id != "" {
			labels[id] = true
		}
	}
	specs, err := store.ListManagedContainers(ctx, s.opts.DB, env)
	if err != nil {
		s.log.Warn("could not list recreate specifications", "environment_id", env, "error", err)
		return
	}
	for _, m := range specs {
		if labels[m.ID] {
			continue
		}
		if m.CreateJobID != "" {
			j, err := s.opts.Jobs.Get(ctx, m.CreateJobID)
			if err == nil && !j.State.Terminal() {
				continue // still being created
			}
		} else if s.clk.Now().Sub(m.CreatedAt) < time.Minute {
			continue // its job is being enqueued right now
		}
		if err := store.DeleteManagedContainer(ctx, s.opts.DB, m.ID); err != nil {
			s.log.Warn("could not drop a stale recreate specification", "spec_id", m.ID, "error", err)
		}
	}
}

// ReconcileSession is the agents.Reconciler of the service.
func (s *Service) ReconcileSession(ctx context.Context, env string, req func(ctx context.Context, name string, input any) ([]byte, error)) {
	s.Reconcile(ctx, env, func(ctx context.Context) ([]protocol.ContainerSummary, error) {
		raw, err := req(ctx, protocol.ReqContainerList, protocol.ContainerListInput{})
		if err != nil {
			return nil, err
		}
		var out protocol.ContainerListOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out.Containers, nil
	})
}
