package runtime

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestContainerRecreateFeature: the agent announces container.recreate
// with the command (the manager refuses Recreate for other agents). Its
// resources executors always include it, a test executor of the kind too.
func TestContainerRecreateFeature(t *testing.T) {
	recreate := jobexec.Executor{Kind: jobspec.ContainerRecreate,
		Steps: map[string]jobexec.StepFunc{"recreate": func(context.Context, *jobexec.StepContext) error { return nil }}}
	for _, execs := range [][]jobexec.Executor{nil, {recreate}} {
		a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
			Geteuid: func() int { return 0 }, Executors: execs})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := a.CapabilitiesPayload()
		if !slices.Contains(p.Commands, string(jobspec.ContainerRecreate)) || !slices.Contains(p.Features, protocol.FeatureContainerRecreate) {
			t.Errorf("executors %d: commands %v features %v", len(execs), p.Commands, p.Features)
		}
	}
}
