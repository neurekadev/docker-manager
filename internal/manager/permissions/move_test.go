package permissions_test

import (
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/permissions"
)

// TestMoveImpact (#35 permission preview): moving stack s1 from e1 to e2,
// stack- and service-scoped rules follow the stack, environment rules do
// not, exact container rules stay with the source's containers; the owner
// never changes; users without a change are not listed.
func TestMoveImpact(t *testing.T) {
	f := newFixture(t)
	ops, _ := f.svc.CreateGroup(f.ctx, "Ops")
	stackRules := f.user("stackrules", ops.ID, false) // stack-scoped only: unchanged
	f.setGroup(ops.ID, "allow stack.deploy @stack:s1", "allow container.logs.read @stack:s1")
	envSrc := f.user("envsrc", f.def, false) // environment rules on the source: loses
	f.setUser(envSrc, "allow stack.read @env:e1", "allow container.restart @env:e1")
	envDst := f.user("envdst", f.def, false) // environment rules on the destination: gains
	f.setUser(envDst, "allow stack.read @env:e2")
	exact := f.user("exact", f.def, false) // exact rule on the source container: loses
	f.setUser(exact, "allow container.logs.read @container:e1/shop-web-1")
	denied := f.user("denied", ops.ID, false) // a user deny on the stack follows it too: unchanged
	f.setUser(denied, "deny stack.deploy @stack:s1")
	_ = stackRules

	stackAt := func(env string) authz.Resource {
		return authz.Resource{Type: catalog.TypeStack, ID: "s1", EnvironmentID: env, Parents: []authz.ResourceRef{}}
	}
	web := func(env string) authz.Resource {
		return authz.Resource{Type: catalog.TypeContainer, ID: "shop-web-1", EnvironmentID: env,
			Parents: []authz.ResourceRef{{Type: catalog.TypeService, ID: authz.ServiceID("s1", "web")}, {Type: catalog.TypeStack, ID: "s1"}}}
	}
	var checks []permissions.MoveCheck
	for _, c := range []string{"stack.read", "stack.deploy"} {
		checks = append(checks, permissions.MoveCheck{Capability: c, Before: stackAt("e1"), After: stackAt("e2")})
	}
	for _, c := range []string{"container.logs.read", "container.restart"} {
		checks = append(checks, permissions.MoveCheck{Capability: c, Before: web("e1"), After: web("e2"), Label: c + " (web)"})
	}
	got, err := f.svc.MoveImpact(f.ctx, checks)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]permissions.AccessChange{}
	for _, ch := range got {
		by[ch.UserID] = ch
	}
	if len(by) != 3 {
		t.Fatalf("changes for %d users, want 3: %+v", len(by), got)
	}
	if ch := by[envSrc]; !slices.Equal(ch.Lost, []string{"stack.read", "container.restart (web)"}) || len(ch.Gained) != 0 {
		t.Errorf("source environment user %+v", ch)
	}
	if ch := by[envDst]; !slices.Equal(ch.Gained, []string{"stack.read"}) || len(ch.Lost) != 0 {
		t.Errorf("destination environment user %+v", ch)
	}
	if ch := by[exact]; !slices.Equal(ch.Lost, []string{"container.logs.read (web)"}) {
		t.Errorf("exact container rule user %+v", ch)
	}
	for _, u := range []string{f.owner, stackRules, denied} {
		if _, ok := by[u]; ok {
			t.Errorf("user %s listed although nothing changes", u)
		}
	}
}
