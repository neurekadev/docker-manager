package app

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/auth"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/server/sse"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// streamServer serves a test SSE route behind the manager's identity
// middleware (same sessions and revocation hub as the real API), so
// stream closing can be observed for accounts without job access.
func (e *env) streamServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/test/stream", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authz.PrincipalFrom(r.Context()); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sse.SetHeaders(w.Header().Set)
		w.WriteHeader(http.StatusOK)
		sw := sse.NewWriter(w)
		_ = sw.Comment("open")
		<-r.Context().Done()
	})
	srv := httptest.NewServer(e.m.Identity().Middleware(mux))
	e.t.Cleanup(srv.Close)
	return srv
}

// openStream opens an SSE stream with c's session and returns a channel
// closed when the server ends the stream.
func (c *client) openStream(url string) <-chan struct{} {
	t := c.e.t
	t.Helper()
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", publicHost)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine (or below on failure)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("stream: %d %s", resp.StatusCode, b)
	}
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, br)
	}()
	return done
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-testutil.Context(t).Done():
		t.Fatalf("%s: stream still open", what)
	}
}

func (e *env) userID(username string) string {
	e.t.Helper()
	var id string
	if err := e.m.DB().NewRaw("SELECT id FROM users WHERE username = ?", username).Scan(testutil.Context(e.t), &id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// TestDisableAndResetsEndSessionsAndStreams: disabling an account, owner
// factor resets, session revocations and password resets end the
// account's sessions and close its open streams at once.
func TestDisableAndResetsEndSessionsAndStreams(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	streams := e.streamServer()
	user, pw, s := e.newUser(owner, "sam")
	id := s.User.ID

	patch := func(status string) {
		r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+id, nil)
		owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+id, map[string]string{"status": status}, header("If-Match", r.header.Get("ETag")))
	}

	// Disable.
	done := user.openStream(streams.URL + "/api/v1/test/stream")
	patch("disabled")
	waitClosed(t, done, "disable")
	user.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "sam", "password": pw})
	patch("active")

	// Factor reset.
	user.signIn("sam", pw)
	done = user.openStream(streams.URL + "/api/v1/test/stream")
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/users/"+id+"/factor-resets", nil)
	waitClosed(t, done, "factor reset")
	user.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// Session revocation.
	user.signIn("sam", pw)
	done = user.openStream(streams.URL + "/api/v1/test/stream")
	owner.must(http.StatusNoContent, http.MethodPost, "/api/v1/users/"+id+"/session-revocations", nil)
	waitClosed(t, done, "session revocation")
	user.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// Owner-issued password reset, redeemed by the user.
	user.signIn("sam", pw)
	done = user.openStream(streams.URL + "/api/v1/test/stream")
	var reset struct {
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/users/"+id+"/password-resets", nil, secretOK).json(t, &reset)
	e.secrets.Register("reset-code", "password reset", reset.Code)
	if !strings.HasPrefix(reset.URL, publicOrigin+"/password-reset#code=dyr_") {
		t.Fatalf("reset %+v", reset)
	}
	newPW := e.secrets.New(canary.Password, "sam new password")
	e.client().fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": reset.Code, "newPassword": "short"})
	e.client().must(http.StatusNoContent, http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": reset.Code, "newPassword": newPW})
	waitClosed(t, done, "password reset")
	user.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": reset.Code, "newPassword": newPW})
	e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "sam", "password": pw})
	user.signIn("sam", newPW)

	// Deleting the account ends its session.
	done = user.openStream(streams.URL + "/api/v1/test/stream")
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/users/"+id, nil)
	waitClosed(t, done, "delete")
	user.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestOwnerJobStreamClosesOnRevocation: a real job event stream of the
