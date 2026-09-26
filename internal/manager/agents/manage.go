package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// --- agents ---

// ListAgents returns agents matching f (newest first).
func (s *Service) ListAgents(ctx context.Context, f domain.AgentFilter) ([]domain.Agent, error) {
	return store.ListAgents(ctx, s.db, f)
}

// GetAgent returns one agent.
func (s *Service) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return store.GetAgent(ctx, s.db, id)
}

// MaxLabel bounds an agent label.
const MaxLabel = 200

// UpdateAgentLabel sets an agent's operator label (compare-and-swap on
// expectRevision: domain.ErrRevisionMismatch when it changed meanwhile).
func (s *Service) UpdateAgentLabel(ctx context.Context, id string, expectRevision int64, label string) (domain.Agent, error) {
	if len(label) > MaxLabel || !cleanLabel(label) {
		return domain.Agent{}, inputErr("label", "must be at most %d characters without control characters", MaxLabel)
	}
	var a domain.Agent
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if a, err = store.GetAgent(ctx, tx, id); err != nil {
			return err
		}
		if a.Revision != expectRevision {
			return domain.ErrRevisionMismatch
		}
		a.Label, a.UpdatedAt = label, s.now()
		a.Revision++
		return store.UpdateAgent(ctx, tx, &a, expectRevision)
	})
	if err == nil {
		s.publish(events.Event{Type: events.AgentUpdated, ResourceType: events.ResourceAgent, ResourceID: a.ID, EnvironmentID: a.EnvironmentID, Revision: a.Revision})
	}
	return a, err
}

func cleanLabel(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// RemoveAgent revokes an agent: its credentials stop working at once, a
// live session is closed with 4403 and its environment stays offline and
// detached (re-attach it with a reattach enrollment). Removing an already
// revoked agent changes nothing. expectRevision is compared-and-swapped.
func (s *Service) RemoveAgent(ctx context.Context, id string, expectRevision int64) (domain.Agent, error) {
	var a domain.Agent
	var env domain.Environment
	changed := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if a, err = store.GetAgent(ctx, tx, id); err != nil {
			return err
		}
		if a.Revision != expectRevision {
			return domain.ErrRevisionMismatch
		}
		if a.Status == domain.AgentRevoked {
			return nil
		}
		now := s.now()
		if err := s.revokeAgentTx(ctx, tx, &a, "removed", now); err != nil {
			return err
		}
		if env, err = store.GetEnvironment(ctx, tx, a.EnvironmentID); err != nil {
			return err
		}
		if env.AgentID == a.ID {
			env.AgentID, env.UpdatedAt = "", now
			if env.Online {
				env.Online, env.ConnectionChangedAt = false, &now
			}
			env.Revision++
			if err := store.UpdateEnvironment(ctx, tx, &env, 0); err != nil {
				return err
			}
		}
		changed = true
		return nil
	})
	if err != nil || !changed {
		return a, err
	}
	s.log.Info("agent removed; its credential is revoked", "agent_id", a.ID, "environment_id", a.EnvironmentID)
	s.hub.kick(a.ID, protocol.CloseRevoked, "agent removed")
	s.publish(
		events.Event{Type: events.AgentRevoked, ResourceType: events.ResourceAgent, ResourceID: a.ID, EnvironmentID: a.EnvironmentID, Revision: a.Revision},
		events.Event{Type: events.EnvironmentUpdated, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision},
	)
	return a, nil
}

