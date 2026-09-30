package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/store/storetest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestUserSessionStore: signed-in devices round-trip, list only the
// current session epoch (most recently active first), are deleted per
// user, and the sweep removes ended ones by their own limits.
func TestUserSessionStore(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	group, err := store.DefaultGroupID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	rita, err := store.CreateUser(ctx, db, domain.NewUser{ID: ids.New(), Username: "rita", GroupID: group, WebAuthnHandle: []byte("h1"), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	sam, err := store.CreateUser(ctx, db, domain.NewUser{ID: ids.New(), Username: "sam", GroupID: group, WebAuthnHandle: []byte("h2"), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	add := func(user domain.User, stay bool, created, seen time.Time) domain.UserSession {
		t.Helper()
		s := domain.UserSession{ID: ids.New(), UserID: user.ID, Epoch: user.SessionEpoch, StaySignedIn: stay, CreatedAt: created,
			LastSeenAt: seen, IP: "198.51.100.7", UserAgent: "Firefox"}
		if err := store.InsertUserSession(ctx, db, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	laptop := add(rita, true, now.Add(-48*time.Hour), now.Add(-time.Hour))
	phone := add(rita, false, now.Add(-2*time.Hour), now.Add(-time.Minute))
	other := add(sam, false, now, now)

	got, err := store.GetUserSession(ctx, db, laptop.ID)
	if err != nil || !got.StaySignedIn || got.UserID != rita.ID || got.IP != "198.51.100.7" || got.UserAgent != "Firefox" || !got.CreatedAt.Equal(laptop.CreatedAt) {
		t.Fatalf("round trip %+v %v", got, err)
	}
	if _, err := store.GetUserSession(ctx, db, "missing"); !errors.Is(err, domain.ErrUserSessionNotFound) {
		t.Fatalf("missing: %v", err)
	}
	list, err := store.ListUserSessions(ctx, db, rita.ID)
	if err != nil || len(list) != 2 || list[0].ID != phone.ID || list[1].ID != laptop.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := store.TouchUserSession(ctx, db, laptop.ID, now, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetUserSession(ctx, db, laptop.ID); !got.LastSeenAt.Equal(now) || got.IP != "203.0.113.9" {
		t.Fatalf("touched %+v", got)
	}
	// A session belongs to one user: another user's ID does not delete it.
	if err := store.DeleteUserSession(ctx, db, other.ID, rita.ID); !errors.Is(err, domain.ErrUserSessionNotFound) {
		t.Fatalf("delete another user's session: %v", err)
	}
	live, err := store.LiveUserSessionIDs(ctx, db, []string{laptop.ID, other.ID, "missing"})
	if err != nil || !live[laptop.ID] || !live[other.ID] || len(live) != 2 {
		t.Fatalf("live %v %v", live, err)
	}

	// Ending all of rita's sessions (a new epoch) hides and then sweeps
	// them; the session that moved to the new epoch stays.
	epoch, err := store.BumpSessionEpoch(ctx, db, rita.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserSessionEpoch(ctx, db, phone.ID, epoch); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.ListUserSessions(ctx, db, rita.ID); len(list) != 1 || list[0].ID != phone.ID {
		t.Fatalf("after epoch bump %+v", list)
	}
	if live, _ := store.LiveUserSessionIDs(ctx, db, []string{laptop.ID}); live[laptop.ID] {
		t.Fatal("a session of an ended epoch is live")
	}
	limits := store.SessionLimits{Idle: 8 * time.Hour, Lifetime: 24 * time.Hour, StayIdle: 30 * 24 * time.Hour, StayLifetime: 365 * 24 * time.Hour}
	n, err := store.DeleteStaleUserSessions(ctx, db, now, limits)
	if err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	// Idle for longer than the normal idle timeout: swept; a "Stay signed
	// in" session idle as long is kept.
	stay := add(sam, true, now, now)
	n, err = store.DeleteStaleUserSessions(ctx, db, now.Add(9*time.Hour), limits)
	if err != nil || n != 2 { // rita's phone and sam's other
		t.Fatalf("idle sweep %d %v", n, err)
	}
	if _, err := store.GetUserSession(ctx, db, stay.ID); err != nil {
		t.Fatalf("stay signed in session swept: %v", err)
	}
	// Turning "Stay signed in" off gives it the normal limits.
	if err := store.EndStaySignedIn(ctx, db); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.DeleteStaleUserSessions(ctx, db, now.Add(9*time.Hour), limits); n != 1 {
		t.Fatalf("downgraded session kept (%d)", n)
	}

	a, b := add(rita, false, now, now), add(rita, false, now, now)
	removed, err := store.DeleteOtherUserSessions(ctx, db, rita.ID, a.ID)
	if err != nil || len(removed) != 1 || removed[0] != b.ID {
		t.Fatalf("delete others %v %v", removed, err)
	}
	all, err := store.UserSessionIDs(ctx, db)
	if err != nil || len(all) != 1 || !all[a.ID] {
		t.Fatalf("all %v %v", all, err)
	}
	if n, err := store.DeleteAllSessions(ctx, db); err != nil || n != 0 {
		t.Fatalf("delete all %d %v", n, err)
	}
	if all, _ := store.UserSessionIDs(ctx, db); len(all) != 0 {
		t.Fatalf("devices after restore cleanup %v", all)
	}
}
