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
// installs Maintenance().SetBackupReferences, so a standalone volume the
// backups select is never a prune candidate (shown as protected, also
// while no container uses it), and a volume mounted into the Docker Agent
// is never one either. Neither survives by accident: an equally old
// volume the backups leave out is removed by the same run.
func TestPruneKeepsWhatBackupsRelyOn(t *testing.T) {
	e := newEnv(t)
	fe := maintenanceHost(e)
	old := e.clk.Now().AddDate(0, -2, 0)
	for _, v := range []string{"photos", "agent_data"} {
		fe.AddVolume(v, nil)
		fe.SetVolumeCreated(v, old)
	}
	// The agent keeps its data on a named volume.
	fe.AddContainer(engine.ContainerSpec{Name: "docker-agent", Image: "nginx:1.27", Labels: map[string]string{protocol.LabelRole: "agent"},
		Mounts: []engine.MountSpec{{Type: "volume", Source: "agent_data", Target: "/backups"}}}, true)
	a := e.connectAgent("Maint", fe)
	owner, _ := e.setupOwner()
	ctx := testutil.Context(t)

	now := e.clk.Now().UTC()
	repo := domain.BackupRepository{ID: ids.New(), Name: "Offsite", Endpoint: "https://s3.example.com", Bucket: "backups", State: domain.BackupRepositoryReady,
		VerifyCron: "0 5 * * 0", VerifyTimeZone: "UTC", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertBackupRepository(ctx, e.m.DB(), &repo, store.BackupRepositorySealed{}); err != nil {
		t.Fatal(err)
	}
	setup, err := store.GetBackupSetup(ctx, e.m.DB())
	if err != nil {
		t.Fatal(err)
	}
	rev := setup.Revision
	setup.Enabled, setup.PrimaryRepositoryID, setup.ExcludeVolumes, setup.Revision = true, repo.ID, []string{a.env + "/olddata"}, rev+1
	if err := store.UpdateBackupSetup(ctx, e.m.DB(), setup, rev); err != nil {
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
	if r, ok := reasons["protected photos"]; !ok || !strings.Contains(r, "Backups") {
		t.Errorf("the backed-up volume is not protected by the backups: %v", reasons)
	}
	// The agent's volume is in use by Docker Manager's (protected) agent
	// container: never a candidate.
	if _, ok := reasons["remove agent_data"]; ok {
		t.Errorf("the agent volume is a prune candidate: %v", reasons)
	}
	if _, ok := reasons["remove olddata"]; !ok {
		t.Errorf("the unreferenced old volume is not a candidate: %v", reasons)
	}
	j := e.runJob(runJobs(t, owner.must(http.StatusOK, http.MethodPost, maintPath+"/runs", map[string]any{"confirm": true}))[0])
	if j.State != domain.JobSucceeded {
		t.Fatalf("prune %+v", j)
	}
	vols := fe.VolumeNames()
	if !slices.Contains(vols, "photos") || !slices.Contains(vols, "agent_data") || slices.Contains(vols, "olddata") {
		t.Fatalf("volumes after the prune: %v", vols)
	}
}
