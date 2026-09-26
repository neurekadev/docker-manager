package totp

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// RFC 6238 appendix B, SHA-1 test vectors (8 digits there; the last six
// digits are the 6-digit code).
func TestRFC6238Vectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037"}
	for unix, want := range vectors {
		got, err := Code(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("T=%d: %s %v, want %s", unix, got, err, want)
		}
		if step, ok, err := Verify(secret, want, time.Unix(unix, 0), 0); !ok || err != nil || step != Step(time.Unix(unix, 0)) {
			t.Errorf("verify T=%d: step=%d ok=%v err=%v", unix, step, ok, err)
		}
	}
}

func TestGenerate(t *testing.T) {
	k, err := Generate("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Secret) != 32 { // 20 bytes base32 without padding
		t.Fatalf("secret %q", k.Secret)
	}
	u, err := url.Parse(k.URI)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Docker Manager:alice" {
		t.Fatalf("uri %q", k.URI)
	}
	q := u.Query()
	if q.Get("secret") != k.Secret || q.Get("issuer") != "Docker Manager" || q.Get("digits") != "6" || q.Get("period") != "30" || q.Get("algorithm") != "SHA1" {
		t.Fatalf("uri query %v", q)
	}
	k2, _ := Generate("alice")
	if k2.Secret == k.Secret {
		t.Fatal("secrets repeat")
	}
}

// TestSkewWindowAndReplay: codes from one step before/after are accepted,
// two steps are not, and an accepted step (or any earlier one) is never
// accepted again.
func TestSkewWindowAndReplay(t *testing.T) {
	k, _ := Generate("bob")
	clk := testutil.FakeClock()
	now := clk.Now()
	code := func(t0 time.Time) string {
		c, err := Code(k.Secret, t0)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, d := range []time.Duration{-Period, 0, Period} {
		if _, ok, _ := Verify(k.Secret, code(now.Add(d)), now, 0); !ok {
			t.Errorf("offset %v rejected", d)
		}
	}
	for _, d := range []time.Duration{-2 * Period, 2 * Period, 10 * Period} {
		if _, ok, _ := Verify(k.Secret, code(now.Add(d)), now, 0); ok {
			t.Errorf("offset %v accepted", d)
		}
	}

	c := code(now)
	step, ok, err := Verify(k.Secret, c, now, 0)
	if !ok || err != nil {
		t.Fatal("first use rejected")
	}
	// Replay within the same step and within the skew window afterwards.
	if _, ok, _ := Verify(k.Secret, c, now, step); ok {
		t.Fatal("replayed code accepted")
	}
	clk.Advance(Period)
	if _, ok, _ := Verify(k.Secret, c, clk.Now(), step); ok {
		t.Fatal("replayed code accepted one step later")
	}
	// An older, never-used code from before the last accepted step is also dead.
	if _, ok, _ := Verify(k.Secret, code(now.Add(-Period)), clk.Now(), step); ok {
		t.Fatal("code older than the last accepted step accepted")
	}
	// The next code works.
	if s2, ok, _ := Verify(k.Secret, code(clk.Now()), clk.Now(), step); !ok || s2 <= step {
		t.Fatalf("next code: ok=%v step %d after %d", ok, s2, step)
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{"123456": "123456", " 123 456 ": "123456", "123-456": "123456"} {
		if got, err := NormalizeCode(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "12345a", "１２３４５６", strings.Repeat("1", 100)} {
		if _, err := NormalizeCode(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q accepted", bad)
		}
		if _, ok, err := Verify("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", bad, time.Unix(59, 0), 0); ok || err == nil {
			t.Errorf("verify %q: ok=%v err=%v", bad, ok, err)
		}
	}
}
