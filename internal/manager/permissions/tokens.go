package permissions

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// ValidateTokenScope checks the scope of a new API token of userID (#31):
// every grant must be a valid allow rule of a grantable catalog capability
// (owner-only capabilities never are) at a scope the capability supports,
// and the user must currently hold it there: the grant's capability is
// allowed on the scope's representative resource (all resources of the
// type, a new resource in the environment, or the resource itself), as in
// the effective-permission view. A token can therefore never carry more
// than its user had when it was created; afterwards every request is
// evaluated as token scope ∩ the user's current permissions, so later
// narrowing applies at once. The owner holds every grantable capability.
// It returns a *domain.RuleError (field "scopes") listing each problem.
func (s *Service) ValidateTokenScope(ctx context.Context, userID string, scopes []domain.PermissionRule) error {
	if err := s.validate("scopes", scopes); err != nil {
		return err
	}
	re := &domain.RuleError{Field: "scopes"}
	for i, r := range scopes {
		if r.Effect != domain.PermissionAllow {
			re.Problems = append(re.Problems, domain.RuleProblem{Index: i, Message: "token scopes contain allow grants only"})
		}
	}
	if len(re.Problems) > 0 {
		return re
	}
	ps, err := store.PermissionSubject(ctx, s.db, userID)
	if err != nil {
		return err
	}
	if !ps.Exists {
		return domain.ErrUserNotFound
	}
	c := s.checker(ctx, subjectFrom(ps))
	for i, r := range scopes {
		cp, _ := s.cat.Lookup(r.Capability)
		if d := c.Decide(r.Capability, s.probeFor(ctx, cp, r.Scope)); !d.Allowed {
			re.Problems = append(re.Problems, domain.RuleProblem{Index: i,
				Message: "you do not hold " + r.Capability + " on " + toPolicyRule(r).Scope.String() + " (" + d.Reason + ")"})
		}
	}
	if len(re.Problems) > 0 {
		return re
	}
	return nil
}
