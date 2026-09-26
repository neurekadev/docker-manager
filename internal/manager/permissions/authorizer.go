package permissions

import (
	"context"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/policy"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Conversions between stored rules and evaluator rules.

func toPolicyRule(r domain.PermissionRule) policy.Rule {
	return policy.Rule{Capability: r.Capability, Effect: policy.Effect(r.Effect), Scope: policy.Scope{
		Kind: policy.ScopeKind(r.Scope.Kind), EnvironmentID: r.Scope.EnvironmentID,
		ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}}
}

func toPolicyRules(rs []domain.PermissionRule) []policy.Rule {
	out := make([]policy.Rule, 0, len(rs))
	for _, r := range rs {
		out = append(out, toPolicyRule(r))
	}
	return out
}

func fromPolicyRule(r policy.Rule) domain.PermissionRule {
	return domain.PermissionRule{Capability: r.Capability, Effect: domain.PermissionEffect(r.Effect), Scope: domain.PermissionScope{
		Kind: string(r.Scope.Kind), EnvironmentID: r.Scope.EnvironmentID, ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}}
}

// Can implements authz.Authorizer (one check; lists use Compile).
func (s *Service) Can(ctx context.Context, p authz.Principal, capability string, r authz.Resource) authz.Decision {
	c, err := s.Compile(ctx, p)
	if err != nil {
		return authz.Deny("the permission policy could not be loaded")
	}
	return c.Can(capability, r)
}

// Compile implements authz.Compiler: it loads p's rules once.
func (s *Service) Compile(ctx context.Context, p authz.Principal) (authz.Checker, error) {
	if p.IsService() {
		return serviceChecker{}, nil
	}
	subj, err := s.subjectOf(ctx, p)
	if err != nil {
		s.log.Warn("load permissions", "principal", p.Key(), "error", err)
		return nil, err
	}
	return s.checker(ctx, subj), nil
}

// serviceChecker is the manager service identity: scheduled work under an
// enabled policy is not subject to user grants (#17, #26).
type serviceChecker struct{}

func (serviceChecker) Can(string, authz.Resource) authz.Decision {
	return authz.Allow("manager service identity")
}

// subjectOf loads what the evaluator needs about p.
func (s *Service) subjectOf(ctx context.Context, p authz.Principal) (policy.Subject, error) {
	if !p.Valid() {
		return policy.Subject{Inactive: true}, nil
	}
	ps, err := store.PermissionSubject(ctx, s.db, p.UserID)
	if err != nil {
		return policy.Subject{}, err
	}
	subj := subjectFrom(ps)
	if p.Kind == authz.KindAPIToken {
		subj.Token = []policy.Rule{}
		if f := s.tokenScopes(); f != nil {
			rules, ok, err := f(ctx, p.TokenID)
			if err != nil {
				return policy.Subject{}, err
			}
			if ok {
				for _, r := range rules {
					if r.Effect == domain.PermissionAllow {
						subj.Token = append(subj.Token, toPolicyRule(r))
					}
				}
			}
		}
	}
	return subj, nil
}

func subjectFrom(ps domain.PermissionSubject) policy.Subject {
	return policy.Subject{Owner: ps.Exists && ps.Owner, Inactive: !ps.Exists || !ps.Active,
		UserRules: toPolicyRules(ps.UserRules), GroupRules: toPolicyRules(ps.GroupRules)}
}

// checker compiles subj with the service's resource graph.
func (s *Service) checker(ctx context.Context, subj policy.Subject) *policy.Checker {
	return policy.NewChecker(s.cat, subj, func(ref authz.ResourceRef) policy.Location { return s.locate(ctx, ref) })
}
