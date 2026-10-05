package resources

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// runAgain runs a job again as a repeated attempt that resumes with the
// result output the previous attempt recorded.
func runAgain(t *testing.T, s *Service, kind domain.JobKind, input any, output json.RawMessage) protocol.ResultPayload {
	t.Helper()
	var exec jobexec.Executor
	for _, x := range s.Executors() {
		if x.Kind == kind {
			exec = x
		}
	}
	raw, _ := json.Marshal(input)
	res, err := jobexec.Run(testutil.Context(t), exec, &jobexec.State{JobID: "j", Attempt: 2, FencingToken: 2, Kind: kind, Input: raw, Output: output},
		jobexec.Options{Journal: memJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func recreateOutput(t *testing.T, res protocol.ResultPayload) protocol.ContainerRecreateOutput {
	t.Helper()
	var out protocol.ContainerRecreateOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output %s: %v", res.Output, err)
	}
	return out
}

// asideLeft reports whether a container set aside by a recreate is left.
func asideLeft(t *testing.T, fe *enginefake.Engine) bool {
	t.Helper()
	cs, err := fe.ListContainers(testutil.Context(t), engine.ContainerFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(cs, func(c engine.Container) bool {
		return slices.ContainsFunc(c.Names, func(n string) bool { return strings.Contains(n, protocol.RecreateAsideInfix) })
	})
}

// TestContainerRecreate (#273): a running standalone container is replaced
// by a new one with the same name, environment, labels (Docker Manager's
// under their current keys, so a saved specification stays attached),
// restart policy and volumes (the anonymous one mounted again by name), on
// the image its tag names now, and started; the old one is gone. A stopped
// container is recreated and stays stopped.
func TestContainerRecreate(t *testing.T) {
	s, fe := fixture(t)
	ctx := testutil.Context(t)
	fe.AddVolume("appdata", nil)
	oldID := fe.AddContainer(engine.ContainerSpec{Name: "app", Image: "nginx:1.27", Env: []string{"TOKEN=s3cret"}, RestartPolicy: "unless-stopped",
		Labels: map[string]string{"team": "ops", protocol.LegacyLabel(protocol.LabelSpec): "spec-1"},
		Mounts: []engine.MountSpec{{Type: "volume", Target: "/cache"}, {Type: "volume", Source: "appdata", Target: "/data"}}}, true)
	old, _ := fe.Container(oldID)
	anon := ""
	for _, m := range old.Details.Mounts {
		if m.Destination == "/cache" {
			anon = m.Name
		}
	}
	// A newer image was pulled under the tag since.
	newer := fe.AddImage("nginx:1.28")
	if err := fe.TagImage(ctx, "nginx:1.28", "nginx:1.27"); err != nil {
		t.Fatal(err)
	}
	ten := 10
	res, _ := run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: "app", ID: oldID, TimeoutSeconds: &ten})
	ok(t, "recreate", res)
	c, found := fe.Container("app")
	if !found || c.Details.ID == oldID || !c.Details.State.Running || c.Details.ImageID != newer || c.Details.RestartPolicy != "unless-stopped" ||
		!slices.Equal(c.Env, []string{"TOKEN=s3cret"}) {
		t.Fatalf("new container %+v", c)
	}
	if c.Details.Labels["team"] != "ops" || c.Details.Labels[protocol.LabelSpec] != "spec-1" || c.Details.Labels[protocol.LegacyLabel(protocol.LabelSpec)] != "" {
		t.Fatalf("labels %v", c.Details.Labels)
	}
	mounts := map[string]string{}
	for _, m := range c.Details.Mounts {
		mounts[m.Destination] = m.Name
	}
	if anon == "" || mounts["/cache"] != anon || mounts["/data"] != "appdata" {
		t.Fatalf("mounts %v, want the anonymous volume %s kept", mounts, anon)
	}
	if _, err := fe.InspectVolume(ctx, anon); err != nil {
		t.Fatalf("anonymous volume removed: %v", err)
	}
	if _, found := fe.Container(oldID); found || asideLeft(t, fe) {
		t.Fatal("the old container is left")
	}
	if out := recreateOutput(t, res); !out.WasRunning || out.ContainerID != c.Details.ID {
		t.Fatalf("output %+v", out)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "app" || strings.Contains(string(res.Output)+res.Items[0].Message, "s3cret") {
		t.Fatalf("items %+v output %s", res.Items, res.Output)
	}

	// A stopped container stays stopped.
	stoppedID := fe.AddContainer(engine.ContainerSpec{Name: "batch", Image: "postgres:17"}, false)
	res, _ = run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: "batch", ID: stoppedID})
	ok(t, "recreate stopped", res)
	if b, _ := fe.Container("batch"); b.Details.ID == stoppedID || b.Details.State.Running {
		t.Fatalf("stopped container %+v", b.Details)
	}
	if out := recreateOutput(t, res); out.WasRunning {
		t.Fatalf("output %+v", out)
	}
}

