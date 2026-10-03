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

// TestNoGroupsAtFirst: a fresh database has no groups and no default group
// (#233): new accounts are in none and denied everything.
func TestNoGroupsAtFirst(t *testing.T) {
	db := storetest.Migrated(t)
	ctx := testutil.Context(t)
	gs, err := store.ListGroups(ctx, db)
	if err != nil || len(gs) != 0 {
		t.Fatalf("groups %+v %v", gs, err)
	}
	var n int
	if err := db.NewRaw("SELECT count(*) FROM sqlite_schema WHERE name = 'default_group'").Scan(ctx, &n); err != nil || n != 0 {
		t.Fatalf("default_group table %d %v", n, err)
	}
}

func TestOneOwnerAndUniqueUsernames(t *testing.T) {
	db := storetest.Migrated(t)
	ctx := testutil.Context(t)
	now := testutil.Epoch
	mk := func(name string, owner bool) error {
		_, err := store.CreateUser(ctx, db, domain.NewUser{ID: ids.New(), Username: name, Owner: owner,
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
