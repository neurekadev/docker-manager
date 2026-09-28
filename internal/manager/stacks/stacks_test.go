package stacks_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/stacks"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

const shopYAML = `services:
  db:
    image: registry.example:5000/db:${DB_TAG}
    labels:
      dev.neureka.docker-manager.description: Orders database
  web:
    image: nginx:1.27
    depends_on:
      db:
        condition: service_started
    volumes:
      - ./html:/usr/share/nginx/html:ro
`

const shopEnv = "DB_TAG=16\nDB_PASSWORD=s3cret-canary\n"

func revisions(t *testing.T, h *harness, id string) []domain.StackRevision {
	t.Helper()
	revs, err := h.svc.Revisions(h.ctx, id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return revs
}

func TestCreateWritesProjectAndRecordsFirstRevision(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.Subscribe(0, nil)
	defer sub.Close()
	st := h.create("shop", shopYAML, shopEnv)

	if h.read("shop", "compose.yaml") != shopYAML || h.read("shop", ".env") != shopEnv {
		t.Fatal("the definition was not written into the stacks volume")
	}
	if st.Status != domain.StackUndeployed || st.Root != domain.StackRootStacks || st.Dir != "shop" || st.Origin != domain.StackOriginCreated {
		t.Errorf("stack %+v", st)
	}
	if st.Applied != nil || st.Observed == nil || st.Observed.Seq != 1 || !st.UndeployedChanges() {
		t.Errorf("revisions: applied %+v observed %+v", st.Applied, st.Observed)
	}
	if st.ServiceMeta["db"].Description != "Orders database" {
		t.Errorf("label metadata not imported: %+v", st.ServiceMeta)
	}
	if len(st.Services) != 2 || len(st.Binds) != 1 || st.Binds[0].RelPath != "html" {
		t.Errorf("services %+v binds %+v", st.Services, st.Binds)
	}
	revs := revisions(t, h, st.ID)
	if len(revs) != 1 || revs[0].Source != domain.RevisionEditor || revs[0].AuthorUserID != "alice" {
		t.Fatalf("revisions %+v", revs)
	}
	rev, err := h.svc.Revision(h.ctx, st.ID, revs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, f := range rev.Files {
		contents[f.Path] = string(f.Content)
	}
	if contents["compose.yaml"] != shopYAML || contents[".env"] != shopEnv {
		t.Errorf("revision contents %v", contents)
	}
	// .env values are sealed at rest: the canary is nowhere in the database.
	var raw []string
	if err := h.db.NewRaw("SELECT content || files FROM stack_revisions").Scan(h.ctx, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		if strings.Contains(r, "s3cret-canary") || strings.Contains(r, "DB_PASSWORD") {
			t.Fatal("revision content is stored in plaintext")
		}
	}
	if e := <-sub.C(); e.Type != events.StackCreated || e.ResourceID != st.ID || e.ResourceType != events.ResourceStack {
		t.Errorf("event %+v", e)
	}
}

func TestCreateNeverOverwrites(t *testing.T) {
	h := newHarness(t)
	h.create("shop", shopYAML, "DB_TAG=16\n")

	// A second stack with the same project name.
	_, _, err := h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "shop",
		Files: files("services: {}\n", "")}})
	if !errors.Is(err, domain.ErrStackNameTaken) {
		t.Errorf("same name: %v", err)
	}
	// A directory that exists in the stacks volume but is not managed.
	h.write("services:\n  old:\n    image: old\n", "legacy", "compose.yaml")
	_, _, err = h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "legacy",
		Files: files("services:\n  new:\n    image: new\n", "")}})
	if stackErrCode(err) != domain.StackErrDirectoryExists {
		t.Errorf("existing directory: %v", err)
	}
	if h.read("legacy", "compose.yaml") != "services:\n  old:\n    image: old\n" {
		t.Error("an existing project directory was overwritten")
	}
	// A Compose project of that name already runs on the Engine.
	h.engine.setProject("running", []engine.Container{{ID: "r1", Names: []string{"/running-app-1"}, State: "running",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "running", lifecycle.ComposeServiceLabel: "app"}}})
	_, _, err = h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "running",
		Files: files("services:\n  app:\n    image: app\n", "")}})
	if stackErrCode(err) != domain.StackErrProjectExists {
		t.Errorf("running project: %v", err)
	}
	// An invalid definition is refused with the findings; nothing is written.
	_, v, err := h.svc.Create(h.ctx, alice, domain.StackCreate{StackDefinition: domain.StackDefinition{EnvironmentID: env, Name: "bad",
		Files: files("services:\n  x:\n    image: a\n    use_api_socket: true\n", "")}})
	var se *domain.StackError
	if !errors.As(err, &se) || se.Code != domain.StackErrInvalidDefinition || len(se.Issues) != 1 ||
		se.Issues[0].Code != protocol.IssueUnsupportedFeature || v.Valid {
		t.Errorf("invalid definition: %v %+v", err, v)
	}
	if _, err := h.svc.Get(h.ctx, "bad"); !errors.Is(err, domain.ErrStackNotFound) {
		t.Error("an invalid stack was stored")
	}
	if _, err := os.Stat(h.path("bad")); !errors.Is(err, os.ErrNotExist) {
		t.Error("an invalid definition was written")
	}
}

