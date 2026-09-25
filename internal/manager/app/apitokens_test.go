package app

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// API tokens (#31) through the real manager: identity middleware, the
// permission service, the job engine, audit and the environment, job and
// owner routes. Tokens are used like curl would: no cookie, no browser
// headers, Authorization: Bearer.

// opsUser creates rita in group Ops with the given group rules and returns
// her signed-in client (a fresh sign-in counts as a recent step-up).
func (e *env) opsUser(owner *client, rules ...string) (*client, string) {
	e.t.Helper()
	ops := owner.createGroup("Ops")
	owner.putRules("/api/v1/groups/"+ops.ID+"/permissions", rules...)
	rita, _, rs := e.newUser(owner, "rita")
	if r := owner.moveUser(rs.User.ID, ops.ID); r.status != http.StatusOK {
		e.t.Fatalf("move rita: %d %s", r.status, r.body)
	}
	return rita, rs.User.ID
}

// TestAPITokenActsOnlyWithinItsScope (#31 Done-when 1, with the routes that
// exist today): a token scoped to one capability on one resource performs
// exactly that through HTTP (and the job engine), cannot read other data
// its user could read, cannot touch other resources, and cannot call owner
// endpoints, sign-in flows or token management. Cookies are ignored on
// bearer requests and CSRF checks do not apply; every token-authenticated
// mutation is audited with the token ID.
func TestAPITokenActsOnlyWithinItsScope(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	e.seedEnvironment("e2", "Cloud")
	webJob := e.restartJob(ownerID, "e1", "web")
	dbJob := e.restartJob(ownerID, "e1", "db")
	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all", "allow environment.read @all", "allow environment.manage @all",
		"allow environment.system.read @all", "allow container.restart @env:e1", "allow container.start @env:e1")

	// Rita herself sees everything her group grants.
	if envs := rita.environments(); len(envs) != 2 || envs[0].View != "full" {
		t.Fatalf("rita's environments %+v", envs)
	}
	tok, secret := rita.createToken("ci", "allow environment.manage @env:e1", "allow container.restart @container:e1/web")
	if tok.Status != "active" || tok.UserID != ritaID || len(tok.Scopes) != 2 || tok.ExpiresAt == nil {
		t.Fatalf("created token %+v", tok)
	}
	b := e.bot(secret)

	// Only e1, only the minimal view (environment.read is not in the
	// token), with the one granted action.
	var page struct {
		Items []envItem `json:"items"`
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != "e1" || page.Items[0].View != "minimal" ||
		!slices.Equal(page.Items[0].Actions, []string{"environment.manage"}) || page.Items[0].EngineID != "" {
		t.Fatalf("token environments %+v", page.Items)
	}
	r := b.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1", nil)
	if strings.Contains(string(r.body), "ENGINE-e1") || r.header.Get("ETag") == "" {
		t.Fatalf("minimal environment with edit action: %v %s", r.header, r.body)
	}
	// The one granted action works, from any origin (no CSRF for bearer
	// requests: there is no ambient credential to abuse).
	b.must(http.StatusOK, http.MethodPatch, "/api/v1/environments/e1", map[string]string{"name": "NAS-renamed"},
		header("If-Match", r.header.Get("ETag")), header("Origin", "https://evil.example"), header("Sec-Fetch-Site", "cross-site"))
	// A cookie request from that origin is still refused.
	owner.fail(http.StatusForbidden, "cross_origin_request", http.MethodPatch, "/api/v1/environments/e1", map[string]string{"name": "x"},
		header("If-Match", "*"), header("Origin", "https://evil.example"), header("Sec-Fetch-Site", "cross-site"))
	// Other resources and other capabilities are refused, although rita
	// holds them herself.
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/environments/e2", nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodPatch, "/api/v1/environments/e2", map[string]string{"name": "x"}, header("If-Match", "*"))
	b.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/environments/e1/system", nil)
	b.fail(http.StatusForbidden, "forbidden", http.MethodDelete, "/api/v1/environments/e1", nil, header("If-Match", "*"))
	// Jobs: the restart jobs of web only (the kind's capability on the
	// target), never db's.
	var jobPage struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/jobs", nil).json(t, &jobPage)
	if len(jobPage.Items) != 1 || jobPage.Items[0].ID != webJob.ID {
		t.Fatalf("token jobs %+v", jobPage.Items)
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+dbJob.ID, nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+dbJob.ID+"/events/stream", nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodPost, "/api/v1/jobs/"+dbJob.ID+"/cancellations", nil)
	b.must(http.StatusAccepted, http.MethodPost, "/api/v1/jobs/"+webJob.ID+"/cancellations", nil)

	// Jobs started with the token: origin api_token, token ID and owning
	// user recorded; only the scoped restart is allowed, although rita
	// may also start containers and restart db.
	ctx := testutil.Context(t)
	p := authz.Principal{Kind: authz.KindAPIToken, UserID: ritaID, TokenID: tok.ID}
	web := []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}
	j, _, err := e.m.Jobs().Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1", Targets: web})
	if err != nil {
		t.Fatal(err)
	}
	if j.Origin != domain.OriginAPIToken || j.InitiatorTokenID != tok.ID || j.InitiatorUserID != ritaID {
		t.Fatalf("token job origin %+v", j)
	}
	for _, req := range []jobs.Request{
		{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1", Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "db"}}},
		{Kind: jobspec.ContainerStart, Principal: p, EnvironmentID: "e1", Targets: web},
	} {
		if _, _, err := e.m.Jobs().Enqueue(ctx, req); !errors.Is(err, domain.ErrJobForbidden) {
			t.Errorf("%s on %v with the token: %v", req.Kind, req.Targets, err)
		}
	}
	var job struct {
		Origin           string `json:"origin"`
		InitiatorUserID  string `json:"initiatorUserId"`
		InitiatorTokenID string `json:"initiatorTokenId"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+j.ID, nil).json(t, &job)
	if job.Origin != "api_token" || job.InitiatorTokenID != tok.ID || job.InitiatorUserID != ritaID {
		t.Fatalf("job as the owner sees it %+v", job)
	}
	// Job event streams accept tokens.
	stream := e.openTokenStream("/api/v1/jobs/"+j.ID+"/events/stream", secret)

	// Owner endpoints, sign-in and factor flows and token management are
	// never reachable with a token (the exhaustive list is
	// TestOwnerAndSessionRoutesRefuseAPITokens).
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users"}, {http.MethodGet, "/api/v1/groups"}, {http.MethodPost, "/api/v1/invitations"},
		{http.MethodGet, "/api/v1/settings/security"}, {http.MethodGet, "/api/v1/api-tokens"}, {http.MethodGet, "/api/v1/auth/session"},
		{http.MethodPost, "/api/v1/auth/step-ups"}, {http.MethodGet, "/api/v1/me/api-tokens"}, {http.MethodPost, "/api/v1/me/api-tokens"},
		{http.MethodDelete, "/api/v1/me/api-tokens/" + tok.ID}, {http.MethodPatch, "/api/v1/me/password"},
	} {
		b.fail(http.StatusForbidden, "api_token_not_allowed", c.method, c.path, nil)
	}
	// Authenticated routes that are not session-bound work: the account
	// and its (token-narrowed) permissions.
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).json(t, &me)
	if me.ID != ritaID || me.Username != "rita" {
		t.Fatalf("me with the token %+v", me)
	}
	var mp myPermissions
	b.must(http.StatusOK, http.MethodGet, "/api/v1/me/permissions", nil).json(t, &mp)
	if len(mp.Environments) != 1 || mp.Environments[0].ID != "e1" || mp.Environments[0].View != "minimal" {
		t.Fatalf("token permissions %+v", mp)
	}
	// A bearer request ignores the cookie sent with it: the owner's cookie
	// plus rita's token is rita's token.
	owner.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodGet, "/api/v1/users", nil, bearer(secret))
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil, bearer(secret)).json(t, &me)
	if me.ID != ritaID {
		t.Fatalf("cookie used on a bearer request: %+v", me)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/users", nil) // the cookie alone still works

	// Every token-authenticated mutation is audited with the token ID
	// (and its user), refused ones as denied.
	var renamed, denied, queued, cancelled bool
	for _, row := range e.auditRows() {
		if row.ActorToken != tok.ID {
			continue
		}
		if row.ActorKind != "api_token" || row.ActorUser != ritaID {
			t.Errorf("token record with actor %s/%s: %+v", row.ActorKind, row.ActorUser, row)
		}
		switch {
		case row.Action == "environment.manage" && row.Outcome == "success" && strings.Contains(row.Targets, "e1"):
			renamed = true
		case row.Action == "invitation.create" && row.Outcome == "denied" && row.ErrorClass == "api_token_not_allowed":
			denied = true
		case row.Action == "job.queued" && row.Outcome == "success":
			queued = true
		case row.Action == "job.cancel" && row.Outcome == "success":
			cancelled = true
		}
	}
	if !renamed || !denied || !queued || !cancelled {
		t.Fatalf("token audit records: rename %v, denied %v, job queued %v, cancel %v", renamed, denied, queued, cancelled)
	}

	// Revoking the token (rita, from her session) closes its stream.
	rita.must(http.StatusNoContent, http.MethodDelete, "/api/v1/me/api-tokens/"+tok.ID, nil)
	waitStreamEnd(t, stream, "job stream after revocation", "event: close\ndata: {\"reason\":\"session_expired\"}")
	b.refused("revoked token")
	if got := rita.myToken(tok.ID); got.Status != "revoked" || got.RevokedReason != "user" || got.RevokedAt == nil {
		t.Fatalf("revoked token %+v", got)
	}
	e.assertNoTokenValues()
}

// TestAPITokenNarrowedByGrantChanges (#31 Done-when 2, first half):
// narrowing the user's group grant or adding a user deny narrows the token
// on the very next request (token scope ∩ current permissions), ends its
// open streams, and re-granting restores it; queued jobs of the token are
// rechecked with the same rules (e.m.Permissions is the engine's
// authorizer).
func TestAPITokenNarrowedByGrantChanges(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	webJob := e.restartJob(ownerID, "e1", "web")
	groupRules := []string{"allow api_tokens.create @all", "allow environment.manage @env:e1", "allow container.restart @env:e1"}
	rita, ritaID := e.opsUser(owner, groupRules...)
	tok, secret := rita.createToken("deploy", "allow environment.manage @env:e1", "allow container.restart @container:e1/web")
	b := e.bot(secret)
	p := authz.Principal{Kind: authz.KindAPIToken, UserID: ritaID, TokenID: tok.ID}
	web := authz.Resource{Type: "container", ID: "web", EnvironmentID: "e1", Parents: []authz.ResourceRef{}}
	can := func(capability string, r authz.Resource) bool {
		return e.m.Permissions().Can(testutil.Context(t), p, capability, r).Allowed
	}
	if !can("container.restart", web) || !can("environment.manage", authz.EnvironmentResource("e1")) {
		t.Fatal("token lacks its scope")
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	stream := e.openTokenStream("/api/v1/jobs/"+webJob.ID+"/events/stream", secret)

	// The owner drops container.restart from the group.
	ops := owner.groupNamed("Ops")
	owner.putRules("/api/v1/groups/"+ops.ID+"/permissions", "allow api_tokens.create @all", "allow environment.manage @env:e1")
	waitStreamEnd(t, stream, "token job stream after the grant was narrowed", "event: close\ndata: {\"reason\":\"permissions_changed\"}")
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	if can("container.restart", web) {
		t.Fatal("token still restarts after the group lost the grant")
	}
	if _, _, err := e.m.Jobs().Enqueue(testutil.Context(t), jobs.Request{Kind: jobspec.ContainerRestart, Principal: p, EnvironmentID: "e1",
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}}); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("restart job with a narrowed token: %v", err)
	}
	// The rest of the scope still works.
	r := b.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1", nil)
	b.must(http.StatusOK, http.MethodPatch, "/api/v1/environments/e1", map[string]string{"name": "one"}, header("If-Match", r.header.Get("ETag")))

	// A user deny overrides the group for the token too.
	owner.putRules("/api/v1/users/"+ritaID+"/permissions", "deny environment.manage @env:e1")
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/environments/e1", nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodPatch, "/api/v1/environments/e1", map[string]string{"name": "two"}, header("If-Match", "*"))
	// Clearing the deny and re-granting restores the token (its scope never
	// changed).
	owner.putRules("/api/v1/users/" + ritaID + "/permissions")
	owner.putRules("/api/v1/groups/"+ops.ID+"/permissions", groupRules...)
	b.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	if !can("container.restart", web) {
		t.Fatal("token not restored with the grant")
	}
	// Moving rita to Restricted empties it.
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+ritaID, map[string]string{"groupId": owner.groupNamed("Restricted").ID},
		header("If-Match", owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ritaID, nil).header.Get("ETag")))
	var page struct {
		Items []envItem `json:"items"`
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil).json(t, &page)
	if len(page.Items) != 0 || can("environment.manage", authz.EnvironmentResource("e1")) {
		t.Fatalf("token of a Restricted user still sees %+v", page.Items)
	}
	e.assertNoTokenValues()
}

// TestAPITokenRefusalsAreGeneric (#31 Done-when 2, second half): tokens of
// disabled or deleted users, expired tokens, revoked tokens (by the user,
// the owner, credential resets that ask for it and the disaster-restore
// hook), tokens while tokens are disabled instance-wide and malformed or
// unknown tokens all fail with the same generic 401. Revocation closes
// open streams.
func TestAPITokenRefusalsAreGeneric(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	webJob := e.restartJob(ownerID, "e1", "web")
	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all", "allow environment.read @all", "allow container.restart @env:e1")
	scope := []string{"allow environment.read @env:e1", "allow container.restart @container:e1/web"}

	e.bot("garbage").refused("malformed token")
	e.bot("dy_0190a6e0-0000-7000-8000-000000000031_" + strings.Repeat("A", 43)).refused("unknown token")
	_, live := rita.createToken("live", scope...)
	_, fake := rita.createToken("other", scope...)
	e.bot(fake[:len(fake)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(fake, "A")]).refused("token with a wrong secret")
	e.bot(live).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)

	// Expired: the expiry is exact, on the manager clock.
	short, shortSecret := rita.createTokenWith(map[string]any{"name": "short", "expiresAt": e.clk.Now().Add(30 * time.Minute).Format(time.RFC3339),
		"scopes": tokenGrants(t, scope...)})
	e.bot(shortSecret).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	e.clk.Advance(30 * time.Minute)
	e.bot(shortSecret).refused("expired token")
	e.bot(live).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	var mine struct {
		Items []apiTokenBody `json:"items"`
	}
	rita.signIn("rita", e.passwordOf("rita"))
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/me/api-tokens", nil).json(t, &mine)
	for _, it := range mine.Items {
		if it.ID == short.ID && it.Status != "expired" {
			t.Fatalf("expired token listed as %s", it.Status)
		}
	}

	// Revoked by the owner (any user's token), with its stream closed.
	byOwner, byOwnerSecret := rita.createToken("owner-revokes", scope...)
	stream := e.openTokenStream("/api/v1/jobs/"+webJob.ID+"/events/stream", byOwnerSecret)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-tokens/"+byOwner.ID, nil)
	waitStreamEnd(t, stream, "stream of a token revoked by the owner", "session_expired")
	e.bot(byOwnerSecret).refused("token revoked by the owner")
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-tokens/"+byOwner.ID, nil) // again: harmless
	owner.fail(http.StatusNotFound, "not_found", http.MethodDelete, "/api/v1/api-tokens/0190a6e0-0000-7000-8000-00000000dead", nil)
	if got := rita.myToken(byOwner.ID); got.RevokedReason != "owner" {
		t.Fatalf("owner revocation %+v", got)
	}

	// Disabled instance-wide: every token stops (streams too), creation is
	// refused; re-enabling brings unrevoked tokens back.
	stream = e.openTokenStream("/api/v1/jobs/"+webJob.ID+"/events/stream", live)
	owner.setSecurity(map[string]any{"apiTokensEnabled": false})
	waitStreamEnd(t, stream, "stream while tokens were disabled", "session_expired")
	e.bot(live).refused("token while tokens are disabled")
	rita.fail(http.StatusForbidden, "api_tokens_disabled", http.MethodPost, "/api/v1/me/api-tokens", rita.tokenRequest("x", scope...))
	owner.setSecurity(map[string]any{"apiTokensEnabled": true})
	e.bot(live).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)

	// Credential resets offer revocation: without it tokens survive, with
	// it they are revoked.
	owner.stepUp()
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/users/"+ritaID+"/factor-resets", nil)
	e.bot(live).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/users/"+ritaID+"/factor-resets", map[string]bool{"revokeApiTokens": true})
	e.bot(live).refused("token revoked by a factor reset")
	rita.signIn("rita", e.passwordOf("rita"))
	_, afterReset := rita.createToken("after-reset", scope...)
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/users/"+ritaID+"/password-resets", map[string]bool{"revokeApiTokens": true}, secretOK)
	e.bot(afterReset).refused("token revoked by a password reset")
	_, own := rita.createToken("own-password-change", scope...)
	newPW := e.passwordOf("rita") + "-2"
	e.secrets.Register(canary.Password, "rita new password", newPW)
	rita.must(http.StatusNoContent, http.MethodPatch, "/api/v1/me/password", map[string]any{"currentPassword": e.passwordOf("rita"),
		"newPassword": newPW, "revokeApiTokens": true})
	e.bot(own).refused("token revoked by the user's password change")

	// Disabling the account revokes its tokens for good (open streams
	// close); re-enabling does not bring them back.
	_, dis := rita.createToken("disable", scope...)
	stream = e.openTokenStream("/api/v1/jobs/"+webJob.ID+"/events/stream", dis)
	etag := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+ritaID, nil).header.Get("ETag")
	r := owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+ritaID, map[string]string{"status": "disabled"}, header("If-Match", etag))
	waitStreamEnd(t, stream, "stream of a disabled user's token", "session_expired")
	e.bot(dis).refused("token of a disabled user")
	// The disable's audit record carries how many tokens it revoked,
	// unredacted: a count is metadata, never a secret.
	disabled := false
	for _, row := range e.auditRows() {
		if row.Action == "user.update" && strings.Contains(row.Details, "user.disable") {
			disabled = true
			if !strings.Contains(row.Details, `"apiTokenCount":1`) || strings.Contains(row.Details, audit.Redacted) {
				t.Errorf("disable audit details %s", row.Details)
			}
		}
	}
	if !disabled {
		t.Error("no user.disable audit record")
	}
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+ritaID, map[string]string{"status": "active"}, header("If-Match", r.header.Get("ETag")))
	e.bot(dis).refused("token of a re-enabled user")
	var all struct {
		Items []apiTokenBody `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/api-tokens", nil).json(t, &all)
	reasons := map[string]int{}
	for _, it := range all.Items {
		if it.Username != "rita" || it.UserID != ritaID {
			t.Errorf("owner list item %+v", it)
		}
		reasons[it.Status+"/"+it.RevokedReason]++
	}
	// The reset that asked for it revoked the expired token as well.
	if reasons["revoked/user_disabled"] != 1 || reasons["revoked/credential_reset"] != 5 || reasons["revoked/owner"] != 1 || len(reasons) != 3 {
		t.Fatalf("owner list statuses %v", reasons)
	}

	// The disaster-restore hook (#24) revokes every token.
	rita.signIn("rita", newPW)
	_, restored := rita.createToken("restored", scope...)
	_, ownerTok := owner.createToken("owner-restored", "allow environment.read @all")
	n, err := e.m.Identity().RevokeAllAPITokens(testutil.Context(t), domain.RevokedRestore)
	if err != nil || n != 2 {
		t.Fatalf("revoke all: %d %v", n, err)
	}
	e.bot(restored).refused("token after a restore")
	e.bot(ownerTok).refused("owner token after a restore")

	// Deleting the account deletes its tokens.
	_, gone := rita.createToken("deleted-user", scope...)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/users/"+ritaID, nil)
	e.bot(gone).refused("token of a deleted user")
	e.assertNoTokenValues()
}

// TestAPITokensNeverSatisfyFactorPolicy: a token never stands in for a
// TOTP or passkey the sign-in policy requires. When the policy requires
// factors the account has not enrolled, its tokens stop until it enrolls.
func TestAPITokensNeverSatisfyFactorPolicy(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	rita, _ := e.opsUser(owner, "allow api_tokens.create @all", "allow environment.read @all")
	_, secret := rita.createToken("ci", "allow environment.read @all")
	e.bot(secret).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	owner.setSecurity(map[string]any{"requiredFactors": "totp"})
	e.bot(secret).refused("token of an account without the required TOTP")
	rita.signIn("rita", e.passwordOf("rita")) // an enrollment session
	rita.enrollTOTP()
	e.bot(secret).must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
}

// TestAPITokenCreationRules: creating needs api_tokens.create, a recent
// step-up and a scope within the creator's current permissions; the
// expiry is required and bounded by the instance maximum unless the owner
// allows non-expiring tokens; the value is shown once; tokens are listed,
// renamed and revoked by their user only; last use is recorded with a
// bounded write frequency.
func TestAPITokenCreationRules(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	e.seedEnvironment("e1", "NAS")
	e.seedEnvironment("e2", "Cloud")
	rita, ritaID := e.opsUser(owner, "allow environment.read @env:e1", "allow container.restart @stack:s1")
	ops := owner.groupNamed("Ops")

	// No api_tokens.create: refused.
	rita.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/me/api-tokens", rita.tokenRequest("x", "allow environment.read @env:e1"))
	owner.putRules("/api/v1/groups/"+ops.ID+"/permissions", "allow api_tokens.create @all", "allow environment.read @env:e1",
		"allow container.restart @stack:s1")
	// Scope must be held by the creator now, and grantable.
	for _, c := range []struct{ rule, field string }{
		{"allow environment.read @env:e2", "body.scopes[0]"},
		{"allow environment.manage @env:e1", "body.scopes[0]"},
		{"allow users.manage @all", "body.scopes[0]"},
		{"allow container.restart @all", "body.scopes[0]"},
	} {
		r := rita.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/me/api-tokens", rita.tokenRequest("x", c.rule))
		if !strings.Contains(string(r.body), `"field":"`+c.field+`"`) {
			t.Errorf("%s: %s", c.rule, r.body)
		}
	}
	// A narrower scope inside a grant is fine: one service of the stack.
	rita.createToken("svc", "allow container.restart @service:s1/web", "allow environment.read @env:e1")
	rita.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/me/api-tokens",
		map[string]any{"name": "x", "scopes": tokenGrants(t, "allow environment.read @env:e1")}) // no expiry
	rita.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/me/api-tokens",
		map[string]any{"name": "x", "expiresAt": e.clk.Now().Add(91 * 24 * time.Hour).Format(time.RFC3339), "scopes": tokenGrants(t, "allow environment.read @env:e1")})
	rita.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/me/api-tokens",
		map[string]any{"name": "x", "neverExpires": true, "scopes": tokenGrants(t, "allow environment.read @env:e1")})
	rita.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/me/api-tokens",
		map[string]any{"name": "x", "expiresAt": e.clk.Now().Add(time.Hour).Format(time.RFC3339), "scopes": []any{}})
	// The owner raises the maximum and allows non-expiring tokens.
	owner.setSecurity(map[string]any{"apiTokenMaxLifetimeDays": 365, "apiTokensNonExpiring": true})
	rita.createTokenWith(map[string]any{"name": "year", "expiresAt": e.clk.Now().Add(300 * 24 * time.Hour).Format(time.RFC3339),
		"scopes": tokenGrants(t, "allow environment.read @env:e1")})
	forever, foreverSecret := rita.createTokenWith(map[string]any{"name": "forever", "neverExpires": true,
		"scopes": tokenGrants(t, "allow environment.read @env:e1")})
	if forever.ExpiresAt != nil {
		t.Fatalf("non-expiring token %+v", forever)
	}

	// Step-up: 10 minutes after sign-in creation needs a new one.
	e.clk.Advance(11 * time.Minute)
	rita.fail(http.StatusForbidden, "step_up_required", http.MethodPost, "/api/v1/me/api-tokens", rita.tokenRequest("late", "allow environment.read @env:e1"))
	rita.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": e.passwordOf("rita")})
	// Idempotent creation replays the same response (sealed at rest).
	req := rita.tokenRequest("late", "allow environment.read @env:e1")
	late, lateSecret := rita.createTokenWith(req, header("Idempotency-Key", "k-1"))
	var replay struct {
		APIToken apiTokenBody `json:"apiToken"`
		Token    string       `json:"token"`
	}
	r := rita.must(http.StatusCreated, http.MethodPost, "/api/v1/me/api-tokens", req, header("Idempotency-Key", "k-1"), secretOK)
	r.json(t, &replay)
	if replay.APIToken.ID != late.ID || replay.Token != lateSecret || r.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %+v %v", replay.APIToken, r.header)
	}

	// Management: the value is never shown again; only the name changes.
	var list struct {
		Items []apiTokenBody `json:"items"`
	}
	rita.must(http.StatusOK, http.MethodGet, "/api/v1/me/api-tokens", nil).json(t, &list)
	if len(list.Items) != 4 || list.Items[0].ID != late.ID {
		t.Fatalf("my tokens %+v", list.Items)
	}
	var renamed apiTokenBody
	rita.must(http.StatusOK, http.MethodPatch, "/api/v1/me/api-tokens/"+late.ID, map[string]string{"name": "renamed"}).json(t, &renamed)
	if renamed.Name != "renamed" || len(renamed.Scopes) != 1 {
		t.Fatalf("renamed %+v", renamed)
	}
	// Other users' tokens are not found through /me.
	owner.stepUp()
	ownerTok, _ := owner.createToken("owner", "allow environment.read @all")
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		rita.fail(http.StatusNotFound, "not_found", m, "/api/v1/me/api-tokens/"+ownerTok.ID, map[string]string{"name": "mine"})
	}
	// The owner's list needs the owner.
	rita.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/api-tokens", nil)
	rita.fail(http.StatusForbidden, "forbidden", http.MethodDelete, "/api/v1/api-tokens/"+ownerTok.ID, nil)

	// Last use: time and client IP, written at most once a minute.
	b := e.bot(foreverSecret)
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil)
	first := rita.myToken(forever.ID)
	if first.LastUsedAt == nil || !first.LastUsedAt.Equal(e.clk.Now().UTC()) || first.LastUsedIP != b.c.ip {
		t.Fatalf("last use %+v (client %s)", first, b.c.ip)
	}
	e.clk.Advance(30 * time.Second)
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil)
	if got := rita.myToken(forever.ID); !got.LastUsedAt.Equal(*first.LastUsedAt) {
		t.Fatalf("last use rewritten within a minute: %v", got.LastUsedAt)
	}
	e.clk.Advance(31 * time.Second)
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil)
	if got := rita.myToken(forever.ID); !got.LastUsedAt.After(*first.LastUsedAt) {
		t.Fatalf("last use not updated after a minute: %v", got.LastUsedAt)
	}
	// The creation is audited with the scope and never the value.
	found := false
	for _, row := range e.auditRows() {
		if row.Action == "api_tokens.create" && row.Outcome == "success" && strings.Contains(row.Targets, forever.ID) {
			found = row.ActorUser == ritaID && strings.Contains(row.Details, "allow environment.read @env:e1") &&
				strings.Contains(row.Details, `"neverExpires":true`)
		}
	}
	if !found {
		t.Fatal("no audit record of the token creation with its scope")
	}
	e.assertNoTokenValues()
}

