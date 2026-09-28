package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
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

// deleteGroup runs store.DeleteGroup in a transaction, as the service does.
func (f *groupFixture) deleteGroup(id string, revision int64) (movedOwner, toGroup string, err error) {
	err = f.db.RunInTx(f.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		movedOwner, toGroup, err = store.DeleteGroup(ctx, tx, id, revision, testutil.Epoch)
		return err
	})
	return movedOwner, toGroup, err
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

// TestDeleteGroupMovesTheOwnerToTheDefault: the owner's account never
// blocks deleting a group; it moves to the default group in the same
// transaction. Other members, the default group and stale revisions still
// refuse the deletion, and then the owner stays where it is.
func TestDeleteGroupMovesTheOwnerToTheDefault(t *testing.T) {
	f := newGroupFixture(t)
	ops := f.group("Ops")
	owner := f.user("owner", ops.ID, true)
	rita := f.user("rita", ops.ID, false)
	before, err := store.GetUser(f.ctx, f.db, owner)
	if err != nil {
		t.Fatal(err)
	}
	groupOf := func(id string) string {
		u, err := store.GetUser(f.ctx, f.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return u.GroupID
	}

	if _, _, err := f.deleteGroup(ops.ID, ops.Revision); !errors.Is(err, domain.ErrGroupNotEmpty) {
		t.Fatalf("delete with a member: %v", err)
	}
	r, err := store.GetUser(f.ctx, f.db, rita)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchUser(f.ctx, f.db, rita, r.Revision, domain.UserPatch{GroupID: &f.def}, testutil.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.deleteGroup(ops.ID, ops.Revision+1); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if _, _, err := f.deleteGroup(f.def, 1); !errors.Is(err, domain.ErrGroupIsDefault) {
		t.Fatalf("delete the default: %v", err)
	}
	if g := groupOf(owner); g != ops.ID {
		t.Fatalf("a refused deletion moved the owner to %s", g)
	}

	moved, to, err := f.deleteGroup(ops.ID, ops.Revision)
	if err != nil || moved != owner || to != f.def {
		t.Fatalf("delete: moved %q to %q, %v", moved, to, err)
	}
	after, err := store.GetUser(f.ctx, f.db, owner)
	if err != nil || after.GroupID != f.def || after.Revision != before.Revision+1 {
		t.Fatalf("owner after the delete %+v %v", after, err)
	}
	if _, err := store.GetGroupInfo(f.ctx, f.db, ops.ID); !errors.Is(err, domain.ErrGroupNotFound) {
		t.Fatalf("deleted group: %v", err)
	}
	if n := f.members(f.def); n != 1 {
		t.Fatalf("default group has %d members, want rita only", n)
	}

	// A group without the owner is deleted without moving anyone.
	empty := f.group("Empty")
	moved, to, err = f.deleteGroup(empty.ID, empty.Revision)
	if err != nil || moved != "" || to != "" {
		t.Fatalf("delete an empty group: moved %q to %q, %v", moved, to, err)
	}
	if g := groupOf(owner); g != f.def {
		t.Fatalf("owner moved to %s", g)
	}
}
