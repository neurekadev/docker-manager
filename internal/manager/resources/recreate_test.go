package resources

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// recreateAgents is an agentRequester whose agent announces (or not)
// protocol.FeatureContainerRecreate.
type recreateAgents struct {
	*agentRequester
	recreate bool
}

func (f recreateAgents) EnvironmentHasFeature(_, feature string) bool {
	return feature == protocol.FeatureContainerRecreate && f.recreate
}

// TestContainerRecreate (#273): a standalone container is recreated with
// its stop timeout; containers of a managed stack (stack_managed) or of
// another Compose project, temporary containers and containers being
// removed (conflict), Docker Manager's own containers (protected) and
// agents without the feature (agent_unsupported) are refused before a job
// exists.
func TestContainerRecreate(t *testing.T) {
	svc, fe, fj, _, req := fixture(t)
	ctx := testutil.Context(t)
	p := authz.Principal{Kind: authz.KindUser, UserID: "u"}
	fe.AddContainer(engine.ContainerSpec{Name: "legacy-app-1", Image: "nginx:1.27", Labels: map[string]string{
		protocol.ComposeProjectLabel: "legacy", protocol.ComposeServiceLabel: "app", protocol.ComposeWorkingDirLabel: "/srv/legacy"}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "web" + protocol.RecreateAsideInfix + "0123456789ab", Image: "nginx:1.27"}, false)
	inspect := func(name string) protocol.ContainerDetails {
		t.Helper()
		d, err := svc.InspectContainer(ctx, "env-1", name)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	recreate := func(d protocol.ContainerDetails) string {
		_, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRecreate, d, protocol.ContainerActionInput{}, "")
		var de *domain.DockerError
		if errors.As(err, &de) {
			return de.Code
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	svc.opts.Agents = recreateAgents{req, true}
	removing := inspect("web")
	removing.State = "removing"
	own := fe.Deploy(true)
	svc.opts.ManagerContainerID = own.ManagerID
	for _, c := range []struct {
		what string
		d    protocol.ContainerDetails
		code string
	}{
		{"managed stack", inspect("shop-web-1"), domain.DockerStackManaged},
		{"unmanaged Compose project", inspect("legacy-app-1"), domain.DockerConflict},
		{"set aside", inspect("web" + protocol.RecreateAsideInfix + "0123456789ab"), domain.DockerConflict},
		{"being removed", removing, domain.DockerConflict},
		{"Docker Manager itself", protocol.ContainerDetails{ContainerSummary: protocol.ContainerSummary{ID: own.ManagerID, Name: "mgr"}},
			domain.DockerProtected},
	} {
		if got := recreate(c.d); got != c.code {
			t.Errorf("%s: %q, want %s", c.what, got, c.code)
		}
	}
	svc.opts.Agents = recreateAgents{req, false}
	if got := recreate(inspect("web")); got != domain.DockerAgentUnsupported {
		t.Fatalf("agent without the feature: %q", got)
	}
	if len(fj.jobs) != 0 {
		t.Fatal("a refused recreate enqueued a job")
	}

	svc.opts.Agents = recreateAgents{req, true}
	web := inspect("web")
	ten := 10
	j, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRecreate, web, protocol.ContainerActionInput{TimeoutSeconds: &ten}, "")
	if err != nil {
		t.Fatal(err)
	}
	var in protocol.ContainerActionInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		t.Fatal(err)
	}
	if j.Kind != jobspec.ContainerRecreate || len(j.Targets) != 1 || j.Targets[0].ID != "web" || in.ID != web.ID || in.Name != "web" ||
		in.TimeoutSeconds == nil || *in.TimeoutSeconds != 10 {
		t.Fatalf("job %+v input %+v", j, in)
	}
	hour := 3601
	if _, err := svc.ContainerAction(ctx, p, "env-1", jobspec.ContainerRecreate, web, protocol.ContainerActionInput{TimeoutSeconds: &hour}, ""); err == nil {
		t.Fatal("a timeout above an hour was accepted")
	}
}
