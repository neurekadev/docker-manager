package templates

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDraftsBackupRoundTrip(t *testing.T) {
	src := t.TempDir()
	for p, c := range map[string]string{
		"0190a6e0-0000-7000-8000-000000000001/draft/compose.yaml":    "services: {}\n",
		"0190a6e0-0000-7000-8000-000000000001/draft/config/app.conf": "x",
		"0190a6e0-0000-7000-8000-000000000002/draft/.env":            "A=1\n",
		"0190a6e0-0000-7000-8000-000000000002/draft.old/stale":       "never backed up",
	} {
		full := filepath.Join(src, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := WriteDrafts(&buf, src); err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	dst := DraftsDir(data)
	if err := os.MkdirAll(filepath.Join(dst, "old-template", "draft"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(data, "pre-restore", "templates")
	if err := RestoreDrafts(bytes.NewReader(buf.Bytes()), dst, keep); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "0190a6e0-0000-7000-8000-000000000001", "draft", "config", "app.conf")); err != nil || string(b) != "x" {
		t.Fatalf("restored file = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "0190a6e0-0000-7000-8000-000000000002", "draft", ".env")); err != nil || string(b) != "A=1\n" {
		t.Fatalf("restored .env = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "0190a6e0-0000-7000-8000-000000000002", "draft.old")); !os.IsNotExist(err) {
		t.Fatal("a leftover next to a draft was backed up")
	}
	if _, err := os.Stat(filepath.Join(keep, "old-template")); err != nil {
		t.Fatalf("the replaced drafts were not kept: %v", err)
	}
	// Applying again (a crash before the staged file was removed) keeps the
	// first copy of the originals.
	if err := RestoreDrafts(bytes.NewReader(buf.Bytes()), dst, keep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(keep, "old-template")); err != nil {
		t.Fatalf("the kept originals were lost: %v", err)
	}
	// An empty or missing templates directory is an empty archive.
	var empty bytes.Buffer
	if err := WriteDrafts(&empty, filepath.Join(data, "missing")); err != nil || empty.Len() == 0 {
		t.Fatalf("missing directory: %v", err)
	}
}
