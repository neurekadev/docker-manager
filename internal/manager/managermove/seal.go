package managermove

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
)

// The secret key leaves the old manager only sealed under the move code
// (secret-key.sealed): XChaCha20-Poly1305 under HKDF-SHA256 of the code's
// secret part (random salt), with the format, the instance ID and the key
// ID as associated data. Only the holder of the code opens it.

const (
	sealedKeyFormat  = "docker-manager-move-key"
	sealedKeyVersion = 1
	sealedKeyInfo    = "docker-manager/move-key/v1"
)

// sealedKey is secret-key.sealed.
type sealedKey struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	InstanceID  string `json:"instanceId"`
	SecretKeyID string `json:"secretKeyId"`
	KDF         string `json:"kdf"`
	Salt        string `json:"salt"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

// errSealedKey means the sealed secret key does not open with the code
// (another code, or a damaged or forged file).
var errSealedKey = errors.New("the sealed secret key does not open with this move code")

func sealedKeyAD(b sealedKey) []byte {
	return []byte(sealedKeyFormat + "\x00" + b.InstanceID + "\x00" + b.SecretKeyID)
}

// codeSecret returns the raw secret of a move code.
func codeSecret(code string) ([]byte, error) {
	_, secret, ok := authsep.ParseMoveCode(code)
	if !ok {
		return nil, errSealedKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return nil, errSealedKey
	}
	return raw, nil
}

func sealingKey(code string, salt []byte) ([]byte, error) {
	secret, err := codeSecret(code)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, secret, salt, sealedKeyInfo, chacha20poly1305.KeySize)
}

// sealSecretKey seals key under code for instanceID.
func sealSecretKey(key secrets.Key, code, instanceID string) ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	k, err := sealingKey(code, salt)
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
	b := sealedKey{Format: sealedKeyFormat, Version: sealedKeyVersion, InstanceID: instanceID, SecretKeyID: key.ID(), KDF: "hkdf-sha256",
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce)}
	b.Ciphertext = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, key.Bytes(), sealedKeyAD(b)))
	return json.MarshalIndent(b, "", "  ")
}

// openSecretKey opens a sealed secret key with code; the key must belong
// to instanceID (errSealedKey otherwise).
func openSecretKey(data []byte, code, instanceID string) (secrets.Key, error) {
	var b sealedKey
	if err := json.Unmarshal(data, &b); err != nil || b.Format != sealedKeyFormat || b.Version != sealedKeyVersion || b.KDF != "hkdf-sha256" ||
		b.InstanceID != instanceID {
		return secrets.Key{}, errSealedKey
	}
	salt, err1 := base64.StdEncoding.DecodeString(b.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(b.Nonce)
	ct, err3 := base64.StdEncoding.DecodeString(b.Ciphertext)
	if errors.Join(err1, err2, err3) != nil || len(salt) != 32 {
		return secrets.Key{}, errSealedKey
	}
	k, err := sealingKey(code, salt)
	if err != nil {
		return secrets.Key{}, errSealedKey
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil || len(nonce) != aead.NonceSize() {
		return secrets.Key{}, errSealedKey
	}
	raw, err := aead.Open(nil, nonce, ct, sealedKeyAD(b))
	if err != nil {
		return secrets.Key{}, errSealedKey
	}
	key, err := secrets.NewKey(raw)
	if err != nil || key.ID() != b.SecretKeyID {
		return secrets.Key{}, errSealedKey
	}
	return key, nil
}
