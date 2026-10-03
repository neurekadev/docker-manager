package scheduler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
)

// Schedule kinds. A kind is one kind of scheduled policy work with its own
// editable default expression; a policy workstream registers the
// PolicySource of its kind (Register).
const (
	// KindBackup: backup policies (#10).
	KindBackup = "backup"
	// KindBackupVerification: restic repository verification (#10).
	KindBackupVerification = "backup_verification"
	// KindUpdateCheck: image update checks (#20).
	KindUpdateCheck = "update_check"
	// KindUpdateRun: image update runs (#20).
	KindUpdateRun = "update_run"
	// KindPrune: Docker prune policies (#14).
	KindPrune = "prune"
)

// Kind describes a schedule kind.
type Kind struct {
	// Key is the stable identifier (lower snake_case).
	Key string
	// Label is the plain-language name, in Title Case ("Image Update
	// Checks").
	Label string
	// Noun is the name as it reads inside a sentence, in sentence case
	// ("Image update checks schedules do not catch up missed runs"); the
	// run reasons use it. Empty means Label.
	Noun string
	// Suggested is Docker Manager's shipped default expression (editable per
	// instance in the schedule defaults, per policy in the policy).
	Suggested string
	// CatchUp says what happens to runs missed while the manager was not
	// running.
	CatchUp domain.ScheduleCatchUp
	// PolicyType is the #17 catalog resource type of the kind's policies
	// and ReadCapability the key that shows one; GET /schedules lists an
	// entry only to callers holding it on the policy.
	PolicyType     string
	ReadCapability string
	// JobKinds are the job kinds a run of the kind enqueues. A policy with
	// a non-terminal job of these kinds (scheduled or manual, linked by
	// the job's policy ID) does not start another scheduled run.
	JobKinds []domain.JobKind
	// MaxJobs bounds the jobs one run enqueues; 0 means MaxJobsPerRun.
	MaxJobs int
}

// maxJobs is the bound of the jobs one run of the kind enqueues.
func (k Kind) maxJobs() int {
	if k.MaxJobs > 0 {
		return k.MaxJobs
	}
	return MaxJobsPerRun
}

var kindKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// noun is the kind's name inside a sentence: Noun, else Label.
func (k Kind) noun() string {
	if k.Noun != "" {
		return k.Noun
	}
	return k.Label
}

func (k Kind) validate() error {
	if !kindKeyRE.MatchString(k.Key) {
		return fmt.Errorf("scheduler: kind key %q must be lower snake_case", k.Key)
	}
	if k.Label == "" {
		return fmt.Errorf("scheduler: kind %s needs a label", k.Key)
	}
	if err := ValidateSpec(k.Suggested, "UTC"); err != nil {
		return fmt.Errorf("scheduler: kind %s: suggested expression: %w", k.Key, err)
	}
	if k.CatchUp != domain.CatchUpOnce && k.CatchUp != domain.CatchUpSkip {
		return fmt.Errorf("scheduler: kind %s: catch-up policy %q", k.Key, k.CatchUp)
	}
	if k.PolicyType == "" || k.ReadCapability == "" {
		return fmt.Errorf("scheduler: kind %s needs a policy type and read capability", k.Key)
	}
	if len(k.JobKinds) == 0 {
		return fmt.Errorf("scheduler: kind %s needs its job kinds", k.Key)
	}
	for _, jk := range k.JobKinds {
		if _, ok := jobspec.Lookup(jk); !ok {
			return fmt.Errorf("scheduler: kind %s: unknown job kind %s", k.Key, jk)
		}
	}
	return nil
}

// BuiltinKinds returns the v1 kinds (#13) with their suggested defaults.
// Backups, verification and update checks catch up once after downtime;
// prune and update runs never start at an unexpected time.
func BuiltinKinds() []Kind {
	return []Kind{
		{Key: KindBackup, Label: "Backups", Suggested: "0 * * * *", CatchUp: domain.CatchUpOnce,
			PolicyType: catalog.TypeBackupPolicy, ReadCapability: "backup_policy.read",
			JobKinds: []domain.JobKind{jobspec.BackupRun, jobspec.ManagerBackup}},
		{Key: KindUpdateCheck, Label: "Image Update Checks", Noun: "Image update checks", Suggested: "0 3 * * *",
			CatchUp: domain.CatchUpOnce, PolicyType: catalog.TypeUpdatePolicy, ReadCapability: "update_policy.read",
			JobKinds: []domain.JobKind{jobspec.UpdateCheck}, MaxJobs: MaxUpdateJobsPerRun},
		{Key: KindUpdateRun, Label: "Image Update Runs", Noun: "Image update runs", Suggested: "0 4 * * *",
			CatchUp: domain.CatchUpSkip, PolicyType: catalog.TypeUpdatePolicy, ReadCapability: "update_policy.read",
			JobKinds: []domain.JobKind{jobspec.UpdateRun}, MaxJobs: MaxUpdateJobsPerRun},
		{Key: KindPrune, Label: "Docker Prune", Noun: "Docker prune", Suggested: "0 3 * * 0", CatchUp: domain.CatchUpSkip,
			PolicyType: catalog.TypeMaintenancePolicy, ReadCapability: "maintenance_policy.read",
			JobKinds: []domain.JobKind{jobspec.PruneRun}},
		{Key: KindBackupVerification, Label: "Repository Verification", Noun: "Repository verification", Suggested: "0 5 * * 0",
			CatchUp: domain.CatchUpOnce, PolicyType: catalog.TypeBackupRepository, ReadCapability: "backup_repository.read",
			JobKinds: []domain.JobKind{jobspec.BackupVerify, jobspec.ManagerVerify}},
	}
}