// owner closes when the owner's sessions are revoked from another browser.
func TestOwnerJobStreamClosesOnRevocation(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	ownerID := e.userID("owner")
	job, _, err := e.m.Jobs().Enqueue(testutil.Context(t), jobs.Request{
		Kind: "container.restart", Principal: authz.Principal{Kind: authz.KindUser, UserID: ownerID},
		EnvironmentID: "env-1", Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := owner.openStream(e.srv.URL + "/api/v1/jobs/" + job.ID + "/events/stream")
	other := e.client()
	other.signIn("owner", pw)
	other.must(http.StatusNoContent, http.MethodPost, "/api/v1/users/"+ownerID+"/session-revocations", nil)
	waitClosed(t, done, "owner session revocation")
	owner.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	other.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestOwnerRecovery: the CLI path (another process on the data volume)
// signs out the owner, closes the owner's streams at the next sweep, and
// its code resets the owner's password and factors once.
func TestOwnerRecovery(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	owner.enrollTOTP()
	streams := e.streamServer()
	done := owner.openStream(streams.URL + "/api/v1/test/stream")

	code, err := auth.IssueOwnerRecovery(testutil.Context(t), e.m.DB(), nil, e.clk.Now(), e.m.opts.Config.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	e.secrets.Register("owner-recovery-code", "owner recovery", code.Code)
	if !strings.HasPrefix(code.Code, "dyo_") || !code.ExpiresAt.Equal(e.clk.Now().Add(auth.OwnerRecoveryTTL)) {
		t.Fatalf("code %+v", code)
	}
	owner.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	if err := e.m.Identity().SweepStreams(testutil.Context(t)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, done, "owner recovery")

	newPW := e.secrets.New(canary.Password, "owner recovered password")
	e.client().must(http.StatusNoContent, http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": code.Code, "newPassword": newPW})
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": code.Code, "newPassword": newPW})
	e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "owner", "password": pw})
	s := owner.signIn("owner", newPW)
	if s.State != "authenticated" || s.User.Factors.TOTP {
		t.Fatalf("after recovery %+v (TOTP must be removed)", s)
	}
	// Expired codes do not work.
	code2, err := auth.IssueOwnerRecovery(testutil.Context(t), e.m.DB(), nil, e.clk.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.secrets.Register("owner-recovery-code", "owner recovery 2", code2.Code)
	e.clk.Advance(auth.OwnerRecoveryTTL)
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/auth/password-resets/redemptions", map[string]string{"code": code2.Code, "newPassword": newPW})

	// The CLI wrapper refuses a missing database.
	cfg := e.m.opts.Config
	cfg.DataDir = t.TempDir()
	if _, err := OwnerRecovery(testutil.Context(t), cfg, testutil.Logger(t), e.clk); err == nil {
		t.Fatal("owner recovery on an empty data directory succeeded")
	}
}

// TestOwnerIsProtected: the owner cannot be disabled, deleted or reset
// through the API, and the database refuses it too.
func TestOwnerIsProtected(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	id := e.userID("owner")
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+id, nil)
	owner.fail(http.StatusConflict, "owner_protected", http.MethodPatch, "/api/v1/users/"+id, map[string]string{"status": "disabled"}, header("If-Match", r.header.Get("ETag")))
	owner.fail(http.StatusConflict, "owner_protected", http.MethodDelete, "/api/v1/users/"+id, nil)
	owner.fail(http.StatusConflict, "owner_protected", http.MethodPost, "/api/v1/users/"+id+"/factor-resets", nil)
	owner.fail(http.StatusConflict, "owner_protected", http.MethodPost, "/api/v1/users/"+id+"/password-resets", nil)
	ctx := testutil.Context(t)
	if _, err := e.m.DB().ExecContext(ctx, "DELETE FROM users WHERE id = ?", id); err == nil {
		t.Fatal("database deleted the owner")
	}
	if _, err := e.m.DB().ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = ?", id); err == nil {
		t.Fatal("database disabled the owner")
	}
	if _, err := e.m.DB().ExecContext(ctx, "UPDATE users SET is_owner = 0 WHERE id = ?", id); err == nil {
		t.Fatal("database demoted the owner")
	}
	// Stale If-Match on user edits.
	owner.fail(http.StatusPreconditionFailed, "precondition_failed", http.MethodPatch, "/api/v1/users/"+id, map[string]string{"displayName": "x"}, header("If-Match", `"999"`))
	owner.fail(http.StatusPreconditionRequired, "precondition_required", http.MethodPatch, "/api/v1/users/"+id, map[string]string{"displayName": "x"})
}

// TestPasswordChange: other sessions end, this one continues, the policy
// applies and the old password stops working.
func TestPasswordChange(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	a, pw, _ := e.newUser(owner, "pia")
	b := e.client()
	b.signIn("pia", pw)
	a.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, "/api/v1/me/password", map[string]string{"currentPassword": pw, "newPassword": "passwordpassword"})
	a.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPatch, "/api/v1/me/password", map[string]string{"currentPassword": e.secrets.New(canary.Password, "x"), "newPassword": e.secrets.New(canary.Password, "y")})
	newPW := e.secrets.New(canary.Password, "pia new")
	before := a.cookie
	a.must(http.StatusNoContent, http.MethodPatch, "/api/v1/me/password", map[string]string{"currentPassword": pw, "newPassword": newPW})
	if a.cookie == before || a.cookie == "" {
		t.Fatal("password change did not renew the session token")
	}
	a.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	b.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "pia", "password": pw})
	e.client().signIn("pia", newPW)
}

