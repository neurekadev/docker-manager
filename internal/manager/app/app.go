// Package app wires the manager together and owns its startup order:
//
//	config → data dir → open DB → (snapshot) → migrate → secret key /
//	instance → auth primitives → audit trail → identity → permissions → agents (all environments
//	offline) → job engine recovery → metrics database (own file and
//	migrations) → observation → Docker resources → HTTP handler → listener → serve (+ job
//	engine loop, scheduler, auth housekeeping, audit retention, metrics
//	collection, rollups and retention)
//
// Migrations always finish before any listener or background worker starts;
// a migration failure aborts startup with the database unchanged.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/db/migrations"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/agents"
	"github.com/neurekadev/docker-manager/internal/manager/api"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/auth"
	"github.com/neurekadev/docker-manager/internal/manager/auth/password"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/authz/catalog"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/manager/builds"
	"github.com/neurekadev/docker-manager/internal/manager/config"
	"github.com/neurekadev/docker-manager/internal/manager/containerio"
	"github.com/neurekadev/docker-manager/internal/manager/diagnostics"
	"github.com/neurekadev/docker-manager/internal/manager/events"
	"github.com/neurekadev/docker-manager/internal/manager/files"
	"github.com/neurekadev/docker-manager/internal/manager/gitcreds"
	"github.com/neurekadev/docker-manager/internal/manager/idempotency"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/live"
	"github.com/neurekadev/docker-manager/internal/manager/maintenance"
	"github.com/neurekadev/docker-manager/internal/manager/managermove"
	"github.com/neurekadev/docker-manager/internal/manager/metrics"
	envmigrations "github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/movelock"
	"github.com/neurekadev/docker-manager/internal/manager/observe"
	"github.com/neurekadev/docker-manager/internal/manager/permissions"
	"github.com/neurekadev/docker-manager/internal/manager/regclient"
	"github.com/neurekadev/docker-manager/internal/manager/registries"
	"github.com/neurekadev/docker-manager/internal/manager/removal"
	"github.com/neurekadev/docker-manager/internal/manager/resources"
	"github.com/neurekadev/docker-manager/internal/manager/scheduler"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/server"
	"github.com/neurekadev/docker-manager/internal/manager/settings"
	"github.com/neurekadev/docker-manager/internal/manager/stacks"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/manager/templates"
	"github.com/neurekadev/docker-manager/internal/manager/updates"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/selfid"
)

// ShutdownGrace bounds graceful HTTP shutdown.
const ShutdownGrace = 15 * time.Second

// reconcileRequestTimeout bounds the inventory request of the Docker
// resource reconciler after an agent (re)connects (it never fails the
// reconnect).
const reconcileRequestTimeout = 10 * time.Second

// Options configures the manager. Only Config, Logger and UI are required.
type Options struct {
	Config config.Config
	Logger *slog.Logger
	UI     fs.FS
	// UIBuilt reports whether UI is the real SvelteKit build (for logs).
	UIBuilt bool
	Clock   clock.Clock
	// Migrations defaults to migrations.Migrations; tests inject failing sets.
	Migrations *migrate.Migrations
	// Listen defaults to net.Listen; tests use it to observe binding.
	Listen func(network, address string) (net.Listener, error)
	// OnListening is called with the bound address before serving.
	OnListening func(net.Addr)
	// OnStarted is called with every started manager (Run starts a new one
	// after a controlled restart; tests).
	OnStarted func(*Manager)
	// PasswordParams overrides the Argon2id parameters (tests only; the
	// production set is password.Current).
	PasswordParams *password.Params
	// OnPasswordCompute is called for every Argon2id computation (tests only).
	OnPasswordCompute func()
	// Audit records security events (#30); nil logs them.
	Audit auth.Auditor
	// RegistryHTTPClient overrides the HTTP client of manager-side
	// registry checks (#19; tests trust a fake registry's certificate).
	RegistryHTTPClient *http.Client
	// GitHTTPClient overrides the HTTP client of Git credential connection
	// tests (#33; tests trust a fake Git server's certificate).
	GitHTTPClient *http.Client
	// TemplateHTTPClient overrides the HTTP client that reads other
	// instances' template registries (tests).
	TemplateHTTPClient *http.Client
	// ContainerID overrides the detection of the manager's own container
	// (selfid.Detect; #32 tells co-located agents which container is the
	// manager). Tests set it.
	ContainerID string
	// MigrationReconnectWait overrides how long a migration's transfer
	// waits for a disconnected agent (#35; tests, 0 = the default).
	MigrationReconnectWait time.Duration
	// AgentSession tunes the agent session protocol (#3; zero values use the
	// protocol constants). Tests on a fake clock set a heartbeat timeout
	// longer than any clock jump they make: a jump delivers none of the
	// heartbeats real time would have carried, so the default watchdog
	// would close idle agent sessions.
	AgentSession agents.SessionOptions
	// Restic overrides the manager's restic runner (#10; tests use
	// restictest).
	Restic restic.Opener
	// BackupHTTPClient overrides the HTTP client of S3 connection tests.
	BackupHTTPClient *http.Client
	// MoveHTTPClient overrides the HTTP client with which a new manager in
	// waiting mode reaches the old manager of a move (tests).
	MoveHTTPClient *http.Client
	// LogRing keeps the recent log lines for the support bundle (#34);
	// Run and Start create it (and tee Logger into it) when nil.
	LogRing *logging.Ring
}

