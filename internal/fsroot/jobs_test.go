package fsroot

import (
	"os"
	"path/filepath"
	"testing"
)

func swapRoot(t *testing.T) (*scopeRoot, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &scopeRoot{root: root, scope: scope, key: dir}, dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func onlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries %v, want %v", got, want)
		}
	}
}

func TestSwapInReplacesDestAndRemovesTheOldOne(t *testing.T) {
	r, dir := swapRoot(t)
	writeFile(t, dir, "a/old.txt", "old")
	writeFile(t, dir, "new/inner.txt", "new")
	left, err := swapIn(r, "a", "new")
	if err != nil || left != "" {
		t.Fatalf("swapIn = %q, %v", left, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a", "inner.txt")); err != nil || string(b) != "new" {
		t.Fatalf("a/inner.txt = %q, %v", b, err)
	}
	onlyEntries(t, dir, "a")
}

func TestSwapInPutsDestBackWhenTheSwapFails(t *testing.T) {
	r, dir := swapRoot(t)
	writeFile(t, dir, "a/a/inner.txt", "inner")
	// The entry to swap in is missing: the rename fails after dest moved aside.
	left, err := swapIn(r, "a", "missing")
	if err == nil || left != "" {
		t.Fatalf("swapIn = %q, %v; want an error and dest put back", left, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a", "a", "inner.txt")); err != nil || string(b) != "inner" {
		t.Fatalf("a/a/inner.txt = %q, %v", b, err)
	}
	onlyEntries(t, dir, "a")
}
