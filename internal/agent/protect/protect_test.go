package protect

import (
	"encoding/json"
	"errors"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/protection"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func identify(t *testing.T, g *Guard, fe *enginefake.Engine) *Set {
	t.Helper()
	cs, err := fe.ListContainers(testutil.Context(t), engine.ContainerFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	return g.Identify(testutil.Context(t), fe, cs)
}

func role(t *testing.T, what string, p *protocol.Protection, want string, self bool) {
	t.Helper()
	if want == "" {
		if p != nil {
			t.Errorf("%s protected: %+v", what, p)
		}
		return
	}
	if p == nil || p.Role != want || p.Self != self || p.Reason == "" {
		t.Errorf("%s: %+v, want role %s self %v", what, p, want, self)
	}
}

// TestHostWithManagerAndAgent (#32 Done-when 2): on the co-located host the
// agent finds itself, the manager (by the container ID from
// manager.identity), the rest of Docker Manager's Compose project, their images,
// the manager data, agent state, stacks and proxy volumes and their
// network; user objects stay unprotected.
func TestHostWithManagerAndAgent(t *testing.T) {
	fe := enginefake.New("ENG")
	d := fe.Deploy(true)
	user := fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27",
		Mounts: []engine.MountSpec{{Type: "volume", Source: "webdata", Target: "/data"}}}, true)
	userNet := fe.AddNetwork("apps", nil)
	g := New(Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks, Logger: testutil.Logger(t)})

	// Before manager.identity: the manager is recognized by its label only.
	s := identify(t, g, fe)
	role(t, "manager (label)", s.Container(d.ManagerID), protection.RoleManager, false)

	h := g.ManagerIdentityHandler(func() engine.Engine { return fe })
	raw, _ := json.Marshal(protocol.ManagerIdentityInput{InstanceID: "inst-1", ContainerID: d.ManagerID})
	out, err := h(testutil.Context(t), raw)
	if err != nil || !out.(protocol.ManagerIdentityOutput).Colocated {
		t.Fatalf("manager.identity: %+v %v", out, err)
	}
	s = identify(t, g, fe)
	role(t, "agent", s.Container(d.AgentID), protection.RoleAgent, true)
	role(t, "manager", s.Container(d.ManagerID), protection.RoleManager, true)
	role(t, "proxy", s.Container(d.ProxyID), protection.RoleProject, false)
	role(t, "user container", s.Container(user), "", false)
	if p := s.Container(d.AgentID); p.RestartAllowed {
		t.Error("the connected agent may be restarted through a job")
	}
	if p := s.Container(d.ManagerID); !p.RestartAllowed {
		t.Error("the manager cannot be restarted with a confirmation")
	}
	for what, id := range map[string]string{"agent image": d.AgentImage, "manager image": d.ManagerImage, "proxy image": d.ProxyImage} {
		role(t, what, s.Image(id), protection.RoleImage, false)
	}
	role(t, "manager data", s.Volume(d.ManagerData, nil), protection.RoleManagerData, false)
	role(t, "agent state", s.Volume(d.AgentState, nil), protection.RoleAgentState, false)
	role(t, "stacks", s.Volume(d.Stacks, nil), protection.RoleStacks, false)
	role(t, "proxy data", s.Volume(d.ProxyData, nil), protection.RoleVolume, false)
	role(t, "user volume", s.Volume("webdata", nil), "", false)
	role(t, "volume of the project (label)", s.Volume("docker-manager_extra", map[string]string{protocol.ComposeProjectLabel: "docker-manager"}), protection.RoleVolume, false)
	n, _ := fe.InspectNetwork(testutil.Context(t), d.Network)
	role(t, "network by id", s.Network(n.ID, "", nil), protection.RoleNetwork, false)
	role(t, "network by name", s.Network("", d.Network, nil), protection.RoleNetwork, false)
	role(t, "user network", s.Network(userNet, "apps", nil), "", false)
	role(t, "bridge", s.Network("", "bridge", nil), "", false)
	role(t, "project", s.Project("docker-manager"), protection.RoleProject, false)
	if s.DockerRootDir != "/var/lib/docker" {
		t.Errorf("docker root %q", s.DockerRootDir)
	}
	if inst, id := g.Manager(); inst != "inst-1" || id != d.ManagerID {
		t.Errorf("manager identity %s %s", inst, id)
	}
	// A manager container without the label is found by its ID alone.
	unlabeled := fe.AddContainer(engine.ContainerSpec{Name: "mgr", Image: "code.neureka.dev/docker-manager/docker-manager:edge"}, true)
	g.SetManager("inst-1", unlabeled)
	role(t, "unlabeled manager", identify(t, g, fe).Container(unlabeled), protection.RoleManager, true)
}

// TestHostWithOnlyAnAgent (#32 Done-when 2): a remote host has the agent,
// its state and the stacks volume; a manager elsewhere is not colocated;
// another installation's labeled manager is still protected (not self).
func TestHostWithOnlyAnAgent(t *testing.T) {
	fe := enginefake.New("REMOTE")
	d := fe.Deploy(false)
	g := New(Options{SelfContainerID: d.AgentID, StacksVolume: d.Stacks})
	raw, _ := json.Marshal(protocol.ManagerIdentityInput{InstanceID: "inst-1",
		ContainerID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	out, err := g.ManagerIdentityHandler(func() engine.Engine { return fe })(testutil.Context(t), raw)
	if err != nil || out.(protocol.ManagerIdentityOutput).Colocated {
		t.Fatalf("remote manager reported colocated: %+v %v", out, err)
	}
	s := identify(t, g, fe)
	role(t, "agent", s.Container(d.AgentID), protection.RoleAgent, true)
	role(t, "state", s.Volume(d.AgentState, nil), protection.RoleAgentState, false)
	role(t, "stacks", s.Volume(d.Stacks, nil), protection.RoleStacks, false)
	role(t, "agent image", s.Image(d.AgentImage), protection.RoleImage, false)
	role(t, "project", s.Project("docker-manager"), protection.RoleProject, false)
	other := fe.AddContainer(engine.ContainerSpec{Name: "other-manager", Image: "nginx:1.27",
		Labels: map[string]string{protocol.LabelRole: "manager"}}, true)
	role(t, "other installation's manager", identify(t, g, fe).Container(other), protection.RoleManager, false)

	// Self-detection failed: the role label still protects the agent.
	blind := New(Options{StacksVolume: d.Stacks})
	role(t, "agent by label", identify(t, blind, fe).Container(d.AgentID), protection.RoleAgent, false)

	// Malformed identities are refused.
	for _, in := range []string{`{"instanceId":"i","containerId":"not-hex"}`, `not json`} {
		_, err := g.ManagerIdentityHandler(func() engine.Engine { return fe })(testutil.Context(t), json.RawMessage(in))
		var he *session.HandlerError
		if !errors.As(err, &he) || he.Code != protocol.CodeInvalidFrame {
			t.Errorf("%s: %v", in, err)
		}
	}
	// A nil set protects nothing (callers without an Engine).
	var none *Set
	if none.Container("x") != nil || none.Volume("x", nil) != nil || none.Image("x") != nil || none.Network("x", "x", nil) != nil || none.Project("x") != nil {
		t.Fatal("nil set protects")
	}
}
