package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustKey(t *testing.T, b byte) Key {
	t.Helper()
	k, err := NewKey(bytes.Repeat([]byte{b}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr := NewKeyring(mustKey(t, 1))
	env, err := kr.Seal([]byte("hunter2"), "registry/1/password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(env, "dy1."+kr.PrimaryKeyID()+".") {
		t.Fatalf("envelope = %q", env)
	}
	if strings.Contains(env, "hunter2") {
		t.Fatal("plaintext visible in envelope")
	}
	pt, err := kr.Open(env, "registry/1/password")
	if err != nil || string(pt) != "hunter2" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if id, err := KeyIDOf(env); err != nil || id != kr.PrimaryKeyID() {
		t.Fatalf("KeyIDOf = %q, %v", id, err)
	}

	env2, _ := kr.Seal([]byte("hunter2"), "registry/1/password")
	if env2 == env {
		t.Fatal("nonce reuse: identical envelopes")
	}
}

func TestOpenFailures(t *testing.T) {
	kr := NewKeyring(mustKey(t, 1))
	env, _ := kr.Seal([]byte("v"), "ctx-a")

	if _, err := kr.Open(env, "ctx-b"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong context: %v", err)
	}
	other := NewKeyring(mustKey(t, 2))
	if _, err := other.Open(env, "ctx-a"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
	tampered := env[:len(env)-2] + "AA"
	if tampered == env {
		tampered = env[:len(env)-2] + "BB"
	}
	if _, err := kr.Open(tampered, "ctx-a"); !errors.Is(err, ErrDecrypt) && !errors.Is(err, ErrMalformed) {
		t.Errorf("tampered: %v", err)
	}
	for _, bad := range []string{"", "dy1", "dy2.x.y", "dy1..abc", "dy1." + kr.PrimaryKeyID() + ".!!!", "dy1." + kr.PrimaryKeyID() + ".AAAA"} {
		if _, err := kr.Open(bad, "ctx-a"); !errors.Is(err, ErrMalformed) {
			t.Errorf("Open(%q) = %v, want ErrMalformed", bad, err)
		}
	}
}

func TestRetiredKeysStillOpen(t *testing.T) {
	oldKey, newKey := mustKey(t, 1), mustKey(t, 2)
	env, _ := NewKeyring(oldKey).Seal([]byte("v"), "c")
	kr := NewKeyring(newKey, oldKey)
	if pt, err := kr.Open(env, "c"); err != nil || string(pt) != "v" {
		t.Fatalf("Open with retired key = %q, %v", pt, err)
	}
	if oldKey.ID() == newKey.ID() {
		t.Fatal("key IDs collide")
	}
}

func TestKeyFileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secret.key")
	if _, err := LoadKeyFile(path); !errors.Is(err, ErrKeyFileMissing) {
		t.Fatalf("missing file: %v", err)
	}
	k, err := CreateKeyFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
	loaded, err := LoadKeyFile(path)
	if err != nil || loaded.ID() != k.ID() {
		t.Fatalf("reload = %v, %v", loaded.ID(), err)
	}
	if _, err := CreateKeyFile(path, nil); err == nil {
		t.Fatal("CreateKeyFile overwrote an existing key")
	}

	bad := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(bad, []byte("c2hvcnQ=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyFile(bad); err == nil {
		t.Fatal("short key accepted")
	}
}
