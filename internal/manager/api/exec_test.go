package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
)

// TestAuthorizeExecNeedsExplicitTokenGrant: the shared exec check (#8,
// #31) accepts sessions holding container.exec and API tokens only when
// container.exec is in the token's own scope and the user holds it now;
// every other container capability, however broad, never implies it.
func TestAuthorizeExecNeedsExplicitTokenGrant(t *testing.T) {
	everythingButExec := []string{"allow container.restart @all", "allow container.logs.read @all", "allow container.details.read @all",
		"allow container.start @all", "allow container.stop @all", "allow container.metrics.read @all"}
	pol := authztest.New().Owner("olga").
		Member("rita", "ops").Group("ops", append([]string{"allow container.exec @env:e1"}, everythingButExec...)...).
		Member("sam", "restricted").
		Token("t-noexec", "rita", everythingButExec...).
		Token("t-exec", "rita", "allow container.exec @container:e1/web").
		Token("t-exec-e2", "rita", "allow container.exec @env:e2").
		Token("t-owner-noexec", "olga", "allow container.restart @all").
		Token("t-owner-exec", "olga", "allow container.exec @all")
	web := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	db := authz.Resource{Type: "container", ID: "db", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	other := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e2", Parents: []authz.ResourceRef{}}
	user := func(u string) authz.Principal { return authz.Principal{Kind: authz.KindUser, UserID: u} }
	token := func(id, u string) authz.Principal {
		return authz.Principal{Kind: authz.KindAPIToken, UserID: u, TokenID: id}
	}
	for _, tc := range []struct {
		name   string
		p      authz.Principal
		r      authz.Resource
		status int // 0: allowed
	}{
		{"session with exec", user("rita"), web, 0},
		{"owner session", user("olga"), web, 0},
		{"token without exec, every other container capability", token("t-noexec", "rita"), web, http.StatusForbidden},
		{"token with exec on web", token("t-exec", "rita"), web, 0},
		{"token with exec on web, other container", token("t-exec", "rita"), db, http.StatusNotFound},
		{"token scope exceeds the user (e2)", token("t-exec-e2", "rita"), other, http.StatusNotFound},
		{"owner token without exec", token("t-owner-noexec", "olga"), web, http.StatusForbidden},
		{"owner token with exec", token("t-owner-exec", "olga"), web, 0},
		{"restricted user", user("sam"), web, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := authz.WithPrincipal(context.Background(), tc.p)
			if err != nil {
				t.Fatal(err)
			}
			p, err := AuthorizeExec(ctx, pol, tc.r)
			var ae *Error
			switch {
			case tc.status == 0 && err != nil:
				t.Fatalf("denied: %v", err)
			case tc.status == 0 && p != tc.p:
				t.Fatalf("principal %+v", p)
			case tc.status != 0 && (!errors.As(err, &ae) || ae.status != tc.status):
				t.Fatalf("got %v, want %d", err, tc.status)
			}
		})
	}
	if _, err := AuthorizeExec(context.Background(), pol, web); err == nil {
		t.Fatal("exec without a principal")
	}
	if d := authz.CanExec(authz.For(context.Background(), pol, user("olga")), authz.Resource{Type: "stack", ID: "s"}); d.Allowed {
		t.Fatal("exec on a non-container")
	}
}
