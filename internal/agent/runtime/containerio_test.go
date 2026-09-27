package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestContainerIOAnnouncesShellLookup (#8): with ContainerIO the agent
// serves container.exec.create and announces exec.shell; an explicitly
// configured exec handler wins and the feature is not announced for it.
func TestContainerIOAnnouncesShellLookup(t *testing.T) {
	newAgent := func(reqs map[string]session.RequestHandler) *Agent {
		t.Helper()
		a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
			Geteuid: func() int { return 0 }, ContainerIO: true, Requests: reqs})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newAgent(nil)
	if a.opts.Requests[protocol.ReqContainerExecCreate] == nil {
		t.Fatal("container.exec.create not served")
	}
	if p, _ := a.CapabilitiesPayload(); !slices.Contains(p.Features, protocol.FeatureExecShell) {
		t.Fatalf("features %v lack %s", p.Features, protocol.FeatureExecShell)
	}

	own := func(context.Context, json.RawMessage) (any, error) { return "own", nil }
	a = newAgent(map[string]session.RequestHandler{protocol.ReqContainerExecCreate: own})
	if p, _ := a.CapabilitiesPayload(); slices.Contains(p.Features, protocol.FeatureExecShell) {
		t.Fatalf("features %v announce %s for a custom handler", p.Features, protocol.FeatureExecShell)
	}
}
