package app

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/canary"
)

// sessionStay is the part of GET /auth/session these tests read.
type sessionStay struct {
	State        string `json:"state"`
	SessionID    string `json:"sessionId"`
	StaySignedIn bool   `json:"staySignedIn"`
}

type deviceList struct {
	Items []struct {
		ID           string `json:"id"`
		Current      bool   `json:"current"`
		StaySignedIn bool   `json:"staySignedIn"`
		IP           string `json:"ip"`
		UserAgent    string `json:"userAgent"`
	} `json:"items"`
}

func (c *client) staySignIn(username, pw string, stay bool, opts ...reqOpt) (sessionStay, string) {
	c.e.t.Helper()
	r := c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session",
		map[string]any{"username": username, "password": pw, "staySignedIn": stay}, opts...)
	var s sessionStay
	r.json(c.e.t, &s)
	return s, strings.Join(r.header.Values("Set-Cookie"), "\n")
}

func (c *client) devices(path string) deviceList {
	c.e.t.Helper()
	var l deviceList
	c.must(http.StatusOK, http.MethodGet, path, nil).json(c.e.t, &l)
	return l
}

// TestStaySignedIn: "Stay signed in" keeps a device signed in with a
// persistent cookie for 30 days of inactivity and a year at most.
func TestStaySignedIn(t *testing.T) {
	e := newEnv(t)
	_, pw := e.setupOwner()
	var status struct {
		StaySignedInAllowed bool `json:"staySignedInAllowed"`
	}
	e.client().must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil).json(t, &status)
	if !status.StaySignedInAllowed {
		t.Fatal("stay signed in is not offered by default")
	}

	c := e.client()
	s, raw := c.staySignIn("owner", pw, true)
	if !s.StaySignedIn || s.SessionID == "" || !strings.Contains(raw, "Max-Age=") {
		t.Fatalf("stay signed in session %+v, cookie %q", s, raw)
	}
	// 30 days of inactivity.
	for range 2 {
		e.clk.Advance(29 * 24 * time.Hour)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	}
	e.clk.Advance(30 * 24 * time.Hour)
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// A year at most, whatever the activity; step-ups keep the lifetime.
	c.staySignIn("owner", pw, true)
	for range 13 {
		e.clk.Advance(28 * 24 * time.Hour)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	}
	c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": pw})
	e.clk.Advance(2 * 24 * time.Hour) // 366 days after sign-in
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
}

// TestStaySignedInPolicy: when the owner no longer allows "Stay signed
// in", new sign-ins get normal sessions and existing ones fall back to the
// normal limits.
func TestStaySignedInPolicy(t *testing.T) {
	e := newEnv(t)
	owner, pw := e.setupOwner()
	c := e.client()
	if s, _ := c.staySignIn("owner", pw, true); !s.StaySignedIn {
		t.Fatalf("session %+v", s)
	}
	owner.setSecurity(map[string]any{"allowStaySignedIn": false})

	var status struct {
		StaySignedInAllowed bool `json:"staySignedInAllowed"`
	}
	e.client().must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil).json(t, &status)
	if status.StaySignedInAllowed {
		t.Fatal("setup status still offers stay signed in")
	}
	r := c.must(http.StatusOK, http.MethodGet, "/api/v1/auth/session", nil)
	var s sessionStay
	r.json(t, &s)
	if raw := strings.Join(r.header.Values("Set-Cookie"), "\n"); s.StaySignedIn || strings.Contains(raw, "Max-Age=") {
		t.Fatalf("session not moved to the normal limits: %+v, cookie %q", s, raw)
	}
	e.clk.Advance(9 * time.Hour)
	c.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	s, raw := e.client().staySignIn("owner", pw, true)
	if s.StaySignedIn || strings.Contains(raw, "Max-Age=") {
		t.Fatalf("stay signed in granted against the policy: %+v, cookie %q", s, raw)
	}
}

