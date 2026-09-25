package app

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/migration/migrationtest"
	"github.com/neurekadev/dockyard/internal/agent/session"
	agentstacks "github.com/neurekadev/dockyard/internal/agent/stacks"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// Environment migration (#35) through the real manager: two agents with
// real sessions serve the migration requests and streams over in-memory
// Engines and host filesystems (migrationtest); the destination's
// stack.deploy is simulated (the Compose SDK needs a Docker Engine: the
// integration test in internal/manager/migrations covers that part).

type migrationStackDeps struct{ me *migrationtest.Env }

func (d migrationStackDeps) Composer() agentstacks.Composer { return nil }
func (d migrationStackDeps) Engine() engine.Engine {
	return migrationtest.HostEngine{Engine: d.me.Engine, Host: d.me.Host}
}
func (d migrationStackDeps) Storage() *storage.Result { s := *d.me.Storage; return &s }

// simulatedDeploy is a stack.deploy executor that "deploys" by reading the
// definition from the environment's host and starting the containers.
func simulatedDeploy(me *migrationtest.Env) jobexec.Executor {
	noop := func(context.Context, *jobexec.StepContext) error { return nil }
	apply := func(ctx context.Context, sc *jobexec.StepContext) error {
		var in protocol.StackJobInput
		if err := json.Unmarshal(sc.Input, &in); err != nil {
			return err
		}
		dir := me.ProjectDir(in.Stack.Dir)
		fs, err := me.Host.Opener()(dir)
		if err != nil {
			return err
		}
		var files []protocol.SourceFile
		for _, n := range []string{"compose.yaml", ".env"} {
			f, err := fs.Open(n)
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(f)
			_ = f.Close()
			files = append(files, protocol.SourceFile{Path: n, Content: b})
		}
		snap := protocol.NewSourceSnapshot(files)
		for _, svc := range []string{"db", "web"} {
			me.Engine.AddContainer(engine.ContainerSpec{Name: "shop-" + svc + "-1", Image: "postgres:17", Labels: map[string]string{
				protocol.ComposeProjectLabel: in.Stack.ProjectName, protocol.ComposeServiceLabel: svc, protocol.ComposeWorkingDirLabel: dir}}, true)
		}
		return sc.SetOutput(ctx, protocol.StackJobOutput{Sources: &snap,
			Services: []protocol.ComposeService{{Name: "db", Image: "postgres:17"}, {Name: "web", Image: "shop-web:local",
				DependsOn: []protocol.ComposeDependency{{Service: "db", Condition: "service_started", Required: true}}}},
			Before: []protocol.ServiceState{}, After: []protocol.ServiceState{{Service: "db", Containers: 1, Running: 1}, {Service: "web", Containers: 1, Running: 1}}})
	}
	return jobexec.Executor{Kind: jobspec.StackDeploy, Steps: map[string]jobexec.StepFunc{
		"resolve_sources": noop, "pull_images": noop, "build_images": noop, "apply": apply}}
}

// connectMigrationAgent connects an agent for me: the migration requests,
// streams and stack.remove_source executor, compose.services and the
// simulated deploy.
func (e *env) connectMigrationAgent(name string, me *migrationtest.Env) *testAgent {
	e.t.Helper()
	reqs := me.Service.Requests()
	maps.Copy(reqs, map[string]session.RequestHandler{protocol.ReqComposeServices: agentstacks.New(agentstacks.Options{
		Deps: migrationStackDeps{me}, Logger: testutil.Logger(e.t)}).Requests()[protocol.ReqComposeServices]})
	return e.connectAgentParts(name, me.Engine, agentParts{requests: reqs, streams: me.Service.Streams(),
		executors: append(me.Service.Executors(), simulatedDeploy(me))})
}