func TestDeployRecordsAppliedRevisionAndLeavesSourcesUntouched(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	j, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.run(); !slices.Equal(done, []string{j.ID}) {
		t.Fatalf("ran %v", done)
	}
	if j = h.job(j.ID); j.State != domain.JobSucceeded {
		t.Fatalf("deploy %s: %s %s", j.State, j.ErrorClass, j.ErrorMessage)
	}
	st = h.get(st.ID)
	if st.Status != domain.StackDeployed || st.Applied == nil || st.Applied.Seq != 2 || st.UndeployedChanges() {
		t.Fatalf("after deploy: status %s applied %+v observed %+v", st.Status, st.Applied, st.Observed)
	}
	revs := revisions(t, h, st.ID)
	if revs[0].Source != domain.RevisionDeploy || revs[0].JobID != j.ID || revs[0].AuthorUserID != "alice" || revs[0].Hash != revs[1].Hash {
		t.Errorf("deploy revision %+v (created %+v)", revs[0], revs[1])
	}
	// Deploys only read: the Compose and env files are byte-for-byte the
	// ones created, and nothing wrote them.
	if h.read("shop", "compose.yaml") != shopYAML || h.read("shop", ".env") != shopEnv {
		t.Error("the deploy modified the definition")
	}
	writes := 0
	for _, r := range h.agents.Requests() {
		if r == protocol.ReqComposeWrite {
			writes++
		}
	}
	if writes != 1 {
		t.Errorf("%d compose.write requests, want only the creation's", writes)
	}
	if len(st.Images) != 2 || st.Images[0].Image != "registry.example:5000/db:16" || st.Images[0].ImageID != "sha256:db" {
		t.Errorf("images %+v", st.Images)
	}
	if st.EngineState != domain.EngineStateRunning {
		t.Errorf("engine state %s", st.EngineState)
	}
	if st.LastJobID != j.ID || st.LastJobKind != jobspec.StackDeploy {
		t.Errorf("last job %s %s", st.LastJobID, st.LastJobKind)
	}
}

