package updates_test

import (
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/updates"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// TestDockerManagerProjectIsNeverUpdated (#32 × #20): Docker Manager's own Compose
// project (it runs a Docker Agent or manager container) cannot become
// an update policy's target, and a policy that already targets it (the
// project became Docker Manager's after the policy was created) is refused at
// run time with a clear reason, before any job exists. The agent refuses
// such an update.run too (internal/agent/protect TestGuardStacks).
func TestDockerManagerProjectIsNeverUpdated(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	web := h.ref("acme/web:1.4")
	st := h.deployStack("st-dy", "docker-manager", "services:\n  web:\n    image: "+web+"\n", nil, []stackService{{name: "web", image: web, running: true}})
	h.engine.AddContainer(engine.ContainerSpec{Name: "docker-manager-docker-agent-1", Image: web, Labels: map[string]string{
		lifecycle.ComposeProjectLabel: "docker-manager", lifecycle.ComposeServiceLabel: "docker-agent", protocol.LabelRole: "agent"}}, true)

	_, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "docker-manager", TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	if updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("policy for Docker Manager's own project: %v", err)
	}

	now := h.clk.Now().UTC()
	p := domain.UpdatePolicy{ID: ids.New(), EnvironmentID: env, Name: "older policy", TargetType: domain.UpdateTargetStack, TargetID: st.ID,
		Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}, Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"},
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertUpdatePolicy(h.ctx, h.db, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("run of a policy on Docker Manager's own project: %v", err)
	}
	if n := h.updateRuns(); n != 0 {
		t.Fatalf("%d update.run jobs for Docker Manager's own project", n)
	}
}
