package backups

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/restic"
)

// Finish hooks record what backup jobs did, inside the transaction that
// finishes the job: set members, the snapshot index, location state and
// key rotation progress. They tolerate missing or malformed output (a job
// cancelled while queued has none) and never fail the transaction for it.

func (s *Service) registerHooks() {
	e := s.opts.Jobs
	e.OnFinish(jobspec.BackupRun, s.onBackupRun)
	e.OnFinish(jobspec.ManagerBackup, s.onManagerBackup)
	e.OnFinish(jobspec.BackupRetention, s.onRetention)
	e.OnFinish(jobspec.ManagerRetention, s.onRetention)
	e.OnFinish(jobspec.BackupVerify, s.onVerify)
	e.OnFinish(jobspec.ManagerVerify, s.onVerify)
	e.OnFinish(jobspec.RestoreRun, s.onRestore)
	e.OnFinish(jobspec.BackupImport, s.onImport)
}

func jobClass(j domain.Job) string {
	if j.ErrorClass != "" {
		return j.ErrorClass
	}
	if j.State == domain.JobCancelled {
		return domain.ErrorCancelled
	}
	return string(j.State)
}

func (s *Service) onBackupRun(ctx context.Context, db bun.IDB, j domain.Job) error {
	var in protocol.BackupRunInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		s.log.Warn("backup job with unreadable input", "job_id", j.ID)
		return nil
	}
	var out protocol.BackupRunOutput
	if len(j.ResultOutput) > 0 {
		if err := json.Unmarshal(j.ResultOutput, &out); err != nil {
			s.log.Warn("backup job with unreadable output", "job_id", j.ID)
		}
	}
	scope := backup.EnvironmentScope(j.EnvironmentID)
	items := map[string]bool{}
	for _, it := range in.Items {
		items[it.Key()] = true
	}
	return s.recordMembers(ctx, db, j, in.SetID, in.PolicyID, in.Repository.RepositoryID, scope, items, out.Members,
		out.ResticRepositoryID, out.KeyGeneration, "")
}

func (s *Service) onManagerBackup(ctx context.Context, db bun.IDB, j domain.Job) error {
	var in managerBackupInput
	if err := json.Unmarshal(j.Input, &in); err != nil {
		return nil
	}
	var out managerBackupOutput
	if len(j.ResultOutput) > 0 {
		_ = json.Unmarshal(j.ResultOutput, &out)
	}
	var members []backup.Member
	if out.Member != nil {
		members = append(members, *out.Member)
	}
	return s.recordMembers(ctx, db, j, in.SetID, in.PolicyID, in.RepositoryID, backup.ScopeManager,
		map[string]bool{backup.ItemManagerState: true}, members, out.ResticRepositoryID, out.KeyGeneration, out.ManifestSnapshotID)
}

// recordMembers applies a backup job's results to its set, the snapshot
// index and the location.
func (s *Service) recordMembers(ctx context.Context, db bun.IDB, j domain.Job, setID, policyID, repositoryID, scope string,
	items map[string]bool, results []backup.Member, resticID string, keyGen int, manifestID string) error {
	now := s.now()
	byItem := map[string]backup.Member{}
	var latest *time.Time
	for _, m := range results {
		byItem[m.Item] = m
		if m.SnapshotID == "" {
			continue
		}
		env, _ := backup.ScopeEnvironment(scope)
		t := m.SnapshotTime
		if latest == nil || t.After(*latest) {
			latest = &t
		}
		state := m.State
		if state != backup.StatePartial {
			state = backup.StateComplete
		}
		sn := domain.BackupSnapshot{ID: ids.New(), SetID: setID, PolicyID: policyID, RepositoryID: repositoryID, Scope: scope,
			EnvironmentID: env, Kind: m.Kind, Item: m.Item, StackID: m.StackID, StackName: m.StackName, Volume: m.Volume,
			ResticSnapshotID: m.SnapshotID, SnapshotTime: m.SnapshotTime, Paths: m.Paths, Volumes: m.Volumes, ProjectPath: m.ProjectPath, VolumePaths: m.VolumePaths, Consistency: m.Consistency,
			State: state, ErrorClass: m.ErrorClass, BytesTotal: m.Bytes, JobID: j.ID, CreatedAt: now}
		if _, err := store.InsertBackupSnapshot(ctx, db, &sn); err != nil {
			return err
		}
	}
	if resticID != "" || keyGen > 0 {
		if err := store.UpsertBackupLocation(ctx, db, repositoryID, scope, store.LocationUpdate{ResticRepositoryID: resticID,
			KeyGeneration: keyGen, Initialized: resticID != "", BackupAt: latest}, now); err != nil &&
			!errors.Is(err, domain.ErrBackupRepositoryNotFound) {
			return err
		}
	}
	set, err := store.GetBackupSet(ctx, db, setID)
	if errors.Is(err, domain.ErrBackupSetNotFound) {
		return s.completeRotation(ctx, db)
	}
	if err != nil {
		return err
	}
	for i := range set.Members {
		m := &set.Members[i]
		if m.Scope != scope || !items[m.Item] {
			continue
		}
		m.JobID = j.ID
		r, ok := byItem[m.Item]
		switch {
		case ok && r.SnapshotID != "":
			m.State, m.SnapshotID, m.ErrorClass = r.State, r.SnapshotID, r.ErrorClass
			if m.State != backup.StatePartial {
				m.State = backup.StateComplete
			}
			t := r.SnapshotTime
			m.SnapshotTime = &t
		case ok:
			m.State, m.ErrorClass = backup.StateFailed, r.ErrorClass
			if m.ErrorClass == "" {
				m.ErrorClass = jobClass(j)
			}
		case j.State == domain.JobSucceeded:
			m.State, m.ErrorClass = backup.StateFailed, "no_result"
		default:
			m.State, m.ErrorClass = backup.StateFailed, jobClass(j)
		}
	}
	if manifestID != "" {
		set.ManifestSnapshotID = manifestID
	}
	wasPending := set.State == backup.StatePending
	s.settle(&set)
	if wasPending && set.State != backup.StatePending && set.State != backup.StateFailed && set.FollowUp == "" && set.PolicyID != "" {
		if p, err := store.GetBackupPolicy(ctx, db, set.PolicyID); err == nil && p.Retention.AfterBackup && !retentionRules(p.Retention).Empty() {
			set.FollowUp = "retention"
			s.poke()
		}
	}
	if err := store.UpdateBackupSet(ctx, db, &set); err != nil {
		return err
	}
	return s.completeRotation(ctx, db)
}

