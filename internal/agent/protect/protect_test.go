package protect

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginefake"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/protection"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
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
	before := len(fe.Calls())
	s = identify(t, g, fe)
	// The networks come from the list: identifying inspects no container,
	// which the Engine may block while it removes one (#307).
	if slices.Contains(fe.Calls()[before:], "container.inspect") {
		t.Errorf("identify inspected containers: %v", fe.Calls()[before:])
	}
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
	unlabeled := fe.AddContainer(engine.ContainerSpec{Name: "mgr", Image: "ghcr.io/neurekadev/docker-manager:edge"}, true)
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

// TestLegacyRoleLabels: containers started from a compose.yaml or docker
// run command written before the label prefix changed carry the legacy
// role key; they are Docker Manager's all the same (and their Compose
// project with them).
func TestLegacyRoleLabels(t *testing.T) {
	fe := enginefake.New("ENG")
	legacy := protocol.LegacyLabel(protocol.LabelRole)
	if legacy != "dev.neureka.docker-manager.role" || protocol.LabelRole != "docker-manager.role" {
		t.Fatalf("keys %q / %q", protocol.LabelRole, legacy)
	}
	manager := fe.AddContainer(engine.ContainerSpec{Name: "old-manager", Image: "nginx:1.27",
		Labels: map[string]string{legacy: "manager", protocol.ComposeProjectLabel: "old"}}, true)
	agent := fe.AddContainer(engine.ContainerSpec{Name: "old-agent", Image: "nginx:1.27", Labels: map[string]string{legacy: "agent"}}, true)
	helper := fe.AddContainer(engine.ContainerSpec{Name: "old-helper", Image: "nginx:1.27",
		Labels: map[string]string{legacy: protocol.RoleSelfUpdate}}, false)
	proxy := fe.AddContainer(engine.ContainerSpec{Name: "old-proxy", Image: "nginx:1.27",
		Labels: map[string]string{protocol.ComposeProjectLabel: "old"}}, true)
	fake := fe.AddContainer(engine.ContainerSpec{Name: "look-alike", Image: "nginx:1.27",
		Labels: map[string]string{"dev.neureka.role": "agent", "role": "manager"}}, true)
	s := identify(t, New(Options{}), fe)
	role(t, "legacy manager", s.Container(manager), protection.RoleManager, false)
	role(t, "legacy agent", s.Container(agent), protection.RoleAgent, false)
	role(t, "legacy helper", s.Container(helper), protection.RoleAgent, false)
	role(t, "project of a legacy manager", s.Container(proxy), protection.RoleProject, false)
	role(t, "look-alike", s.Container(fake), "", false)
}

// identityWith sends manager.identity with generation to g.
func identityWith(t *testing.T, g *Guard, instance string, generation int64) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(protocol.ManagerIdentityInput{InstanceID: instance, Generation: generation})
	return g.ManagerIdentityHandler(func() engine.Engine { return nil })(testutil.Context(t), raw)
}

