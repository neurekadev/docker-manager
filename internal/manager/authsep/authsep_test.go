package authsep

import (
	"net/http"
	"strings"
	"testing"
)

func TestBearerToken(t *testing.T) {
	cases := map[string]struct {
		tok string
		ok  bool
	}{
		"":                    {"", false},
		"Bearer abc":          {"abc", true},
		"bearer  abc ":        {"abc", true},
		"BEARER dya_x":        {"dya_x", true},
		"Basic YWxhZGRpbjpv":  {"", false},
		"Bearer":              {"", false},
		"Bearer a b":          {"", false},
		"Bearer\tabc":         {"", false},
		"Token dya_something": {"", false},
	}
	for header, want := range cases {
		h := http.Header{}
		if header != "" {
			h.Set("Authorization", header)
		}
		tok, ok := BearerToken(h)
		if tok != want.tok || ok != want.ok {
			t.Errorf("%q: got %q %v, want %q %v", header, tok, ok, want.tok, want.ok)
		}
	}
}

func TestClassifyAndGenerate(t *testing.T) {
	cred, err := NewAgentCredential()
	if err != nil {
		t.Fatal(err)
	}
	enroll, err := NewEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cred, AgentCredentialPrefix) || !strings.HasPrefix(enroll, EnrollmentTokenPrefix) || len(cred) != len(AgentCredentialPrefix)+43 {
		t.Fatalf("credential %q enrollment %q", cred, enroll)
	}
	if again, _ := NewAgentCredential(); again == cred {
		t.Fatal("credentials repeat")
	}
	for tok, want := range map[string]Kind{"": KindNone, cred: KindAgent, enroll: KindAgent, "dyt_api": KindOther, "random": KindOther, "DYA_upper": KindOther} {
		if got := Classify(tok); got != want {
			t.Errorf("Classify(%q) = %v, want %v", tok, got, want)
		}
	}
}
