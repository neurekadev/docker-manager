package stacks

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/lifecycle"
	"github.com/neurekadev/docker-manager/internal/agent/selfupdate"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// fakeSelf is the agent's own service in the project "docker-manager".
type fakeSelf struct {
	own   string
	plans []selfupdate.Plan
}

func (f *fakeSelf) OwnService(_ context.Context, project string) (string, bool, error) {
	return f.own, project == "docker-manager", nil
}

func (f *fakeSelf) Schedule(p selfupdate.Plan) { f.plans = append(f.plans, p) }

const ownYAML = `services:
  docker-manager:
    image: ghcr.io/neurekadev/docker-manager:edge
  docker-agent:
    image: ghcr.io/neurekadev/docker-agent:edge
    depends_on: [docker-manager]
  caddy:
    image: caddy:2
`

// TestDeployOfDockerManagerHandsTheAgentOver (#32): a deploy of Docker
// Manager's own project converges every other service itself and hands the
// agent's own service to the helper with exactly the bytes it loaded; a
// deploy of only the agent runs no Compose up in the job; removing orphans
// when the definition dropped the agent is refused.
func TestDeployOfDockerManagerHandsTheAgentOver(t *testing.T) {
	e := newEnv(t)
	self := &fakeSelf{own: "docker-agent"}
	e.svc.opts.Self = self
	writeTree(t, filepath.Join(e.root, "docker-manager"), map[string]string{"compose.yaml": ownYAML})

	res, out := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("docker-manager"), ForceRecreate: true, TimeoutSeconds: 30})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	// compose-go lists services by name.
	if len(e.c.upServices) != 1 || !slices.Equal(e.c.upServices[0], []string{"caddy", "docker-manager"}) {
		t.Fatalf("up services %v", e.c.upServices)
	}
	if len(self.plans) != 1 {
		t.Fatalf("plans %+v", self.plans)
	}
	p := self.plans[0]
	if p.JobID != "job-1" || p.ProjectName != "docker-manager" || !slices.Equal(p.Services, []string{"docker-agent"}) ||
		!p.ForceRecreate || p.StopTimeoutSeconds != 30 || string(p.Content["compose.yaml"]) != ownYAML {
		t.Fatalf("plan %+v", p)
	}
	if !slices.ContainsFunc(out.Warnings, func(w protocol.ComposeIssue) bool { return w.Code == protocol.IssueAgentSelfUpdate }) {
		t.Fatalf("warnings %+v", out.Warnings)
	}

	e.c.upServices, self.plans = nil, nil
	res, _ = run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("docker-manager"), Services: []string{"docker-agent"}})
	if res.Outcome != jobexec.OutcomeSucceeded || len(e.c.upServices) != 0 || len(self.plans) != 1 {
		t.Fatalf("agent only: %+v up %v plans %v", res, e.c.upServices, self.plans)
	}

	// Other projects are untouched.
	e.c.upServices, self.plans = nil, nil
	writeTree(t, filepath.Join(e.root, "shop"), map[string]string{"compose.yaml": ownYAML})
	if res, _ := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("shop")}); res.Outcome != jobexec.OutcomeSucceeded ||
		len(e.c.upServices) != 1 || e.c.upServices[0] != nil || len(self.plans) != 0 {
		t.Fatalf("another project: %+v up %v plans %v", res, e.c.upServices, self.plans)
	}

	self.own = "gone"
	res, _ = run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("docker-manager"), RemoveOrphans: true})
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protection.CodeProtected {
		t.Fatalf("orphan removal of the agent: %+v", res)
	}
}

// TestDeployReportsTheHandedOverAgentsNewImage (#32): the agent's own
// service still runs its old container when the job reports; the deploy
// records the image its reference names now (the one the helper runs), so
// the stack shows no image drift for the agent after a self-update.
func TestDeployReportsTheHandedOverAgentsNewImage(t *testing.T) {
	e := newEnv(t)
	e.svc.opts.Self = &fakeSelf{own: "docker-agent"}
	writeTree(t, filepath.Join(e.root, "docker-manager"), map[string]string{"compose.yaml": ownYAML})
	lbl := func(svc string) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: "docker-manager", lifecycle.ComposeServiceLabel: svc}
	}
	const agentRef = "ghcr.io/neurekadev/docker-agent:edge"
	e.eng.containers = []engine.Container{
		{ID: "mgr", Names: []string{"/docker-manager"}, ImageID: "sha256:mgr", State: "running", Labels: lbl("docker-manager")},
		{ID: "agent", Names: []string{"/docker-agent"}, ImageID: "sha256:agent-old", State: "running", Labels: lbl("docker-agent")},
	}
	// The deploy pulled the new agent image: its tag names it now.
	e.eng.images[agentRef] = engine.ImageDetails{ID: "sha256:agent-new"}
	e.eng.images["sha256:agent-new"] = engine.ImageDetails{ID: "sha256:agent-new", OS: "linux", Architecture: "amd64",
		RepoDigests: []string{"ghcr.io/neurekadev/docker-agent@sha256:9999"}}

	res, out := run(t, e.svc, jobspec.StackDeploy, protocol.StackJobInput{Stack: ref("docker-manager")})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	img := map[string]protocol.AppliedImage{}
	for _, i := range out.Images {
		img[i.Service] = i
	}
	if a := img["docker-agent"]; a.ImageID != "sha256:agent-new" || a.Digest != "sha256:9999" || a.Platform != "linux/amd64" {
		t.Errorf("agent image %+v, want the pulled image", a)
	}
	if m := img["docker-manager"]; m.ImageID != "sha256:mgr" {
		t.Errorf("manager image %+v, want its running container's image", m)
	}
}
