package permissions

import (
	"context"
	"sort"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/policy"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// Effective is what a principal may do: one entry per capability and
// scope named by any of its rules, with the decision there and the rule
// that made it (the editor's "reason").
type Effective struct {
	UserID  string
	Owner   bool
	GroupID string
	Entries []EffectiveEntry
}

// EffectiveEntry is the decision for one capability at one scope.
type EffectiveEntry struct {
	Capability string
	Scope      domain.PermissionScope
	Allowed    bool
	// Source: owner, user_rule, group_rule, default_deny, token_scope, ...
	Source string
	// Rule is the deciding rule (user_rule and group_rule sources).
	Rule   *domain.PermissionRule
	Reason string
}

// CheckResult is the decision for one requested check.
type CheckResult struct {
	Capability string
	Resource   authz.Resource
	Allowed    bool
	Source     string
	Rule       *domain.PermissionRule
	Reason     string
}

func (s *Service) effective(ctx context.Context, userID, groupID string, subj policy.Subject) Effective {
	out := Effective{UserID: userID, GroupID: groupID, Owner: subj.Owner}
	if subj.Owner && subj.Token == nil {
		return out
	}
	c := s.checker(ctx, subj)
	seen := map[string]bool{}
	for _, r := range append(append(append([]policy.Rule{}, subj.UserRules...), subj.GroupRules...), subj.Token...) {
		k := r.Capability + "#" + r.Scope.Key()
		if seen[k] {
			continue
		}
		seen[k] = true
		cp, ok := s.cat.Lookup(r.Capability)
		if !ok {
			continue
		}
		dr := fromPolicyRule(r)
		d := c.Decide(r.Capability, s.probeFor(ctx, cp, dr.Scope))
		e := EffectiveEntry{Capability: r.Capability, Scope: dr.Scope, Allowed: d.Allowed, Source: string(d.Source), Reason: d.Reason}
		if d.Rule != nil {
			rule := fromPolicyRule(*d.Rule)
			e.Rule = &rule
		}
		out.Entries = append(out.Entries, e)
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Capability != b.Capability {
			return a.Capability < b.Capability
		}
		return scopeRank(a.Scope) < scopeRank(b.Scope)
	})
	return out
}

func scopeRank(s domain.PermissionScope) string {
	switch s.Kind {
	case domain.ScopeKindInstance:
		return "0"
	case domain.ScopeKindEnvironment:
		return "1" + s.EnvironmentID
	}
	return "2" + s.ResourceType + "/" + s.EnvironmentID + "/" + s.ResourceID
}

func (s *Service) check(c *policy.Checker, checks []authz.Resource, caps []string) []CheckResult {
	out := make([]CheckResult, 0, len(checks))
	for i, r := range checks {
		res := CheckResult{Capability: caps[i], Resource: r}
		if r.Type == "job" {
			d := c.Can(caps[i], r)
			res.Allowed, res.Reason, res.Source = d.Allowed, d.Reason, "job_targets"
		} else {
			d := c.Decide(caps[i], r)
			res.Allowed, res.Source, res.Reason = d.Allowed, string(d.Source), d.Reason
			if d.Rule != nil {
				rule := fromPolicyRule(*d.Rule)
				res.Rule = &rule
			}
		}
		out = append(out, res)
	}
	return out
}

// Mine returns the caller's effective permissions (any principal).
func (s *Service) Mine(ctx context.Context, p authz.Principal) (Effective, error) {
	subj, err := s.subjectOf(ctx, p)
	if err != nil {
		return Effective{}, err
	}
	ps, err := store.PermissionSubject(ctx, s.db, p.UserID)
	if err != nil {
		return Effective{}, err
	}
	return s.effective(ctx, p.UserID, ps.GroupID, subj), nil
}

// UserEffective returns a user's effective permissions (owner).
func (s *Service) UserEffective(ctx context.Context, userID string) (Effective, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return Effective{}, err
	}
	ps, err := store.PermissionSubject(ctx, s.db, userID)
	if err != nil {
		return Effective{}, err
	}
	if !ps.Exists {
		return Effective{}, domain.ErrUserNotFound
	}
	return s.effective(ctx, userID, ps.GroupID, subjectFrom(ps)), nil
}

// PreviewRequest is an owner-only "view as" evaluation (no impersonated
// session): a user (or a group's typical member), optionally with unsaved
// edits applied.
type PreviewRequest struct {
	// UserID previews that user; empty previews a member of GroupID
	// without user overrides.
	UserID string
	// GroupID previews the user as a member of this group (a group move).
	GroupID string
	// GroupRules, when set, replace the (previewed) group's rules.
	GroupRules *[]domain.PermissionRule
	// UserRules, when set, replace the user's override rules.
	UserRules *[]domain.PermissionRule
	// TokenScope, when set, previews an API token with this scope (#31):
	// token scope ∩ the user's effective permissions.
	TokenScope *[]domain.PermissionRule
	// Checks are evaluated with their deciding rule.
	Checks []PreviewCheck
}

// PreviewCheck asks for one decision.
type PreviewCheck struct {
	Capability string
	Resource   authz.Resource
}

// Preview is the result of a PreviewRequest.
type Preview struct {
	Effective Effective
	Checks    []CheckResult
}

// Preview evaluates a PreviewRequest (owner). It changes nothing.
func (s *Service) Preview(ctx context.Context, req PreviewRequest) (Preview, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return Preview{}, err
	}
	var ps domain.PermissionSubject
	switch {
	case req.UserID != "":
		var err error
		if ps, err = store.PermissionSubject(ctx, s.db, req.UserID); err != nil {
			return Preview{}, err
		}
		if !ps.Exists {
			return Preview{}, domain.ErrUserNotFound
		}
	case req.GroupID != "":
		ps = domain.PermissionSubject{Exists: true, Active: true}
	default:
		return Preview{}, &domain.FieldError{Field: "userId", Message: "name a user or a group to preview"}
	}
	if req.GroupID != "" && req.GroupID != ps.GroupID {
		doc, err := store.GroupPermissions(ctx, s.db, req.GroupID)
		if err != nil {
			return Preview{}, err
		}
		ps.GroupID, ps.GroupRules = req.GroupID, doc.Rules
	}
	for _, edit := range []struct {
		field string
		rules *[]domain.PermissionRule
		dst   *[]domain.PermissionRule
	}{{"groupRules", req.GroupRules, &ps.GroupRules}, {"userRules", req.UserRules, &ps.UserRules}} {
		if edit.rules == nil {
			continue
		}
		if err := s.validate(edit.field, *edit.rules); err != nil {
			return Preview{}, err
		}
		*edit.dst = *edit.rules
	}
	subj := subjectFrom(ps)
	if req.TokenScope != nil {
		if err := s.validate("tokenScope", *req.TokenScope); err != nil {
			return Preview{}, err
		}
		subj.Token = []policy.Rule{}
		for i, r := range *req.TokenScope {
			if r.Effect != domain.PermissionAllow {
				return Preview{}, &domain.RuleError{Field: "tokenScope", Problems: []domain.RuleProblem{{Index: i, Message: "token scopes contain allow grants only"}}}
			}
			subj.Token = append(subj.Token, toPolicyRule(r))
		}
	}
	out := Preview{Effective: s.effective(ctx, req.UserID, ps.GroupID, subj)}
	c := s.checker(ctx, subj)
	resources := make([]authz.Resource, len(req.Checks))
	caps := make([]string, len(req.Checks))
	for i, ch := range req.Checks {
		resources[i], caps[i] = ch.Resource, ch.Capability
	}
	out.Checks = s.check(c, resources, caps)
	return out, nil
}
