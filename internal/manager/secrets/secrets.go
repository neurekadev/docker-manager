// Package secrets protects sensitive manager settings at rest (registry
// credentials, S3 keys, TOTP seeds, recovery-key working copies, ...).
//
// Values are sealed with XChaCha20-Poly1305 under the application
// secret-protection key (DOCKYARD_SECRET_KEY_FILE). A sealed value is a
// versioned text envelope that records which key sealed it:
//
//	dy1.<keyID>.<base64url(nonce || ciphertext)>
//
// The key ID and envelope version are bound into the AEAD associated data
// together with a caller-supplied context string (for example
// "registry/0190a6e0-.../password"), so a ciphertext cannot be moved to a
// different field or record undetected. A Keyring can hold retired keys for
// decryption so rotation (#24) can re-seal values lazily.
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the size in bytes of a secret-protection key.
const KeySize = chacha20poly1305.KeySize

// envelopeVersion prefixes every sealed value.
const envelopeVersion = "dy1"

var (
	// ErrMalformed is returned for values that are not a valid envelope.
	ErrMalformed = errors.New("secrets: malformed envelope")
	// ErrUnknownKey is returned when the envelope names a key the keyring lacks.
	ErrUnknownKey = errors.New("secrets: envelope sealed with an unknown key")
	// ErrDecrypt is returned when authentication fails (wrong key, wrong
	// context or tampered ciphertext).
	ErrDecrypt = errors.New("secrets: decryption failed")
)

// Key is a secret-protection key with its derived identifier.
type Key struct {
	id  string
	raw []byte
}

// NewKey wraps raw key material (KeySize bytes).
func NewKey(raw []byte) (Key, error) {
	if len(raw) != KeySize {
		return Key{}, fmt.Errorf("secrets: key must be %d bytes, got %d", KeySize, len(raw))
	}
	sum := sha256.Sum256(append([]byte("dockyard/secret-key-id/v1\x00"), raw...))
	k := Key{id: hex.EncodeToString(sum[:8]), raw: make([]byte, KeySize)}
	copy(k.raw, raw)
	return k, nil
}

// GenerateKey creates a new random key using r (crypto/rand.Reader if nil).
func GenerateKey(r io.Reader) (Key, error) {
	if r == nil {
		r = rand.Reader
	}
	raw := make([]byte, KeySize)
	if _, err := io.ReadFull(r, raw); err != nil {
		return Key{}, fmt.Errorf("secrets: generate key: %w", err)
	}
	return NewKey(raw)
}

// ID is a non-secret fingerprint of the key, safe to log and store.
func (k Key) ID() string { return k.id }

// Keyring seals with a primary key and opens with any known key.
type Keyring struct {
	primary Key
	keys    map[string]Key
	rand    io.Reader
}

// NewKeyring returns a keyring sealing with primary and additionally able to
// open values sealed with any of retired.
func NewKeyring(primary Key, retired ...Key) *Keyring {
	kr := &Keyring{primary: primary, keys: map[string]Key{primary.id: primary}, rand: rand.Reader}
	for _, k := range retired {
		kr.keys[k.id] = k
	}
	return kr
}

// PrimaryKeyID returns the ID of the key used for sealing.
func (kr *Keyring) PrimaryKeyID() string { return kr.primary.id }

// Seal encrypts plaintext bound to context (e.g. "settings/smtp.password").
func (kr *Keyring) Seal(plaintext []byte, context string) (string, error) {
	aead, err := chacha20poly1305.NewX(kr.primary.raw)
	if err != nil {
		return "", fmt.Errorf("secrets: %w", err)
	}
	buf := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := io.ReadFull(kr.rand, buf); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	buf = aead.Seal(buf, buf[:aead.NonceSize()], plaintext, associatedData(kr.primary.id, context))
	return envelopeVersion + "." + kr.primary.id + "." + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Open decrypts an envelope produced by Seal with the same context.
func (kr *Keyring) Open(envelope string, context string) ([]byte, error) {
	version, rest, ok := strings.Cut(envelope, ".")
	if !ok || version != envelopeVersion {
		return nil, ErrMalformed
	}
	keyID, body, ok := strings.Cut(rest, ".")
	if !ok || keyID == "" {
		return nil, ErrMalformed
	}
	key, ok := kr.keys[keyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, ErrMalformed
	}
	aead, err := chacha20poly1305.NewX(key.raw)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	if len(data) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrMalformed
	}
	pt, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], associatedData(keyID, context))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// KeyIDOf returns the key ID recorded in an envelope (for rotation sweeps).
func KeyIDOf(envelope string) (string, error) {
	parts := strings.SplitN(envelope, ".", 3)
	if len(parts) != 3 || parts[0] != envelopeVersion || parts[1] == "" {
		return "", ErrMalformed
	}
	return parts[1], nil
}

func associatedData(keyID, context string) []byte {
	return []byte(envelopeVersion + "\x00" + keyID + "\x00" + context)
}
