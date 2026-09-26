package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Key file format: the base64 (standard encoding) of KeySize random bytes,
// optionally followed by a newline. Operators can create one with
//
//	openssl rand -base64 32 > secret.key
//
// and mount it separately from the data volume (DOCKER_MANAGER_SECRET_KEY_FILE).

// ErrKeyFileMissing is returned by LoadKeyFile when the file does not exist.
var ErrKeyFileMissing = errors.New("secrets: key file does not exist")

// LoadKeyFile reads a key file. File contents are never included in errors.
func LoadKeyFile(path string) (Key, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-configured path
	if errors.Is(err, os.ErrNotExist) {
		return Key{}, ErrKeyFileMissing
	}
	if err != nil {
		return Key{}, fmt.Errorf("secrets: read key file: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return Key{}, fmt.Errorf("secrets: key file %s is not valid base64", path)
	}
	return NewKey(raw)
}

// ReplaceKeyFile atomically replaces the key file at path with k (mode
// 0600, written to a temporary file in the same directory, synced, then
// renamed). Only a manager-state restore (#24) replaces a key; callers
// keep the previous file themselves.
func ReplaceKeyFile(path string, k Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secrets: create key directory: %w", err)
	}
	tmp := path + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // operator-configured path
	if err != nil {
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(k.raw) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secrets: replace key file: %w", err)
	}
	return nil
}

// CreateKeyFile generates a key and writes it to path with mode 0600. It
// fails if the file already exists, so it never overwrites a key.
func CreateKeyFile(path string, r io.Reader) (Key, error) {
	k, err := GenerateKey(r)
	if err != nil {
		return Key{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Key{}, fmt.Errorf("secrets: create key directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // operator-configured path
	if err != nil {
		return Key{}, fmt.Errorf("secrets: create key file: %w", err)
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(k.raw) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(path)
		return Key{}, fmt.Errorf("secrets: write key file: %w", err)
	}
	return k, nil
}
