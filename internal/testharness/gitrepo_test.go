package testharness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not available: the Git fixture needs it (CI runners have it)")
	}
}

func TestSeedGitFixturesForDumbHTTP(t *testing.T) {
	requireGit(t)
	dst := t.TempDir()
	names, err := SeedGitFixtures(t.Context(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "private,public" {
		t.Fatalf("names = %v", names)
	}
	refs := map[string]string{}
	for _, n := range names {
		bare := filepath.Join(dst, n+".git")
		data, err := os.ReadFile(filepath.Join(bare, "info", "refs"))
		if err != nil {
			t.Fatalf("%s: info/refs: %v", n, err)
		}
		if !strings.Contains(string(data), "\trefs/heads/"+GitDefaultBranch+"\n") {
			t.Errorf("%s: info/refs = %q", n, data)
		}
		refs[n] = string(data)
		packs, err := os.ReadFile(filepath.Join(bare, "objects", "info", "packs"))
		if err != nil || !strings.Contains(string(packs), ".pack") {
			t.Errorf("%s: objects/info/packs = %q, %v", n, packs, err)
		}
		head, err := os.ReadFile(filepath.Join(bare, "HEAD"))
		if err != nil || strings.TrimSpace(string(head)) != "ref: refs/heads/"+GitDefaultBranch {
			t.Errorf("%s: HEAD = %q, %v", n, head, err)
		}
	}

	// Reproducible: seeding again yields the same commit IDs.
	again := t.TempDir()
	if _, err := SeedGitFixtures(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	for n, want := range refs {
		got, err := os.ReadFile(filepath.Join(again, n+".git", "info", "refs"))
		if err != nil || string(got) != want {
			t.Errorf("%s: reseeded refs %q != %q (%v)", n, got, want, err)
		}
	}
}

func TestSeedBareRepoRejectsSymlinks(t *testing.T) {
	requireGit(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	err := SeedBareRepo(t.Context(), src, filepath.Join(t.TempDir(), "x.git"))
	if err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatalf("err = %v", err)
	}
}