// TestIdempotentResponsesForgottenOnRevocation: stored Idempotency-Key
// responses of a principal are dropped when its sessions end.
func TestIdempotentResponsesForgottenOnRevocation(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	create := func() (string, string) {
		var out struct {
			Invitation struct {
				ID string `json:"id"`
			} `json:"invitation"`
			Code string `json:"code"`
		}
		r := owner.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", nil, secretOK, header("Idempotency-Key", "invite-1"))
		r.json(t, &out)
		e.secrets.Register("invitation-code", "idem "+out.Invitation.ID, out.Code)
		return out.Invitation.ID, r.header.Get("Idempotent-Replayed")
	}
	id1, replayed := create()
	id2, replayed2 := create()
	if id1 != id2 || replayed != "" || replayed2 != "true" {
		t.Fatalf("replay: %s %q / %s %q", id1, replayed, id2, replayed2)
	}
	owner.must(http.StatusNoContent, http.MethodPost, "/api/v1/users/"+e.userID("owner")+"/session-revocations", nil)
	owner.signIn("owner", pw)
	id3, replayed3 := create()
	if id3 == id1 || replayed3 != "" {
		t.Fatalf("stored response survived the revocation: %s %q", id3, replayed3)
	}
}

// TestSecretsAtRest: passwords, TOTP seeds, recovery codes, invitation and
// reset codes and session tokens never appear in the database in clear.
func TestSecretsAtRest(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	owner.enrollTOTP()
	var codes struct {
		Codes []string `json:"codes"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/me/recovery-codes", nil, secretOK).json(t, &codes)
	for i, c := range codes.Codes {
		e.secrets.Register("recovery-code", fmt.Sprint("recovery ", i), c)
	}
	e.invite(owner, map[string]any{"email": "x@example.com"})
	e.secrets.Register("session-token", "owner session", owner.cookie)
	_, _, s := e.newUser(owner, "zoe")
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/users/"+s.User.ID+"/password-resets", nil, secretOK)

	ctx := testutil.Context(t)
	for _, table := range []string{"users", "sessions", "invitations", "recovery_codes", "account_resets", "passkeys", "idempotency_keys", "audit_events"} {
		rows, err := e.m.DB().QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		var dump strings.Builder
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				fmt.Fprintf(&dump, "%v\n", v)
			}
		}
		_ = rows.Close()
		e.secrets.AssertClean(t, "table "+table, dump.String())
	}
}

// TestIdentityAuditTrail: identity operations reach the #30 audit trail
// with the right actor, target and outcome (and no secrets, see
// TestSecretsAtRest).
func TestIdentityAuditTrail(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	ownerID := e.userID("owner")
	e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session",
		map[string]string{"username": "owner", "password": e.secrets.New(canary.Password, "bad")})
	e.client().signIn("owner", pw)
	invID, _ := e.invite(owner, nil)
	_, _, s := e.newUser(owner, "una")
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+s.User.ID, nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+s.User.ID, map[string]string{"status": "disabled"}, header("If-Match", r.header.Get("ETag")))

	type row struct {
		Action    string `bun:"action"`
		ActorKind string `bun:"actor_kind"`
		ActorUser string `bun:"actor_user_id"`
		Outcome   string `bun:"outcome"`
		Targets   string `bun:"targets"`
		Details   string `bun:"details"`
		Category  string `bun:"category"`
	}
	var rows []row
	if err := e.m.DB().NewRaw("SELECT action, actor_kind, actor_user_id, outcome, targets, details, category FROM audit_events ORDER BY seq").
		Scan(testutil.Context(t), &rows); err != nil {
		t.Fatal(err)
	}
	find := func(action, outcome string) row {
		t.Helper()
		for _, r := range rows {
			if r.Action == action && r.Outcome == outcome {
				return r
			}
		}
		t.Fatalf("no %s/%s record in %+v", action, outcome, rows)
		return row{}
	}
	if r := find("setup_owner.create", "success"); r.ActorKind != "user" || r.ActorUser != ownerID || r.Category != "identity" {
		t.Errorf("setup record %+v", r)
	}
	if r := find("auth_session.create", "denied"); r.ActorKind != "anonymous" || !strings.Contains(r.Details, "invalid_credentials") {
		t.Errorf("failed sign-in record %+v", r)
	}
	if r := find("auth_session.create", "success"); r.ActorKind != "user" || r.ActorUser != ownerID {
		t.Errorf("sign-in record %+v", r)
	}
	if r := find("invitation.create", "success"); r.ActorUser != ownerID || !strings.Contains(r.Targets, invID) {
		t.Errorf("invitation record %+v", r)
	}
	if r := find("invitation_redemption.create", "success"); r.ActorUser != s.User.ID {
		t.Errorf("redemption record %+v", r)
	}
	if r := find("user.update", "success"); r.ActorUser != ownerID || !strings.Contains(r.Targets, s.User.ID) || !strings.Contains(r.Details, "user.disable") {
		t.Errorf("disable record %+v", r)
	}
	if rep, err := e.m.Audit().Verify(testutil.Context(t)); err != nil || rep.Err() != nil {
		t.Fatalf("audit chain: %+v %v", rep, err)
	}
}
