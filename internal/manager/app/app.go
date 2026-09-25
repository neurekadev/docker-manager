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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/manager/agents"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/auth"
	"github.com/neurekadev/dockyard/internal/manager/auth/password"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/authz/catalog"
	"github.com/neurekadev/dockyard/internal/manager/builds"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/containerio"
	"github.com/neurekadev/dockyard/internal/manager/events"
	"github.com/neurekadev/dockyard/internal/manager/files"
	"github.com/neurekadev/dockyard/internal/manager/gitcreds"
	"github.com/neurekadev/dockyard/internal/manager/idempotency"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/metrics"
	"github.com/neurekadev/dockyard/internal/manager/observe"
	"github.com/neurekadev/dockyard/internal/manager/permissions"
	"github.com/neurekadev/dockyard/internal/manager/regclient"
	"github.com/neurekadev/dockyard/internal/manager/registries"
	"github.com/neurekadev/dockyard/internal/manager/resources"
	"github.com/neurekadev/dockyard/internal/manager/scheduler"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/server"
	"github.com/neurekadev/dockyard/internal/manager/stacks"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/selfid"
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
	// ContainerID overrides the detection of the manager's own container
	// (selfid.Detect; #32 tells co-located agents which container is the
	// manager). Tests set it.
	ContainerID string
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
	stacks    *stacks.Service
	io        *containerio.Service
	handler   http.Handler
	git       *gitcreds.Service
	builds    *builds.Service
	sched     *scheduler.Service
}

// ErrSecretKeyMissing means the database belongs to an existing installation
// but its secret-protection key file is absent.
var ErrSecretKeyMissing = errors.New("secret-protection key file is missing for an existing installation; " +
	"restore it (DOCKYARD_SECRET_KEY_FILE) instead of letting DockYard generate a new one, or encrypted settings become unreadable")

// Start opens and migrates the database and prepares the HTTP handler.
func Start(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Migrations == nil {
		opts.Migrations = migrations.Migrations
	}
	cfg, log := opts.Config, opts.Logger

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := store.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return nil, err
	}
	m := &Manager{opts: opts, db: db}
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

	// Auth primitives (#18): a public URL that cannot be a WebAuthn relying
	// party fails startup here, before anything listens.
	if m.auth, err = auth.NewKit(auth.KitOptions{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "auth"), PublicURL: cfg.PublicURL,
		IdleTimeout: cfg.Sessions.IdleTimeout, Lifetime: cfg.Sessions.Lifetime, PasswordParams: opts.PasswordParams,
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
		ManagerVersion: buildinfo.Get().Version, PublicURL: cfg.PublicURL, Audit: m.audit,
	})
	if err != nil {
		return nil, err
	}
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
		Jobs: m.jobs, Audit: m.audit}); err != nil {
		m.jobs.Close()
		return nil, err
	}
	if err := m.jobs.Recover(ctx); err != nil {
		m.jobs.Close()
		return nil, fmt.Errorf("recover jobs: %w", err)
	}
	m.agents.AttachJobs(m.jobs)
	// The scoped file manager (#15): stack scopes resolve through the stack
	// service (#7), which records a revision when a definition file changes.
	m.files = files.New(files.Options{Agents: m.agents.Hub(), Jobs: m.jobs, Logger: log.With("component", "files")})
	m.files.SetStacks(m.stacks, m.stacks)

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
		}})
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
	// Stack deploy/stop/restart/down/remove refuse DockYard's own Compose
	// project (#32).
	m.stacks.SetProtection(m.resources)
	hub.AddReconciler(func(ctx context.Context, s *agents.Session) error {
		// Self-protection (#32): the agent learns which manager it serves
		// and which container is that manager (co-located or not).
		if s.Serves(protocol.ReqManagerIdentity) {
			if _, err := s.Request(ctx, protocol.ReqManagerIdentity,
				protocol.ManagerIdentityInput{InstanceID: m.instance.ID, ContainerID: containerID}, reconcileRequestTimeout); err != nil {
				log.Warn("could not send the manager identity to the agent", "environment_id", s.EnvironmentID(), "error", err)
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

	srv, err := server.New(server.Options{
		Logger: log,
		Clock:  opts.Clock,
		UI:     opts.UI,
		API: api.Deps{
			Build:          buildinfo.Get(),
			Readiness:      m.readiness,
			Jobs:           m.jobs,
			Authorizer:     authorizer,
			Clock:          opts.Clock,
			Idempotency:    m.idem,
			Audit:          m.audit,
			SSEHeartbeat:   cfg.StreamHeartbeat,
			Identity:       m.identity,
			Agents:         m.agents,
			Permissions:    m.perms,
			Registries:     m.regs,
			APITokens:      m.identity,
			Observe:        m.observe,
			Docker:         m.resources,
			InstanceID:     m.instance.ID,
			Files:          m.files,
			FilesMaxUpload: cfg.FilesMaxUpload,
			GitCredentials: m.git,
			Builds:         m.builds,
			Stacks:         m.stacks,
			Events:         m.events,
			ContainerIO:    m.io,
			Schedules:      m.sched,
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
	s := &protocol.CommandSecrets{Registries: regs, Git: git}
	if s.Empty() {
		return nil, nil
	}
	return s, nil
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
		log.Info("initialized new DockYard instance", "instance_id", inst.ID)
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

// Events returns the internal event bus.
func (m *Manager) Events() *events.Bus { return m.events }

// Resources returns the Docker resource service (#6).
func (m *Manager) Resources() *resources.Service { return m.resources }

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
	defer func() {
		stopEngine()
		<-engineDone
		<-authDone
		<-auditDone
		<-sweepDone
		<-observeDone
		<-metricsDone
		<-schedDone
	}()
	srv := server.HTTPServer(m.handler, m.opts.Logger)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
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
	cfg, log := opts.Config, opts.Logger
	info := buildinfo.Get()
	log.Info("starting dockyard-manager", "version", info.Version, "commit", info.Commit,
		"public_url", cfg.PublicURL.String(), "data_dir", cfg.DataDir, "ui_built", opts.UIBuilt)
	if cfg.LocalDevelopment {
		log.Warn("DOCKYARD_PUBLIC_URL is plain http on a loopback host: local development mode, not for production")
	}
	if !opts.UIBuilt {
		log.Warn("serving the placeholder UI; this binary was built without the SvelteKit assets")
	}

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
	if opts.OnListening != nil {
		opts.OnListening(ln.Addr())
	}
	return m.Serve(ctx, ln)
}
