package backups

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/secrets"
)

func TestRecoveryKeyFormatAndParsing(t *testing.T) {
	k, err := GenerateRecoveryKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := k.String()
	if !regexp.MustCompile(`^DYRK(-[A-Z2-7]{4}){13}$`).MatchString(s) {
		t.Fatalf("format %q", s)
	}
	k2, err := GenerateRecoveryKey(nil)
	if err != nil || k2.Equal(k) || k2.Fingerprint() == k.Fingerprint() {
		t.Fatal("two keys are equal")
	}
	// Typed with spaces, lower case, without prefix, 0/1/8 look-alikes.
	loose := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(s, "DYRK-"), "-", " "))
	loose = strings.NewReplacer("o", "0", "i", "1", "b", "8").Replace(loose)
	p, err := ParseRecoveryKey(loose)
	if err != nil || !p.Equal(k) || p.String() != s {
		t.Fatalf("parse %q: %v", loose, err)
	}
	if !regexp.MustCompile(`^rk_[0-9a-f]{16}$`).MatchString(k.Fingerprint()) || strings.Contains(s, k.Fingerprint()[3:]) {
		t.Errorf("fingerprint %q", k.Fingerprint())
	}
	// A typo is detected by the checksum, not reported as a wrong key.
	typo := []byte(s)
	i := len(typo) - 8
	if typo[i] == 'A' {
		typo[i] = 'B'
	} else {
		typo[i] = 'A'
	}
	for _, bad := range []string{string(typo), s[:len(s)-5], s + "AAAA", "", "DYRK-", "not a key at all"} {
		if _, err := ParseRecoveryKey(bad); !errors.Is(err, domain.ErrRecoveryKeyMalformed) {
			t.Errorf("parse %q: %v", bad, err)
		}
	}
	if strings.Contains(strings.ToLower(k.GoString()), strings.ToLower(s[5:15])) {
		t.Error("GoString reveals the key")
	}
}

func TestKeyBundleRoundTrip(t *testing.T) {
	sk, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rk, _ := GenerateRecoveryKey(nil)
	b, err := SealKeyBundle(sk, rk, "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, sk.Bytes()) || bytes.Contains(b, []byte(rk.String())) {
		t.Fatal("bundle contains the key material")
	}
	got, meta, err := OpenKeyBundle(b, rk)
	if err != nil || got.ID() != sk.ID() || !bytes.Equal(got.Bytes(), sk.Bytes()) || meta.InstanceID != "inst-1" ||
		meta.RecoveryKeyFingerprint != rk.Fingerprint() {
		t.Fatalf("open: %v %+v", err, meta)
	}
	// A value sealed under the old key opens with the recovered keyring.
	sealed, _ := secrets.NewKeyring(sk).Seal([]byte("s3-secret"), "ctx")
	if v, err := secrets.NewKeyring(got).Open(sealed, "ctx"); err != nil || string(v) != "s3-secret" {
		t.Fatalf("recovered keyring: %q %v", v, err)
	}

	other, _ := GenerateRecoveryKey(nil)
	if _, _, err := OpenKeyBundle(b, other); !errors.Is(err, ErrBundleKeyMismatch) {
		t.Errorf("other key: %v", err)
	}
	var tampered KeyBundle
	_ = json.Unmarshal(b, &tampered)
	tampered.InstanceID = "inst-2"
	tb, _ := json.Marshal(tampered)
	if _, _, err := OpenKeyBundle(tb, rk); !errors.Is(err, ErrBundleCorrupt) {
		t.Errorf("tampered bundle: %v", err)
	}
	junk := make([]byte, 64)
	_, _ = rand.Read(junk)
	for _, bad := range [][]byte{nil, junk, b[:len(b)/2]} {
		if _, _, err := OpenKeyBundle(bad, rk); !errors.Is(err, ErrBundleCorrupt) {
			t.Errorf("corrupt bundle: %v", err)
		}
	}
}