// TestContainerRecreateRollsBack: when the new container cannot be
// created, the old one gets its name back and keeps running.
func TestContainerRecreateRollsBack(t *testing.T) {
	s, fe := fixture(t)
	web, _ := fe.Container("web")
	fe.Fail("container.clone", enginefake.Err("container.clone", engine.CodeConflict, "the Engine refused"))
	res, _ := run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: "web", ID: web.Details.ID})
	failed(t, "clone fails", res, string(engine.CodeConflict))
	if c, _ := fe.Container("web"); c.Details.ID != web.Details.ID || !c.Details.State.Running || asideLeft(t, fe) {
		t.Fatalf("old container %+v", c.Details)
	}
}

// TestContainerRecreateResumes: a repeated attempt finishes what an
// earlier one started, from the new container it created (the old one set
// aside and still running) or after the old one is gone, and starts the
// new one because the old one ran.
func TestContainerRecreateResumes(t *testing.T) {
	s, fe := fixture(t)
	web, _ := fe.Container("web")
	in := protocol.ContainerActionInput{Name: "web", ID: web.Details.ID}
	fe.Fail("container.stop", enginefake.Err("container.stop", engine.CodeTimeout, "the Engine did not answer"))
	res, _ := run(t, s, jobspec.ContainerRecreate, in)
	failed(t, "stop fails", res, string(engine.CodeTimeout))
	created := recreateOutput(t, res).ContainerID
	if c, _ := fe.Container("web"); created == "" || c.Details.ID != created || !asideLeft(t, fe) {
		t.Fatalf("after the failed stop: %+v, new %s", c.Details, created)
	}
	ok(t, "resumed", runAgain(t, s, jobspec.ContainerRecreate, in, res.Output))
	if c, _ := fe.Container("web"); c.Details.ID != created || !c.Details.State.Running || asideLeft(t, fe) {
		t.Fatalf("resumed: %+v", c.Details)
	}

	// The old container already removed: only the start is left.
	db, _ := fe.Container("shop-db-1")
	cur, _ := fe.Container("web")
	fe.Fail("container.start", enginefake.Err("container.start", engine.CodeTimeout, "the Engine did not answer"))
	in = protocol.ContainerActionInput{Name: "web", ID: cur.Details.ID}
	res, _ = run(t, s, jobspec.ContainerRecreate, in)
	failed(t, "start fails", res, string(engine.CodeTimeout))
	if _, found := fe.Container(cur.Details.ID); found {
		t.Fatal("the old container is left")
	}
	ok(t, "resumed start", runAgain(t, s, jobspec.ContainerRecreate, in, res.Output))
	if c, _ := fe.Container("web"); c.Details.ID != recreateOutput(t, res).ContainerID || !c.Details.State.Running {
		t.Fatalf("started: %+v", c.Details)
	}
	if c, _ := fe.Container("shop-db-1"); c.Details.ID != db.Details.ID {
		t.Fatal("another container was touched")
	}
}

// TestContainerRecreateRefusals: the agent itself refuses the containers
// of a managed stack and of another Compose project, a container renamed
// since the request and Docker Manager's own containers, whatever the
// manager decided; nothing changes.
func TestContainerRecreateRefusals(t *testing.T) {
	s, fe := fixture(t)
	for name, class := range map[string]string{"shop-web-1": ClassStackManaged, "legacy-app-1": ClassComposeProject} {
		c, _ := fe.Container(name)
		res, _ := run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: name, ID: c.Details.ID})
		failed(t, name, res, class)
		if now, _ := fe.Container(name); now.Details.ID != c.Details.ID {
			t.Fatalf("%s was replaced", name)
		}
	}
	web, _ := fe.Container("web")
	res, _ := run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: "api", ID: web.Details.ID})
	failed(t, "name mismatch", res, ClassRecreated)

	s, fe, d := deployed(t)
	res, _ = run(t, s, jobspec.ContainerRecreate, protocol.ContainerActionInput{Name: "docker-manager-docker-agent-1", ID: d.AgentID})
	failed(t, "agent", res, protection.CodeProtected)
	if c, _ := fe.Container(d.AgentID); c.Details.Name != "docker-manager-docker-agent-1" || !c.Details.State.Running {
		t.Fatalf("agent %+v", c.Details)
	}
}
