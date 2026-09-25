package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/neurekadev/dockyard/internal/manager/authz"
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
	sess := open(func(c context.CancelCauseFunc) func() { return h.register("rita", 3, c) })
	tok1 := open(func(c context.CancelCauseFunc) func() { return h.registerToken("rita", "t1", c) })
	tok2 := open(func(c context.CancelCauseFunc) func() { return h.registerToken("rita", "t2", c) })
	other := open(func(c context.CancelCauseFunc) func() { return h.registerToken("sam", "t3", c) })
	live := func(ctx context.Context) bool { return ctx.Err() == nil }

	sessions, tokens := h.snapshot()
	if _, ok := sessions["rita"]; !ok || len(sessions) != 1 || len(tokens) != 3 || tokens["t3"] != "sam" {
		t.Fatalf("snapshot %v %v", sessions, tokens)
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