type migrationPreviewJSON struct {
	Allowed  bool `json:"allowed"`
	Blockers []struct {
		Code string `json:"code"`
	} `json:"blockers"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
	Services []struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	} `json:"services"`
	Volumes []struct {
		Source string `json:"source"`
		Action string `json:"action"`
		Bytes  int64  `json:"bytes"`
	} `json:"volumes"`
	Transport struct {
		DestinationPlainHTTP bool `json:"destinationPlainHttp"`
	} `json:"transport"`
	Access struct {
		Complete bool `json:"complete"`
	} `json:"access"`
}

// TestStackMigrationThroughTheManager (#35 Done-when 1 and 4, simulated
// Engines): the preview, the migration job relayed over the agents'
// sessions, the cut-over and deploy on the destination, the audit trail
// with sizes and checksums, and the confirmed source removal.
func TestStackMigrationThroughTheManager(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	go func() { _ = e.m.Jobs().Run(ctx) }()
	base := filepath.ToSlash(t.TempDir())
	srcEnv, dstEnv := migrationtest.NewEnv(t, "src", base+"/src", 10<<30), migrationtest.NewEnv(t, "dst", base+"/dst", 10<<30)
	migrationtest.SeedShop(srcEnv)
	srcProject := srcEnv.Host.Tree(srcEnv.ProjectDir("shop"))
	srcVolume := srcEnv.Host.Tree(srcEnv.VolumesDir + "/shop_dbdata/_data")
	nas, cloud := e.connectMigrationAgent("NAS", srcEnv), e.connectMigrationAgent("Cloud", dstEnv)

	now := testutil.Epoch
	st := domain.Stack{ID: ids.New(), EnvironmentID: nas.env, Name: "shop", Root: protocol.RootStacks, Dir: "shop", Origin: domain.StackOriginImported,
		Status: domain.StackDeployed, EngineState: domain.EngineStateRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertStack(ctx, e.m.DB(), &st); err != nil {
		t.Fatal(err)
	}
	owner, _ := e.setupOwner()
	base1 := "/api/v1/stacks/" + st.ID

	var p migrationPreviewJSON
	owner.must(http.StatusOK, http.MethodPost, base1+"/migration-previews", map[string]any{"targetEnvironmentId": cloud.env}).json(t, &p)
	var warnings []string
	for _, w := range p.Warnings {
		warnings = append(warnings, w.Code)
	}
	if !p.Allowed || !slices.Contains(warnings, "image_transfer") || !slices.Contains(warnings, "external_bind_path") ||
		!slices.Contains(warnings, "plain_http_transport") || !p.Access.Complete || len(p.Volumes) != 1 || p.Volumes[0].Action != "copy" ||
		p.Volumes[0].Bytes < 300_000 {
		t.Fatalf("preview %+v", p)
	}
	if c, _ := srcEnv.Engine.Container("shop-web-1"); !c.Details.State.Running {
		t.Fatal("the preview stopped the source")
	}

	id := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base1+"/migrations", map[string]any{"targetEnvironmentId": cloud.env},
		header("Idempotency-Key", "migrate-1")))
	j := e.runJob(id)
	if j.State != domain.JobSucceeded || j.Kind != jobspec.StackMigrate {
		t.Fatalf("migration job %+v", j)
	}
	// The stack moved (same ID) and was deployed there.
	var moved struct {
		EnvironmentID string `json:"environmentId"`
		Status        string `json:"status"`
	}
	owner.must(http.StatusOK, http.MethodGet, base1, nil).json(t, &moved)
	if moved.EnvironmentID != cloud.env || moved.Status != "deployed" {
		t.Fatalf("stack %+v", moved)
	}
	for _, want := range []struct {
		what      string
		src, dst  map[string]migrationtest.Snapshot
		exactTime bool
	}{
		{"project", srcProject, dstEnv.Host.Tree(dstEnv.ProjectDir("shop")), true},
		{"volume", srcVolume, dstEnv.Host.Tree(dstEnv.VolumesDir + "/shop_dbdata/_data"), true},
	} {
		for k, w := range want.src {
			g := want.dst[k]
			if w.Type == "symlink" {
				w.MTime, w.ATime, w.Mode = g.MTime, g.ATime, g.Mode
			}
			if g != w {
				t.Errorf("%s %s: got %+v want %+v", want.what, k, g, w)
			}
		}
	}
	for _, n := range []string{"shop-db-1", "shop-web-1"} {
		if c, _ := srcEnv.Engine.Container(n); c.Details.State.Running {
			t.Errorf("source %s still runs", n)
		}
		if c, ok := dstEnv.Engine.Container(n); !ok || !c.Details.State.Running {
			t.Errorf("destination %s does not run", n)
		}
	}
	m, err := e.m.Migrations().Get(ctx, id)
	if err != nil || m.State != domain.MigrationCompleted || len(m.Parts) != 3 {
		t.Fatalf("migration %+v %v", m, err)
	}
	// Audit: the request with source and destination, the job's outcome
	// with every part's size and checksum.
	var requested, finished bool
	for _, row := range e.auditRows() {
		if row.Action == "stack.migrate" && strings.Contains(row.Details, cloud.env) && strings.Contains(row.Details, nas.env) {
			requested = true
		}
		if row.Action == "migration.finished" && row.Outcome == "success" && strings.Contains(row.Details, m.Parts[0].SHA256) &&
			strings.Contains(row.Details, m.Parts[1].SHA256) && strings.Contains(row.Details, cloud.env) && strings.Contains(row.Details, `"bytes"`) {
			finished = true
		}
	}
	if !requested || !finished {
		t.Fatalf("audit: requested %v finished %v", requested, finished)
	}

	// Confirm: the source is removed.
	rid := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, base1+"/migrations/"+id+"/source-removals", nil))
	if j := e.runJob(rid); j.State != domain.JobSucceeded || j.EnvironmentID != nas.env {
		t.Fatalf("removal job %+v", j)
	}
	if srcEnv.Host.Exists(srcEnv.ProjectDir("shop")) {
		t.Error("the source project directory is still there")
	}
	if _, err := srcEnv.Engine.InspectVolume(ctx, "shop_dbdata"); err == nil {
		t.Error("the source volume is still there")
	}
	if _, ok := srcEnv.Engine.Container("shop-web-1"); ok {
		t.Error("the source containers are still there")
	}
	if m, _ := e.m.Migrations().Get(ctx, id); m.State != domain.MigrationSourceRemoved {
		t.Errorf("migration state %s", m.State)
	}
	owner.fail(http.StatusConflict, "migration_source_removed", http.MethodPost, base1+"/migrations/"+id+"/source-removals", nil)
}