func TestObservedChangesAndRestore(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	j, _ := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	h.run()
	st = h.get(st.ID)
	applied := *st.Applied

	// An external edit (watcher, #23) is recorded and shows undeployed changes.
	edited := strings.Replace(shopYAML, "nginx:1.27", "nginx:1.28", 1)
	h.write(edited, "shop", "compose.yaml")
	rev, err := h.svc.RecordObserved(h.ctx, st.ID, domain.RevisionExternal, authz.Service())
	if err != nil || rev == nil || rev.Seq != 3 || rev.Source != domain.RevisionExternal {
		t.Fatalf("observed %+v %v", rev, err)
	}
	st = h.get(st.ID)
	if !st.UndeployedChanges() || st.Applied.ID != applied.ID || st.Observed.ID != rev.ID {
		t.Fatalf("after edit: applied %+v observed %+v", st.Applied, st.Observed)
	}
	// Observing the same bytes again records nothing.
	if again, err := h.svc.RecordObserved(h.ctx, st.ID, domain.RevisionExternal, authz.Service()); err != nil || again != nil {
		t.Errorf("unchanged definition recorded again: %+v %v", again, err)
	}
	// A file-manager save of a non-definition file records nothing; a save
	// of compose.yaml does (#15 hook).
	h.write("<h1>hi</h1>", "shop", "html", "index.html")
	if r, err := h.svc.RecordFileSave(h.ctx, st.ID, "html/index.html", alice); err != nil || r != nil {
		t.Errorf("html save: %+v %v", r, err)
	}
	h.write(edited+"# saved\n", "shop", "compose.yaml")
	fm, err := h.svc.RecordFileSave(h.ctx, st.ID, "compose.yaml", alice)
	if err != nil || fm == nil || fm.Source != domain.RevisionFileManager || fm.AuthorUserID != "alice" {
		t.Fatalf("compose save: %+v %v", fm, err)
	}

	// Restoring the applied revision writes its bytes back: no undeployed
	// changes, no deploy offered, and the edit stays in history.
	res, err := h.svc.Restore(h.ctx, alice, h.get(st.ID), applied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.read("shop", "compose.yaml") != shopYAML || h.read("shop", ".env") != shopEnv {
		t.Error("restore did not write the revision's bytes")
	}
	if res.DeployOffered || res.Revision.Source != domain.RevisionRestore || res.Revision.RestoredFrom != applied.ID ||
		res.Revision.Hash != applied.Hash || res.Stack.UndeployedChanges() {
		t.Errorf("restore %+v", res)
	}
	// Restoring the edit offers a deploy and never starts one.
	res, err = h.svc.Restore(h.ctx, alice, h.get(st.ID), rev.ID)
	if err != nil || !res.DeployOffered || h.read("shop", "compose.yaml") != edited {
		t.Fatalf("restore edit: %+v %v", res, err)
	}
	if h.disp.Pending(env) != 0 {
		t.Error("a restore started a job")
	}
	if h.job(j.ID).State != domain.JobSucceeded {
		t.Error("deploy did not succeed")
	}
	seqs := []int64{}
	for _, r := range revisions(t, h, st.ID) {
		seqs = append(seqs, r.Seq)
	}
	if !slices.Equal(seqs, []int64{6, 5, 4, 3, 2, 1}) {
		t.Errorf("history %v", seqs)
	}
}

func TestRestoreRefusedWhileOffline(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.agents.setOnline(false)
	_, err := h.svc.Restore(h.ctx, alice, st, st.Observed.ID)
	if stackErrCode(err) != domain.StackErrOffline {
		t.Fatalf("offline restore: %v", err)
	}
	// The last known revision stays readable.
	if _, err := h.svc.Revision(h.ctx, st.ID, st.Observed.ID); err != nil {
		t.Errorf("revision unreadable while offline: %v", err)
	}
	v, err := h.svc.Services(h.ctx, st)
	if err != nil || v.Live {
		t.Errorf("services while offline: %+v %v", v, err)
	}
}

func TestFailedDeployKeepsLastAppliedRevision(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.deploy(st)
	h.run()
	applied := h.get(st.ID).Applied

	h.write(strings.Replace(shopYAML, "nginx:1.27", "nginx:broken", 1), "shop", "compose.yaml")
	h.comp.upErr = engine.Errorf("compose.up", engine.CodeDependencyFailed, "dependency failed to start: container shop-db-1 is unhealthy")
	j, err := h.svc.Deploy(h.ctx, alice, h.get(st.ID), domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.run()
	if j = h.job(j.ID); j.State != domain.JobFailed {
		t.Fatalf("deploy %s", j.State)
	}
	st = h.get(st.ID)
	if st.Status != domain.StackFailed || st.Applied.ID != applied.ID || st.Failed == nil || st.Failed.Hash == applied.Hash {
		t.Fatalf("failed deploy: status %s applied %+v failed %+v", st.Status, st.Applied, st.Failed)
	}
	if len(st.PreviousState) != 2 || st.PreviousState[0].ImageIDs[0] != "sha256:db" {
		t.Errorf("pre-deploy state %+v", st.PreviousState)
	}
	// Recovery: restore the applied revision and deploy it.
	if _, err := h.svc.Restore(h.ctx, alice, st, applied.ID); err != nil {
		t.Fatal(err)
	}
	h.comp.upErr = nil
	h.deploy(h.get(st.ID))
	h.run()
	st = h.get(st.ID)
	if st.Status != domain.StackDeployed || st.Failed != nil || st.Applied.Hash != applied.Hash {
		t.Errorf("after recovery: %s applied %+v failed %+v", st.Status, st.Applied, st.Failed)
	}
}

func TestDeploySelectsRegistryConnections(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.regs.byHost["registry.example:5000/"] = "reg-1"
	j := h.deploy(st)
	var in protocol.StackJobInput
	if err := json.Unmarshal(h.job(j.ID).Input, &in); err != nil {
		t.Fatal(err)
	}
	// Only the connection of the private image; nothing secret in the input.
	if !slices.Equal(in.RegistryConnections, []string{"reg-1"}) || strings.Contains(string(h.job(j.ID).Input), "pw-reg-1") {
		t.Fatalf("job input %s", h.job(j.ID).Input)
	}
	h.run()
	if h.job(j.ID).State != domain.JobSucceeded {
		t.Fatalf("deploy with credentials: %s %s", h.job(j.ID).State, h.job(j.ID).ErrorMessage)
	}
	// Ambiguous or revoked selections refuse the deploy up front.
	h.regs.err = domain.ErrRegistryConnectionRevoked
	if _, err := h.svc.Deploy(h.ctx, alice, h.get(st.ID), domain.StackJobRequest{}, domain.StackDeployOptions{}); !errors.Is(err, domain.ErrRegistryConnectionRevoked) {
		t.Errorf("revoked connection: %v", err)
	}
}

func TestTwoDeploysOfAStackSerialize(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	other := h.create("blog", "services:\n  app:\n    image: ghost:5\n", "")
	first, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Deploy(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	third, err := h.svc.Deploy(h.ctx, alice, other, domain.StackJobRequest{}, domain.StackDeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.DispatchPending(h.ctx); err != nil {
		t.Fatal(err)
	}
	// The second deploy of the same stack waits for the first (stack lock);
	// another stack's deploy runs alongside.
	if s := h.job(second.ID); s.State != domain.JobBlocked || s.BlockedReason != "lock" || s.BlockedBy != first.ID {
		t.Fatalf("second deploy: %s %s by %s", s.State, s.BlockedReason, s.BlockedBy)
	}
	if h.job(first.ID).State != domain.JobDispatched || h.job(third.ID).State != domain.JobDispatched {
		t.Fatal("independent deploys did not dispatch")
	}
	// Run what is dispatched (put the drained commands back through run).
	done := h.run()
	if !slices.Contains(done, first.ID) || slices.Contains(done, second.ID) {
		t.Fatalf("ran %v", done)
	}
	if done := h.run(); !slices.Equal(done, []string{second.ID}) {
		t.Fatalf("after the first finished, ran %v", done)
	}
	if h.job(second.ID).State != domain.JobSucceeded {
		t.Error("second deploy did not succeed")
	}
	// Each deploy recorded its own revision.
	if n := len(revisions(t, h, st.ID)); n != 3 {
		t.Errorf("%d revisions, want create + two deploys", n)
	}
}

func TestOperationsFollowJobs(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.deploy(st)
	h.run()
	if _, err := h.svc.Operate(h.ctx, alice, st, "explode", domain.StackJobRequest{}); err == nil {
		t.Error("unknown action accepted")
	}
	j, err := h.svc.Operate(h.ctx, alice, st, "stop", domain.StackJobRequest{})
	if err != nil {
		t.Fatal(err)
	}
	h.run()
	st = h.get(st.ID)
	if h.job(j.ID).State != domain.JobSucceeded || st.Status != domain.StackStopped || st.EngineState != domain.EngineStateStopped {
		t.Fatalf("after stop: job %s status %s engine %s", h.job(j.ID).State, st.Status, st.EngineState)
	}
	// Drift: the stack is stopped but a container runs again.
	h.engine.setProject("shop", []engine.Container{{ID: "x", Names: []string{"/shop-web-1"}, ImageID: "sha256:web", State: "running",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "shop", lifecycle.ComposeServiceLabel: "web"}}})
	v, err := h.svc.Services(h.ctx, st)
	if err != nil || !v.Live || !v.Drift {
		t.Fatalf("services %+v %v", v, err)
	}
	for _, sv := range v.Services {
		if sv.Name == "web" && !slices.Equal(sv.Drift, []string{stacks.DriftRunningWhileStopped}) {
			t.Errorf("web drift %v", sv.Drift)
		}
	}
	j, _ = h.svc.Operate(h.ctx, alice, h.get(st.ID), "down", domain.StackJobRequest{})
	h.run()
	if st = h.get(st.ID); st.Status != domain.StackDown || st.EngineState != domain.EngineStateMissing {
		t.Errorf("after down: %s %s", st.Status, st.EngineState)
	}
}

func TestDeleteRemovesStackButKeepsFiles(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	sub := h.bus.Subscribe(0, func(e events.Event) bool { return e.Type == events.StackRemoved })
	defer sub.Close()
	j, err := h.svc.Delete(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackRemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.run()
	if h.job(j.ID).State != domain.JobSucceeded {
		t.Fatal("remove job failed")
	}
	if _, err := h.svc.Get(h.ctx, st.ID); !errors.Is(err, domain.ErrStackNotFound) {
		t.Errorf("stack still present: %v", err)
	}
	if h.read("shop", "compose.yaml") != shopYAML {
		t.Error("the project directory was touched")
	}
	if e := <-sub.C(); e.ResourceID != st.ID {
		t.Errorf("event %+v", e)
	}
}

func TestDiscoveryAndImport(t *testing.T) {
	h := newHarness(t)
	root := strings.ReplaceAll(h.root, "\\", "/")
	lbl := func(project, wd, files string) map[string]string {
		return map[string]string{lifecycle.ComposeProjectLabel: project, lifecycle.ComposeServiceLabel: "app",
			"com.docker.compose.project.working_dir": wd, "com.docker.compose.project.config_files": files}
	}
	// "inplace" lives in the stacks volume; "legacy" in a home directory.
	h.write("services:\n  app:\n    image: app:1\n", "inplace", "compose.yaml")
	h.engine.setProject("inplace", []engine.Container{{ID: "i1", Names: []string{"/inplace-app-1"}, Image: "app:1", State: "running",
		Labels: lbl("inplace", root+"/inplace", root+"/inplace/compose.yaml")}})
	h.engine.setProject("legacy", []engine.Container{{ID: "l1", Names: []string{"/legacy-app-1"}, Image: "app:2", State: "running",
		Labels: lbl("legacy", "/home/me/legacy", "/home/me/legacy/compose.yaml")}})

	list, err := h.svc.Discovered(h.ctx, env)
	if err != nil || len(list) != 2 {
		t.Fatalf("discovered %+v %v", list, err)
	}
	if !list[0].Adoptable || list[0].Name != "inplace" || list[1].Adoptable {
		t.Errorf("discovered %+v", list)
	}

	st, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "inplace"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Origin != domain.StackOriginImported || st.Dir != "inplace" || st.Applied != nil || st.Status != domain.StackDeployed ||
		st.Observed == nil || !st.UndeployedChanges() || st.EngineState != domain.EngineStateRunning {
		t.Errorf("imported %+v", st)
	}
	if revs := revisions(t, h, st.ID); len(revs) != 1 || revs[0].Source != domain.RevisionExternal {
		t.Errorf("import revision %+v", revs)
	}
	// Importing again, or creating over it, never adopts it twice.
	if _, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "inplace"}); !errors.Is(err, domain.ErrStackNameTaken) {
		t.Errorf("second import: %v", err)
	}
	list, _ = h.svc.Discovered(h.ctx, env)
	if list[0].StackID != st.ID || list[0].Adoptable {
		t.Errorf("managed project %+v", list[0])
	}

	// Outside the roots: only with an explicit source.
	if _, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "legacy"}); stackErrCode(err) != domain.StackErrNotAdoptable {
		t.Errorf("legacy in place: %v", err)
	}
	// ... which never overwrites a directory that exists.
	h.write("keep me\n", "legacy", "notes.txt")
	src := files("services:\n  app:\n    image: app:2\n", "")
	if _, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "legacy", Files: src}); stackErrCode(err) != domain.StackErrDirectoryExists {
		t.Errorf("legacy over an existing directory: %v", err)
	}
	if h.read("legacy", "notes.txt") != "keep me\n" {
		t.Error("an existing directory was changed")
	}
	h.engine.setProject("legacy2", []engine.Container{{ID: "l2", Names: []string{"/legacy2-app-1"}, Image: "app:2", State: "running",
		Labels: lbl("legacy2", "/home/me/legacy2", "/home/me/legacy2/compose.yaml")}})
	st2, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "legacy2", Files: src})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Dir != "legacy2" || h.read("legacy2", "compose.yaml") != string(src[0].Content) {
		t.Errorf("explicit source import %+v", st2)
	}
	// A project that is not on the Engine.
	if _, err := h.svc.Import(h.ctx, alice, domain.StackImport{EnvironmentID: env, ProjectName: "ghost"}); stackErrCode(err) != domain.StackErrProjectNotFound {
		t.Errorf("unknown project: %v", err)
	}
}

