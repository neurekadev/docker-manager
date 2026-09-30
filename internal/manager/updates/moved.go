package updates

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// ActionPolicyMoved is the audit action of a policy re-homed with its
// migrated stack (#35).
const ActionPolicyMoved = "update_policy.move"

// StackMoved re-homes the update policy of a stack migrated to another
// environment (#35: "update and backup policies that target the stack
// follow it"). Register it with Migrations().OnStackMoved: it runs in the
// transaction that completes the migration, so the policy moves exactly
// when the stack does.
//
// The policy keeps its ID (permission rules follow it), schedules, window,
// history and quarantined digests; its environment becomes the
// destination, so scheduled checks and runs are no longer refused with
// target_not_found. The candidates are reset to unchecked: the migration's
// deploy recorded the destination's applied images, and only a new check
// (for the destination's platform) may make a candidate runnable again. A
// name already taken by another policy of the destination gets a
// " (moved)" suffix.
func (s *Service) StackMoved(ctx context.Context, db bun.IDB, stackID, from, to string) error {
	if from == to {
		return nil
	}
	ps, err := store.UpdatePoliciesForTarget(ctx, db, from, domain.UpdateTargetStack, stackID)
	if err != nil || len(ps) == 0 {
		return err
	}
	for _, p := range ps {
		next := p
		next.EnvironmentID = to
		if next.Name, err = freeName(ctx, db, to, p.Name); err != nil {
			return err
		}
		next.Revision, next.UpdatedAt = p.Revision+1, s.now()
		if err := store.UpdateUpdatePolicy(ctx, db, &next, p.Revision); err != nil {
			return fmt.Errorf("updates: move policy %s: %w", p.ID, err)
		}
		cands, err := store.UpdateCandidates(ctx, db, p.ID)
		if err != nil {
			return err
		}
		for _, c := range cands {
			if c.Status == domain.CandidateIneligible {
				continue
			}
			c.Status, c.CandidateDigest, c.CandidateIndexDigest, c.UpdatedAt = domain.CandidateUnchecked, "", "", s.now()
			c.CandidatePublishedAt = nil
			c.ErrorClass, c.ErrorMessage, c.RetryAfterSeconds = "", "", 0
			if err := store.UpsertUpdateCandidate(ctx, db, &c); err != nil {
				return err
			}
		}
		if s.opts.Audit != nil {
			details := map[string]any{"stackId": stackID, "fromEnvironmentId": from, "toEnvironmentId": to}
			if next.Name != p.Name {
				details["renamedFrom"], details["name"] = p.Name, next.Name
			}
			if err := s.opts.Audit.RecordTx(ctx, db, domain.AuditEvent{Category: domain.AuditOperations, Action: ActionPolicyMoved,
				Actor: audit.ServiceActor(), EnvironmentID: to, Outcome: domain.AuditSuccess,
				Targets: []domain.AuditTarget{{Type: catalog.TypeUpdatePolicy, ID: p.ID, EnvironmentID: to}},
				Details: details}); err != nil {
				return err
			}
		}
		s.log.Info("update policy follows its migrated stack", "policy_id", p.ID, "stack_id", stackID, "from", from, "to", to)
	}
	// The scheduler re-reads the policies (their environment changed)
	// once the migration's transaction committed.
	s.notify()
	return nil
}

// freeName returns name, or name with a " (moved)" suffix (numbered when
// needed), so it is unique among the destination's policies.
func freeName(ctx context.Context, db bun.IDB, env, name string) (string, error) {
	taken := map[string]bool{}
	after := ""
	for {
		page, err := store.ListUpdatePolicies(ctx, db, env, after, 500)
		if err != nil {
			return "", err
		}
		for _, p := range page {
			taken[store.NameKey(p.Name)] = true
		}
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].ID
	}
	for i := 0; ; i++ {
		suffix := ""
		switch {
		case i == 1:
			suffix = " (moved)"
		case i > 1:
			suffix = fmt.Sprintf(" (moved %d)", i)
		}
		candidate := truncateName(name, 100-utf8.RuneCountInString(suffix)) + suffix
		if !taken[store.NameKey(candidate)] {
			return candidate, nil
		}
	}
}

func truncateName(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
