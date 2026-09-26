package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Idempotency-Key reservations and stored responses of non-job operations
// (#4). internal/manager/idempotency seals response bodies before they reach
// these functions.

type idempotencyRow struct {
	bun.BaseModel `bun:"table:idempotency_keys"`

	Scope       string    `bun:"scope,pk"`
	Key         string    `bun:"key,pk"`
	RequestHash string    `bun:"request_hash,notnull"`
	State       string    `bun:"state,notnull"`
	Status      int       `bun:"status,notnull"`
	Response    string    `bun:"response,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	ExpiresAt   time.Time `bun:"expires_at,notnull"`
}

const (
	idemPending   = "pending"
	idemCompleted = "completed"
)

// StoredIdempotentResponse is a completed reservation's status and sealed
// response.
type StoredIdempotentResponse struct {
	Status int
	Sealed string
}

// BeginIdempotency reserves r until now+lease, or returns the stored
// response of a completed reservation with the same request hash. Expired
// rows are deleted first. Run it inside a transaction.
func BeginIdempotency(ctx context.Context, db bun.IDB, r domain.IdempotencyReservation, now time.Time, lease time.Duration) (*StoredIdempotentResponse, error) {
	now = now.UTC()
	if _, err := db.NewDelete().Model((*idempotencyRow)(nil)).Where("expires_at <= ?", now).Exec(ctx); err != nil {
		return nil, fmt.Errorf("store: expire idempotency keys: %w", err)
	}
	var row idempotencyRow
	err := db.NewSelect().Model(&row).Where("scope = ? AND key = ?", r.Scope, r.Key).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		row = idempotencyRow{Scope: r.Scope, Key: r.Key, RequestHash: r.RequestHash, State: idemPending,
			CreatedAt: now, ExpiresAt: now.Add(lease)}
		if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
			return nil, fmt.Errorf("store: reserve idempotency key: %w", err)
		}
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("store: read idempotency key: %w", err)
	case row.RequestHash != r.RequestHash:
		return nil, domain.ErrIdempotencyMismatch
	case row.State != idemCompleted:
		return nil, domain.ErrIdempotencyInFlight
	}
	return &StoredIdempotentResponse{Status: row.Status, Sealed: row.Response}, nil
}

// CompleteIdempotency stores the response of r's pending reservation and
// keeps it until now+ttl.
func CompleteIdempotency(ctx context.Context, db bun.IDB, r domain.IdempotencyReservation, status int, sealed string, now time.Time, ttl time.Duration) error {
	res, err := db.NewUpdate().Model((*idempotencyRow)(nil)).
		Set("state = ?", idemCompleted).Set("status = ?", status).Set("response = ?", sealed).
		Set("expires_at = ?", now.UTC().Add(ttl)).
		Where("scope = ? AND key = ? AND request_hash = ? AND state = ?", r.Scope, r.Key, r.RequestHash, idemPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: complete idempotency key: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("store: complete idempotency key: reservation lost (lease expired?)")
	}
	return nil
}

// ReleaseIdempotency drops r's pending reservation.
func ReleaseIdempotency(ctx context.Context, db bun.IDB, r domain.IdempotencyReservation) error {
	_, err := db.NewDelete().Model((*idempotencyRow)(nil)).
		Where("scope = ? AND key = ? AND request_hash = ? AND state = ?", r.Scope, r.Key, r.RequestHash, idemPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: release idempotency key: %w", err)
	}
	return nil
}

// ForgetIdempotencyKeys deletes every reservation and stored response of a
// principal (scope prefix "<principal key> "). Call it when the principal's
// sessions, token or permissions change (#16, #17, #31) so a replay never
// outlives the access that produced it.
func ForgetIdempotencyKeys(ctx context.Context, db bun.IDB, principalKey string) (int, error) {
	if principalKey == "" {
		return 0, errors.New("store: forget idempotency keys: empty principal")
	}
	res, err := db.NewDelete().Model((*idempotencyRow)(nil)).
		Where("substr(scope, 1, ?) = ?", len(principalKey)+1, principalKey+" ").Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: forget idempotency keys: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
