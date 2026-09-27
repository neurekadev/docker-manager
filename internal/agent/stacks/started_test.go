package stacks

import (
	"testing"
	"time"
)

// TestStartedAny: a deploy counts as deployed only when a container's
// uptime reset (a new container that runs, or one that started again).
func TestStartedAny(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	cases := []struct {
		name          string
		before, after map[string]time.Time
		want          bool
	}{
		{"nothing changed", map[string]time.Time{"a": t0}, map[string]time.Time{"a": t0}, false},
		{"stopped one removed", map[string]time.Time{"a": t0, "b": {}}, map[string]time.Time{"a": t0}, false},
		{"created, not started", map[string]time.Time{"a": t0}, map[string]time.Time{"a": t0, "b": {}}, false},
		{"recreated", map[string]time.Time{"a": t0}, map[string]time.Time{"b": t1}, true},
		{"restarted", map[string]time.Time{"a": t0}, map[string]time.Time{"a": t1}, true},
		{"started a stopped one", map[string]time.Time{"a": {}}, map[string]time.Time{"a": t1}, true},
	}
	for _, tc := range cases {
		if got := startedAny(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: startedAny = %v, want %v", tc.name, got, tc.want)
		}
	}
}
