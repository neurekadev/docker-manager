package app

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestPruneKeepsWhatBackupsRelyOn (#14 × #10, #32): the backup service
// installs Maintenance().SetBackupReferences, so a standalone volume a
// backup policy selects is never a prune candidate (shown as protected
// with the policy's name, also while no container uses it), and a local
// backup repository on a volume mounted into the Docker Agent is never
// one either. Neither survives by accident: an equally old, unreferenced
// volume is removed by the same run.
func TestPruneKeepsWhatBackupsRelyOn(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	old := e.clk.Now().AddDate(0, -2, 0)
	for _, v := range []string{"photos", "restic_repo"} {
		fe.AddVolume(v, nil)
		fe.SetVolumeCreated(v, old)
	}
	// The agent keeps a local backup repository on a named volume.
	fe.AddContainer(engine.ContainerSpec{Name: "docker-agent", Image: "nginx:1.27", Labels: map[string]string{protocol.LabelRole: "agent"},
		Mounts: []engine.MountSpec{{Type: "volume", Source: "restic_repo", Target: "/backups"}}}, true)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)

	now := e.clk.Now().UTC()
	repo := domain.BackupRepository{ID: ids.New(), Name: "Agent disk", Kind: "local", Executor: a.env, Path: "/backups", State: domain.BackupRepositoryReady,
		VerifyCron: "0 5 * * 0", VerifyTimeZone: "UTC", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertBackupRepository(ctx, e.m.DB(), &repo, store.BackupRepositorySealed{}); err != nil {
		t.Fatal(err)
	}
	pol := domain.BackupPolicy{ID: ids.New(), Name: "Photos nightly", RepositoryID: repo.ID, EnvironmentRepos: map[string]string{},
		Volumes: []domain.BackupVolumeSelection{{EnvironmentID: a.env, Volume: "photos"}}, Cron: "0 2 * * *", TimeZone: "UTC",
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertBackupPolicy(ctx, e.m.DB(), &pol); err != nil {
		t.Fatal(err)
	}

	var st maintSettings
	owner.must(http.StatusOK, http.MethodGet, maintPath, nil).json(t, &st)
	owner.must(http.StatusOK, http.MethodPatch, maintPath, map[string]any{"rules": []maintRule{
		{Category: "named_volumes", Enabled: true, MinAgeHours: 720, VolumeOptIn: true},
	}}, etag(st.Revision)).json(t, &st)
	pv := onlyPreview(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/previews", nil))
	reasons := map[string]string{}
	for _, c := range pv.Categories {
		for _, it := range c.Items {
			reasons[it.Decision+" "+it.Name] = it.Reason
		}
	}
	if r, ok := reasons["protected photos"]; !ok || !strings.Contains(r, "Photos nightly") {
		t.Errorf("the backed-up volume is not protected by its policy: %v", reasons)
	}
	// The repository volume is in use by Docker Manager's (protected) agent
	// container: never a candidate.
	if _, ok := reasons["remove restic_repo"]; ok {
		t.Errorf("the local repository volume is a prune candidate: %v", reasons)
	}
	if _, ok := reasons["remove olddata"]; !ok {
		t.Errorf("the unreferenced old volume is not a candidate: %v", reasons)
	}
	j := e.runJob(runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true}))[0])
	if j.State != domain.JobSucceeded {
		t.Fatalf("prune %+v", j)
	}
	vols := fe.VolumeNames()
	if !slices.Contains(vols, "photos") || !slices.Contains(vols, "restic_repo") || slices.Contains(vols, "olddata") {
		t.Fatalf("volumes after the prune: %v", vols)
	}
}
