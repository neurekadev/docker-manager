// Package password hashes and checks passwords (#16, #18).
//
// Hashing uses alexedwards/argon2id (Argon2id from golang.org/x/crypto,
// PHC-encoded with salt and parameters); Docker Manager never implements KDF
// primitives. Parameters are versioned: every encoded hash carries the
// parameters it was made with, Verify reports when they differ from the
// current set, and callers re-hash after a successful sign-in
// (upgrade-on-login). Passwords are NFKC-normalized before hashing and
// checking (NIST SP 800-63B), so the same passphrase typed on different
// keyboards verifies.
package password

import (
	"context"
	"crypto/rand"
	"fmt"
	"runtime"

	"github.com/alexedwards/argon2id"
	"golang.org/x/text/unicode/norm"
)

// Params are Argon2id parameters.
type Params = argon2id.Params

// ParamsV1 is the current parameter set: RFC 9106's memory-constrained
// profile scaled for small Docker hosts (Raspberry Pi class arm64 as well as
// servers): 64 MiB, 3 passes, 2 lanes, 16-byte salt, 32-byte key. About
// 100-250 ms per hash on the supported hosts; sign-in throttling bounds
// how often an attacker can make the manager compute it.
//
// Changing it: add ParamsV2, make it Current, keep verification of older
// hashes (they carry their own parameters) and let upgrade-on-login move
// accounts over. Never edit an existing set.
var ParamsV1 = &Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

// Current is the parameter set new hashes use.
var Current = ParamsV1

// ErrBusy is returned when the context ends while waiting for a hashing
// slot. The API answers it with a retryable 503 (it implements Busy()).
var ErrBusy error = busyError{}

type busyError struct{}

func (busyError) Error() string { return "password: hashing capacity exhausted" }

// Busy marks the error as a temporary capacity problem.
func (busyError) Busy() bool { return true }

// Hasher hashes and verifies passwords with bounded concurrency (each
// Argon2id computation holds Params.Memory; unbounded parallel sign-ins
// could exhaust a small host's memory).
type Hasher struct {
	params *Params
	slots  chan struct{}
	// dummy is a valid hash of a random value, verified when the account
	// does not exist or has no password so both paths cost one Argon2id
	// computation (enumeration resistance).
	dummy string
	// computed counts Argon2id computations (tests: timing class).
	computed func()
}

// Options configures a Hasher.
type Options struct {
	// Params defaults to Current. Tests pass cheap parameters.
	Params *Params
	// MaxConcurrent bounds simultaneous computations (default: GOMAXPROCS,
	// at most 4).
	MaxConcurrent int
	// OnCompute is called for every Argon2id computation (tests only).
	OnCompute func()
}

// NewHasher returns a Hasher.
func NewHasher(o Options) (*Hasher, error) {
	if o.Params == nil {
		o.Params = Current
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = min(runtime.GOMAXPROCS(0), 4)
	}
	h := &Hasher{params: o.Params, slots: make(chan struct{}, o.MaxConcurrent), computed: o.OnCompute}
	if h.computed == nil {
		h.computed = func() {}
	}
	dummy, err := argon2id.CreateHash(randomString(), h.params)
	if err != nil {
		return nil, fmt.Errorf("password: create dummy hash: %w", err)
	}
	h.dummy = dummy
	return h, nil
}

// Normalize returns the NFKC form Docker Manager hashes and checks.
func Normalize(pw string) string { return norm.NFKC.String(pw) }

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrBusy
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns the PHC-encoded Argon2id hash of pw.
func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	h.computed()
	return argon2id.CreateHash(Normalize(pw), h.params)
}

// Verify checks pw against encoded. An empty encoded (unknown account or
// no password set) is compared against a dummy hash, so it costs the same
// and returns ok=false. needsRehash reports that encoded was made with
// parameters other than the current ones; after a successful sign-in the
// caller stores Hash(pw) (upgrade-on-login).
func (h *Hasher) Verify(ctx context.Context, pw, encoded string) (ok, needsRehash bool, err error) {
	target := encoded
	if target == "" {
		target = h.dummy
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	h.computed()
	match, params, err := argon2id.CheckHash(Normalize(pw), target)
	if err != nil {
		return false, false, fmt.Errorf("password: verify: %w", err)
	}
	if encoded == "" {
		return false, false, nil
	}
	return match, match && !sameParams(params, h.params), nil
}

func sameParams(a, b *Params) bool {
	return a.Memory == b.Memory && a.Iterations == b.Iterations && a.Parallelism == b.Parallelism &&
		a.SaltLength == b.SaltLength && a.KeyLength == b.KeyLength
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand never fails (Go 1.24+)
	return string(b)
}
