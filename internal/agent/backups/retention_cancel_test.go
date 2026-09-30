package backups

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/restic/restictest"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestRetentionCancelledDuringPrune: a cancellation while restic prunes
// stops the prune (restic keeps the repository usable; the next prune
// frees the rest) and ends the job cancelled; the backups forget removed
// stay forgotten in the output, so the index follows the repository.
func TestRetentionCancelledDuringPrune(t *testing.T) {
	e := newEnv(t)
	ctx := testutil.Context(t)
	day := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	e.store = restictest.New(func() time.Time { day = day.Add(24 * time.Hour); return day })
	e.svc.opts.Restic = e.store
	for range 3 {
		if res, _, _ := e.run(ctx, jobspec.BackupRun, e.runInput(false, stackItem(protocol.BackupRules{})), e.credential("DYRK-K"), nil); res.Outcome != jobexec.OutcomeSucceeded {
			t.Fatalf("backup: %+v", res)
		}
	}
	clk := e.svc.opts.Clock.(*clock.Fake)
	var cancel atomic.Bool
	e.store.OnPrune = func(ctx context.Context) error {
		cancel.Store(true)
		clk.Advance(jobexec.DefaultCancelPoll)
		<-ctx.Done()
		return &restic.Error{Op: "prune", Code: restic.CodeCancelled, Message: "cancelled"}
	}
	in := protocol.BackupRetentionInput{Repository: e.repoRef(), PolicyID: "pol-1", Rules: backup.RetentionRules{Last: 1}, TimeZone: "UTC"}
	res, _, err := e.run(ctx, jobspec.BackupRetention, in, e.credential("DYRK-K"), cancel.Load)
	if err != nil || res.Outcome != jobexec.OutcomeCancelled || res.ErrorClass != domain.ErrorCancelled {
		t.Fatalf("retention: %+v %v", res, err)
	}
	var out protocol.RetentionOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Forgotten) == 0 || out.PruneError != restic.CodeCancelled {
		t.Errorf("output = %+v", out)
	}
}
