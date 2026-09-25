package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// One-time code prefixes. They make codes recognizable (and scannable by
// secret scanners) and keep them apart from agent secrets (authsep dya_/dye_).
const (
	InvitationCodePrefix    = "dyi_"
	PasswordResetCodePrefix = "dyr_"
	OwnerRecoveryCodePrefix = "dyo_"
)

// newLinkCode returns prefix + 256 random bits (base64url). Only its
// verifier is stored.
func newLinkCode(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand never fails (Go 1.24+)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// verifier is the stored form of a high-entropy code: SHA-256 is enough
// (no brute-forceable input), and lookups by verifier are exact matches.
func verifier(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

var recoveryAlphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newRecoveryCode returns an 80-bit code formatted xxxx-xxxx-xxxx-xxxx.
func newRecoveryCode() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	s := recoveryAlphabet.EncodeToString(b) // 16 characters
	return s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:]
}

// recoveryVerifier binds a recovery code to its user, so equal codes of two
// users never collide and a stolen table cannot be matched across users.
func recoveryVerifier(userID, code string) string {
	c := strings.ToLower(strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, code))
	sum := sha256.Sum256([]byte(userID + ":" + c))
	return hex.EncodeToString(sum[:])
}
