// Package totp wraps pquerna/otp for DockYard's RFC 6238 second factor
// (#16, #18). The library owns secret generation, the otpauth:// URI and
// the HOTP computation with constant-time comparison; DockYard owns the
// clock-skew window, replay prevention (the time step of the last accepted
// code is stored and only later steps are accepted), attempt throttling and
// encrypted seed storage (secrets.Keyring).
package totp

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// Parameters every authenticator app understands: 30-second steps, six
// digits, HMAC-SHA1 (RFC 6238 defaults) and a 160-bit secret.
const (
	Period     = 30 * time.Second
	Digits     = 6
	SecretSize = 20
	// Skew is the number of steps accepted before and after the current one
	// (±30 s of clock drift between the phone and the manager).
	Skew = 1
)

// Issuer labels DockYard entries in authenticator apps.
const Issuer = "DockYard"

// ErrMalformed is returned for codes that are not six digits.
var ErrMalformed = errors.New("totp: code must be 6 digits")

// Key is a freshly generated TOTP secret. Secret and URI are shown to the
// user exactly once during enrollment and must never be logged.
type Key struct {
	// Secret is the base32 secret (manual entry).
	Secret string
	// URI is the otpauth:// URI (QR code).
	URI string
}

// Generate creates a new secret for account (the username).
func Generate(account string) (Key, error) {
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer: Issuer, AccountName: account, Period: uint(Period / time.Second),
		SecretSize: SecretSize, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1, Rand: rand.Reader,
	})
	if err != nil {
		return Key{}, err
	}
	return Key{Secret: k.Secret(), URI: k.URL()}, nil
}

// Step is the RFC 6238 time step containing t.
func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

// NormalizeCode strips the spaces and dashes apps and users insert and
// checks the format.
func NormalizeCode(code string) (string, error) {
	c := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, code)
	if len(c) != Digits {
		return "", ErrMalformed
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return "", ErrMalformed
		}
	}
	return c, nil
}

// Verify checks code against secret at now. It accepts the steps within
// ±Skew of now that are strictly after lastStep (the step of the last code
// accepted for this secret; 0 when none), so a code can never be used
// twice, not even within its own 30 seconds. On success it returns the
// matched step, which the caller must persist atomically as the new
// lastStep before treating the factor as proven.
func Verify(secret, code string, now time.Time, lastStep int64) (step int64, ok bool, err error) {
	c, err := NormalizeCode(code)
	if err != nil {
		return 0, false, err
	}
	cur := Step(now)
	opts := hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	for s := cur - Skew; s <= cur+Skew; s++ {
		if s <= lastStep || s < 0 {
			continue
		}
		match, err := hotp.ValidateCustom(c, uint64(s), secret, opts)
		if err != nil {
			return 0, false, err
		}
		if match {
			return s, true, nil
		}
	}
	return 0, false, nil
}

// Code returns the code for secret at t (tests and diagnostics only).
func Code(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period: uint(Period / time.Second), Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
}
