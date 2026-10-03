package permissions

import (
	"context"
	"slices"
	"sort"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Effective is what a principal may do: one entry per capability and
// scope named by any of its rules, with the decision there and the rule
// that made it (the editor's "reason").
type Effective struct {
	UserID string
	Owner  bool
	// GroupIDs are the principal's groups, highest priority first.
	GroupIDs []string
	Entries  []EffectiveEntry
}

// EffectiveEntry is the decision for one capability at one scope.
type EffectiveEntry struct {
	Capability string
	Scope      domain.PermissionScope
	Allowed    bool
	// Source: owner, user_rule, group_rule, default_deny, token_scope, ...
	Source string
	// Rule is the deciding rule (user_rule and group_rule sources).
	Rule *domain.PermissionRule
	// GroupID is the group of the deciding rule (group_rule source).
	GroupID string
	Reason  string
}

// CheckResult is the decision for one requested check.
type CheckResult struct {
	Capability string
	Resource   authz.Resource
	Allowed    bool
	Source     string
	Rule       *domain.PermissionRule
	GroupID    string
	Reason     string
}

func (s *Service) effective(ctx context.Context, userID string, subj policy.Subject) Effective {
	out := Effective{UserID: userID, GroupIDs: []string{}, Owner: subj.Owner}
	for _, g := range subj.Groups {
		out.GroupIDs = append(out.GroupIDs, g.ID)
	}
	if subj.Owner && subj.Token == nil {
		return out
	}
	c := s.checker(ctx, subj)
	seen := map[string]bool{}
	for _, r := range append(append(append([]policy.Rule{}, subj.UserRules...), subj.GroupRules()...), subj.Token...) {
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
		e := EffectiveEntry{Capability: r.Capability, Scope: dr.Scope, Allowed: d.Allowed, Source: string(d.Source), GroupID: d.GroupID,
			Reason: d.Reason}
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
			res.Allowed, res.Source, res.GroupID, res.Reason = d.Allowed, string(d.Source), d.GroupID, d.Reason
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
	return s.effective(ctx, p.UserID, subj), nil
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
	return s.effective(ctx, userID, subjectFrom(ps)), nil
}

// PreviewRequest is an owner-only "view as" evaluation (no impersonated
// session): a user (or a member of some groups), optionally with unsaved
// edits applied.
type PreviewRequest struct {
	// UserID previews that user; empty previews a member of GroupIDs
	// without user overrides.
	UserID string
	// GroupIDs, when set, previews the user (or the member) in exactly
	// these groups (any order: they are evaluated in priority order).
	GroupIDs *[]string
	// RulesGroupID is the group whose rules GroupRules replace; empty
	// means the only previewed group.
	RulesGroupID string
	// GroupRules, when set, replace RulesGroupID's rules.
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
	case req.GroupIDs != nil:
		ps = domain.PermissionSubject{Exists: true, Active: true}
	default:
		return Preview{}, &domain.FieldError{Field: "userId", Message: "name a user or groups to preview"}
	}
	if req.GroupIDs != nil {
		for _, id := range *req.GroupIDs {
			if _, err := store.GetGroupInfo(ctx, s.db, id); err != nil {
				return Preview{}, err
			}
		}
		ordered, err := store.SortGroups(ctx, s.db, *req.GroupIDs)
		if err != nil {
			return Preview{}, err
		}
		if ps.Groups, err = store.GroupRuleSets(ctx, s.db, ordered); err != nil {
			return Preview{}, err
		}
	}
	if req.GroupRules != nil {
		target := req.RulesGroupID
		if target == "" && len(ps.Groups) == 1 {
			target = ps.Groups[0].GroupID
		}
		i := slices.IndexFunc(ps.Groups, func(g domain.GroupRules) bool { return g.GroupID == target })
		if i < 0 {
			return Preview{}, &domain.FieldError{Field: "rulesGroupId", Message: "name one of the previewed groups whose rules groupRules replace"}
		}
		if err := s.validate("groupRules", *req.GroupRules); err != nil {
			return Preview{}, err
		}
		ps.Groups[i].Rules = *req.GroupRules
	}
	if req.UserRules != nil {
		if err := s.validate("userRules", *req.UserRules); err != nil {
			return Preview{}, err
		}
		ps.UserRules = *req.UserRules
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
	out := Preview{Effective: s.effective(ctx, req.UserID, subj)}
	c := s.checker(ctx, subj)
	resources := make([]authz.Resource, len(req.Checks))
	caps := make([]string, len(req.Checks))
	for i, ch := range req.Checks {
		resources[i], caps[i] = ch.Resource, ch.Capability
	}
	out.Checks = s.check(c, resources, caps)
	return out, nil
}
