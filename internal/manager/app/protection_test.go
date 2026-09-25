package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/agent/protect"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/protection"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

type protectedItem struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Protection *struct {
		Role           string `json:"role"`
		Reason         string `json:"reason"`
		Self           bool   `json:"self"`
		RestartAllowed bool   `json:"restartAllowed"`
	} `json:"protection"`
}

// TestSelfProtectionOnTwoHosts (#32 Done-when 1 and 2): NAS runs the
// manager and an agent (the deploy example), Cloud only an agent. The
// manager tells each agent its identity after connecting; both hosts show
// DockYard's resources as protected; through the API, stopping or removing
// the agent, deleting the manager data or stacks volume and removing
// DockYard's images fail with a clear reason for the owner and for an API
// token; the manager restarts only with a confirmation; and an agent
// refuses a protected operation even when the manager sends it (a job
// enqueued past the API).
func TestSelfProtectionOnTwoHosts(t *testing.T) {
	nasEngine, cloudEngine := enginefake.New("ENGINE-NAS"), enginefake.New("ENGINE-CLOUD")
	nasEngine.AddImage("nginx:1.27")
	nd, cd := nasEngine.Deploy(true), cloudEngine.Deploy(false)
	e := newEnv(t, func(o *Options) { o.ContainerID = nd.ManagerID })
	nas := e.connectGuardedAgent("NAS", nasEngine, protect.New(protect.Options{SelfContainerID: nd.AgentID, StacksVolume: nd.Stacks}))
	cloud := e.connectGuardedAgent("Cloud", cloudEngine, protect.New(protect.Options{SelfContainerID: cd.AgentID, StacksVolume: cd.Stacks}))
	owner, _ := e.setupOwner()
	nasEnv, cloudEnv := "/api/v1/environments/"+nas.env, "/api/v1/environments/"+cloud.env

	protectedOf := func(path string) map[string]protectedItem {
		var page struct {
			Items []protectedItem `json:"items"`
		}
		owner.must(http.StatusOK, http.MethodGet, path, nil).json(t, &page)
		out := map[string]protectedItem{}
		for _, it := range page.Items {
			if it.Protection != nil {
				out[it.Name] = it
			}
		}
		return out
	}
	// Host with manager and agent: the manager was matched through
	// manager.identity (self), the agent found itself.
	p := protectedOf(nasEnv + "/containers")
	if a, m, x := p["dockyard-dockyard-agent-1"], p["dockyard-dockyard-manager-1"], p["dockyard-caddy-1"]; len(p) != 3 || a.Protection.Role != "agent" ||
		!a.Protection.Self || m.Protection.Role != "manager" || !m.Protection.Self || !m.Protection.RestartAllowed || x.Protection.Role != "dockyard_project" {
		t.Fatalf("NAS protections %+v", p)
	}
	vols := protectedOf(nasEnv + "/volumes")
	if len(vols) != 4 || vols[nd.ManagerData].Protection.Role != "manager_data" || vols[nd.Stacks].Protection.Role != "stacks" ||
		vols[nd.AgentState].Protection.Role != "agent_state" {
		t.Fatalf("NAS volumes %+v", vols)
	}
	// Host with only an agent.
	p = protectedOf(cloudEnv + "/containers")
	if a := p["dockyard-dockyard-agent-1"]; len(p) != 1 || a.Protection == nil || !a.Protection.Self || a.Protection.RestartAllowed {
		t.Fatalf("Cloud protections %+v", p)
	}
	if vols := protectedOf(cloudEnv + "/volumes"); len(vols) != 2 {
		t.Fatalf("Cloud volumes %+v", vols)
	}

	// Refusals with a clear reason, for the owner.
	for _, c := range []struct{ method, path, code string }{
		{http.MethodPost, nasEnv + "/containers/dockyard-dockyard-agent-1/stop", "protected"},
		{http.MethodDelete, nasEnv + "/containers/dockyard-dockyard-agent-1?force=true", "protected"},
		{http.MethodPost, nasEnv + "/containers/dockyard-dockyard-manager-1/stop", "protected"},
		{http.MethodPost, nasEnv + "/containers/dockyard-dockyard-manager-1/restart", "confirmation_required"},
		{http.MethodDelete, nasEnv + "/volumes/" + nd.ManagerData, "protected"},
		{http.MethodDelete, nasEnv + "/volumes/" + nd.Stacks, "protected"},
		{http.MethodDelete, nasEnv + "/images/" + nd.AgentImage + "?force=true", "protected"},
		{http.MethodPost, cloudEnv + "/containers/dockyard-dockyard-agent-1/stop", "protected"},
		{http.MethodDelete, cloudEnv + "/volumes/" + cd.Stacks, "protected"},
	} {
		r := owner.fail(http.StatusConflict, c.code, c.method, c.path, nil)
		if !strings.Contains(string(r.body), "DockYard") {
			t.Errorf("%s %s: no clear reason: %s", c.method, c.path, r.body)
		}
	}
	id := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, nasEnv+"/containers/dockyard-dockyard-manager-1/restart", map[string]any{"confirm": true}))
	if j := e.runJob(id); j.State != domain.JobSucceeded {
		t.Fatalf("confirmed manager restart %+v", j)
	}

	// An API token with every container grant is refused the same way.
	rita, _ := e.opsUser(owner, "allow api_tokens.create @all", "allow container.stop @env:"+nas.env, "allow container.remove @env:"+nas.env,
		"allow container.details.read @env:"+nas.env)
	_, secret := rita.createToken("ops", "allow container.stop @env:"+nas.env, "allow container.remove @env:"+nas.env)
	e.bot(secret).fail(http.StatusConflict, "protected", http.MethodPost, nasEnv+"/containers/dockyard-dockyard-agent-1/stop", nil)

	// Past the manager: a job sent straight to the agent is refused there.
	ctx := testutil.Context(t)
	for _, kind := range []domain.JobKind{jobspec.ContainerStop, jobspec.ContainerRemove} {
		j, _, err := e.m.Jobs().Enqueue(ctx, jobs.Request{Kind: kind, Principal: authz.Service(), EnvironmentID: cloud.env,
			Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "dockyard-dockyard-agent-1"}},
			Input:   protocol.ContainerActionInput{Name: "dockyard-dockyard-agent-1", ID: cd.AgentID, Force: true}})
		if err != nil {
			t.Fatal(err)
		}
		done := e.runJob(j.ID)
		if done.State != domain.JobFailed || done.ErrorClass != protection.CodeProtected || !strings.Contains(done.ErrorMessage, "DockYard agent") {
			t.Fatalf("%s sent past the manager: %+v", kind, done)
		}
	}
	if c, _ := cloudEngine.Container(cd.AgentID); !c.Details.State.Running {
		t.Fatal("Cloud's agent was stopped")
	}
	if _, err := cloudEngine.InspectVolume(ctx, cd.Stacks); err != nil {
		t.Fatalf("Cloud's stacks volume is gone: %v", err)
	}
	if _, ok := nasEngine.Container(nd.AgentID); !ok {
		t.Fatal("NAS's agent was removed")
	}
}