func TestReconcileRecordsEditsMadeWhileOffline(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	h.agents.setOnline(false)
	h.write(shopYAML+"# edited on the host\n", "shop", "compose.yaml")
	h.agents.setOnline(true)
	h.svc.ReconcileForTest(h.ctx, env, h.agents)
	st = h.get(st.ID)
	if st.Observed.Seq != 2 || !st.UndeployedChanges() || st.EngineState != domain.EngineStateMissing || st.EngineObservedAt == nil {
		t.Errorf("after reconcile: observed %+v engine %s", st.Observed, st.EngineState)
	}
	if revs := revisions(t, h, st.ID); revs[0].Source != domain.RevisionExternal || revs[0].AuthorUserID != "" {
		t.Errorf("reconciled revision %+v", revs[0])
	}
}

func TestImageStatusEligibility(t *testing.T) {
	h := newHarness(t)
	st := domain.Stack{Services: []domain.StackServiceDef{{Name: "built", Image: "shop-built", Build: true}},
		Images: []domain.StackImage{{Service: "db", Image: "postgres:16", Digest: "sha256:1"}, {Service: "pinned", Image: "redis@sha256:2"},
			{Service: "untagged", Image: "registry:5000/app"}}}
	got := map[string]domain.StackImageView{}
	for _, v := range h.svc.ImageStatus(st) {
		got[v.Service] = v
	}
	if !got["db"].Eligible || got["pinned"].Reason != domain.UpdateReasonDigestPinned || got["untagged"].Reason != domain.UpdateReasonUntagged ||
		got["built"].Reason != domain.UpdateReasonBuildOnly {
		t.Errorf("image status %+v", got)
	}
}