func storedGeneration(t *testing.T, st *state.Store) int64 {
	t.Helper()
	g, err := st.ManagerGeneration()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestManagerGeneration (#35, manager moves): the first manager.identity
// records the generation (0, a manager that predates moves, counts as 1);
// a higher one raises it; a lower one is refused with conflict and ends the
// session with a close code the agent reconnects after (the credential
// stays), without taking over the manager identity.
func TestManagerGeneration(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cred := state.Credential{AgentID: "a1", EnvironmentID: "e1", Credential: "dya_c1_secret", ManagerURL: "https://m"}
	if err := st.SaveCredential(cred); err != nil {
		t.Fatal(err)
	}
	log, logs := testutil.CaptureLogger()
	g := New(Options{Logger: log})
	g.SetGenerations(st)

	// First identity, from a manager that predates moves: stored as 1.
	if _, err := identityWith(t, g, "old", 0); err != nil {
		t.Fatalf("first identity: %v", err)
	}
	if n := storedGeneration(t, st); n != 1 {
		t.Fatalf("stored %d, want 1 (0 counts as 1)", n)
	}
	// Generation 1 explicitly equals 0: accepted, nothing changes.
	if _, err := identityWith(t, g, "old", 1); err != nil || storedGeneration(t, st) != 1 {
		t.Fatalf("equal generation: %v", err)
	}
	// The moved manager (generation 2) raises it.
	if _, err := identityWith(t, g, "new", 2); err != nil {
		t.Fatalf("higher generation: %v", err)
	}
	if n := storedGeneration(t, st); n != 2 {
		t.Fatalf("stored %d, want 2", n)
	}
	if inst, _ := g.Manager(); inst != "new" {
		t.Fatalf("manager %q", inst)
	}

	// The old manager comes back (generation 0 = 1 < 2): refused.
	for _, old := range []int64{0, 1} {
		out, err := identityWith(t, g, "old", old)
		var end *session.EndSessionError
		if out != nil || !errors.As(err, &end) {
			t.Fatalf("generation %d accepted: %+v %v", old, out, err)
		}
		if end.Code != protocol.CloseManagerSuperseded || !protocol.ReconnectAllowed(end.Code) {
			t.Fatalf("close code %d", end.Code)
		}
		var he *session.HandlerError
		if !errors.As(err, &he) || he.Code != protocol.CodeConflict || !strings.Contains(he.Message, "generation 2") {
			t.Fatalf("error frame %+v", he)
		}
	}
	if inst, _ := g.Manager(); inst != "new" {
		t.Fatalf("the refused manager took over the identity: %q", inst)
	}
	if n := storedGeneration(t, st); n != 2 {
		t.Fatalf("refusal changed the stored generation: %d", n)
	}
	if c, err := st.Credential(); err != nil || c == nil || *c != cred {
		t.Fatalf("credential changed by the refusal: %+v %v", c, err)
	}
	if !strings.Contains(logs.String(), "refused an older Docker Manager") || strings.Contains(logs.String(), "dya_") {
		t.Fatalf("log: %s", logs.String())
	}

	// A negative generation is malformed.
	if _, err := identityWith(t, g, "x", -1); err == nil {
		t.Fatal("negative generation accepted")
	}
}

// TestManagerGenerationMissingOrCorrupt: no record accepts any manager; a
// corrupt record counts as 0 (with a warning) and is replaced.
func TestManagerGenerationMissingOrCorrupt(t *testing.T) {
	dir := t.TempDir()
	st, _ := state.Open(dir)
	if err := os.WriteFile(filepath.Join(dir, state.ManagerFile), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, logs := testutil.CaptureLogger()
	g := New(Options{Logger: log})
	g.SetGenerations(st)
	if _, err := identityWith(t, g, "m", 3); err != nil {
		t.Fatalf("identity over a corrupt record: %v", err)
	}
	if !strings.Contains(logs.String(), "cannot read the highest manager generation") {
		t.Fatalf("no warning: %s", logs.String())
	}
	if n := storedGeneration(t, st); n != 3 {
		t.Fatalf("stored %d, want 3", n)
	}

	// Without a generation store (no state directory) nothing is checked.
	bare := New(Options{Logger: testutil.Logger(t)})
	if _, err := identityWith(t, bare, "m", 0); err != nil {
		t.Fatal(err)
	}
}

// TestAcceptGenerationSharedWithWelcome: the welcome check
// (AcceptGeneration, wired as session.Options.AcceptWelcome) and
// manager.identity share one record: 0 counts as 1, a higher welcome
// raises it, a lower one is a *SupersededError, and manager.identity then
// refuses what the welcome already refused.
func TestAcceptGenerationSharedWithWelcome(t *testing.T) {
	st, _ := state.Open(t.TempDir())
	g := New(Options{Logger: testutil.Logger(t)})
	g.SetGenerations(st)
	if err := g.AcceptGeneration("", 0); err != nil || storedGeneration(t, st) != 1 {
		t.Fatalf("welcome with 0: %v", err)
	}
	if err := g.AcceptGeneration("", 5); err != nil || storedGeneration(t, st) != 5 {
		t.Fatalf("higher welcome: %v", err)
	}
	var superseded *SupersededError
	if err := g.AcceptGeneration("", 4); !errors.As(err, &superseded) || superseded.Generation != 4 || superseded.Followed != 5 {
		t.Fatalf("lower welcome: %v", err)
	}
	if storedGeneration(t, st) != 5 {
		t.Fatal("a refusal lowered the record")
	}
	var end *session.EndSessionError
	if _, err := identityWith(t, g, "old", 4); !errors.As(err, &end) {
		t.Fatalf("manager.identity accepted what the welcome refused: %v", err)
	}
}
