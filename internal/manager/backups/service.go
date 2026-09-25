// Package backups is the manager side of DockYard backups (#10, #24):
// repositories (destinations) and their physical locations, the instance
// Recovery Key, policies, runs as backup sets, the snapshot index, the
// manager-state snapshot and portable manifests, the scheduler sources of
// backup and verification schedules, the manager-executed job kinds
// (manager.backup, manager.retention, manager.verify) and the credentials
// of the agent-executed ones (backup.run, backup.retention, backup.verify).
//
// Every repository, key, policy, set and snapshot belongs to the instance.
// Scheduled work runs as the manager service identity and survives the
// removal of the user who configured it; users appear only in audit
// history.
package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/restic"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// AgentHub reaches agents (implemented by *agents.Hub).
type AgentHub interface {
	RequestEnvironment(ctx context.Context, environmentID, name string, input any, timeout time.Duration) (json.RawMessage, error)
	OpenStream(ctx context.Context, environmentID, kind string, input any, o streammux.OpenOptions) (*streammux.Stream, error)
}

// Environments looks up environments (implemented by *agents.Service).
type Environments interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
}

// Stacks looks up stacks (implemented by *stacks.Service).
type Stacks interface {
	Get(ctx context.Context, id string) (domain.Stack, error)
}

// OwnerGuard enforces owner-only Recovery Key administration
// (implemented by *auth.Service).
type OwnerGuard interface {
	RequireOwner(ctx context.Context, recent bool) (userID string, err error)
}

// Scheduler is the shared cron scheduler (#13).
type Scheduler interface {
	Register(kind string, src scheduler.PolicySource) error
	Notify()
	Default(ctx context.Context, kind string) (cronExpr, tz string, err error)
	Status(ctx context.Context, kind, policyID string, runs int) (domain.Schedule, []domain.ScheduleRun, bool, error)
}

// NextRun returns a policy's (or repository verification's) next
// scheduled run (nil when none).
func (s *Service) NextRun(ctx context.Context, kind, policyID string) *time.Time {
	if s.opts.Scheduler == nil {
		return nil
	}
	sc, _, ok, err := s.opts.Scheduler.Status(ctx, kind, policyID, 0)
	if err != nil || !ok || sc.NextRunAt == nil {
		return nil
	}
	t := *sc.NextRunAt
	return &t
}

// Options configures the service.
type Options struct {
	DB      *bun.DB
	Keyring *secrets.Keyring
	Clock   clock.Clock
	Logger  *slog.Logger
	Jobs    *jobs.Engine
	// Scheduler registers the backup and verification schedules; nil in
	// focused tests.
	Scheduler    Scheduler
	Agents       AgentHub
	Environments Environments
	Stacks       Stacks
	Guard        OwnerGuard
	Audit        *audit.Log
	InstanceID   string
	// Restic runs restic on the manager (manager scope, S3 probes).
	Restic restic.Opener
	// DataDir holds the staging directory of manager-state snapshots.
	DataDir string
	// DatabasePath and MetricsPath locate the databases (previews).
	DatabasePath string
	MetricsPath  string
	// MetricsSnapshot writes a consistent copy of the metrics database to
	// dst (VACUUM INTO) for policies that include it; nil excludes it.
	MetricsSnapshot func(ctx context.Context, dst string) error
	// SecretKeyFile is the manager's secret-protection key file (restores).
	SecretKeyFile string
	// LocalRoots are the directories local manager repositories may live
	// in (DOCKYARD_BACKUP_LOCAL_ROOTS).
	LocalRoots []string
	// Migrations lists the applied migrations of the manager database
	// (the schema recorded in manager-state snapshots).
	Migrations func(ctx context.Context) ([]string, error)
	// KnownMigrations lists the migrations this binary knows (restore
	// compatibility checks).
	KnownMigrations func() []string
	// HTTPClient is used by S3 connection probes (tests trust fakes).
	HTTPClient *http.Client
	// ForgetResource drops authorization state of deleted resources.
	ForgetResource func(ctx context.Context, ref authz.ResourceRef) (int, error)
	// Build identifies this build (manifests).
	Build buildinfo.Info
	// Random overrides crypto/rand (tests).
	Random io.Reader
}

