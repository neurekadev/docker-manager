package idempotency

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type fixture struct {
	ctx context.Context
	db  *bun.DB
	clk *clock.Fake
	s   *Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clk := testutil.FakeClock()
	s, err := New(Options{DB: db, Keyring: secrets.NewKeyring(key), Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{ctx: ctx, db: db, clk: clk, s: s}
}

func res(scope, key, hash string) domain.IdempotencyReservation {
	return domain.IdempotencyReservation{Scope: scope, Key: key, RequestHash: hash}
}

func TestReserveCompleteReplay(t *testing.T) {
	f := newFixture(t)
	r := res("user:alice create-api-token", "k1", "h1")
	if got, err := f.s.Begin(f.ctx, r); got != nil || err != nil {
		t.Fatalf("first Begin = %v, %v", got, err)
	}
	if _, err := f.s.Begin(f.ctx, r); !errors.Is(err, domain.ErrIdempotencyInFlight) {
		t.Fatalf("second Begin = %v, want in flight", err)
	}
	if _, err := f.s.Begin(f.ctx, res(r.Scope, r.Key, "h2")); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("different hash = %v, want mismatch", err)
	}
	resp := domain.IdempotentResponse{Status: 201, Header: map[string][]string{"Location": {"/api/v1/me/api-tokens/1"}}, Body: []byte(`{"secret":"dy_tok_canary"}`)}
	if err := f.s.Complete(f.ctx, r, resp); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Begin(f.ctx, r)
	if err != nil || got == nil || got.Status != 201 || string(got.Body) != string(resp.Body) || got.Header["Location"][0] != "/api/v1/me/api-tokens/1" {
		t.Fatalf("replay = %+v, %v", got, err)
	}
	// The response is sealed at rest.
	var raw string
	if err := f.db.QueryRowContext(f.ctx, `SELECT response FROM idempotency_keys WHERE key = 'k1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "canary") || strings.Contains(raw, "api-tokens") {
		t.Fatalf("stored response is not sealed: %s", raw)
	}
	// A sealed response moved to another row does not open.
	if _, err := f.db.ExecContext(f.ctx, `INSERT INTO idempotency_keys SELECT scope, 'k2', request_hash, state, status, response, created_at, expires_at FROM idempotency_keys WHERE key = 'k1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Begin(f.ctx, res(r.Scope, "k2", "h1")); err == nil {
		t.Fatal("copied sealed response opened under another key")
	}
}

func TestLeaseTTLReleaseAndForget(t *testing.T) {
	f := newFixture(t)
	r := res("user:alice op", "k", "h")
	if _, err := f.s.Begin(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	// A reservation abandoned by a crashed request frees the key after the lease.
	f.clk.Advance(domain.IdempotencyLease)
	if got, err := f.s.Begin(f.ctx, r); got != nil || err != nil {
		t.Fatalf("after lease = %v, %v", got, err)
	}
	if err := f.s.Complete(f.ctx, r, domain.IdempotentResponse{Status: 200, Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(domain.IdempotencyTTL - time.Second)
	if got, err := f.s.Begin(f.ctx, r); got == nil || err != nil {
		t.Fatalf("before TTL = %v, %v", got, err)
	}
	f.clk.Advance(time.Second)
	if got, err := f.s.Begin(f.ctx, r); got != nil || err != nil {
		t.Fatalf("after TTL = %v, %v (want a fresh reservation)", got, err)
	}
	// Release frees the key; completing a released reservation fails.
	if err := f.s.Release(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Complete(f.ctx, r, domain.IdempotentResponse{Status: 200}); err == nil {
		t.Fatal("completed a released reservation")
	}
	// Forget removes one principal's keys only.
	for _, sc := range []string{"user:alice a", "user:alice b", "user:alicex a", "token:t1 a"} {
		if _, err := f.s.Begin(f.ctx, res(sc, "k", "h")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := f.s.Forget(f.ctx, "user:alice")
	if err != nil || n != 2 {
		t.Fatalf("Forget = %d, %v", n, err)
	}
	if _, err := f.s.Begin(f.ctx, res("user:alicex a", "k", "h")); !errors.Is(err, domain.ErrIdempotencyInFlight) {
		t.Fatalf("Forget removed another principal's key: %v", err)
	}
	if _, err := f.s.Forget(f.ctx, ""); err == nil {
		t.Fatal("Forget with empty principal")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without DB")
	}
}

// TestAPIWithDurableStore runs an IdempotencyStored operation against the
// SQLite store: a retried request replays the stored 201 without running
// the handler again.
func TestAPIWithDurableStore(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	mux := http.NewServeMux()
	a := api.New(mux, api.Deps{Idempotency: f.s})
	type in struct {
		api.IdempotencyKeyParam
		Body struct {
			Name string `json:"name"`
		}
	}
	type out struct {
		Body struct {
			ID string `json:"id"`
		}
	}
	api.Register(a, api.Operation{
		Operation:  huma.Operation{OperationID: "create-thing", Method: http.MethodPost, Path: api.BasePath + "/things", Summary: "create", DefaultStatus: 201},
		Capability: "thing.create", Scope: api.ScopeInstance, Idempotency: api.IdempotencyStored,
	}, func(context.Context, *in) (*out, error) {
		o := &out{}
		o.Body.ID = fmt.Sprint(calls.Add(1))
		return o, nil
	})
	logger := testutil.Logger(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logging.IntoContext(r.Context(), logger)
		ctx, _ = authz.WithPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: "alice"})
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, api.BasePath+"/things", strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "retry-1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first, second := post(), post()
	if first.Code != 201 || second.Code != 201 || first.Body.String() != second.Body.String() ||
		second.Header().Get(api.HeaderIdempotentReplayed) != "true" || calls.Load() != 1 {
		t.Fatalf("first %d %s; second %d %v %s; calls %d", first.Code, first.Body, second.Code, second.Header(), second.Body, calls.Load())
	}
}
