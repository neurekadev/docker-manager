package managermove

import (
	"context"
	"encoding/json"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Back to HTTPS (new manager, docs/internal/architecture/manager-move.md,
// "Back to HTTPS"). A move puts two agents on plain HTTP across the
// network: the new server's agent enrolls with the old manager at
// http://<old server> (its credential is minted over it), and the agent
// next to the old manager is redirected to http://<new server>, which it
// dials with its credential after every restart. Once the move arrived
// and the public URL is https, the Run loop (secureDue):
//
//   - sends the agent that still dials the move's plain-HTTP address the
//     public URL at the same generation (manager.redirect, agents
//     announcing protocol.FeatureManagerRedirectSecure), once a request
//     reached this manager over HTTPS at its public URL
//     (observePublicRequest: DNS and the reverse proxy lead here);
//   - rotates the credential of both agents once their session no longer
//     crosses the network in clear (https, or the new server's Docker
//     network: NewServerManagerURL).
//
// Each happens once per agent (ReturnedAt, RotatedAt on the move's
// redirect). A plain-http public URL (localhost development) changes
// nothing.

// secureAction is what an agent of the arrived move needs now.
type secureAction int

const (
	secureNone secureAction = iota
	// secureReturn: send the public URL (manager.redirect).
	secureReturn
	// secureRotate: rotate the credential.
	secureRotate
)

// secureStep decides the next step for the agent of redirect r, whose
// session reports transport t. publicReached: a request reached this
// manager at its HTTPS public URL; canReturn: the agent accepts a
// redirect at the same generation.
func secureStep(r domain.ManagerMoveRedirect, t protocol.TransportInfo, publicReached, canReturn bool) secureAction {
	if secured(r) {
		return secureNone
	}
	switch {
	case r.Role == domain.RedirectOldServer && t.PlainHTTP && t.ManagerURL == r.URL:
		if r.ReturnedAt == nil && publicReached && canReturn {
			return secureReturn
		}
	case !t.PlainHTTP || t.ManagerURL == NewServerManagerURL:
		return secureRotate
	}
	return secureNone
}

// crossedInClear reports whether the move sent the agent's credential
// over the network in plain HTTP: the new server's agent enrolled with
// the old manager over http; the agent next to the old manager dialed the
// new server over http once it got the redirect.
func crossedInClear(r domain.ManagerMoveRedirect) bool {
	return r.Role == domain.RedirectNewServer || r.Sent
}

// secured: nothing is left to do for the agent of r (its credential was
// rotated after it left the plain-HTTP address, or never crossed).
func secured(r domain.ManagerMoveRedirect) bool {
	return r.RotatedAt != nil || !crossedInClear(r)
}

// securePublicOrigin is the public URL's origin when it is https ("" for
// a plain-http, localhost development public URL).
func (s *Service) securePublicOrigin() string {
	u := s.opts.PublicURL
	if u == nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// observePublicRequest notes a request that reached this manager over
// HTTPS at its public URL (the owner's dashboard reads the arrived move):
// the agent left on the move's plain-HTTP address may follow the public
// URL now.
func (s *Service) observePublicRequest(ctx context.Context) {
	info, ok := requestinfo.From(ctx)
	if !ok || s.securePublicOrigin() == "" || requestinfo.CheckSecureOrigin(s.opts.PublicURL, false, info) != nil {
		return
	}
	if !s.publicReached.Swap(true) {
		s.notify()
	}
}

// secureDue takes the next step for each agent of the arrived move that
// still needs one (the Run loop; woken when an environment comes online
// and by observePublicRequest).
func (s *Service) secureDue(ctx context.Context) {
	if s.securePublicOrigin() == "" || s.opts.Hub == nil || s.opts.Enrollments == nil {
		return
	}
	a, found, err := store.LatestArrivedManagerMove(ctx, s.db)
	if err != nil || !found {
		return
	}
	for _, r := range a.Redirects {
		if ctx.Err() != nil {
			return
		}
		if !secured(r) {
			s.secureOne(ctx, a.ID, r)
		}
	}
}

// secureOne takes the next step for the agent of redirect r, if it is
// connected.
func (s *Service) secureOne(ctx context.Context, moveID string, r domain.ManagerMoveRedirect) {
	if !s.opts.Hub.Online(r.EnvironmentID) {
		return
	}
	e, err := s.opts.Enrollments.GetEnvironment(ctx, r.EnvironmentID)
	if err != nil || e.Status != domain.EnvironmentActive || e.AgentID == "" {
		return
	}
	ag, err := s.opts.Enrollments.GetAgent(ctx, e.AgentID)
	if err != nil || ag.Status != domain.AgentActive {
		return
	}
	t, ok := agentTransport(ag)
	if !ok {
		return
	}
	canReturn := s.opts.Hub.EnvironmentHasFeature(r.EnvironmentID, protocol.FeatureManagerRedirectSecure)
	switch secureStep(r, t, s.publicReached.Load(), canReturn) {
	case secureReturn:
		public := s.securePublicOrigin()
		if class := s.redirectOne(ctx, r.EnvironmentID, protocol.ManagerRedirectInput{URL: public,
			Generation: max(s.opts.Instance.Generation, 1)}); class != "" {
			s.log.Warn("could not give an agent this manager's HTTPS address; it keeps the plain-HTTP address of the move",
				"move_id", moveID, "environment_id", r.EnvironmentID, "class", class)
			return
		}
		s.log.Info("gave the agent that followed the move over plain HTTP this manager's HTTPS address", "move_id", moveID,
			"environment_id", r.EnvironmentID, "url", public, "previous_url", r.URL)
		s.markSecured(ctx, moveID, r.EnvironmentID, func(r *domain.ManagerMoveRedirect, now time.Time) { r.ReturnedAt = &now })
	case secureRotate:
		rot, err := s.opts.Enrollments.RotateCredential(ctx, ag.ID)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("could not rotate the credential of an agent that used plain HTTP during the move", "move_id", moveID,
					"environment_id", r.EnvironmentID, "agent_id", ag.ID, "error", err)
			}
			return
		}
		s.log.Info("rotated the credential of an agent that used plain HTTP during the move", "move_id", moveID,
			"environment_id", r.EnvironmentID, "agent_id", ag.ID, "state", rot.State)
		s.markSecured(ctx, moveID, r.EnvironmentID, func(r *domain.ManagerMoveRedirect, now time.Time) { r.RotatedAt = &now })
	}
}

// markSecured records a step on the arrived move's redirect of an
// environment (under mu, reading the move again).
func (s *Service) markSecured(ctx context.Context, moveID, environmentID string, set func(*domain.ManagerMoveRedirect, time.Time)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := store.GetManagerMove(ctx, s.db, moveID)
	if err != nil {
		s.log.Error("could not record the step on the arrived move", "move_id", moveID, "error", err)
		return
	}
	now := s.now()
	for i := range m.Redirects {
		if m.Redirects[i].EnvironmentID == environmentID {
			set(&m.Redirects[i], now)
		}
	}
	m.UpdatedAt = now
	if err := store.UpdateManagerMove(ctx, s.db, &m, domain.MoveArrived); err != nil {
		s.log.Error("could not record the step on the arrived move", "move_id", moveID, "error", err)
		return
	}
	s.publish(m.ID)
}

// agentTransport is the transport the agent reported in its last
// capabilities (how its session reaches this manager).
func agentTransport(a domain.Agent) (protocol.TransportInfo, bool) {
	var c struct {
		Transport protocol.TransportInfo `json:"transport"`
	}
	if a.Capabilities == "" || json.Unmarshal([]byte(a.Capabilities), &c) != nil || c.Transport.Validate() != nil {
		return protocol.TransportInfo{}, false
	}
	return c.Transport, true
}
