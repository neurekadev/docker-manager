package testharness

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HtpasswdLine returns "user:<bcrypt hash>" for the registry's htpasswd
// auth and Caddy's basic_auth (which takes the hash alone, see BcryptHash).
func HtpasswdLine(user, password string) (string, error) {
	if user == "" || strings.ContainsAny(user, ":\n") {
		return "", fmt.Errorf("htpasswd: invalid user name %q", user)
	}
	h, err := BcryptHash(password)
	if err != nil {
		return "", err
	}
	return user + ":" + h, nil
}

// BcryptHash hashes password with bcrypt at the minimum cost (fixtures
// only; fast to verify).
func BcryptHash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(h), nil
}

// RandomSecret returns a random hex string with prefix, for fixture
// passwords and keys (distinct per test run, easy to spot in leaks).
func RandomSecret(prefix string, bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return prefix + hex.EncodeToString(b)
}

// RegistryAuth encodes credentials as the Engine API's X-Registry-Auth
// value (base64url JSON AuthConfig), as the Moby client's
// ImagePullOptions.RegistryAuth expects.
func RegistryAuth(user, password, serverAddress string) string {
	b, _ := json.Marshal(map[string]string{
		"username":      user,
		"password":      password,
		"serveraddress": serverAddress,
	})
	return base64.URLEncoding.EncodeToString(b)
}
