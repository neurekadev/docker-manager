package store_test

import (
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestDefaultGroupInvariant: a fresh database has exactly one default
// group, "Restricted", with no grants; it cannot be deleted while it is
// the default and the default cannot be unset.
func TestDefaultGroupInvariant(t *testing.T) {
	db := storetest.Migrated(t)
	ctx := testutil.Context(t)
	id, err := store.DefaultGroupID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.GetGroup(ctx, db, id)
	if err != nil || g.Name != domain.RestrictedGroupName || !g.Default {
		t.Fatalf("default group %+v %v", g, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM groups WHERE id = ?", id); err == nil {
		t.Fatal("deleted the default group")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM default_group"); err == nil {
		t.Fatal("removed the default group designation")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO default_group (singleton, group_id) VALUES (2, ?)", id); err == nil {
		t.Fatal("second default group row accepted")
	}
	if _, err := db.ExecContext(ctx, "UPDATE default_group SET group_id = 'missing'"); err == nil {
		t.Fatal("default group pointing at no group accepted")
	}
	// Renaming keeps the invariant (#17 edits groups).
	if _, err := db.ExecContext(ctx, "UPDATE groups SET name = 'Guests' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.DefaultGroupID(ctx, db); got != id {
		t.Fatal("rename changed the default")
	}
	var n int
	if err := db.NewRaw("SELECT count(*) FROM groups").Scan(ctx, &n); err != nil || n != 1 {
		t.Fatalf("groups %d %v", n, err)
	}
}

func TestOneOwnerAndUniqueUsernames(t *testing.T) {
	db := storetest.Migrated(t)
	ctx := testutil.Context(t)
	group, _ := store.DefaultGroupID(ctx, db)
	now := testutil.Epoch
	mk := func(name string, owner bool) error {
		_, err := store.CreateUser(ctx, db, domain.NewUser{ID: ids.New(), Username: name, Owner: owner, GroupID: group,
			WebAuthnHandle: []byte(ids.New()), CreatedAt: now})
		return err
	}
	if err := mk("owner", true); err != nil {
		t.Fatal(err)
	}
	if err := mk("owner2", true); !errors.Is(err, domain.ErrSetupComplete) {
		t.Fatalf("second owner: %v", err)
	}
	if err := mk("OWNER", false); !errors.Is(err, domain.ErrUsernameTaken) {
		t.Fatalf("case-insensitive duplicate: %v", err)
	}
	if id, ok, err := store.OwnerID(ctx, db); !ok || err != nil || id == "" {
		t.Fatalf("owner id %q %v %v", id, ok, err)
	}
}
