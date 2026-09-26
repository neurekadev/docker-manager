package agents

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// AgentPrincipal is an authenticated agent session credential.
type AgentPrincipal struct {
	AgentID       string
	EnvironmentID string
	InstallID     string
	EngineID      string
	CredentialID  string
}

// Authenticate verifies an agent credential. Unknown, malformed, revoked or
// rotated-out credentials, credentials of revoked agents and of archived
// environments all fail with domain.ErrCredentialInvalid.
//
// Presenting a pending (rotation) credential completes that rotation: the
// agent persisted it, so the old credential is revoked now.
func (s *Service) Authenticate(ctx context.Context, token string) (AgentPrincipal, error) {
	id, secret, ok := authsep.ParseAgentCredential(token)
	if !ok {
		return AgentPrincipal{}, domain.ErrCredentialInvalid
	}
	var p AgentPrincipal
	rotated := false
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cred, err := store.GetCredential(ctx, tx, id)
		if err != nil {
			return err
		}
		if cred.State == domain.CredentialRevoked || !authsep.VerifierMatches(cred.Verifier, secret) {
			return domain.ErrCredentialInvalid
		}
		a, err := store.GetAgent(ctx, tx, cred.AgentID)
		if err != nil {
			if errors.Is(err, domain.ErrAgentNotFound) {
				return domain.ErrCredentialInvalid
			}
			return err
		}
		if a.Status != domain.AgentActive {
			return domain.ErrCredentialInvalid
		}
		env, err := store.GetEnvironment(ctx, tx, a.EnvironmentID)
		if err != nil {
			return err
		}
		if env.Status != domain.EnvironmentActive || env.AgentID != a.ID {
			return domain.ErrCredentialInvalid
		}
		if cred.State == domain.CredentialPending {
			if rotated, err = store.ActivateCredential(ctx, tx, a.ID, cred.ID, s.now()); err != nil {
				return err
			}
			if rotated {
				if err := s.recordTx(ctx, tx, domain.AuditEvent{Action: AuditRotation, Actor: audit.AgentActor(a.ID),
					EnvironmentID: a.EnvironmentID, Targets: agentTargets(a.ID, a.EnvironmentID),
					Details: map[string]any{"completed_by": "presented_new_credential"}}); err != nil {
					return err
				}
			}
		}
		p = AgentPrincipal{AgentID: a.ID, EnvironmentID: a.EnvironmentID, InstallID: a.InstallID, EngineID: a.EngineID, CredentialID: cred.ID}
		return nil
	})
	if errors.Is(err, domain.ErrCredentialInvalid) {
		s.record(ctx, domain.AuditEvent{Action: AuditSessionDenied, Actor: anonymous(), Outcome: domain.AuditDenied,
			ErrorClass: "unauthenticated", Targets: []domain.AuditTarget{{Type: "agent_credential", ID: id}}})
	}
	if err != nil {
		return AgentPrincipal{}, err
	}
	if rotated {
		s.log.Info("agent credential rotation completed by the agent presenting the new credential", "agent_id", p.AgentID)
		s.publish(events.Event{Type: events.AgentCredentialRotated, ResourceType: events.ResourceAgent, ResourceID: p.AgentID, EnvironmentID: p.EnvironmentID})
	}
	return p, nil
}
