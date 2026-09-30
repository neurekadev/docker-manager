package backups

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestBackupRunCancelledDuringSnapshot: a cancellation requested while
// restic runs stops the snapshot step instead of waiting for every item.
// The items backed up so far keep their snapshots, the rest stay pending,
// no host manifest is written, and the stopped containers start again.
func TestBackupRunCancelledDuringSnapshot(t *testing.T) {
	cases := []struct {
		name string
		// at is the item (1-based) whose restic run sees the cancellation;
		// finish lets that run complete anyway (the request came too late).
		at     int
		finish bool
		// complete is how many items end with a snapshot.
		complete int
	}{
		{"while restic reads an item", 2, false, 1},
		{"after restic finished an item", 1, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			clk := e.svc.opts.Clock.(*clock.Fake)
			var cancel atomic.Bool
			var runs []string
			e.store.OnBackup = func(ctx context.Context, req restic.BackupRequest) error {
				if req.Stdin != nil {
					t.Error("the host manifest was written after the cancellation")
					return nil
				}
				runs = append(runs, req.Paths[0])
				if len(runs) != tc.at {
					return nil
				}
				cancel.Store(true)
				if tc.finish {
					// Requested as restic finished: the next item never starts.
					return nil
				}
				clk.Advance(jobexec.DefaultCancelPoll)
				<-ctx.Done()
				return &restic.Error{Op: "backup", Code: restic.CodeCancelled, Message: "cancelled"}
			}
			res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, e.runInput(true, stackItem(protocol.BackupRules{}),
				protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"}), e.credential("DYRK-TEST"), cancel.Load)
			if err != nil || res.Outcome != jobexec.OutcomeCancelled || res.ErrorClass != domain.ErrorCancelled {
				t.Fatalf("outcome %+v, %v", res, err)
			}
			if !slices.Contains(res.CompletedSteps, "stop_containers") || slices.Contains(res.CompletedSteps, "snapshot") {
				t.Errorf("completed steps = %v", res.CompletedSteps)
			}
			if len(runs) != tc.at {
				t.Errorf("restic ran for %d items, want %d", len(runs), tc.at)
			}
			out := outputOf(t, res)
			members := memberStates(out)
			stack, vol := members[backup.StackItem("st-app")], members[backup.VolumeItem("uploads")]
			if stack.State != backup.StateComplete || stack.SnapshotID == "" {
				t.Errorf("stack member = %+v", stack)
			}
			if vol.State != backup.StatePending || vol.SnapshotID != "" {
				t.Errorf("volume member = %+v", vol)
			}
			if out.ManifestSnapshotID != "" {
				t.Errorf("manifest written: %s", out.ManifestSnapshotID)
			}
			repoPath := e.repoRef().Destination.Repository(e.repoRef().Scope)
			if n := len(e.store.Snapshots(repoPath)); n != tc.complete {
				t.Errorf("snapshots = %d, want %d", n, tc.complete)
			}
			if len(res.Compensations) != 1 || !res.Compensations[0].Done {
				t.Errorf("compensations = %+v", res.Compensations)
			}
			for _, s := range []string{"db", "api", "web"} {
				if !e.eng.running(s) {
					t.Errorf("%s was not restarted", s)
				}
			}
			if e.eng.running("worker") {
				t.Error("the previously stopped worker was started")
			}
		})
	}
}
