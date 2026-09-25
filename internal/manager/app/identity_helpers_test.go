package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/manager/auth/password"
	"github.com/neurekadev/dockyard/internal/manager/auth/totp"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// The identity tests run the real manager handler behind a simulated TLS
// reverse proxy: DOCKYARD_PUBLIC_URL is https://docker.example.com, the
// test client is a trusted proxy (127.0.0.1) and sends X-Forwarded-Proto /
// X-Forwarded-Host like Caddy/Traefik/nginx do (#27).
const (
	publicOrigin = "https://docker.example.com"
	publicHost   = "docker.example.com"
	cookieName   = "__Host-dockyard_session"
)

// cheapParams keep Argon2id fast in tests (production: password.Current).
var cheapParams = &password.Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

type env struct {
	t        *testing.T
	m        *Manager
	srv      *httptest.Server
	clk      *clock.Fake
	secrets  *canary.Set
	computes atomic.Int64
	nextIP   atomic.Int32
}

func newEnv(t *testing.T, with ...func(*Options)) *env {
	t.Helper()
	dataDir := t.TempDir()
	// A fixed start instant: nothing a test checks may depend on the wall
	// clock (session idle/absolute expiry, hourly schedules, token TTLs).
	e := &env{t: t, clk: testutil.FakeClock(), secrets: canary.New()}
	cfg := config.Config{
		PublicURL:      &url.URL{Scheme: "https", Host: publicHost},
		ListenAddr:     "127.0.0.1:0",
		DataDir:        dataDir,
		SecretKeyFile:  filepath.Join(dataDir, config.SecretKeyFileName),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	}
	opts := Options{
		Config: cfg, Logger: e.secrets.CaptureLogger(t), UI: testUI, Clock: e.clk,
		PasswordParams: cheapParams, OnPasswordCompute: func() { e.computes.Add(1) },
	}
	for _, f := range with {
		f(&opts)
	}
	if c, ok := opts.Clock.(*clock.Fake); ok {
		e.clk = c // an option chose another start instant (withWallClockStart)
	}
	m, err := Start(testutil.Context(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	e.m = m
	e.srv = httptest.NewServer(m.Handler())
	t.Cleanup(func() {
		e.srv.Close()
		_ = e.m.Close() // the current manager (tests may restart it)
	})
	return e
}

// client is one browser (its own cookie and client IP).
type client struct {
	e      *env
	cookie string
	ip     string
	// base is the server the client talks to (default e.srv).
	base string
}

func (e *env) client() *client {
	n := e.nextIP.Add(1)
	return &client{e: e, ip: "198.51.100." + itoa(int(n)), base: e.srv.URL}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

// code is the error code of an error response.
func (r response) code() string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Code
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// secretOK marks a response that legitimately reveals a one-time secret
// (enrollment, invitation, reset, recovery codes): it is not canary-checked.
var secretOK reqOpt = func(r *http.Request) { r.Header.Set("X-Test-Secret-Response", "1") }

// do sends a browser-like request through the "proxy": same-origin fetch
// metadata, forwarded HTTPS and host, the client's IP and cookie. Every
// response not marked secretOK is checked for leaked canaries.
func (c *client) do(method, path string, body any, opts ...reqOpt) response {
	t := c.e.t
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(testutil.Context(t), method, c.base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", publicHost)
	req.Header.Set("X-Forwarded-For", c.ip)
	if method != http.MethodGet {
		req.Header.Set("Origin", publicOrigin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	for _, o := range opts {
		o(req)
	}
	checkSecrets := req.Header.Get("X-Test-Secret-Response") == ""
	req.Header.Del("X-Test-Secret-Response")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range resp.Cookies() {
		if sc.Name != cookieName {
			continue
		}
		if sc.MaxAge < 0 || sc.Value == "" {
			c.cookie = ""
		} else {
			c.cookie = sc.Value
		}
	}
	if checkSecrets {
		c.e.secrets.AssertClean(t, method+" "+path+" response", string(b))
		c.e.secrets.AssertClean(t, method+" "+path+" response headers", resp.Header)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

func (c *client) must(status int, method, path string, body any, opts ...reqOpt) response {
	c.e.t.Helper()
	r := c.do(method, path, body, opts...)
	if r.status != status {
		c.e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.status, status, r.body)
	}
	return r
}

func (c *client) fail(status int, code, method, path string, body any, opts ...reqOpt) response {
	c.e.t.Helper()
	r := c.do(method, path, body, opts...)
	if r.status != status || r.code() != code {
		c.e.t.Fatalf("%s %s: %d %s, want %d %s: %s", method, path, r.status, r.code(), status, code, r.body)
	}
	return r
}

type sessionBody struct {
	State string `json:"state"`
	User  *struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Owner    bool   `json:"owner"`
		GroupID  string `json:"groupId"`
		Factors  struct {
			TOTP     bool `json:"totp"`
			Passkeys int  `json:"passkeys"`
		} `json:"factors"`
	} `json:"user"`
	Factors            []string   `json:"factors"`
	MissingFactors     []string   `json:"missingFactors"`
	EnrollmentDeadline *time.Time `json:"enrollmentDeadline"`
}

func (r response) session(t *testing.T) sessionBody {
	t.Helper()
	var s sessionBody
	r.json(t, &s)
	return s
}

// setupOwner creates the owner through first-run setup and returns a
// signed-in client and the password.
func (e *env) setupOwner() (*client, string) {
	e.t.Helper()
	pw := e.secrets.New(canary.Password, "owner password")
	c := e.client()
	s := c.must(http.StatusCreated, http.MethodPost, "/api/v1/setup/owner",
		map[string]string{"username": "owner", "password": pw, "displayName": "The Owner"}).session(e.t)
	if s.State != "authenticated" || s.User == nil || !s.User.Owner {
		e.t.Fatalf("setup session %+v", s)
	}
	return c, pw
}

// invite creates an invitation as owner and returns the code.
func (e *env) invite(owner *client, body map[string]any) (id, code string) {
	e.t.Helper()
	var out struct {
		Invitation struct {
			ID string `json:"id"`
		} `json:"invitation"`
		Code string `json:"code"`
		URL  string `json:"url"`
	}
	var payload any
	if body != nil {
		payload = body
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", payload, secretOK).json(e.t, &out)
	if !strings.HasPrefix(out.Code, "dyi_") || !strings.HasPrefix(out.URL, publicOrigin+"/invitation#code=") {
		e.t.Fatalf("invitation %+v", out)
	}
	e.secrets.Register("invitation-code", "invitation "+out.Invitation.ID, out.Code)
	return out.Invitation.ID, out.Code
}

// newUser invites and redeems an account with a password.
func (e *env) newUser(owner *client, username string) (*client, string, sessionBody) {
	e.t.Helper()
	_, code := e.invite(owner, nil)
	pw := e.secrets.New(canary.Password, username+" password")
	c := e.client()
	s := c.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations/redemptions",
		map[string]string{"code": code, "username": username, "password": pw}).session(e.t)
	return c, pw, s
}

func (c *client) signIn(username, pw string) sessionBody {
	c.e.t.Helper()
	return c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"username": username, "password": pw}).session(c.e.t)
}

// enrollTOTP enrolls TOTP for the client's account and returns the secret.
func (c *client) enrollTOTP() (string, sessionBody) {
	t := c.e.t
	t.Helper()
	var enr struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/auth/totp/enrollments", nil, secretOK).json(t, &enr)
	c.e.secrets.Register(canary.TOTPSeed, "totp seed", enr.Secret)
	code, err := totp.Code(enr.Secret, c.e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/totp/enrollments/verifications", map[string]string{"code": code}).session(t)
	return enr.Secret, s
}

func (e *env) totpCode(secret string) string {
	e.t.Helper()
	code, err := totp.Code(secret, e.clk.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return code
}

// device is a software passkey authenticator for the public origin.
type device struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newDevice(origin, rpID string) *device {
	return &device{
		rp:   virtualwebauthn.RelyingParty{ID: rpID, Name: "DockYard", Origin: origin},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{BackupEligible: true}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// register runs the registration ceremony through the API.
func (c *client) registerPasskey(d *device, name string) response {
	t := c.e.t
	t.Helper()
	opts := c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/passkeys/registration-options", nil)
	ao, err := virtualwebauthn.ParseAttestationOptions(string(opts.body))
	if err != nil {
		t.Fatal(err)
	}
	d.auth.Options.UserHandle = []byte(ao.UserID)
	att := virtualwebauthn.CreateAttestationResponse(d.rp, d.auth, d.cred, *ao)
	return c.do(http.MethodPost, "/api/v1/auth/passkeys/registration-verifications",
		map[string]any{"name": name, "credential": json.RawMessage(att)})
}

// assertPasskey runs an assertion ceremony (purpose sign_in or step_up)
// and returns the verification response.
func (c *client) assertPasskey(d *device, purpose, verifyPath string) response {
	t := c.e.t
	t.Helper()
	opts := c.must(http.StatusOK, http.MethodPost, "/api/v1/auth/passkeys/authentication-options", map[string]string{"purpose": purpose})
	ao, err := virtualwebauthn.ParseAssertionOptions(string(opts.body))
	if err != nil {
		t.Fatal(err)
	}
	d.cred.Counter++
	as := virtualwebauthn.CreateAssertionResponse(d.rp, d.auth, d.cred, *ao)
	return c.do(http.MethodPost, verifyPath, map[string]any{"credential": json.RawMessage(as)})
}
