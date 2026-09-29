package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store/storetest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func TestStoreRoundTripAndExpiry(t *testing.T) {
	db := storetest.Migrated(t)
	clk := testutil.FakeClock()
	s := NewStore(db, clk)
	ctx := testutil.Context(t)

	if _, found, err := s.FindCtx(ctx, "missing"); found || err != nil {
		t.Fatalf("missing token: found=%v err=%v", found, err)
	}
	if err := s.CommitCtx(ctx, "t1", []byte("one"), clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCtx(ctx, "t2", []byte("two"), clk.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Commit replaces data and expiry of an existing token.
	if err := s.CommitCtx(ctx, "t1", []byte("one-v2"), clk.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	b, found, err := s.FindCtx(ctx, "t1")
	if err != nil || !found || string(b) != "one-v2" {
		t.Fatalf("find t1 = %q %v %v", b, found, err)
	}
	all, err := s.AllCtx(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v %v", all, err)
	}

	clk.Advance(10 * time.Minute) // t2 expires exactly now: dead
	if _, found, _ := s.FindCtx(ctx, "t2"); found {
		t.Fatal("expired session found")
	}
	if all, _ := s.AllCtx(ctx); len(all) != 1 || string(all["t1"]) != "one-v2" {
		t.Fatalf("all after expiry = %v", all)
	}
	n, err := s.DeleteExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("delete expired = %d %v", n, err)
	}
	if err := s.DeleteCtx(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCtx(ctx, "t1"); err != nil { // idempotent
		t.Fatal(err)
	}
	if all, _ := s.All(); len(all) != 0 {
		t.Fatalf("all after delete = %v", all)
	}
	// The context-free methods required by scs.Store behave the same.
	if err := s.Commit("t3", []byte("x"), clk.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Find("t3"); !found {
		t.Fatal("Find t3")
	}
	if err := s.Delete("t3"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReportsDatabaseErrors(t *testing.T) {
	db := storetest.Migrated(t)
	s := NewStore(db, testutil.FakeClock())
	_ = db.Close()
	if _, found, err := s.FindCtx(context.Background(), "x"); err == nil || found {
		t.Fatalf("closed db: found=%v err=%v (a failure must not look like an anonymous session)", found, err)
	}
}

type fixture struct {
	sm    *scs.SessionManager
	store *Store
	h     http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.Migrated(t)
	st := NewStore(db, nil) // wall clock: SCS computes expiries from time.Now
	sm, err := NewManager(Options{Store: st, ErrorFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
		t.Errorf("session error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if err := sm.RenewToken(r.Context()); err != nil {
			t.Error(err)
		}
		sm.Put(r.Context(), "user", r.URL.Query().Get("u"))
		sm.RememberMe(r.Context(), r.URL.Query().Get("stay") == "1")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sm.GetString(r.Context(), "user")))
	})
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		if err := sm.Destroy(r.Context()); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return &fixture{sm: sm, store: st, h: sm.LoadAndSave(mux)}
}

func (f *fixture) do(t *testing.T, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", CookieName, rec.Header())
	return nil
}

// TestCookiePolicyAndHashedTokens: the cookie carries Docker Manager's attributes
// and the database holds only token hashes.
func TestCookiePolicyAndHashedTokens(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, http.MethodPost, "/login?u=alice&stay=1", nil)
	c := sessionCookie(t, rec)
	raw := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"__Host-docker_manager_session=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict", "Max-Age="} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie %q lacks %q", raw, want)
		}
	}
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Errorf("Set-Cookie %q has a Domain", raw)
	}
	// Without "Stay signed in" the cookie ends with the browser.
	raw = f.do(t, http.MethodPost, "/login?u=bob", nil).Header().Get("Set-Cookie")
	if strings.Contains(raw, "Max-Age=") || strings.Contains(raw, "Expires=") {
		t.Errorf("session cookie %q persists", raw)
	}
	all, err := f.store.All()
	if err != nil || len(all) != 2 {
		t.Fatalf("stored sessions %v %v", all, err)
	}
	for token := range all {
		if token == c.Value {
			t.Fatal("the raw cookie value is stored; want its hash")
		}
	}
	if got := f.do(t, http.MethodGet, "/me", c).Body.String(); got != "alice" {
		t.Fatalf("session data = %q", got)
	}
}