func (s *Service) onRetention(ctx context.Context, db bun.IDB, j domain.Job) error {
	var out protocol.RetentionOutput
	if len(j.ResultOutput) == 0 || json.Unmarshal(j.ResultOutput, &out) != nil {
		return nil
	}
	repoID, scope := repositoryOf(j), backup.ScopeManager
	if j.EnvironmentID != "" {
		scope = backup.EnvironmentScope(j.EnvironmentID)
	}
	now := s.now()
	if err := store.MarkBackupSnapshotsForgotten(ctx, db, repoID, scope, out.Forgotten, now); err != nil {
		return err
	}
	if err := store.UpsertBackupLocation(ctx, db, repoID, scope, store.LocationUpdate{ResticRepositoryID: out.ResticRepositoryID,
		KeyGeneration: out.KeyGeneration}, now); err != nil && !errors.Is(err, domain.ErrBackupRepositoryNotFound) {
		return err
	}
	return s.completeRotation(ctx, db)
}

func (s *Service) onVerify(ctx context.Context, db bun.IDB, j domain.Job) error {
	var out protocol.VerifyOutput
	if len(j.ResultOutput) > 0 {
		_ = json.Unmarshal(j.ResultOutput, &out)
	}
	repoID, scope := repositoryOf(j), backup.ScopeManager
	if j.EnvironmentID != "" {
		scope = backup.EnvironmentScope(j.EnvironmentID)
	}
	now := s.now()
	result := "ok"
	if j.State != domain.JobSucceeded {
		result = jobClass(j)
	}
	u := store.LocationUpdate{ResticRepositoryID: out.ResticRepositoryID, KeyGeneration: out.KeyGeneration, VerifyResult: result,
		VerifyJobID: j.ID, Initialized: out.ResticRepositoryID != ""}
	if result == "ok" {
		u.VerifiedAt = &now
	}
	if err := store.UpsertBackupLocation(ctx, db, repoID, scope, u, now); err != nil {
		if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
			return nil
		}
		return err
	}
	if result == "ok" {
		if err := store.MarkBackupSnapshotsVerified(ctx, db, repoID, scope, now); err != nil {
			return err
		}
		if err := s.indexListing(ctx, db, repoID, scope, out.Snapshots, out.Manifests, j.ID); err != nil {
			return err
		}
	}
	return s.completeRotation(ctx, db)
}

// indexListing adds snapshots found in a repository that the index does
// not know yet (a restored manager learns the snapshots written after its
// manager-state snapshot, #24). Known ones are left unchanged.
func (s *Service) indexListing(ctx context.Context, db bun.IDB, repositoryID, scope string, snaps []restic.Snapshot,
	manifests []backup.Manifest, jobID string) error {
	members := map[string]backup.Member{}
	for _, m := range manifests {
		for _, mem := range m.Members {
			if mem.SnapshotID != "" {
				members[mem.SnapshotID] = mem
			}
		}
	}
	env, _ := backup.ScopeEnvironment(scope)
	now := s.now()
	for _, sn := range snaps {
		if !sn.HasTag(backup.TagDockYard) && !sn.HasTag(backup.TagManagerState) {
			continue
		}
		item := backup.ItemOf(sn.Tags)
		if item == "" {
			continue
		}
		rec := domain.BackupSnapshot{ID: ids.New(), SetID: backup.SetOf(sn.Tags), PolicyID: backup.PolicyOf(sn.Tags), RepositoryID: repositoryID,
			Scope: scope, EnvironmentID: env, Item: item, ResticSnapshotID: sn.ID, SnapshotTime: sn.Time.UTC(), Paths: sn.Paths,
			State: backup.StateComplete, JobID: jobID, CreatedAt: now}
		switch {
		case item == backup.ItemManagerState:
			rec.Kind = backup.MemberManagerState
		case strings.HasPrefix(item, "stack/"):
			rec.Kind, rec.StackID = backup.MemberStack, strings.TrimPrefix(item, "stack/")
		case strings.HasPrefix(item, "volume/"):
			rec.Kind, rec.Volume = backup.MemberVolume, strings.TrimPrefix(item, "volume/")
		default:
			continue
		}
		if m, ok := members[sn.ID]; ok {
			rec.StackName, rec.Volumes, rec.Consistency, rec.BytesTotal = m.StackName, m.Volumes, m.Consistency, m.Bytes
			rec.ProjectPath, rec.VolumePaths = m.ProjectPath, m.VolumePaths
			if m.State == backup.StatePartial {
				rec.State = backup.StatePartial
			}
		}
		if sn.Summary != nil && rec.BytesTotal == 0 {
			rec.BytesTotal = sn.Summary.TotalBytesProcessed
		}
		if _, err := store.InsertBackupSnapshot(ctx, db, &rec); err != nil {
			return err
		}
	}
	return nil
}
