package permissions_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/authz/policy"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/jobs/jobstest"
	"github.com/neurekadev/docker-manager/internal/manager/permissions"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// guard is a fake owner guard: owner is always the caller; recent can be
// switched off to simulate an expired step-up.
type guard struct {
	mu     sync.Mutex
	owner  string
	stale  bool
	denied bool
}

func (g *guard) RequireOwner(_ context.Context, recent bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.denied:
		return "", domain.ErrForbidden
	case recent && g.stale:
		return "", domain.ErrStepUpRequired
	}
	return g.owner, nil
}

// invalidations records AccessChanged calls.
type invalidations struct {
	mu    sync.Mutex
	users []string
}

func (i *invalidations) AccessChanged(_ context.Context, ids []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.users = append(i.users, ids...)
}

func (i *invalidations) take() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := i.users
	i.users = nil
	slices.Sort(out)
	return out
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	db    *bun.DB
	svc   *permissions.Service
	guard *guard
	inval *invalidations
	def   string // the default (Restricted) group
	owner string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: testutil.Context(t), db: storetest.Migrated(t), guard: &guard{}, inval: &invalidations{}}
	var err error
	f.svc, err = permissions.New(permissions.Options{DB: f.db, Clock: testutil.FakeClock(), Logger: testutil.Logger(t),
		Guard: f.guard, Invalidator: f.inval})
	if err != nil {
		t.Fatal(err)
	}
	if f.def, err = store.DefaultGroupID(f.ctx, f.db); err != nil {
		t.Fatal(err)
	}
	f.owner = f.user("owner", f.def, true)
	f.guard.owner = f.owner
	return f
}

