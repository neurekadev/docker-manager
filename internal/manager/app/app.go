// Package app wires the manager together and owns its startup order:
//
//	config → data dir → open DB → (snapshot) → migrate → secret key /
//	instance → auth primitives → audit trail → job engine recovery →
//	HTTP handler → listener → serve (+ job engine loop, auth housekeeping,
//	audit retention)
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
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/auth"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/manager/idempotency"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/secrets"
	"github.com/neurekadev/dockyard/internal/manager/server"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// ShutdownGrace bounds graceful HTTP shutdown.
const ShutdownGrace = 15 * time.Second

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
}

// Manager is a started (migrated, not yet serving) manager.
type Manager struct {
	opts     Options
	db       *bun.DB
	instance domain.Instance
	keyring  *secrets.Keyring
	auth     *auth.Kit
	jobs     *jobs.Engine
	idem     *idempotency.Store
	audit    *audit.Log
	handler  http.Handler
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
		IdleTimeout: cfg.Sessions.IdleTimeout, Lifetime: cfg.Sessions.Lifetime,
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

	// Authorization fails closed until #16/#17 provide principals and
	// grants; agents are unreachable until the #3 transport exists.
	authorizer := authz.DenyAll{}
	m.jobs, err = jobs.New(jobs.Options{
		DB: db, Clock: opts.Clock, Logger: log.With("component", "jobs"),
		Dispatcher: jobs.NoAgents{}, Authorizer: authorizer, Audit: m.audit,
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
	if err := m.jobs.Recover(ctx); err != nil {
		m.jobs.Close()
		return nil, fmt.Errorf("recover jobs: %w", err)
	}

	m.idem, err = idempotency.New(idempotency.Options{DB: db, Keyring: m.keyring, Clock: opts.Clock})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}

	srv, err := server.New(server.Options{
		Logger: log,
		Clock:  opts.Clock,
		UI:     opts.UI,
		API: api.Deps{
			Build:        buildinfo.Get(),
			Readiness:    m.readiness,
			Jobs:         m.jobs,
			Authorizer:   authorizer,
			Clock:        opts.Clock,
			Idempotency:  m.idem,
			Audit:        m.audit,
			SSEHeartbeat: cfg.StreamHeartbeat,
		},
		TrustedProxies:   cfg.TrustedProxies,
		PublicURL:        cfg.PublicURL,
		LocalDevelopment: cfg.LocalDevelopment,
		StreamHeartbeat:  cfg.StreamHeartbeat,
	})
	if err != nil {
		m.jobs.Close()
		return nil, err
	}
	m.handler = srv.Handler
	ok = true
	return m, nil
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
	defer func() {
		stopEngine()
		<-engineDone
		<-authDone
		<-auditDone
	}()
	srv := server.HTTPServer(m.handler, m.opts.Logger)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownGrace)
		defer cancel()
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
	if m.jobs != nil {
		m.jobs.Close()
	}
	return m.db.Close()
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
