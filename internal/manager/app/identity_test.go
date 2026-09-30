package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/auth"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// TestConcurrentFirstRunSetupCreatesOneOwner: many simultaneous setup
// requests create exactly one owner; everyone else (and every later
// request) gets 409 setup_complete.
func TestConcurrentFirstRunSetupCreatesOneOwner(t *testing.T) {
	e := newEnv(t)
	st := e.client().must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil)
	var status struct {
		SetupComplete bool `json:"setupComplete"`
		SecureOrigin  bool `json:"secureOrigin"`
	}
	st.json(t, &status)
	if status.SetupComplete || !status.SecureOrigin {
		t.Fatalf("status before setup %s", st.body)
	}
	const n = 24
	pw := e.secrets.New(canary.Password, "setup password")
	var wg sync.WaitGroup
	results := make([]response, n)
	for i := range n {
		wg.Go(func() {
			c := e.client()
			results[i] = c.do(http.MethodPost, "/api/v1/setup/owner", map[string]string{"username": "owner" + itoa(i), "password": pw})
		})
	}
	wg.Wait()
	created := 0
	for _, r := range results {
		switch {
		case r.status == http.StatusCreated:
			created++
		case r.status == http.StatusConflict && r.code() == "setup_complete":
		default:
			t.Errorf("setup: %d %s", r.status, r.body)
		}
	}
	var owners, users int
	ctx := testutil.Context(t)
	if err := e.m.DB().NewRaw("SELECT count(*) FROM users WHERE is_owner = 1").Scan(ctx, &owners); err != nil {
		t.Fatal(err)
	}
	if err := e.m.DB().NewRaw("SELECT count(*) FROM users").Scan(ctx, &users); err != nil {
		t.Fatal(err)
	}
	if created != 1 || owners != 1 || users != 1 {
		t.Fatalf("created %d, owners %d, users %d; want exactly one", created, owners, users)
	}
	e.client().fail(http.StatusConflict, "setup_complete", http.MethodPost, "/api/v1/setup/owner", map[string]string{"username": "late", "password": pw})
	e.client().must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil).json(t, &status)
	if !status.SetupComplete {
		t.Fatal("setup not reported complete")
	}
}

