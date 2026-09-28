package permissions

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/catalog"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/policy"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Owner-only management flows (#17). Every change needs a recent step-up,
// is compare-and-set on a revision, is audited with a before/after diff
// and ends the affected users' open requests and streams.

func (s *Service) owner(ctx context.Context, recent bool) (string, error) {
	if s.guard == nil {
		return "", domain.ErrIdentityUnavailable
	}
	return s.guard.RequireOwner(ctx, recent)
}

// RuleText renders a rule in the compact form used by audit diffs and the
// decision corpus: "allow container.restart @container:<env>/web".
func RuleText(r domain.PermissionRule) string { return toPolicyRule(r).Shorthand() }

func ruleTexts(rs []domain.PermissionRule) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, RuleText(r))
	}
	return out
}

// diffDoc records a permission document change in the request's audit
// record: before/after rule lists and the added/removed rules.
func diffDoc(ctx context.Context, before, after domain.PermissionDocument) {
	b, a := ruleTexts(before.Rules), ruleTexts(after.Rules)
	audit.SetDiff(ctx, map[string]any{"revision": before.Revision, "rules": b}, map[string]any{"revision": after.Revision, "rules": a})
	in := func(list []string, x string) bool {
		for _, y := range list {
			if y == x {
				return true
			}
		}
		return false
	}
	added, removed := []string{}, []string{}
	for _, x := range a {
		if !in(b, x) {
			added = append(added, x)
		}
	}
	for _, x := range b {
		if !in(a, x) {
			removed = append(removed, x)
		}
	}
	audit.SetDetail(ctx, "rulesAdded", added)
	audit.SetDetail(ctx, "rulesRemoved", removed)
}

// validate checks a submitted rule list against the catalog.
func (s *Service) validate(field string, rules []domain.PermissionRule) error {
	errs := policy.Validate(s.cat, toPolicyRules(rules))
	if len(errs) == 0 {
		return nil
	}
	out := &domain.RuleError{Field: field}
	for _, e := range errs {
		out.Problems = append(out.Problems, domain.RuleProblem{Index: e.Index, Message: e.Message})
	}
	return out
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", &domain.FieldError{Field: "name", Message: "1-64 characters without control characters"}
	}
	return name, nil
}

// ListGroups returns every group with member and rule counts (owner).
func (s *Service) ListGroups(ctx context.Context) ([]domain.GroupInfo, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return nil, err
	}
	return store.ListGroups(ctx, s.db)
}

// GetGroup returns one group (owner).
func (s *Service) GetGroup(ctx context.Context, id string) (domain.GroupInfo, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return domain.GroupInfo{}, err
	}
	return store.GetGroupInfo(ctx, s.db, id)
}

// CreateGroup creates a group without grants (owner, step-up).
func (s *Service) CreateGroup(ctx context.Context, name string) (domain.GroupInfo, error) {
	if _, err := s.owner(ctx, true); err != nil {
		return domain.GroupInfo{}, err
	}
	name, err := normalizeName(name)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := store.CreateGroup(ctx, s.db, ids.New(), name, s.clk.Now())
	if err != nil {
		return domain.GroupInfo{}, err
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "group", ID: g.ID})
	audit.SetDetail(ctx, "name", g.Name)
	return g, nil
}

// RenameGroup renames a group (owner, step-up, revision). Access does not
// change.
func (s *Service) RenameGroup(ctx context.Context, id string, revision int64, name string) (domain.GroupInfo, error) {
	if _, err := s.owner(ctx, true); err != nil {
		return domain.GroupInfo{}, err
	}
	name, err := normalizeName(name)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	before, err := store.GetGroupInfo(ctx, s.db, id)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := store.RenameGroup(ctx, s.db, id, revision, name, s.clk.Now())
	if err != nil {
		return domain.GroupInfo{}, err
	}
	audit.SetDiff(ctx, map[string]string{"name": before.Name}, map[string]string{"name": g.Name})
	return g, nil
}

// DeleteGroup deletes a group (owner, step-up, revision). The current
// default group cannot be deleted (choose another default first), and a
// group with members cannot be deleted (move them first): Docker Manager
// never moves users implicitly, so deleting a group never changes anyone's
// access. The owner's account is not a member in that sense (group rules
// never apply to it): when it is in the group, it moves to the default
// group in the same transaction, recorded as a target and
// ownerMovedToGroupId in the request's audit record.
func (s *Service) DeleteGroup(ctx context.Context, id string, revision int64) error {
	if _, err := s.owner(ctx, true); err != nil {
		return err
	}
	var g domain.GroupInfo
	var doc domain.PermissionDocument
	var movedOwner, toGroup string
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if g, err = store.GetGroupInfo(ctx, tx, id); err != nil {
			return err
		}
		if doc, err = store.GroupPermissions(ctx, tx, id); err != nil {
			return err
		}
		movedOwner, toGroup, err = store.DeleteGroup(ctx, tx, id, revision, s.clk.Now())
		return err
	})
	if err != nil {
		return err
	}
	audit.SetDetail(ctx, "name", g.Name)
	audit.SetDiff(ctx, map[string]any{"name": g.Name, "rules": ruleTexts(doc.Rules)}, nil)
	if movedOwner != "" {
		audit.AddTarget(ctx, domain.AuditTarget{Type: "user", ID: movedOwner})
		audit.SetDetail(ctx, "ownerMovedToGroupId", toGroup)
	}
	return nil
}

