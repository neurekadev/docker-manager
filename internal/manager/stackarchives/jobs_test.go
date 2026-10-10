package stackarchives

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/migration/migrationtest"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestExportAndCreateUnderNewName: the shop stack is exported (stopped,
// written, started again) and created from its archive in another
// environment as "boutique": the project folder and the volume's data
// arrive with owners, modes, times and links, and the volume gets the
// name and Compose labels of the new project.
func TestExportAndCreateUnderNewName(t *testing.T) {
	w := newWorld(t)
	f := w.export(ExportRequest{})
	if !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("the stack did not start again after the export")
	}
	if !strings.HasPrefix(f.FileName, "shop-") || !strings.HasSuffix(f.FileName, FileExtension) || !slices.Equal(f.Volumes, []string{"dbdata"}) {
		t.Fatalf("file %+v", f)
	}
	if latest := w.svc.LatestExport(w.ctx, "st-shop"); latest == nil || latest.JobID != f.JobID {
		t.Fatalf("latest export %+v", latest)
	}

	u := w.upload(f)
	m := u.Manifest
	if m.Stack.Name != "shop" || m.Stack.DisplayName != "Shop" || len(m.Volumes) != 1 || !m.Volumes[0].FollowsProject {
		t.Fatalf("manifest %+v", m)
	}
	if !slices.ContainsFunc(m.NotIncluded, func(e Exclusion) bool { return e.Kind == ExcludedBind && e.Name == "/srv/shared" }) {
		t.Errorf("the external bind is not listed as not included: %+v", m.NotIncluded)
	}

	r := ImportRequest{EnvironmentID: dstEnv, Name: "boutique"}
	plan, err := w.svc.PreviewImport(w.ctx, w.user, u.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Allowed() || len(plan.Volumes) != 1 || plan.Volumes[0].Name != "boutique_dbdata" || plan.Volumes[0].Source != "shop_dbdata" {
		t.Fatalf("plan %+v", plan)
	}
	if !slices.ContainsFunc(plan.Warnings, func(f migrations.Finding) bool {
		return f.Code == FindingImageMissing && f.Resource == "shop-web:local"
	}) {
		t.Errorf("no warning for the locally built image: %+v", plan.Warnings)
	}
	st, j, err := w.svc.StartImport(w.ctx, w.user, u.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "boutique" || st.DisplayName != "Shop" || j.Kind != jobspec.StackImportArchive {
		t.Fatalf("stack %+v, job %+v", st, j)
	}
	if res := w.run(j); res.Outcome != string(domain.JobSucceeded) {
		t.Fatalf("import ended %s (%s): %s", res.Outcome, res.ErrorClass, res.Message)
	}
	if !slices.Equal(w.stacks.recorded, []string{st.ID}) || w.stacks.deploys != 0 {
		t.Errorf("recorded %v, deploys %d", w.stacks.recorded, w.stacks.deploys)
	}

	if got, want := tree(w.dst, w.dst.ProjectDir("boutique")), tree(w.src, w.src.ProjectDir("shop")); !equalTrees(got, want) {
		t.Errorf("project folder differs:\n got %v\nwant %v", got, want)
	}
	if got, want := tree(w.dst, volumeDir(t, w.dst, "boutique_dbdata")), tree(w.src, volumeDir(t, w.src, "shop_dbdata")); !equalTrees(got, want) {
		t.Errorf("volume data differs:\n got %v\nwant %v", got, want)
	}
	v, _ := w.dst.Engine.InspectVolume(w.ctx, "boutique_dbdata")
	if v.Labels["com.docker.compose.project"] != "boutique" || v.Labels["com.docker.compose.volume"] != "dbdata" ||
		v.Labels["com.docker.compose.config-hash"] == "" || protocol.LabelValue(v.Labels, protocol.LabelMigration) != j.ID {
		t.Errorf("volume labels %v", v.Labels)
	}
	if w.dst.Host.Exists(w.dst.StacksDir + "/" + protocol.MigrationStagingDir + "/" + j.ID) {
		t.Error("the staging directory is left")
	}
	if _, err := w.svc.Upload(w.user, u.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("the upload is kept after the stack was created: %v", err)
	}
}

func equalTrees(a, b map[string]migrationtest.Snapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestExportExcludedVolume: a volume left out is not in the archive and is
// listed as not included.
func TestExportExcludedVolume(t *testing.T) {
	w := newWorld(t)
	f := w.export(ExportRequest{ExcludeVolumes: []string{"dbdata"}})
	u := w.upload(f)
	if len(u.Manifest.Volumes) != 0 || len(u.Volumes) != 0 {
		t.Fatalf("volumes %+v / %+v", u.Manifest.Volumes, u.Volumes)
	}
	if !slices.ContainsFunc(u.Manifest.NotIncluded, func(e Exclusion) bool { return e.Kind == ExcludedVolume && e.Key == "dbdata" }) {
		t.Fatalf("not included %+v", u.Manifest.NotIncluded)
	}
	plan, err := w.svc.PreviewImport(w.ctx, w.user, u.ID, ImportRequest{EnvironmentID: dstEnv, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Warnings, func(f migrations.Finding) bool { return f.Code == FindingVolumeMissing }) {
		t.Errorf("warnings %+v", plan.Warnings)
	}
}

// TestExportFailureStartsTheStackAgain: an export that fails after the
// stop starts the services again and keeps no file.
func TestExportFailureStartsTheStackAgain(t *testing.T) {
	w := newWorld(t)
	j, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The write step reads the definition again; the agent fails it.
	calls := 0
	orig := w.agents.envs[srcEnv].requests[protocol.ReqMigrationPreview]
	w.agents.envs[srcEnv].requests[protocol.ReqMigrationPreview] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		calls++
		if calls == 3 { // prepare, stop, write_archive
			return nil, errBoom
		}
		return orig(ctx, raw)
	}
	res := w.run(j)
	if res.Outcome != string(domain.JobFailed) || res.ErrorClass != ClassTransferFailed {
		t.Fatalf("outcome %s (%s): %s", res.Outcome, res.ErrorClass, res.Message)
	}
	if !w.running(w.src, "shop-db-1", "shop-web-1") {
		t.Fatal("the compensation did not start the stack again")
	}
	if _, err := os.Stat(w.svc.exportPath(j.ID)); !os.IsNotExist(err) {
		t.Errorf("a file is left: %v", err)
	}
	if _, err := w.svc.Export(w.ctx, "st-shop", j.ID); !errors.Is(err, ErrExportNotFound) {
		t.Errorf("Export = %v", err)
	}
}

// TestExportRefusals: Docker Manager's own stack, an offline environment,
// data above the limit and volumes the caller may not download block the
// export before anything stops.
func TestExportRefusals(t *testing.T) {
	blockedBy := func(t *testing.T, err error, code string) {
		t.Helper()
		var be *BlockedError
		if !errors.As(err, &be) || !slices.ContainsFunc(be.Blockers, func(f migrations.Finding) bool { return f.Code == code }) {
			t.Fatalf("err = %v, want a %s blocker", err, code)
		}
	}
	t.Run("own stack", func(t *testing.T) {
		w := newWorld(t)
		w.stacks.protected["st-shop"] = true
		_, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
		blockedBy(t, err, migrations.FindingDockerManagerResource)
	})
	t.Run("offline", func(t *testing.T) {
		w := newWorld(t)
		w.agents.envs[srcEnv].online = false
		_, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
		blockedBy(t, err, migrations.FindingEnvironmentOffline)
	})
	t.Run("too large", func(t *testing.T) {
		w := newWorld(t, func(o *Options) { o.MaxSize = 1000 })
		_, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
		blockedBy(t, err, FindingArchiveTooLarge)
	})
	t.Run("manager space", func(t *testing.T) {
		w := newWorld(t, func(o *Options) { o.FreeBytes = func(string) int64 { return 1 << 20 } })
		_, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
		blockedBy(t, err, FindingManagerSpace)
	})
	t.Run("volume not permitted", func(t *testing.T) {
		w := newWorld(t)
		deny := func(string) bool { return false }
		_, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{MayDownload: deny})
		blockedBy(t, err, FindingVolumeNotPermitted)
		plan, err := w.svc.PreviewExport(w.ctx, w.stack("st-shop"), ExportRequest{MayDownload: deny, ExcludeVolumes: []string{"dbdata"}})
		if err != nil || !plan.Allowed() || len(plan.Volumes) != 1 || !plan.Volumes[0].NotPermitted || !plan.Volumes[0].Excluded {
			t.Fatalf("plan %+v, %v", plan, err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		w := newWorld(t)
		j, err := w.svc.StartExport(w.ctx, w.user, w.stack("st-shop"), ExportRequest{})
		if err != nil {
			t.Fatal(err)
		}
		w.auth.deny["stack.definition.read"] = true
		if res := w.run(j); res.ErrorClass != domain.ErrorAuthorizationRevoked {
			t.Fatalf("outcome %s (%s)", res.Outcome, res.ErrorClass)
		}
		if !w.running(w.src, "shop-db-1", "shop-web-1") {
			t.Fatal("the stack stopped")
		}
	})
}

// TestImportFailureRemovesEverything: an import whose definition does not
// read back removes what it wrote, forgets the stack and keeps the upload.
func TestImportFailureRemovesEverything(t *testing.T) {
	w := newWorld(t)
	u := w.upload(w.export(ExportRequest{}))
	st, j, err := w.svc.StartImport(w.ctx, w.user, u.ID, ImportRequest{EnvironmentID: dstEnv, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	w.stacks.recordErr = &domain.StackError{Code: domain.StackErrInvalidDefinition, Message: "the Compose definition is invalid"}
	res := w.run(j)
	if res.Outcome != string(domain.JobFailed) || res.ErrorClass != ClassInvalid {
		t.Fatalf("outcome %s (%s): %s", res.Outcome, res.ErrorClass, res.Message)
	}
	if w.dst.Host.Exists(w.dst.ProjectDir("shop")) {
		t.Error("the project folder is left")
	}
	if _, err := w.dst.Engine.InspectVolume(w.ctx, "shop_dbdata"); err == nil {
		t.Error("a volume was created")
	}
	if !slices.Equal(w.stacks.forgotten, []string{st.ID}) {
		t.Errorf("forgotten %v", w.stacks.forgotten)
	}
	if _, err := w.svc.Upload(w.user, u.ID); err != nil {
		t.Errorf("the upload is gone after a failed import: %v", err)
	}
	// Another try works once the cause is gone.
	w.stacks.recordErr = nil
	_, j, err = w.svc.StartImport(w.ctx, w.user, u.ID, ImportRequest{EnvironmentID: dstEnv, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if res := w.run(j); res.Outcome != string(domain.JobSucceeded) {
		t.Fatalf("second import ended %s (%s): %s", res.Outcome, res.ErrorClass, res.Message)
	}
	v, _ := w.dst.Engine.InspectVolume(w.ctx, "shop_dbdata")
	if protocol.LabelValue(v.Labels, protocol.LabelMigration) != j.ID {
		t.Errorf("volume labels %v", v.Labels)
	}
}

// TestImportDeployFailureKeepsTheStack: after the files and volumes are
// kept, a failed deploy fails the job but undoes nothing.
func TestImportDeployFailureKeepsTheStack(t *testing.T) {
	w := newWorld(t)
	u := w.upload(w.export(ExportRequest{}))
	w.jobs.deployOutcome = domain.JobFailed
	st, j, err := w.svc.StartImport(w.ctx, w.user, u.ID, ImportRequest{EnvironmentID: dstEnv, Name: "shop", Deploy: true})
	if err != nil {
		t.Fatal(err)
	}
	res := w.run(j)
	if res.Outcome != string(domain.JobFailed) || res.ErrorClass != ClassDeployFailed || w.stacks.deploys != 1 {
		t.Fatalf("outcome %s (%s), deploys %d", res.Outcome, res.ErrorClass, w.stacks.deploys)
	}
	if !w.dst.Host.Exists(w.dst.ProjectDir("shop")) || len(w.stacks.forgotten) != 0 || w.stack(st.ID).ID == "" {
		t.Fatal("the stack was not kept")
	}
	if _, err := w.dst.Engine.InspectVolume(w.ctx, "shop_dbdata"); err != nil {
		t.Fatal("the volume was removed")
	}
}

// TestImportRefusals: conflicts in the environment and a pinned project
// name block the import before anything is written.
func TestImportRefusals(t *testing.T) {
	w := newWorld(t)
	u := w.upload(w.export(ExportRequest{}))
	check := func(t *testing.T, r ImportRequest, code string) {
		t.Helper()
		plan, err := w.svc.PreviewImport(w.ctx, w.user, u.ID, r)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(plan.Blockers, func(f migrations.Finding) bool { return f.Code == code }) {
			t.Fatalf("blockers %+v, want %s", plan.Blockers, code)
		}
		var be *BlockedError
		if _, _, err := w.svc.StartImport(w.ctx, w.user, u.ID, r); !errors.As(err, &be) {
			t.Fatalf("StartImport = %v", err)
		}
	}
	t.Run("volume exists", func(t *testing.T) {
		w.dst.AddVolume("taken_dbdata", nil)
		check(t, ImportRequest{EnvironmentID: dstEnv, Name: "taken"}, migrations.FindingVolumeConflict)
	})
	t.Run("container exists", func(t *testing.T) {
		container(w.dst, "busy-db-1")
		check(t, ImportRequest{EnvironmentID: dstEnv, Name: "busy"}, migrations.FindingContainerConflict)
	})
	t.Run("stack name", func(t *testing.T) {
		check(t, ImportRequest{EnvironmentID: srcEnv, Name: "shop"}, migrations.FindingStackNameConflict)
	})
	t.Run("port", func(t *testing.T) {
		w.dst.Engine.AddContainer(engineSpecWithPort("other", 8080), true)
		check(t, ImportRequest{EnvironmentID: dstEnv, Name: "ported"}, migrations.FindingPortConflict)
	})
	t.Run("offline", func(t *testing.T) {
		w.agents.envs[dstEnv].online = false
		defer func() { w.agents.envs[dstEnv].online = true }()
		check(t, ImportRequest{EnvironmentID: dstEnv, Name: "away"}, migrations.FindingEnvironmentOffline)
	})
	t.Run("old agent", func(t *testing.T) {
		w.agents.envs[dstEnv].features = nil
		defer func() { w.agents.envs[dstEnv].features = []string{protocol.FeatureMigrationComposeVolume} }()
		check(t, ImportRequest{EnvironmentID: dstEnv, Name: "older"}, migrations.FindingAgentUnsupported)
	})
	t.Run("pinned name", func(t *testing.T) {
		pinned := u
		pinned.PinnedName = "shop"
		plan := evaluateImport(importFacts{upload: pinned, name: "other", online: true, supported: true})
		if !slices.ContainsFunc(plan.Blockers, func(f migrations.Finding) bool { return f.Code == FindingNamePinned }) {
			t.Fatalf("blockers %+v", plan.Blockers)
		}
	})
	if len(w.stacks.stacks) != 1 {
		t.Errorf("stacks were reserved: %v", keys(w.stacks.stacks))
	}
}

// TestUploads: uploads are validated, bounded, private and expire.
func TestUploads(t *testing.T) {
	w := newWorld(t)
	f := w.export(ExportRequest{})
	file, err := w.svc.OpenExport(f)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := archive.ReadFrom(file); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	b := archive.Bytes()
	store := func(size int64, body []byte) (Upload, error) {
		return w.svc.StoreUpload(w.ctx, w.user, size, bytes.NewReader(body))
	}

	if _, err := store(int64(len(b)/2), b[:len(b)/2]); !errors.Is(err, ErrInvalid) {
		t.Errorf("a truncated archive: %v", err)
	}
	if _, err := store(int64(len(b)), b[:len(b)-10]); err == nil {
		t.Error("a short body was accepted")
	}
	if _, err := store(10, []byte("not a tar!")); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage: %v", err)
	}
	if _, err := w.svc.StoreUpload(w.ctx, w.user, 2<<30, bytes.NewReader(nil)); !errors.Is(err, ErrUploadTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if entries, _ := os.ReadDir(w.svc.uploadsDir()); len(entries) != 0 {
		t.Fatalf("refused uploads left files: %v", entries)
	}

	u, err := store(int64(len(b)), b)
	if err != nil {
		t.Fatal(err)
	}
	if u.Size != int64(len(b)) || u.SHA256 != f.SHA256 || u.OwnerUserID != "u-alice" {
		t.Fatalf("upload %+v", u)
	}
	bob := w.user
	bob.UserID = "u-bob"
	if _, err := w.svc.Upload(bob, u.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("another user sees the upload: %v", err)
	}
	if err := w.svc.DeleteUpload(bob, u.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("another user discards the upload: %v", err)
	}
	for range MaxUploadsPerUser - 1 {
		if _, err := store(int64(len(b)), b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store(int64(len(b)), b); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("one too many: %v", err)
	}

	// A restart reads the uploads back; a day later they are gone, and so
	// is the export.
	again, err := New(w.svc.opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Upload(w.user, u.ID); err != nil || got.SHA256 != u.SHA256 {
		t.Fatalf("after a restart: %+v, %v", got, err)
	}
	w.clk.Advance(DefaultRetention + time.Minute)
	again.Sweep()
	if len(again.Uploads(w.user)) != 0 {
		t.Error("expired uploads are kept")
	}
	if _, err := os.Stat(again.exportPath(f.JobID)); !os.IsNotExist(err) {
		t.Errorf("the expired export is kept: %v", err)
	}
}