// RevokeAllAgents revokes every agent and its credentials and detaches it
// from its environment (the environments stay, detached, ready for
// `reattach:<environmentId>` enrollments, #34). A restored manager (#24)
// calls it at startup: credentials from a snapshot are never trusted
// silently. It returns the number of agents revoked.
func (s *Service) RevokeAllAgents(ctx context.Context, reason string) (int, error) {
	var revoked []domain.Agent
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		all, err := store.ListAgents(ctx, tx, domain.AgentFilter{})
		if err != nil {
			return err
		}
		now := s.now()
		for i := range all {
			a := all[i]
			if a.Status == domain.AgentRevoked {
				continue
			}
			if err := s.revokeAgentTx(ctx, tx, &a, reason, now); err != nil {
				return err
			}
			env, err := store.GetEnvironment(ctx, tx, a.EnvironmentID)
			if err != nil && !errors.Is(err, domain.ErrEnvironmentNotFound) {
				return err
			}
			if err == nil && env.AgentID == a.ID {
				env.AgentID, env.UpdatedAt = "", now
				if env.Online {
					env.Online, env.ConnectionChangedAt = false, &now
				}
				env.Revision++
				if err := store.UpdateEnvironment(ctx, tx, &env, 0); err != nil {
					return err
				}
			}
			if err := s.recordTx(ctx, tx, domain.AuditEvent{Action: AuditRestoreRevoke, Actor: audit.ServiceActor(),
				EnvironmentID: a.EnvironmentID, Outcome: domain.AuditSuccess, Targets: agentTargets(a.ID, a.EnvironmentID),
				Details: map[string]any{"reason": reason}}); err != nil {
				return err
			}
			revoked = append(revoked, a)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, a := range revoked {
		s.hub.kick(a.ID, protocol.CloseRevoked, "agent revoked")
	}
	return len(revoked), nil
}

// RotationTimeout bounds the wait for the agent's confirmation.
const RotationTimeout = 30 * time.Second

// RotateCredential issues a new credential for an active agent and hands
// it to the agent over its live session (request agent.credential.rotate).
// The agent persists it atomically and confirms; only then is the old
// credential revoked. When the agent is offline or does not confirm, the
// rotation stays pending (the new credential is kept sealed) and is
// delivered when the agent reconnects.
func (s *Service) RotateCredential(ctx context.Context, agentID string) (domain.CredentialRotation, error) {
	now := s.now()
	credID := ids.New()
	minted, err := authsep.MintAgentCredential(credID)
	if err != nil {
		return domain.CredentialRotation{}, err
	}
	sealed, err := s.keyring.Seal([]byte(minted.Token), sealContext(credID))
	if err != nil {
		return domain.CredentialRotation{}, err
	}
	var a domain.Agent
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if a, err = store.GetAgent(ctx, tx, agentID); err != nil {
			return err
		}
		if a.Status != domain.AgentActive {
			return domain.ErrAgentRevoked
		}
		// An earlier unconfirmed rotation stays pending: the agent may have
		// persisted it without the confirmation reaching the manager. The
		// first pending credential the agent presents (or confirms) becomes
		// the only valid one; every other is revoked then.
		return store.InsertCredential(ctx, tx, &domain.AgentCredential{ID: credID, AgentID: a.ID, Verifier: minted.Verifier,
			State: domain.CredentialPending, Sealed: sealed, CreatedAt: now})
	})
	if err != nil {
		return domain.CredentialRotation{}, err
	}
	s.log.Info("agent credential rotation requested", "agent_id", a.ID, "credential_id", credID)
	r := domain.CredentialRotation{AgentID: a.ID, CredentialID: credID, State: domain.RotationPending, RequestedAt: now}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RotationTimeout)
	defer cancel()
	if done, at := s.deliverRotation(dctx, a.ID); done {
		r.State, r.CompletedAt = domain.RotationCompleted, &at
	}
	return r, nil
}

func sealContext(credID string) string { return "agent_credentials/" + credID + "/credential" }

// deliverRotation sends the agent's pending credential over its live
// session and completes the rotation when the agent confirms. It reports
// whether the rotation is complete.
func (s *Service) deliverRotation(ctx context.Context, agentID string) (bool, time.Time) {
	cred, ok, err := store.PendingCredential(ctx, s.db, agentID)
	if err != nil || !ok {
		return false, time.Time{}
	}
	token, err := s.keyring.Open(cred.Sealed, sealContext(cred.ID))
	if err != nil {
		s.log.Error("cannot open a pending agent credential", "agent_id", agentID, "error", err)
		return false, time.Time{}
	}
	out, err := s.hub.Request(ctx, agentID, protocol.ReqAgentCredentialRotate, protocol.CredentialRotateInput{Credential: string(token)}, RotationTimeout)
	if err != nil {
		s.log.Info("agent credential rotation stays pending", "agent_id", agentID, "reason", err.Error())
		return false, time.Time{}
	}
	var res protocol.CredentialRotateOutput
	if err := json.Unmarshal(out, &res); err != nil || !res.Persisted {
		s.log.Warn("agent did not confirm the rotated credential", "agent_id", agentID)
		return false, time.Time{}
	}
	now := s.now()
	var activated bool
	var envID string
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := store.GetAgent(ctx, tx, agentID)
		if err != nil {
			return err
		}
		envID = a.EnvironmentID
		if a.Status != domain.AgentActive {
			return nil
		}
		if activated, err = store.ActivateCredential(ctx, tx, agentID, cred.ID, now); err != nil || !activated {
			return err
		}
		return s.recordTx(ctx, tx, domain.AuditEvent{Action: AuditRotation, Actor: audit.AgentActor(agentID),
			EnvironmentID: envID, Targets: agentTargets(agentID, envID), Details: map[string]any{"completed_by": "agent_confirmation"}})
	})
	if err != nil || !activated {
		return false, time.Time{}
	}
	s.log.Info("agent credential rotated; the old credential is revoked", "agent_id", agentID, "credential_id", cred.ID)
	s.publish(events.Event{Type: events.AgentCredentialRotated, ResourceType: events.ResourceAgent, ResourceID: agentID, EnvironmentID: envID})
	return true, now
}

