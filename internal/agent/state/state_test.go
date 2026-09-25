package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestInstallIDIsStable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InstallID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("install ID %q", id)
	}
	s2, _ := Open(dir)
	if again, _ := s2.InstallID(); again != id {
		t.Fatalf("install ID changed: %s -> %s", id, again)
	}
	if err := os.WriteFile(filepath.Join(dir, InstallIDFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.InstallID(); err == nil {
		t.Fatal("corrupt install ID accepted")
	}
}

func TestCredentialLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if c, err := s.Credential(); c != nil || err != nil {
		t.Fatalf("fresh state: %+v %v", c, err)
	}
	c := Credential{AgentID: "a1", EnvironmentID: "e1", Credential: "dya_c1_secret", ManagerURL: "https://m"}
	if err := s.SaveCredential(c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, CredentialFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("credential file mode %v %v", fi.Mode(), err)
		}
	}
	got, err := s.Credential()
	if err != nil || *got != c {
		t.Fatalf("read back %+v %v", got, err)
	}
	if err := s.ReplaceCredential("dye_not_a_credential"); err == nil {
		t.Fatal("replaced with an enrollment token")
	}
	if err := s.ReplaceCredential("dya_c2_secret"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Credential(); got.Credential != "dya_c2_secret" || got.AgentID != "a1" {
		t.Fatalf("after rotation %+v", got)
	}
	if err := s.ClearCredential(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Credential(); got != nil {
		t.Fatal("credential survived Clear")
	}
	if err := s.ReplaceCredential("dya_c3_secret"); err == nil {
		t.Fatal("rotation without enrollment accepted")
	}
	// No temporary files are left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestTokenHandoverAndUsedTokens(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, bad := range []string{"", "dya_x_y", "dye_a b", strings.Repeat("dye_", 100)} {
		if err := s.SubmitToken(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := s.SubmitToken(" dye_tok_1\n"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.PendingToken(); tok != "dye_tok_1" {
		t.Fatalf("pending %q", tok)
	}
	// A newer handover is not removed by clearing the older token.
	if err := s.SubmitToken("dye_tok_2"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearPendingToken("dye_tok_1"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.PendingToken(); tok != "dye_tok_2" {
		t.Fatalf("pending after stale clear %q", tok)
	}
	if err := s.ClearPendingToken("dye_tok_2"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.PendingToken(); tok != "" {
		t.Fatalf("pending after clear %q", tok)
	}
	for i := range maxUsedTokens + 5 {
		if err := s.MarkTokenUsed("dye_used_" + string(rune('a'+i%26)) + strings.Repeat("x", i)); err != nil {
			t.Fatal(err)
		}
	}
	if used, _ := s.TokenUsed("dye_used_a"); used {
		t.Fatal("oldest used token not evicted")
	}
	last := "dye_used_" + string(rune('a'+(maxUsedTokens+4)%26)) + strings.Repeat("x", maxUsedTokens+4)
	if used, _ := s.TokenUsed(last); !used {
		t.Fatal("recent used token forgotten")
	}
	if TokenID("dye_x") == "" || TokenID("dye_x") == TokenID("dye_y") || strings.Contains(TokenID("dye_secret"), "secret") {
		t.Fatal("token IDs")
	}
	st := EnrollStatus{TokenID: TokenID("dye_x"), Status: EnrollFailed, Code: "unauthenticated"}
	if err := s.WriteEnrollStatus(st); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.EnrollStatus(); got == nil || got.Code != "unauthenticated" {
		t.Fatalf("status %+v", got)
	}
}
