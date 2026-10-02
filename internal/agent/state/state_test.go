package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// TestManagerGeneration: nothing recorded reads as 0; the value is stored
// atomically (0600) and read back; a corrupt or negative file reads as 0
// with an error (the caller warns) and is overwritten by the next save.
func TestManagerGeneration(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if g, err := s.ManagerGeneration(); g != 0 || err != nil {
		t.Fatalf("fresh state: %d %v", g, err)
	}
	if err := s.SaveManagerGeneration(3); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, ManagerFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("manager file mode %v %v", fi.Mode(), err)
		}
	}
	s2, _ := Open(dir)
	if g, err := s2.ManagerGeneration(); g != 3 || err != nil {
		t.Fatalf("read back %d %v", g, err)
	}
	if err := s.SaveManagerGeneration(-1); err == nil {
		t.Fatal("negative generation saved")
	}
	for _, corrupt := range []string{"{not json", `{"generation":-2}`} {
		if err := os.WriteFile(filepath.Join(dir, ManagerFile), []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		if g, err := s.ManagerGeneration(); g != 0 || err == nil {
			t.Fatalf("corrupt %q: %d %v", corrupt, g, err)
		}
	}
	if err := s.SaveManagerGeneration(4); err != nil {
		t.Fatal(err)
	}
	if g, err := s.ManagerGeneration(); g != 4 || err != nil {
		t.Fatalf("after overwrite %d %v", g, err)
	}
}

// TestManagerRedirect (#35, manager moves): a redirect is written with its
// generation in one step and read back after a restart; it needs a higher
// generation (the same redirect again is a no-op); generation writes
// never lower the record and keep the redirect; clearing it keeps the
// generation.
func TestManagerRedirect(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	r := ManagerRedirect{URL: "http://192.0.2.10:8080", Replaces: "https://docker.example.com", At: at}
	if err := s.SaveManagerGeneration(2); err != nil {
		t.Fatal(err)
	}
	for _, g := range []int64{1, 2} {
		if err := s.SaveManagerRedirect(g, r); !errors.Is(err, ErrGenerationNotNewer) {
			t.Fatalf("generation %d: %v", g, err)
		}
	}
	if ms, _ := s.ManagerState(); ms.Redirect != nil || ms.Generation != 2 {
		t.Fatalf("refused redirect written: %+v", ms)
	}
	if err := s.SaveManagerRedirect(3, r); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, ManagerFile))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("manager file mode %v %v", fi.Mode(), err)
		}
	}
	s2, _ := Open(dir)
	ms, err := s2.ManagerState()
	if err != nil || ms.Generation != 3 || ms.Redirect == nil || *ms.Redirect != r {
		t.Fatalf("read back %+v %v", ms, err)
	}
	if err := s.SaveManagerRedirect(3, r); err != nil {
		t.Fatalf("the same redirect again: %v", err)
	}
	other := r
	other.URL = "http://192.0.2.20:8080"
	if err := s.SaveManagerRedirect(3, other); !errors.Is(err, ErrGenerationNotNewer) {
		t.Fatalf("another address at the same generation: %v", err)
	}
	for _, bad := range []ManagerRedirect{{Replaces: "https://m"}, {URL: "http://m"}} {
		if err := s.SaveManagerRedirect(9, bad); err == nil {
			t.Fatalf("incomplete redirect %+v saved", bad)
		}
	}
	if err := s.SaveManagerRedirect(0, r); err == nil {
		t.Fatal("generation 0 saved")
	}

	// Generation writes keep the redirect and never lower the record.
	if err := s.SaveManagerGeneration(2); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveManagerGeneration(4); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ManagerState(); ms.Generation != 4 || ms.Redirect == nil || *ms.Redirect != r {
		t.Fatalf("after generation writes %+v", ms)
	}

	if err := s.ClearManagerRedirect(); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ManagerState(); ms.Generation != 4 || ms.Redirect != nil {
		t.Fatalf("after clearing %+v", ms)
	}
	if err := s.ClearManagerRedirect(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}

	// An incomplete stored redirect is corrupt; the next redirect replaces it.
	if err := os.WriteFile(filepath.Join(dir, ManagerFile), []byte(`{"generation":5,"redirect":{"url":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.ManagerState(); err == nil || ms.Generation != 0 || ms.Redirect != nil {
		t.Fatalf("corrupt redirect: %+v %v", ms, err)
	}
	if err := s.SaveManagerRedirect(1, r); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.ManagerState(); err != nil || ms.Generation != 1 || *ms.Redirect != r {
		t.Fatalf("after replacing the corrupt record %+v %v", ms, err)
	}
}

// TestReplaceManagerRedirect: the manager the agent follows changes its
// address at the recorded generation only (a lower or higher one changes
// nothing), nil forgets the redirect and keeps the generation, and a
// corrupt record is refused rather than replaced.
func TestReplaceManagerRedirect(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	moved := ManagerRedirect{URL: "http://192.0.2.10:8080", Replaces: "http://docker-manager:8080", At: at}
	if err := s.SaveManagerRedirect(3, moved); err != nil {
		t.Fatal(err)
	}
	secure := ManagerRedirect{URL: "https://docker.example.com", Replaces: "http://docker-manager:8080", At: at.Add(time.Hour)}
	for _, g := range []int64{0, 2, 4} {
		if err := s.ReplaceManagerRedirect(g, &secure); !errors.Is(err, ErrGenerationNotNewer) {
			t.Fatalf("generation %d: %v", g, err)
		}
	}
	if ms, _ := s.ManagerState(); ms.Generation != 3 || *ms.Redirect != moved {
		t.Fatalf("refused replacement written: %+v", ms)
	}
	if err := s.ReplaceManagerRedirect(3, &ManagerRedirect{URL: "https://m"}); err == nil {
		t.Fatal("incomplete redirect saved")
	}
	if err := s.ReplaceManagerRedirect(3, &secure); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(dir)
	if ms, err := s2.ManagerState(); err != nil || ms.Generation != 3 || ms.Redirect == nil || *ms.Redirect != secure {
		t.Fatalf("read back %+v %v", ms, err)
	}
	if err := s.ReplaceManagerRedirect(3, nil); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ManagerState(); ms.Generation != 3 || ms.Redirect != nil {
		t.Fatalf("after forgetting %+v", ms)
	}

	if err := os.WriteFile(filepath.Join(dir, ManagerFile), []byte(`{"generation":3,"redirect":{"url":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceManagerRedirect(3, &secure); err == nil || errors.Is(err, ErrGenerationNotNewer) {
		t.Fatalf("corrupt record: %v", err)
	}
}
