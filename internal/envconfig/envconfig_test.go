package envconfig

import "testing"

func TestSecretVariants(t *testing.T) {
	src := Map(map[string]string{
		"A":      "direct",
		"B_FILE": "/run/secrets/b",
		"C":      "x",
		"C_FILE": "/run/secrets/c",
		"D_FILE": "/missing",
	}, map[string]string{"/run/secrets/b": "from-file\n"})

	if v, err := src.Secret("A"); err != nil || v != "direct" {
		t.Fatalf("A = %q, %v", v, err)
	}
	if v, err := src.Secret("B"); err != nil || v != "from-file" {
		t.Fatalf("B = %q, %v", v, err)
	}
	if _, err := src.Secret("C"); err == nil {
		t.Fatal("C: expected mutual exclusion error")
	}
	if _, err := src.Secret("D"); err == nil {
		t.Fatal("D: expected missing file error")
	}
	if v, err := src.Secret("E"); err != nil || v != "" {
		t.Fatalf("E = %q, %v", v, err)
	}
}

func TestBoolAndString(t *testing.T) {
	src := Map(map[string]string{"T": "TRUE", "F": "off", "X": "maybe", "S": "  v  "}, nil)
	if v, err := src.Bool("T", false); err != nil || !v {
		t.Fatal("T")
	}
	if v, err := src.Bool("F", true); err != nil || v {
		t.Fatal("F")
	}
	if _, err := src.Bool("X", false); err == nil {
		t.Fatal("X: expected error")
	}
	if v, _ := src.Bool("missing", true); !v {
		t.Fatal("default")
	}
	if src.String("S", "d") != "v" || src.String("missing", "d") != "d" {
		t.Fatal("String")
	}
}
