package engine

import (
	"testing"

	"github.com/moby/moby/client"

	"github.com/neurekadev/dockyard/internal/testharness"
)

// The Engine matrix's "minimum" entry is the Engine DockYard supports at
// least; it must speak exactly MinSupportedAPIVersion, and every entry must
// be accepted by the adapter and the pinned Moby client.
func TestMatrixMinimumMatchesAdapter(t *testing.T) {
	m, err := testharness.LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	minimum, err := m.Lookup("minimum")
	if err != nil {
		t.Fatal(err)
	}
	if !minimum.HasRole(testharness.RoleMinimum) || minimum.APIVersion != MinSupportedAPIVersion {
		t.Fatalf("matrix minimum %s (API %s), adapter minimum API %s", minimum.Version, minimum.APIVersion, MinSupportedAPIVersion)
	}
	for _, e := range m.Engines {
		if testharness.CompareVersions(e.APIVersion, MinSupportedAPIVersion) < 0 || testharness.CompareVersions(e.APIVersion, client.MinAPIVersion) < 0 {
			t.Errorf("matrix Engine %s (API %s) is below the supported minimum", e.Version, e.APIVersion)
		}
	}
}
