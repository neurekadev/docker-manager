package updates_test

import (
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/lifecycle"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/manager/updates"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// TestDockYardProjectIsNeverUpdated (#32 × #20): DockYard's own Compose
// project (it runs a DockYard agent or manager container) cannot become
// an update policy's target, and a policy that already targets it (the
// project became DockYard's after the policy was created) is refused at
// run time with a clear reason, before any job exists. The agent refuses
// such an update.run too (internal/agent/protect TestGuardStacks).
func TestDockYardProjectIsNeverUpdated(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	web := h.ref("acme/web:1.4")
	st := h.deployStack("st-dy", "dockyard", "services:\n  web:\n    image: "+web+"\n", nil, []stackService{{name: "web", image: web, running: true}})
	h.engine.AddContainer(engine.ContainerSpec{Name: "dockyard-dockyard-agent-1", Image: web, Labels: map[string]string{
		lifecycle.ComposeProjectLabel: "dockyard", lifecycle.ComposeServiceLabel: "dockyard-agent", protocol.LabelRole: "agent"}}, true)

	_, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "dockyard", TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	if updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("policy for DockYard's own project: %v", err)
	}

	now := h.clk.Now().UTC()
	p := domain.UpdatePolicy{ID: ids.New(), EnvironmentID: env, Name: "older policy", TargetType: domain.UpdateTargetStack, TargetID: st.ID,
		Check: domain.UpdateSchedule{Cron: "0 3 * * *", TimeZone: "UTC"}, Run: domain.UpdateSchedule{Cron: "0 4 * * *", TimeZone: "UTC"},
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertUpdatePolicy(h.ctx, h.db, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) != domain.UpdateErrTargetIneligible {
		t.Fatalf("run of a policy on DockYard's own project: %v", err)
	}
	if n := h.updateRuns(); n != 0 {
		t.Fatalf("%d update.run jobs for DockYard's own project", n)
	}
}