// SelectDefaultGroup makes a group the default for new users (owner,
// step-up). It returns the group and whether it grants anything (the
// caller warns: new users will get access).
func (s *Service) SelectDefaultGroup(ctx context.Context, id string) (domain.GroupInfo, error) {
	if _, err := s.owner(ctx, true); err != nil {
		return domain.GroupInfo{}, err
	}
	var prev string
	var g domain.GroupInfo
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if prev, err = store.DefaultGroupID(ctx, tx); err != nil {
			return err
		}
		if err := store.SetDefaultGroup(ctx, tx, id); err != nil {
			return err
		}
		g, err = store.GetGroupInfo(ctx, tx, id)
		return err
	})
	if err != nil {
		return domain.GroupInfo{}, err
	}
	audit.SetDiff(ctx, map[string]string{"defaultGroupId": prev}, map[string]string{"defaultGroupId": id})
	audit.SetDetail(ctx, "grantsAccess", g.AllowCount > 0)
	return g, nil
}

// GroupPermissions returns a group's rule document (owner).
func (s *Service) GroupPermissions(ctx context.Context, id string) (domain.PermissionDocument, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return domain.PermissionDocument{}, err
	}
	return store.GroupPermissions(ctx, s.db, id)
}

// ReplaceGroupPermissions replaces a group's rules (owner, step-up,
// revision). Every member's open requests and streams end at once.
func (s *Service) ReplaceGroupPermissions(ctx context.Context, id string, revision int64, rules []domain.PermissionRule) (domain.PermissionDocument, error) {
	if _, err := s.owner(ctx, true); err != nil {
		return domain.PermissionDocument{}, err
	}
	if err := s.validate("rules", rules); err != nil {
		return domain.PermissionDocument{}, err
	}
	var before, after domain.PermissionDocument
	var members []string
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if before, err = store.GroupPermissions(ctx, tx, id); err != nil {
			return err
		}
		if after, err = store.ReplaceGroupPermissions(ctx, tx, id, revision, rules, s.clk.Now()); err != nil {
			return err
		}
		members, err = store.GroupMembers(ctx, tx, id)
		return err
	})
	if err != nil {
		return domain.PermissionDocument{}, err
	}
	diffDoc(ctx, before, after)
	audit.SetDetail(ctx, "affectedUsers", len(members))
	s.invalidate(ctx, members)
	return after, nil
}

// UserPermissions returns a user's override rules (owner).
func (s *Service) UserPermissions(ctx context.Context, id string) (domain.PermissionDocument, error) {
	if _, err := s.owner(ctx, false); err != nil {
		return domain.PermissionDocument{}, err
	}
	return store.UserPermissions(ctx, s.db, id)
}

// ReplaceUserPermissions replaces a user's override rules (owner, step-up,
// revision). An empty list resets every capability to inherit. The owner's
// capabilities are protected: rules for the owner are refused.
func (s *Service) ReplaceUserPermissions(ctx context.Context, id string, revision int64, rules []domain.PermissionRule) (domain.PermissionDocument, error) {
	if _, err := s.owner(ctx, true); err != nil {
		return domain.PermissionDocument{}, err
	}
	ps, err := store.PermissionSubject(ctx, s.db, id)
	if err != nil {
		return domain.PermissionDocument{}, err
	}
	switch {
	case !ps.Exists:
		return domain.PermissionDocument{}, domain.ErrUserNotFound
	case ps.Owner:
		return domain.PermissionDocument{}, domain.ErrOwnerProtected
	}
	if err := s.validate("rules", rules); err != nil {
		return domain.PermissionDocument{}, err
	}
	var before, after domain.PermissionDocument
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if before, err = store.UserPermissions(ctx, tx, id); err != nil {
			return err
		}
		after, err = store.ReplaceUserPermissions(ctx, tx, id, revision, rules, s.clk.Now())
		return err
	})
	if err != nil {
		return domain.PermissionDocument{}, err
	}
	diffDoc(ctx, before, after)
	s.invalidate(ctx, []string{id})
	return after, nil
}