// TestRenewalPreventsFixation: a token planted before sign-in is never
// adopted, and renewing deletes the previous token.
func TestRenewalPreventsFixation(t *testing.T) {
	f := newFixture(t)
	planted := &http.Cookie{Name: CookieName, Value: "attacker-chosen-token-value-000000000000000"}
	rec := f.do(t, http.MethodPost, "/login?u=alice", planted)
	c := sessionCookie(t, rec)
	if c.Value == planted.Value {
		t.Fatal("planted session token was adopted")
	}
	if got := f.do(t, http.MethodGet, "/me", planted).Body.String(); got != "" {
		t.Fatalf("planted token authenticates as %q", got)
	}
	// Signing in again (privilege change) rotates the token; the old one dies.
	rec = f.do(t, http.MethodPost, "/login?u=alice", c)
	c2 := sessionCookie(t, rec)
	if c2.Value == c.Value {
		t.Fatal("token not renewed")
	}
	if got := f.do(t, http.MethodGet, "/me", c).Body.String(); got != "" {
		t.Fatalf("pre-renewal token still valid (%q)", got)
	}
	if got := f.do(t, http.MethodGet, "/me", c2).Body.String(); got != "alice" {
		t.Fatalf("renewed token = %q", got)
	}
}

// TestDestroyAndIterate: sign-out deletes the row and expires the cookie;
// Iterate (IterableStore) can revoke sessions by content.
func TestDestroyAndIterate(t *testing.T) {
	f := newFixture(t)
	alice := sessionCookie(t, f.do(t, http.MethodPost, "/login?u=alice", nil))
	bob := sessionCookie(t, f.do(t, http.MethodPost, "/login?u=bob", nil))
	alice2 := sessionCookie(t, f.do(t, http.MethodPost, "/login?u=alice", nil))

	ctx := testutil.Context(t)
	n, err := RevokeWhere(ctx, f.sm, f.store, func(ctx context.Context) bool {
		return f.sm.GetString(ctx, "user") == "alice"
	})
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	for _, c := range []*http.Cookie{alice, alice2} {
		if got := f.do(t, http.MethodGet, "/me", c).Body.String(); got != "" {
			t.Fatalf("revoked session still valid: %q", got)
		}
	}
	if got := f.do(t, http.MethodGet, "/me", bob).Body.String(); got != "bob" {
		t.Fatalf("unrelated session revoked: %q", got)
	}
	rec := f.do(t, http.MethodPost, "/logout", bob)
	if c := sessionCookie(t, rec); c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout cookie %+v", c)
	}
	if got := f.do(t, http.MethodGet, "/me", bob).Body.String(); got != "" {
		t.Fatalf("session valid after logout: %q", got)
	}
}

func TestNewManagerValidates(t *testing.T) {
	st := NewStore(storetest.Migrated(t), nil)
	ef := func(http.ResponseWriter, *http.Request, error) {}
	for name, o := range map[string]Options{
		"no store":       {ErrorFunc: ef},
		"no error func":  {Store: st},
		"idle > max age": {Store: st, ErrorFunc: ef, IdleTimeout: 2 * time.Hour, Lifetime: time.Hour},
	} {
		if _, err := NewManager(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	sm, err := NewManager(Options{Store: st, ErrorFunc: ef})
	if err != nil || sm.IdleTimeout != DefaultIdleTimeout || sm.Lifetime != DefaultLifetime || !sm.HashTokenInStore {
		t.Fatalf("defaults: %+v %v", sm, err)
	}
	if sm, err := NewManager(Options{Store: st, ErrorFunc: ef, IdleTimeout: -1}); err != nil || sm.IdleTimeout != 0 {
		t.Fatalf("disabled idle: %+v %v", sm, err)
	}
}
