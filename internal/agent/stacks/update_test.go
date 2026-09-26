package stacks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// The #20 update fixture: a real project directory (Compose file, an
// override file, .env for interpolation and a service env_file) on the
// in-memory Engine. db has a health check, web restarts with db
// (restart: true), worker shares web's tag without restart propagation and
// tools (a stopped one-shot) has its own tag.
const updateYAML = `services:
  db:
    image: ghcr.io/acme/db:${DB_TAG}
    healthcheck:
      test: ["CMD", "/check"]
  web:
    image: ghcr.io/acme/web:1.4
    env_file: [web.env]
    depends_on:
      db:
        condition: service_healthy
        restart: true
  worker:
    image: ghcr.io/acme/web:1.4
    depends_on: [db]
  tools:
    image: ghcr.io/acme/tools:latest
    depends_on: [db]
`

const (
	webRef   = "ghcr.io/acme/web:1.4"
	dbRef    = "ghcr.io/acme/db:16"
	toolsRef = "ghcr.io/acme/tools:latest"
)

func dg(c string) string { return "sha256:" + strings.Repeat(c, 64) }

type updateEnv struct {
	t    *testing.T
	root string
	dir  string
	eng  *enginefake.Engine
	c    *fakeComposer
	svc  *Service
	// ids are the service containers' IDs before the update.
	ids map[string]string
}

