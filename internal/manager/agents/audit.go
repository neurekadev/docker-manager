package agents

import (
	"context"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
)

// Audit (#30). Public API operations on agents, enrollments and
// environments are recorded automatically by api.Register. This file
// records what happens on /agent/v1, which is not a public API operation:
// enrollments (success, refusal, invalid token), credential rotations
// completed by the agent, and refused session upgrades and handshakes.
// Never record tokens, credentials or messages; error classes are stable
// codes.

// Audit actions of agent-side events. (Creating an enrollment token through
// the API is recorded as its capability, agent.enroll.)
const (
	// AuditEnroll: an enrollment token was exchanged for a credential on
	// POST /agent/v1/enroll (success, refusal or invalid token).
	AuditEnroll = "agent.enrollment_exchange"
	// AuditRotation: the agent completed a credential rotation (confirmed
	// the new credential, or presented it); the old one is revoked.
	AuditRotation = "agent.credential_rotate"
	// AuditSessionDenied: a session upgrade or handshake was refused
	// (invalid credential, identity or Engine mismatch, version).
	AuditSessionDenied = "agent.session_refused"
	// AuditRestoreRevoke: a manager restore (#24) revoked the agent's
	// restored credential; the environment awaits a re-attach.
	AuditRestoreRevoke = "agent.restore_revoke"
)

// AuditLog is the audit trail as the service uses it (*audit.Log).
type AuditLog interface {
	audit.Recorder
	audit.TxRecorder
}

type userAgentKey struct{}

// withUserAgent carries the request's User-Agent for audit records.
func withUserAgent(ctx context.Context, ua string) context.Context {
	return context.WithValue(ctx, userAgentKey{}, ua)
}

func fillUserAgent(ctx context.Context, ev *domain.AuditEvent) {
	if ev.UserAgent == "" {
		ev.UserAgent, _ = ctx.Value(userAgentKey{}).(string)
	}
}

// record appends ev in its own transaction (outside any other).
func (s *Service) record(ctx context.Context, ev domain.AuditEvent) {
	if s.audit == nil {
		return
	}
	fillUserAgent(ctx, &ev)
	if err := s.audit.Record(ctx, ev); err != nil {
		s.log.Error("cannot record audit event", "action", ev.Action, "error", err)
	}
}

// recordTx appends ev inside tx (commits with the change it describes).
func (s *Service) recordTx(ctx context.Context, tx bun.IDB, ev domain.AuditEvent) error {
	if s.audit == nil {
		return nil
	}
	fillUserAgent(ctx, &ev)
	return s.audit.RecordTx(ctx, tx, ev)
}

func agentTargets(agentID, environmentID string) []domain.AuditTarget {
	t := []domain.AuditTarget{}
	if agentID != "" {
		t = append(t, domain.AuditTarget{Type: "agent", ID: agentID, EnvironmentID: environmentID})
	}
	if environmentID != "" {
		t = append(t, domain.AuditTarget{Type: "environment", ID: environmentID, EnvironmentID: environmentID})
	}
	return t
}

func anonymous() domain.AuditActor { return domain.AuditActor{Kind: domain.AuditActorAnonymous} }
