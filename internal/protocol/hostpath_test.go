package protocol

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestIsAbsHostPath: agents report slash-separated host paths; Linux paths
// are absolute everywhere, drive paths only where the platform has drives
// (tests on Windows).
func TestIsAbsHostPath(t *testing.T) {
	for _, p := range []string{"/var/lib/docker/volumes", "/"} {
		if !IsAbsHostPath(p) {
			t.Errorf("%q should be absolute", p)
		}
	}
	for _, p := range []string{"", "var/lib", "./x", "C:"} {
		if IsAbsHostPath(p) {
			t.Errorf("%q should not be absolute", p)
		}
	}
	drive := "C:/Users/dev/devstack/stacks"
	if got, want := IsAbsHostPath(drive), runtime.GOOS == "windows"; got != want {
		t.Errorf("IsAbsHostPath(%q) = %v on %s", drive, got, runtime.GOOS)
	}
	if IsAbsHostPath(drive) != filepath.IsAbs(filepath.FromSlash(drive)) {
		t.Error("drive paths follow the platform")
	}
	// The capabilities check accepts exactly these roots.
	caps := CapabilitiesPayload{AgentVersion: "1", Protocols: []string{Version}, OS: "linux", Arch: "amd64",
		Engine:    EngineInfo{ID: "e", APIVersion: "1.47"},
		Transport: TransportInfo{ManagerURL: "https://docker.example"},
		Commands:  []string{}, Requests: []string{}, Streams: []string{},
		Roots: []Root{{Kind: RootStacks, Path: "/var/lib/docker/volumes/dockyard_stacks/_data", Watch: WatchInotify}}}
	if err := caps.Validate(); err != nil {
		t.Fatalf("linux root refused: %v", err)
	}
	caps.Roots[0].Path = "relative/stacks"
	if err := caps.Validate(); err == nil {
		t.Fatal("a relative root was accepted")
	}
}