// TestSetupRefusesInsecureOrigin: setup only completes over HTTPS on the
// public origin, as seen through a trusted proxy.
func TestSetupRefusesInsecureOrigin(t *testing.T) {
	e := newEnv(t)
	pw := e.secrets.New(canary.Password, "pw")
	body := map[string]string{"username": "owner", "password": pw}
	c := e.client()
	r := c.fail(http.StatusForbidden, "insecure_origin", http.MethodPost, "/api/v1/setup/owner", body, header("X-Forwarded-Proto", "http"))
	if !strings.Contains(string(r.body), "HTTPS") {
		t.Fatalf("no explanation: %s", r.body)
	}
	c.fail(http.StatusForbidden, "insecure_origin", http.MethodPost, "/api/v1/setup/owner", body, header("X-Forwarded-Host", "docker-manager:8080"))
	var status struct {
		SecureOrigin bool   `json:"secureOrigin"`
		Explanation  string `json:"explanation"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil, header("X-Forwarded-Proto", "http")).json(t, &status)
	if status.SecureOrigin || status.Explanation == "" {
		t.Fatalf("status %+v", status)
	}
	// Weak passwords are refused before anything is created.
	c.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, "/api/v1/setup/owner", map[string]string{"username": "owner", "password": "passwordpassword"})
	e.setupOwner()
}

// TestSignInEnumerationResistance: unknown accounts, wrong passwords and
// disabled accounts answer identically and cost one Argon2id computation.
func TestSignInEnumerationResistance(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	_, pw, s := e.newUser(owner, "alice")
	var acct struct {
		Revision int64 `json:"revision"`
	}
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/users/"+s.User.ID, nil)
	r.json(t, &acct)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/users/"+s.User.ID, map[string]string{"status": "disabled"}, header("If-Match", r.header.Get("ETag")))

	attempts := map[string]map[string]string{
		"unknown user":   {"username": "mallory", "password": pw},
		"wrong password": {"username": "owner", "password": e.secrets.New(canary.Password, "wrong")},
		"disabled user":  {"username": "alice", "password": pw},
	}
	var bodies []string
	for name, body := range attempts {
		c := e.client()
		before := e.computes.Load()
		r := c.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", body)
		if n := e.computes.Load() - before; n != 1 {
			t.Errorf("%s: %d Argon2id computations, want 1", name, n)
		}
		var b map[string]any
		r.json(t, &b)
		delete(b, "requestId")
		out, _ := json.Marshal(b)
		bodies = append(bodies, string(out))
		if c.cookie != "" {
			t.Errorf("%s: failed sign-in set a session cookie", name)
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("responses differ:\n%s\n%s", bodies[0], b)
		}
	}
}

// TestSignInThrottling: repeated failures for one account are limited
// even across client IPs, and one IP is limited across accounts.
func TestSignInThrottling(t *testing.T) {
	e := newEnv(t)
	_, pw := e.setupOwner()
	wrong := map[string]string{"username": "owner", "password": e.secrets.New(canary.Password, "wrong")}
	for range auth.PerAccount.Burst {
		e.client().fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", wrong)
	}
	r := e.client().fail(http.StatusTooManyRequests, "rate_limited", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "owner", "password": pw})
	if r.header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	e.clk.Advance(auth.PerAccount.Every)
	if s := e.client().signIn("owner", pw); s.State != "authenticated" {
		t.Fatalf("after refill: %+v", s)
	}
	// One IP guessing many accounts.
	c := e.client()
	for i := range auth.PerIP.Burst {
		c.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "user" + itoa(i), "password": pw})
	}
	c.fail(http.StatusTooManyRequests, "rate_limited", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "someone", "password": pw})
}

// TestInvitations: single use, expiry, revocation, email binding and
// brute-force throttling; redeemed users join the default group.
func TestInvitations(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	// No self-registration: without a valid code nothing is created.
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": "dyi_" + strings.Repeat("A", 43), "username": "eve", "password": e.secrets.New(canary.Password, "eve")})

	id, code := e.invite(owner, nil)
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/invitations", nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Status != "pending" {
		t.Fatalf("list %+v", list)
	}
	pw := e.secrets.New(canary.Password, "bob")
	bob := e.client()
	s := bob.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": code, "username": "bob", "password": pw}).session(t)
	var groupID string
	if err := e.m.DB().NewRaw("SELECT group_id FROM default_group").Scan(testutil.Context(t), &groupID); err != nil {
		t.Fatal(err)
	}
	if s.State != "authenticated" || s.User.GroupID != groupID || s.User.Owner {
		t.Fatalf("redeemed session %+v (default group %s)", s, groupID)
	}
	// Second redemption of the same code.
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": code, "username": "bob2", "password": pw})
	owner.fail(http.StatusConflict, "invitation_redeemed", http.MethodDelete, "/api/v1/invitations/"+id, nil)

	// Revoked.
	rid, rcode := e.invite(owner, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/invitations/"+rid, nil)
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": rcode, "username": "carol", "password": pw})

	// Expired.
	_, xcode := e.invite(owner, map[string]any{"expiresInHours": 1})
	e.clk.Advance(time.Hour)
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": xcode, "username": "dave", "password": pw})

	// Bound to an email address.
	owner.signIn("owner", e.ownerPassword(owner))
	_, bcode := e.invite(owner, map[string]any{"email": "erin@example.com"})
	e.client().fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": bcode, "username": "erin", "password": pw, "email": "mallory@example.com"})
	e.client().must(http.StatusCreated, http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": bcode, "username": "erin", "password": e.secrets.New(canary.Password, "erin"), "email": "ERIN@example.com"})

	// Brute force: after the per-IP budget of failures even a valid code is refused.
	_, vcode := e.invite(owner, nil)
	attacker := e.client()
	for range auth.PerIP.Burst {
		attacker.fail(http.StatusBadRequest, "invalid_code", http.MethodPost, "/api/v1/invitations/redemptions",
			map[string]string{"code": "dyi_" + strings.Repeat("B", 43), "username": "x", "password": pw})
	}
	attacker.fail(http.StatusTooManyRequests, "rate_limited", http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": vcode, "username": "frank", "password": pw})
	e.client().must(http.StatusCreated, http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": vcode, "username": "frank", "password": e.secrets.New(canary.Password, "frank")})
}

// TestConcurrentInvitationRedemptionIsOneUse (#12 "one-use invite
// redemption"): simultaneous redemptions of one code create exactly one
// account; every other request fails like an unknown code and the
// invitation ends up redeemed by that one account.
func TestConcurrentInvitationRedemptionIsOneUse(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	id, code := e.invite(owner, nil)
	const n = 6 // below the per-IP failure burst, so no request is throttled
	pw := e.secrets.New(canary.Password, "invitee password")
	var wg sync.WaitGroup
	results := make([]response, n)
	for i := range n {
		wg.Go(func() {
			results[i] = e.client().do(http.MethodPost, "/api/v1/invitations/redemptions",
				map[string]string{"code": code, "username": "invitee" + itoa(i), "password": pw})
		})
	}
	wg.Wait()
	created := 0
	for _, r := range results {
		switch {
		case r.status == http.StatusCreated:
			created++
		case r.status == http.StatusBadRequest && r.code() == "invalid_code":
		default:
			t.Errorf("redemption: %d %s", r.status, r.body)
		}
	}
	var users int
	if err := e.m.DB().NewRaw("SELECT count(*) FROM users WHERE is_owner = 0").Scan(testutil.Context(t), &users); err != nil {
		t.Fatal(err)
	}
	if created != 1 || users != 1 {
		t.Fatalf("created %d, accounts %d; want exactly one", created, users)
	}
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/invitations", nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Status != "redeemed" {
		t.Fatalf("invitation after the race: %+v", list.Items)
	}
}

// ownerPassword returns the canary registered for the owner.
func (e *env) ownerPassword(*client) string {
	for _, c := range e.secrets.All() {
		if c.Name == "owner password" {
			return c.Value
		}
	}
	e.t.Fatal("no owner password")
	return ""
}

// TestRestrictedUserSeesNothing: a new account in the Restricted group
// sees no jobs and cannot use owner routes; the owner sees everything.
func TestRestrictedUserSeesNothing(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	var me struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).json(t, &me)
	job, _, err := e.m.Jobs().Enqueue(testutil.Context(t), jobs.Request{
		Kind: "container.restart", Principal: authz.Principal{Kind: authz.KindUser, UserID: me.ID},
		EnvironmentID: "env-1", Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/jobs", nil).json(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("owner jobs %+v", page)
	}

	user, _, s := e.newUser(owner, "rita")
	if s.State != "authenticated" {
		t.Fatalf("session %+v", s)
	}
	user.must(http.StatusOK, http.MethodGet, "/api/v1/jobs", nil).json(t, &page)
	if len(page.Items) != 0 {
		t.Fatalf("restricted user sees jobs: %+v", page)
	}
	user.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+job.ID, nil)
	user.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/jobs/"+job.ID+"/events/stream", nil)
	user.fail(http.StatusNotFound, "not_found", http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancellations", nil)
	for _, p := range []string{"/api/v1/users", "/api/v1/invitations", "/api/v1/settings/security", "/api/v1/users/" + me.ID} {
		user.fail(http.StatusForbidden, "forbidden", http.MethodGet, p, nil)
	}
	user.fail(http.StatusForbidden, "forbidden", http.MethodPost, "/api/v1/invitations", nil)
	user.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	// Anonymous callers get 401 everywhere.
	e.client().fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/jobs", nil)
	e.client().fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestCrossOriginRequestsRejected: browser requests with unsafe methods
// from another origin are refused before any handler runs.
func TestCrossOriginRequestsRejected(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	owner.fail(http.StatusForbidden, "cross_origin_request", http.MethodPost, "/api/v1/invitations", nil,
		header("Origin", "https://evil.example"), header("Sec-Fetch-Site", "cross-site"))
	e.client().fail(http.StatusForbidden, "cross_origin_request", http.MethodPost, "/api/v1/auth/session",
		map[string]string{"username": "owner", "password": pw}, header("Origin", "https://evil.example"), header("Sec-Fetch-Site", "cross-site"))
	owner.fail(http.StatusForbidden, "cross_origin_request", http.MethodDelete, "/api/v1/auth/session", nil,
		header("Origin", "https://docker.example.com.evil.example"), header("Sec-Fetch-Site", ""))
	// A bearer request never uses the cookie, so it is not a CSRF vector (and
	// carries no session: 401 until API tokens exist, #31).
	owner.fail(http.StatusUnauthorized, "unauthenticated", http.MethodPost, "/api/v1/invitations", nil,
		header("Authorization", "Bearer dyt_notatoken0000000000"), header("Origin", "https://evil.example"), header("Sec-Fetch-Site", "cross-site"))
	// The session still works from the right origin.
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
}

// TestSessionLifecycle: cookie attributes, renewal on privilege change,
// idle and absolute expiry, sign-out.
func TestSessionLifecycle(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	c := e.client()
	r := c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "owner", "password": pw})
	raw := strings.Join(r.header.Values("Set-Cookie"), "\n")
	for _, want := range []string{cookieName + "=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("Set-Cookie %q lacks %q", raw, want)
		}
	}
	first := c.cookie
	// Step-up renews the token; the old one stops working.
	c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": pw})
	if c.cookie == first || c.cookie == "" {
		t.Fatal("step-up did not renew the session token")
	}
	stale := &client{e: e, cookie: first, ip: c.ip, base: c.base}
	stale.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// Without "Stay signed in" the cookie ends with the browser.
	if strings.Contains(raw, "Max-Age=") || strings.Contains(raw, "Expires=") {
		t.Fatalf("session cookie %q persists", raw)
	}

	// Idle timeout (8 h by default).
	e.clk.Advance(7*time.Hour + 59*time.Minute)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	e.clk.Advance(7*time.Hour + 59*time.Minute)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	e.clk.Advance(8 * time.Hour)
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// Absolute lifetime (24 h) despite activity.
	c.signIn("owner", pw)
	for range 4 {
		e.clk.Advance(5*time.Hour + 50*time.Minute)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	}
	e.clk.Advance(50 * time.Minute) // 24h10m after sign-in
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// Sign-out clears the cookie and ends the session.
	owner.signIn("owner", pw)
	saved := owner.cookie
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)
	if owner.cookie != "" {
		t.Fatal("sign-out did not clear the cookie")
	}
	(&client{e: e, cookie: saved, ip: owner.ip, base: owner.base}).fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestStepUpRequired: sensitive changes need a recent re-authentication.
func TestStepUpRequired(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	e.clk.Advance(auth.StepUpWindow + time.Second)
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPost, "/api/v1/invitations", nil)
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPost, "/api/v1/me/recovery-codes", nil)
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/settings/security", nil)
	owner.fail(http.StatusForbidden, "step_up_required", http.MethodPatch, "/api/v1/settings/security", map[string]any{"minPasswordLength": 20}, header("If-Match", r.header.Get("ETag")))
	owner.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": e.secrets.New(canary.Password, "nope")})
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": pw})
	e.invite(owner, nil)
}

// TestRequiredTOTPPolicy: with TOTP required, existing sessions end, users
// get a limited enrollment session until they enroll, sign-in then needs
// password + TOTP (replay-safe), and recovery codes / owner resets are the
// documented recovery path.
func TestRequiredTOTPPolicy(t *testing.T) {
	e := newEnv(t)
	owner, ownerPW := e.setupOwner()
	alice, alicePW, _ := e.newUser(owner, "alice")
	late, latePW, _ := e.newUser(owner, "late")

	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/settings/security", nil)
	var set struct {
		RequiredFactors      string `json:"requiredFactors"`
		EnrollmentGraceHours int    `json:"enrollmentGraceHours"`
	}
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/settings/security", map[string]any{"requiredFactors": "totp"},
		header("If-Match", r.header.Get("ETag"))).json(t, &set)
	if set.RequiredFactors != "totp" {
		t.Fatalf("settings %+v", set)
	}
	// Full-access sessions do not survive the stricter policy.
	alice.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	late.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	// The owner lacks TOTP: limited to enrollment, never locked out.
	s := owner.must(http.StatusOK, http.MethodGet, "/api/v1/auth/session", nil).session(t)
	if s.State != "enrollment_required" || len(s.MissingFactors) != 1 || s.MissingFactors[0] != "totp" || s.EnrollmentDeadline != nil {
		t.Fatalf("owner session after policy change %+v", s)
	}
	owner.fail(http.StatusForbidden, "enrollment_required", http.MethodGet, "/api/v1/users", nil)
	owner.fail(http.StatusForbidden, "enrollment_required", http.MethodGet, "/api/v1/jobs", nil)
	ownerSecret, s := owner.enrollTOTP()
	if s.State != "authenticated" {
		t.Fatalf("owner after enrollment %+v", s)
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/users", nil)

	// Alice signs in with her password: enrollment session with a deadline.
	s = alice.signIn("alice", alicePW)
	if s.State != "enrollment_required" || s.EnrollmentDeadline == nil {
		t.Fatalf("alice %+v", s)
	}
	alice.fail(http.StatusForbidden, "enrollment_required", http.MethodGet, "/api/v1/jobs", nil)
	secret, s := alice.enrollTOTP()
	if s.State != "authenticated" {
		t.Fatalf("alice after enrollment %+v", s)
	}
	var codes struct {
		Codes []string `json:"codes"`
	}
	alice.must(http.StatusCreated, http.MethodPost, "/api/v1/me/recovery-codes", nil, secretOK).json(t, &codes)
	if len(codes.Codes) != auth.RecoveryCodeCount {
		t.Fatalf("codes %+v", codes)
	}
	for i, c := range codes.Codes {
		e.secrets.Register("recovery-code", "recovery code "+itoa(i), c)
	}
	alice.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)

	// Password alone is not enough any more.
	s = alice.signIn("alice", alicePW)
	if s.State != "second_factor_required" || strings.Join(s.Factors, ",") != "totp,recovery_code" {
		t.Fatalf("pending %+v", s)
	}
	alice.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	// The code used for enrollment was already accepted in this time step: replay.
	used := e.totpCode(secret)
	alice.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": used})
	e.clk.Advance(30 * time.Second)
	fresh := e.totpCode(secret)
	if s = alice.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": fresh}).session(t); s.State != "authenticated" {
		t.Fatalf("after totp %+v", s)
	}
	// The same code cannot sign in a second browser.
	other := e.client()
	other.signIn("alice", alicePW)
	other.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": fresh})

	// Lost phone: a recovery code completes the sign-in, once.
	lost := e.client()
	lost.signIn("alice", alicePW)
	if s = lost.must(http.StatusOK, http.MethodPost, "/api/v1/auth/recovery-codes/redemptions", map[string]string{"code": codes.Codes[0]}).session(t); s.State != "authenticated" {
		t.Fatalf("recovery %+v", s)
	}
	again := e.client()
	again.signIn("alice", alicePW)
	again.fail(http.StatusUnauthorized, "invalid_credentials", http.MethodPost, "/api/v1/auth/recovery-codes/redemptions", map[string]string{"code": codes.Codes[0]})
	var rc struct {
		Remaining int `json:"remaining"`
	}
	lost.must(http.StatusOK, http.MethodGet, "/api/v1/me/recovery-codes", nil).json(t, &rc)
	if rc.Remaining != auth.RecoveryCodeCount-1 {
		t.Fatalf("remaining %d", rc.Remaining)
	}
	// Removing the only required factor is refused.
	lost.fail(http.StatusConflict, "factor_required", http.MethodDelete, "/api/v1/auth/totp", nil)

	// "late" never enrolls: after the grace period only an owner reset helps.
	s = late.signIn("late", latePW)
	if s.State != "enrollment_required" {
		t.Fatalf("late %+v", s)
	}
	late.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)
	e.clk.Advance(time.Duration(set.EnrollmentGraceHours)*time.Hour + time.Minute)
	late.fail(http.StatusForbidden, "enrollment_expired", http.MethodPost, "/api/v1/auth/session", map[string]string{"username": "late", "password": latePW})
	owner.signIn("owner", ownerPW)
	e.clk.Advance(30 * time.Second)
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": e.totpCode(ownerSecret)})
	var lateID string
	if err := e.m.DB().NewRaw("SELECT id FROM users WHERE username = 'late'").Scan(testutil.Context(t), &lateID); err != nil {
		t.Fatal(err)
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/users/"+lateID+"/factor-resets", nil)
	if s = late.signIn("late", latePW); s.State != "enrollment_required" {
		t.Fatalf("late after reset %+v", s)
	}
}

// TestPasskeyPolicyAndWebAuthn: passkey-only accounts through the API with
// a software authenticator; ceremonies against the manager's internal
// address or another RP ID fail.
func TestPasskeyPolicyAndWebAuthn(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/settings/security", nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/settings/security", map[string]any{"requiredFactors": "passkey"}, header("If-Match", r.header.Get("ETag")))
	ownerDev := newDevice(publicOrigin, publicHost)
	if reg := owner.registerPasskey(ownerDev, "owner key"); reg.status != http.StatusCreated {
		t.Fatalf("owner passkey: %d %s", reg.status, reg.body)
	}

	_, code := e.invite(owner, nil)
	pat := e.client()
	s := pat.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations/redemptions", map[string]string{"code": code, "username": "pat"}).session(t)
	if s.State != "enrollment_required" || strings.Join(s.MissingFactors, ",") != "passkey" {
		t.Fatalf("passkey-only redemption %+v", s)
	}
	pat.fail(http.StatusForbidden, "enrollment_required", http.MethodGet, "/api/v1/jobs", nil)

	// Registration through the internal address or for another RP ID fails.
	for _, bad := range []*device{newDevice("http://docker-manager:8080", publicHost), newDevice(publicOrigin, "example.com"), newDevice("http://docker.example.com", publicHost)} {
		if reg := pat.registerPasskey(bad, "bad"); reg.status != http.StatusUnprocessableEntity {
			t.Fatalf("registration from %s/%s: %d %s", bad.rp.Origin, bad.rp.ID, reg.status, reg.body)
		}
	}
	dev := newDevice(publicOrigin, publicHost)
	reg := pat.registerPasskey(dev, "laptop")
	if reg.status != http.StatusCreated {
		t.Fatalf("registration %d %s", reg.status, reg.body)
	}
	var out struct {
		Passkey struct {
			ID             string `json:"id"`
			BackupEligible bool   `json:"backupEligible"`
		} `json:"passkey"`
		Session sessionBody `json:"session"`
	}
	reg.json(t, &out)
	if out.Session.State != "authenticated" || !out.Passkey.BackupEligible {
		t.Fatalf("after registration %+v", out)
	}
	pat.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)

	// Username-less sign-in with the passkey.
	b := e.client()
	if s = b.assertPasskey(dev, "sign_in", "/api/v1/auth/passkeys/authentication-verifications").session(t); s.State != "authenticated" || s.User.Username != "pat" {
		t.Fatalf("passkey sign-in %+v", s)
	}
	var sc int64
	if err := e.m.DB().NewRaw("SELECT sign_count FROM passkeys WHERE id = ?", out.Passkey.ID).Scan(testutil.Context(t), &sc); err != nil || sc != int64(dev.cred.Counter) {
		t.Fatalf("stored counter %d (%v), device %d", sc, err, dev.cred.Counter)
	}
	// The same passkey through the internal address, or a clone replaying
	// a stale counter, fails.
	verify := "/api/v1/auth/passkeys/authentication-verifications"
	internal := &device{rp: dev.rp, auth: dev.auth, cred: dev.cred}
	internal.rp.Origin = "http://docker-manager:8080"
	if r := e.client().assertPasskey(internal, "sign_in", verify); r.status != http.StatusUnauthorized || r.code() != "invalid_credentials" {
		t.Fatalf("internal origin: %d %s", r.status, r.body)
	}
	otherRP := &device{rp: dev.rp, auth: dev.auth, cred: dev.cred}
	otherRP.rp.ID = "example.com"
	if r := e.client().assertPasskey(otherRP, "sign_in", verify); r.status != http.StatusUnauthorized {
		t.Fatalf("other RP ID: %d %s", r.status, r.body)
	}
	clone := &device{rp: dev.rp, auth: dev.auth, cred: dev.cred}
	clone.cred.Counter = 0
	if r := e.client().assertPasskey(clone, "sign_in", verify); r.status != http.StatusUnauthorized {
		t.Fatalf("cloned authenticator: %d %s", r.status, r.body)
	}
	// Step-up with the passkey, then the last passkey cannot be removed.
	e.clk.Advance(auth.StepUpWindow + time.Second)
	b.fail(http.StatusForbidden, "step_up_required", http.MethodDelete, "/api/v1/me/passkeys/"+out.Passkey.ID, nil)
	var st struct {
		RecentAuthUntil *time.Time `json:"recentAuthUntil"`
	}
	b.assertPasskey(dev, "step_up", "/api/v1/auth/step-ups").json(t, &st)
	if st.RecentAuthUntil == nil {
		t.Fatal("passkey step-up not recorded")
	}
	b.fail(http.StatusConflict, "factor_required", http.MethodDelete, "/api/v1/me/passkeys/"+out.Passkey.ID, nil)
	var pks struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/me/passkeys", nil).json(t, &pks)
	if len(pks.Items) != 1 || pks.Items[0].Name != "laptop" {
		t.Fatalf("passkeys %+v", pks)
	}
	// Renaming changes the label only (no step-up, no session ends).
	var renamed struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	b.must(http.StatusOK, http.MethodPatch, "/api/v1/me/passkeys/"+out.Passkey.ID, map[string]any{"name": "work laptop"}).json(t, &renamed)
	if renamed.ID != out.Passkey.ID || renamed.Name != "work laptop" {
		t.Fatalf("renamed passkey %+v", renamed)
	}
	b.must(http.StatusOK, http.MethodGet, "/api/v1/me/passkeys", nil).json(t, &pks)
	if len(pks.Items) != 1 || pks.Items[0].Name != "work laptop" {
		t.Fatalf("passkeys after rename %+v", pks)
	}
	b.fail(http.StatusNotFound, "not_found", http.MethodPatch, "/api/v1/me/passkeys/nope", map[string]any{"name": "x"})
	b.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPatch, "/api/v1/me/passkeys/"+out.Passkey.ID, map[string]any{"name": ""})
	// A second passkey makes the first removable.
	if reg := b.registerPasskey(newDevice(publicOrigin, publicHost), "phone"); reg.status != http.StatusCreated {
		t.Fatalf("second passkey %d %s", reg.status, reg.body)
	}
	b.must(http.StatusNoContent, http.MethodDelete, "/api/v1/me/passkeys/"+out.Passkey.ID, nil)
	if r := e.client().assertPasskey(dev, "sign_in", verify); r.status != http.StatusUnauthorized {
		t.Fatalf("revoked passkey signed in: %d %s", r.status, r.body)
	}
	// A password is not a way around the passkey policy: the owner's
	// password sign-in still needs the passkey.
	if s = e.client().signIn("owner", e.ownerPassword(owner)); s.State != "second_factor_required" || strings.Join(s.Factors, ",") != "passkey" {
		t.Fatalf("password under passkey policy %+v", s)
	}
	// Starting a new sign-in in a signed-in browser drops the old identity.
	if s = b.signIn("owner", e.ownerPassword(owner)); s.State != "second_factor_required" {
		t.Fatalf("sign-in over an existing session %+v", s)
	}
	b.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestBothFactorsPolicy: password + TOTP + passkey, in any order after the
// password; a pending sign-in grants nothing.
func TestBothFactorsPolicy(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	secret, _ := owner.enrollTOTP()
	dev := newDevice(publicOrigin, publicHost)
	if reg := owner.registerPasskey(dev, "key"); reg.status != http.StatusCreated {
		t.Fatalf("passkey %d %s", reg.status, reg.body)
	}
	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/settings/security", nil)
	owner.must(http.StatusOK, http.MethodPatch, "/api/v1/settings/security", map[string]any{"requiredFactors": "both"}, header("If-Match", r.header.Get("ETag")))

	c := e.client()
	s := c.signIn("owner", pw)
	if s.State != "second_factor_required" || strings.Join(s.Factors, ",") != "totp,passkey" {
		t.Fatalf("after password %+v", s)
	}
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/jobs", nil)
	e.clk.Advance(30 * time.Second)
	s = c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": e.totpCode(secret)}).session(t)
	if s.State != "second_factor_required" || strings.Join(s.Factors, ",") != "passkey" {
		t.Fatalf("after totp %+v", s)
	}
	if s = c.assertPasskey(dev, "sign_in", "/api/v1/auth/passkeys/authentication-verifications").session(t); s.State != "authenticated" {
		t.Fatalf("after passkey %+v", s)
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/users", nil)
	// A passkey alone does not satisfy "both".
	if r := e.client().assertPasskey(dev, "sign_in", "/api/v1/auth/passkeys/authentication-verifications"); r.status != http.StatusForbidden || r.code() != "sign_in_method_not_allowed" {
		t.Fatalf("passkey alone under both: %d %s", r.status, r.body)
	}
}
