package backups

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
)

// The Recovery Key (#10, #24; decision #25 Q7): one instance-wide,
// high-entropy key that is the restic password of every repository
// DockYard creates (the manager scope and every environment scope, local
// and S3). The owner saves it once; any single repository opens with it,
// and the manager-state snapshot carries the manager's secret-protection
// key sealed under a key derived from it (the key bundle), so the Recovery
// Key alone recovers the encrypted settings of a lost manager.
//
// Format: "DYRK-" followed by 13 groups of four base32 characters: 30
// random bytes (240 bits) plus a 2-byte checksum, so typos are detected
// before a repository ever sees the input. The canonical text is the
// restic password.

const (
	recoveryKeyPrefix  = "DYRK"
	recoveryKeyRandom  = 30
	recoveryKeyCheck   = 2
	recoveryKeyGroups  = 13
	recoveryKeyChars   = 52
	fingerprintContext = "dockyard/recovery-key-fingerprint/v1\x00"
	checksumContext    = "dockyard/recovery-key-checksum/v1\x00"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryKey is a parsed Recovery Key.
type RecoveryKey struct {
	raw [recoveryKeyRandom + recoveryKeyCheck]byte
}

// GenerateRecoveryKey returns a new key from r (crypto/rand when nil).
func GenerateRecoveryKey(r io.Reader) (RecoveryKey, error) {
	if r == nil {
		r = rand.Reader
	}
	var k RecoveryKey
	if _, err := io.ReadFull(r, k.raw[:recoveryKeyRandom]); err != nil {
		return RecoveryKey{}, fmt.Errorf("backups: generate recovery key: %w", err)
	}
	sum := checksum(k.raw[:recoveryKeyRandom])
	copy(k.raw[recoveryKeyRandom:], sum)
	return k, nil
}

func checksum(random []byte) []byte {
	h := sha256.New()
	h.Write([]byte(checksumContext))
	h.Write(random)
	return h.Sum(nil)[:recoveryKeyCheck]
}

// ParseRecoveryKey accepts a key as typed or pasted: case-insensitive,
// with or without the DYRK prefix, dashes and whitespace; the digits 0, 1
// and 8 are read as O, I and B. A wrong checksum is
// domain.ErrRecoveryKeyMalformed.
func ParseRecoveryKey(s string) (RecoveryKey, error) {
	s = strings.ToUpper(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '-', '_':
			return -1
		case '0':
			return 'O'
		case '1':
			return 'I'
		case '8':
			return 'B'
		}
		return r
	}, s)
	s = strings.TrimPrefix(s, recoveryKeyPrefix)
	if len(s) != recoveryKeyChars {
		return RecoveryKey{}, domain.ErrRecoveryKeyMalformed
	}
	b, err := b32.DecodeString(s)
	if err != nil || len(b) != recoveryKeyRandom+recoveryKeyCheck {
		return RecoveryKey{}, domain.ErrRecoveryKeyMalformed
	}
	var k RecoveryKey
	copy(k.raw[:], b)
	if subtle.ConstantTimeCompare(checksum(k.raw[:recoveryKeyRandom]), k.raw[recoveryKeyRandom:]) != 1 {
		return RecoveryKey{}, domain.ErrRecoveryKeyMalformed
	}
	return k, nil
}

// String returns the canonical, grouped form (the restic password).
func (k RecoveryKey) String() string {
	enc := b32.EncodeToString(k.raw[:])
	var sb strings.Builder
	sb.WriteString(recoveryKeyPrefix)
	for i := 0; i < recoveryKeyGroups; i++ {
		sb.WriteByte('-')
		sb.WriteString(enc[i*4 : i*4+4])
	}
	return sb.String()
}

// GoString hides the key (%#v).
func (k RecoveryKey) GoString() string { return "backups.RecoveryKey{redacted}" }

// Fingerprint identifies the key without revealing it: "rk_" and 16 hex
// digits of a domain-separated SHA-256 (a 240-bit random key cannot be
// brute-forced from it).
func (k RecoveryKey) Fingerprint() string {
	h := sha256.New()
	h.Write([]byte(fingerprintContext))
	h.Write(k.raw[:])
	return "rk_" + hex.EncodeToString(h.Sum(nil)[:8])
}

