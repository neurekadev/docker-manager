package permissions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs/jobstest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// tokenSource is an in-memory permissions.TokenScopes (the identity
// service in production) whose tokens can be revoked.
type tokenSource struct {
	mu      sync.Mutex
	scopes  map[string][]domain.PermissionRule
	revoked map[string]bool
}

func (s *tokenSource) scopesOf(_ context.Context, id string) ([]domain.PermissionRule, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.scopes[id]
	return r, ok && !s.revoked[id], nil
}

func (s *tokenSource) revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[id] = true
}

// TestTokenScopeIntersectsCurrentPermissions: an API token principal is
// evaluated as its scope ∩ its user's current rules; unknown or revoked
// tokens deny everything; the owner's tokens are limited to their scope.
func TestTokenScopeIntersectsCurrentPermissions(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	rita := f.user("rita", ops.ID, false)
	f.setGroup(ops.ID, "allow container.restart @env:e1", "allow container.logs.read @env:e1")
	src := &tokenSource{scopes: map[string][]domain.PermissionRule{
		"t-web":   rules(t, "allow container.restart @container:e1/web"),
		"t-wide":  rules(t, "allow container.restart @all", "allow container.exec @all"),
		"t-owner": rules(t, "allow container.restart @container:e1/web"),
	}, revoked: map[string]bool{}}
	f.svc.SetTokenScopes(src.scopesOf)
	web := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	db := authz.Resource{Type: "container", ID: "db", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	e2 := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e2", Parents: []authz.ResourceRef{}}
	tok := func(id, user string) authz.Principal {
		return authz.Principal{Kind: authz.KindAPIToken, UserID: user, TokenID: id}
	}
	can := func(p authz.Principal, capability string, r authz.Resource) bool {
		return f.svc.Can(f.ctx, p, capability, r).Allowed
	}
	for _, c := range []struct {
		p          authz.Principal
		capability string
		r          authz.Resource
		want       bool
	}{
		{tok("t-web", rita), "container.restart", web, true},
		{tok("t-web", rita), "container.restart", db, false},      // outside the token
		{tok("t-web", rita), "container.logs.read", web, false},   // the user has it, the token not
		{tok("t-wide", rita), "container.restart", db, true},      // wide token, user grant in e1
		{tok("t-wide", rita), "container.restart", e2, false},     // the user has nothing in e2
		{tok("t-wide", rita), "container.exec", web, false},       // the user lacks exec
		{tok("t-owner", f.owner), "container.restart", web, true}, // the owner's token: its scope
		{tok("t-owner", f.owner), "container.restart", db, false}, // ... and nothing more
		{tok("t-unknown", rita), "container.restart", web, false}, // unknown token
		{authz.Principal{Kind: authz.KindUser, UserID: rita}, "container.logs.read", web, true},
	} {
		if got := can(c.p, c.capability, c.r); got != c.want {
			t.Errorf("%s %s on %s/%s: %v, want %v", c.p.Key(), c.capability, c.r.EnvironmentID, c.r.ID, got, c.want)
		}
	}
	src.revoke("t-web")
	if can(tok("t-web", rita), "container.restart", web) {
		t.Fatal("revoked token still allowed")
	}
	// Narrowing the group narrows the token at once.
	f.setGroup(ops.ID, "allow container.logs.read @env:e1")
	if can(tok("t-wide", rita), "container.restart", db) {
		t.Fatal("token kept a grant its user lost")
	}
}

// TestQueuedTokenJobRecheckedAtDispatch (#26, #31): a job requested with an
// API token is authorized with the token scope ∩ the user's rules at
// request and again at dispatch; revoking the token fails the queued job
// (authorization_revoked).
func TestQueuedTokenJobRecheckedAtDispatch(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	sam := f.user("sam", ops.ID, false)
	f.setGroup(ops.ID, "allow container.restart @env:e1", "allow container.start @env:e1")
	src := &tokenSource{scopes: map[string][]domain.PermissionRule{"t-1": rules(t, "allow container.restart @container:e1/web")},
		revoked: map[string]bool{}}
	f.svc.SetTokenScopes(src.scopesOf)
	disp := jobstest.New()
	eng, err := jobs.New(jobs.Options{DB: f.db, Clock: testutil.FakeClock(), Logger: testutil.Logger(t), Dispatcher: disp, Authorizer: f.svc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	p := authz.Principal{Kind: authz.KindAPIToken, UserID: sam, TokenID: "t-1"}
	web := []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
	if _, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerStart, Principal: p, EnvironmentID: "e1", Targets: web}); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("start outside the token scope: %v", err)
	}
	j, _, err := eng.Enqueue(f.ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1", Targets: web})
	if err != nil {
		t.Fatal(err)
	}
	if j.Origin != domain.OriginAPIToken || j.InitiatorTokenID != "t-1" || j.InitiatorUserID != sam {
		t.Fatalf("origin %+v", j)
	}
	src.revoke("t-1")
	disp.Connect("e1")
	if err := eng.DispatchPending(f.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := eng.Get(f.ctx, j.ID)
	if got.State != domain.JobFailed || got.ErrorClass != domain.ErrorAuthorizationRevoked {
		t.Fatalf("token job after revocation %+v", got)
	}
}

// TestValidateTokenScope: a new token's scope must consist of grantable
// capabilities at compatible scopes that the user holds now.
func TestValidateTokenScope(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	rita := f.user("rita", ops.ID, false)
	f.setGroup(ops.ID, "allow container.restart @stack:s1", "allow environment.read @env:e1", "deny environment.read @env:e2")
	ok := [][]string{
		{"allow container.restart @stack:s1"},
		{"allow container.restart @service:s1/web"},
		{"allow environment.read @env:e1"},
	}
	for _, sc := range ok {
		if err := f.svc.ValidateTokenScope(f.ctx, rita, rules(t, sc...)); err != nil {
			t.Errorf("%v refused: %v", sc, err)
		}
	}
	for _, c := range []struct {
		scope []string
		want  string
	}{
		{[]string{"allow container.restart @all"}, "you do not hold container.restart"},
		{[]string{"allow environment.read @env:e2"}, "you do not hold environment.read"},
		{[]string{"allow users.manage @all"}, "reserved to the instance owner"},
		{[]string{"deny environment.read @env:e1"}, "allow grants only"},
		{[]string{"allow environment.read @env:e1", "allow environment.read @env:e1"}, "duplicates"},
	} {
		err := f.svc.ValidateTokenScope(f.ctx, rita, rules(t, c.scope...))
		var re *domain.RuleError
		if !errors.As(err, &re) || re.Field != "scopes" || len(re.Problems) == 0 || !strings.Contains(re.Problems[len(re.Problems)-1].Message, c.want) {
			t.Errorf("%v: %v (%+v), want %q", c.scope, err, re, c.want)
		}
	}
	// The owner holds every grantable capability, never owner-only ones.
	if err := f.svc.ValidateTokenScope(f.ctx, f.owner, rules(t, "allow container.exec @all", "allow audit.read @all")); err != nil {
		t.Fatalf("owner scope: %v", err)
	}
	if err := f.svc.ValidateTokenScope(f.ctx, f.owner, rules(t, "allow groups.manage @all")); err == nil {
		t.Fatal("owner-only capability in a token scope")
	}
}
