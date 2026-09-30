package stacks

import (
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestPullOnlyNeverDeploys: stack.pull moves the tags to the newer images
// and reports those services; no container is created, recreated,
// stopped or started.
func TestPullOnlyNeverDeploys(t *testing.T) {
	e := newRenameEnv(t, renameYAML)
	ctx := testutil.Context(t)
	web, _ := e.eng.Container("app-web-1")
	e.eng.Publish("nginx:1.27", "sha256:"+strings.Repeat("a", 64))
	e.c.onPull = func(p *compose.Project) {
		for _, svc := range p.Services {
			_, _ = e.eng.PullImage(ctx, svc.Image, engine.PullOptions{})
		}
	}
	e.c.onCreate = func(*compose.Project, compose.CreateOptions) error {
		t.Error("stack.pull created containers")
		return nil
	}
	calls := len(e.eng.Calls())

	res, out := run(t, e.svc, jobspec.StackPull, protocol.StackJobInput{Stack: ref("app")})
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("pull: %+v", res)
	}
	if !slices.Equal(out.Pulled, []string{"web"}) {
		t.Errorf("pulled %v, want [web]", out.Pulled)
	}
	if !slices.Equal(e.c.calls, []string{"pull:app"}) {
		t.Errorf("compose calls %v", e.c.calls)
	}
	for _, op := range e.eng.Calls()[calls:] {
		if strings.HasPrefix(op, "container.") && op != "container.inspect" && op != "container.list" {
			t.Errorf("container changed: %s", op)
		}
	}
	after, _ := e.eng.Container("app-web-1")
	if after.Details.ID != web.Details.ID || !after.Details.State.Running || after.Details.ImageID != web.Details.ImageID {
		t.Errorf("web container changed: %+v", after.Details)
	}
}
