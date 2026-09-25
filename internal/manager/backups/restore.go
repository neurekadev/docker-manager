package backups

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Host restores (#10): a stack's definition and workspace, volumes, or one
// file, from a snapshot, by the agent of the environment the data lives in
// now (restore.run). Manager-state restores are the owner procedure
// (manager.restore).

// RestoreRequest selects what to restore from a snapshot.
type RestoreRequest struct {
	// Scope is protocol.RestoreScopeStack, RestoreScopeVolume or
	// RestoreScopeFile.
	Scope string
	// Volumes limits a volume restore of a stack snapshot (default: every
	// volume of the snapshot).
	Volumes []string
	// File is the file's path inside the snapshot (file scope).
	File string
	// Shutdown stops the containers using the data while it is restored
	// (default true in the API).
	Shutdown bool
}

// ErrManagerStateRestore is returned for host restores of a manager-state
// snapshot: those use the owner's manager restore procedure.
var ErrManagerStateRestore = errors.New("manager-state backups are restored with the manager restore procedure")

// restoreTarget is where a restore of sn runs and what it needs.
type restorePlan struct {
	environmentID string
	input         protocol.RestoreRunInput
	targets       []domain.JobTarget
}

func (s *Service) planRestore(ctx context.Context, sn domain.BackupSnapshot, req RestoreRequest) (restorePlan, error) {
	if sn.Kind == backup.MemberManagerState {
		return restorePlan{}, ErrManagerStateRestore
	}
	env, ok := backup.ScopeEnvironment(sn.Scope)
	if !ok {
		return restorePlan{}, ErrManagerStateRestore
	}
	repo, err := store.GetBackupRepository(ctx, s.db, sn.RepositoryID)
	if err != nil {
		return restorePlan{}, err
	}
	if repo.State != domain.BackupRepositoryReady {
		return restorePlan{}, domain.ErrRecoveryKeyNotConfirmed
	}
	key, err := s.KeyState(ctx)
	if err != nil {
		return restorePlan{}, err
	}
	in := protocol.RestoreRunInput{Repository: repositoryRef(repo, sn.Scope, key), SnapshotID: sn.ResticSnapshotID, Scope: req.Scope,
		SnapshotPaths: sn.Paths, Shutdown: req.Shutdown}
	p := restorePlan{environmentID: env}
	var st *domain.Stack
	if sn.Kind == backup.MemberStack {
		if got, err := s.stack(ctx, sn.StackID); err == nil {
			st = &got
			// A stack migrated since (#35) is restored where it is now; the
			// repository must be reachable from there (S3).
			if got.EnvironmentID != env {
				if repo.Kind != backup.KindS3 {
					return restorePlan{}, fieldErr("scope", "the stack moved to another environment and the repository is local to %s", env)
				}
				p.environmentID = got.EnvironmentID
			}
			ref := protocol.ProjectRef{Root: got.Root, RootPath: got.RootPath, Dir: got.Dir, ProjectName: got.Name,
				ConfigFiles: got.ConfigFiles, EnvFiles: got.EnvFiles}
			in.StackID, in.StackName, in.Project, in.ProjectSource = got.ID, got.Name, &ref, sn.ProjectPath
		}
	}
	switch req.Scope {
	case protocol.RestoreScopeStack:
		if sn.Kind != backup.MemberStack {
			return restorePlan{}, fieldErr("scope", "only stack backups hold a stack definition")
		}
		if st == nil {
			return restorePlan{}, fieldErr("scope", "the stack no longer exists in DockYard; import or create it first, then restore")
		}
		p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetStack, ID: st.ID})
	case protocol.RestoreScopeVolume:
		vols := req.Volumes
		switch sn.Kind {
		case backup.MemberVolume:
			if len(vols) == 0 {
				vols = []string{sn.Volume}
			}
			if len(vols) != 1 || vols[0] != sn.Volume {
				return restorePlan{}, fieldErr("volumes", "this backup holds volume %s only", sn.Volume)
			}
		case backup.MemberStack:
			if len(vols) == 0 {
				vols = sn.Volumes
			}
			if len(vols) == 0 {
				return restorePlan{}, fieldErr("volumes", "this stack backup holds no volume")
			}
		}
		for _, v := range vols {
			if !slices.Contains(sn.Volumes, v) && v != sn.Volume {
				return restorePlan{}, fieldErr("volumes", "volume %s is not in this backup", v)
			}
			rv := protocol.RestoreVolume{Name: v, Source: sn.VolumePaths[v]}
			if sn.Kind == backup.MemberStack && sn.StackName != "" && strings.HasPrefix(v, sn.StackName+"_") {
				rv.ComposeProject, rv.ComposeKey = sn.StackName, strings.TrimPrefix(v, sn.StackName+"_")
			}
			in.Volumes = append(in.Volumes, rv)
			p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetVolume, ID: v, EnvironmentID: p.environmentID})
		}
	case protocol.RestoreScopeFile:
		if !protocol.ValidSnapshotPath(req.File) || req.File == "/" {
			return restorePlan{}, fieldErr("path", "must be the absolute path of a file inside the backup")
		}
		in.File = req.File
		owner := ""
		for v, p := range sn.VolumePaths {
			if strings.HasPrefix(req.File, p+"/") {
				owner = v
			}
		}
		switch {
		case owner != "":
			p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetVolume, ID: owner, EnvironmentID: p.environmentID})
		case st != nil:
			p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetStack, ID: st.ID})
		case sn.Kind == backup.MemberVolume:
			p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetVolume, ID: sn.Volume, EnvironmentID: p.environmentID})
		default:
			return restorePlan{}, fieldErr("path", "the file's stack no longer exists in DockYard")
		}
	default:
		return restorePlan{}, fieldErr("scope", "must be stack, volume or file")
	}
	if err := in.Validate(); err != nil {
		return restorePlan{}, fieldErr("scope", "%s", err.Error())
	}
	p.targets = append(p.targets, repoTarget(repo.ID))
	p.input = in
	return p, nil
}

