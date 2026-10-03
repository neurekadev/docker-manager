// Package backups is the manager side of Docker Manager backups (#10, #24):
// repositories (destinations) and their physical locations, the instance
// Recovery Key, the one backup setup (#246), runs as backup sets, the
// snapshot index, the manager-state snapshot and portable manifests, the
// scheduler sources of backup and verification schedules, the
// manager-executed job kinds (manager.backup, manager.retention,
// manager.verify) and the credentials of the agent-executed ones
// (backup.run, backup.retention, backup.verify).
//
// Every repository, key, set and snapshot and the setup belong to the
// instance. Scheduled work runs as the manager service identity and
// survives the removal of the user who configured it; users appear only in
// audit history.
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
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/streammux"
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

// Volumes discovers standalone Docker volumes for environment policies.
type Volumes interface {
	ListVolumes(ctx context.Context, environmentID string) ([]protocol.VolumeInfo, error)
	ListContainers(ctx context.Context, environmentID string) ([]protocol.ContainerSummary, error)
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

// NextRuns returns the next scheduled runs of several policies of kind in
// one lookup (policies without one are absent), for policy lists.
func (s *Service) NextRuns(ctx context.Context, kind string, policyIDs []string) map[string]time.Time {
	if s.opts.Scheduler == nil {
		return map[string]time.Time{}
	}
	out, err := store.NextScheduledRuns(ctx, s.db, kind, policyIDs)
	if err != nil {
		return map[string]time.Time{}
	}
	return out
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
	Volumes      Volumes
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
	// CheckSchema reports whether this build can run a manager database
	// (a restored snapshot's): nil, or an error wrapping
	// store.ErrUnknownMigrations for a newer schema.
	CheckSchema func(ctx context.Context, db bun.IDB) error
	// RequestRestart asks the manager process for a controlled restart
	// (a staged manager-state restore is applied at startup); nil in tests
	// that restart by hand.
	RequestRestart func()
	// Build identifies this build (manifests).
	Build buildinfo.Info
	// Random overrides crypto/rand (tests).
	Random io.Reader
	// OnPrimaryMissing learns whether the setup is enabled without a
	// Primary repository (its runs are refused) after every change of the
	// setup or the repositories; nil in focused tests.
	OnPrimaryMissing func(ctx context.Context, missing bool)
}

// Service is the backup service.
type Service struct {
	opts    Options
	db      *bun.DB
	log     *slog.Logger
	volumes Volumes

	wake chan struct{}

	// imports holds the secrets of running fresh-manager imports (#24),
	// in memory only, by job ID.
	importMu sync.Mutex
	imports  map[string]*importSecrets

	// activity holds the latest live report of running backups (#10).
	activity activityStore
}

// SetVolumes installs the resource inventory after the manager wires Docker.
func (s *Service) SetVolumes(v Volumes) { s.volumes = v }

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
	s := &Service{opts: opts, db: opts.DB, log: opts.Logger, volumes: opts.Volumes, wake: make(chan struct{}, 1), imports: map[string]*importSecrets{}}
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
		_ = os.RemoveAll(filepath.Join(opts.DataDir, importDirName))
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
	return backup.Destination{Kind: backup.KindS3, Endpoint: r.Endpoint, Bucket: r.Bucket, Prefix: r.Prefix,
		Region: r.Region, PathStyle: r.PathStyle, Compression: DestinationCompression(r.Compression)}
}

// DestinationCompression is a repository's compression mode as a
// destination carries it: "" for auto (never written out), else the mode.
func DestinationCompression(mode string) string {
	if mode == domain.BackupCompressionAuto {
		return ""
	}
	return mode
}

// validCompression reports whether mode is a repository compression mode.
func validCompression(mode string) bool {
	switch mode {
	case domain.BackupCompressionAuto, domain.BackupCompressionMax, domain.BackupCompressionOff:
		return true
	}
	return false
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
