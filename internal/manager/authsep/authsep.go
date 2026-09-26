// Package authsep keeps the two credential worlds of the single public
// origin apart (#27):
//
//   - browser sessions (cookies, #16) and API tokens (#31) authenticate only
//     /api/v1;
//   - agent credentials and enrollment tokens (#3) authenticate only
//     /agent/v1.
//
// API tokens are "dy_<id>_<secret>" (MintAPIToken); agent secrets are
// "dya_..." and "dye_...", so the three never parse as each other.
//
// Agent credentials and enrollment tokens are recognizable by their
// prefixes, so the server can refuse them on /api/v1 before any handler
// runs, and it strips the Cookie header from every /agent/v1 request so no
// agent handler can ever see (let alone accept) a browser session. The
// middleware lives in internal/manager/server (routeBoundaries); #3 must
// mint credentials with NewAgentCredential /
// NewEnrollmentToken (or at least keep the prefixes) and read them with
// BearerToken.
package authsep

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Credential prefixes. Never reuse them for other token types.
const (
	// AgentCredentialPrefix marks an agent's long-lived session credential.
	AgentCredentialPrefix = protocol.CredentialPrefix
	// EnrollmentTokenPrefix marks a one-use agent enrollment token.
	EnrollmentTokenPrefix = protocol.EnrollmentTokenPrefix
	// APITokenPrefix marks a user's API token (#31, /api/v1 only).
	APITokenPrefix = "dy_"
)

// secretBytes is the entropy of generated agent secrets.
const secretBytes = 32

// Kind classifies a presented bearer token.
type Kind int

// Token kinds.
const (
	// KindNone: no bearer token presented.
	KindNone Kind = iota
	// KindAgent: an agent credential or enrollment token (agent routes only).
	KindAgent
	// KindOther: any other bearer token (e.g. an API token, #31).
	KindOther
)

// Classify returns the kind of a bearer token value.
func Classify(token string) Kind {
	switch {
	case token == "":
		return KindNone
	case IsAgentSecret(token):
		return KindAgent
	default:
		return KindOther
	}
}

// IsAgentSecret reports whether token is an agent credential or an
// enrollment token.
func IsAgentSecret(token string) bool {
	return strings.HasPrefix(token, AgentCredentialPrefix) || strings.HasPrefix(token, EnrollmentTokenPrefix)
}

// BearerToken extracts the token from "Authorization: Bearer <token>"
// (scheme case-insensitive). ok is false when the header is absent, uses
// another scheme or is malformed.
func BearerToken(h http.Header) (token string, ok bool) {
	v := h.Get("Authorization")
	scheme, tok, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok, true
}

// NewAgentCredential returns a fresh agent session credential
// (AgentCredentialPrefix + 256 random bits, base64url).
func NewAgentCredential() (string, error) { return newSecret(AgentCredentialPrefix) }

// NewEnrollmentToken returns a fresh one-use enrollment token
// (EnrollmentTokenPrefix + 256 random bits, base64url).
func NewEnrollmentToken() (string, error) { return newSecret(EnrollmentTokenPrefix) }

func newSecret(prefix string) (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Minted is a freshly generated agent secret with an embedded record ID:
// "<prefix><id>_<secret>". The manager stores only Verifier (SHA-256 of the
// secret part) and looks the record up by ID, then compares verifiers in
// constant time (docs/protocol/agent-v1.md).
type Minted struct {
	// Token is shown (enrollment) or sent (credential) exactly once.
	Token string
	// ID is the record ID embedded in Token.
	ID string
	// Verifier is the hex SHA-256 of the secret part; the only stored form.
	Verifier string
}

// MintAgentCredential returns a new agent credential for record id.
func MintAgentCredential(id string) (Minted, error) { return mint(AgentCredentialPrefix, id) }

// MintEnrollmentToken returns a new enrollment token for record id.
func MintEnrollmentToken(id string) (Minted, error) { return mint(EnrollmentTokenPrefix, id) }

// MintAPIToken returns a new API token (#31) for record id:
// "dy_<id>_<256-bit secret>". Only Verifier is stored.
func MintAPIToken(id string) (Minted, error) { return mint(APITokenPrefix, id) }

func mint(prefix, id string) (Minted, error) {
	if !validRecordID(id) {
		return Minted{}, errInvalidID
	}
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return Minted{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	return Minted{Token: prefix + id + "_" + secret, ID: id, Verifier: Verifier(secret)}, nil
}

type idError string

func (e idError) Error() string { return string(e) }

const errInvalidID = idError("authsep: record IDs must be 1-64 characters of [A-Za-z0-9-]")

func validRecordID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// ParseAgentCredential splits an agent credential into its record ID and
// secret. ok is false for anything that is not a well-formed credential.
func ParseAgentCredential(token string) (id, secret string, ok bool) {
	return parse(AgentCredentialPrefix, token)
}

// ParseEnrollmentToken splits an enrollment token into its record ID and
// secret.
func ParseEnrollmentToken(token string) (id, secret string, ok bool) {
	return parse(EnrollmentTokenPrefix, token)
}

// ParseAPIToken splits an API token into its record ID and secret.
func ParseAPIToken(token string) (id, secret string, ok bool) {
	return parse(APITokenPrefix, token)
}

// maxTokenLen bounds a parsed token (prefix, 64-byte ID, separator and a
// 43-character secret fit comfortably).
const maxTokenLen = 256

func parse(prefix, token string) (id, secret string, ok bool) {
	if len(token) > maxTokenLen {
		return "", "", false
	}
	rest, found := strings.CutPrefix(token, prefix)
	if !found {
		return "", "", false
	}
	id, secret, found = strings.Cut(rest, "_")
	if !found || !validRecordID(id) || len(secret) != base64.RawURLEncoding.EncodedLen(secretBytes) {
		return "", "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(secret); err != nil {
		return "", "", false
	}
	return id, secret, true
}

// Verifier returns the stored form of a secret: hex SHA-256. The secrets
// carry 256 random bits, so a fast hash is sufficient.
func Verifier(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// VerifierMatches compares secret with a stored verifier in constant time.
func VerifierMatches(stored, secret string) bool {
	got := Verifier(secret)
	return len(stored) == len(got) && subtle.ConstantTimeCompare([]byte(stored), []byte(got)) == 1
}