// Manager is a started (migrated, not yet serving) manager.
type Manager struct {
	opts     Options
	db       *bun.DB
	instance domain.Instance
	keyring  *secrets.Keyring
	auth     *auth.Kit
	identity *auth.Service
	jobs     *jobs.Engine
	idem     *idempotency.Store
	audit    *audit.Log
	events   *events.Bus
	agents   *agents.Service
	perms    *permissions.Service
	metrics  *metrics.Store
	observe  *observe.Service
	regs     *registries.Service
	// resources is the Docker resource service (#6).
	resources *resources.Service
	files     *files.Service
	templates *templates.Service
	// Live synchronization (#23): the live stream hub, its job source and
	// the file watch set of every agent.
	live      *live.Hub
	liveJobs  *live.JobSource
	fileWatch *files.Watcher
	stacks    *stacks.Service
	io        *containerio.Service
	handler   http.Handler
	git       *gitcreds.Service
	builds    *builds.Service
	sched     *scheduler.Service
	maint     *maintenance.Service
	// migrations moves stacks and volumes between environments (#35).
	migrations *envmigrations.Service
	updates    *updates.Service
	backups    *backups.Service
	// diag serves the internal metrics and the support bundle (#34).
	diag *diagnostics.Service
	// moveLock and moves move the manager to a new server
	// (docs/internal/architecture/manager-move.md).
	moveLock *movelock.Lock
	moves    *managermove.Service
	// restart is signaled when the manager must restart in process (a
	// staged manager-state restore, #24); Serve returns ErrRestart.
	restart chan struct{}
}

// ErrRestart is returned by Serve when a controlled restart was requested
// (Run starts the manager again).
var ErrRestart = errors.New("manager restart requested")

// RequestRestart asks Serve to stop gracefully and return ErrRestart.
func (m *Manager) RequestRestart() {
	select {
	case m.restart <- struct{}{}:
	default:
	}
}

// RestartRequested reports (and consumes) a pending restart request
// (tests that restart by hand).
func (m *Manager) RestartRequested() bool {
	select {
	case <-m.restart:
		return true
	default:
		return false
	}
}

// ErrSecretKeyMissing means the database belongs to an existing installation
// but its secret-protection key file is absent.
var ErrSecretKeyMissing = errors.New("secret-protection key file is missing for an existing installation; " +
	"restore it (DOCKER_MANAGER_SECRET_KEY_FILE) instead of letting Docker Manager generate a new one, or encrypted settings become unreadable")

