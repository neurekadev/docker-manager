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

// TestBackupsFollowMigratedStack (#35 × #10, #246): the backup setup covers
// every environment, so after a migration its runs back the stack up in
// its new environment, and leaving that environment out leaves the stack
// out. Snapshots taken before the move keep the source's environment.
func TestBackupsFollowMigratedStack(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	h := b.connectHost(hostOpts{name: "prod", stacks: b.stacks, volumes: b.volumes})
	b.fe, b.agent = h.fe, h.agent
	b.seedStack()
	src := b.agent.env
	dst := b.connectHost(hostOpts{name: "edge", stacks: filepath.Join(b.root, "edge-stacks"), volumes: filepath.Join(b.root, "edge-volumes")}).agent.env

	repo := b.createS3Repo(owner, "Offsite")
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+repo.Repository.ID+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	type runBody struct {
		Set struct {
			Members []struct {
				Kind          string `json:"kind"`
				EnvironmentID string `json:"environmentId"`
			} `json:"members"`
		} `json:"set"`
		Jobs []struct {
			ID string `json:"id"`
		} `json:"jobs"`
	}
	stackIn := func(r runBody) string {
		for _, m := range r.Set.Members {
			if m.Kind == "stack" {
				return m.EnvironmentID
			}
		}
		return ""
	}
	run := func() runBody {
		t.Helper()
		var r runBody
		owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-settings/runs", nil).json(t, &r)
		for _, j := range r.Jobs {
			if got := b.runJob(j.ID); got.State != domain.JobSucceeded {
				t.Fatalf("job %s: %s %s", j.ID, got.State, got.ErrorClass)
			}
		}
		return r
	}
	if env := stackIn(run()); env != src {
		t.Fatalf("stack backed up in %q before the move, want %s", env, src)
	}

	// The migration's completing transaction moves the stack record (the
	// full migration is in TestStackMigrationThroughTheManager).
	ctx := testutil.Context(t)
	if err := b.m.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		st, err := store.GetStack(ctx, tx, b.stackID)
		if err != nil {
			return err
		}
		st.EnvironmentID, st.Revision = dst, st.Revision+1
		return store.UpdateStack(ctx, tx, &st)
	}); err != nil {
		t.Fatal(err)
	}
	if env := stackIn(run()); env != dst {
		t.Fatalf("stack backed up in %q after the move, want %s", env, dst)
	}
	b.settings(owner, map[string]any{"excludeEnvironments": []string{dst}})
	if env := stackIn(run()); env != "" {
		t.Fatalf("stack backed up in %q although its environment is left out", env)
	}

	// Every snapshot keeps the environment it was taken in.
	var list struct {
		Items []struct {
			EnvironmentID string `json:"environmentId"`
		} `json:"items"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/backups?kind=stack", nil).json(t, &list)
	envs := map[string]int{}
	for _, it := range list.Items {
		envs[it.EnvironmentID]++
	}
	if envs[src] != 1 || envs[dst] != 1 {
		t.Fatalf("stack snapshots by environment %v", envs)
	}
}
