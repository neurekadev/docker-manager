package stacks_test

import (
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// TestUnchangedDeployKeepsTheLastDeployTime: a deploy that started no
// container still records its revision and moves the applied revision,
// but the stack's last deploy time stays the one of the deploy that
// changed something. Results without the flag (older agents) move it.
func TestUnchangedDeployKeepsTheLastDeployTime(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	snap := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(shopYAML)}, {Path: ".env", Content: []byte(shopEnv)}})
	deploy := func(unchanged bool) domain.Stack {
		t.Helper()
		j := h.deploy(h.get(st.ID))
		h.finishWith("succeeded", protocol.StackJobOutput{Sources: &snap, Unchanged: unchanged,
			After: []protocol.ServiceState{{Service: "web", Containers: 1, Running: 1}}})
		if j = h.job(j.ID); j.State != domain.JobSucceeded {
			t.Fatalf("deploy %s", j.State)
		}
		return h.get(st.ID)
	}
	first := deploy(false)
	if first.AppliedAt == nil || first.Applied == nil {
		t.Fatalf("first deploy: applied %+v at %v", first.Applied, first.AppliedAt)
	}
	h.clk.Advance(time.Hour)
	same := deploy(true)
	if same.AppliedAt == nil || !same.AppliedAt.Equal(*first.AppliedAt) {
		t.Errorf("unchanged deploy moved the last deploy time: %v -> %v", first.AppliedAt, same.AppliedAt)
	}
	if same.Applied == nil || same.Applied.Seq <= first.Applied.Seq || same.Status != domain.StackDeployed {
		t.Errorf("unchanged deploy: applied %+v (was %+v) status %s", same.Applied, first.Applied, same.Status)
	}
	h.clk.Advance(time.Hour)
	older := deploy(false)
	if older.AppliedAt == nil || !older.AppliedAt.After(*first.AppliedAt) {
		t.Errorf("a deploy without the flag kept the time: %v", older.AppliedAt)
	}
}
