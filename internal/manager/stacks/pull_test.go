package stacks_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestPullMarksNewerImagesWithoutDeploying: a stack.pull (agents announcing
// stack.pull only) selects registry connections like a deploy, changes no
// deploy state, and marks the services whose reference now names another
// image than the one they run; a pull that finds the running image again
// clears the mark, and so does the next deploy.
func TestPullMarksNewerImagesWithoutDeploying(t *testing.T) {
	h := newHarness(t)
	h.regs.byHost["registry.example:5000/"] = "reg-1"
	st := h.create("shop", shopYAML, shopEnv)
	if _, err := h.svc.Pull(h.ctx, alice, st, domain.StackJobRequest{}); stackErrCode(err) != domain.StackErrEnvironmentUnsupported {
		t.Fatalf("agent without stack.pull: %v", err)
	}
	h.agents.mu.Lock()
	h.agents.features = map[string]bool{protocol.FeatureStackPull: true}
	h.agents.mu.Unlock()

	snap := protocol.NewSourceSnapshot([]protocol.SourceFile{{Path: "compose.yaml", Content: []byte(shopYAML)}, {Path: ".env", Content: []byte(shopEnv)}})
	running := []protocol.AppliedImage{{Service: "db", Image: "registry.example:5000/db:16", ImageID: "sha256:db1"},
		{Service: "web", Image: "nginx:1.27", ImageID: "sha256:web1"}}
	h.deploy(st)
	h.finishWith("succeeded", protocol.StackJobOutput{Sources: &snap, Images: running})
	deployed := h.get(st.ID)

	h.clk.Advance(time.Hour)
	j, err := h.svc.Pull(h.ctx, alice, deployed, domain.StackJobRequest{IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var in protocol.StackJobInput
	_ = json.Unmarshal(j.Input, &in)
	if j.Kind != jobspec.StackPull || in.Stack.ProjectName != "shop" || !slices.Equal(in.RegistryConnections, []string{"reg-1"}) {
		t.Fatalf("pull job %s input %+v", j.Kind, in)
	}
	h.finishWith("succeeded", protocol.StackJobOutput{Pulled: []string{"db"},
		Images: []protocol.AppliedImage{{Service: "db", Image: "registry.example:5000/db:16", ImageID: "sha256:db2", Digest: "sha256:d2"},
			{Service: "web", Image: "nginx:1.27", ImageID: "sha256:web1"}}})
	if j = h.job(j.ID); j.State != domain.JobSucceeded {
		t.Fatalf("pull %s", j.State)
	}
	cur := h.get(st.ID)
	if cur.Status != deployed.Status || cur.Applied.ID != deployed.Applied.ID || !cur.AppliedAt.Equal(*deployed.AppliedAt) {
		t.Errorf("a pull changed the deploy state: %s %+v %v", cur.Status, cur.Applied, cur.AppliedAt)
	}
	byService := map[string]domain.StackImage{}
	for _, i := range cur.Images {
		byService[i.Service] = i
	}
	if db := byService["db"]; db.ImageID != "sha256:db1" || db.PulledImageID != "sha256:db2" || db.PulledDigest != "sha256:d2" || db.PulledAt == nil {
		t.Errorf("db after the pull %+v", db)
	}
	if web := byService["web"]; web.PulledImageID != "" {
		t.Errorf("web marked although unchanged: %+v", web)
	}
	views := h.svc.ImageStatus(cur)
	if i := slices.IndexFunc(views, func(v domain.StackImageView) bool { return v.Service == "db" }); i < 0 || views[i].PulledImageID != "sha256:db2" {
		t.Errorf("image status %+v", views)
	}

	// The running image again (the tag moved back): the mark goes.
	if _, err := h.svc.Pull(h.ctx, alice, cur, domain.StackJobRequest{}); err != nil {
		t.Fatal(err)
	}
	h.finishWith("succeeded", protocol.StackJobOutput{Pulled: []string{"db"}, Images: running})
	for _, i := range h.get(st.ID).Images {
		if i.PulledImageID != "" {
			t.Errorf("mark kept: %+v", i)
		}
	}
	// Offline: environment_offline.
	h.agents.setOnline(false)
	if _, err := h.svc.Pull(h.ctx, alice, cur, domain.StackJobRequest{}); stackErrCode(err) != domain.StackErrOffline {
		t.Errorf("offline: %v", err)
	}
}
