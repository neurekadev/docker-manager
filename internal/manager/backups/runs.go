package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// A run of the setup is one backup set: one backup.run job per environment
// and repository (its agent snapshots every selected stack and volume there
// and writes a host manifest into the repository) and a manager.backup job
// per repository (the manager-state snapshot and the set manifest), the
// Primary's jobs first, then the Secondary's: each copy is taken straight
// from the source data. Multi-host sets are not atomic: each member
// records its own snapshot time, a set with failed members is partial,
// never complete, and a retry re-runs only the members that did not
// complete (per repository). A member whose stack or volume was removed
// before its turn is skipped: it counts neither for nor against the set,
// and a set of skipped members only is skipped.

// managerBackupInput is the input of manager.backup.
type managerBackupInput struct {
	SetID          string    `json:"setId"`
	PolicyID       string    `json:"policyId,omitempty"`
	RepositoryID   string    `json:"repositoryId"`
	IncludeMetrics bool      `json:"includeMetrics,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
}

// managerRetentionInput is the input of manager.retention.
type managerRetentionInput struct {
	RepositoryID string                `json:"repositoryId"`
	PolicyID     string                `json:"policyId"`
	Rules        backup.RetentionRules `json:"rules"`
	TimeZone     string                `json:"timeZone"`
	// AnyPolicy also retires the backups of earlier policies (#246).
	AnyPolicy bool `json:"anyPolicy,omitempty"`
}

// managerVerifyInput is the input of manager.verify.
type managerVerifyInput struct {
	RepositoryID   string `json:"repositoryId"`
	ReadDataSubset string `json:"readDataSubset,omitempty"`
}

func verifyInputFor(ref protocol.BackupRepositoryRef, subset string) protocol.BackupVerifyInput {
	return protocol.BackupVerifyInput{Repository: ref, ReadDataSubset: subset}
}

// RunOptions configure a manual run.
type RunOptions struct {
	Principal      authz.Principal
	IdempotencyKey string
	// RetrySetID re-runs the members of that set that did not complete.
	RetrySetID string
	// WithoutManager leaves the manager state out (the caller may not back
	// it up: manager.backup is owner-only).
	WithoutManager bool
}

// RunResult is a started run.
type RunResult struct {
	Set  domain.BackupSet
	Jobs []domain.Job
}

// ErrNothingToRetry is returned when every member of the set completed
// (or was skipped: removed before its turn).
var ErrNothingToRetry = errors.New("every member of the backup set completed or was skipped; nothing to retry")

// RunNow starts a manual run of the setup (or retries a set's missing
// members). The caller authorized backup.run; the job engine authorizes
// every job again.
func (s *Service) RunNow(ctx context.Context, o RunOptions) (RunResult, error) {
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return RunResult{}, err
	}
	if err := s.checkReady(ctx, st); err != nil {
		return RunResult{}, err
	}
	now := s.now()
	setID := ids.New()
	existing := false
	if o.IdempotencyKey != "" && o.RetrySetID == "" {
		// A repeated request reuses its set (and so the jobs' inputs).
		setID = scheduledSetID("manual:" + o.Principal.Key() + ":" + o.IdempotencyKey)
		if prev, err := store.GetBackupSet(ctx, s.db, setID); err == nil {
			now, existing = prev.StartedAt, true
		}
	}
	if !existing && o.RetrySetID == "" {
		// One run at a time, like scheduled runs (which skip): a second
		// run would queue behind the first and back up the same data again.
		active, err := store.ActivePolicyJob(ctx, s.db, st.ID, []domain.JobKind{jobspec.BackupRun, jobspec.ManagerBackup}, nil)
		if err != nil {
			return RunResult{}, err
		}
		if active != "" {
			return RunResult{}, &domain.BackupRunActiveError{JobID: active}
		}
	}
	var only map[string]bool
	if o.RetrySetID != "" {
		prev, err := store.GetBackupSet(ctx, s.db, o.RetrySetID)
		if err != nil {
			return RunResult{}, err
		}
		if prev.PolicyID != st.ID {
			return RunResult{}, domain.ErrBackupSetNotFound
		}
		only = retryItems(prev.Members)
		if len(only) == 0 {
			return RunResult{}, ErrNothingToRetry
		}
		setID = prev.ID
	}
	set, reqs, err := s.planRun(ctx, s.db, st, setID, now, only, !o.WithoutManager)
	if err != nil {
		return RunResult{}, err
	}
	set.Origin = originOf(o.Principal)
	switch {
	case existing:
		if set, err = store.GetBackupSet(ctx, s.db, setID); err != nil {
			return RunResult{}, err
		}
	case o.RetrySetID == "":
		if _, err := store.InsertBackupSet(ctx, s.db, &set); err != nil {
			return RunResult{}, err
		}
	default:
		if err := s.markRetry(ctx, set); err != nil {
			return RunResult{}, err
		}
	}
	out := RunResult{Set: set}
	for i, req := range reqs {
		req.Principal, req.PolicyID = o.Principal, st.ID
		if o.IdempotencyKey != "" {
			req.IdempotencyKey = fmt.Sprintf("%s#%d", o.IdempotencyKey, i)
		}
		j, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			s.failPlanned(ctx, set.ID, req, err)
			if i == 0 {
				return RunResult{}, err
			}
			continue
		}
		out.Jobs = append(out.Jobs, j)
	}
	audit.AddTarget(ctx, domain.AuditTarget{Type: "backup_set", ID: set.ID})
	audit.SetDetail(ctx, "jobs", len(out.Jobs))
	audit.SetDetail(ctx, "retry", o.RetrySetID != "")
	return out, nil
}

// memberKey identifies a member of a set: its repository, scope and item
// (a run backs every item up to each repository).
func memberKey(repositoryID, scope, item string) string {
	return repositoryID + "\x00" + scope + "\x00" + item
}

// retryItems are the members a retry of a set re-runs: the ones that did
// not complete, except skipped ones (removed before their turn: nothing to
// retry).
func retryItems(members []domain.BackupSetMember) map[string]bool {
	only := map[string]bool{}
	for _, m := range members {
		if m.State != backup.StateComplete && m.State != backup.StatePending && m.State != backup.StateSkipped {
			only[memberKey(m.RepositoryID, m.Scope, m.Item)] = true
		}
	}
	return only
}

func originOf(p authz.Principal) domain.JobOrigin {
	switch p.Kind {
	case authz.KindService:
		return domain.OriginScheduled
	case authz.KindAPIToken:
		return domain.OriginAPIToken
	}
	return domain.OriginManual
}

// checkReady refuses runs without a Primary repository and to repositories
// whose key is not confirmed.
func (s *Service) checkReady(ctx context.Context, st domain.BackupSetup) error {
	if st.PrimaryRepositoryID == "" {
		return domain.ErrBackupNoPrimary
	}
	for _, id := range st.Repositories() {
		r, err := store.GetBackupRepository(ctx, s.db, id)
		if err != nil {
			return err
		}
		if r.State != domain.BackupRepositoryReady {
			return domain.ErrRecoveryKeyNotConfirmed
		}
	}
	return nil
}

// planRun computes a run's set members and job requests (without
// principal, policy ID and idempotency key): every environment's, then the
// manager state's, for the Primary, then the same for the Secondary. only
// limits it to the given members (retries).
func (s *Service) planRun(ctx context.Context, db bun.IDB, st domain.BackupSetup, setID string, startedAt time.Time,
	only map[string]bool, withManager bool) (domain.BackupSet, []jobs.Request, error) {
	key, _, err := store.GetBackupKey(ctx, db)
	if err != nil {
		return domain.BackupSet{}, nil, err
	}
	set := domain.BackupSet{ID: setID, PolicyID: st.ID, PolicyName: SetupName, State: backup.StatePending, StartedAt: startedAt,
		UpdatedAt: startedAt}
	plans, err := s.planItems(ctx, st)
	if err != nil {
		return set, nil, err
	}
	var reqs []jobs.Request
	archived, envs := 0, 0
	for _, repoID := range st.Repositories() {
		repo, err := store.GetBackupRepository(ctx, db, repoID)
		if err != nil {
			return set, nil, err
		}
		for _, e := range plans {
			scope := backup.EnvironmentScope(e.EnvironmentID)
			env, envErr := s.environment(ctx, e.EnvironmentID)
			if envErr == nil && env.Status == domain.EnvironmentArchived {
				// Archived hosts are hidden from operations (#34): their
				// selections resume after a re-attach.
				archived++
				continue
			}
			in := protocol.BackupRunInput{SetID: setID, PolicyID: st.ID, PolicyName: SetupName, InstanceID: s.opts.InstanceID,
				Repository: repositoryRef(repo, scope, key.State), Shutdown: st.Shutdown, StartedAt: startedAt}
			if envErr == nil {
				in.EnvironmentName = env.Name
			}
			// Agents reject unknown input fields: live activity is asked only
			// of agents announcing it (#10; an older agent backs up without).
			if fh, ok := s.opts.Agents.(FeatureHub); ok && fh.EnvironmentHasFeature(e.EnvironmentID, protocol.FeatureBackupActivity) {
				in.Activity = true
			}
			targets := []domain.JobTarget{}
			for _, it := range e.Items {
				if only != nil && !only[memberKey(repo.ID, scope, it.Key())] {
					continue
				}
				in.Items = append(in.Items, it)
				switch it.Kind {
				case backup.MemberStack:
					targets = append(targets, domain.JobTarget{Type: domain.TargetStack, ID: it.StackID})
				case backup.MemberVolume:
					targets = append(targets, domain.JobTarget{Type: domain.TargetVolume, ID: it.Volume})
				}
				set.Members = append(set.Members, domain.BackupSetMember{Item: it.Key(), Kind: it.Kind, Scope: scope,
					RepositoryID: repo.ID, EnvironmentID: e.EnvironmentID, StackID: it.StackID, StackName: it.StackName,
					Volume: it.Volume, State: backup.StatePending})
			}
			if len(in.Items) == 0 {
				continue
			}
			if repoID == st.PrimaryRepositoryID {
				envs++
			}
			targets = append(targets, repoTarget(repo.ID))
			reqs = append(reqs, jobs.Request{Kind: jobspec.BackupRun, EnvironmentID: e.EnvironmentID, Targets: targets, Input: in})
		}
		if withManager && (only == nil || only[memberKey(repo.ID, backup.ScopeManager, backup.ItemManagerState)]) {
			set.Members = append(set.Members, domain.BackupSetMember{Item: backup.ItemManagerState, Kind: backup.MemberManagerState,
				Scope: backup.ScopeManager, RepositoryID: repo.ID, State: backup.StatePending})
			reqs = append(reqs, jobs.Request{Kind: jobspec.ManagerBackup, Targets: []domain.JobTarget{repoTarget(repo.ID)},
				Input: managerBackupInput{SetID: setID, PolicyID: st.ID, RepositoryID: repo.ID, IncludeMetrics: st.IncludeMetrics,
					StartedAt: startedAt}})
		}
	}
	if len(reqs) == 0 && archived > 0 {
		return set, nil, fieldErr("excludeEnvironments", "only archived environments have stacks and volumes to back up")
	}
	if len(reqs) == 0 {
		return set, nil, fieldErr("excludeEnvironments", "there is nothing to back up")
	}
	if len(reqs) > scheduler.MaxJobsPerRun {
		return set, nil, fieldErr("excludeEnvironments", "a run may span at most %d environments (%d now)",
			scheduler.MaxJobsPerRun/len(st.Repositories())-1, envs)
	}
	return set, reqs, nil
}

// markRetry resets the retried members of an existing set to pending.
func (s *Service) markRetry(ctx context.Context, planned domain.BackupSet) error {
	return s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		set, err := store.GetBackupSet(ctx, tx, planned.ID)
		if err != nil {
			return err
		}
		retry := map[string]bool{}
		for _, m := range planned.Members {
			retry[memberKey(m.RepositoryID, m.Scope, m.Item)] = true
		}
		for i := range set.Members {
			m := &set.Members[i]
			if retry[memberKey(m.RepositoryID, m.Scope, m.Item)] {
				m.State, m.ErrorClass = backup.StatePending, ""
			}
		}
		set.State, set.FinishedAt, set.UpdatedAt = backup.StatePending, nil, s.now()
		return store.UpdateBackupSet(ctx, tx, &set)
	})
}

// failPlanned marks the members of a request that could not be enqueued.
func (s *Service) failPlanned(ctx context.Context, setID string, req jobs.Request, cause error) {
	class := "enqueue_failed"
	switch {
	case errors.Is(cause, domain.ErrJobForbidden):
		class = domain.ErrorAuthorizationRevoked
	case errors.Is(cause, domain.ErrJobInvalid):
		class = domain.ErrorRejected
	}
	repoID := repositoryOf(domain.Job{Targets: req.Targets})
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		set, err := store.GetBackupSet(ctx, tx, setID)
		if err != nil {
			return err
		}
		scope := backup.ScopeManager
		if req.EnvironmentID != "" {
			scope = backup.EnvironmentScope(req.EnvironmentID)
		}
		for i := range set.Members {
			m := &set.Members[i]
			if m.Scope == scope && m.RepositoryID == repoID && m.State == backup.StatePending {
				m.State, m.ErrorClass = backup.StateFailed, class
			}
		}
		wasPending := set.State == backup.StatePending
		s.settle(&set)
		s.flagRetention(ctx, tx, &set, wasPending)
		return store.UpdateBackupSet(ctx, tx, &set)
	})
	if err != nil {
		s.log.Warn("could not record a backup job that was not queued", "set_id", setID, "error", err)
	}
}

// settle recomputes a set's state and finish time.
//
// A set stays pending while any member still runs, even when another one
// already failed (Completeness calls that partial): the set finishes, and
// its retention follows, only once every environment has reported.
func (s *Service) settle(set *domain.BackupSet) {
	members := make([]backup.Member, 0, len(set.Members))
	running := false
	for _, m := range set.Members {
		members = append(members, backup.Member{State: m.State, SnapshotID: m.SnapshotID})
		running = running || m.State == backup.StatePending
	}
	set.State = backup.Completeness(members)
	if running {
		set.State = backup.StatePending
	}
	now := s.now()
	set.UpdatedAt = now
	if set.State != backup.StatePending && set.FinishedAt == nil {
		set.FinishedAt = &now
	}
}

// flagRetention marks a set that has just finished for retention after
// the backup (RunFollowUps queues it): once per set, only after every
// member settled, never for a failed set or a skipped one (it wrote no
// snapshot). Retention therefore runs once per backup run and location,
// never per stack or volume.
func (s *Service) flagRetention(ctx context.Context, db bun.IDB, set *domain.BackupSet, wasPending bool) {
	if !wasPending || set.State == backup.StatePending || set.State == backup.StateFailed || set.State == backup.StateSkipped ||
		set.FollowUp != "" || set.PolicyID == "" {
		return
	}
	if st, err := store.GetBackupSetup(ctx, db); err == nil && st.ID == set.PolicyID && st.Retention.AfterBackup &&
		retentionActive(st.Retention) {
		set.FollowUp = "retention"
		s.poke()
	}
}

// scheduledSetID derives a scheduled run's set ID from its idempotency
// key, so a repeated Jobs call for the same run reuses the set.
func scheduledSetID(dueKey string) string {
	sum := sha256.Sum256([]byte("docker-manager/backup-set/" + dueKey))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// --- retention runs ---

// RetentionRun queues retention on every location that holds Docker
// Manager backups (manual run; idempotency key per location).
func (s *Service) RetentionRun(ctx context.Context, principal authz.Principal, idempotencyKey string) ([]domain.Job, error) {
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if !retentionActive(st.Retention) {
		return nil, fieldErr("retention", "no retention rule is set")
	}
	reqs, err := s.retentionRequests(ctx, st, "")
	if err != nil {
		return nil, err
	}
	var out []domain.Job
	for i, req := range reqs {
		req.Principal, req.PolicyID = principal, st.ID
		if idempotencyKey != "" {
			req.IdempotencyKey = fmt.Sprintf("%s#%d", idempotencyKey, i)
		}
		j, _, err := s.opts.Jobs.Enqueue(ctx, req)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			s.log.Warn("could not queue a retention job", "error", err)
			continue
		}
		out = append(out, j)
	}
	audit.SetDetail(ctx, "jobs", len(out))
	return out, nil
}

// retentionRequests builds one retention job per location that holds
// Docker Manager backups (setID limits it to the set's locations). Each
// environment location also names the deleted items whose backups expire,
// judged from every backup there.
func (s *Service) retentionRequests(ctx context.Context, st domain.BackupSetup, setID string) ([]jobs.Request, error) {
	snaps, err := s.retainedSnapshots(ctx, setID)
	if err != nil {
		return nil, err
	}
	all := snaps
	if setID != "" && st.Retention.ExpireDeletedDays > 0 {
		if all, err = s.retainedSnapshots(ctx, ""); err != nil {
			return nil, err
		}
	}
	key, _, err := store.GetBackupKey(ctx, s.db)
	if err != nil {
		return nil, err
	}
	seen := map[[2]string]bool{}
	var reqs []jobs.Request
	rules := retentionRules(st.Retention)
	for _, sn := range snaps {
		k := [2]string{sn.RepositoryID, sn.Scope}
		if seen[k] {
			continue
		}
		seen[k] = true
		repo, err := store.GetBackupRepository(ctx, s.db, sn.RepositoryID)
		if err != nil {
			continue // the repository was removed from Docker Manager
		}
		if sn.Scope == backup.ScopeManager {
			reqs = append(reqs, jobs.Request{Kind: jobspec.ManagerRetention, Targets: []domain.JobTarget{repoTarget(repo.ID)},
				Input: managerRetentionInput{RepositoryID: repo.ID, PolicyID: st.ID, Rules: rules, TimeZone: st.TimeZone, AnyPolicy: true}})
			continue
		}
		env, _ := backup.ScopeEnvironment(sn.Scope)
		in := protocol.BackupRetentionInput{Repository: repositoryRef(repo, sn.Scope, key.State), PolicyID: st.ID, Rules: rules,
			TimeZone: st.TimeZone}
		// Agents reject unknown input fields: the expiry and the backups of
		// earlier policies are sent only to agents announcing them (an older
		// one applies the rules to the setup's own backups).
		fh, _ := s.opts.Agents.(FeatureHub)
		if fh != nil && fh.EnvironmentHasFeature(env, protocol.FeatureBackupAnyPolicy) {
			in.AnyPolicy = true
		}
		if fh != nil && fh.EnvironmentHasFeature(env, protocol.FeatureBackupExpire) {
			var here []domain.BackupSnapshot
			for _, x := range all {
				if x.RepositoryID == sn.RepositoryID && x.Scope == sn.Scope {
					here = append(here, x)
				}
			}
			in.Expire = s.expiredItems(ctx, st, sn.Scope, here)
		}
		reqs = append(reqs, jobs.Request{Kind: jobspec.BackupRetention, EnvironmentID: env, Targets: []domain.JobTarget{repoTarget(repo.ID)},
			Input: in})
	}
	return reqs, nil
}

// RunFollowUps queues the automatic retention of finished sets of the
// setup when it applies retention after backups.
func (s *Service) RunFollowUps(ctx context.Context) error {
	sets, err := store.ListBackupSetsWithFollowUp(ctx, s.db, "retention", 50)
	if err != nil {
		return err
	}
	st, err := store.GetBackupSetup(ctx, s.db)
	if err != nil {
		return err
	}
	for _, set := range sets {
		if set.PolicyID == st.ID && st.Retention.AfterBackup && retentionActive(st.Retention) {
			reqs, err := s.retentionRequests(ctx, st, set.ID)
			if err != nil {
				return err
			}
			for _, req := range reqs {
				req.Principal, req.PolicyID = authz.Service(), st.ID
				req.IdempotencyKey = "retention:" + set.ID + ":" + repositoryOf(domain.Job{Targets: req.Targets}) + ":" + req.EnvironmentID
				if _, _, err := s.opts.Jobs.Enqueue(ctx, req); err != nil {
					s.log.Warn("could not queue retention after a backup", "set_id", set.ID, "error", err)
				}
			}
		}
		set.FollowUp = "done"
		if err := store.UpdateBackupSet(ctx, s.db, &set); err != nil {
			return err
		}
	}
	return nil
}

// --- scheduler sources (#13) ---

// RejectNoPrimary rejects a scheduled run of a setup without a Primary
// repository.
const RejectNoPrimary = "no_primary_repository"

type policySource struct{ s *Service }

func (ps policySource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	st, err := store.GetBackupSetup(ctx, ps.s.db)
	if err != nil {
		return nil, err
	}
	return []scheduler.PolicySchedule{{PolicyID: st.ID, Name: SetupName, Cron: st.Cron, TimeZone: st.TimeZone, Enabled: st.Enabled}}, nil
}

// Validate revalidates the setup when due and at dispatch: enabled, a
// Primary repository, repositories confirmed. The user who configured it
// plays no part: scheduled runs belong to the instance.
func (ps policySource) Validate(ctx context.Context, policyID string) error {
	_, err := ps.setup(ctx, policyID)
	return err
}

func (ps policySource) setup(ctx context.Context, policyID string) (domain.BackupSetup, error) {
	st, err := store.GetBackupSetup(ctx, ps.s.db)
	if err != nil {
		return st, err
	}
	if st.ID != policyID {
		return st, scheduler.Reject(scheduler.RejectPolicyNotFound, "the backup schedule no longer exists")
	}
	if !st.Enabled {
		return st, scheduler.Reject(scheduler.RejectPolicyDisabled, "backups are turned off")
	}
	if err := ps.s.checkReady(ctx, st); err != nil {
		switch {
		case errors.Is(err, domain.ErrBackupNoPrimary):
			return st, scheduler.Reject(RejectNoPrimary, "no Primary backup repository is set")
		case errors.Is(err, domain.ErrBackupRepositoryNotFound):
			return st, scheduler.Reject(scheduler.RejectTargetNotFound, "a backup repository was deleted")
		case errors.Is(err, domain.ErrRecoveryKeyNotConfirmed):
			return st, scheduler.Reject("recovery_key_not_confirmed", "the Recovery Key of a backup repository is not confirmed")
		}
		return st, err
	}
	return st, nil
}

func (ps policySource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	st, err := ps.setup(ctx, due.PolicyID)
	if err != nil {
		return nil, err
	}
	set, reqs, err := ps.s.planRun(ctx, ps.s.db, st, scheduledSetID(due.Key), due.ScheduledFor.UTC(), nil, true)
	if err != nil {
		var fe *domain.FieldError
		if errors.As(err, &fe) {
			return nil, scheduler.Reject(domain.ErrorRejected, fe.Message)
		}
		return nil, err
	}
	set.Origin = domain.OriginScheduled
	if _, err := store.InsertBackupSet(ctx, ps.s.db, &set); err != nil {
		return nil, err
	}
	return reqs, nil
}

type verifySource struct{ s *Service }

func (vs verifySource) Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error) {
	repos, err := store.ListBackupRepositories(ctx, vs.s.db, "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.PolicySchedule, 0, len(repos))
	for _, r := range repos {
		out = append(out, scheduler.PolicySchedule{PolicyID: r.ID, Name: r.Name, Cron: r.VerifyCron, TimeZone: r.VerifyTimeZone,
			Enabled: r.VerifyEnabled && r.State == domain.BackupRepositoryReady})
	}
	return out, nil
}

func (vs verifySource) Validate(ctx context.Context, repositoryID string) error {
	r, err := store.GetBackupRepository(ctx, vs.s.db, repositoryID)
	if errors.Is(err, domain.ErrBackupRepositoryNotFound) {
		return scheduler.Reject(scheduler.RejectPolicyNotFound, "the backup repository was deleted")
	}
	if err != nil {
		return err
	}
	if !r.VerifyEnabled {
		return scheduler.Reject(scheduler.RejectPolicyDisabled, "scheduled verification is disabled")
	}
	if r.State != domain.BackupRepositoryReady {
		return scheduler.Reject("recovery_key_not_confirmed", "the repository's Recovery Key is not confirmed")
	}
	return nil
}

// Jobs verifies every known location of the repository.
func (vs verifySource) Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error) {
	r, err := store.GetBackupRepository(ctx, vs.s.db, due.PolicyID)
	if err != nil {
		return nil, err
	}
	locs, err := store.ListBackupLocations(ctx, vs.s.db, r.ID)
	if err != nil {
		return nil, err
	}
	var reqs []jobs.Request
	for _, l := range locs {
		if len(reqs) >= scheduler.MaxJobsPerRun {
			break
		}
		req, err := vs.s.verifyRequest(ctx, r.ID, l.Scope, r.VerifyReadData)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, req)
	}
	return reqs, nil
}
