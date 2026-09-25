package app

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// Docker resources (#6) through the real manager and real agent sessions:
// identity, authorization (#17), API tokens (#31), the job engine (#26),
// audit (#30), the agent transport (#3) and the agent's resource executors
// over in-memory fake Engines.

type dockerContainer struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	EnvironmentID string   `json:"environmentId"`
	State         string   `json:"state"`
	View          string   `json:"view"`
	Actions       []string `json:"actions"`
	Image         string   `json:"image"`
	Managed       *struct {
		SpecSaved    bool `json:"specSaved"`
		ThisInstance bool `json:"thisInstance"`
	} `json:"managed"`
	Details *struct {
		Recreate struct {
			EnvKeys []string `json:"envKeys"`
		} `json:"recreate"`
	} `json:"details"`
}

type dockerJob struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	State            string `json:"state"`
	Origin           string `json:"origin"`
	InitiatorUserID  string `json:"initiatorUserId"`
	InitiatorTokenID string `json:"initiatorTokenId"`
	Error            *struct {
		Class    string `json:"class"`
		Message  string `json:"message"`
		Recovery string `json:"recovery"`
	} `json:"error"`
}

// twoHosts starts the manager with two agents: NAS (web, db and the
// DockYard-managed stack "shop") and Cloud (another "web").
func twoHosts(t *testing.T) (*env, *testAgent, *testAgent) {
	e := newEnv(t)
	nas, cloud := enginefake.New("ENGINE-NAS"), enginefake.New("ENGINE-CLOUD")
	for _, fe := range []*enginefake.Engine{nas, cloud} {
		fe.AddImage("nginx:1.27")
		fe.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27"}, true)
	}
	nas.AddContainer(engine.ContainerSpec{Name: "db", Image: "postgres:17"}, true)
	nas.AddContainer(engine.ContainerSpec{Name: "shop-web-1", Image: "nginx:1.27", Labels: map[string]string{protocol.ComposeProjectLabel: "shop",
		protocol.ComposeServiceLabel: "web", protocol.ComposeWorkingDirLabel: stacksRoot + "/shop"}}, true)
	return e, e.connectAgent("NAS", nas), e.connectAgent("Cloud", cloud)
}

