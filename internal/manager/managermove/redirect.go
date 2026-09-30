package managermove

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// RedirectTimeout bounds one manager.redirect (best effort: the handoff
// goes on without an answer).
const RedirectTimeout = 5 * time.Second

// Redirect error classes (domain.ManagerMoveRedirect.ErrorClass).
const (
	RedirectOffline     = "offline"
	RedirectUnsupported = "unsupported"
	RedirectTimedOut    = "timeout"
	RedirectRefused     = "refused"
)

// redirectTarget is one agent the old manager can place.
type redirectTarget struct {
	environmentID, role, url string
}

// redirectTargets are the agents a move's handoff tells the new address:
// the new server's agent (its own server's manager in the generated
// compose) and the agent next to this manager (the new server's
// address). Agents elsewhere dial the public address, which DNS moves.
func redirectTargets(m domain.ManagerMove) []redirectTarget {
	var out []redirectTarget
	if m.TargetEnvironmentID != "" {
		out = append(out, redirectTarget{m.TargetEnvironmentID, domain.RedirectNewServer, NewServerManagerURL})
	}
	if m.SourceEnvironmentID != "" && m.SourceEnvironmentID != m.TargetEnvironmentID && m.NewServerAddress != "" {
		out = append(out, redirectTarget{m.SourceEnvironmentID, domain.RedirectOldServer, serverURL(m.NewServerAddress)})
	}
	return out
}

// sendRedirects sends manager.redirect {url, generation + 1} to every
// agent the move can place, at the same time, each bounded by
// RedirectTimeout, and returns what happened (never nil).
func (s *Service) sendRedirects(ctx context.Context, m domain.ManagerMove) []domain.ManagerMoveRedirect {
	targets := redirectTargets(m)
	out := make([]domain.ManagerMoveRedirect, len(targets))
	gen := max(s.opts.Instance.Generation, 1) + 1
	var wg sync.WaitGroup
	for i, t := range targets {
		r := domain.ManagerMoveRedirect{EnvironmentID: t.environmentID, Role: t.role, URL: t.url}
		if e, err := store.GetEnvironment(ctx, s.db, t.environmentID); err == nil {
			r.EnvironmentName = e.Name
		}
		out[i] = r
		wg.Go(func() {
			out[i].ErrorClass = s.redirectOne(ctx, t.environmentID, protocol.ManagerRedirectInput{URL: t.url, Generation: gen})
			out[i].Sent = out[i].ErrorClass == ""
		})
	}
	wg.Wait()
	for _, r := range out {
		if r.Sent {
			s.log.Info("told an agent the new manager's address", "move_id", m.ID, "environment_id", r.EnvironmentID, "role", r.Role,
				"url", r.URL, "generation", gen)
		} else {
			s.log.Warn("could not tell an agent the new manager's address; it needs DOCKER_AGENT_MANAGER_URL changed by hand",
				"move_id", m.ID, "environment_id", r.EnvironmentID, "role", r.Role, "class", r.ErrorClass)
		}
	}
	return out
}

// redirectOne sends one manager.redirect; "" when the agent accepted it,
// else the error class.
func (s *Service) redirectOne(ctx context.Context, environmentID string, in protocol.ManagerRedirectInput) string {
	if s.opts.Hub == nil || !s.opts.Hub.Online(environmentID) {
		return RedirectOffline
	}
	if !s.opts.Hub.EnvironmentServes(environmentID, protocol.ReqManagerRedirect) {
		return RedirectUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, RedirectTimeout)
	defer cancel()
	_, err := s.opts.Hub.RequestEnvironment(ctx, environmentID, protocol.ReqManagerRedirect, in, RedirectTimeout)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, jobs.ErrAgentOffline):
		return RedirectOffline
	case errors.Is(err, protocol.ErrRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		return RedirectTimedOut
	}
	// An error frame (*agents.RequestError) or a broken session.
	return RedirectRefused
}
