package password

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alexedwards/argon2id"
)

// cheap are fast test parameters (production uses Current).
var cheap = &Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestCurrentParamsAreVersionedAndStrong(t *testing.T) {
	if Current != ParamsV1 {
		t.Fatal("Current must name a versioned parameter set")
	}
	// RFC 9106 / OWASP floor for Argon2id.
	if ParamsV1.Memory < 19*1024 || ParamsV1.Iterations < 2 || ParamsV1.SaltLength < 16 || ParamsV1.KeyLength < 32 {
		t.Fatalf("ParamsV1 below the Argon2id floor: %+v", ParamsV1)
	}
}

func TestHashVerifyAndUpgradeOnLogin(t *testing.T) {
	ctx := context.Background()
	old, err := NewHasher(Options{Params: cheap})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := old.Hash(ctx, "correct horse battery staple")
	if err != nil || !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash %q %v", enc, err)
	}
	ok, rehash, err := old.Verify(ctx, "correct horse battery staple", enc)
	if err != nil || !ok || rehash {
		t.Fatalf("verify same params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := old.Verify(ctx, "correct horse battery stapl3", enc); ok {
		t.Fatal("wrong password verified")
	}

	// The instance moves to new parameters: old hashes still verify and ask
	// for an upgrade, and only after a successful verification.
	upgraded := &Params{Memory: 128, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	cur, err := NewHasher(Options{Params: upgraded})
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err = cur.Verify(ctx, "correct horse battery staple", enc)
	if err != nil || !ok || !rehash {
		t.Fatalf("old hash under new params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, rehash, _ := cur.Verify(ctx, "nope nope nope nope", enc); ok || rehash {
		t.Fatal("failed verification must not ask for a rehash")
	}
	newEnc, err := cur.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	p, _, _, err := argon2id.DecodeHash(newEnc)
	if err != nil || p.Memory != 128 || p.Iterations != 2 {
		t.Fatalf("upgraded params %+v %v", p, err)
	}
	if ok, rehash, _ := cur.Verify(ctx, "correct horse battery staple", newEnc); !ok || rehash {
		t.Fatal("upgraded hash does not verify cleanly")
	}
}

func TestNormalization(t *testing.T) {
	h, _ := NewHasher(Options{Params: cheap})
	ctx := context.Background()
	// "ﬁ" (U+FB01 ligature) and fullwidth digits NFKC-normalize to ASCII.
	enc, err := h.Hash(ctx, "ﬁle cabinet １２３ blue")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := h.Verify(ctx, "file cabinet 123 blue", enc); !ok {
		t.Fatal("NFKC-equivalent password did not verify")
	}
}

// TestUnknownAccountCostsOneComputation: verifying against a missing hash
// performs the same single Argon2id computation (enumeration resistance)
// and never succeeds.
func TestUnknownAccountCostsOneComputation(t *testing.T) {
	var n atomic.Int32
	h, err := NewHasher(Options{Params: cheap, OnCompute: func() { n.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	enc, _ := h.Hash(ctx, "a long enough passphrase")
	n.Store(0)
	if ok, _, err := h.Verify(ctx, "a long enough passphrase", ""); ok || err != nil {
		t.Fatalf("empty hash: ok=%v err=%v", ok, err)
	}
	unknown := n.Load()
	n.Store(0)
	if _, _, err := h.Verify(ctx, "wrong", enc); err != nil {
		t.Fatal(err)
	}
	if unknown != 1 || n.Load() != 1 {
		t.Fatalf("computations: unknown account %d, known account %d; want 1 each", unknown, n.Load())
	}
}

func TestConcurrencyBound(t *testing.T) {
	h, _ := NewHasher(Options{Params: cheap, MaxConcurrent: 1})
	h.slots <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "whatever whatever"); !errors.Is(err, ErrBusy) {
		t.Fatalf("hash with no slot and a dead context: %v", err)
	}
	if _, _, err := h.Verify(ctx, "x", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("verify with no slot: %v", err)
	}
	<-h.slots
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := h.Hash(context.Background(), "parallel passphrase"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestPolicy(t *testing.T) {
	if BlocklistSize() < 30000 {
		t.Fatalf("blocklist has %d entries", BlocklistSize())
	}
	base := Policy{}
	strict := Policy{Strict: true, MinLength: DefaultStrictMinLength}
	cases := []struct {
		pw     string
		policy Policy
		ctx    []string
		want   []string
	}{
		{"short", base, nil, []string{ViolationTooShort}},
		{"password1", base, nil, []string{ViolationCommon}},
		{"PASSWORD123", base, nil, []string{ViolationCommon}}, // case-insensitive
		{"iloveyou2", base, nil, []string{ViolationCommon}},
		{"aaaaaaaaaa", base, nil, []string{ViolationCommon, ViolationRepeated}},
		{"ttttttttttttttttttttttt", base, nil, []string{ViolationRepeated}},
		{"123456789012", base, nil, []string{ViolationCommon}},
		{"abcdefghijklmnopqrstu", base, nil, []string{ViolationRepeated}},
		{"alice.smith!", base, []string{"alice.smith@example.com"}, []string{ViolationContext}},
		{"docker-manager2026", base, nil, []string{ViolationContext}},
		{"tangerine kettle", base, nil, nil},
		{"tangerine pot", strict, nil, []string{ViolationTooShort}},
		{"tangerine kettle orbit", strict, nil, nil},
		// A long passphrase that merely mentions the username is fine.
		{"alice rides a green bicycle home", strict, []string{"alice"}, nil},
		{strings.Repeat("xy", MaxLength), base, nil, []string{ViolationTooLong}},
	}
	for _, c := range cases {
		var got []string
		for _, v := range c.policy.Check(c.pw, c.ctx...) {
			got = append(got, v.Code)
			if v.Message == "" {
				t.Errorf("%q: violation %s without message", c.pw, v.Code)
			}
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("Check(%q, strict=%v) = %v, want %v", c.pw, c.policy.Strict, got, c.want)
		}
	}
	if (Policy{Strict: true, MinLength: 3}).EffectiveMinLength() != FloorMinLength ||
		(Policy{Strict: true, MinLength: 500}).EffectiveMinLength() != MaxMinLength ||
		(Policy{MinLength: 30}).EffectiveMinLength() != FloorMinLength {
		t.Fatal("EffectiveMinLength bounds")
	}
}
