package app

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/migration/migrationtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/ids"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestMigrationCutOverFollowUps (#35 with #14, #20): through the real
// manager and two agents over in-memory Engines,
//   - a scheduled update run queued against the source while the migration
//     holds the stack lock is refused at dispatch with target_moved instead
//     of pulling and recreating the stopped source containers;
//   - the stopped source (no longer a Docker Manager stack) survives a prune run
//     with every rule enabled (volumes opted in) and the Docker resource
//     routes refuse to delete its volume and network, until the source
//     removal is confirmed.
func TestMigrationCutOverFollowUps(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	go func() { _ = e.m.Jobs().Run(ctx) }()
	base := filepath.ToSlash(t.TempDir())
	srcEnv, dstEnv := migrationtest.NewEnv(t, "src", base+"/src", 10<<30), migrationtest.NewEnv(t, "dst", base+"/dst", 10<<30)
	migrationtest.SeedShop(srcEnv)
	srcEnv.Engine.AddNetwork("shop_default", map[string]string{protocol.ComposeProjectLabel: "shop", "com.docker.compose.network": "default"})
	nas, cloud := e.connectMigrationAgent("NAS", srcEnv), e.connectMigrationAgent("Cloud", dstEnv)

	now := e.clk.Now().UTC()
	st := domain.Stack{ID: ids.New(), EnvironmentID: nas.env, Name: "shop", Root: protocol.RootStacks, Dir: "shop", Origin: domain.StackOriginImported,
		Status: domain.StackDeployed, EngineState: domain.EngineStateRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertStack(ctx, e.m.DB(), &st); err != nil {
		t.Fatal(err)
	}
	owner, _ := e.setupOwner()
	stackPath := "/api/v1/stacks/" + st.ID

	id := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, stackPath+"/migrations", map[string]any{"targetEnvironmentId": cloud.env},
		header("Idempotency-Key", "migrate-1")))
	// A scheduled update run of the stack, queued against the source while
	// the migration holds the stack lock.
	upd, _, err := e.m.Jobs().Enqueue(ctx, jobs.Request{Kind: jobspec.StackUpdate, Principal: authz.Service(), EnvironmentID: nas.env,
		Targets: []domain.JobTarget{{Type: domain.TargetStack, ID: st.ID}}, Input: map[string]any{"stackId": st.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if j := e.runJob(id); j.State != domain.JobSucceeded {
		t.Fatalf("migration job %+v", j)
	}
	u := e.runJob(upd.ID)
	if u.State != domain.JobFailed || u.ErrorClass != domain.ErrorTargetMoved || u.DispatchedAt != nil ||
		!strings.Contains(u.ErrorMessage, cloud.env) || !strings.Contains(u.Recovery, "new environment") {
		t.Fatalf("update run queued before the cut-over %+v", u)
	}
	if !slices.Contains(srcEnv.Engine.ContainerNames(), "shop-web-1") {
		t.Fatal("the source containers are gone")
	}

	// The source is stopped and old enough for every prune rule.
	old := e.clk.Now().Add(-60 * 24 * time.Hour)
	for _, n := range []string{"shop-db-1", "shop-web-1"} {
		srcEnv.Engine.SetContainerTimes(n, old, old)
	}
	srcEnv.Engine.SetVolumeCreated("shop_dbdata", old)
	srcEnv.Engine.SetNetworkCreated("shop_default", old)
	var pol maintPolicy
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/maintenance-policies",
		map[string]any{"environmentId": nas.env, "name": "Cleanup"}).json(t, &pol)
	polPath := "/api/v1/maintenance-policies/" + pol.ID
	owner.must(http.StatusOK, http.MethodPatch, polPath, map[string]any{"rules": []maintRule{
		{Category: "stopped_containers", Enabled: true, MinAgeHours: 720},
		{Category: "unused_networks", Enabled: true, MinAgeHours: 720},
		{Category: "anonymous_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true},
		{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true},
	}}, etag(pol.Revision)).json(t, &pol)
	var pv maintPreview
	owner.must(http.StatusOK, http.MethodPost, polPath+"/previews", nil).json(t, &pv)
	protected := map[string]string{}
	for _, c := range pv.Categories {
		for _, it := range c.Items {
			if strings.HasPrefix(it.Name, "shop") {
				if it.Decision != "protected" {
					t.Fatalf("preview would %s %s (%s)", it.Decision, it.Name, it.Reason)
				}
				protected[it.Name] = it.Reason
			}
		}
	}
	for _, n := range []string{"shop-db-1", "shop-web-1", "shop_default"} {
		if !strings.Contains(protected[n], `stopped source of migrated stack "shop"`) {
			t.Fatalf("%s is not protected as the migrated source: %v", n, protected)
		}
	}
	pj := e.runJob(jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, polPath+"/runs", map[string]any{"confirm": true})))
	if pj.State != domain.JobSucceeded {
		t.Fatalf("prune run %+v", pj)
	}
	if !slices.Contains(srcEnv.Engine.ContainerNames(), "shop-db-1") || !slices.Contains(srcEnv.Engine.ContainerNames(), "shop-web-1") ||
		!slices.Contains(srcEnv.Engine.VolumeNames(), "shop_dbdata") || !slices.Contains(srcEnv.Engine.NetworkNames(), "shop_default") {
		t.Fatalf("the prune removed part of the migrated source: containers %v volumes %v networks %v",
			srcEnv.Engine.ContainerNames(), srcEnv.Engine.VolumeNames(), srcEnv.Engine.NetworkNames())
	}
	// Removing its objects one by one is refused as well.
	envPath := "/api/v1/environments/" + nas.env
	r := owner.fail(http.StatusConflict, "stack_managed", http.MethodDelete, envPath+"/networks/shop_default", nil)
	if !strings.Contains(string(r.body), "removal from the source") {
		t.Fatalf("refusal %s", r.body)
	}
	owner.fail(http.StatusConflict, "stack_managed", http.MethodDelete, envPath+"/volumes/shop_dbdata", nil)

	// Confirming the source removal ends the hold.
	rid := jobOf(t, owner.must(http.StatusAccepted, http.MethodPost, stackPath+"/migrations/"+id+"/source-removals", nil))
	if j := e.runJob(rid); j.State != domain.JobSucceeded {
		t.Fatalf("removal job %+v", j)
	}
	if rs, err := e.m.Migrations().RetainedSources(ctx, nas.env); err != nil || len(rs) != 0 {
		t.Fatalf("retained sources after the confirmed removal %+v %v", rs, err)
	}
}
