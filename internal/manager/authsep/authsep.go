// Package authsep keeps the two credential worlds of the single public
// origin apart (#27):
//
//   - browser sessions (cookies, #16) and API tokens (#31) authenticate only
//     /api/v1;
//   - agent credentials and enrollment tokens (#3) authenticate only
//     /agent/v1.
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
	"encoding/base64"
	"net/http"
	"strings"
)

// Credential prefixes. Never reuse them for other token types.
const (
	// AgentCredentialPrefix marks an agent's long-lived session credential.
	AgentCredentialPrefix = "dya_"
	// EnrollmentTokenPrefix marks a one-use agent enrollment token.
	EnrollmentTokenPrefix = "dye_"
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