// TestOwnerAndSessionRoutesRefuseAPITokens: every owner operation (users,
// groups, invitations, security settings, registry/Git credential
// administration, other users' tokens, permission previews) and every
// session-only operation (sign-in and factor flows, token management)
// answers an API token — even the owner's own, with a broad scope — with
// 403 api_token_not_allowed before its handler runs, and documents
// cookie-only security. Operations accepting tokens document the bearer
// scheme.
func TestOwnerAndSessionRoutesRefuseAPITokens(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	e.seedEnvironment("e1", "NAS")
	_, secret := owner.createToken("owner-ci", "allow environment.read @all", "allow environment.manage @all", "allow agent.enroll @all",
		"allow audit.read @all")
	b := e.bot(secret)
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string                `json:"operationId"`
			Capability  string                `json:"x-dockyard-capability"`
			Security    []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/openapi.json", nil).json(t, &spec)
	refused, accepted := 0, 0
	for path, item := range spec.Paths {
		for method, op := range item {
			if op.OperationID == "" || op.Capability == "public" {
				continue
			}
			bearerOK := slices.ContainsFunc(op.Security, func(alt map[string][]string) bool { _, ok := alt["bearerToken"]; return ok })
			if op.Capability == "owner" && bearerOK {
				t.Errorf("%s (owner) documents the bearer scheme", op.OperationID)
			}
			if bearerOK {
				accepted++
				continue
			}
			target := strings.NewReplacer("{userId}", "u1", "{groupId}", "g1", "{invitationId}", "i1", "{tokenId}", "t1",
				"{credentialId}", "c1", "{registryId}", "r1").Replace(path)
			r := b.do(strings.ToUpper(method), target, nil)
			if r.status != http.StatusForbidden || r.code() != "api_token_not_allowed" {
				t.Errorf("%s %s (%s) with the owner's token: %d %s", strings.ToUpper(method), target, op.OperationID, r.status, r.body)
			}
			refused++
		}
	}
	if refused < 40 || accepted < 15 {
		t.Fatalf("refused %d, accepted %d operations", refused, accepted)
	}
	// Nothing happened: no invitation, the settings unchanged.
	var inv struct {
		Items []any `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/invitations", nil).json(t, &inv)
	if len(inv.Items) != 0 {
		t.Fatalf("an owner route ran for a token: %+v", inv.Items)
	}
	// The owner's token does what its scope allows.
	b.must(http.StatusOK, http.MethodGet, "/api/v1/audit", nil)
	b.must(http.StatusOK, http.MethodGet, "/api/v1/environments/e1", nil)
	b.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/environments/e1/system", nil)
	e.assertNoTokenValues()
}

// setSecurity patches the security settings as the owner (with step-up).
func (c *client) setSecurity(patch map[string]any) {
	c.e.t.Helper()
	c.stepUp()
	cur := c.must(http.StatusOK, http.MethodGet, "/api/v1/settings/security", nil)
	c.must(http.StatusOK, http.MethodPatch, "/api/v1/settings/security", patch, header("If-Match", cur.header.Get("ETag")))
}

// stepUp re-authenticates the owner client with its password.
func (c *client) stepUp() {
	c.e.t.Helper()
	c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": c.e.ownerPassword(c)})
}

// passwordOf returns the canary password registered for username.
func (e *env) passwordOf(username string) string {
	for _, c := range e.secrets.All() {
		if c.Name == username+" password" {
			return c.Value
		}
	}
	e.t.Fatalf("no password for %s", username)
	return ""
}

// assertNoTokenValues checks that no token value reached the audit trail,
// the token table (verifiers only), stored idempotent responses (sealed),
// sessions or jobs; logs and responses are checked by the canary logger
// and client.
func (e *env) assertNoTokenValues() {
	e.t.Helper()
	e.secrets.AssertClean(e.t, "stored rows", e.tableDump("audit_events", "api_tokens", "api_token_scopes", "idempotency_keys",
		"sessions", "jobs", "job_events"))
}

// TestExpiredTokenStreamSwept: a stream opened with a token that expires
// while it is open is closed by the stream sweeper, and a session
// revocation leaves the user's token streams alone (separate credential).
func TestExpiredTokenStreamSwept(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	ownerID := e.userID("owner")
	e.seedEnvironment("e1", "NAS")
	webJob := e.restartJob(ownerID, "e1", "web")
	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all", "allow container.restart @env:e1")
	_, secret := rita.createTokenWith(map[string]any{"name": "short", "expiresAt": e.clk.Now().Add(20 * time.Minute).Format(time.RFC3339),
		"scopes": tokenGrants(t, "allow container.restart @container:e1/web")})
	stream := e.openTokenStream("/api/v1/jobs/"+webJob.ID+"/events/stream", secret)
	owner.must(http.StatusNoContent, http.MethodPost, "/api/v1/users/"+ritaID+"/session-revocations", nil)
	if err := e.m.Identity().SweepStreams(testutil.Context(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-stream:
		t.Fatalf("token stream ended by a session revocation: %q", body)
	default:
	}
	e.bot(secret).must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+webJob.ID, nil)
	e.clk.Advance(20 * time.Minute)
	if err := e.m.Identity().SweepStreams(testutil.Context(t)); err != nil {
		t.Fatal(err)
	}
	waitStreamEnd(t, stream, "stream of an expired token", "session_expired")
}
