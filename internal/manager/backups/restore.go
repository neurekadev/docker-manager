package backups

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/backup"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Host restores (#10): a stack's definition and workspace, volumes, or one
// file, from a snapshot, by the agent of the environment the data lives in
// now (restore.run). Manager-state restores are the owner procedure
// (manager.restore). Full and paths restores (#10) need an agent announcing
// protocol.FeatureRestoreSelection.

// RestoreRequest selects what to restore from a snapshot.
type RestoreRequest struct {
	// Scope is protocol.RestoreScopeStack, RestoreScopeVolume,
	// RestoreScopeFile, RestoreScopeFull or RestoreScopePaths.
	Scope string
	// Volumes limits a volume restore of a stack snapshot (default: every
	// volume of the snapshot).
	Volumes []string
	// File is the file's path inside the snapshot (file scope).
	File string
	// Paths are the files and directories inside the snapshot a paths
	// restore puts back.
	Paths []string
	// Redeploy (full scope of a stack backup) deploys the stack with the
	// services that were running once the restore succeeded.
	Redeploy bool
	// Shutdown stops the containers using the data while it is restored
	// (default true in the API).
	Shutdown bool
}

// ErrManagerStateRestore is returned for host restores of a manager-state
// snapshot: those use the owner's manager restore procedure.
var ErrManagerStateRestore = errors.New("manager-state backups are restored with the manager restore procedure")

// ErrRestoreAgentOutdated is returned when the environment's agent
// predates full and paths restores (or is offline, so it cannot say).
var ErrRestoreAgentOutdated = errors.New("the environment's agent cannot restore a whole backup or selected paths")

// FeatureHub reports whether an environment's connected agent announced
// a capabilities feature (implemented by *agents.Hub).
type FeatureHub interface {
	EnvironmentHasFeature(environmentID, feature string) bool
}

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
	// A full restore of a volume backup is that volume's restore.
	if req.Scope == protocol.RestoreScopeFull && sn.Kind == backup.MemberVolume {
		req.Scope, req.Volumes, req.Redeploy = protocol.RestoreScopeVolume, nil, false
		in.Scope = req.Scope
	}
	stackTarget := func() error {
		if st == nil {
			return fieldErr("scope", "the stack no longer exists in Docker Manager; import or create it first, then restore")
		}
		p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetStack, ID: st.ID})
		return nil
	}
	addVolume := func(v string) {
		if slices.ContainsFunc(in.Volumes, func(x protocol.RestoreVolume) bool { return x.Name == v }) {
			return
		}
		rv := protocol.RestoreVolume{Name: v, Source: sn.VolumePaths[v]}
		if sn.Kind == backup.MemberStack && sn.StackName != "" && strings.HasPrefix(v, sn.StackName+"_") {
			rv.ComposeProject, rv.ComposeKey = sn.StackName, strings.TrimPrefix(v, sn.StackName+"_")
		}
		in.Volumes = append(in.Volumes, rv)
		p.targets = append(p.targets, domain.JobTarget{Type: domain.TargetVolume, ID: v, EnvironmentID: p.environmentID})
	}
	switch req.Scope {
	case protocol.RestoreScopeStack:
		if sn.Kind != backup.MemberStack {
			return restorePlan{}, fieldErr("scope", "only stack backups hold a stack definition")
		}
		if err := stackTarget(); err != nil {
			return restorePlan{}, err
		}
	case protocol.RestoreScopeFull:
		if sn.Kind != backup.MemberStack {
			return restorePlan{}, fieldErr("scope", "only stack and volume backups can be restored whole")
		}
		if err := stackTarget(); err != nil {
			return restorePlan{}, err
		}
		for _, v := range sn.Volumes {
			addVolume(v)
		}
		in.Redeploy = req.Redeploy
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
			addVolume(v)
		}
	case protocol.RestoreScopeFile:
		if !protocol.ValidSnapshotPath(req.File) || req.File == "/" {
			return restorePlan{}, fieldErr("path", "must be the absolute path of a file inside the backup")
		}
		in.File = req.File
		t, err := s.pathTarget(sn, st, req.File, p.environmentID)
		if err != nil {
			return restorePlan{}, err
		}
		p.targets = append(p.targets, t)
	case protocol.RestoreScopePaths:
		if err := protocol.ValidRestorePaths(req.Paths); err != nil {
			return restorePlan{}, fieldErr("paths", "%s", strings.TrimPrefix(err.Error(), "restore: "))
		}
		in.Paths = slices.Clone(req.Paths)
		for _, sp := range req.Paths {
			t, err := s.pathTarget(sn, st, sp, p.environmentID)
			if err != nil {
				return restorePlan{}, err
			}
			if t.Type == domain.TargetVolume {
				addVolume(t.ID)
			} else if !slices.ContainsFunc(p.targets, func(x domain.JobTarget) bool { return x.Type == t.Type && x.ID == t.ID }) {
				p.targets = append(p.targets, t)
			}
		}
	default:
		return restorePlan{}, fieldErr("scope", "must be full, paths, stack, volume or file")
	}
	if req.Redeploy && req.Scope != protocol.RestoreScopeFull {
		return restorePlan{}, fieldErr("redeploy", "only a full restore of a stack backup redeploys")
	}
	if err := in.Validate(); err != nil {
		return restorePlan{}, fieldErr("scope", "%s", err.Error())
	}
	// Agents reject unknown fields: the new scopes reach only agents that
	// announced them (#34 version window).
	if in.Scope == protocol.RestoreScopeFull || in.Scope == protocol.RestoreScopePaths {
		if fh, ok := s.opts.Agents.(FeatureHub); ok && !fh.EnvironmentHasFeature(p.environmentID, protocol.FeatureRestoreSelection) {
			return restorePlan{}, ErrRestoreAgentOutdated
		}
	}
	p.targets = append(p.targets, s.restoreContainers(ctx, p, sn)...)
	p.targets = append(p.targets, repoTarget(repo.ID))
	p.input = in
	return p, nil
}