func TestLocatorAndRoot(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	loc, err := h.svc.Locator().Locate(h.ctx, authz.ResourceRef{Type: "stack", ID: st.ID})
	if err != nil || !loc.Found || loc.EnvironmentID != env {
		t.Errorf("locate %+v %v", loc, err)
	}
	if loc, _ := h.svc.Locator().Locate(h.ctx, authz.ResourceRef{Type: "stack", ID: "nope"}); loc.Found {
		t.Error("unknown stack located")
	}
	ids, err := h.svc.StackIDs(h.ctx, env)
	if err != nil || len(ids) != 1 || ids["shop"] != st.ID {
		t.Errorf("stack IDs %v %v", ids, err)
	}
	root, err := h.svc.Root(h.ctx, st.ID)
	if err != nil || root.Dir != "shop" || root.Root != domain.StackRootStacks || !slices.Equal(root.DefinitionFiles, []string{".env", "compose.yaml"}) {
		t.Errorf("root %+v %v", root, err)
	}
	if !stacks.IsDefinitionFile(st, root.DefinitionFiles, "compose.override.yaml") || stacks.IsDefinitionFile(st, root.DefinitionFiles, "html/x") {
		t.Error("definition file classification")
	}
}

type ownProject string

func (p ownProject) ProjectProtection(_ context.Context, _, project string) (*protocol.Protection, error) {
	if project == string(p) {
		return &protocol.Protection{Role: "docker_manager_project", Reason: "Docker Manager's own Compose project " + project}, nil
	}
	return nil, nil
}

