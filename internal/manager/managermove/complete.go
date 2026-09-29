package managermove

import (
	"context"
	"errors"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// "Move complete" (new manager): which agents the old manager told the
// new address and which still need it by hand, and the old server's
// environment with its stopped copies to remove and to archive.

// Complete is the arrived move's "Move complete" data.
type Complete struct {
	// Redirects are the agents the old manager could place, with whether
	// they reached this manager since.
	Redirects []RedirectStatus
	// OldEnvironment is the old server's environment ("nil" when the old
	// manager had none next to it).
	OldEnvironment *OldEnvironment
}

// RedirectStatus is one redirect of the move and where its agent stands.
type RedirectStatus struct {
	domain.ManagerMoveRedirect
	// Connected: the agent is online here or connected since the arrival.
	Connected bool
	// NeedsFix: it did not get the new address and has not connected
	// since: its DOCKER_AGENT_MANAGER_URL must be changed by hand (URL).
	NeedsFix bool
}

// OldEnvironment is the old server's environment after the move.
type OldEnvironment struct {
	EnvironmentID string
	Name          string
	Online        bool
	Archived      bool
	// StackCount is the stacks still managed there (not moved).
	StackCount int
	// StoppedCopies are the moved stacks' stopped sources still there
	// (removed from the stack's migration record).
	StoppedCopies int
	// MigrationID is the environment migration of the apps ("" none).
	MigrationID string
}

// complete builds the "Move complete" data of an arrived move.
func (s *Service) complete(ctx context.Context, a domain.ManagerMove) (Complete, error) {
	out := Complete{Redirects: []RedirectStatus{}}
	for _, r := range a.Redirects {
		st := RedirectStatus{ManagerMoveRedirect: r}
		connected, err := s.connectedSince(ctx, r.EnvironmentID, a.ArrivedAt)
		if err != nil {
			return Complete{}, err
		}
		st.Connected = connected
		st.NeedsFix = !r.Sent && !connected
		out.Redirects = append(out.Redirects, st)
	}
	if a.SourceEnvironmentID == "" {
		return out, nil
	}
	e, err := store.GetEnvironment(ctx, s.db, a.SourceEnvironmentID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return out, nil
	}
	if err != nil {
		return Complete{}, err
	}
	old := &OldEnvironment{EnvironmentID: e.ID, Name: e.Name, Online: e.Online, Archived: e.Status == domain.EnvironmentArchived,
		MigrationID: a.MigrationID}
	if !old.Archived {
		stacks, err := store.ListStacks(ctx, s.db, domain.StackFilter{EnvironmentID: e.ID})
		if err != nil {
			return Complete{}, err
		}
		old.StackCount = len(stacks)
		if s.opts.Migrations != nil {
			rs, err := s.opts.Migrations.RetainedSources(ctx, e.ID)
			if err != nil {
				return Complete{}, err
			}
			old.StoppedCopies = len(rs)
		}
	}
	out.OldEnvironment = old
	return out, nil
}

// connectedSince reports whether an environment's agent reached this
// manager after the arrival: it is online here, or it connected since.
func (s *Service) connectedSince(ctx context.Context, environmentID string, arrivedAt *time.Time) (bool, error) {
	e, err := store.GetEnvironment(ctx, s.db, environmentID)
	if errors.Is(err, domain.ErrEnvironmentNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if e.Online {
		return true, nil
	}
	if e.AgentID == "" || arrivedAt == nil {
		return false, nil
	}
	ag, err := store.GetAgent(ctx, s.db, e.AgentID)
	if errors.Is(err, domain.ErrAgentNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ag.LastConnectedAt != nil && !ag.LastConnectedAt.Before(*arrivedAt), nil
}
