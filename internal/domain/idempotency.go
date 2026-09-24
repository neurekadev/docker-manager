package domain

import (
	"errors"
	"time"
)

// Stored idempotent responses (#4). Job-starting operations use the job
// engine's own idempotency (#26); this covers dangerous non-job operations.

// IdempotencyTTL is how long a completed response is replayable.
const IdempotencyTTL = 24 * time.Hour

// IdempotencyLease is how long an unfinished reservation blocks its key.
// After it expires (the manager died mid-request) the key may be used again.
const IdempotencyLease = 5 * time.Minute

// IdempotencyReservation identifies one keyed request.
type IdempotencyReservation struct {
	// Scope is the principal key plus the operation ID; keys never collide
	// across callers or operations.
	Scope string
	// Key is the client's Idempotency-Key.
	Key string
	// RequestHash fingerprints method, path, query, content type and body.
	RequestHash string
}

// IdempotentResponse is a stored response replayed for a repeated request.
type IdempotentResponse struct {
	Status int
	// Header holds only replay-safe headers (Content-Type, Location, ETag).
	Header map[string][]string
	Body   []byte
}

// Idempotency store errors.
var (
	// ErrIdempotencyMismatch: the key was used for a different request.
	ErrIdempotencyMismatch = errors.New("idempotency key reused for a different request")
	// ErrIdempotencyInFlight: the first request with the key is still running.
	ErrIdempotencyInFlight = errors.New("idempotency key in flight")
)