// PreviewRestore asks the agent which targets, files, owners, space and
// containers a restore involves and what blocks it. Nothing changes.
func (s *Service) PreviewRestore(ctx context.Context, sn domain.BackupSnapshot, req RestoreRequest) (protocol.RestorePreviewOutput, []domain.JobTarget, error) {
	p, err := s.planRestore(ctx, sn, req)
	if err != nil {
		return protocol.RestorePreviewOutput{}, nil, err
	}
	if s.opts.Agents == nil {
		return protocol.RestorePreviewOutput{}, nil, ErrContentUnavailable
	}
	cred, err := s.credentialFor(ctx, s.db, sn.RepositoryID)
	if err != nil {
		return protocol.RestorePreviewOutput{}, nil, err
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, p.environmentID, protocol.ReqRestorePreview,
		protocol.RestorePreviewInput{Input: p.input, Credential: cred}, 2*time.Minute)
	if err != nil {
		return protocol.RestorePreviewOutput{}, nil, agentFailure(err)
	}
	var out protocol.RestorePreviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, nil, ErrContentUnavailable
	}
	return out, p.targets, nil
}

// Restore queues restore.run (the caller authorized backup.restore on the
// backup; the job engine checks every target again).
func (s *Service) Restore(ctx context.Context, sn domain.BackupSnapshot, req RestoreRequest, principal authz.Principal, idempotencyKey string) (domain.Job, error) {
	p, err := s.planRestore(ctx, sn, req)
	if err != nil {
		return domain.Job{}, err
	}
	j, _, err := s.opts.Jobs.Enqueue(ctx, jobs.Request{Kind: jobspec.RestoreRun, Principal: principal, EnvironmentID: p.environmentID,
		Targets: p.targets, Input: p.input, IdempotencyKey: idempotencyKey})
	if err != nil {
		return j, err
	}
	audit.SetDetail(ctx, "backupId", sn.ID)
	audit.SetDetail(ctx, "scope", req.Scope)
	if req.File != "" {
		audit.SetDetail(ctx, "path", req.File)
	}
	return j, nil
}

// RestoreTargets returns the job targets of a restore (authorization).
func (s *Service) RestoreTargets(ctx context.Context, sn domain.BackupSnapshot, req RestoreRequest) ([]domain.JobTarget, string, error) {
	p, err := s.planRestore(ctx, sn, req)
	return p.targets, p.environmentID, err
}

// onRestore records a stack definition restored on disk as an observed
// revision (after the job's transaction): the stack shows undeployed
// changes and the user decides when to deploy.
func (s *Service) onRestore(hookCtx context.Context, _ bun.IDB, j domain.Job) error {
	if j.State != domain.JobSucceeded {
		return nil
	}
	var in protocol.RestoreRunInput
	if json.Unmarshal(j.Input, &in) != nil || in.StackID == "" {
		return nil
	}
	definition := in.Scope == protocol.RestoreScopeStack || (in.Scope == protocol.RestoreScopeFile && in.Project != nil)
	rec, ok := s.opts.Stacks.(ObservedRecorder)
	if !definition || !ok {
		return nil
	}
	stackID := in.StackID
	base := context.WithoutCancel(hookCtx)
	go func() {
		// After the job's transaction: RecordObserved reads the definition
		// from the agent and writes a revision.
		ctx, cancel := context.WithTimeout(base, time.Minute)
		defer cancel()
		if _, err := rec.RecordObserved(ctx, stackID, domain.RevisionExternal, authz.Service()); err != nil {
			s.log.Warn("could not record the restored stack definition", "stack_id", stackID, "error", err)
		}
	}()
	return nil
}

// ObservedRecorder records a stack definition observed on disk
// (implemented by *stacks.Service).
type ObservedRecorder interface {
	RecordObserved(ctx context.Context, stackID string, source domain.RevisionSource, author authz.Principal) (*domain.StackRevision, error)
}
