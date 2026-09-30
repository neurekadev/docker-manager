package backup

import "testing"

// TestPrunePendingMark: a location's mark is set and cleared on its own;
// without a directory nothing is kept.
func TestPrunePendingMark(t *testing.T) {
	dir := t.TempDir()
	if PrunePending(dir, "r1", "env:e1") {
		t.Fatal("pending before any prune")
	}
	if err := MarkPrunePending(dir, "r1", "env:e1", true); err != nil {
		t.Fatal(err)
	}
	if !PrunePending(dir, "r1", "env:e1") || PrunePending(dir, "r1", "env:e2") || PrunePending(dir, "r2", "env:e1") {
		t.Error("the mark is not per location")
	}
	if err := MarkPrunePending(dir, "r1", "env:e1", false); err != nil || PrunePending(dir, "r1", "env:e1") {
		t.Errorf("clear: %v", err)
	}
	if err := MarkPrunePending(dir, "r1", "env:e1", false); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	if err := MarkPrunePending("", "r1", "env:e1", true); err != nil || PrunePending("", "r1", "env:e1") {
		t.Error("kept a mark without a directory")
	}
}