// --- environments ---

// ListEnvironments returns environments matching f (ID order).
func (s *Service) ListEnvironments(ctx context.Context, f domain.EnvironmentFilter) ([]domain.Environment, error) {
	return store.ListEnvironments(ctx, s.db, f)
}

// GetEnvironment returns one environment.
func (s *Service) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	return store.GetEnvironment(ctx, s.db, id)
}

// MaxServiceAddress bounds a service address.
const MaxServiceAddress = 253

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validServiceAddress accepts a DNS host name or an IP address ("" clears).
func validServiceAddress(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > MaxServiceAddress {
		return false
	}
	return net.ParseIP(s) != nil || hostnameRE.MatchString(s)
}

// UpdateEnvironment edits an active environment's name and service address
// (compare-and-swap on expectRevision).
func (s *Service) UpdateEnvironment(ctx context.Context, id string, expectRevision int64, p domain.EnvironmentPatch) (domain.Environment, error) {
	if p.Name != nil {
		if err := validName(*p.Name); err != nil {
			return domain.Environment{}, inputErr("name", "%v", err)
		}
	}
	if p.ServiceAddress != nil && !validServiceAddress(*p.ServiceAddress) {
		return domain.Environment{}, inputErr("serviceAddress", "must be a host name or an IP address (no scheme, port or path)")
	}
	var env domain.Environment
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if env, err = store.GetEnvironment(ctx, tx, id); err != nil {
			return err
		}
		if env.Revision != expectRevision {
			return domain.ErrRevisionMismatch
		}
		if env.Status == domain.EnvironmentArchived {
			return domain.ErrEnvironmentArchived
		}
		if p.Name != nil {
			env.Name = *p.Name
		}
		if p.ServiceAddress != nil {
			env.ServiceAddress = *p.ServiceAddress
		}
		env.UpdatedAt = s.now()
		env.Revision++
		return store.UpdateEnvironment(ctx, tx, &env, expectRevision)
	})
	if err == nil {
		s.publish(events.Event{Type: events.EnvironmentUpdated, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision})
	}
	return env, err
}

// ArchiveHook runs inside the transaction that archives an environment
// (#34: the permission service removes the rules scoped to it). after, if
// not nil, runs once the transaction committed.
type ArchiveHook func(ctx context.Context, tx bun.Tx, env domain.Environment) (after func(), err error)

// SetArchiveHook installs the archive hook (startup only).
func (s *Service) SetArchiveHook(h ArchiveHook) { s.archiveHook = h }

// ArchiveEnvironment archives an environment: it is hidden from operations
// while its records are kept, its agent is revoked (a live session closes
// with 4403) and nothing on the host is touched. Re-enrolling its Engine
// with a reattach:<environmentId> enrollment re-attaches it. The archive
// hook removes the permission rules scoped to it in the same transaction;
// the removal preview (#34) lists every dependent record beforehand.
func (s *Service) ArchiveEnvironment(ctx context.Context, id string, expectRevision int64) (domain.Environment, error) {
	var env domain.Environment
	var revoked *domain.Agent
	var after func()
	changed := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if env, err = store.GetEnvironment(ctx, tx, id); err != nil {
			return err
		}
		if env.Revision != expectRevision {
			return domain.ErrRevisionMismatch
		}
		if env.Status == domain.EnvironmentArchived {
			return nil
		}
		now := s.now()
		if env.AgentID != "" {
			a, err := store.GetAgent(ctx, tx, env.AgentID)
			if err != nil {
				return err
			}
			if a.Status == domain.AgentActive {
				if err := s.revokeAgentTx(ctx, tx, &a, "environment archived", now); err != nil {
					return err
				}
				revoked = &a
			}
		}
		env.Status, env.ArchivedAt, env.AgentID = domain.EnvironmentArchived, &now, ""
		if env.Online {
			env.Online, env.ConnectionChangedAt = false, &now
		}
		env.UpdatedAt = now
		env.Revision++
		changed = true
		if err := store.UpdateEnvironment(ctx, tx, &env, 0); err != nil {
			return err
		}
		if s.archiveHook != nil {
			if after, err = s.archiveHook(ctx, tx, env); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !changed {
		return env, err
	}
	if after != nil {
		after()
	}
	s.log.Info("environment archived", "environment_id", env.ID)
	evs := []events.Event{{Type: events.EnvironmentArchived, ResourceType: events.ResourceEnvironment, ResourceID: env.ID, EnvironmentID: env.ID, Revision: env.Revision}}
	if revoked != nil {
		s.hub.kick(revoked.ID, protocol.CloseRevoked, "environment archived")
		evs = append(evs, events.Event{Type: events.AgentRevoked, ResourceType: events.ResourceAgent, ResourceID: revoked.ID, EnvironmentID: env.ID, Revision: revoked.Revision})
	}
	s.publish(evs...)
	return env, nil
}

// EnvironmentSystem returns the environment's reported system information.
func (s *Service) EnvironmentSystem(ctx context.Context, id string) (domain.EnvironmentSystem, error) {
	env, err := store.GetEnvironment(ctx, s.db, id)
	if err != nil {
		return domain.EnvironmentSystem{}, err
	}
	info := domain.EnvironmentSystem{Environment: env}
	if env.AgentID == "" {
		return info, nil
	}
	a, err := store.GetAgent(ctx, s.db, env.AgentID)
	if err != nil {
		return domain.EnvironmentSystem{}, err
	}
	info.Agent = &a
	return info, nil
}

// --- session lifecycle (called by the hub) ---

// sessionStarted records a newly established session.
func (s *Service) sessionStarted(ctx context.Context, p AgentPrincipal, sessionID string, hello protocol.HelloPayload, versionStatus string) error {
	now := s.now()
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := store.GetAgent(ctx, tx, p.AgentID)
		if err != nil {
			return err
		}
		if a.Status != domain.AgentActive {
			return domain.ErrAgentRevoked
		}
		a.SessionID, a.Version, a.VersionStatus = sessionID, hello.AgentVersion, versionStatus
		a.LastConnectedAt, a.LastSeenAt, a.UpdatedAt = &now, &now, now
		return store.UpdateAgent(ctx, tx, &a, 0)
	})
}

