package updates_test

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestDockerManagerProjectCanBeUpdated (#32 × #20): Docker Manager updates
// itself. Its own Compose project (it runs a Docker Agent or manager
// container) can be an update policy's target and a run is not refused;
// the agent hands its own container to a helper container
// (internal/agent/selfupdate).
func TestDockerManagerProjectCanBeUpdated(t *testing.T) {
	h := newHarness(t)
	h.publish("acme/web", "1.4", "")
	web := h.ref("acme/web:1.4")
	st := h.deployStack("st-dy", "docker-manager", "services:\n  web:\n    image: "+web+"\n", nil, []stackService{{name: "web", image: web, running: true}})
	h.engine.AddContainer(engine.ContainerSpec{Name: "docker-manager-docker-agent-1", Image: web, Labels: map[string]string{
		lifecycle.ComposeProjectLabel: "docker-manager", lifecycle.ComposeServiceLabel: "docker-agent", protocol.LabelRole: "agent"}}, true)

	p, err := h.svc.Create(h.ctx, updates.NewPolicy{EnvironmentID: env, Name: "docker-manager", TargetType: domain.UpdateTargetStack, TargetID: st.ID})
	if err != nil {
		t.Fatalf("policy for Docker Manager's own project: %v", err)
	}
	if _, err := h.svc.Run(h.ctx, updates.RunRequest{Principal: authz.Service(), PolicyID: p.ID}); updateCode(err) == domain.UpdateErrTargetIneligible {
		t.Fatalf("run of a policy on Docker Manager's own project refused: %v", err)
	}
}
