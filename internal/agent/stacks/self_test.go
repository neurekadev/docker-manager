package stacks

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/selfupdate"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
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
    image: code.neureka.dev/docker-manager/docker-manager:edge
  docker-agent:
    image: code.neureka.dev/docker-manager/docker-agent:edge
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
	if len(e.c.upServices) != 1 || !slices.Equal(e.c.upServices[0], []string{"docker-manager", "caddy"}) {
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
