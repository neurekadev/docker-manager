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
// exactly when it executes the kind (the manager refuses Recreate for
// other agents).
func TestContainerRecreateFeature(t *testing.T) {
	recreate := jobexec.Executor{Kind: jobspec.ContainerRecreate,
		Steps: map[string]jobexec.StepFunc{"recreate": func(context.Context, *jobexec.StepContext) error { return nil }}}
	for _, with := range []bool{false, true} {
		var execs []jobexec.Executor
		if with {
			execs = append(execs, recreate)
		}
		a, err := New(Options{Config: testConfig(filepath.Join(t.TempDir(), "state")), Logger: testutil.Logger(t), Clock: testutil.FakeClock(),
			Geteuid: func() int { return 0 }, Executors: execs})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := a.CapabilitiesPayload()
		if got := slices.Contains(p.Features, protocol.FeatureContainerRecreate); got != with {
			t.Errorf("executor %v: features %v", with, p.Features)
		}
	}
}
