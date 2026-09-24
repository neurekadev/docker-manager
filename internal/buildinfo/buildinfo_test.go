package buildinfo

import (
	"strings"
	"testing"
)

func TestGetDefaults(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origV, origC, origD })

	Version, Commit, Date = "", "abc123", "2026-09-24T00:00:00Z"
	info := Get()
	if info.Version != DefaultVersion {
		t.Fatalf("version = %q, want %q", info.Version, DefaultVersion)
	}
	if info.Commit != "abc123" || info.Date != "2026-09-24T00:00:00Z" {
		t.Fatalf("unexpected info %+v", info)
	}
	if !strings.Contains(info.String(), "abc123") {
		t.Fatalf("String() = %q", info.String())
	}
}
