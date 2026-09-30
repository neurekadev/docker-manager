package backups

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/templates"
)

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// TestRestoreApplyIsRepeatable: a staged manager-state restore replaces the
// database (with its WAL) and the secret key, keeps both, survives a crash
// between its steps, and leaves the applied marker for the completion.
func TestRestoreApplyIsRepeatable(t *testing.T) {
	data := t.TempDir()
	dbPath := filepath.Join(data, "docker-manager.db")
	keyFile := filepath.Join(data, "secret.key")
	oldKey, err := secrets.CreateKeyFile(keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, dbPath, "fresh database")
	writeTestFile(t, dbPath+"-wal", "fresh wal")
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	// Nothing staged: nothing happens.
	if mk, err := ApplyPendingRestore(data, dbPath, keyFile, now); err != nil || mk != nil {
		t.Fatalf("nothing staged: %v %v", mk, err)
	}

	pending := filepath.Join(data, PendingRestoreDir)
	writeTestFile(t, filepath.Join(pending, restoredDBFile), "restored database")
	restoredKey, err := secrets.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.ReplaceKeyFile(filepath.Join(pending, restoredKeyFile), restoredKey); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(pending, restoreMarkerFile), RestoreMarker{Format: RestoreMarkerFormat, Version: 1, JobID: "j1",
		SetID: "s1", StagedAt: now}); err != nil {
		t.Fatal(err)
	}

	// A crash after the database moved: the key file cannot be written.
	blocked := filepath.Join(data, "not-a-dir")
	writeTestFile(t, blocked, "x")
	if _, err := ApplyPendingRestore(data, dbPath, filepath.Join(blocked, "secret.key"), now); err == nil {
		t.Fatal("the key replacement did not fail")
	}
	if readTestFile(t, dbPath) != "restored database" {
		t.Fatal("the database step did not run")
	}

	// The next start finishes it.
	mk, err := ApplyPendingRestore(data, dbPath, keyFile, now.Add(time.Minute))
	if err != nil || mk == nil || mk.AppliedAt == nil || mk.SetID != "s1" {
		t.Fatalf("apply: %+v %v", mk, err)
	}
	if readTestFile(t, dbPath) != "restored database" || readTestFile(t, dbPath+"-wal") != "<missing>" {
		t.Error("database not in place")
	}
	got, err := secrets.LoadKeyFile(keyFile)
	if err != nil || got.ID() != restoredKey.ID() {
		t.Errorf("key file: %v", err)
	}
	if readTestFile(t, filepath.Join(mk.PreRestoreDir, "docker-manager.db")) != "fresh database" ||
		readTestFile(t, filepath.Join(mk.PreRestoreDir, "docker-manager.db-wal")) != "fresh wal" {
		t.Error("the replaced database was not kept")
	}
	kept, err := secrets.LoadKeyFile(filepath.Join(mk.PreRestoreDir, "secret.key"))
	if err != nil || kept.ID() != oldKey.ID() {
		t.Errorf("the replaced key was not kept: %v", err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Error("the staging directory is left behind")
	}
	applied, err := AppliedRestore(data)
	if err != nil || applied == nil || applied.JobID != "j1" {
		t.Fatalf("applied marker: %+v %v", applied, err)
	}
	// Applying again is a no-op; finishing removes the marker.
	if mk, err := ApplyPendingRestore(data, dbPath, keyFile, now); err != nil || mk != nil {
		t.Fatalf("second apply: %v %v", mk, err)
	}
	if err := FinishedRestore(data); err != nil {
		t.Fatal(err)
	}
	if applied, err := AppliedRestore(data); err != nil || applied != nil {
		t.Fatalf("after finishing: %+v %v", applied, err)
	}
}

// TestRestoreApplyPutsBackTemplateDrafts: the snapshot's template drafts
// replace the current ones, which are kept in the pre-restore directory.
func TestRestoreApplyPutsBackTemplateDrafts(t *testing.T) {
	data := t.TempDir()
	dbPath := filepath.Join(data, "docker-manager.db")
	keyFile := filepath.Join(data, "secret.key")
	if _, err := secrets.CreateKeyFile(keyFile, nil); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, dbPath, "fresh database")
	current := templates.DraftsDir(data)
	writeTestFile(t, filepath.Join(current, "tcurrent", "draft", "compose.yaml"), "current")

	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "trestored", "draft", "compose.yaml"), "restored")
	var drafts bytes.Buffer
	if err := templates.WriteDrafts(&drafts, src); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(data, PendingRestoreDir)
	writeTestFile(t, filepath.Join(pending, restoredDBFile), "restored database")
	writeTestFile(t, filepath.Join(pending, restoredDraftsFile), drafts.String())
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if err := writeJSONFile(filepath.Join(pending, restoreMarkerFile), RestoreMarker{Format: RestoreMarkerFormat, Version: 1, JobID: "j1",
		SetID: "s1", StagedAt: now}); err != nil {
		t.Fatal(err)
	}

	mk, err := ApplyPendingRestore(data, dbPath, keyFile, now)
	if err != nil || mk == nil {
		t.Fatalf("apply: %+v %v", mk, err)
	}
	if readTestFile(t, filepath.Join(current, "trestored", "draft", "compose.yaml")) != "restored" {
		t.Error("the snapshot's drafts are not in place")
	}
	if readTestFile(t, filepath.Join(current, "tcurrent", "draft", "compose.yaml")) != "<missing>" {
		t.Error("a replaced draft is left in place")
	}
	if readTestFile(t, filepath.Join(mk.PreRestoreDir, "templates", "tcurrent", "draft", "compose.yaml")) != "current" {
		t.Error("the replaced drafts were not kept")
	}
}

// TestRestoreApplyRefusesAnUnreadableMarker: a damaged staged restore stops
// the startup with instructions instead of guessing.
func TestRestoreApplyRefusesAnUnreadableMarker(t *testing.T) {
	data := t.TempDir()
	writeTestFile(t, filepath.Join(data, PendingRestoreDir, restoreMarkerFile), "{not json")
	if _, err := ApplyPendingRestore(data, filepath.Join(data, "docker-manager.db"), filepath.Join(data, "secret.key"), time.Now()); err == nil {
		t.Fatal("an unreadable marker was accepted")
	}
}

func TestSnapshotPathOfRecordedPaths(t *testing.T) {
	for in, want := range map[string]string{
		"/data/backup-staging/j/docker-manager-state": "/data/backup-staging/j/docker-manager-state/state.json",
		"C:/Users/x/docker-manager-state":             "/C/Users/x/docker-manager-state/state.json",
	} {
		if got := stateFile([]string{in}, "state.json"); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