// TestStaySignedInPendingSignIn: a sign-in that needs a second factor keeps
// the choice of its first step.
func TestStaySignedInPendingSignIn(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	bob, pw, _ := e.newUser(owner, "bob")
	secret, _ := bob.enrollTOTP()
	e.clk.Advance(30 * time.Second)

	c := e.client()
	if s, _ := c.staySignIn("bob", pw, true); s.State != "second_factor_required" {
		t.Fatalf("pending sign-in %+v", s)
	}
	var s sessionStay
	c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": e.totpCode(secret)}).json(t, &s)
	if s.State != "authenticated" || !s.StaySignedIn {
		t.Fatalf("completed sign-in %+v", s)
	}
}

// TestSignedInDevices: users list their devices and sign them out one by
// one or all but the current one; the owner does the same for any user.
func TestSignedInDevices(t *testing.T) {
	e := newEnv(t)
	owner, _ := e.setupOwner()
	bob, pw, first := e.newUser(owner, "bob")
	phone := e.client()
	phone.staySignIn("bob", pw, true, header("User-Agent", "Mozilla/5.0 (iPhone) Mobile Safari"))

	l := bob.devices("/api/v1/me/sessions")
	if len(l.Items) != 2 || l.Items[0].Current == l.Items[1].Current {
		t.Fatalf("devices %+v", l)
	}
	p := l.Items[0]
	if p.Current {
		p = l.Items[1]
	}
	if !p.StaySignedIn || p.IP != phone.ip || !strings.Contains(p.UserAgent, "iPhone") {
		t.Fatalf("phone %+v (ip %s)", p, phone.ip)
	}
	// Another user's device is not found.
	owner.fail(http.StatusNotFound, "not_found", http.MethodDelete, "/api/v1/me/sessions/"+p.ID, nil)

	bob.must(http.StatusNoContent, http.MethodDelete, "/api/v1/me/sessions/"+p.ID, nil)
	phone.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	bob.fail(http.StatusNotFound, "not_found", http.MethodDelete, "/api/v1/me/sessions/"+p.ID, nil)
	bob.must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)

	// Sign out every other device.
	laptop, tablet := e.client(), e.client()
	laptop.signIn("bob", pw)
	tablet.signIn("bob", pw)
	var out struct {
		Count int `json:"count"`
	}
	bob.must(http.StatusOK, http.MethodPost, "/api/v1/me/session-revocations", nil).json(t, &out)
	if out.Count != 2 {
		t.Fatalf("signed out %d devices", out.Count)
	}
	laptop.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	tablet.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	if l := bob.devices("/api/v1/me/sessions"); len(l.Items) != 1 || !l.Items[0].Current {
		t.Fatalf("devices after signing out the others %+v", l)
	}

	// Signing out removes the device.
	laptop.signIn("bob", pw)
	laptop.must(http.StatusNoContent, http.MethodDelete, "/api/v1/auth/session", nil)
	if l := bob.devices("/api/v1/me/sessions"); len(l.Items) != 1 {
		t.Fatalf("devices after sign-out %+v", l)
	}

	// The owner lists and signs out a user's devices; users cannot.
	bobID := first.User.ID
	bob.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/users/"+bobID+"/sessions", nil)
	l = owner.devices("/api/v1/users/" + bobID + "/sessions")
	if len(l.Items) != 1 || l.Items[0].Current {
		t.Fatalf("owner's view %+v", l)
	}
	owner.fail(http.StatusNotFound, "not_found", http.MethodDelete, "/api/v1/users/"+bobID+"/sessions/missing", nil)
	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/users/"+bobID+"/sessions/"+l.Items[0].ID, nil)
	bob.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)

	// A password change keeps only the device that made it.
	bob.signIn("bob", pw)
	laptop.signIn("bob", pw)
	newPW := e.secrets.New(canary.Password, "bob new password")
	bob.must(http.StatusNoContent, http.MethodPatch, "/api/v1/me/password", map[string]string{"currentPassword": pw, "newPassword": newPW})
	laptop.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/me", nil)
	if l := bob.devices("/api/v1/me/sessions"); len(l.Items) != 1 || !l.Items[0].Current {
		t.Fatalf("devices after a password change %+v", l)
	}
}
