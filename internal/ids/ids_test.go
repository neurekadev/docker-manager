package ids

import "testing"

func TestNewIsValidAndUnique(t *testing.T) {
	a, b := New(), New()
	if !Valid(a) || !Valid(b) || a == b {
		t.Fatalf("bad ids %q %q", a, b)
	}
	if a[14] != '7' {
		t.Fatalf("not a UUIDv7: %q", a)
	}
	for _, s := range []string{"", "not-a-uuid", "0190A6E0-0000-7000-8000-000000000000", "{0190a6e0-0000-7000-8000-000000000000}"} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}
