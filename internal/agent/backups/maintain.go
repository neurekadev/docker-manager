package backups

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/restic"
)

// backup.retention: forget what the policy's rules do not keep in this
// environment's repository (exactly the preview's decision), then prune.
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
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	res, err := backup.ApplyRetention(ctx, o.Repo, in.PolicyID, in.Rules, in.TimeZone)
	out := protocol.RetentionOutput{ResticRepositoryID: o.ResticRepositoryID, KeyGeneration: in.Repository.KeyGeneration,
		Forgotten: res.Forgotten, Kept: res.Kept}
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	return err
}

func (s *Service) stepPrune(ctx context.Context, sc *jobexec.StepContext) error {
	var in protocol.BackupRetentionInput
	if err := json.Unmarshal(sc.Input, &in); err != nil {
		return err
	}
	var out protocol.RetentionOutput
	_ = json.Unmarshal(sc.Output(), &out)
	o, err := s.openForJob(ctx, sc, in.Repository, false)
	if err != nil {
		return err
	}
	out.ReclaimedBytes, err = backup.Prune(ctx, o.Repo)
	if err != nil {
		out.PruneError = restic.CodeOf(err)
	}
	if serr := sc.SetOutput(ctx, out); serr != nil {
		return serr
	}
	return err
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
