package movelock

import (
	"slices"
	"testing"
)

func TestLockLevels(t *testing.T) {
	var nilLock *Lock
	if nilLock.ReadOnly() || nilLock.AgentsRefused() || nilLock.Level() != Open {
		t.Fatal("a nil lock must be open")
	}
	nilLock.Set(AgentsRefused) // no panic
	nilLock.OnChange(func(Level) {})

	l := New()
	var seen []Level
	l.OnChange(func(v Level) { seen = append(seen, v) })
	if l.ReadOnly() || l.AgentsRefused() {
		t.Fatal("a new lock must be open")
	}
	l.Set(ReadOnly)
	if !l.ReadOnly() || l.AgentsRefused() {
		t.Fatalf("read-only: ReadOnly=%v AgentsRefused=%v", l.ReadOnly(), l.AgentsRefused())
	}
	l.Set(ReadOnly) // unchanged: no notification
	l.Set(AgentsRefused)
	if !l.ReadOnly() || !l.AgentsRefused() {
		t.Fatal("agents refused implies read-only")
	}
	l.Set(Open)
	if l.ReadOnly() {
		t.Fatal("open again")
	}
	if want := []Level{ReadOnly, AgentsRefused, Open}; !slices.Equal(seen, want) {
		t.Fatalf("notifications = %v, want %v", seen, want)
	}
	if AgentsRefused.String() != "agents_refused" || ReadOnly.String() != "read_only" || Open.String() != "open" {
		t.Fatal("level names")
	}
}
