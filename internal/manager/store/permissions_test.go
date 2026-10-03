package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

type groupFixture struct {
	t   *testing.T
	ctx context.Context
	db  *bun.DB
	def string
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	f := &groupFixture{t: t, ctx: testutil.Context(t), db: storetest.Migrated(t)}
	var err error
	if f.def, err = store.DefaultGroupID(f.ctx, f.db); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *groupFixture) group(name string) domain.GroupInfo {
	f.t.Helper()
	g, err := store.CreateGroup(f.ctx, f.db, ids.New(), name, testutil.Epoch)
	if err != nil {
		f.t.Fatal(err)
	}
	return g
}

func (f *groupFixture) user(name, group string, owner bool) string {
	f.t.Helper()
	id := ids.New()
	if _, err := store.CreateUser(f.ctx, f.db, domain.NewUser{ID: id, Username: name, Owner: owner, GroupID: group,
		WebAuthnHandle: []byte(id), CreatedAt: testutil.Epoch}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *groupFixture) members(id string) int {
	f.t.Helper()
	g, err := store.GetGroupInfo(f.ctx, f.db, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return g.MemberCount
}

// TestGroupMemberCountExcludesOwner: group rules never apply to the owner,
// so the owner's account is never counted as a member.
func TestGroupMemberCountExcludesOwner(t *testing.T) {
	f := newGroupFixture(t)
	f.user("owner", f.def, true)
	if n := f.members(f.def); n != 0 {
		t.Fatalf("default group with only the owner has %d members", n)
	}
	f.user("rita", f.def, false)
	ops := f.group("Ops")
	f.user("sam", ops.ID, false)
	f.user("tom", ops.ID, false)
	gs, err := store.ListGroups(f.ctx, f.db)
	if err != nil || len(gs) != 2 {
		t.Fatalf("groups %+v %v", gs, err)
	}
	for _, g := range gs {
		want := map[string]int{f.def: 1, ops.ID: 2}[g.ID]
		if g.MemberCount != want {
			t.Errorf("group %s: %d members, want %d", g.Name, g.MemberCount, want)
		}
	}
}

// TestDeleteGroupNeedsAnEmptyNonDefaultGroup: members, the default group
// and stale revisions refuse a deletion; the owner is in no group, so it
// never blocks one.
func TestDeleteGroupNeedsAnEmptyNonDefaultGroup(t *testing.T) {
	f := newGroupFixture(t)
	ops := f.group("Ops")
	f.user("owner", ops.ID, true)
	rita := f.user("rita", ops.ID, false)
	if err := store.DeleteGroup(f.ctx, f.db, ops.ID, ops.Revision); !errors.Is(err, domain.ErrGroupNotEmpty) {
		t.Fatalf("delete with a member: %v", err)
	}
	r, err := store.GetUser(f.ctx, f.db, rita)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchUser(f.ctx, f.db, rita, r.Revision, domain.UserPatch{GroupIDs: &[]string{f.def}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGroup(f.ctx, f.db, ops.ID, ops.Revision+1); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := store.DeleteGroup(f.ctx, f.db, f.def, 1); !errors.Is(err, domain.ErrGroupIsDefault) {
		t.Fatalf("delete the default: %v", err)
	}
	if err := store.DeleteGroup(f.ctx, f.db, ops.ID, ops.Revision); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetGroupInfo(f.ctx, f.db, ops.ID); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("deleted group: %v", err)
	}
}

// TestUsersInSeveralGroups: memberships are replaced as a whole, read in
// priority order, roll back with an unknown group, never include the
// owner and end with the account.
func TestUsersInSeveralGroups(t *testing.T) {
	f := newGroupFixture(t)
	ops := f.group("Ops")
	owner := f.user("owner", f.def, true)
	rita := f.user("rita", f.def, false)
	if u, err := store.GetUser(f.ctx, f.db, owner); err != nil || len(u.GroupIDs) != 0 {
		t.Fatalf("owner in groups %v (%v)", u.GroupIDs, err)
	}
	if _, err := store.ReplaceGroupPermissions(f.ctx, f.db, ops.ID, 1, []domain.PermissionRule{{Capability: "stack.read",
		Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: domain.ScopeKindInstance}}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetUser(f.ctx, f.db, rita)
	if err != nil || !slices.Equal(r.GroupIDs, []string{f.def}) {
		t.Fatalf("rita in %v (%v)", r.GroupIDs, err)
	}
	u, err := store.PatchUser(f.ctx, f.db, rita, r.Revision, domain.UserPatch{GroupIDs: &[]string{ops.ID, f.def}}, testutil.Epoch)
	if err != nil || !slices.Equal(u.GroupIDs, []string{f.def, ops.ID}) || u.Revision != r.Revision+1 {
		t.Fatalf("after the patch %+v %v", u, err)
	}
	ps, err := store.PermissionSubject(f.ctx, f.db, rita)
	if err != nil || len(ps.Groups) != 2 || ps.Groups[0].GroupID != f.def || ps.Groups[1].Name != "Ops" || len(ps.Groups[1].Rules) != 1 {
		t.Fatalf("subject %+v %v", ps, err)
	}
	if !slices.Equal(ps.GroupIDs(), []string{f.def, ops.ID}) {
		t.Fatalf("subject groups %v", ps.GroupIDs())
	}
	users, err := store.ListUsers(f.ctx, f.db, "", 10)
	if err != nil || len(users) != 2 {
		t.Fatalf("users %+v %v", users, err)
	}
	for _, x := range users {
		if want := map[string]int{owner: 0, rita: 2}[x.ID]; len(x.GroupIDs) != want {
			t.Errorf("%s in %v", x.Username, x.GroupIDs)
		}
	}
	if _, err := store.PatchUser(f.ctx, f.db, rita, u.Revision, domain.UserPatch{GroupIDs: &[]string{ops.ID, "missing"}}, testutil.Epoch); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if after, _ := store.GetUser(f.ctx, f.db, rita); after.Revision != u.Revision || len(after.GroupIDs) != 2 {
		t.Fatalf("a refused patch changed %+v", after)
	}
	if u, err = store.PatchUser(f.ctx, f.db, rita, u.Revision, domain.UserPatch{GroupIDs: &[]string{}}, testutil.Epoch); err != nil || len(u.GroupIDs) != 0 {
		t.Fatalf("no groups: %+v %v", u, err)
	}
	if n := f.members(ops.ID); n != 0 {
		t.Fatalf("ops has %d members", n)
	}
	if _, err := store.PatchUser(f.ctx, f.db, rita, u.Revision, domain.UserPatch{GroupIDs: &[]string{ops.ID}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteUser(f.ctx, f.db, rita); err != nil {
		t.Fatal(err)
	}
	if n := f.members(ops.ID); n != 0 {
		t.Fatalf("a deleted user is still a member (%d)", n)
	}
}

// TestReorderGroups: new groups go last; a reorder names every group once
// and changes the order lists and subjects are read in.
func TestReorderGroups(t *testing.T) {
	f := newGroupFixture(t)
	a, b := f.group("A"), f.group("B")
	if a.Position != 1 || b.Position != 2 {
		t.Fatalf("positions %d %d", a.Position, b.Position)
	}
	order, err := store.GroupOrder(f.ctx, f.db)
	if err != nil || !slices.Equal(order, []string{f.def, a.ID, b.ID}) {
		t.Fatalf("order %v %v", order, err)
	}
	for _, bad := range [][]string{{b.ID, a.ID}, {b.ID, a.ID, a.ID}, {b.ID, a.ID, "missing"}, {b.ID, a.ID, f.def, f.def}} {
		if err := store.ReorderGroups(f.ctx, f.db, bad, testutil.Epoch); !errors.Is(err, domain.ErrGroupOrderStale) {
			t.Errorf("order %v: %v", bad, err)
		}
	}
	if err := store.ReorderGroups(f.ctx, f.db, []string{b.ID, f.def, a.ID}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	gs, err := store.ListGroups(f.ctx, f.db)
	if err != nil || len(gs) != 3 || gs[0].ID != b.ID || gs[0].Position != 0 || gs[2].ID != a.ID {
		t.Fatalf("groups %+v %v", gs, err)
	}
	rita := f.user("rita", a.ID, false)
	r, _ := store.GetUser(f.ctx, f.db, rita)
	if _, err := store.PatchUser(f.ctx, f.db, rita, r.Revision, domain.UserPatch{GroupIDs: &[]string{a.ID, b.ID}}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if ps, _ := store.PermissionSubject(f.ctx, f.db, rita); !slices.Equal(ps.GroupIDs(), []string{b.ID, a.ID}) {
		t.Fatalf("subject groups %v", ps.GroupIDs())
	}
	if c := f.group("C"); c.Position != 3 {
		t.Fatalf("a new group at %d", c.Position)
	}
}