// TestDockerManagerProjectIsProtected (#32): deploy, stop, restart, down and
// removal of Docker Manager's own Compose project are refused before a job
// exists; start and other stacks are unaffected.
func TestDockerManagerProjectIsProtected(t *testing.T) {
	h := newHarness(t)
	h.svc.SetProtection(ownProject("docker-manager"))
	own := h.create("docker-manager", shopYAML, shopEnv)
	other := h.create("shop", shopYAML, shopEnv)
	refused := func(what string, _ domain.Job, err error) {
		t.Helper()
		var de *domain.DockerError
		if !errors.As(err, &de) || de.Code != domain.DockerProtected || !strings.Contains(de.Message, "Docker Manager") {
			t.Errorf("%s: %v", what, err)
		}
	}
	// Docker Manager redeploys itself (the agent hands its own container to
	// a helper); it never stops or deletes itself.
	if _, err := h.svc.Deploy(h.ctx, alice, own, domain.StackJobRequest{}, domain.StackDeployOptions{}); err != nil {
		t.Errorf("deploy: %v", err)
	}
	for _, action := range []string{"stop", "restart", "down"} {
		j, err := h.svc.Operate(h.ctx, alice, own, action, domain.StackJobRequest{})
		refused(action, j, err)
	}
	j, err := h.svc.Delete(h.ctx, alice, own, domain.StackJobRequest{}, domain.StackRemoveOptions{})
	refused("delete", j, err)
	if _, err := h.svc.Operate(h.ctx, alice, own, "start", domain.StackJobRequest{}); err != nil {
		t.Errorf("start: %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, alice, other, domain.StackJobRequest{}, domain.StackDeployOptions{}); err != nil {
		t.Errorf("deploy another stack: %v", err)
	}
}

// TestDeleteWithVolumesNeedsAnAgentThatSupportsIt: an agent that does not
// announce stack.remove_volumes would ignore the option and keep the
// volumes; the removal is refused instead and nothing is queued.
func TestDeleteWithVolumesNeedsAnAgentThatSupportsIt(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	_, err := h.svc.Delete(h.ctx, alice, st, domain.StackJobRequest{}, domain.StackRemoveOptions{Volumes: true})
	var se *domain.StackError
	if !errors.As(err, &se) || se.Code != domain.StackErrEnvironmentUnsupported {
		t.Fatalf("err = %v", err)
	}
	if cur := h.get(st.ID); cur.LastJobKind == jobspec.StackRemove {
		t.Error("a removal was queued")
	}
}

// TestValidateStackReadsItsOwnDirectory (#7): an existing stack's
// validation loads the definition on disk in its own project directory
// (the .env there feeds interpolation, relative binds resolve inside it),
// reports findings as the result, writes nothing and records no revision;
// an offline agent is an error.
func TestValidateStackReadsItsOwnDirectory(t *testing.T) {
	h := newHarness(t)
	st := h.create("shop", shopYAML, shopEnv)
	before := len(revisions(t, h, st.ID))
	h.write("DB_TAG=17\nDB_PASSWORD=s3cret-canary\n", "shop", ".env")

	v, err := h.svc.ValidateStack(h.ctx, h.get(st.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Valid || v.ProjectName != "shop" || len(v.Services) != 2 || len(v.Errors) != 0 {
		t.Fatalf("validation %+v", v)
	}
	images := map[string]string{}
	for _, s := range v.Services {
		images[s.Name] = s.Image
	}
	if images["db"] != "registry.example:5000/db:17" {
		t.Errorf("db image %q, want the tag of the .env on disk", images["db"])
	}
	if len(v.Binds) != 1 || v.Binds[0].External || v.Binds[0].RelPath != "html" {
		t.Errorf("binds %+v, want ./html inside the project directory", v.Binds)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "s3cret-canary") {
		t.Errorf("the result carries a .env value: %s", b)
	}

	// Findings are the answer, not an error; the files stay as they are.
	broken := "services:\n  web:\n    image: [nginx\n"
	h.write(broken, "shop", "compose.yaml")
	v, err = h.svc.ValidateStack(h.ctx, h.get(st.ID))
	if err != nil || v.Valid || len(v.Errors) == 0 {
		t.Errorf("broken definition: %+v %v", v, err)
	}
	if h.read("shop", "compose.yaml") != broken || len(revisions(t, h, st.ID)) != before {
		t.Error("validation changed the files or recorded a revision")
	}

	h.agents.setOnline(false)
	if _, err := h.svc.ValidateStack(h.ctx, h.get(st.ID)); stackErrCode(err) != domain.StackErrOffline {
		t.Errorf("offline: %v", err)
	}
}