// pathTarget is the root a snapshot path belongs to: one of the
// snapshot's volumes, else the stack's project directory.
func (s *Service) pathTarget(sn domain.BackupSnapshot, st *domain.Stack, sp, env string) (domain.JobTarget, error) {
	owner := ""
	for v, vp := range sn.VolumePaths {
		if sp == vp || strings.HasPrefix(sp, vp+"/") {
			owner = v
		}
	}
	switch {
	case owner != "":
		return domain.JobTarget{Type: domain.TargetVolume, ID: owner, EnvironmentID: env}, nil
	case sn.Kind == backup.MemberVolume:
		return domain.JobTarget{Type: domain.TargetVolume, ID: sn.Volume, EnvironmentID: env}, nil
	case st != nil:
		return domain.JobTarget{Type: domain.TargetStack, ID: st.ID}, nil
	}
	return domain.JobTarget{}, fieldErr("path", "the stack of %s no longer exists in Docker Manager", sp)
}

// restoreContainers are lock-only targets for the containers outside the
// restored stack that mount a restored volume (a standalone container):
// the restore stops them, and nothing starts them until it ended
// (jobspec.Spec.StartsContainers). Best effort: without the list the
// agent still stops them, only the refusal of starts is weaker.
func (s *Service) restoreContainers(ctx context.Context, p restorePlan, sn domain.BackupSnapshot) []domain.JobTarget {
	if s.opts.Volumes == nil {
		return nil
	}
	vols := map[string]bool{}
	for _, t := range p.targets {
		if t.Type == domain.TargetVolume {
			vols[t.ID] = true
		}
	}
	if len(vols) == 0 {
		return nil
	}
	list, err := s.opts.Volumes.ListContainers(ctx, p.environmentID)
	if err != nil {
		return nil
	}
	var out []domain.JobTarget
	for _, c := range list {
		if c.Stack != nil && c.Stack.Project == sn.StackName && sn.Kind == backup.MemberStack {
			continue // the restored stack's own containers: its stack lock
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" && vols[m.Name] {
				out = append(out, domain.JobTarget{Type: domain.TargetContainer, ID: strings.TrimPrefix(c.Name, "/"), EnvironmentID: p.environmentID})
				break
			}
		}
	}
	return out
}

// restoreRefusals are the agent's refusals of a restore as requested
// (internal/agent/backups): their messages name paths and volumes of the
// request, never contents.
var restoreRefusals = []string{protocol.CodeSnapshotPathUnknown, protocol.CodePathNotRestorable, protocol.CodeTargetMissing}

// RestoreRefusal is the agent's refusal of a restore preview.
type RestoreRefusal struct {
	Class   string
	Message string
}