// Start opens and migrates the database and prepares the HTTP handler.
func Start(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Migrations == nil {
		opts.Migrations = migrations.Migrations
	}
	opts = withLogRing(opts)
	cfg, log := opts.Config, opts.Logger

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// A manager-state restore staged by a backup import (#24) or a move to
	// this server (kind move) replaces the database and the secret key
	// before anything opens them.
	applied, err := backups.ApplyPendingRestore(cfg.DataDir, cfg.DatabasePath(), cfg.SecretKeyFile, opts.Clock.Now())
	if err != nil {
		return nil, fmt.Errorf("apply the staged manager restore: %w", err)
	}
	if applied != nil {
		log.Warn("applied a staged manager-state restore; the previous database and key are kept",
			"kind", applied.Kind, "set_id", applied.SetID, "move_id", applied.MoveID, "kept_in", applied.PreRestoreDir)
	}
	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return nil, err
	}
	m := &Manager{opts: opts, db: db, restart: make(chan struct{}, 1)}
	ok := false
	defer func() {
		if !ok {
			_ = db.Close()
		}
	}()

	if _, err := store.Migrate(ctx, db, store.MigrateOptions{
		Migrations:  opts.Migrations,
		SnapshotDir: cfg.SnapshotDir(),
		Clock:       opts.Clock,
		Logger:      log,
	}); err != nil {
		return nil, err
	}

	if err := m.initInstance(ctx); err != nil {
		return nil, err
	}
	// A manager moving (or moved) to a new server stays locked across
	// restarts: the level is read before anything runs.
	m.moveLock = movelock.New()
	if lvl, err := managermove.LockLevel(ctx, db, opts.Clock.Now()); err != nil {
		return nil, fmt.Errorf("read the manager move state: %w", err)
	} else if lvl != movelock.Open {
		m.moveLock.Set(lvl)
		log.Warn("this manager is moving (or moved) to a new server: it is read-only", "lock", lvl.String())
	}
	// A new, empty manager with DOCKER_MANAGER_MOVE_FROM and
	// DOCKER_MANAGER_MOVE_CODE waits for the move's handoff.
	waiting, err := m.decideWaiting(ctx)
	if err != nil {
		return nil, fmt.Errorf("decide the move's waiting mode: %w", err)
	}

	// Auth primitives (#18): a public URL that cannot be a WebAuthn relying
	// party fails startup here, before anything listens.
	if m.auth, err = auth.NewKit(auth.KitOptions{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "auth"), PublicURL: cfg.PublicURL,
		IdleTimeout: cfg.Sessions.IdleTimeout, Lifetime: cfg.Sessions.Lifetime,
		StayIdleTimeout: cfg.Sessions.StayIdleTimeout, StayLifetime: cfg.Sessions.StayLifetime, PasswordParams: opts.PasswordParams,
		OnPasswordCompute: opts.OnPasswordCompute,
		SessionError: func(w http.ResponseWriter, r *http.Request, err error) {
			api.WriteError(w, r, api.Internal(err))
		},
	}); err != nil {
		return nil, err
	}

	// The audit trail (#30) records every mutating API operation and every
	// job's lifecycle; its retention purge runs with the job engine.
	auditOpts := audit.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "audit"),
		Retention: cfg.Audit.Retention(), MaxBytes: cfg.Audit.MaxBytes,
	}
	if cfg.Audit.LogMirror {
		auditOpts.Mirror = log.With("component", "audit_mirror")
	}
	if m.audit, err = audit.New(auditOpts); err != nil {
		return nil, err
	}

	identityAuditor := opts.Audit
	if identityAuditor == nil {
		identityAuditor = auth.TrailAuditor{Recorder: m.audit, Logger: log.With("component", "identity")}
	}

	m.idem, err = idempotency.New(idempotency.Options{DB: db, Keyring: m.keyring, Clock: opts.Clock})
	if err != nil {
		return nil, err
	}

	// Identity (#16): sessions and principals.
	m.identity, err = auth.NewService(ctx, auth.ServiceOptions{
		DB: db, Kit: m.auth, Keyring: m.keyring, Logger: log.With("component", "identity"),
		PublicURL: cfg.PublicURL, LocalDevelopment: cfg.LocalDevelopment,
		IdleTimeout: cfg.Sessions.IdleTimeout, Lifetime: cfg.Sessions.Lifetime,
		StayIdleTimeout: cfg.Sessions.StayIdleTimeout, StayLifetime: cfg.Sessions.StayLifetime,
		Audit: identityAuditor, Idempotency: m.idem,
	})
	if err != nil {
		return nil, err
	}
	// Authorization (#17): the permission service is the Authorizer of
	// every route and of the job engine (request and dispatch checks); the
	// identity service guards its owner-only flows and ends the open
	// requests and streams of users whose access changed.
	m.perms, err = permissions.New(permissions.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "permissions"),
		Guard: m.identity, Invalidator: m.identity,
	})
	if err != nil {
		return nil, err
	}
	authorizer := m.perms
	// API tokens (#31): the permission service evaluates token scope ∩
	// the user's current permissions (also at job dispatch) and validates
	// the scope of new tokens against the creator's permissions.
	m.perms.SetTokenScopes(m.identity.APITokenScopes)
	m.identity.SetScopeValidator(m.perms)

	m.events = events.New(opts.Clock)
	m.agents, err = agents.New(agents.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "agents"), Keyring: m.keyring, Bus: m.events,
		ManagerVersion: buildinfo.Get().Version, PublicURL: cfg.PublicURL, Audit: m.audit, Session: opts.AgentSession,
		Generation: m.instance.Generation, MoveLock: m.moveLock,
	})
	if err != nil {
		return nil, err
	}
	// Archiving an environment removes the permission rules scoped to it,
	// audited, in the same transaction (#34).
	m.agents.SetArchiveHook(m.archiveEnvironment)
	// The resource graph for authorization (#17): agents live in their
	// environment. Feature workstreams register their resource types here.
	m.perms.RegisterLocator(catalog.TypeAgent, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		a, err := m.agents.GetAgent(ctx, ref.ID)
		if errors.Is(err, domain.ErrAgentNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, EnvironmentID: a.EnvironmentID, Parents: []authz.ResourceRef{}}, nil
	}))
	// No session survives a restart: environments come back online only
	// after their agent reconnected and its jobs were reconciled.
	if err := m.agents.ResetOnline(ctx); err != nil {
		return nil, fmt.Errorf("reset environment connection state: %w", err)
	}
	// Registry connections (#19): owner-administered credentials, resolved
	// into each agent command at dispatch (never stored with a job).
	regLog := log.With("component", "registries")
	m.regs, err = registries.New(registries.Options{
		DB: db, Keyring: m.keyring, Clock: opts.Clock, Logger: regLog,
		Guard: m.identity, Audit: m.audit, ForgetResource: m.perms.ForgetResource,
		Client: regclient.New(regclient.Options{HTTP: opts.RegistryHTTPClient, Clock: opts.Clock, Logger: regLog}),
	})
	if err != nil {
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeRegistry, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		_, err := m.regs.Get(ctx, ref.ID)
		if errors.Is(err, domain.ErrRegistryConnectionNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{}}, nil
	}))
	// Git credentials (#33), handled like registry connections.
	m.git, err = gitcreds.New(gitcreds.Options{
		DB: db, Keyring: m.keyring, Clock: opts.Clock, Logger: log.With("component", "gitcreds"),
		Guard: m.identity, Audit: m.audit, HTTP: opts.GitHTTPClient, ForgetResource: m.perms.ForgetResource,
	})
	if err != nil {
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeGitCredential, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		_, err := m.git.Get(ctx, ref.ID)
		if errors.Is(err, domain.ErrGitCredentialNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{}}, nil
	}))
	m.jobs, err = jobs.New(jobs.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "jobs"),
		Dispatcher: m.agents.Hub(), Authorizer: authorizer, Audit: m.audit, CommandSecrets: m.commandSecrets,
		CommandInput: m.commandInput,
		MoveLock:     m.moveLock,
		Limits: jobs.Limits{
			ConcurrencyCaps: map[string]int{jobspec.ClassPull: cfg.Jobs.MaxConcurrentPulls, jobspec.ClassBuild: cfg.Jobs.MaxConcurrentBuilds},
			HistoryMaxAge:   cfg.Jobs.HistoryRetention,
			HistoryMaxJobs:  cfg.Jobs.HistoryMax,
			MaxEventsPerJob: cfg.Jobs.EventsMax,
		},
	})
	if err != nil {
		return nil, err
	}
	// Compose stacks (#7): finish hooks on the stack.* jobs (registered
	// before recovery and the engine loop), the stack Locator of the
	// resource graph and the reconciliation after agent reconnects.
	m.stacks, err = stacks.New(stacks.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "stacks"), Keyring: m.keyring,
		Agents: m.agents.Hub(), Environments: m.agents, Jobs: m.jobs, Bus: m.events, Registries: m.regs, Systems: m.agents,
	})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeStack, m.stacks.Locator())
	m.agents.Hub().AddReconciler(m.stacks.Reconciler())
	// The shared cron scheduler (#13): its finish hooks and the dispatch
	// revalidation of scheduled jobs are installed on the engine before
	// recovery; policy workstreams (#10, #14, #20) Register their sources.
	if m.sched, err = scheduler.New(scheduler.Options{DB: db, Clock: opts.Clock, Logger: log.With("component", "scheduler"),
		Jobs: m.jobs, Audit: m.audit, MoveLock: m.moveLock}); err != nil {
		m.jobs.Close()
		return nil, err
	}
	// Docker maintenance (#14): prune policies, their finish hook (before
	// recovery), the prune PolicySource of the scheduler and the policy
	// Locator. Saved container specifications (#6) are wired below.
	if m.maint, err = maintenance.New(maintenance.Options{DB: db, Clock: opts.Clock, Logger: log.With("component", "maintenance"),
		Jobs: m.jobs, Agents: m.agents.Hub(), Environments: m.agents, Scheduler: m.sched, Stacks: m.stacks,
		ForgetResource: m.perms.ForgetResource}); err != nil {
		m.jobs.Close()
		return nil, err
	}
	// Digest-driven updates (#20): the update.check executor and the
	// update.run/stack.deploy finish hooks (before recovery), the check and
	// run schedule sources and the policy Locator.
	if m.updates, err = updates.New(updates.Options{DB: db, Clock: opts.Clock, Logger: log.With("component", "updates"),
		Jobs: m.jobs, Stacks: m.stacks, Registries: m.regs, Agents: m.agents.Hub(), Environments: m.agents, Audit: m.audit,
		ForgetResource: m.perms.ForgetResource}); err != nil {
		m.jobs.Close()
		return nil, err
	}
	if err := m.sched.Register(scheduler.KindPrune, m.maint.PolicySource()); err != nil {
		m.jobs.Close()
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeMaintenancePolicy, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		p, err := m.maint.Get(ctx, ref.ID)
		if errors.Is(err, domain.ErrMaintenancePolicyNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, EnvironmentID: p.EnvironmentID, Parents: []authz.ResourceRef{}}, nil
	}))
	// Environment migration (#35): the stack.migrate/volume.migrate manager
	// executors (registered before recovery) relay data between agents;
	// their finish hooks keep the migration records.
	m.migrations, err = envmigrations.New(envmigrations.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "migrations"), Agents: m.agents.Hub(), Environments: m.agents,
		Jobs: m.jobs, Stacks: m.stacks, Registries: m.regs, Permissions: m.perms, Authorizer: authorizer, Audit: m.audit,
		BandwidthLimit: cfg.MigrationBandwidthLimit, ReconnectWait: opts.MigrationReconnectWait,
	})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}
	if err := m.updates.Register(m.sched); err != nil {
		m.jobs.Close()
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeUpdatePolicy, m.updates.Locator())
	// Backups (#10, #24): manager-side job kinds and finish hooks are
	// registered before recovery; the backup and verification schedules
	// are registered with the scheduler.
	if err := m.startBackups(ctx); err != nil {
		m.jobs.Close()
		return nil, err
	}
	// Moving the manager (manager.move runs here, registered before
	// recovery).
	if err := m.startMoves(waiting); err != nil {
		m.jobs.Close()
		return nil, err
	}
	// Policies that target a stack follow it when it migrates (#35): the
	// hooks run in the transaction that completes the migration. Update
	// policies move to the destination; backup policies select stacks by
	// ID (they follow by construction) and record whether a repository can
	// hold the destination's data. Maintenance policies target whole
	// environments, not stacks.
	m.migrations.OnStackMoved(m.updates.StackMoved)
	m.migrations.OnStackMoved(m.backups.StackMoved)
	if err := m.jobs.Recover(ctx); err != nil {
		m.jobs.Close()
		return nil, fmt.Errorf("recover jobs: %w", err)
	}
	m.agents.AttachJobs(m.jobs)
	// A manager-state restore (#24) applied above is finished before
	// anything is served: sessions, API tokens and agents are revoked,
	// the repository and index are brought up to date.
	if err := m.finishRestore(ctx); err != nil {
		m.jobs.Close()
		return nil, fmt.Errorf("finish the manager restore: %w", err)
	}
	// A move applied above is finished the other way: this manager is the
	// same instance, so sessions, API tokens and agents are kept.
	if err := m.finishMove(ctx); err != nil {
		m.jobs.Close()
		return nil, fmt.Errorf("finish the manager move: %w", err)
	}
	// The scoped file manager (#15): stack scopes resolve through the stack
	// service (#7), which records a revision when a definition file changes.
	m.files = files.New(files.Options{Agents: m.agents.Hub(), Jobs: m.jobs, Logger: log.With("component", "files"), Limits: cfg.Files})
	m.files.SetStacks(m.stacks, m.stacks)
	// Stack templates (template registry): drafts in the data directory,
	// served to the file manager; template.files.* jobs run here.
	m.templates, err = templates.New(ctx, templates.Options{
		DB: db, Keyring: m.keyring, Clock: opts.Clock, Logger: log.With("component", "templates"), DataDir: cfg.DataDir,
		MaxSize: cfg.TemplateMaxSize, MaxEdit: cfg.Files.Edit, Bus: m.events, Executors: m.jobs.RegisterManagerExecutor, ForgetResource: m.perms.ForgetResource,
		InstanceID: m.instance.ID, HTTPClient: opts.TemplateHTTPClient, SyncInterval: cfg.TemplateRegistrySync,
	})
	if err != nil {
		return nil, err
	}
	m.files.SetTemplates(m.templates)
	m.stacks.SetTemplates(templateSource{own: m.templates, instanceID: m.instance.ID})
	m.templates.StartSync()
	m.perms.RegisterLocator(catalog.TypeTemplate, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		err := m.templates.Exists(ctx, ref.ID)
		if errors.Is(err, domain.ErrTemplateNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, Parents: []authz.ResourceRef{}}, nil
	}))
	// Live synchronization (#23): every agent watches its stacks and the
	// open volume views (files.watch); external definition edits become
	// revisions; the live stream relays the bus (jobs through the engine's
	// change listener) to every open UI tab, filtered per user.
	m.fileWatch = files.NewWatcher(files.WatcherOptions{Agents: m.agents.Hub(), Stacks: m.stacks, Bus: m.events, Clock: opts.Clock,
		Logger: log.With("component", "files")})
	m.files.SetWatcher(m.fileWatch)
	m.live = live.New(live.Options{Bus: m.events, Clock: opts.Clock, Logger: log})
	m.liveJobs = live.NewJobSource(m.jobs.Get, m.events, opts.Clock, log)
	m.jobs.OnChange(m.liveJobs.Changed)

	// Image builds (#33): build records, definitions and the image.build
	// jobs they enqueue.
	m.builds, err = builds.New(builds.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "builds"), Jobs: m.jobs, Git: m.git, Registries: m.regs,
		ForgetResource: m.perms.ForgetResource,
	})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}
	m.perms.RegisterLocator(catalog.TypeBuildDefinition, permissions.LocatorFunc(func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		d, err := m.builds.GetDefinition(ctx, ref.ID)
		if errors.Is(err, domain.ErrBuildDefinitionNotFound) {
			return permissions.Location{}, nil
		}
		if err != nil {
			return permissions.Location{}, err
		}
		return permissions.Location{Found: true, EnvironmentID: d.EnvironmentID, Parents: []authz.ResourceRef{}}, nil
	}))
	// Container logs and exec terminals (#8).
	m.io = containerio.New(containerio.Options{Agents: m.agents.Hub(), Clock: opts.Clock, Logger: log.With("component", "containerio"),
		PublicURL: cfg.PublicURL, PingInterval: cfg.StreamHeartbeat})

	// Observation (#5): metrics live in their own database file so sample
	// writes never contend with jobs and auth; manager-state backups (#10)
	// leave it out by default.
	m.metrics, err = metrics.Open(ctx, metrics.Options{
		Path: cfg.MetricsPath(), Clock: opts.Clock, Logger: log.With("component", "metrics"),
		Retention: metrics.Retention{Raw: cfg.Metrics.RetentionRaw, Minute: cfg.Metrics.RetentionMinute, Quarter: cfg.Metrics.RetentionQuarter},
		MaxBytes:  cfg.Metrics.MaxBytes, MaxSeries: cfg.Metrics.MaxSeries,
	})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}
	hub := m.agents.Hub()
	m.observe = observe.New(observe.Options{Store: m.metrics, Agents: hub, Bus: m.events, Clock: opts.Clock,
		Logger: log.With("component", "observe"), Environments: func() []string {
			var ids []string
			for _, s := range hub.Sessions() {
				if s.Online {
					ids = append(ids, s.EnvironmentID)
				}
			}
			return ids
		},
		// Live CPU and memory (metrics.live) only while a browser watches.
		LiveDemand: func() bool { return m.live.Subscribers() > 0 }})
	if err := m.observe.Load(ctx); err != nil {
		_ = m.metrics.Close()
		m.jobs.Close()
		return nil, err
	}
	hub.AddReconciler(func(ctx context.Context, s *agents.Session) error {
		return m.observe.Reconcile(ctx, s.EnvironmentID())
	})

	// Docker resources (#6): containers, images, volumes and networks of
	// every environment through its agent; mutations are jobs, pulls use
	// the matching registry connection (#19). Their permission Locators
	// place Compose-stack members in their stack (#17) and a reconciler
	// refreshes that after every (re)connect.
	containerID := opts.ContainerID
	if containerID == "" {
		containerID = selfid.Detect()
	}
	log.Info("self-protection", "manager_container_id", containerID)
	m.resources, err = resources.New(resources.Options{
		DB: db, Keyring: m.keyring, Agents: hub, Jobs: m.jobs, Permissions: m.perms, Registries: m.regs, Stacks: m.stacks,
		InstanceID: m.instance.ID, ManagerContainerID: containerID, Clock: opts.Clock, Logger: log.With("component", "resources"),
	})
	if err != nil {
		_ = m.metrics.Close()
		m.jobs.Close()
		return nil, err
	}
	for _, typ := range []string{catalog.TypeContainer, catalog.TypeVolume, catalog.TypeNetwork} {
		m.perms.RegisterLocator(typ, m.resources.Locator(typ))
	}
	// Stack deploy/stop/restart/down/remove refuse Docker Manager's own Compose
	// project (#32).
	m.stacks.SetProtection(m.resources)
	// A stack rename moves the stack's volumes to new names: the saved
	// recreate specifications of standalone containers mounting them follow
	// (#6), in the transaction that finishes the rename.
	m.stacks.OnRenamed(m.resources.StackRenamed)
	// Prune runs protect what saved container specifications reference (#14).
	m.maint.SetSpecs(m.resources)
	// The stopped source of a migrated stack is no longer a Docker Manager stack:
	// until the user confirms its removal, prune runs keep its project
	// and volumes and the Docker resource routes refuse to remove them (#35).
	m.resources.SetRetainedProjects(m.migrations.RetainedProjects)
	// Removing a stack with its volumes keeps a retained source's volumes of
	// the same project.
	m.stacks.SetVolumeHolds(func(ctx context.Context, environmentID, project string) ([]string, error) {
		rs, err := m.migrations.RetainedSources(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		var keep []string
		for _, r := range rs {
			if r.Project == project {
				keep = append(keep, r.Volumes...)
			}
		}
		return keep, nil
	})
	m.maint.AddReferences(func(ctx context.Context, environmentID string) ([]maintenance.Reference, error) {
		rs, err := m.migrations.RetainedSources(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		var out []maintenance.Reference
		for _, r := range rs {
			out = append(out, maintenance.Reference{Kind: "project", Name: r.Project, Reason: r.Reason})
			for _, v := range r.Volumes {
				out = append(out, maintenance.Reference{Kind: "volume", Name: v,
					Reason: fmt.Sprintf("volume of the stopped source of migrated stack %q, kept until its removal is confirmed", r.Project)})
			}
		}
		return out, nil
	})
	// Container update policies read containers, saved recreate
	// specifications and protection through the resource service (#6, #32).
	m.updates.SetResources(m.resources)
	m.backups.SetVolumes(m.resources)
	hub.AddReconciler(func(ctx context.Context, s *agents.Session) error {
		// Self-protection (#32): the agent learns which manager it serves
		// and which container is that manager (co-located or not).
		if s.Serves(protocol.ReqManagerIdentity) {
			raw, err := s.Request(ctx, protocol.ReqManagerIdentity,
				protocol.ManagerIdentityInput{InstanceID: m.instance.ID, ContainerID: containerID, Generation: m.instance.Generation}, reconcileRequestTimeout)
			if err != nil {
				log.Warn("could not send the manager identity to the agent", "environment_id", s.EnvironmentID(), "error", err)
			} else {
				// A move of the manager moves the apps of the environment
				// next to it (docs/internal/architecture/manager-move.md).
				var out protocol.ManagerIdentityOutput
				if json.Unmarshal(raw, &out) == nil {
					m.moves.ObserveColocation(s.EnvironmentID(), out.Colocated)
				}
			}
		}
		if !s.Serves(protocol.ReqContainerList) {
			return nil // an agent without the #6 requests
		}
		m.resources.ReconcileSession(ctx, s.EnvironmentID(), func(ctx context.Context, name string, input any) ([]byte, error) {
			return s.Request(ctx, name, input, reconcileRequestTimeout)
		})
		return nil
	})

	// Diagnostics (#34): internal metrics and the support bundle.
	if m.diag, err = m.newDiagnostics(); err != nil {
		m.resources.Close()
		_ = m.metrics.Close()
		m.jobs.Close()
		return nil, err
	}

	srv, err := server.New(server.Options{
		Logger: log,
		Clock:  opts.Clock,
		UI:     opts.UI,
		API: api.Deps{
			Build:                    buildinfo.Get(),
			Readiness:                m.readiness,
			Jobs:                     m.jobs,
			Authorizer:               authorizer,
			Clock:                    opts.Clock,
			Idempotency:              m.idem,
			Audit:                    m.audit,
			SSEHeartbeat:             cfg.StreamHeartbeat,
			Identity:                 m.identity,
			Agents:                   m.agents,
			Permissions:              m.perms,
			Registries:               m.regs,
			APITokens:                m.identity,
			Observe:                  m.observe,
			Docker:                   m.resources,
			InstanceID:               m.instance.ID,
			Files:                    m.files,
			FileLimits:               cfg.Files,
			GitCredentials:           m.git,
			Builds:                   m.builds,
			Stacks:                   m.stacks,
			Events:                   m.events,
			ContainerIO:              m.io,
			Schedules:                m.sched,
			Maintenance:              m.maint,
			Migrations:               m.migrations,
			EnvironmentMigrations:    m.migrations,
			Updates:                  m.updates,
			Backups:                  m.backups,
			Templates:                m.templates,
			TemplateRegistries:       m.templates,
			TemplateRegistryDisabled: !cfg.TemplateRegistryEnabled,
			Removal:                  removal.New(db),
			Diagnostics:              m.diag,
			Live:                     m.live,
			FileWatch:                m.fileWatch,
			Settings:                 settings.New(db, opts.Clock),
			Deployment: api.DeploymentInfo{PublicURL: originString(cfg.PublicURL), LocalDevelopment: cfg.LocalDevelopment,
				TrustedProxies: len(cfg.TrustedProxies), MetricsEnabled: cfg.MetricsEnabled},
			ManagerMove: m.moves,
			MoveLock:    m.moveLock,
		},
		Agent:            m.agents.Handler(),
		TrustedProxies:   cfg.TrustedProxies,
		PublicURL:        cfg.PublicURL,
		LocalDevelopment: cfg.LocalDevelopment,
		StreamHeartbeat:  cfg.StreamHeartbeat,
		Auth:             m.identity.Middleware,
	})
	if err != nil {
		m.resources.Close()
		_ = m.metrics.Close()
		m.jobs.Close()
		return nil, err
	}
	m.handler = srv.Handler
	ok = true
	return m, nil
}

// commandSecrets resolves the credentials named by a job's input for its
// agent command (#19 registry connections, #33 Git credentials).
func (m *Manager) commandSecrets(ctx context.Context, j *domain.Job) (*protocol.CommandSecrets, error) {
	regs, err := m.regs.CommandSecrets(ctx, j)
	if err != nil {
		return nil, err
	}
	git, err := m.git.CommandSecrets(ctx, j)
	if err != nil {
		return nil, err
	}
	repos, err := m.backups.CommandSecrets(ctx, j)
	if err != nil {
		return nil, err
	}
	s := &protocol.CommandSecrets{Registries: regs, Git: git, Repositories: repos}
	if s.Empty() {
		return nil, nil
	}
	return s, nil
}

// commandInput adapts a job's input to the agent receiving it (#10: the
// backup repository's compression mode, for agents announcing it).
func (m *Manager) commandInput(ctx context.Context, j *domain.Job) json.RawMessage {
	return m.backups.CommandInput(ctx, j)
}

func (m *Manager) initInstance(ctx context.Context) error {
	cfg, log := m.opts.Config, m.opts.Logger
	inst, exists, err := store.GetInstance(ctx, m.db)
	if err != nil {
		return err
	}
	key, err := secrets.LoadKeyFile(cfg.SecretKeyFile)
	switch {
	case errors.Is(err, secrets.ErrKeyFileMissing) && exists:
		return ErrSecretKeyMissing
	case errors.Is(err, secrets.ErrKeyFileMissing):
		if key, err = secrets.CreateKeyFile(cfg.SecretKeyFile, nil); err != nil {
			return err
		}
		log.Info("generated secret-protection key; back it up together with the data volume",
			"path", cfg.SecretKeyFile, "key_id", key.ID())
	case err != nil:
		return err
	}
	m.keyring = secrets.NewKeyring(key)

	if !exists {
		if inst, err = store.CreateInstance(ctx, m.db, m.opts.Clock.Now()); err != nil {
			return err
		}
		log.Info("initialized new Docker Manager instance", "instance_id", inst.ID)
	}
	m.instance = inst
	return nil
}

func (m *Manager) readiness(ctx context.Context) []api.Check {
	checks := []api.Check{{Name: "database", OK: true}, {Name: "migrations", OK: true}}
	if err := m.db.PingContext(ctx); err != nil {
		checks[0] = api.Check{Name: "database", OK: false, Message: "database unreachable"}
		checks[1] = api.Check{Name: "migrations", OK: false, Message: "unknown"}
		return checks
	}
	if _, pending, err := store.Status(ctx, m.db, m.opts.Migrations); err != nil {
		checks[1] = api.Check{Name: "migrations", OK: false, Message: "cannot read migration status"}
	} else if len(pending) > 0 {
		checks[1] = api.Check{Name: "migrations", OK: false, Message: fmt.Sprintf("%d pending", len(pending))}
	}
	return checks
}

// Handler returns the HTTP handler.
func (m *Manager) Handler() http.Handler { return m.handler }

// Instance returns the installation record.
func (m *Manager) Instance() domain.Instance { return m.instance }

// Keyring returns the secret-protection keyring.
func (m *Manager) Keyring() *secrets.Keyring { return m.keyring }

// DB returns the database handle.
func (m *Manager) DB() *bun.DB { return m.db }

// Jobs returns the job engine.
func (m *Manager) Jobs() *jobs.Engine { return m.jobs }

// Auth returns the authentication primitives (#16, #18).
func (m *Manager) Auth() *auth.Kit { return m.auth }

// Audit returns the audit trail (Record for non-HTTP events, Verify for
// diagnostics, #34).
func (m *Manager) Audit() *audit.Log { return m.audit }

// Identity returns the identity service (#16).
func (m *Manager) Identity() *auth.Service { return m.identity }

// Agents returns the agent/environment service and session hub (#3).
func (m *Manager) Agents() *agents.Service { return m.agents }

// Permissions returns the authorization service (#17): feature workstreams
// register their resource Locators on it and call ForgetResource after
// deleting a resource.
func (m *Manager) Permissions() *permissions.Service { return m.perms }

// Metrics returns the metrics store (#5).
func (m *Manager) Metrics() *metrics.Store { return m.metrics }

// Observe returns the observation service (#5).
func (m *Manager) Observe() *observe.Service { return m.observe }

// Registries returns the registry connection service (#19): Select is
// the resolver pull/deploy/build/update handlers use, Check the
// manager-side digest check (#20).
func (m *Manager) Registries() *registries.Service { return m.regs }

// Live returns the live stream hub (#23).
func (m *Manager) Live() *live.Hub { return m.live }

// FileWatch returns the file watcher's manager side (#23): the agents'
// watch sets and the recording of external definition edits.
func (m *Manager) FileWatch() *files.Watcher { return m.fileWatch }

// Files returns the scoped file service (#15): #7 installs its stack root
// resolver and source observer with SetStacks.
func (m *Manager) Files() *files.Service { return m.files }

// GitCredentials returns the Git credential service (#33).
func (m *Manager) GitCredentials() *gitcreds.Service { return m.git }

// Builds returns the image build service (#33).
func (m *Manager) Builds() *builds.Service { return m.builds }

// Stacks returns the Compose stack service (#7): the file manager (#15)
// resolves stack roots with Root and reports definition saves with
// RecordFileSave; the watcher (#23) reports external edits with
// RecordObserved.
func (m *Manager) Stacks() *stacks.Service { return m.stacks }

// Scheduler returns the shared cron scheduler (#13): policy workstreams
// register their PolicySource with Register, prefill new policies from
// Default, validate with scheduler.ValidateSpec (api.ValidateSchedule) and
// call Notify after changing a policy.
func (m *Manager) Scheduler() *scheduler.Service { return m.sched }

// Maintenance returns the Docker maintenance service (#14): backups (#10)
// install SetBackupReferences so backup destinations are never pruned.
func (m *Manager) Maintenance() *maintenance.Service { return m.maint }

// Migrations returns the environment migration service (#35).
func (m *Manager) Migrations() *envmigrations.Service { return m.migrations }

// Updates returns the digest-driven update service (#20).
func (m *Manager) Updates() *updates.Service { return m.updates }

// Events returns the internal event bus.
func (m *Manager) Events() *events.Bus { return m.events }

// Resources returns the Docker resource service (#6).
func (m *Manager) Resources() *resources.Service { return m.resources }

// Backups returns the backup service (#10, #24).
func (m *Manager) Backups() *backups.Service { return m.backups }

// Idempotency returns the Idempotency-Key response store (its Forget is
// called when a principal's sessions, token or permissions change).
func (m *Manager) Idempotency() *idempotency.Store { return m.idem }

// Serve serves HTTP on ln and runs the job engine, auth housekeeping
// (expired-session sweeping) and the audit retention purge until ctx is
// canceled, then shuts down gracefully.
func (m *Manager) Serve(ctx context.Context, ln net.Listener) error {
	engineCtx, stopEngine := context.WithCancel(ctx)
	engineDone := make(chan struct{})
	go func() {
		defer close(engineDone)
		_ = m.jobs.Run(engineCtx)
	}()
	authDone := make(chan struct{})
	go func() {
		defer close(authDone)
		m.auth.RunHousekeeping(engineCtx)
	}()
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		_ = m.audit.Run(engineCtx)
	}()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		m.identity.RunStreamSweeper(engineCtx)
	}()
	// Observation (#5): collection, inventory refresh, the event journal,
	// and metrics rollups/retention (bounded passes).
	observeDone := make(chan struct{})
	go func() {
		defer close(observeDone)
		m.observe.Run(engineCtx)
	}()
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		m.metrics.Run(engineCtx)
	}()
	// The scheduler (#13) enqueues due policy runs in the job engine.
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		_ = m.sched.Run(engineCtx)
	}()
	// Manager moves: code expiry and the confirmation to the old manager.
	movesDone := make(chan struct{})
	go func() {
		defer close(movesDone)
		m.moves.Run(engineCtx)
	}()
	// Backups (#10): retention after finished sets.
	backupsDone := make(chan struct{})
	go func() {
		defer close(backupsDone)
		m.backups.Run(engineCtx)
	}()
	// Live synchronization (#23): the live stream hub, its job source and
	// the agents' file watch sets.
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		var wg sync.WaitGroup
		wg.Go(func() { m.live.Run(engineCtx) })
		wg.Go(func() { m.liveJobs.Run(engineCtx) })
		wg.Go(func() { m.fileWatch.Run(engineCtx) })
		wg.Wait()
	}()
	defer func() {
		stopEngine()
		<-engineDone
		<-authDone
		<-auditDone
		<-sweepDone
		<-observeDone
		<-metricsDone
		<-schedDone
		<-backupsDone
		<-movesDone
		<-liveDone
	}()
	srv := server.HTTPServer(m.handler, m.opts.Logger)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownGrace)
		defer cancel()
		// Hijacked agent WebSockets are not closed by srv.Shutdown: close
		// them with 1001 (going away) so agents reconnect with backoff.
		m.agents.Hub().Shutdown(shutdownCtx)
		// Exec terminals (#8) are hijacked too: close them with 1001.
		m.io.Close()
		err := srv.Shutdown(shutdownCtx)
		<-errCh
		return err
	}
	select {
	case <-ctx.Done():
		return shutdown()
	case <-m.restart:
		m.opts.Logger.Warn("restarting the manager (controlled restart)")
		if err := shutdown(); err != nil {
			return err
		}
		return ErrRestart
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Close stops manager-local jobs (recovered on the next start) and releases
// the database.
func (m *Manager) Close() error {
	if m.templates != nil {
		m.templates.Close()
	}
	if m.resources != nil {
		m.resources.Close()
	}
	if m.files != nil {
		m.files.Close()
	}
	if m.io != nil {
		m.io.Close()
	}
	if m.jobs != nil {
		m.jobs.Close()
	}
	var errs []error
	if m.metrics != nil {
		errs = append(errs, m.metrics.Close())
	}
	errs = append(errs, m.db.Close())
	return errors.Join(errs...)
}

// Run starts the manager, binds the listener and serves until ctx ends.
func Run(ctx context.Context, opts Options) error {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	// One log ring for every manager started by this Run (controlled
	// restarts keep the recent lines).
	opts = withLogRing(opts)
	cfg, log := opts.Config, opts.Logger
	info := buildinfo.Get()
	log.Info("starting docker-manager", "version", info.Version, "commit", info.Commit,
		"public_url", cfg.PublicURL.String(), "data_dir", cfg.DataDir, "ui_built", opts.UIBuilt)
	if cfg.LocalDevelopment {
		log.Warn("DOCKER_MANAGER_PUBLIC_URL is plain http on a loopback host: local development mode, not for production")
	}
	if !opts.UIBuilt {
		log.Warn("serving the placeholder UI; this binary was built without the SvelteKit assets")
	}

	// A controlled restart (a staged manager-state restore, #24) starts the
	// manager again in this process.
	for {
		err := runOnce(ctx, opts)
		if !errors.Is(err, ErrRestart) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// runOnce starts, serves and closes one manager instance.
func runOnce(ctx context.Context, opts Options) error {
	cfg, log := opts.Config, opts.Logger
	m, err := Start(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	listen := opts.Listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	log.Info("listening", "addr", ln.Addr().String(), "instance_id", m.Instance().ID)
	if opts.OnStarted != nil {
		opts.OnStarted(m)
	}
	if opts.OnListening != nil {
		opts.OnListening(ln.Addr())
	}
	return m.Serve(ctx, ln)
}

// originString renders the public URL for the settings view ("" unset).
func originString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