// user creates an account in group.
func (f *fixture) user(name, group string, owner bool) string {
	f.t.Helper()
	id := ids.New()
	if _, err := store.CreateUser(f.ctx, f.db, domain.NewUser{ID: id, Username: name, Owner: owner, GroupID: group,
		WebAuthnHandle: []byte(id), CreatedAt: testutil.Epoch}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func rules(t *testing.T, shorthand ...string) []domain.PermissionRule {
	t.Helper()
	out := make([]domain.PermissionRule, 0, len(shorthand))
	for _, s := range shorthand {
		r, err := policy.ParseRule(catalog.Default(), s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, domain.PermissionRule{Capability: r.Capability, Effect: domain.PermissionEffect(r.Effect),
			Scope: domain.PermissionScope{Kind: string(r.Scope.Kind), EnvironmentID: r.Scope.EnvironmentID,
				ResourceType: r.Scope.ResourceType, ResourceID: r.Scope.ResourceID}})
	}
	return out
}

func (f *fixture) setGroup(group string, shorthand ...string) domain.PermissionDocument {
	f.t.Helper()
	cur, err := f.svc.GroupPermissions(f.ctx, group)
	if err != nil {
		f.t.Fatal(err)
	}
	doc, err := f.svc.ReplaceGroupPermissions(f.ctx, group, cur.Revision, rules(f.t, shorthand...))
	if err != nil {
		f.t.Fatal(err)
	}
	return doc
}

func (f *fixture) setUser(user string, shorthand ...string) domain.PermissionDocument {
	f.t.Helper()
	cur, err := f.svc.UserPermissions(f.ctx, user)
	if err != nil {
		f.t.Fatal(err)
	}
	doc, err := f.svc.ReplaceUserPermissions(f.ctx, user, cur.Revision, rules(f.t, shorthand...))
	if err != nil {
		f.t.Fatal(err)
	}
	return doc
}

func (f *fixture) can(user, capability string, r authz.Resource) bool {
	return f.svc.Can(f.ctx, authz.Principal{Kind: authz.KindUser, UserID: user}, capability, r).Allowed
}

func container(env, name string, parents ...authz.ResourceRef) authz.Resource {
	if parents == nil {
		parents = []authz.ResourceRef{}
	}
	return authz.Resource{Type: catalog.TypeContainer, ID: name, EnvironmentID: env, Parents: parents}
}

func TestRestrictedDefaultAndGroupDocuments(t *testing.T) {
	f := newFixture(t)
	gs, err := f.svc.ListGroups(f.ctx)
	if err != nil || len(gs) != 1 || !gs[0].Default || gs[0].Name != domain.RestrictedGroupName || gs[0].RuleCount != 0 || gs[0].MemberCount != 0 {
		t.Fatalf("groups %+v %v", gs, err)
	}
	rita := f.user("rita", f.def, false)
	if f.can(rita, "container.metrics.read", container("e1", "web")) || f.can(rita, "environment.read", authz.EnvironmentResource("e1")) {
		t.Fatal("a Restricted user is granted something")
	}
	if !f.can(f.owner, "container.exec", container("e1", "web")) {
		t.Fatal("owner bypass")
	}

	ops, err := f.svc.CreateGroup(f.ctx, "  Ops ")
	if err != nil || ops.Name != "Ops" || ops.Default || ops.PermissionsRevision != 1 {
		t.Fatalf("create %+v %v", ops, err)
	}
	if _, err := f.svc.CreateGroup(f.ctx, "ops"); !errors.Is(err, domain.ErrGroupNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	var fe *domain.FieldError
	if _, err := f.svc.CreateGroup(f.ctx, " "); !errors.As(err, &fe) {
		t.Fatalf("blank name: %v", err)
	}
	sam := f.user("sam", ops.ID, false)
	f.inval.take()

	doc := f.setGroup(ops.ID, "allow container.restart @env:e1", "deny container.restart @container:e1/db")
	if doc.Revision != 2 || len(doc.Rules) != 2 || doc.Rules[1].Effect != domain.PermissionDeny {
		t.Fatalf("doc %+v", doc)
	}
	if got := f.inval.take(); !slices.Equal(got, []string{sam}) {
		t.Fatalf("invalidated %v, want the group's members", got)
	}
	if !f.can(sam, "container.restart", container("e1", "web")) || f.can(sam, "container.restart", container("e1", "db")) ||
		f.can(sam, "container.start", container("e1", "web")) {
		t.Fatal("group rules not applied")
	}
	// Stale revision, invalid and ambiguous rules.
	if _, err := f.svc.ReplaceGroupPermissions(f.ctx, ops.ID, 1, nil); !errors.Is(err, domain.ErrPermissionConflict) {
		t.Fatalf("stale: %v", err)
	}
	var re *domain.RuleError
	_, err = f.svc.ReplaceGroupPermissions(f.ctx, ops.ID, 2, rules(t, "allow container.restart @all", "deny container.restart @all", "allow users.manage @all"))
	if !errors.As(err, &re) || len(re.Problems) != 2 || re.Problems[0].Index != 1 || re.Problems[1].Index != 2 || re.Field != "rules" {
		t.Fatalf("invalid rules: %v %+v", err, re)
	}
	if _, err := f.svc.ReplaceGroupPermissions(f.ctx, "missing", 1, nil); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("missing group: %v", err)
	}
	// Moving sam to the Restricted group removes the access at once.
	if _, err := store.PatchUser(f.ctx, f.db, sam, 1, domain.UserPatch{GroupIDs: &[]string{f.def}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if f.can(sam, "container.restart", container("e1", "web")) {
		t.Fatal("old group's rules still apply after a move")
	}
	// Disabled accounts are denied everything.
	if _, err := store.PatchUser(f.ctx, f.db, sam, 2, domain.UserPatch{GroupIDs: &[]string{ops.ID}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	disabled := domain.UserDisabled
	if _, err := store.PatchUser(f.ctx, f.db, sam, 3, domain.UserPatch{Status: &disabled}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if f.can(sam, "container.restart", container("e1", "web")) {
		t.Fatal("disabled user allowed")
	}
}

func TestUserOverridesAndResetToInherit(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	f.setGroup(ops.ID, "allow container.restart @env:e1", "deny container.logs.read @all")
	sam := f.user("sam", ops.ID, false)
	f.inval.take()

	f.setUser(sam, "deny container.restart @container:e1/web", "allow container.logs.read @container:e1/web")
	if got := f.inval.take(); !slices.Equal(got, []string{sam}) {
		t.Fatalf("invalidated %v", got)
	}
	if f.can(sam, "container.restart", container("e1", "web")) || !f.can(sam, "container.restart", container("e1", "db")) {
		t.Fatal("user deny did not override the group allow")
	}
	if !f.can(sam, "container.logs.read", container("e1", "web")) || f.can(sam, "container.logs.read", container("e1", "db")) {
		t.Fatal("user allow did not override the group deny")
	}
	eff, err := f.svc.UserEffective(f.ctx, sam)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range eff.Entries {
		if e.Capability == "container.restart" && e.Scope.Kind == domain.ScopeKindResource {
			found = !e.Allowed && e.Source == "user_rule" && e.Rule != nil && e.Rule.Effect == domain.PermissionDeny && e.Reason != ""
		}
	}
	if !found || !slices.Equal(eff.GroupIDs, []string{ops.ID}) {
		t.Fatalf("effective %+v", eff)
	}
	// Reset to inherit.
	f.setUser(sam)
	if !f.can(sam, "container.restart", container("e1", "web")) || f.can(sam, "container.logs.read", container("e1", "web")) {
		t.Fatal("clearing the overrides did not restore inheritance")
	}
	// The owner's capabilities are protected.
	cur, _ := f.svc.UserPermissions(f.ctx, f.owner)
	if _, err := f.svc.ReplaceUserPermissions(f.ctx, f.owner, cur.Revision, rules(t, "deny container.exec @all")); !errors.Is(err, domain.ErrOwnerProtected) {
		t.Fatalf("owner rules: %v", err)
	}
	if _, err := f.svc.ReplaceUserPermissions(f.ctx, "nobody", 1, nil); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestGroupInvariantsAndStepUp(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	if err := f.svc.DeleteGroup(f.ctx, f.def, 1); !errors.Is(err, domain.ErrGroupIsDefault) {
		t.Fatalf("delete default: %v", err)
	}
	renamed, err := f.svc.RenameGroup(f.ctx, f.def, 1, "Newcomers")
	if err != nil || renamed.Name != "Newcomers" || !renamed.Default || renamed.Revision != 2 {
		t.Fatalf("rename default: %+v %v", renamed, err)
	}
	if _, err := f.svc.RenameGroup(f.ctx, f.def, 1, "Again"); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale rename: %v", err)
	}
	f.setGroup(ops.ID, "allow stack.read @all")
	g, err := f.svc.SelectDefaultGroup(f.ctx, ops.ID)
	if err != nil || !g.Default || g.AllowCount != 1 {
		t.Fatalf("select default: %+v %v", g, err)
	}
	if def, _ := store.DefaultGroupID(f.ctx, f.db); def != ops.ID {
		t.Fatalf("default %s", def)
	}
	// A member keeps the old default from being deleted; the owner is in
	// no group.
	rita := f.user("rita", f.def, false)
	old, _ := f.svc.GetGroup(f.ctx, f.def)
	if old.MemberCount != 1 {
		t.Fatalf("members %d, want 1 (the owner is in no group)", old.MemberCount)
	}
	if err := f.svc.DeleteGroup(f.ctx, f.def, old.Revision); !errors.Is(err, domain.ErrGroupNotEmpty) {
		t.Fatalf("delete non-empty: %v", err)
	}
	r, _ := store.GetUser(f.ctx, f.db, rita)
	if _, err := store.PatchUser(f.ctx, f.db, rita, r.Revision, domain.UserPatch{GroupIDs: &[]string{ops.ID}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	// Empty, the group is deleted; nobody's access changes.
	f.inval.take()
	if err := f.svc.DeleteGroup(f.ctx, f.def, old.Revision); err != nil {
		t.Fatalf("delete an empty group: %v", err)
	}
	if got := f.inval.take(); len(got) != 0 {
		t.Fatalf("invalidated %v", got)
	}
	empty, _ := f.svc.CreateGroup(f.ctx, "Empty")
	f.setGroup(empty.ID, "allow stack.deploy @all")
	if err := f.svc.DeleteGroup(f.ctx, empty.ID, 99); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := f.svc.DeleteGroup(f.ctx, empty.ID, empty.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GroupPermissions(f.ctx, f.db, empty.ID); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("rules of a deleted group: %v", err)
	}
	var n int
	if err := f.db.NewRaw("SELECT count(*) FROM group_permission_rules WHERE group_id = ?", empty.ID).Scan(f.ctx, &n); err != nil || n != 0 {
		t.Fatalf("rules survived the group: %d %v", n, err)
	}

	// Every change needs a recent step-up; reads do not.
	f.guard.stale = true
	for name, err := range map[string]error{
		"create": func() error { _, err := f.svc.CreateGroup(f.ctx, "X"); return err }(),
		"rename": func() error { _, err := f.svc.RenameGroup(f.ctx, ops.ID, 1, "X"); return err }(),
		"delete": f.svc.DeleteGroup(f.ctx, ops.ID, 1),
		"default": func() error {
			_, err := f.svc.SelectDefaultGroup(f.ctx, f.def)
			return err
		}(),
		"group rules": func() error { _, err := f.svc.ReplaceGroupPermissions(f.ctx, ops.ID, 2, nil); return err }(),
		"group order": func() error {
			order, _ := f.svc.GroupOrder(f.ctx)
			_, err := f.svc.ReorderGroups(f.ctx, order, order)
			return err
		}(),
		"user rules": func() error { _, err := f.svc.ReplaceUserPermissions(f.ctx, f.owner, 1, nil); return err }(),
	} {
		if !errors.Is(err, domain.ErrStepUpRequired) {
			t.Errorf("%s without step-up: %v", name, err)
		}
	}
	if _, err := f.svc.ListGroups(f.ctx); err != nil {
		t.Fatalf("read without step-up: %v", err)
	}
	f.guard.denied = true
	if _, err := f.svc.ListGroups(f.ctx); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-owner: %v", err)
	}
	if _, err := f.svc.Preview(f.ctx, permissions.PreviewRequest{UserID: f.owner}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-owner preview: %v", err)
	}
}

// TestResourceGraph: Locators resolve parents and environments, rules on a
// stack follow it to another environment, grants inside an environment or
// stack make it reachable (minimal view), and deleted resources lose
// their exact rules.
func TestResourceGraph(t *testing.T) {
	f := newFixture(t)
	stackEnv := map[string]string{"s1": "e1"}
	var mu sync.Mutex
	f.svc.RegisterLocator(catalog.TypeStack, permissions.LocatorFunc(func(_ context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		mu.Lock()
		defer mu.Unlock()
		env, ok := stackEnv[ref.ID]
		return permissions.Location{Found: ok, EnvironmentID: env}, nil
	}))
	f.svc.RegisterLocator(catalog.TypeContainer, permissions.LocatorFunc(func(_ context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		if ref.ID == "s1-web-1" {
			return permissions.Location{Found: true, EnvironmentID: ref.EnvironmentID,
				Parents: []authz.ResourceRef{{Type: catalog.TypeService, ID: authz.ServiceID("s1", "web")}, {Type: catalog.TypeStack, ID: "s1"}}}, nil
		}
		return permissions.Location{Found: true, EnvironmentID: ref.EnvironmentID}, nil
	}))
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	sam := f.user("sam", ops.ID, false)
	f.setGroup(ops.ID, "allow container.logs.read @stack:s1", "allow stack.deploy @stack:s1", "allow stack.read @env:e1")
	p := authz.Principal{Kind: authz.KindUser, UserID: sam}

	// A container without parents is located: the stack rule applies.
	web := authz.Resource{Type: catalog.TypeContainer, ID: "s1-web-1", EnvironmentID: "e1"}
	if !f.svc.Can(f.ctx, p, "container.logs.read", web).Allowed || f.svc.Can(f.ctx, p, "container.details.read", web).Allowed {
		t.Fatal("located stack-scoped container rule")
	}
	if f.svc.Can(f.ctx, p, "container.logs.read", authz.Resource{Type: catalog.TypeContainer, ID: "adhoc", EnvironmentID: "e1"}).Allowed {
		t.Fatal("stack rule applied to a standalone container")
	}
	c := authz.For(f.ctx, f.svc, p)
	views := func() (e1, e2 authz.Level) {
		return authz.ViewOf(c, authz.EnvironmentResource("e1")).Level, authz.ViewOf(c, authz.EnvironmentResource("e2")).Level
	}
	if e1, e2 := views(); e1 != authz.Minimal || e2 != authz.Hidden {
		t.Fatalf("environment views %v %v", e1, e2)
	}
	stack := authz.Resource{Type: catalog.TypeStack, ID: "s1", EnvironmentID: "e1"}
	if v := authz.ViewOf(c, stack); v.Level != authz.Full || !v.Has("stack.deploy") {
		t.Fatalf("stack view %+v", v)
	}
	// Migration: the stack moves to e2. Stack rules follow it; the
	// environment rule (stack.read @env:e1) does not.
	mu.Lock()
	stackEnv["s1"] = "e2"
	mu.Unlock()
	c = authz.For(f.ctx, f.svc, p)
	// e2 becomes reachable through the stack rules; e1 stays reachable
	// only through its own environment rule (future stacks there).
	if e1, e2 := views(); e1 != authz.Minimal || e2 != authz.Minimal {
		t.Fatalf("environment views after the move %v %v", e1, e2)
	}
	moved := authz.Resource{Type: catalog.TypeStack, ID: "s1", EnvironmentID: "e2"}
	if v := authz.ViewOf(c, moved); v.Level != authz.Minimal || !v.Has("stack.deploy") || v.Has("stack.read") {
		t.Fatalf("moved stack view %+v", v)
	}

	// ForgetResource removes exact rules on a deleted resource only.
	f.setUser(sam, "allow container.restart @container:e1/web", "allow container.restart @container:e1/db")
	f.inval.take()
	n, err := f.svc.ForgetResource(f.ctx, authz.ResourceRef{Type: catalog.TypeContainer, ID: "web", EnvironmentID: "e1"})
	if err != nil || n != 1 {
		t.Fatalf("forget: %d %v", n, err)
	}
	if got := f.inval.take(); !slices.Equal(got, []string{sam}) {
		t.Fatalf("invalidated %v", got)
	}
	if f.can(sam, "container.restart", container("e1", "web")) || !f.can(sam, "container.restart", container("e1", "db")) {
		t.Fatal("forget removed the wrong rules")
	}
	doc, _ := f.svc.UserPermissions(f.ctx, sam)
	if doc.Revision != 3 || len(doc.Rules) != 1 {
		t.Fatalf("user document after forget %+v", doc)
	}
	if _, err := f.svc.ForgetResource(f.ctx, authz.ResourceRef{Type: catalog.TypeJob, ID: "j"}); err == nil {
		t.Fatal("forget accepted an unscopable type")
	}
}

func TestAPITokenScopeIntersection(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	sam := f.user("sam", ops.ID, false)
	f.setGroup(ops.ID, "allow container.restart @all", "allow container.logs.read @all")
	tok := authz.Principal{Kind: authz.KindAPIToken, UserID: sam, TokenID: "t1"}
	web := container("e1", "web")
	if f.svc.Can(f.ctx, tok, "container.restart", web).Allowed {
		t.Fatal("token allowed without a scope source (#31 not wired)")
	}
	f.svc.SetTokenScopes(func(_ context.Context, id string) ([]domain.PermissionRule, bool, error) {
		return rules(t, "allow container.restart @container:e1/web"), id == "t1", nil
	})
	if !f.svc.Can(f.ctx, tok, "container.restart", web).Allowed ||
		f.svc.Can(f.ctx, tok, "container.logs.read", web).Allowed ||
		f.svc.Can(f.ctx, tok, "container.restart", container("e1", "db")).Allowed {
		t.Fatal("token scope ∩ user permissions")
	}
	f.setUser(sam, "deny container.restart @env:e1")
	if f.svc.Can(f.ctx, tok, "container.restart", web).Allowed {
		t.Fatal("narrowing the user did not narrow the token")
	}
	other := authz.Principal{Kind: authz.KindAPIToken, UserID: sam, TokenID: "revoked"}
	if f.svc.Can(f.ctx, other, "container.logs.read", web).Allowed {
		t.Fatal("unknown token allowed")
	}
}

func TestPreviewAndEffective(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	f.setGroup(ops.ID, "allow container.restart @all", "deny container.restart @env:e2")
	sam := f.user("sam", f.def, false)
	web := container("e1", "web")
	check := func(req permissions.PreviewRequest) permissions.Preview {
		t.Helper()
		req.Checks = []permissions.PreviewCheck{{Capability: "container.restart", Resource: web},
			{Capability: "container.restart", Resource: container("e2", "web")}}
		p, err := f.svc.Preview(f.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// As stored: Restricted, nothing allowed.
	p := check(permissions.PreviewRequest{UserID: sam})
	if p.Checks[0].Allowed || p.Checks[0].Source != "default_deny" || len(p.Effective.Entries) != 0 {
		t.Fatalf("stored preview %+v", p)
	}
	// Previewing a move to Ops: exact environment beats all.
	inOps := &[]string{ops.ID}
	p = check(permissions.PreviewRequest{UserID: sam, GroupIDs: inOps})
	if !p.Checks[0].Allowed || p.Checks[1].Allowed || p.Checks[1].Rule == nil || p.Checks[1].Rule.Scope.EnvironmentID != "e2" ||
		p.Checks[1].GroupID != ops.ID || !slices.Equal(p.Effective.GroupIDs, []string{ops.ID}) || len(p.Effective.Entries) != 2 {
		t.Fatalf("move preview %+v", p)
	}
	// Unsaved user overrides and an API token scope.
	over := rules(t, "deny container.restart @container:e1/web")
	p = check(permissions.PreviewRequest{UserID: sam, GroupIDs: inOps, UserRules: &over})
	if p.Checks[0].Allowed || p.Checks[0].Source != "user_rule" {
		t.Fatalf("override preview %+v", p)
	}
	scope := rules(t, "allow container.restart @env:e2")
	p = check(permissions.PreviewRequest{UserID: sam, GroupIDs: inOps, TokenScope: &scope})
	if p.Checks[0].Allowed || p.Checks[0].Source != "token_scope" || p.Checks[1].Allowed {
		t.Fatalf("token preview %+v", p)
	}
	bad := rules(t, "deny container.restart @all")
	var re *domain.RuleError
	if _, err := f.svc.Preview(f.ctx, permissions.PreviewRequest{UserID: sam, TokenScope: &bad}); !errors.As(err, &re) || re.Field != "tokenScope" {
		t.Fatalf("deny in a token scope: %v", err)
	}
	// A group's typical member; nothing was stored.
	p = check(permissions.PreviewRequest{GroupIDs: inOps})
	if !p.Checks[0].Allowed {
		t.Fatalf("group preview %+v", p)
	}
	// Several groups decide in priority order: a higher group with
	// unsaved rules first.
	blocked, _ := f.svc.CreateGroup(f.ctx, "Blocked")
	order, _ := f.svc.GroupOrder(f.ctx)
	if _, err := f.svc.ReorderGroups(f.ctx, order, []string{blocked.ID, ops.ID, f.def}); err != nil {
		t.Fatal(err)
	}
	unsaved := rules(t, "deny container.restart @env:e1")
	both := &[]string{ops.ID, blocked.ID}
	p = check(permissions.PreviewRequest{UserID: sam, GroupIDs: both, RulesGroupID: blocked.ID, GroupRules: &unsaved})
	if p.Checks[0].Allowed || p.Checks[0].GroupID != blocked.ID || !slices.Equal(p.Effective.GroupIDs, []string{blocked.ID, ops.ID}) {
		t.Fatalf("several groups preview %+v", p)
	}
	var fe *domain.FieldError
	if _, err := f.svc.Preview(f.ctx, permissions.PreviewRequest{UserID: sam, GroupIDs: both, GroupRules: &unsaved}); !errors.As(err, &fe) ||
		fe.Field != "rulesGroupId" {
		t.Fatalf("ambiguous group rules: %v", err)
	}
	if _, err := f.svc.Preview(f.ctx, permissions.PreviewRequest{GroupIDs: &[]string{"missing"}}); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if doc, _ := f.svc.UserPermissions(f.ctx, sam); len(doc.Rules) != 0 {
		t.Fatal("preview stored rules")
	}
	mine, err := f.svc.Mine(f.ctx, authz.Principal{Kind: authz.KindUser, UserID: f.owner})
	if err != nil || !mine.Owner {
		t.Fatalf("owner effective %+v %v", mine, err)
	}
}

// TestQueuedJobRecheckedAtDispatch: the job engine authorizes a manual job
// with the permission service when it is requested and again when it is
// dispatched; a revoked grant fails the queued job (authorization_revoked),
// while scheduled work runs as the service identity.
func TestQueuedJobRecheckedAtDispatch(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	sam := f.user("sam", ops.ID, false)
	f.setGroup(ops.ID, "allow container.restart @container:e1/web")
	disp := jobstest.New()
	eng, err := jobs.New(jobs.Options{DB: f.db, Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Dispatcher: disp, Authorizer: f.svc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	p := authz.Principal{Kind: authz.KindUser, UserID: sam}
	target := []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
	if _, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerStart, Principal: p, EnvironmentID: "e1", Targets: target}); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("start with restart-only: %v", err)
	}
	if _, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1",
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "db"}}}); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("restart of another container: %v", err)
	}
	j, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1", Targets: target})
	if err != nil {
		t.Fatal(err)
	}
	sched, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: authz.Service(), EnvironmentID: "e1", Targets: target})
	if err != nil {
		t.Fatal(err)
	}
	// The job is visible to its initiator through the kind's capability.
	jr := authz.JobResource(j)
	if !f.can(sam, "job.read", jr) || !f.can(sam, "job.cancel", jr) {
		t.Fatal("restart-only user cannot follow the restart")
	}
	f.setGroup(ops.ID) // revoke
	if f.can(sam, "job.read", jr) {
		t.Fatal("job visible after the grant was revoked")
	}
	disp.Connect("e1")
	if err := eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := eng.Get(f.ctx, j.ID)
	if got.State != domain.JobFailed || got.ErrorClass != domain.ErrorAuthorizationRevoked {
		t.Fatalf("manual job after revocation %+v", got)
	}
	if got, _ := eng.Get(f.ctx, sched.ID); got.State != domain.JobDispatched {
		t.Fatalf("scheduled job %+v", got)
	}
}

// TestReorderGroups: the order decides for members of several groups; a
// reorder is compare-and-set on the order, names every group once and
// ends the streams of members of several groups only.
func TestReorderGroups(t *testing.T) {
	f := newFixture(t)
	admins, _ := f.svc.CreateGroup(f.ctx, "Admins")
	limited, _ := f.svc.CreateGroup(f.ctx, "Limited")
	f.setGroup(admins.ID, "allow container.restart @all")
	f.setGroup(limited.ID, "deny container.restart @container:e1/web")
	both := f.user("both", admins.ID, false)
	r, _ := store.GetUser(f.ctx, f.db, both)
	if _, err := store.PatchUser(f.ctx, f.db, both, r.Revision, domain.UserPatch{GroupIDs: &[]string{admins.ID, limited.ID}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	f.user("one", limited.ID, false)
	web := container("e1", "web")
	if !f.can(both, "container.restart", web) {
		t.Fatal("the higher group (Admins) does not decide")
	}
	order, err := f.svc.GroupOrder(f.ctx)
	if err != nil || !slices.Equal(order, []string{f.def, admins.ID, limited.ID}) {
		t.Fatalf("order %v %v", order, err)
	}
	f.inval.take()
	want := []string{limited.ID, f.def, admins.ID}
	gs, err := f.svc.ReorderGroups(f.ctx, order, want)
	if err != nil || len(gs) != 3 || gs[0].ID != limited.ID {
		t.Fatalf("reorder %+v %v", gs, err)
	}
	if f.can(both, "container.restart", web) || !f.can(both, "container.restart", container("e1", "db")) {
		t.Fatal("after the reorder Limited does not decide first")
	}
	if got := f.inval.take(); !slices.Equal(got, []string{both}) {
		t.Fatalf("invalidated %v, want the member of several groups only", got)
	}
	if _, err := f.svc.ReorderGroups(f.ctx, order, want); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale order: %v", err)
	}
	if _, err := f.svc.ReorderGroups(f.ctx, want, want[:2]); !errors.Is(err, domain.ErrGroupOrderStale) {
		t.Fatalf("incomplete order: %v", err)
	}
	// The same order again changes nothing.
	if _, err := f.svc.ReorderGroups(f.ctx, want, want); err != nil {
		t.Fatal(err)
	}
	if got := f.inval.take(); len(got) != 0 {
		t.Fatalf("an unchanged order invalidated %v", got)
	}
}
