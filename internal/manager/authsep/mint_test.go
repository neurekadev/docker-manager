package authsep

import (
	"strings"
	"testing"
)

func TestMintParseVerify(t *testing.T) {
	const id = "0190a6e0-4444-7000-8000-000000000004"
	cred, err := MintAgentCredential(id)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := MintEnrollmentToken(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cred.Token, AgentCredentialPrefix+id+"_") || !strings.HasPrefix(tok.Token, EnrollmentTokenPrefix+id+"_") {
		t.Fatalf("formats %q %q", cred.Token, tok.Token)
	}
	if Classify(cred.Token) != KindAgent || Classify(tok.Token) != KindAgent {
		t.Fatal("minted secrets are not classified as agent secrets")
	}
	gotID, secret, ok := ParseAgentCredential(cred.Token)
	if !ok || gotID != id || !VerifierMatches(cred.Verifier, secret) || strings.Contains(cred.Verifier, secret) {
		t.Fatalf("parse credential: %q %v", gotID, ok)
	}
	if _, _, ok := ParseAgentCredential(tok.Token); ok {
		t.Fatal("enrollment token parsed as credential")
	}
	if _, _, ok := ParseEnrollmentToken(cred.Token); ok {
		t.Fatal("credential parsed as enrollment token")
	}
	other, _ := MintAgentCredential(id)
	_, otherSecret, _ := ParseAgentCredential(other.Token)
	if VerifierMatches(cred.Verifier, otherSecret) || VerifierMatches("", secret) {
		t.Fatal("verifier matched another secret")
	}
	for _, bad := range []string{"", "dya_", "dya_id", "dya_id_short", "dya__" + strings.Repeat("A", 43), "dya_a/b_" + strings.Repeat("A", 43),
		"dya_" + id + "_" + strings.Repeat("A", 42) + "=", "dya_" + id + "_" + strings.Repeat("*", 43), "dya_" + strings.Repeat("a", 65) + "_" + strings.Repeat("A", 43)} {
		if _, _, ok := ParseAgentCredential(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := MintAgentCredential("bad id"); err == nil {
		t.Fatal("minted with an invalid record ID")
	}
}

// FuzzParseAgentSecret: parsing presented bearer secrets never panics, and
// whatever parses re-assembles to the same token with a valid ID.
func FuzzParseAgentSecret(f *testing.F) {
	c, _ := MintAgentCredential("0190a6e0-4444-7000-8000-000000000004")
	e, _ := MintEnrollmentToken("0190a6e0-4444-7000-8000-000000000005")
	for _, s := range []string{c.Token, e.Token, "dya_x_y", "dye_", "dya_" + strings.Repeat("_", 50), "Bearer dya_x", "dya_\x00_\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for prefix, parse := range map[string]func(string) (string, string, bool){
			AgentCredentialPrefix: ParseAgentCredential, EnrollmentTokenPrefix: ParseEnrollmentToken,
		} {
			id, secret, ok := parse(s)
			if !ok {
				continue
			}
			if prefix+id+"_"+secret != s || !validRecordID(id) || len(secret) != 43 {
				t.Fatalf("inconsistent parse of %q: %q %q", s, id, secret)
			}
		}
	})
}
