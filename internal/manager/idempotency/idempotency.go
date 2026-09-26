// Package idempotency is the durable api.IdempotencyStore: reservations and
// replayable responses of Idempotency-Key requests to non-job operations
// (#4), kept in SQLite with response headers and bodies sealed by the
// secret-protection keyring (responses can carry one-time secrets such as a
// new API token). Job-starting operations use the job engine's idempotency
// instead (#26).
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Store implements api.IdempotencyStore.
type Store struct {
	db      *bun.DB
	keyring *secrets.Keyring
	clock   clock.Clock
	ttl     time.Duration
	lease   time.Duration
}

// Options configures a Store. TTL and Lease default to
// domain.IdempotencyTTL and domain.IdempotencyLease.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	TTL     time.Duration
	Lease   time.Duration
}

// New returns a Store.
func New(o Options) (*Store, error) {
	if o.DB == nil || o.Keyring == nil {
		return nil, errors.New("idempotency: DB and Keyring are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.TTL <= 0 {
		o.TTL = domain.IdempotencyTTL
	}
	if o.Lease <= 0 {
		o.Lease = domain.IdempotencyLease
	}
	return &Store{db: o.DB, keyring: o.Keyring, clock: o.Clock, ttl: o.TTL, lease: o.Lease}, nil
}

type sealedResponse struct {
	Header map[string][]string `json:"header"`
	Body   []byte              `json:"body"`
}

// sealContext binds a sealed response to its row, so a sealed value copied
// to another row does not open.
func sealContext(r domain.IdempotencyReservation) string {
	sum := sha256.Sum256([]byte(r.Scope + "\x00" + r.Key + "\x00" + r.RequestHash))
	return "idempotency_keys/" + hex.EncodeToString(sum[:]) + "/response"
}

// Begin implements api.IdempotencyStore.
func (s *Store) Begin(ctx context.Context, r domain.IdempotencyReservation) (*domain.IdempotentResponse, error) {
	var stored *store.StoredIdempotentResponse
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		stored, err = store.BeginIdempotency(ctx, tx, r, s.clock.Now(), s.lease)
		return err
	})
	if err != nil || stored == nil {
		return nil, err
	}
	pt, err := s.keyring.Open(stored.Sealed, sealContext(r))
	if err != nil {
		return nil, fmt.Errorf("idempotency: open stored response: %w", err)
	}
	var sr sealedResponse
	if err := json.Unmarshal(pt, &sr); err != nil {
		return nil, fmt.Errorf("idempotency: decode stored response: %w", err)
	}
	return &domain.IdempotentResponse{Status: stored.Status, Header: sr.Header, Body: sr.Body}, nil
}

// Complete implements api.IdempotencyStore.
func (s *Store) Complete(ctx context.Context, r domain.IdempotencyReservation, resp domain.IdempotentResponse) error {
	pt, err := json.Marshal(sealedResponse{Header: resp.Header, Body: resp.Body})
	if err != nil {
		return fmt.Errorf("idempotency: encode response: %w", err)
	}
	sealed, err := s.keyring.Seal(pt, sealContext(r))
	if err != nil {
		return err
	}
	return store.CompleteIdempotency(ctx, s.db, r, resp.Status, sealed, s.clock.Now(), s.ttl)
}

// Release implements api.IdempotencyStore.
func (s *Store) Release(ctx context.Context, r domain.IdempotencyReservation) error {
	return store.ReleaseIdempotency(ctx, s.db, r)
}

// Forget drops every stored response of a principal (authz.Principal.Key()).
// Authentication (#16/#31) and authorization (#17) call it when the
// principal's sessions, token or permissions change.
func (s *Store) Forget(ctx context.Context, principalKey string) (int, error) {
	return store.ForgetIdempotencyKeys(ctx, s.db, principalKey)
}
