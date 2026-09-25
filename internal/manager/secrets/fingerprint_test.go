package secrets

import (
	"strings"
	"testing"
)

func TestFingerprintIsKeyedAndContextBound(t *testing.T) {
	a, b := NewKeyring(mustKey(t, 1)), NewKeyring(mustKey(t, 2))
	fp := a.Fingerprint([]byte("hunter2hunter2"), "registry_connections/1/secret")
	if !strings.HasPrefix(fp, "fp_") || len(fp) != 19 {
		t.Fatalf("fingerprint = %q", fp)
	}
	if fp != a.Fingerprint([]byte("hunter2hunter2"), "registry_connections/1/secret") {
		t.Fatal("fingerprint is not deterministic")
	}
	for name, other := range map[string]string{
		"other key":     b.Fingerprint([]byte("hunter2hunter2"), "registry_connections/1/secret"),
		"other context": a.Fingerprint([]byte("hunter2hunter2"), "registry_connections/2/secret"),
		"other value":   a.Fingerprint([]byte("hunter2hunter3"), "registry_connections/1/secret"),
	} {
		if other == fp {
			t.Errorf("%s: same fingerprint", name)
		}
	}
	if strings.Contains(fp, "hunter2") {
		t.Fatal("fingerprint contains the value")
	}
}
