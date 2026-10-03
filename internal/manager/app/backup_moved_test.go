package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestBackupPolicyFollowsMigratedStack (#35 × #10): a backup policy on the
// source environment no longer covers a stack migrated away, while the
// rest of its scope still runs; a policy on the destination covers it.
// Snapshots taken before the move keep the source's repository and
// environment.
func TestBackupPolicyFollowsMigratedStack(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	h := b.connectHost(hostOpts{name: "prod", stacks: b.stacks, volumes: b.volumes})
	b.fe, b.agent = h.fe, h.agent
	b.seedStack()
	src := b.agent.env
	dst := b.connectHost(hostOpts{name: "edge", stacks: filepath.Join(b.root, "edge-stacks"), volumes: filepath.Join(b.root, "edge-volumes")}).agent.env

	prod := b.createS3Repo(owner, "Prod")
	key := prod.RecoveryKey.Key
	offsite := b.createS3Repo(owner, "Offsite")
	for _, id := range []string{prod.Repository.ID, offsite.Repository.ID} {
		owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
			map[string]any{"recoveryKey": key, "backedUp": true})
	}
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Prod", "scope": "environment", "environmentId": src,
		"repositoryId": prod.Repository.ID}).json(t, &pol)
	type runBody struct {
		Set struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Members []struct {
				Kind          string `json:"kind"`
				EnvironmentID string `json:"environmentId"`
				RepositoryID  string `json:"repositoryId"`
				State         string `json:"state"`
				ErrorClass    string `json:"errorClass"`
			} `json:"members"`
		} `json:"set"`
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	// Before the move: both members on the source, in its repository.
	var before runBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil).json(t, &before)
	if len(before.Jobs) != 1 {
		t.Fatalf("run before the move %+v", before)
	}
	if j := b.runJob(before.Jobs[0].ID); j.State != domain.JobSucceeded || j.EnvironmentID != src {
		t.Fatalf("backup before the move %+v", j)
	}

	// The migration's completing transaction: the stack record moves and
	// the backup hook runs (the full migration is in
	// TestStackMigrationThroughTheManager).
	ctx := testutil.Context(t)
	if err := b.m.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		st, err := store.GetStack(ctx, tx, b.stackID)
		if err != nil {
			return err
		}
		st.EnvironmentID, st.Revision = dst, st.Revision+1
		if err := store.UpdateStack(ctx, tx, &st); err != nil {
			return err
		}
		return b.m.Backups().StackMoved(ctx, tx, b.stackID, src, dst)
	}); err != nil {
		t.Fatal(err)
	}
	// The policy remains on its source environment. The moved stack is no
	// longer in its scope, while the standalone source volume still is.
	var pv struct {
		Environments []struct {
			EnvironmentID string `json:"environmentId"`
			ErrorClass    string `json:"errorClass"`
		} `json:"environments"`
		Warnings []string `json:"warnings"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/scope-previews", nil).json(t, &pv)
	for _, e := range pv.Environments {
		if e.EnvironmentID != src || e.ErrorClass != "" {
			t.Fatalf("scope preview after the move %+v", pv)
		}
	}
	var after runBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil).json(t, &after)
	if len(after.Jobs) != 1 || len(after.Set.Members) != 2 || after.Set.Members[0].Kind != "volume" || after.Set.Members[1].Kind != "volume" {
		t.Fatalf("run after the move %+v", after)
	}
	if j := b.runJob(after.Jobs[0].ID); j.State != domain.JobSucceeded || j.EnvironmentID != src {
		t.Fatalf("source job after the move %+v", j)
	}
	// A separate policy on the destination covers the moved stack.
	var destinationPolicy struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{
		"name": "Edge", "scope": "environment", "environmentId": dst, "repositoryId": offsite.Repository.ID,
	}).json(t, &destinationPolicy)
	var fixed runBody
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+destinationPolicy.ID+"/runs", nil).json(t, &fixed)
	if len(fixed.Jobs) != 1 {
		t.Fatalf("run with a destination repository %+v", fixed)
	}
	for _, j := range fixed.Jobs {
		if got := b.runJob(j.ID); got.State != domain.JobSucceeded {
			t.Fatalf("job %s: %s %s", j.ID, got.State, got.ErrorClass)
		}
	}
	// Every snapshot keeps the environment and repository it was written
	// to: the stack's first snapshot stays with the source's repository.
	var list struct {
		Items []struct {
			EnvironmentID string `json:"environmentId"`
			RepositoryID  string `json:"repositoryId"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?kind=stack", nil).json(t, &list)
	envs := map[string]string{}
	for _, it := range list.Items {
		envs[it.EnvironmentID] = it.RepositoryID
	}
	if envs[src] != prod.Repository.ID || envs[dst] != offsite.Repository.ID {
		t.Fatalf("stack snapshots by environment %v", envs)
	}
}