// Service is the backup service.
type Service struct {
	opts Options
	db   *bun.DB
	log  *slog.Logger

	wake chan struct{}
}

// New returns the service and registers its job executors and finish
// hooks on the engine (call before the engine's Recover).
func New(opts Options) (*Service, error) {
	if opts.DB == nil || opts.Keyring == nil || opts.Jobs == nil {
		return nil, errors.New("backups: DB, Keyring and Jobs are required")
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Restic == nil {
		opts.Restic = &restic.Runner{Logger: opts.Logger}
	}
	if opts.Build.Version == "" {
		opts.Build = buildinfo.Get()
	}
	s := &Service{opts: opts, db: opts.DB, log: opts.Logger, wake: make(chan struct{}, 1)}
	for _, x := range s.executors() {
		if err := opts.Jobs.RegisterManagerExecutor(x); err != nil {
			return nil, err
		}
	}
	s.registerHooks()
	if opts.Scheduler != nil {
		if err := opts.Scheduler.Register(scheduler.KindBackup, policySource{s}); err != nil {
			return nil, err
		}
		if err := opts.Scheduler.Register(scheduler.KindBackupVerification, verifySource{s}); err != nil {
			return nil, err
		}
	}
	if opts.DataDir != "" {
		// Staging left behind by a crash holds a database copy: remove it.
		_ = os.RemoveAll(filepath.Join(opts.DataDir, stagingDirName))
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.opts.Clock.Now().UTC().Truncate(time.Microsecond) }

func (s *Service) tx(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return s.db.RunInTx(ctx, nil, fn)
}

func (s *Service) notify() {
	if s.opts.Scheduler != nil {
		s.opts.Scheduler.Notify()
	}
}

func (s *Service) owner(ctx context.Context, recent bool) error {
	if s.opts.Guard == nil {
		return domain.ErrForbidden
	}
	_, err := s.opts.Guard.RequireOwner(ctx, recent)
	return err
}

func fieldErr(field, format string, args ...any) error {
	return &domain.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Run applies follow-up work (retention after successful backups) until
// ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := s.opts.Clock.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.RunFollowUps(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("backup follow-ups failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		case <-s.wake:
		}
	}
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Wake runs pending follow-ups soon.
func (s *Service) Wake() { s.poke() }

// destination converts a repository to its backup.Destination.
func destination(r domain.BackupRepository) backup.Destination {
	return backup.Destination{Kind: r.Kind, Path: r.Path, Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix,
		Region: r.Region, PathStyle: r.PathStyle}
}

// Serves reports whether repository r can hold scope (a local repository
// serves only its own executor).
func Serves(r domain.BackupRepository, scope string) bool {
	if r.Kind == backup.KindS3 {
		return true
	}
	if scope == backup.ScopeManager {
		return r.Executor == domain.BackupExecutorManager
	}
	env, ok := backup.ScopeEnvironment(scope)
	return ok && r.Executor == env
}

// repoTarget is the job target of a repository.
func repoTarget(id string) domain.JobTarget {
	return domain.JobTarget{Type: domain.TargetRepository, ID: id}
}

func repositoryOf(j domain.Job) string {
	for _, t := range j.Targets {
		if t.Type == domain.TargetRepository {
			return t.ID
		}
	}
	return ""
}

// backupKinds are the kinds whose agent commands carry repository
// credentials.
var backupKinds = []domain.JobKind{jobspec.BackupRun, jobspec.RestoreRun, jobspec.BackupRetention, jobspec.BackupVerify}

func isBackupKind(k domain.JobKind) bool { return slices.Contains(backupKinds, k) }

func trimName(s string) string { return strings.TrimSpace(s) }