// ForgetResource removes every group and user rule on one resource: call
// it after deleting a resource through Docker Manager (stack delete, volume or
// container removal), so a later resource with the same name in the same
// environment does not inherit the old rules. It returns the number of
// rules removed; affected users' streams end. Environment-, instance- and
// parent-scoped rules are unaffected.
func (s *Service) ForgetResource(ctx context.Context, ref authz.ResourceRef) (int, error) {
	env := ""
	if rt, ok := s.cat.Type(ref.Type); !ok || !rt.Scopable {
		return 0, errors.New("permissions: " + ref.Type + " is not a scopable resource type")
	} else if rt.NamedPerEnvironment {
		env = ref.EnvironmentID
	}
	var affected []string
	var n int
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		groups, users, err := store.ResourceRuleSubjects(ctx, tx, ref.Type, env, ref.ID)
		if err != nil {
			return err
		}
		affected = append(affected, users...)
		for _, g := range groups {
			m, err := store.GroupMembers(ctx, tx, g)
			if err != nil {
				return err
			}
			affected = append(affected, m...)
		}
		n, err = store.DeleteResourceRules(ctx, tx, ref.Type, env, ref.ID, s.clk.Now())
		return err
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		audit.SetDetail(ctx, "permissionRulesRemoved", n)
		s.invalidate(ctx, affected)
	}
	return n, nil
}

// probeFor is the resource a rule's scope stands for when showing what it
// decides: the scoped resource itself, a new resource of the capability's
// type in the environment or anywhere, or a new child inside a parent
// resource (a stack-scoped container rule).
func (s *Service) probeFor(ctx context.Context, cp catalog.Capability, sc domain.PermissionScope) authz.Resource {
	switch sc.Kind {
	case domain.ScopeKindEnvironment:
		if cp.Type == catalog.TypeEnvironment {
			return authz.EnvironmentResource(sc.EnvironmentID)
		}
		return authz.InEnvironment(cp.Type, sc.EnvironmentID)
	case domain.ScopeKindResource:
		ref := authz.ResourceRef{Type: sc.ResourceType, ID: sc.ResourceID, EnvironmentID: sc.EnvironmentID}
		loc := s.locate(ctx, ref)
		if stackID, _, ok := strings.Cut(ref.ID, "/"); !loc.Found && ref.Type == catalog.TypeService && ok && stackID != "" {
			// A service without a Locator lives in its stack (as in
			// policy.Checker): the stack's rules apply to it.
			stack := authz.ResourceRef{Type: catalog.TypeStack, ID: stackID}
			sl := s.locate(ctx, stack)
			loc = Location{Found: true, EnvironmentID: sl.EnvironmentID, Parents: append([]authz.ResourceRef{stack}, sl.Parents...)}
		}
		env := loc.EnvironmentID
		if env == "" {
			env = sc.EnvironmentID
		}
		if sc.ResourceType == cp.Type {
			parents := loc.Parents
			if parents == nil {
				parents = []authz.ResourceRef{}
			}
			return authz.Resource{Type: ref.Type, ID: ref.ID, EnvironmentID: env, Parents: parents}
		}
		return authz.Resource{Type: cp.Type, EnvironmentID: env, Parents: append([]authz.ResourceRef{ref}, loc.Parents...)}
	}
	return authz.Resource{Type: cp.Type, Parents: []authz.ResourceRef{}}
}

// ForgetEnvironment removes every group and user rule scoped to an
// environment being archived (#34): its environment-scoped rules and the
// rules on containers, images, volumes and networks named in it. It runs
// in the caller's transaction (the archive) and returns the removed rules
// and the users whose access changed; call AccessChanged with them after
// the transaction committed. Rules on stacks and policies stay with those
// records (they resume after a re-attach).
func (s *Service) ForgetEnvironment(ctx context.Context, tx bun.IDB, environmentID string) ([]domain.RemovedPermissionRule, []string, error) {
	rules, err := store.EnvironmentRules(ctx, tx, environmentID)
	if err != nil || len(rules) == 0 {
		return nil, nil, err
	}
	seen := map[string]bool{}
	var users []string
	for _, r := range rules {
		key := r.SubjectKind + ":" + r.SubjectID
		if seen[key] {
			continue
		}
		seen[key] = true
		if r.SubjectKind == "user" {
			users = append(users, r.SubjectID)
			continue
		}
		members, err := store.GroupMembers(ctx, tx, r.SubjectID)
		if err != nil {
			return nil, nil, err
		}
		users = append(users, members...)
	}
	if _, err := store.DeleteEnvironmentRules(ctx, tx, environmentID, s.clk.Now()); err != nil {
		return nil, nil, err
	}
	return rules, users, nil
}

// AccessChanged ends the open requests and streams of users whose
// effective permissions changed outside this service's own flows
// (ForgetEnvironment).
func (s *Service) AccessChanged(ctx context.Context, userIDs []string) { s.invalidate(ctx, userIDs) }
