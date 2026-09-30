package backups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestManagerBackupFinishRemovesStaging: the staged copy of the database
// (secrets) never outlives its job, whatever the outcome: a cancellation
// right after restic finished ends the job before write_manifest.
func TestManagerBackupFinishRemovesStaging(t *testing.T) {
	s := &Service{opts: Options{DataDir: t.TempDir(), Clock: testutil.FakeClock()}, log: testutil.Logger(t)}
	for _, state := range []domain.JobState{domain.JobCancelled, domain.JobFailed, domain.JobInterrupted} {
		j := domain.Job{ID: "job-" + string(state), Kind: jobspec.ManagerBackup, State: state, Input: []byte("not json")}
		dir := filepath.Join(s.staging(j.ID), StateDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stateDBFile), []byte("db"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.onManagerBackup(testutil.Context(t), nil, j); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.staging(j.ID)); !os.IsNotExist(err) {
			t.Errorf("%s: staging left behind (%v)", state, err)
		}
	}
}
