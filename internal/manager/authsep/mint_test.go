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

func TestMintParseAPIToken(t *testing.T) {
	const id = "0190a6e0-4444-7000-8000-000000000031"
	tok, err := MintAPIToken(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, "dy_"+id+"_") || Classify(tok.Token) != KindOther {
		t.Fatalf("format %q, kind %v", tok.Token, Classify(tok.Token))
	}
	gotID, secret, ok := ParseAPIToken(tok.Token)
	if !ok || gotID != id || !VerifierMatches(tok.Verifier, secret) || strings.Contains(tok.Verifier, secret) {
		t.Fatalf("parse: %q %v", gotID, ok)
	}
	agent, _ := MintAgentCredential(id)
	enroll, _ := MintEnrollmentToken(id)
	for _, other := range []string{agent.Token, enroll.Token} {
		if _, _, ok := ParseAPIToken(other); ok {
			t.Fatalf("agent secret %q parsed as an API token", other[:4])
		}
	}
	if _, _, ok := ParseAgentCredential(tok.Token); ok {
		t.Fatal("API token parsed as an agent credential")
	}
	for _, bad := range []string{"", "dy_", "dy_" + id, "dy_" + id + "_short", "Dy_" + id + "_" + strings.Repeat("A", 43)} {
		if _, _, ok := ParseAPIToken(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestMintParseMoveCode(t *testing.T) {
	const id = "0190a6e0-4444-7000-8000-000000000035"
	code, err := MintMoveCode(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code.Token, "dmm_"+id+"_") || !IsMoveCode(code.Token) || Classify(code.Token) != KindOther {
		t.Fatalf("format %q", code.Token)
	}
	gotID, secret, ok := ParseMoveCode(code.Token)
	if !ok || gotID != id || !VerifierMatches(code.Verifier, secret) {
		t.Fatalf("parse move code: %q %v", gotID, ok)
	}
	api, _ := MintAPIToken(id)
	if _, _, ok := ParseMoveCode(api.Token); ok || IsMoveCode(api.Token) {
		t.Fatal("an API token parsed as a move code")
	}
	if _, _, ok := ParseAPIToken(code.Token); ok {
		t.Fatal("a move code parsed as an API token")
	}
}
