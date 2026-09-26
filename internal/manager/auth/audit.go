package auth

import (
	"context"
	"log/slog"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
)

// TrailAuditor sends identity events to the audit trail (#30).
//
// Inside an audited API request (every identity route that changes
// something), api.Register already records one record per call with the
// operation, client IP, request ID and an outcome derived from the status.
// The event then only enriches that record: the actor who just signed in
// (the request had no principal yet), the affected user or resource, and
// the non-sensitive reason or step. Outside a request (the owner-recovery
// CLI) it appends a standalone identity record.
type TrailAuditor struct {
	Recorder audit.Recorder
	// Logger reports records that could not be stored.
	Logger *slog.Logger
}

// Record implements Auditor.
func (a TrailAuditor) Record(ctx context.Context, e AuditEvent) {
	if _, ok := audit.DraftFrom(ctx); ok {
		if e.ActorUserID != "" {
			audit.SetActor(ctx, domain.AuditActor{Kind: domain.AuditActorUser, UserID: e.ActorUserID})
		}
		if e.TargetType != "" && e.TargetID != "" {
			audit.AddTarget(ctx, domain.AuditTarget{Type: e.TargetType, ID: e.TargetID})
		}
		audit.SetDetail(ctx, "event", e.Action)
		if e.Reason != "" {
			audit.SetDetail(ctx, "reason", e.Reason)
		}
		return
	}
	if a.Recorder == nil {
		return
	}
	actor := domain.AuditActor{Kind: domain.AuditActorService}
	if e.ActorUserID != "" {
		actor = domain.AuditActor{Kind: domain.AuditActorUser, UserID: e.ActorUserID}
	}
	outcome := domain.AuditSuccess
	if e.Outcome == OutcomeFailure {
		outcome = domain.AuditFailure
	}
	ev := domain.AuditEvent{Category: domain.AuditIdentity, Action: e.Action, Actor: actor, Outcome: outcome}
	if e.TargetType != "" && e.TargetID != "" {
		ev.Targets = []domain.AuditTarget{{Type: e.TargetType, ID: e.TargetID}}
	}
	if e.Reason != "" {
		ev.Details = map[string]any{"reason": e.Reason}
	}
	if err := a.Recorder.Record(ctx, ev); err != nil && a.Logger != nil {
		a.Logger.Error("record audit event", "action", e.Action, "error", err)
	}
}
