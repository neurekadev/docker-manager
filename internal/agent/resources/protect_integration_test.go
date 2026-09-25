//go:build integration

package resources_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	"github.com/neurekadev/dockyard/internal/agent/resources"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protection"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineSelfProtection (#32) on two real Engines of the matrix: one
// runs DockYard like the co-located deploy example (manager, agent, proxy
// in Compose project "dockyard" with their volumes and network), the other
// only an agent. The workload image stands in for the DockYard images. On
// both, the agent's inventory marks DockYard's resources as protected and
// its executors refuse to stop or remove the agent, the manager and their
// volumes, images and network, and to mount their volumes or the Docker
// data root into a new container; the manager restarts only with a
// confirmation.
func TestEngineSelfProtection(t *testing.T) {
	engines := testharness.StartEngines(t, 2, testharness.EngineOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)
	for i, withManager := range []bool{true, false} {
		e := engines[i]
		e.LoadWorkload(t)
		c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		name := map[bool]string{true: "manager_and_agent", false: "agent_only"}[withManager]
		t.Run(name, func(t *testing.T) { selfProtection(ctx, t, c, withManager) })
	}
}

func selfProtection(ctx context.Context, t *testing.T, c *engine.Client, withManager bool) {
	project := "dockyard-agent"
	if withManager {
		project = "dockyard"
	}
	netID, err := c.CreateNetwork(ctx, engine.NetworkSpec{Name: project + "_default", Labels: map[string]string{protocol.ComposeProjectLabel: project}})
	if err != nil {
		t.Fatal(err)
	}
	labels := func(service, role string) map[string]string {
		l := map[string]string{protocol.ComposeProjectLabel: project, protocol.ComposeServiceLabel: service}
		if role != "" {
			l[protocol.LabelRole] = role
		}
		return l
	}
	run := func(name string, l map[string]string, mounts ...engine.MountSpec) string {
		id, _, err := c.CreateContainer(ctx, engine.ContainerSpec{Name: name, Image: testharness.WorkloadImage, Cmd: []string{"serve", "up"},
			Labels: l, NetworkMode: project + "_default", Mounts: mounts})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	agentID := run(project+"-dockyard-agent-1", labels("dockyard-agent", "agent"),
		engine.MountSpec{Type: "volume", Source: "dockyard_stacks", Target: "/var/lib/docker/volumes/dockyard_stacks/_data"},
		engine.MountSpec{Type: "volume", Source: project + "_agent_state", Target: protect.AgentStateDir})
	var managerID string
	if withManager {
		managerID = run(project+"-dockyard-manager-1", labels("dockyard-manager", "manager"),
			engine.MountSpec{Type: "volume", Source: project + "_dockyard_data", Target: protect.ManagerDataDir})
		run(project+"-caddy-1", labels("caddy", ""))
	}
	g := protect.New(protect.Options{SelfContainerID: agentID, StacksVolume: "dockyard_stacks", Logger: testutil.Logger(t)})
	svc := resources.New(resources.Options{Engine: func() engine.Engine { return c }, Guard: g, Logger: testutil.Logger(t)})
	if withManager {
		raw, _ := json.Marshal(protocol.ManagerIdentityInput{InstanceID: "inst-it", ContainerID: managerID})
		out, err := svc.Requests()[protocol.ReqManagerIdentity](ctx, raw)
		if err != nil || !out.(protocol.ManagerIdentityOutput).Colocated {
			t.Fatalf("manager.identity: %+v %v", out, err)
		}
	}
	raw, _ := json.Marshal(protocol.ContainerListInput{})
	res, err := svc.Requests()[protocol.ReqContainerList](ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	protected := 0
	for _, s := range res.(protocol.ContainerListOutput).Containers {
		if s.Protection != nil {
			protected++
		}
	}
	if want := map[bool]int{true: 3, false: 1}[withManager]; protected != want {
		t.Errorf("%d protected containers, want %d", protected, want)
	}

	execs := map[domain.JobKind]jobexec.Executor{}
	for _, x := range svc.Executors() {
		execs[x.Kind] = x
	}
	job := func(kind domain.JobKind, input any) protocol.ResultPayload {
		b, _ := json.Marshal(input)
		r, err := jobexec.Run(ctx, execs[kind], &jobexec.State{JobID: "j", Attempt: 1, FencingToken: 1, Kind: kind, Input: b},
			jobexec.Options{Journal: journal{}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	refused := func(what string, r protocol.ResultPayload, class string) {
		t.Helper()
		if r.Outcome != jobexec.OutcomeFailed || r.ErrorClass != class {
			t.Errorf("%s: %+v, want %s", what, r, class)
		}
	}
	agent := protocol.ContainerActionInput{Name: project + "-dockyard-agent-1", ID: agentID, Force: true, Confirmed: true}
	refused("stop agent", job(jobspec.ContainerStop, agent), protection.CodeProtected)
	refused("remove agent", job(jobspec.ContainerRemove, agent), protection.CodeProtected)
	refused("restart agent", job(jobspec.ContainerRestart, agent), protection.CodeProtected)
	refused("remove stacks volume", job(jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: "dockyard_stacks"}), protection.CodeProtected)
	refused("remove agent state", job(jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: project + "_agent_state"}), protection.CodeProtected)
	refused("remove network", job(jobspec.NetworkRemove, protocol.NetworkRemoveInput{Name: project + "_default", ID: netID}), protection.CodeProtected)
	refused("bind docker root", job(jobspec.ContainerCreate, protocol.ContainerCreateInput{Spec: protocol.ContainerSpec{Name: "thief",
		Image: testharness.WorkloadImage, Mounts: []protocol.MountSpec{{Type: "bind", Source: "/var/lib/docker", Target: "/d"}}}}), protection.CodeProtected)
	if withManager {
		manager := protocol.ContainerActionInput{Name: project + "-dockyard-manager-1", ID: managerID, Force: true}
		refused("stop manager", job(jobspec.ContainerStop, manager), protection.CodeProtected)
		refused("remove manager data", job(jobspec.VolumeRemove, protocol.VolumeRemoveInput{Name: project + "_dockyard_data"}), protection.CodeProtected)
		refused("restart manager unconfirmed", job(jobspec.ContainerRestart, manager), protection.CodeConfirmationRequired)
		manager.Confirmed = true
		if r := job(jobspec.ContainerRestart, manager); r.Outcome != jobexec.OutcomeSucceeded {
			t.Errorf("confirmed manager restart: %+v", r)
		}
	}
	d, err := c.InspectContainer(ctx, agentID)
	if err != nil || !d.State.Running {
		t.Fatalf("agent container after the refusals: %+v %v", d.State, err)
	}
}
