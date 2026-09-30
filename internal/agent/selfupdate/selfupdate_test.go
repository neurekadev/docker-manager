package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func project(services ...compose.ServiceInfo) *compose.Project {
	return &compose.Project{Name: "docker-manager", Services: services}
}

func dependsOn(name string, deps ...string) compose.ServiceInfo {
	s := compose.ServiceInfo{Name: name}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, compose.Dependency{Service: d})
	}
	return s
}

// TestHandoff: the agent's own service and everything depending on it
// (directly or not) go to the helper; the rest is converged by the job.
func TestHandoff(t *testing.T) {
	p := project(dependsOn("docker-manager"), dependsOn("docker-agent", "docker-manager"), dependsOn("caddy", "docker-manager"),
		dependsOn("sidecar", "docker-agent"), dependsOn("watcher", "sidecar"))
	rest, handoff := Handoff(p, "docker-agent", nil)
	if !slices.Equal(rest, []string{"docker-manager", "caddy"}) || !slices.Equal(handoff, []string{"docker-agent", "sidecar", "watcher"}) {
		t.Fatalf("all services: rest %v handoff %v", rest, handoff)
	}
	rest, handoff = Handoff(p, "docker-agent", []string{"caddy"})
	if !slices.Equal(rest, []string{"caddy"}) || handoff != nil {
		t.Fatalf("caddy only: rest %v handoff %v", rest, handoff)
	}
	rest, handoff = Handoff(p, "docker-agent", []string{"docker-agent"})
	if rest != nil || !slices.Equal(handoff, []string{"docker-agent"}) {
		t.Fatalf("agent only: rest %v handoff %v", rest, handoff)
	}
	// A dependency cycle does not loop.
	cyc := project(dependsOn("a", "b"), dependsOn("b", "a"), dependsOn("docker-agent"))
	if rest, handoff := Handoff(cyc, "docker-agent", nil); !slices.Equal(rest, []string{"a", "b"}) || !slices.Equal(handoff, []string{"docker-agent"}) {
		t.Fatalf("cycle: rest %v handoff %v", rest, handoff)
	}
	if !HasService(p, "docker-agent") || HasService(p, "gone") {
		t.Fatal("HasService")
	}
}

func TestMounts(t *testing.T) {
	got := Mounts([]engine.Mount{
		{Type: "bind", Source: "/run/engine", Destination: "/run/engine", ReadWrite: true},
		{Type: "volume", Name: "docker-manager_agent", Source: "/var/lib/docker/volumes/docker-manager_agent/_data", Destination: "/var/lib/docker-agent", ReadWrite: true},
		{Type: "volume", Destination: "/anon"},
		{Type: "tmpfs", Destination: "/tmp"},
		{Type: "bind", Source: "/etc/ca", Destination: "/etc/ca"},
	})
	want := []engine.MountSpec{
		{Type: "bind", Source: "/run/engine", Target: "/run/engine"},
		{Type: "volume", Source: "docker-manager_agent", Target: "/var/lib/docker-agent"},
		{Type: "bind", Source: "/etc/ca", Target: "/etc/ca", ReadOnly: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("mounts %+v", got)
	}
}

// TestLauncher: the agent recognizes its own service in Docker Manager's
// project only; a plan starts a helper only after its job succeeded, with
// the agent's image, mounts and the plan file in the state directory.
func TestLauncher(t *testing.T) {
	ctx := testutil.Context(t)
	fe := enginefake.New("ENG")
	d := fe.Deploy(true)
	state := t.TempDir()
	l := New(Options{StateDir: state, SelfContainerID: d.AgentID, Engine: func() engine.Engine { return fe },
		Logger: testutil.Logger(t)})

	if svc, ok, err := l.OwnService(ctx, "docker-manager"); err != nil || !ok || svc != "docker-agent" {
		t.Fatalf("own service: %q %v %v", svc, ok, err)
	}
	if _, ok, err := l.OwnService(ctx, "shop"); err != nil || ok {
		t.Fatalf("another project: %v %v", ok, err)
	}
	if _, ok, _ := New(Options{Engine: func() engine.Engine { return fe }}).OwnService(ctx, "docker-manager"); ok {
		t.Fatal("an agent outside a container hands nothing over")
	}

	plan := Plan{JobID: "0190-aa", ProjectName: "docker-manager", Dir: "/opt/docker-manager",
		Content: map[string][]byte{"compose.yaml": []byte("services: {}\n")}, Services: []string{"docker-agent"}}
	l.Schedule(plan)
	l.JobFinished(ctx, plan.JobID, false)
	if _, ok := fe.Container(HelperName(plan.JobID)); ok {
		t.Fatal("a helper started after a failed job")
	}
	l.JobFinished(ctx, plan.JobID, true) // dropped with the failure
	if _, ok := fe.Container(HelperName(plan.JobID)); ok {
		t.Fatal("a dropped plan started a helper")
	}

	l.Schedule(plan)
	l.JobFinished(ctx, "another-job", true)
	if _, ok := fe.Container(HelperName(plan.JobID)); ok {
		t.Fatal("another job started the helper")
	}
	l.JobFinished(ctx, plan.JobID, true)
	h, ok := fe.Container(HelperName(plan.JobID))
	if !ok || !h.Details.State.Running {
		t.Fatalf("helper %+v", h.Details)
	}
	path := filepath.Join(state, dirName, plan.JobID+".json")
	if !slices.Equal(h.Details.Cmd, []string{Subcommand, path}) || h.Details.Labels[protocol.LabelRole] != RoleHelper {
		t.Fatalf("helper command %v labels %v", h.Details.Cmd, h.Details.Labels)
	}
	agent, _ := fe.Container(d.AgentID)
	if h.Details.ImageID != agent.Details.ImageID {
		t.Fatalf("helper image %s, agent %s", h.Details.ImageID, agent.Details.ImageID)
	}
	var dests []string
	for _, m := range h.Details.Mounts {
		dests = append(dests, m.Destination)
	}
	for _, want := range []string{"/var/lib/docker-agent", "/var/lib/docker/volumes"} {
		if !slices.Contains(dests, want) {
			t.Fatalf("helper mounts %v miss %s", dests, want)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Plan
	if err := json.Unmarshal(b, &got); err != nil || got.AgentContainerID != d.AgentID || string(got.Content["compose.yaml"]) != "services: {}\n" {
		t.Fatalf("plan %+v %v", got, err)
	}
}

// TestCollect: the next agent logs and removes the helper's results.
func TestCollect(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Result{JobID: "j1", Services: []string{"docker-agent"}, Error: "boom", FinishedAt: time.Unix(0, 0)})
	if err := os.WriteFile(filepath.Join(dir, "j1.result.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	log, buf := testutil.CaptureLogger()
	Collect(state, log)
	if _, err := os.Stat(filepath.Join(dir, "j1.result.json")); !os.IsNotExist(err) {
		t.Fatalf("result kept: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "the last self-update failed") || !strings.Contains(out, "j1") {
		t.Fatalf("log %s", out)
	}
	Collect(filepath.Join(state, "missing"), log) // no directory: nothing to do
}