func newUpdateEnv(t *testing.T) *updateEnv {
	t.Helper()
	ctx := testutil.Context(t)
	root := t.TempDir()
	res := &storage.Result{StacksDir: filepath.ToSlash(root), Roots: []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(root), OK: true}}}
	e := &updateEnv{t: t, root: root, dir: filepath.Join(root, "app"), eng: enginefake.New("e1"), c: &fakeComposer{}, ids: map[string]string{}}
	e.svc = New(Options{Deps: fakeDeps{c: e.c, eng: e.eng, st: res}, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)})
	writeTree(t, e.dir, map[string]string{
		"compose.yaml":          updateYAML,
		"compose.override.yaml": "services:\n  web:\n    labels:\n      team: shop\n",
		".env":                  "DB_TAG=16\n",
		"web.env":               "API_KEY=do-not-touch\n",
	})
	// The applied images, pulled from the "registry".
	for ref, d := range map[string]string{dbRef: dg("1"), webRef: dg("2"), toolsRef: dg("3")} {
		e.eng.Publish(ref, d)
		if _, err := e.eng.PullImage(ctx, ref, engine.PullOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e.eng.SetStartHealth(dbRef, "healthy")
	deps := map[string][]lifecycle.Dependency{
		"web":    {{Service: "db", Condition: lifecycle.ConditionHealthy, Required: true, Restart: true}},
		"worker": {{Service: "db", Condition: lifecycle.ConditionStarted, Required: true}},
		"tools":  {{Service: "db", Condition: lifecycle.ConditionStarted, Required: true}},
	}
	for _, s := range []struct {
		name, ref string
		running   bool
	}{{"db", dbRef, true}, {"web", webRef, true}, {"worker", webRef, true}, {"tools", toolsRef, false}} {
		spec := e.containerSpec(s.name, s.ref, deps[s.name])
		id := e.eng.AddContainer(spec, false)
		if s.running {
			if err := e.eng.StartContainer(ctx, id); err != nil {
				t.Fatal(err)
			}
		} else if err := e.eng.StopContainer(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		e.ids[s.name] = id
	}
	// Compose's create: containers whose image changed are replaced by
	// created (not started) ones, others are left alone.
	e.c.onCreate = func(p *compose.Project, o compose.CreateOptions) error {
		for _, name := range o.Services {
			var svc compose.ServiceInfo
			for _, s := range p.Services {
				if s.Name == name {
					svc = s
				}
			}
			img, err := e.eng.InspectImage(ctx, svc.Image)
			if err != nil {
				return err
			}
			list, err := lifecycle.ProjectContainers(ctx, e.eng, p.Name)
			if err != nil {
				return err
			}
			for _, c := range list {
				if c.Labels[lifecycle.ComposeServiceLabel] != name || c.ImageID == img.ID {
					continue
				}
				if err := e.eng.RemoveContainer(ctx, c.ID, engine.RemoveOptions{Force: true}); err != nil {
					return err
				}
				var ds []lifecycle.Dependency
				for _, d := range svc.DependsOn {
					ds = append(ds, lifecycle.Dependency{Service: d.Service, Condition: d.Condition, Required: d.Required, Restart: d.Restart})
				}
				if _, _, err := e.eng.CreateContainer(ctx, e.containerSpec(name, svc.Image, ds)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return e
}

func (e *updateEnv) containerSpec(service, ref string, deps []lifecycle.Dependency) engine.ContainerSpec {
	spec := engine.ContainerSpec{Name: "app-" + service + "-1", Image: ref, Labels: map[string]string{
		lifecycle.ComposeProjectLabel: "app", lifecycle.ComposeServiceLabel: service, lifecycle.DependsOnLabel: lifecycle.FormatDependsOn(deps)}}
	if service == "db" {
		spec.Healthcheck = &engine.HealthcheckSpec{Test: []string{"CMD", "/check"}}
	}
	return spec
}

// sourceHash is the applied revision's hash (the definition as deployed).
func (e *updateEnv) sourceHash() string {
	e.t.Helper()
	out, err := call[protocol.ComposeReadOutput](e.t, e.svc.Requests()[protocol.ReqComposeRead], protocol.ComposeReadInput{Stack: ref("app")})
	if err != nil {
		e.t.Fatal(err)
	}
	return out.Snapshot.Hash
}

func (e *updateEnv) input(services ...protocol.UpdateService) protocol.UpdateRunInput {
	r := ref("app")
	return protocol.UpdateRunInput{PolicyID: "p1", StackID: "s1", Stack: &r, ExpectSourceHash: e.sourceHash(), Services: services}
}

func (e *updateEnv) run(in protocol.UpdateRunInput, secrets *protocol.CommandSecrets) (protocol.ResultPayload, protocol.UpdateRunOutput) {
	e.t.Helper()
	return runUpdate(e.t, e.svc, in, secrets)
}

func runUpdate(t *testing.T, s *Service, in protocol.UpdateRunInput, secrets *protocol.CommandSecrets) (protocol.ResultPayload, protocol.UpdateRunOutput) {
	t.Helper()
	var exec jobexec.Executor
	for _, x := range s.Executors() {
		if x.Kind == jobspec.UpdateRun {
			exec = x
		}
	}
	if err := exec.Validate(domain.ExecutorAgent); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(in)
	st := &jobexec.State{JobID: "job-1", Attempt: 1, Kind: jobspec.UpdateRun, Input: b, Secrets: secrets}
	res, err := jobexec.Run(testutil.Context(t), exec, st, jobexec.Options{Journal: &memJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	var out protocol.UpdateRunOutput
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

// container returns the service's current container.
func (e *updateEnv) container(service string) enginefake.Container {
	e.t.Helper()
	c, ok := e.eng.Container("app-" + service + "-1")
	if !ok {
		e.t.Fatalf("no container for %s", service)
	}
	return c
}

// assertSourcesUnchanged compares every file of the project directory
// byte for byte (and its modification time) with the snapshot.
func assertSourcesUnchanged(t *testing.T, before, after map[string]fileState) {
	t.Helper()
	if len(after) != len(before) {
		t.Errorf("files before %d, after %d", len(before), len(after))
	}
	for p, st := range before {
		if a, ok := after[p]; !ok || !bytes.Equal(a.content, st.content) || !a.mod.Equal(st.mod) {
			t.Errorf("%s changed during the update", filepath.Base(p))
		}
	}
}

func outcome(o protocol.UpdateRunOutput, service string) string {
	if r := o.Result(service); r != nil {
		return r.Outcome
	}
	return ""
}

func TestUpdateRecreatesChangedServicesAndKeepsSourceBytes(t *testing.T) {
	e := newUpdateEnv(t)
	before := snapshotTree(t, e.dir)
	hash := e.sourceHash()
	e.eng.Publish(webRef, dg("4")) // the same tag now names a new build
	res, out := e.run(e.input(
		protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("4")},
		protocol.UpdateService{Service: "worker", Reference: webRef, Digest: dg("4")},
		protocol.UpdateService{Service: "db", Reference: dbRef, Digest: dg("1")},
	), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	assertSourcesUnchanged(t, before, snapshotTree(t, e.dir))
	if out.Stage != protocol.UpdateStageDone || out.SourceHashBefore != hash || out.SourceHashAfterPull != hash || out.SourceHashAfter != hash {
		t.Errorf("stage %s hashes %s %s %s, want %s", out.Stage, out.SourceHashBefore, out.SourceHashAfterPull, out.SourceHashAfter, hash)
	}
	if outcome(out, "web") != protocol.UpdateUpdated || outcome(out, "worker") != protocol.UpdateUpdated || outcome(out, "db") != protocol.UpdateUnchanged {
		t.Fatalf("outcomes %+v", out.Services)
	}
	web := out.Result("web")
	if web.FromDigest != dg("2") || web.ToDigest != dg("4") || web.FromImageID == web.ToImageID {
		t.Errorf("web result %+v", web)
	}
	for _, s := range []string{"web", "worker"} {
		c := e.container(s)
		if c.Details.ImageID != web.ToImageID || !c.Details.State.Running || c.Details.ID == e.ids[s] {
			t.Errorf("%s: %+v, want a new running container of the new image", s, c.Details.State)
		}
	}
	// db's digest is unchanged: never recreated; tools was not a candidate.
	if e.container("db").Details.ID != e.ids["db"] || e.container("tools").Details.ID != e.ids["tools"] || e.container("tools").Details.State.Running {
		t.Error("db or tools was touched")
	}
	if got := e.c.calls; !slices.Equal(got, []string{"create:app:web,worker"}) {
		t.Errorf("compose calls %v", got)
	}
	if len(out.Restarted) != 0 {
		t.Errorf("restarted %v", out.Restarted)
	}
}

func TestUpdateUnchangedDigestIsANoOp(t *testing.T) {
	e := newUpdateEnv(t)
	res, out := e.run(e.input(protocol.UpdateService{Service: "db", Reference: dbRef, Digest: dg("1")}), nil)
	if res.Outcome != jobexec.OutcomeSucceeded || outcome(out, "db") != protocol.UpdateUnchanged {
		t.Fatalf("result %+v output %+v", res, out.Services)
	}
	if len(e.c.calls) != 0 || e.container("db").Details.ID != e.ids["db"] {
		t.Errorf("recreated although the digest is unchanged: %v", e.c.calls)
	}
}

func TestUpdateRestartsDependentsAndPreservesStoppedServices(t *testing.T) {
	e := newUpdateEnv(t)
	e.eng.Publish(dbRef, dg("5"))
	e.eng.Publish(toolsRef, dg("6"))
	if _, err := e.eng.PullImage(testutil.Context(t), dbRef, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	e.eng.SetStartHealth(dbRef, "healthy")
	res, out := e.run(e.input(
		protocol.UpdateService{Service: "db", Reference: dbRef, Digest: dg("5")},
		protocol.UpdateService{Service: "tools", Reference: toolsRef, Digest: dg("6")},
	), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	// web declares restart: true on db and ran: restarted, not recreated;
	// the worker does not declare it: untouched; tools was stopped: kept
	// stopped (not recreated, not started).
	if !slices.Equal(out.Restarted, []string{"web"}) || outcome(out, "db") != protocol.UpdateUpdated || outcome(out, "tools") != protocol.UpdateKeptStopped {
		t.Fatalf("restarted %v outcomes %+v", out.Restarted, out.Services)
	}
	if c := e.container("web"); c.Details.ID != e.ids["web"] || !c.Details.State.Running {
		t.Errorf("web %+v", c.Details.State)
	}
	if c := e.container("tools"); c.Details.ID != e.ids["tools"] || c.Details.State.Running {
		t.Errorf("tools %+v", c.Details.State)
	}
	if c := e.container("worker"); c.Details.ID != e.ids["worker"] || !c.Details.State.Running {
		t.Errorf("worker %+v", c.Details.State)
	}
	if got := e.c.calls; !slices.Equal(got, []string{"create:app:db"}) {
		t.Errorf("compose calls %v", got)
	}
}

// containerActions returns the Engine's start/stop events of the
// project's containers as "action:service", in order.
func (e *updateEnv) containerActions() []string {
	var out []string
	for _, ev := range e.eng.EmittedEvents() {
		if ev.Type != "container" || (ev.Action != "start" && ev.Action != "stop") {
			continue
		}
		out = append(out, ev.Action+":"+strings.TrimSuffix(strings.TrimPrefix(ev.Attributes["name"], "app-"), "-1"))
	}
	return out
}

// An automatic update follows the Compose file's depends_on: the dependent
// declaring restart: true stops before its dependency and starts only
// after the recreated dependency is healthy; a dependent without restart
// propagation keeps running and a stopped one stays stopped.
func TestUpdateFollowsDependsOnOrder(t *testing.T) {
	e := newUpdateEnv(t)
	e.eng.Publish(dbRef, dg("5"))
	if _, err := e.eng.PullImage(testutil.Context(t), dbRef, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	e.eng.SetStartHealth(dbRef, "healthy")
	start := len(e.containerActions())
	res, out := e.run(e.input(protocol.UpdateService{Service: "db", Reference: dbRef, Digest: dg("5")}), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("result %+v", res)
	}
	got := e.containerActions()[start:]
	if want := []string{"stop:web", "stop:db", "start:db", "start:web"}; !slices.Equal(got, want) {
		t.Fatalf("container actions %v, want %v (dependents stop first, dependencies start first)", got, want)
	}
	if !slices.Equal(out.Restarted, []string{"web"}) || outcome(out, "db") != protocol.UpdateUpdated {
		t.Errorf("restarted %v outcomes %+v", out.Restarted, out.Services)
	}
}

// An update that would need a stopped required dependency is refused
// before anything is stopped or recreated (and nothing is quarantined).
func TestUpdateRefusesWhenARequiredDependencyIsStopped(t *testing.T) {
	e := newUpdateEnv(t)
	if err := e.eng.StopContainer(testutil.Context(t), e.ids["db"], nil); err != nil {
		t.Fatal(err)
	}
	e.eng.Publish(webRef, dg("8"))
	start := len(e.containerActions())
	res, out := e.run(e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("8")}), nil)
	if res.Outcome != jobexec.OutcomeFailed {
		t.Fatalf("result %+v", res)
	}
	if out.Quarantine {
		t.Error("a refused update quarantined the digest")
	}
	if got := e.containerActions()[start:]; len(got) != 0 {
		t.Errorf("container actions %v: nothing may be stopped or started", got)
	}
	if slices.ContainsFunc(e.c.calls, func(c string) bool { return strings.HasPrefix(c, "create:") }) {
		t.Errorf("compose calls %v: nothing may be recreated", e.c.calls)
	}
	if c := e.container("web"); c.Details.ID != e.ids["web"] || !c.Details.State.Running {
		t.Errorf("web %+v", c.Details.State)
	}
}

func TestUpdateFailedHealthIsQuarantinedWithoutTouchingSources(t *testing.T) {
	e := newUpdateEnv(t)
	before := snapshotTree(t, e.dir)
	e.eng.Publish(webRef, dg("7"))
	if _, err := e.eng.PullImage(testutil.Context(t), webRef, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	e.eng.SetStartHealth(webRef, "unhealthy")
	res, out := e.run(e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("7")}), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.UpdateClassUnhealthy {
		t.Fatalf("result %+v", res)
	}
	if !out.Quarantine || outcome(out, "web") != protocol.UpdateFailed || out.Stage != protocol.UpdateStageConfirm {
		t.Fatalf("output %+v", out)
	}
	// No rollback; the guidance names the previous digest to pin.
	if !strings.Contains(res.Recovery, "no automatic rollback") || !strings.Contains(res.Recovery, "ghcr.io/acme/web@"+dg("2")) {
		t.Errorf("recovery %q", res.Recovery)
	}
	assertSourcesUnchanged(t, before, snapshotTree(t, e.dir))
	if out.SourceHashAfter != out.SourceHashBefore {
		t.Error("source hash changed")
	}
}

func TestUpdateRefusesUndeployedEditsBeforePulling(t *testing.T) {
	e := newUpdateEnv(t)
	in := e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("4")})
	// The user edits the definition after the deploy: the update must not
	// deploy that edit.
	writeTree(t, e.dir, map[string]string{".env": "DB_TAG=17\n"})
	before := snapshotTree(t, e.dir)
	e.eng.Publish(webRef, dg("4"))
	res, _ := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.UpdateClassSourceChanged {
		t.Fatalf("result %+v", res)
	}
	if pullCalls(e.eng) != 3 || len(e.c.calls) != 0 { // the fixture's own three pulls
		t.Errorf("pulled or recreated: %v %v", e.eng.Calls(), e.c.calls)
	}
	assertSourcesUnchanged(t, before, snapshotTree(t, e.dir))
}

func TestUpdateRefusesATagThatMovedAgain(t *testing.T) {
	e := newUpdateEnv(t)
	e.eng.Publish(webRef, dg("8")) // not the checked candidate dg("4")
	res, out := e.run(e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("4"), IndexDigest: dg("9")}), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.UpdateClassCandidateChanged || out.Quarantine {
		t.Fatalf("result %+v quarantine %v", res, out.Quarantine)
	}
	if len(e.c.calls) != 0 || e.container("web").Details.ID != e.ids["web"] {
		t.Error("recreated a service whose tag moved to an unchecked digest")
	}
}

func TestUpdateAcceptsTheIndexDigest(t *testing.T) {
	e := newUpdateEnv(t)
	// Multi-platform image: the Engine records the index digest.
	e.eng.Publish(webRef, dg("a"))
	res, out := e.run(e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("b"), IndexDigest: dg("a")}), nil)
	if res.Outcome != jobexec.OutcomeSucceeded || outcome(out, "web") != protocol.UpdateUpdated {
		t.Fatalf("result %+v output %+v", res, out.Services)
	}
}

func TestUpdateNeverPullsAnonymouslyWhenAConnectionIsNamed(t *testing.T) {
	e := newUpdateEnv(t)
	e.eng.Publish(webRef, dg("4"))
	in := e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("4"), RegistryConnection: "conn-1"})
	in.RegistryConnections = []string{"conn-1"}
	res, _ := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != classCredentialUnavailable {
		t.Fatalf("result %+v", res)
	}
	if len(e.eng.PullAuths()) != 3 { // the fixture's own three pulls
		t.Errorf("pulls %d", len(e.eng.PullAuths()))
	}
	// With the credential the pull authenticates.
	secrets := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{{ConnectionID: "conn-1", Host: "ghcr.io", Username: "bot", Secret: "pw"}}}
	res, _ = e.run(in, secrets)
	auths := e.eng.PullAuths()
	if res.Outcome != jobexec.OutcomeSucceeded || auths[len(auths)-1] == nil || auths[len(auths)-1].Username != "bot" {
		t.Fatalf("result %+v auth %+v", res, auths[len(auths)-1])
	}
}

func TestUpdateRegistryRefusalIsReportedNotRetried(t *testing.T) {
	e := newUpdateEnv(t)
	e.eng.Fail("image.pull", enginefake.Err("image.pull", engine.CodeRateLimited, "toomanyrequests"))
	res, out := e.run(e.input(protocol.UpdateService{Service: "web", Reference: webRef, Digest: dg("4")}), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != string(engine.CodeRateLimited) || out.Quarantine {
		t.Fatalf("result %+v", res)
	}
	if pulls := pullCalls(e.eng); pulls != 4 { // the fixture's three, then exactly one attempt
		t.Errorf("pull calls %d", pulls)
	}
}

// Standalone containers.

func standaloneFixture(t *testing.T, running bool) (*updateEnv, protocol.UpdateRunInput) {
	t.Helper()
	e := newUpdateEnv(t)
	ctx := testutil.Context(t)
	const ref = "ghcr.io/acme/api:3"
	e.eng.Publish(ref, dg("c"))
	if _, err := e.eng.PullImage(ctx, ref, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := protocol.ContainerSpec{Name: "api", Image: ref, Env: []string{"TOKEN=keep-me"}, RestartPolicy: "unless-stopped",
		Mounts: []protocol.MountSpec{{Type: "volume", Target: "/cache"}}}
	own := map[string]string{protocol.LabelManaged: protocol.ManagedStandalone, protocol.LabelSpec: "spec-1"}
	id := e.eng.AddContainer(engine.ContainerSpec{Name: "api", Image: ref, Env: spec.Env, Labels: own,
		Mounts: []engine.MountSpec{{Type: "volume", Target: "/cache"}}}, false)
	if running {
		if err := e.eng.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	e.eng.Publish(ref, dg("d"))
	in := protocol.UpdateRunInput{PolicyID: "p2", Container: &protocol.UpdateContainer{Name: "api", ID: id, Spec: spec, Ownership: own},
		Services: []protocol.UpdateService{{Service: "api", Reference: ref, Digest: dg("d")}}}
	return e, in
}

func TestUpdateStandaloneContainerFromItsSavedSpec(t *testing.T) {
	e, in := standaloneFixture(t, true)
	oldVol := ""
	if c, ok := e.eng.Container("api"); ok {
		oldVol = c.Details.Mounts[0].Name
	}
	res, out := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeSucceeded || outcome(out, "api") != protocol.UpdateUpdated {
		t.Fatalf("result %+v output %+v", res, out)
	}
	c, ok := e.eng.Container("api")
	if !ok || c.Details.ID == in.Container.ID || !c.Details.State.Running || c.Details.ImageID != out.Services[0].ToImageID {
		t.Fatalf("container %+v", c.Details)
	}
	if c.Details.Image != "ghcr.io/acme/api:3" || !slices.Equal(c.Env, []string{"TOKEN=keep-me"}) ||
		c.Details.Labels[protocol.LabelSpec] != "spec-1" || c.Details.RestartPolicy != "unless-stopped" {
		t.Errorf("recreated container %+v env %v", c.Details, c.Env)
	}
	if len(c.Details.Mounts) != 1 || c.Details.Mounts[0].Name != oldVol {
		t.Errorf("anonymous volume not carried over: %+v, want %s", c.Details.Mounts, oldVol)
	}
	if _, ok := e.eng.Container(in.Container.ID); ok {
		t.Error("the replaced container still exists")
	}
}

func TestUpdateStandaloneStoppedContainerStaysStopped(t *testing.T) {
	e, in := standaloneFixture(t, false)
	res, out := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeSucceeded || outcome(out, "api") != protocol.UpdateUpdated {
		t.Fatalf("result %+v", res)
	}
	if c, _ := e.eng.Container("api"); c.Details.State.Running || c.Details.ID == in.Container.ID {
		t.Errorf("container %+v", c.Details.State)
	}
}

func TestUpdateStandaloneUnhealthyIsQuarantined(t *testing.T) {
	e, in := standaloneFixture(t, true)
	in.Container.Spec.Healthcheck = &protocol.HealthcheckSpec{Test: []string{"CMD", "/check"}}
	if _, err := e.eng.PullImage(testutil.Context(t), in.Services[0].Reference, engine.PullOptions{}); err != nil {
		t.Fatal(err)
	}
	e.eng.SetStartHealth(in.Services[0].Reference, "unhealthy")
	res, out := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.UpdateClassUnhealthy || !out.Quarantine {
		t.Fatalf("result %+v output %+v", res, out)
	}
}

func TestUpdateStandaloneRefusesProtectedOrReplacedContainers(t *testing.T) {
	e, in := standaloneFixture(t, true)
	// The container is the agent itself (#32).
	e.svc.opts.Guard = protect.New(protect.Options{SelfContainerID: in.Container.ID, Logger: testutil.Logger(t)})
	res, _ := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != "protected" {
		t.Fatalf("protected: %+v", res)
	}
	e.svc.opts.Guard = nil
	// A container replaced under the same name since the plan.
	in.Container.ID = "gone"
	res, _ = e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != protocol.UpdateClassRecreated {
		t.Fatalf("replaced: %+v", res)
	}
	if c, _ := e.eng.Container("api"); !c.Details.State.Running {
		t.Error("a refused update stopped the container")
	}
}

func TestUpdateStandaloneCreateFailurePutsTheOldContainerBack(t *testing.T) {
	e, in := standaloneFixture(t, true)
	e.eng.Fail("container.create", enginefake.Err("container.create", engine.CodeInvalidArgument, "bad spec"))
	res, out := e.run(in, nil)
	if res.Outcome != jobexec.OutcomeFailed || !out.Quarantine {
		t.Fatalf("result %+v", res)
	}
	c, ok := e.eng.Container("api")
	if !ok || c.Details.ID != in.Container.ID || !c.Details.State.Running {
		t.Fatalf("old container not restored: %+v", c.Details)
	}
}

func TestUpdateInputValidation(t *testing.T) {
	r := ref("app")
	ok := protocol.UpdateRunInput{Stack: &r, ExpectSourceHash: strings.Repeat("a", 64),
		Services: []protocol.UpdateService{{Service: "web", Reference: webRef, Digest: dg("1")}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*protocol.UpdateRunInput){
		"no target":       func(in *protocol.UpdateRunInput) { in.Stack = nil },
		"no hash":         func(in *protocol.UpdateRunInput) { in.ExpectSourceHash = "" },
		"pinned":          func(in *protocol.UpdateRunInput) { in.Services[0].Reference = "ghcr.io/acme/web@" + dg("1") },
		"bad digest":      func(in *protocol.UpdateRunInput) { in.Services[0].Digest = "sha256:xyz" },
		"duplicate":       func(in *protocol.UpdateRunInput) { in.Services = append(in.Services, in.Services[0]) },
		"no services":     func(in *protocol.UpdateRunInput) { in.Services = nil },
		"stack+container": func(in *protocol.UpdateRunInput) { in.Container = &protocol.UpdateContainer{Name: "x", ID: "y"} },
	} {
		in := ok
		in.Services = slices.Clone(ok.Services)
		mut(&in)
		if in.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func pullCalls(eng *enginefake.Engine) int {
	n := 0
	for _, c := range eng.Calls() {
		if c == "image.pull" {
			n++
		}
	}
	return n
}