// TestDockerOperationsThroughAgents (#6 Done-when 1 and 2): container IDs
// cannot be confused across two hosts; operations run as jobs on the right
// agent and are reflected in the inventory; failed pulls carry the registry
// failure class and guidance; an offline host yields a clear error;
// created containers are labeled and their recreate specification is saved
// sealed.
func TestDockerOperationsThroughAgents(t *testing.T) {
	e, nas, cloud := twoHosts(t)
	owner, _ := e.setupOwner()
	nasBase, cloudBase := "/api/v1/environments/"+nas.env+"/containers", "/api/v1/environments/"+cloud.env+"/containers"

	var a, b dockerContainer
	owner.must(http.StatusOK, http.MethodGet, nasBase+"/web", nil).json(t, &a)
	owner.must(http.StatusOK, http.MethodGet, cloudBase+"/web", nil).json(t, &b)
	if a.ID == b.ID || a.EnvironmentID != nas.env || b.EnvironmentID != cloud.env || a.View != "full" {
		t.Fatalf("NAS web %+v, Cloud web %+v", a, b)
	}
	owner.fail(http.StatusNotFound, "not_found", http.MethodGet, nasBase+"/"+b.ID, nil)
	owner.fail(http.StatusNotFound, "not_found", http.MethodPost, nasBase+"/"+b.ID[:12]+"/restart", nil)

	// Restart NAS's web: a job on NAS's agent only.
	r := owner.must(http.StatusAccepted, http.MethodPost, nasBase+"/web/restart", nil, header("Idempotency-Key", "restart-1"))
	id := jobOf(t, r)
	if j := e.runJob(id); j.State != domain.JobSucceeded || j.EnvironmentID != nas.env {
		t.Fatalf("restart job %+v", j)
	}
	if c, _ := nas.engine.Container("web"); c.Details.RestartCount != 1 {
		t.Fatalf("NAS web restarts %d", c.Details.RestartCount)
	}
	if c, _ := cloud.engine.Container("web"); c.Details.RestartCount != 0 {
		t.Fatal("Cloud's web was restarted")
	}
	// The same key returns the same job.
	if again := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, nasBase+"/web/restart", nil, header("Idempotency-Key", "restart-1"))); again != id {
		t.Fatalf("idempotent restart created %s, want %s", again, id)
	}

	// Stop, then the inventory shows it.
	e.runJob(jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, nasBase+"/web/stop", map[string]any{"timeoutSeconds": 3})))
	owner.must(http.StatusOK, http.MethodGet, nasBase+"/web", nil).json(t, &a)
	if a.State != "exited" {
		t.Fatalf("stopped web state %s", a.State)
	}

	// Create a standalone container: labeled, spec saved sealed, env
	// values never returned or stored in plaintext.
	e.secrets.Register(canary.EnvValue, "container env", "sup3r-s3cret-value")
	r = owner.must(http.StatusAccepted, http.MethodPost, nasBase, map[string]any{"name": "api", "image": "nginx:1.27",
		"env": []string{"API_TOKEN=sup3r-s3cret-value"}, "restartPolicy": "always"})
	if j := e.runJob(jobOf(t, r)); j.State != domain.JobSucceeded {
		t.Fatalf("create job %+v", j)
	}
	c, ok := nas.engine.Container("api")
	if !ok || !c.Details.State.Running || c.Details.Labels[protocol.LabelManaged] != protocol.ManagedStandalone ||
		c.Details.Labels[protocol.LabelInstance] != e.m.Instance().ID || c.Details.Labels[protocol.LabelSpec] == "" {
		t.Fatalf("created container %+v", c.Details)
	}
	var api dockerContainer
	owner.must(http.StatusOK, http.MethodGet, nasBase+"/api", nil).json(t, &api)
	if api.Managed == nil || !api.Managed.SpecSaved || !api.Managed.ThisInstance || api.Details == nil ||
		!slices.Equal(api.Details.Recreate.EnvKeys, []string{"API_TOKEN"}) {
		t.Fatalf("managed container %+v", api)
	}
	e.secrets.AssertClean(t, "managed specifications and audit", e.tableDump("managed_containers", "audit_events"))
	spec, specBody, err := e.m.Resources().ManagedSpec(testutil.Context(t), nas.env, c.Details.Labels)
	if err != nil || spec == nil || !slices.Equal(specBody.Env, []string{"API_TOKEN=sup3r-s3cret-value"}) || spec.CreateJobID == "" {
		t.Fatalf("saved spec %+v %+v %v", spec, specBody, err)
	}

	// A failed pull keeps the registry failure class and its guidance.
	nas.engine.Fail("image.pull", enginefake.Err("image.pull", engine.CodeRateLimited, "toomanyrequests: You have reached your pull rate limit"))
	r = owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/"+nas.env+"/images/pulls", map[string]any{"reference": "library/busybox:1.37"})
	pid := jobOf(t, r)
	e.runJob(pid)
	var pj dockerJob
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+pid, nil).json(t, &pj)
	if pj.State != "failed" || pj.Error == nil || pj.Error.Class != "rate_limited" || !strings.Contains(pj.Error.Recovery, "rate limit") {
		t.Fatalf("failed pull %+v", pj)
	}
	nas.engine.Fail("image.pull", enginefake.Err("image.pull", engine.CodeUnauthorized, "pull access denied for ghcr.io/org/private"))
	pid = jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/"+nas.env+"/images/pulls", map[string]any{"reference": "ghcr.io/org/private:1"}))
	e.runJob(pid)
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/jobs/"+pid, nil).json(t, &pj)
	if pj.Error == nil || pj.Error.Class != "unauthorized" || !strings.Contains(pj.Error.Recovery, "registry connection") {
		t.Fatalf("unauthorized pull %+v", pj)
	}
	// A successful pull appears in the image list.
	e.runJob(jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, "/api/v1/environments/"+nas.env+"/images/pulls", map[string]any{"reference": "alpine:3.22"})))
	var images struct {
		Items []struct {
			RepoTags []string `json:"repoTags"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+nas.env+"/images?q=alpine", nil).json(t, &images)
	if len(images.Items) != 1 || images.Items[0].RepoTags[0] != "alpine:3.22" {
		t.Fatalf("pulled image not listed %+v", images)
	}

	// A DockYard-managed stack's container cannot be removed directly.
	owner.fail(http.StatusConflict, "stack_managed", http.MethodDelete, nasBase+"/shop-web-1?force=true", nil)

	// Audit: the restart request and its job lifecycle, with the target.
	var requested, finished bool
	for _, row := range e.auditRows() {
		if row.Action == "container.restart" && row.Outcome == "success" && strings.Contains(row.Targets, "web") {
			requested = true
		}
		if row.Action == "job.finished" && strings.Contains(row.Details, "container.restart") {
			finished = true
		}
	}
	if !requested || !finished {
		t.Fatalf("audit: requested %v finished %v", requested, finished)
	}

	// Cloud goes offline: a clear error, nothing queued.
	cloud.stop()
	owner.fail(http.StatusServiceUnavailable, "environment_offline", http.MethodGet, cloudBase, nil)
	owner.fail(http.StatusServiceUnavailable, "environment_offline", http.MethodPost, cloudBase+"/web/restart", nil)
}

// TestAPITokenRestartsOneContainer (#31 Done-when 1 with the container
// routes, #17 restart-only): a token scoped to container.restart on one
// container restarts it (job origin api_token with the token ID), cannot
// touch another container, cannot use other container actions and cannot
// call owner endpoints.
func TestAPITokenRestartsOneContainer(t *testing.T) {
	e, nas, _ := twoHosts(t)
	owner, _ := e.setupOwner()
	rita, ritaID := e.opsUser(owner, "allow api_tokens.create @all", "allow container.restart @env:"+nas.env,
		"allow container.stop @env:"+nas.env, "allow container.details.read @env:"+nas.env)
	tok, secret := rita.createToken("ci", "allow container.restart @container:"+nas.env+"/web")
	b := e.bot(secret)
	base := "/api/v1/environments/" + nas.env + "/containers"

	r := b.must(http.StatusAccepted, http.MethodPost, base+"/web/restart", nil)
	id := jobOf(t, r)
	var j dockerJob
	r.json(t, &j)
	if j.Origin != "api_token" || j.InitiatorTokenID != tok.ID || j.InitiatorUserID != ritaID || j.Kind != "container.restart" {
		t.Fatalf("token job %+v", j)
	}
	if done := e.runJob(id); done.State != domain.JobSucceeded {
		t.Fatalf("token restart %+v", done)
	}
	if c, _ := nas.engine.Container("web"); c.Details.RestartCount != 1 {
		t.Fatal("web was not restarted")
	}
	// Another container: not visible to the token (rita herself could).
	b.fail(http.StatusNotFound, "not_found", http.MethodPost, base+"/db/restart", nil)
	b.fail(http.StatusNotFound, "not_found", http.MethodGet, base+"/db", nil)
	// The token's container is visible minimally; other actions are
	// refused although rita holds them. (Logs are #8: its route will
	// answer 403 the same way; until then the stop action stands in.)
	var web dockerContainer
	b.must(http.StatusOK, http.MethodGet, base+"/web", nil).json(t, &web)
	if web.View != "minimal" || !slices.Equal(web.Actions, []string{"container.restart"}) || web.Image != "" {
		t.Fatalf("token view %+v", web)
	}
	b.fail(http.StatusForbidden, "forbidden", http.MethodPost, base+"/web/stop", nil)
	b.fail(http.StatusForbidden, "forbidden", http.MethodDelete, base+"/web", nil)
	// Owner endpoints are never reachable with a token.
	b.fail(http.StatusForbidden, "api_token_not_allowed", http.MethodGet, "/api/v1/users", nil)
	// The token-authenticated restart is audited with the token ID.
	found := false
	for _, row := range e.auditRows() {
		if row.Action == "container.restart" && row.ActorToken == tok.ID && row.ActorKind == "api_token" && row.Outcome == "success" {
			found = true
		}
	}
	if !found {
		t.Fatal("token restart not audited with the token ID")
	}
	e.assertNoTokenValues()
}

// TestMetricsOnlyUserSeesOnlyIdentityAndStatus (#17 Done-when 2 with the
// container routes): container.metrics.read on one container shows only
// that container with identity, state and the metrics action; its details,
// other containers and every action are refused.
func TestMetricsOnlyUserSeesOnlyIdentityAndStatus(t *testing.T) {
	e, nas, cloud := twoHosts(t)
	owner, _ := e.setupOwner()
	mia, _ := e.opsUser(owner, "allow container.metrics.read @container:"+nas.env+"/web")
	base := "/api/v1/environments/" + nas.env + "/containers"
	var page struct {
		Items []dockerContainer `json:"items"`
	}
	r := mia.must(http.StatusOK, http.MethodGet, base, nil)
	r.json(t, &page)
	if len(page.Items) != 1 || page.Items[0].Name != "web" || page.Items[0].View != "minimal" ||
		!slices.Equal(page.Items[0].Actions, []string{"container.metrics.read"}) {
		t.Fatalf("metrics-only list %s", r.body)
	}
	for _, leak := range []string{"nginx", `"labels"`, `"ports"`, `"details"`, `"imageId"`, `"command"`} {
		if strings.Contains(string(r.body), leak) {
			t.Fatalf("metrics-only list leaks %s: %s", leak, r.body)
		}
	}
	mia.fail(http.StatusNotFound, "not_found", http.MethodGet, base+"/db", nil)
	mia.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/environments/"+cloud.env+"/containers/web", nil)
	for _, verb := range []string{"restart", "stop", "start"} {
		mia.fail(http.StatusForbidden, "forbidden", http.MethodPost, base+"/web/"+verb, nil)
	}
	mia.fail(http.StatusForbidden, "forbidden", http.MethodDelete, base+"/web", nil)
	var images struct {
		Items []any `json:"items"`
	}
	mia.must(http.StatusOK, http.MethodGet, "/api/v1/environments/"+nas.env+"/images", nil).json(t, &images)
	if len(images.Items) != 0 {
		t.Fatalf("metrics-only user sees images %+v", images)
	}
}

// TestPullWithRegistryConnection (#6 with #19): a pull of a private image
// selects the matching manager-owned registry connection; only its ID is
// in the job, the credential reaches the agent with the dispatched
// attempt and the Engine pull, and never a table, response or log.
func TestPullWithRegistryConnection(t *testing.T) {
	e, nas, _ := twoHosts(t)
	owner, _ := e.setupOwner()
	secret := e.secrets.New(canary.RegistryCredential, "ghcr token")
	var conn struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{"name": "GHCR", "host": "ghcr.io",
		"username": "robot", "secret": secret, "repositoryPattern": "org/*"}).json(t, &conn)
	pulls := "/api/v1/environments/" + nas.env + "/images/pulls"

	id := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, pulls, map[string]any{"reference": "ghcr.io/org/private:1"}))
	j, err := e.m.Jobs().Get(testutil.Context(t), id)
	if err != nil || !strings.Contains(string(j.Input), `"registryConnections":["`+conn.ID+`"]`) {
		t.Fatalf("pull job input %s %v", j.Input, err)
	}
	if done := e.runJob(id); done.State != domain.JobSucceeded {
		t.Fatalf("authenticated pull %+v", done)
	}
	auths := nas.engine.PullAuths()
	if len(auths) != 1 || auths[0] == nil || auths[0].Username != "robot" || string(auths[0].Password) != secret || auths[0].ServerAddress != "ghcr.io" {
		t.Fatalf("Engine pull auth %+v", auths)
	}
	// An image of another registry pulls anonymously.
	e.runJob(jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, pulls, map[string]any{"reference": "alpine:3.22"})))
	if a := nas.engine.PullAuths(); len(a) != 2 || a[1] != nil {
		t.Fatalf("anonymous pull auth %+v", a)
	}
	// An unknown explicit connection is refused before any job.
	owner.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, pulls,
		map[string]any{"reference": "ghcr.io/org/private:1", "registryConnectionId": "0190a6e0-0000-7000-8000-00000000dead"})
	e.secrets.AssertClean(t, "stored rows", e.tableDump("jobs", "job_events", "audit_events"))
}
