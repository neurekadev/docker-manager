package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
)

// backup.retention: forget what the policy's rules do not keep in this
// environment's repository (exactly the preview's decision), then prune
// when anything was forgotten (a prune downloads and rewrites pack data,
// so a retention that removed nothing never pays for one).
// backup.verify: check the repository (optionally reading a subset of the
// data) and list its snapshots and host manifests so the manager's index
// catches up (#24).

func (s *Service) stepForget(ctx context.Context, sc *jobexec.StepContext) error {
	var in protocol.BackupRetentionInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return backup.Refuse(domain.ErrorRejected, "invalid retention input", "Run retention again from the policy.")
	}
	if errs := in.Rules.Validate(); len(errs) > 0 {
		return backup.Refuse(domain.ErrorRejected, "invalid retention rules", "Fix the policy's retention rules.")
	}
	sc.Progress(ctx, 5, "opening the backup repository")
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 10, "removing the backups the rules no longer keep")
	res, err := backup.ApplyRetention(ctx, o.Repo, in.PolicyID, in.Rules, in.Expire, in.TimeZone)
	out := protocol.RetentionOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: in.Repository.KeyGeneration,
		Forgotten: res.Forgotten, Kept: res.Kept}
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	if err == nil {
		sc.Progress(ctx, 30, retentionForgotMessage(len(res.Forgotten), res.Kept))
	}
	return err
}

// retentionForgotMessage is the progress after forget (counts only).
func retentionForgotMessage(forgotten, kept int) string {
	return fmt.Sprintf("removed %d backups, kept %d", forgotten, kept)
}

func (s *Service) stepPrune(ctx context.Context, sc *jobexec.StepContext) error {
	var in protocol.BackupRetentionInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	var out protocol.RetentionOutput
	_ = json.Unmarshal(sc.Output(), &out)
	if len(out.Forgotten) == 0 {
		return nil
	}
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 40, "freeing the space of the removed backups")
	// A cancellation stops restic's prune: restic keeps the repository
	// usable wherever a prune stops, and the next prune finishes the job.
	pctx, stop := sc.WatchCancel(ctx, s.opts.Clock, jobexec.DefaultCancelPoll)
	defer stop()
	var after *restic.Stats
	out.ReclaimedBytes, after, err = backup.Prune(pctx, o.Repo)
	if err != nil && ctx.Err() == nil && pctx.Err() != nil {
		return pruneStopped(ctx, sc, o.Repo, out)
	}
	if err != nil {
		out.PruneError = restic.CodeOf(err)
		after = backup.MeasureStats(ctx, o.Repo) // forget still changed it
	}
	out.Stats = protocol.StatsOf(after)
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	return err
}

// pruneStopped ends a prune cancelled on request: the forgotten backups
// stay forgotten (the output keeps them), the space is freed by the next
// prune, and the job ends cancelled.
func pruneStopped(ctx context.Context, sc *jobexec.StepContext, repo restic.Repo, out protocol.RetentionOutput) error {
	out.PruneError = restic.CodeCancelled
	out.Stats = protocol.StatsOf(backup.MeasureStats(ctx, repo))
	if err := sc.SetOutput(ctx, out); err != nil {
		return err
	}
	return fmt.Errorf("the prune was stopped: %w", jobexec.ErrStepCancelled)
}

func (s *Service) stepCheck(ctx context.Context, sc *jobexec.StepContext) error {
	var in protocol.BackupVerifyInput
	if err := json.Unmarshal(sc.Input, &in); err != nil || !protocol.ValidReadDataSubset(in.ReadDataSubset) {
		return backup.Refuse(domain.ErrorRejected, "invalid verification input", "Run the verification again.")
	}
	sc.Progress(ctx, 5, "opening the backup repository")
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	sc.Progress(ctx, 10, "checking the repository")
	res, err := backup.Verify(ctx, o.Repo, in.ReadDataSubset)
	out := protocol.VerifyOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: in.Repository.KeyGeneration,
		ReadData: res.ReadData, Damaged: res.Damaged, Snapshots: res.Snapshots}
	if err == nil {
		if all, lerr := o.Repo.Snapshots(ctx, restic.SnapshotFilter{Tags: []string{backup.TagManifest}}); lerr == nil {
			out.Manifests = readManifests(ctx, o.Repo, all, 10)
		}
	}
	if serr := setVerifyOutput(ctx, sc, out); serr != nil {
		return serr
	}
	return err
}

// setVerifyOutput stores the output, dropping manifests and then the
// oldest snapshots until it fits the result size limit (the index only
// uses them to catch up).
func setVerifyOutput(ctx context.Context, sc *jobexec.StepContext, out protocol.VerifyOutput) error {
	for {
		err := sc.SetOutput(ctx, out)
		if !errors.Is(err, jobexec.ErrOutputTooLarge) {
			return err
		}
		switch {
		case len(out.Manifests) > 0:
			out.Manifests = out.Manifests[:len(out.Manifests)/2]
		case len(out.Snapshots) > 0:
			out.Snapshots = out.Snapshots[len(out.Snapshots)/2:]
		default:
			return err
		}
	}
}
