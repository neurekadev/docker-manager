package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/store/storetest"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestAPITokenStore: tokens round-trip with their scope, revocations are
// final and reported once, and the schema refuses changes to a token's
// owner, secret, expiry and scope.
func TestAPITokenStore(t *testing.T) {
	ctx := testutil.Context(t)
	db := storetest.Migrated(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	group, err := store.DefaultGroupID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	u, err := store.CreateUser(ctx, db, domain.NewUser{ID: ids.New(), Username: "rita", GroupID: group, WebAuthnHandle: []byte("h1"), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	exp := now.Add(24 * time.Hour)
	tok := domain.APIToken{ID: ids.New(), UserID: u.ID, Name: "ci", CreatedAt: now, ExpiresAt: &exp, Scopes: []domain.PermissionRule{
		{Capability: "container.restart", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: "resource", EnvironmentID: "e1", ResourceType: "container", ResourceID: "web"}},
		{Capability: "environment.read", Effect: domain.PermissionAllow, Scope: domain.PermissionScope{Kind: "environment", EnvironmentID: "e1"}},
	}}
	verifier := strings.Repeat("a", 64)
	if err := store.InsertAPIToken(ctx, db, tok, verifier); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAPIToken(ctx, db, tok.ID, u.ID)
	if err != nil || got.Name != "ci" || got.Username != "rita" || len(got.Scopes) != 2 || got.Scopes[0].Scope.ResourceID != "web" ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.Status(now) != domain.APITokenActive {
		t.Fatalf("round trip %+v %v", got, err)
	}
	if _, err := store.GetAPIToken(ctx, db, tok.ID, "someone-else"); !errors.Is(err, domain.ErrAPITokenNotFound) {
		t.Fatalf("other user's token: %v", err)
	}
	creds, err := store.APITokenCredentials(ctx, db, []string{tok.ID, "missing"})
	if err != nil || len(creds) != 1 || creds[tok.ID].Verifier != verifier || !creds[tok.ID].UserActive || creds[tok.ID].Revoked {
		t.Fatalf("credentials %+v %v", creds, err)
	}
	if err := store.TouchAPIToken(ctx, db, tok.ID, now.Add(time.Minute), "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAPIToken(ctx, db, tok.ID, u.ID, "deploy"); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListAPITokens(ctx, db, u.ID, "", 10)
	if err != nil || len(list) != 1 || list[0].Name != "deploy" || list[0].LastUsedIP != "198.51.100.7" {
		t.Fatalf("list %+v %v", list, err)
	}
	// The schema keeps a token's identity and scope fixed.
	for _, stmt := range []string{
		`UPDATE api_tokens SET user_id = 'x'`, `UPDATE api_tokens SET verifier = '` + strings.Repeat("b", 64) + `'`,
		`UPDATE api_tokens SET expires_at = NULL`, `UPDATE api_token_scopes SET resource_id = 'db'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s was allowed", stmt)
		}
	}
	owner, revoked, err := store.RevokeAPIToken(ctx, db, tok.ID, u.ID, now, u.ID, domain.RevokedByUser)
	if err != nil || owner != u.ID || !revoked {
		t.Fatalf("revoke: %s %v %v", owner, revoked, err)
	}
	if _, revoked, err := store.RevokeAPIToken(ctx, db, tok.ID, "", now, "x", domain.RevokedByOwner); err != nil || revoked {
		t.Fatalf("second revoke: %v %v", revoked, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = NULL, revoked_reason = NULL`); err == nil {
		t.Fatal("a revocation was undone")
	}
	got, _ = store.GetAPIToken(ctx, db, tok.ID, "")
	if got.Status(now) != domain.APITokenRevoked || got.RevokedReason != domain.RevokedByUser {
		t.Fatalf("revoked %+v", got)
	}
	// Deleting the user deletes its tokens and scopes.
	if err := store.DeleteUser(ctx, db, u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.NewRaw(`SELECT (SELECT count(*) FROM api_tokens) + (SELECT count(*) FROM api_token_scopes)`).Scan(ctx, &n); err != nil || n != 0 {
		t.Fatalf("rows left after deleting the user: %d %v", n, err)
	}
}
