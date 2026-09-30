package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
)

// TestHubSeparatesSessionsAndTokens: ending a user's sessions leaves the
// user's API-token requests open (tokens are a separate credential);
// revoking a token ends only its requests; ending the user (disable,
// delete, permission change) ends both.
func TestHubSeparatesSessionsAndTokens(t *testing.T) {
	h := newHub()
	open := func(register func(context.CancelCauseFunc) func()) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		t.Cleanup(register(cancel))
		return ctx
	}
	sess := open(func(c context.CancelCauseFunc) func() { _, done := h.register("rita", 3, "s1", c); return done })
	tok1 := open(func(c context.CancelCauseFunc) func() { return h.registerToken("rita", "t1", c) })
	tok2 := open(func(c context.CancelCauseFunc) func() { return h.registerToken("rita", "t2", c) })
	other := open(func(c context.CancelCauseFunc) func() { return h.registerToken("sam", "t3", c) })
	live := func(ctx context.Context) bool { return ctx.Err() == nil }

	users, sessions, tokens := h.snapshot()
	if _, ok := users["rita"]; !ok || len(users) != 1 || len(tokens) != 3 || tokens["t3"] != "sam" {
		t.Fatalf("snapshot %v %v", users, tokens)
	}
	if _, ok := sessions["s1"]; !ok || len(sessions) != 1 {
		t.Fatalf("snapshot sessions %v", sessions)
	}
	h.revoke("rita", -1)
	if live(sess) || !live(tok1) || !live(tok2) {
		t.Fatal("ending sessions must not end token requests")
	}
	h.revokeTokens([]string{"t1"}, authz.ErrSessionEnded)
	if live(tok1) || !live(tok2) || !errors.Is(context.Cause(tok1), authz.ErrSessionEnded) {
		t.Fatal("token revocation")
	}
	h.revokeUser("rita", authz.ErrPermissionsChanged)
	if live(tok2) || !live(other) || authz.CloseReason(tok2) != "permissions_changed" {
		t.Fatal("ending the user")
	}
	h.revokeAllTokens(authz.ErrSessionEnded)
	if live(other) || h.count() != 0 {
		t.Fatalf("all tokens: %d left", h.count())
	}
}

// TestHubRevokesOneSession: signing out a device ends only its requests,
// never the calling request itself, and never API-token requests.
func TestHubRevokesOneSession(t *testing.T) {
	h := newHub()
	open := func(sid string) (context.Context, uint64) {
		ctx, cancel := context.WithCancelCause(context.Background())
		id, done := h.register("rita", 1, sid, cancel)
		t.Cleanup(done)
		return ctx, id
	}
	phone, _ := open("phone")
	laptop, self := open("laptop")
	laptop2, _ := open("laptop")
	tctx, tcancel := context.WithCancelCause(context.Background())
	t.Cleanup(h.registerToken("rita", "phone", tcancel))

	h.revokeSessions([]string{"phone"}, authz.ErrSessionEnded, 0)
	if phone.Err() == nil || laptop.Err() != nil || tctx.Err() != nil || !errors.Is(context.Cause(phone), authz.ErrSessionEnded) {
		t.Fatal("revoking one session")
	}
	h.revokeSessions([]string{"laptop"}, authz.ErrSessionEnded, self)
	if laptop.Err() != nil || laptop2.Err() == nil {
		t.Fatal("the calling request must survive; the session's other requests end")
	}
}