// PolicySchedule is one policy's schedule as its owner stores it.
type PolicySchedule struct {
	// PolicyID identifies the policy within its kind (at most 64 bytes);
	// it becomes the jobs' policy ID.
	PolicyID string
	// Name is the policy's display name.
	Name string
	// EnvironmentID is set for environment-scoped policies ("" for
	// instance-wide ones such as backup policies).
	EnvironmentID string
	// Cron and TimeZone are the policy's saved expression and IANA zone
	// (explicit: prefill new policies from Service.Default).
	Cron     string
	TimeZone string
	// Enabled: automatic/destructive policies start disabled (#13).
	Enabled bool
}

// Due is one run the scheduler is starting.
type Due struct {
	Kind     string
	PolicyID string
	// ScheduledFor is the instant the run was due (UTC).
	ScheduledFor time.Time
	// CatchUp is true for a late run replacing runs missed while the
	// manager was not running.
	CatchUp bool
	// Key is the run's idempotency key (<kind>:<policy>:<instant>). Use it
	// to make per-run bookkeeping idempotent: Jobs can be called again
	// for the same run after a manager crash.
	Key string
}

// PolicySource is what a policy workstream (#10 backups and
// verification, #14 prune, #20 update checks/runs) registers for its kind.
// Every method must be safe for concurrent use and must not call the
// scheduler back.
type PolicySource interface {
	// Schedules lists every policy of the kind that has a schedule,
	// enabled or not. The scheduler synchronizes its durable state from it
	// on every pass (at least once a minute, and right after Notify): new
	// policies, edits and enable/disable take effect then; a policy missing
	// from the list is removed with its run history.
	Schedules(ctx context.Context) ([]PolicySchedule, error)
	// Validate revalidates a policy when a run is due and again when its
	// jobs are dispatched (#13, #17): still present, enabled, targets
	// still exist and are in scope. Return Reject(...) to refuse the run
	// (recorded as rejected / the job fails with policy_rejected); any
	// other error is a failure of the source (recorded as failed at due
	// time, retried at dispatch).
	Validate(ctx context.Context, policyID string) error
	// Jobs builds the job requests of a due run (1 to the kind's MaxJobs, in a
	// deterministic order). The scheduler sets Principal (the manager
	// service identity), PolicyID and IdempotencyKey (<Due.Key>#<index>)
	// and enqueues them through the job engine. Return Reject(...) to
	// refuse the run, or no requests to skip it (nothing to do).
	Jobs(ctx context.Context, due Due) ([]jobs.Request, error)
}

// MaxJobsPerRun bounds the jobs one run enqueues (one per environment of
// a backup or prune run; manual runs keep the same bound).
const MaxJobsPerRun = 256

// MaxUpdateJobsPerRun bounds one run of the updates setup: a check or an
// update of every stack and container of the instance is one run (manual
// runs keep the same bound).
const MaxUpdateJobsPerRun = 4096

// Rejection refuses a due run or a dispatch with a user-facing reason.
type Rejection struct {
	// Class is a stable snake_case code (policy_disabled, policy_not_found,
	// target_not_found, ...).
	Class string
	// Reason is shown in the schedule history and the job error; never
	// include secrets.
	Reason string
}

func (r *Rejection) Error() string { return r.Reason }

// Reject returns a *Rejection.
func Reject(class, reason string) error { return &Rejection{Class: class, Reason: reason} }

// Common rejection classes.
const (
	RejectPolicyNotFound = "policy_not_found"
	RejectPolicyDisabled = "policy_disabled"
	RejectTargetNotFound = "target_not_found"
)

func asRejection(err error) (*Rejection, bool) {
	var r *Rejection
	ok := errors.As(err, &r)
	return r, ok
}