func (e *RestoreRefusal) Error() string { return e.Class + ": " + e.Message }

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
		var ce protocol.CodedError
		if errors.As(err, &ce) && slices.Contains(restoreRefusals, ce.ProtocolCode()) {
			return protocol.RestorePreviewOutput{}, nil, &RestoreRefusal{Class: ce.ProtocolCode(), Message: ce.ProtocolMessage()}
		}
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
	audit.SetDetail(ctx, "scope", p.input.Scope)
	if req.File != "" {
		audit.SetDetail(ctx, "path", req.File)
	}
	if len(p.input.Paths) > 0 {
		audit.SetDetail(ctx, "pathCount", len(p.input.Paths))
	}
	if p.input.Redeploy {
		audit.SetDetail(ctx, "redeploy", true)
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
	definition := in.Scope == protocol.RestoreScopeStack || in.Scope == protocol.RestoreScopeFull ||
		((in.Scope == protocol.RestoreScopeFile || in.Scope == protocol.RestoreScopePaths) && in.Project != nil)
	rec, ok := s.opts.Stacks.(ObservedRecorder)
	if !definition || !ok {
		return nil
	}
	stackID := in.StackID
	var running []string
	if in.Redeploy {
		var out protocol.RestoreRunOutput
		if json.Unmarshal(j.ResultOutput, &out) == nil && out.Shutdown != nil {
			running = wasRunning(out.Shutdown, in.StackName)
		}
	}
	initiator := jobPrincipal(j)
	base := context.WithoutCancel(hookCtx)
	go func() {
		// After the job's transaction: RecordObserved reads the definition
		// from the agent and writes a revision; the redeploy is a job of
		// its own (the engine authorizes stack.deploy for the initiator).
		ctx, cancel := context.WithTimeout(base, time.Minute)
		defer cancel()
		if _, err := rec.RecordObserved(ctx, stackID, domain.RevisionExternal, authz.Service()); err != nil {
			s.log.Warn("could not record the restored stack definition", "stack_id", stackID, "error", err)
		}
		if in.Redeploy {
			s.redeploy(ctx, j.ID, stackID, running, initiator)
		}
	}()
	return nil
}

// wasRunning lists the services of project that ran before the restore
// stopped them.
func wasRunning(r *protocol.ShutdownReport, project string) []string {
	var out []string
	for _, st := range r.PreState {
		if st.Project == project && st.Running && !slices.Contains(out, st.Service) {
			out = append(out, st.Service)
		}
	}
	return out
}

// StackDeployer deploys a stack (implemented by *stacks.Service).
type StackDeployer interface {
	Deploy(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest, o domain.StackDeployOptions) (domain.Job, error)
}

// redeploy deploys a fully restored stack from its restored definition,
// with the services that were running before (none running: nothing is
// started, the stack only shows its undeployed changes).
func (s *Service) redeploy(ctx context.Context, restoreJobID, stackID string, services []string, p authz.Principal) {
	dep, ok := s.opts.Stacks.(StackDeployer)
	if !ok || len(services) == 0 {
		return
	}
	st, err := s.opts.Stacks.Get(ctx, stackID)
	if err != nil {
		s.log.Warn("could not deploy the restored stack", "stack_id", stackID, "error", err)
		return
	}
	slices.Sort(services)
	if _, err := dep.Deploy(ctx, p, st, domain.StackJobRequest{IdempotencyKey: "restore-redeploy:" + restoreJobID, Services: services},
		domain.StackDeployOptions{}); err != nil {
		s.log.Warn("could not deploy the restored stack", "stack_id", stackID, "restore_job_id", restoreJobID, "error", err)
	}
}

// jobPrincipal is the principal that started a job (scheduled and
// internal work: the service identity).
func jobPrincipal(j domain.Job) authz.Principal {
	switch j.Origin {
	case domain.OriginAPIToken:
		return authz.Principal{Kind: authz.KindAPIToken, UserID: j.InitiatorUserID, TokenID: j.InitiatorTokenID}
	case domain.OriginManual:
		return authz.Principal{Kind: authz.KindUser, UserID: j.InitiatorUserID}
	}
	return authz.Service()
}

// ObservedRecorder records a stack definition observed on disk
// (implemented by *stacks.Service).
type ObservedRecorder interface {
	RecordObserved(ctx context.Context, stackID string, source domain.RevisionSource, author authz.Principal) (*domain.StackRevision, error)
}