// Equal compares keys in constant time.
func (k RecoveryKey) Equal(o RecoveryKey) bool {
	return subtle.ConstantTimeCompare(k.raw[:], o.raw[:]) == 1
}

// --- the secret-key bundle ---

// The key bundle is a small JSON file inside every manager-state snapshot:
// the manager's secret-protection key sealed with XChaCha20-Poly1305 under
// HKDF-SHA256(Recovery Key, salt). The snapshot itself is encrypted by
// restic with the same Recovery Key; the bundle keeps the secret key
// unreadable to anyone holding only a restic key added outside DockYard.

// Bundle format identifiers.
const (
	BundleFormat  = "dockyard-secret-key-bundle"
	BundleVersion = 1
	BundleFile    = "secret-key.bundle"
	bundleInfo    = "dockyard/secret-key-bundle/v1"
)

// KeyBundle is the serialized bundle.
type KeyBundle struct {
	Format                 string `json:"format"`
	Version                int    `json:"version"`
	InstanceID             string `json:"instanceId"`
	SecretKeyID            string `json:"secretKeyId"`
	RecoveryKeyFingerprint string `json:"recoveryKeyFingerprint"`
	KDF                    string `json:"kdf"`
	Salt                   string `json:"salt"`
	Nonce                  string `json:"nonce"`
	Ciphertext             string `json:"ciphertext"`
}

// Bundle errors.
var (
	// ErrBundleKeyMismatch: the bundle was sealed under another Recovery
	// Key (e.g. a snapshot taken before a key rotation).
	ErrBundleKeyMismatch = errors.New("the manager secret key in this snapshot is sealed under a different Recovery Key")
	// ErrBundleCorrupt: the bundle is unreadable or fails authentication.
	ErrBundleCorrupt = errors.New("the manager secret key bundle is corrupt")
)

func bundleAD(b KeyBundle) []byte {
	return []byte(BundleFormat + "\x00" + b.InstanceID + "\x00" + b.SecretKeyID + "\x00" + b.RecoveryKeyFingerprint)
}

func bundleKey(rk RecoveryKey, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, rk.raw[:], salt, bundleInfo, chacha20poly1305.KeySize)
}

// SealKeyBundle seals the manager's secret key under rk.
func SealKeyBundle(key secrets.Key, rk RecoveryKey, instanceID string) ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	k, err := bundleKey(rk, salt)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	b := KeyBundle{Format: BundleFormat, Version: BundleVersion, InstanceID: instanceID, SecretKeyID: key.ID(),
		RecoveryKeyFingerprint: rk.Fingerprint(), KDF: "hkdf-sha256", Salt: base64.StdEncoding.EncodeToString(salt),
		Nonce: base64.StdEncoding.EncodeToString(nonce)}
	b.Ciphertext = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, key.Bytes(), bundleAD(b)))
	return json.MarshalIndent(b, "", "  ")
}

// OpenKeyBundle recovers the secret key from a bundle with rk.
func OpenKeyBundle(data []byte, rk RecoveryKey) (secrets.Key, KeyBundle, error) {
	var b KeyBundle
	if err := json.Unmarshal(data, &b); err != nil || b.Format != BundleFormat || b.Version != BundleVersion || b.KDF != "hkdf-sha256" {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	if b.RecoveryKeyFingerprint != rk.Fingerprint() {
		return secrets.Key{}, b, ErrBundleKeyMismatch
	}
	salt, err1 := base64.StdEncoding.DecodeString(b.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(b.Nonce)
	ct, err3 := base64.StdEncoding.DecodeString(b.Ciphertext)
	if err := errors.Join(err1, err2, err3); err != nil || len(salt) != 32 {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	k, err := bundleKey(rk, salt)
	if err != nil {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil || len(nonce) != aead.NonceSize() {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	raw, err := aead.Open(nil, nonce, ct, bundleAD(b))
	if err != nil {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	key, err := secrets.NewKey(raw)
	if err != nil || key.ID() != b.SecretKeyID {
		return secrets.Key{}, b, ErrBundleCorrupt
	}
	return key, b, nil
}