// stillActive reports whether the session's agent is still the active
// agent of its (active) environment.
func (s *Service) stillActive(ctx context.Context, p AgentPrincipal) bool {
	a, err := store.GetAgent(ctx, s.db, p.AgentID)
	if err != nil || a.Status != domain.AgentActive {
		return false
	}
	env, err := store.GetEnvironment(ctx, s.db, p.EnvironmentID)
	return err == nil && env.Status == domain.EnvironmentActive && env.AgentID == p.AgentID
}

// capabilitiesReported stores the agent's capabilities.
func (s *Service) capabilitiesReported(ctx context.Context, p AgentPrincipal, c protocol.CapabilitiesPayload) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	now := s.now()
	changed := false
	err = s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := store.GetAgent(ctx, tx, p.AgentID)
		if err != nil {
			return err
		}
		changed = a.Capabilities != string(b)
		a.Capabilities, a.CapabilitiesAt, a.LastSeenAt = string(b), &now, &now
		return store.UpdateAgent(ctx, tx, &a, 0)
	})
	if err == nil && changed {
		s.publish(events.Event{Type: events.AgentCapabilitiesUpdate, ResourceType: events.ResourceAgent, ResourceID: p.AgentID, EnvironmentID: p.EnvironmentID})
	}
	return err
}

// setOnline persists an environment's online/offline transition and
// publishes it.
func (s *Service) setOnline(ctx context.Context, p AgentPrincipal, online bool) {
	now := s.now()
	changed := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		env, err := store.GetEnvironment(ctx, tx, p.EnvironmentID)
		if err != nil {
			return err
		}
		if a, err := store.GetAgent(ctx, tx, p.AgentID); err == nil {
			a.LastSeenAt = &now
			if !online {
				a.SessionID = ""
			}
			if err := store.UpdateAgent(ctx, tx, &a, 0); err != nil {
				return err
			}
		}
		env.LastSeenAt = &now
		if online && (env.AgentID != p.AgentID || env.Status != domain.EnvironmentActive) {
			return nil // revoked meanwhile: never report it online
		}
		if env.Online != online {
			env.Online, env.ConnectionChangedAt, changed = online, &now, true
		}
		return store.UpdateEnvironment(ctx, tx, &env, 0)
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Error("cannot record environment connection state", "environment_id", p.EnvironmentID, "online", online, "error", err)
		}
		return
	}
	if !changed {
		return
	}
	typ := events.EnvironmentOffline
	if online {
		typ = events.EnvironmentOnline
	}
	s.log.Info("environment "+map[bool]string{true: "online", false: "offline"}[online], "environment_id", p.EnvironmentID, "agent_id", p.AgentID)
	s.publish(events.Event{Type: typ, ResourceType: events.ResourceEnvironment, ResourceID: p.EnvironmentID, EnvironmentID: p.EnvironmentID})
}
