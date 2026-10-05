// Package runtime is the agent's main loop.
//
// The agent is an outbound-only connector: it dials the manager and never
// opens a listening socket (enforced by nolisten_test.go). It must run as
// root (UID 0, #28). At startup it connects to the local Docker Engine
// through the Moby adapter (internal/agent/engine, #21), negotiates the API
// version and logs the Engine identity; Capabilities() exposes that
// identity for the hello/capabilities frame of the session protocol (#3).
//
// Once the Engine identity is known, the control loop (control.go) enrolls
// with a one-use token (DOCKER_AGENT_ENROLLMENT_TOKEN(_FILE) or one handed over
// by `docker-agent enroll` through the state directory), stores the
// credential (internal/agent/state) and keeps the manager session up
// (internal/agent/session) with the job runner (internal/agent/jobs) wired
// in. The loop reports liveness and the connection state through a health
// file in the state directory, which `docker-agent healthcheck` checks
// for freshness.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/backups"
	"github.com/neurekadev/docker-manager/internal/agent/builds"
	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/config"
	"github.com/neurekadev/docker-manager/internal/agent/containerio"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/files"
	"github.com/neurekadev/docker-manager/internal/agent/health"
	agentjobs "github.com/neurekadev/docker-manager/internal/agent/jobs"
	"github.com/neurekadev/docker-manager/internal/agent/migration"
	"github.com/neurekadev/docker-manager/internal/agent/observe"
	"github.com/neurekadev/docker-manager/internal/agent/protect"
	"github.com/neurekadev/docker-manager/internal/agent/prune"
	"github.com/neurekadev/docker-manager/internal/agent/resources"
	"github.com/neurekadev/docker-manager/internal/agent/selfupdate"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/agent/smartctl"
	"github.com/neurekadev/docker-manager/internal/agent/stacks"
	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/agent/transport"
	"github.com/neurekadev/docker-manager/internal/agent/volumelabels"
	"github.com/neurekadev/docker-manager/internal/agent/watch"
	"github.com/neurekadev/docker-manager/internal/buildinfo"
	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobexec"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/selfid"
)

// Health file settings.
const (
	HealthFileName = "health.json"
	HealthInterval = 15 * time.Second
	// HealthMaxAge is how stale the health file may be before the agent is
	// considered unhealthy (several missed updates).
	HealthMaxAge = 4 * HealthInterval
)

// Engine connection retry backoff.
const (
	EngineRetryMin = 2 * time.Second
	EngineRetryMax = time.Minute
	// engineConnectTimeout bounds one connection attempt.
	engineConnectTimeout = 30 * time.Second
	// enginePingTimeout bounds the liveness ping on every health tick.
	enginePingTimeout = 5 * time.Second
)

// ErrNotRoot is returned when the agent is not running as UID 0.
var ErrNotRoot = errors.New("docker-agent must run as root (UID 0): it needs to read and write container-owned files " +
	"in Docker volumes for browsing, backup and restore. Running Docker Manager containers as a non-root user is not supported " +
	"(remove any `user:` override from the agent service)")

// Options configures Run.
type Options struct {
	Config config.Config
	Logger *slog.Logger
	Clock  clock.Clock
	// Geteuid defaults to os.Geteuid; tests inject it.
	Geteuid func() int
	// ConnectEngine opens the Engine adapter; default engine.Connect on
	// Config.DockerHost. Tests inject fakes.
	ConnectEngine func(ctx context.Context) (engine.Engine, error)
	// ConnectCompose opens the Compose SDK adapter once the Engine is
	// connected; default compose.New on Config.DockerHost. guard is the
	// storage check every project directory must pass (#28).
	ConnectCompose func(ctx context.Context, eng engine.Engine, guard func(dir string) error) (*compose.Adapter, error)
	// VerifyStorage checks the identical-path layout (#28); default
	// storage.Verify with Config.StacksVolume and Config.StackRoots.
	VerifyStorage func(ctx context.Context, eng engine.Engine) storage.Result
	// OnCapabilities is called whenever Capabilities change (Engine
	// connected, lost or recovered); the live session resends its
	// capabilities frame at the same time. It must not block.
	OnCapabilities func(Capabilities)
	// Executors are the agent job executors (feature workstreams add
	// theirs; internal/agent/jobs).
	Executors []jobexec.Executor
	// Requests are the named request handlers the session serves
	// (docs/internal/protocol/agent-v1.md, "Allowed requests").
	Requests map[string]session.RequestHandler
	// SelfContainerID overrides the detection of the agent's own container
	// (selfid.Detect, #32); tests set it.
	SelfContainerID string
	// Observe runs the host/container sampler and the Docker event relay
	// and serves engine.info and host.metrics (#5). The docker-agent
	// command sets it; focused tests leave it off.
	Observe bool
	// Streams are the stream handlers by kind ("Allowed streams").
	Streams map[string]session.StreamHandler
	// Files serves the scoped file manager (#15): files.* requests, the
	// files.download/files.upload streams and the files.* job executors,
	// confined to stack project directories and local volumes (#28).
	Files bool
	// ContainerIO serves container logs and exec sessions (#8): the
	// container.logs and container.exec.* requests and streams.
	ContainerIO bool
	// Backups serves backups (#10): the backup.* executors, requests and
	// the backup.file stream, running restic (Config.ResticBinary).
	Backups bool
	// Restic overrides the restic runner of backups (tests).
	Restic restic.Opener
	// TokenPoll is how often a handed-over enrollment token is looked for
	// (default DefaultTokenPoll).
	TokenPoll time.Duration
	// Backoff overrides the session reconnect policy (tests).
	Backoff session.Backoff
	// NewNotifier overrides the file watcher's kernel notifier (tests;
	// default fsnotify).
	NewNotifier func() (watch.Notifier, error)

	afterHealthWrite func(time.Time) // test hook
}

// HealthState is the content of the health file.
type HealthState struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	// Engine is "connected" or the error code of the last connection
	// attempt (e.g. "engine_unavailable", "unsupported_api_version").
	Engine string `json:"engine"`
	// Storage is "verified", "pending" or the first storage diagnostic code
	// (#28), e.g. "storage_path_mismatch".
	Storage string `json:"storage"`
	// AgentID and EnvironmentID are set once enrolled.
	AgentID       string `json:"agentId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	// ManagerURL is the manager origin the agent dials; ManagerURLSource
	// says where it comes from: config (DOCKER_AGENT_MANAGER_URL) or move
	// (the address a moving manager sent, manager.redirect).
	ManagerURL       string `json:"managerUrl,omitempty"`
	ManagerURLSource string `json:"managerUrlSource,omitempty"`
}

// Capabilities is what the agent reports to the manager in its
// hello/capabilities frame (#3). Engine is nil until the Engine was
// reached; EngineError then explains why.
type Capabilities struct {
	AgentVersion string
	AgentCommit  string
	Engine       *engine.Identity
	EngineError  *EngineError
	// Storage is the result of the #28 layout check (nil until the Engine
	// was reached). Stack operations are allowed only when Storage.StacksOK().
	Storage *storage.Result
}

// EngineError is a stable code plus a human-readable message.
type EngineError struct {
	Code    engine.Code
	Message string
}

// Agent is a running agent.
type Agent struct {
	opts Options
	log  *slog.Logger

	mu        sync.RWMutex
	transport *transport.Transport
	tinfo     protocol.TransportInfo
	eng       engine.Engine
	compose   *compose.Adapter
	storage   *storage.Result
	engErr    *EngineError
	healthy   bool // the Engine answered the last ping
	status    string
	identity  *state.Credential

	// guard identifies Docker Manager's own resources (#32).
	guard *protect.Guard
	// volumeLabels are the labels stack definitions declare on volumes that
	// Docker could not apply (recorded at deploys, honored by backups and
	// maintenance, shown on volumes).
	volumeLabels *volumelabels.Store
	// self hands the agent's own Compose service to a helper container
	// when Docker Manager redeploys or updates itself (#32).
	self *selfupdate.Launcher

	healthMu    sync.Mutex
	store       *state.Store
	client      *session.Client
	engineReady chan struct{}
	readyOnce   sync.Once

	sampler *observe.Sampler
	// health reads SMART and RAID state (#143); nil without Observe.
	health *health.Monitor
	// watcher watches the declared file scopes (#23); nil without Files.
	watcher *watch.Watcher
	// execShell: container.exec.create is served by containerio, which
	// resolves terminal shells (#8, FeatureExecShell).
	execShell bool
	// redirectSecure: manager.redirect is served by managerRedirect, which
	// accepts a secure address at the current generation
	// (FeatureManagerRedirectSecure).
	redirectSecure bool
}

// Run runs the agent until ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	a, err := New(opts)
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// New validates opts (including the root check) and returns an Agent.
func New(opts Options) (*Agent, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Geteuid == nil {
		opts.Geteuid = os.Geteuid
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if uid := opts.Geteuid(); uid != 0 {
		return nil, fmt.Errorf("%w (current UID %d)", ErrNotRoot, uid)
	}
	if opts.ConnectEngine == nil {
		host, logger := opts.Config.DockerHost, opts.Logger
		opts.ConnectEngine = func(ctx context.Context) (engine.Engine, error) {
			return engine.Connect(ctx, engine.Options{Host: host, Logger: logger})
		}
	}
	if opts.ConnectCompose == nil {
		host, logger := opts.Config.DockerHost, opts.Logger
		opts.ConnectCompose = func(ctx context.Context, eng engine.Engine, guard func(string) error) (*compose.Adapter, error) {
			return compose.New(ctx, compose.Options{Host: host, Engine: eng, Logger: logger, Guard: guard})
		}
	}
	if opts.VerifyStorage == nil {
		volume, roots := opts.Config.StacksVolume, opts.Config.StackRoots
		if volume == "" {
			volume = config.DefaultStacksVolume
		}
		opts.VerifyStorage = func(ctx context.Context, eng engine.Engine) storage.Result {
			return storage.Verify(ctx, storage.Options{Engine: eng, StacksVolume: volume, StackRoots: roots})
		}
	}
	if opts.TokenPoll <= 0 {
		opts.TokenPoll = DefaultTokenPoll
	}
	a := &Agent{opts: opts, log: opts.Logger, status: StatusNotEnrolled, engineReady: make(chan struct{})}
	// Image builds from Git (#33) run on every agent unless a test
	// supplies its own image.build executor.
	if !slices.ContainsFunc(opts.Executors, func(e jobexec.Executor) bool { return e.Kind == jobspec.ImageBuild }) {
		a.opts.Executors = append(slices.Clone(opts.Executors), builds.Executor(builds.Options{
			Engine: a.Engine, Clock: opts.Clock, Logger: opts.Logger.With("component", "builds")}))
	}
	if opts.Observe {
		a.sampler = observe.New(observe.Options{Clock: opts.Clock, Logger: opts.Logger, ProcRoot: opts.Config.HostProc,
			SysRoot: opts.Config.HostSys, Engine: a.observedEngine, Roots: a.observedRoots})
		a.health = newHealthMonitor(opts, a.sampler)
		reqs := make(map[string]session.RequestHandler, len(opts.Requests)+4)
		reqs[protocol.ReqEngineInfo] = a.sampler.EngineInfo
		reqs[protocol.ReqHostMetrics] = a.sampler.HostMetrics
		reqs[protocol.ReqMetricsLive] = a.sampler.LiveMetrics
		reqs[protocol.ReqHostHealth] = a.health.HostHealth
		for k, v := range opts.Requests {
			reqs[k] = v
		}
		a.opts.Requests = reqs
	}
	a.volumeLabels = volumelabels.New(opts.Config.StateDir)
	a.addResources()
	a.enableRedirect()
	if opts.Files {
		a.enableFiles()
	}
	// Compose stacks (#7): the compose.* requests and stack.* executors run
	// on the live Engine/Compose adapters and the verified storage roots.
	a.self = selfupdate.New(selfupdate.Options{StateDir: opts.Config.StateDir, SelfContainerID: a.guard.SelfContainerID(),
		Engine: a.Engine, DockerHost: opts.Config.DockerHost, Logger: opts.Logger})
	st := stacks.New(stacks.Options{Deps: stackDeps{a}, Clock: opts.Clock, Logger: opts.Logger.With("component", "stacks"), Guard: a.guard,
		Self: a.self, VolumeLabels: a.volumeLabels})
	own := map[domain.JobKind]bool{}
	for _, x := range a.opts.Executors {
		own[x.Kind] = true
	}
	// Docker Manager's own Compose project is never stopped or taken down
	// through a stack job (#32); deploys hand the agent to a helper.
	for _, x := range a.guard.GuardStacks(a.Engine, st.Executors()) {
		if !own[x.Kind] {
			a.opts.Executors = append(a.opts.Executors, x)
		}
	}
	reqs := st.Requests()
	maps.Copy(reqs, a.opts.Requests)
	a.opts.Requests = reqs
	if opts.ContainerIO {
		a.enableContainerIO()
	}
	a.enableMigration()
	if opts.Backups {
		a.enableBackups()
	}
	return a, nil
}

// newHealthMonitor builds the disk health monitor (#143): SMART through
// the pinned smartctl unless DOCKER_AGENT_SMART_ENABLED is false, RAID
// from the sampler's procfs.
func newHealthMonitor(opts Options, sampler *observe.Sampler) *health.Monitor {
	cfg := opts.Config
	var smart health.SMART
	if cfg.SMARTEnabled {
		bin := cfg.SmartctlBinary
		if bin == "" {
			bin = config.DefaultSmartctlBinary
		}
		smart = &smartctl.Runner{Binary: bin, Logger: opts.Logger.With("component", "smartctl")}
	}
	return health.New(health.Options{Clock: opts.Clock, Logger: opts.Logger, Proc: sampler.Proc(), SMART: smart,
		Interval: cfg.SMARTInterval, WakeAfter: cfg.SMARTWakeAfter})
}

// enableBackups wires backups (#10): requests, the backup.file stream and
// the backup.* executors. Explicitly configured handlers win.
func (a *Agent) enableBackups() {
	cfg := a.opts.Config
	opener := a.opts.Restic
	if opener == nil {
		bin := cfg.ResticBinary
		if bin == "" {
			bin = config.DefaultResticBinary
		}
		opener = &restic.Runner{Binary: bin, CacheDir: filepath.Join(cfg.StateDir, "restic-cache"),
			TempDir: filepath.Join(cfg.StateDir, "tmp"), Logger: a.log.With("component", "restic")}
	}
	svc := backups.New(backups.Options{
		Engine: a.Engine,
		Loader: func() backups.Loader {
			if c := a.Compose(); c != nil {
				return c
			}
			return nil
		},
		Storage:           func() *storage.Result { return a.Capabilities().Storage },
		Guard:             a.guard,
		VolumeLabels:      a.volumeLabels,
		StateDir:          cfg.StateDir,
		Restic:            opener,
		ExternalAllowlist: cfg.BackupExternalAllowlist,
		Clock:             a.opts.Clock,
		Logger:            a.log.With("component", "backups"),
	})
	reqs := svc.Requests()
	maps.Copy(reqs, a.opts.Requests)
	a.opts.Requests = reqs
	streams := svc.Streams()
	maps.Copy(streams, a.opts.Streams)
	a.opts.Streams = streams
	own := map[domain.JobKind]bool{}
	for _, x := range a.opts.Executors {
		own[x.Kind] = true
	}
	for _, x := range svc.Executors() {
		if !own[x.Kind] {
			a.opts.Executors = append(a.opts.Executors, x)
		}
	}
}

// enableMigration wires environment migration (#35): the migration.*
// requests, the migration.send/receive streams and the
// stack.remove_source executor, over the live Engine and the verified
// storage roots. Explicitly configured handlers and executors win.
func (a *Agent) enableMigration() {
	svc := migration.New(migration.Options{Deps: stackDeps{a}, Guard: a.guard, Clock: a.opts.Clock,
		Logger: a.opts.Logger})
	reqs := svc.Requests()
	maps.Copy(reqs, a.opts.Requests)
	a.opts.Requests = reqs
	streams := svc.Streams()
	maps.Copy(streams, a.opts.Streams)
	a.opts.Streams = streams
	own := map[domain.JobKind]bool{}
	for _, x := range a.opts.Executors {
		own[x.Kind] = true
	}
	for _, x := range svc.Executors() {
		if !own[x.Kind] {
			a.opts.Executors = append(a.opts.Executors, x)
		}
	}
}

// addResources adds the Docker resource requests and executors (#6) to
// the options; handlers and executors the caller passed for the same
// names or kinds win (tests).
func (a *Agent) addResources() {
	self := a.opts.SelfContainerID
	if self == "" {
		self = selfid.Detect()
	}
	stacks := a.opts.Config.StacksVolume
	if stacks == "" {
		stacks = config.DefaultStacksVolume
	}
	// Docker Manager's own resources (#32): the agent refuses to stop or remove
	// itself, the co-located manager and their data, whatever the manager
	// sends.
	a.guard = protect.New(protect.Options{SelfContainerID: self, StacksVolume: stacks, Logger: a.log})
	a.log.Info("self-protection", "agent_container_id", self, "stacks_volume", stacks)
	svc := resources.New(resources.Options{
		Engine:          a.Engine,
		ManagedStackDir: func(dir string) bool { return a.StackGuard(dir) == nil },
		Guard:           a.guard,
		VolumeLabels:    a.volumeLabels,
		Clock:           a.opts.Clock,
		Logger:          a.log,
	})
	reqs := svc.Requests()
	maps.Copy(reqs, a.opts.Requests)
	a.opts.Requests = reqs
	own := map[domain.JobKind]bool{}
	for _, x := range a.opts.Executors {
		own[x.Kind] = true
	}
	for _, x := range svc.Executors() {
		if !own[x.Kind] {
			a.opts.Executors = append(a.opts.Executors, x)
		}
	}
	// Prune policies (#14): the maintenance.preview request and the
	// prune.run executor share the guard, so Docker Manager's own resources are
	// never candidates.
	pr := prune.New(prune.Options{
		Engine:          a.Engine,
		ManagedStackDir: func(dir string) bool { return a.StackGuard(dir) == nil },
		Guard:           a.guard,
		VolumeLabels:    a.volumeLabels,
		Clock:           a.opts.Clock,
		Logger:          a.log,
	})
	reqs = pr.Requests()
	maps.Copy(reqs, a.opts.Requests)
	a.opts.Requests = reqs
	if x := pr.Executor(); !own[x.Kind] {
		a.opts.Executors = append(a.opts.Executors, x)
	}
}

// rescan is the session's rescan handler (nil without the watcher).
func (a *Agent) rescan() func(context.Context, protocol.RescanPayload) (protocol.RescanResult, error) {
	if a.watcher == nil {
		return nil
	}
	return a.watcher.Rescan
}

// observedEngine is the Engine for observation (nil while disconnected).
func (a *Agent) observedEngine() observe.EngineAPI {
	if e := a.Engine(); e != nil {
		return e
	}
	return nil
}

// stackDeps exposes the agent's live components to the stacks service.
type stackDeps struct{ a *Agent }

func (d stackDeps) Engine() engine.Engine { return d.a.Engine() }

func (d stackDeps) Composer() stacks.Composer {
	if c := d.a.Compose(); c != nil {
		return c
	}
	return nil
}

// observedRoots are the verified storage roots whose filesystems are
// reported (#28, #5).
func (a *Agent) observedRoots() []observe.Root {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.storage == nil {
		return nil
	}
	var out []observe.Root
	for _, r := range a.storage.Roots {
		if r.OK {
			out = append(out, observe.Root{Kind: r.Kind, Path: r.Path})
		}
	}
	return out
}

// enableContainerIO wires container logs and exec sessions (#8) into the
// session's requests and streams. Explicitly configured handlers win.
func (a *Agent) enableContainerIO() {
	svc := containerio.New(containerio.Options{
		Engine: func() containerio.Engine {
			if e := a.Engine(); e != nil {
				return e
			}
			return nil
		},
		Clock: a.opts.Clock, Logger: a.opts.Logger,
	})
	a.execShell = a.opts.Requests[protocol.ReqContainerExecCreate] == nil
	reqs := svc.Requests()
	for k, v := range a.opts.Requests {
		reqs[k] = v
	}
	a.opts.Requests = reqs
	streams := svc.Streams()
	for k, v := range a.opts.Streams {
		streams[k] = v
	}
	a.opts.Streams = streams
}

// enableFiles wires the scoped file service (#15) into the session's
// requests, streams and the job runner's executors. Explicitly configured
// handlers win.
func (a *Agent) enableFiles() {
	svc := files.New(files.Options{
		Engine: func() files.Engine {
			if e := a.Engine(); e != nil {
				return e
			}
			return nil
		},
		Storage: func() *storage.Result { return a.Capabilities().Storage },
		Clock:   a.opts.Clock, Logger: a.opts.Logger,
		Invalidate: func(p protocol.FSInvalidationPayload) {
			if c := a.client; c != nil {
				c.FileInvalidations().Publish(p)
			}
		},
	})
	reqs := svc.Requests()
	// The scoped file watcher (#23): the manager declares the scopes
	// (files.watch), changes become fs_invalidation frames, rescans are
	// answered from its reconciliation baseline.
	a.watcher = watch.New(watch.Options{
		Resolve: svc.ScopeDir, Clock: a.opts.Clock, Logger: a.opts.Logger, NewNotifier: a.opts.NewNotifier,
		Invalidate: func(p protocol.FSInvalidationPayload) {
			if c := a.client; c != nil {
				c.FileInvalidations().Publish(p)
			}
		},
		MaxWatches: a.opts.Config.WatchMax,
	})
	reqs[protocol.ReqFilesWatch] = func(ctx context.Context, input json.RawMessage) (any, error) {
		var in protocol.FilesWatchInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed files.watch input"}
		}
		if err := in.Validate(); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
		}
		return a.watcher.SetScopes(ctx, in), nil
	}
	for k, v := range a.opts.Requests {
		reqs[k] = v
	}
	a.opts.Requests = reqs
	streams := svc.Streams()
	for k, v := range a.opts.Streams {
		streams[k] = v
	}
	a.opts.Streams = streams
	a.opts.Executors = append(append([]jobexec.Executor(nil), a.opts.Executors...), svc.Executors()...)
}

// rootWatchMode is how the watcher can observe a root's filesystem (#23).
func (a *Agent) rootWatchMode(dir string) string {
	switch {
	case a.watcher == nil:
		return "none"
	case !a.watcher.Notifying() || watch.RemoteFilesystem(dir):
		return protocol.WatchPoll
	}
	return protocol.WatchInotify
}

func (d stackDeps) Storage() *storage.Result {
	d.a.mu.RLock()
	defer d.a.mu.RUnlock()
	if d.a.storage == nil {
		return nil
	}
	r := *d.a.storage
	return &r
}

// Capabilities returns the current capabilities (safe for concurrent use).
func (a *Agent) Capabilities() Capabilities {
	info := buildinfo.Get()
	c := Capabilities{AgentVersion: info.Version, AgentCommit: info.Commit}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.eng != nil {
		id := a.eng.Identity()
		c.Engine = &id
	}
	if a.engErr != nil {
		e := *a.engErr
		c.EngineError = &e
	}
	if a.storage != nil {
		r := *a.storage
		c.Storage = &r
	}
	return c
}

// StackGuard is the compose adapter's guard: a project directory must be in
// a verified stack root (#28). Before the check ran it refuses.
func (a *Agent) StackGuard(dir string) error {
	a.mu.RLock()
	r := a.storage
	a.mu.RUnlock()
	if r == nil {
		return storage.Diagnostic{Code: storage.CodeUnverified, Message: "the storage layout has not been verified yet"}
	}
	return r.Allows(dir)
}

// CapabilitiesPayload maps Capabilities to the protocol's capabilities
// frame (#3). ok is false while the Engine is not usable: the frame needs
// the Engine ID and API version. Commands, requests, streams and roots are
// filled in by the workstreams that implement them.
func (a *Agent) CapabilitiesPayload() (protocol.CapabilitiesPayload, bool) {
	c := a.Capabilities()
	a.mu.RLock()
	ti := a.tinfo
	a.mu.RUnlock()
	p := protocol.CapabilitiesPayload{
		AgentVersion: c.AgentVersion,
		Protocols:    []string{protocol.Version},
		OS:           goruntime.GOOS,
		Arch:         goruntime.GOARCH,
		Commands:     a.commands(),
		Requests:     []string{},
		Streams:      []string{},
		Transport:    ti,
	}
	for _, e := range a.opts.Executors {
		if !slices.Contains(p.Commands, string(e.Kind)) {
			p.Commands = append(p.Commands, string(e.Kind))
		}
	}
	slices.Sort(p.Commands)
	// It writes Docker Manager's labels under protocol.LabelPrefix and
	// accepts ownership labels under both prefixes.
	p.Features = append(p.Features, protocol.FeatureLabels)
	// This agent's restore.run serves the full and paths scopes (#10).
	if slices.Contains(p.Commands, "restore.run") {
		p.Features = append(p.Features, protocol.FeatureRestoreSelection)
	}
	// Its stack.remove also removes the stack's own volumes when asked.
	if slices.Contains(p.Commands, "stack.remove") {
		p.Features = append(p.Features, protocol.FeatureStackRemoveVolumes)
	}
	// It imports projects by copying them from its import mounts (#7).
	if slices.Contains(p.Commands, "stack.import") {
		p.Features = append(p.Features, protocol.FeatureStackImportCopy)
		// ... also projects that have no containers.
		p.Features = append(p.Features, protocol.FeatureStackImportContainerless)
	}
	// It pulls a stack's images without deploying them.
	if slices.Contains(p.Commands, "stack.pull") {
		p.Features = append(p.Features, protocol.FeatureStackPull)
	}
	// It renames stacks, moving their volumes and directory (#7).
	if slices.Contains(p.Commands, "stack.rename") {
		p.Features = append(p.Features, protocol.FeatureStackRename)
	}
	// It recreates standalone containers (#273).
	if slices.Contains(p.Commands, "container.recreate") {
		p.Features = append(p.Features, protocol.FeatureContainerRecreate)
	}
	// Its file service applies the manager's file manager limits (#15).
	if slices.Contains(p.Commands, "files.extract") {
		p.Features = append(p.Features, protocol.FeatureFileLimits)
		// ... and follows no symlink in stack scopes.
		p.Features = append(p.Features, protocol.FeatureStackFilesNoFollow)
	}
	// Its backup.run reports live activity when asked (#10).
	if slices.Contains(p.Commands, "backup.run") {
		p.Features = append(p.Features, protocol.FeatureBackupActivity)
		// Its backup.retention removes the backups of deleted items.
		p.Features = append(p.Features, protocol.FeatureBackupExpire)
		// ... and, when asked, every Docker Manager backup of a location (#246).
		p.Features = append(p.Features, protocol.FeatureBackupAnyPolicy)
		// It writes with the destination's compression mode (#10).
		p.Features = append(p.Features, protocol.FeatureBackupCompression)
	}
	// Its container.exec.create resolves terminal shells (#8).
	if a.execShell {
		p.Features = append(p.Features, protocol.FeatureExecShell)
	}
	// Its manager.redirect accepts a secure address at the current
	// generation (after a manager move).
	if a.redirectSecure {
		p.Features = append(p.Features, protocol.FeatureManagerRedirectSecure)
	}
	if c.EngineError != nil {
		p.Diagnostics = append(p.Diagnostics, protocol.Diagnostic{
			Area: protocol.DiagnosticEngine, Code: string(c.EngineError.Code), Message: bound(c.EngineError.Message),
		})
	}
	if r := c.Storage; r != nil {
		for _, root := range r.Roots {
			if root.OK {
				p.Roots = append(p.Roots, protocol.Root{Kind: root.Kind, Path: root.Path, Watch: a.rootWatchMode(root.Path)})
			}
		}
		if r.StacksOK() {
			p.Features = append(p.Features, "stacks")
		}
		for _, d := range r.Diagnostics {
			p.Diagnostics = append(p.Diagnostics, protocol.Diagnostic{
				Area: protocol.DiagnosticStorage, Code: d.Code, Message: bound(d.Message), Path: d.Path,
			})
		}
	}
	// The frame needs the Engine identity: once known it is sent even while
	// the Engine is unreachable (the diagnostics say so).
	if c.Engine == nil {
		return p, false
	}
	id := c.Engine
	// engine.apiVersion is the negotiated version (docs/internal/protocol/agent-v1.md).
	p.Engine = protocol.EngineInfo{
		ID: id.EngineID, Version: id.Version, APIVersion: id.NegotiatedAPIVersion, MinAPIVersion: id.MinAPIVersion,
		OS: id.OS, Arch: id.Arch, Rootless: id.Rootless,
	}
	for _, capa := range id.Capabilities {
		if capa.Supported {
			p.Features = append(p.Features, "engine."+capa.Name)
		}
	}
	return p, true
}

// commands lists the job kinds this agent executes (sorted).
func (a *Agent) commands() []string {
	out := make([]string, 0, len(a.opts.Executors))
	for _, x := range a.opts.Executors {
		out = append(out, string(x.Kind))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// bound truncates a diagnostic message to the protocol limit.
func bound(s string) string {
	if len(s) > protocol.MaxDiagnosticMessage {
		return s[:protocol.MaxDiagnosticMessage]
	}
	return s
}

// Engine returns the connected Engine adapter, or nil.
func (a *Agent) Engine() engine.Engine {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.eng
}

// Compose returns the Compose SDK adapter, or nil before the Engine is
// connected.
func (a *Agent) Compose() *compose.Adapter {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.compose
}

// Run runs the agent until ctx is canceled.
func (a *Agent) Run(ctx context.Context) error {
	cfg, log, clk := a.opts.Config, a.log, a.opts.Clock
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	// The transport validates the TLS trust (system roots plus the optional
	// DOCKER_AGENT_MANAGER_CA_FILE) before anything is sent to the manager.
	configured, err := transport.New(cfg)
	if err != nil {
		return err
	}
	if a.store, err = state.Open(cfg.StateDir); err != nil {
		return err
	}
	// After a manager move the agent dials the address the manager sent
	// (manager.redirect, kept in manager.json) until DOCKER_AGENT_MANAGER_URL
	// is changed.
	tr := a.resolveTransport(configured)
	a.setTransport(tr)
	ti := tr.Info()
	// manager.identity refuses a manager older than the newest one this
	// agent has seen (manager moves, #35); the state directory keeps it.
	a.guard.SetGenerations(a.store)
	installID, err := a.store.InstallID()
	if err != nil {
		return err
	}
	cred, err := a.store.Credential()
	if err != nil {
		return err
	}
	a.setIdentity(cred)
	info0 := buildinfo.Get()
	a.client = session.New(session.Options{
		State: a.store, Clock: clk, Logger: log,
		// Asked before every dial: a manager.redirect switches the address.
		Target:       a.sessionTarget,
		AgentVersion: info0.Version, UserAgent: userAgent(), Capabilities: a.CapabilitiesPayload,
		Requests: a.opts.Requests, Streams: a.opts.Streams, Backoff: a.opts.Backoff, OnStatus: a.onSessionStatus,
		Rescan: a.rescan(),
		// A manager older than the newest one seen is refused at the
		// handshake (manager moves, #35); manager.identity checks again.
		AcceptWelcome: func(w protocol.WelcomePayload) error { return a.guard.AcceptGeneration("", w.Generation) },
	})
	// A helper that recreated this agent (#32) left its result behind.
	selfupdate.Collect(cfg.StateDir, log)
	runner, err := agentjobs.New(ctx, agentjobs.Options{StateDir: cfg.StateDir, Clock: clk, Logger: log.With("component", "jobs"),
		Sender: a.client, Executors: a.opts.Executors, OnFinished: func(ctx context.Context, st jobexec.State) {
			a.self.JobFinished(ctx, st.JobID, st.Outcome.Outcome == jobexec.OutcomeSucceeded)
		}})
	if err != nil {
		return err
	}
	a.client.SetRunner(runner)

	info := buildinfo.Get()
	log.Info("starting docker-agent",
		"version", info.Version, "commit", info.Commit,
		"manager_url", ti.ManagerURL, "manager_url_source", managerURLSource(tr), "docker_host", cfg.DockerHost,
		"environment_name", cfg.EnvironmentName, "state_dir", cfg.StateDir, "install_id", installID,
		"enrolled", cred != nil, "manager_plain_http", ti.PlainHTTP, "manager_custom_ca", ti.CustomCA)
	if tr.Redirected() {
		log.Info("dialing the address Docker Manager gave when it moved to a new server instead of DOCKER_AGENT_MANAGER_URL; "+
			"changing DOCKER_AGENT_MANAGER_URL makes the agent dial that value again", "manager_url", ti.ManagerURL,
			"configured_manager_url", configured.Info().ManagerURL)
	}
	switch {
	case ti.Flagged() && tr.Redirected():
		// Reported to the manager in the capabilities (protocol.TransportInfo,
		// #3) so the host page flags this environment.
		log.Warn("the address Docker Manager gave when it moved uses plain HTTP; once its usual HTTPS address reaches the new " +
			"server, Docker Manager gives the agent that address (or set DOCKER_AGENT_MANAGER_URL to it)")
	case ti.Flagged():
		log.Warn("manager URL uses plain HTTP (DOCKER_AGENT_MANAGER_ALLOW_HTTP=true); only acceptable on the manager's internal Docker network")
	}
	if cred == nil && cfg.EnrollmentToken == "" {
		log.Warn("agent is not enrolled: set DOCKER_AGENT_ENROLLMENT_TOKEN or hand it a token with `docker-agent enroll`")
	}
	defer a.closeEngine()
	var control sync.WaitGroup
	control.Add(1)
	go func() { defer control.Done(); a.control(ctx) }()
	if a.watcher != nil {
		control.Add(1)
		go func() { defer control.Done(); a.watcher.Run(ctx) }()
		defer func() { _ = a.watcher.Close() }()
	}
	if a.sampler != nil {
		relay := observe.NewEventRelay(observe.EventOptions{Clock: clk, Logger: log, Engine: a.observedEngine, Publisher: a.client.Events()})
		control.Add(3)
		go func() { defer control.Done(); a.sampler.Run(ctx) }()
		go func() { defer control.Done(); relay.Run(ctx) }()
		go func() { defer control.Done(); a.health.Run(ctx) }()
	}
	defer func() {
		control.Wait()
		runner.Wait()
	}()

	backoff := EngineRetryMin
	var retry clock.Timer
	var retryC <-chan time.Time
	if !a.connect(ctx) {
		retry = clk.NewTimer(backoff)
		retryC = retry.C()
	}
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()

	// The ticker exists before the first write, so whoever sees that write
	// (tests advancing a fake clock) also sees the next tick scheduled.
	ticker := clk.NewTicker(HealthInterval)
	defer ticker.Stop()
	if err := a.writeHealth(clk.Now()); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			log.Info("docker-agent stopping")
			return nil
		case <-retryC:
			if a.connect(ctx) {
				retryC = nil
				continue
			}
			backoff = min(backoff*2, EngineRetryMax)
			retry.Reset(backoff)
		case now := <-ticker.C():
			a.checkEngine(ctx)
			if err := a.writeHealth(now); err != nil {
				log.Error("could not update health file", "error", err)
			}
		}
	}
}

// connect tries to open the Engine adapter once.
func (a *Agent) connect(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, engineConnectTimeout)
	defer cancel()
	eng, err := a.opts.ConnectEngine(cctx)
	var comp *compose.Adapter
	if err == nil {
		if comp, err = a.opts.ConnectCompose(cctx, eng, a.StackGuard); err != nil {
			_ = eng.Close()
		}
	}
	if err != nil {
		e := &EngineError{Code: engine.CodeOf(err), Message: err.Error()}
		a.mu.Lock()
		changed := a.engErr == nil || a.engErr.Code != e.Code
		a.engErr = e
		a.mu.Unlock()
		if changed {
			a.log.Error("cannot use the Docker Engine; retrying", "code", e.Code, "error", err)
			a.notify()
		}
		return false
	}
	id := eng.Identity()
	a.mu.Lock()
	a.eng, a.compose, a.engErr, a.healthy = eng, comp, nil, true
	a.mu.Unlock()
	defer a.readyOnce.Do(func() { close(a.engineReady) })
	a.log.Info("connected to Docker Engine",
		"engine_id", id.EngineID, "engine_name", id.Name, "engine_version", id.Version,
		"api_version", id.APIVersion, "negotiated_api_version", id.NegotiatedAPIVersion,
		"os", id.OS, "arch", id.Arch, "operating_system", id.OperatingSystem,
		"docker_root_dir", id.DockerRootDir, "storage_driver", id.StorageDriver,
		"rootless", id.Rootless, "docker_desktop", id.DockerDesktop)
	a.verifyStorage(ctx, eng)
	a.notify()
	return true
}

// verifyStorage runs the #28 layout check and logs its outcome. Stack
// operations stay refused (StackGuard) until it passes; everything else
// keeps working.
func (a *Agent) verifyStorage(ctx context.Context, eng engine.Engine) {
	r := a.opts.VerifyStorage(ctx, eng)
	a.mu.Lock()
	a.storage = &r
	a.mu.Unlock()
	var roots []string
	for _, root := range r.Roots {
		if root.OK {
			roots = append(roots, root.Kind+"="+root.Path)
		}
	}
	if r.StacksOK() {
		a.log.Info("storage layout verified", "stacks_dir", r.StacksDir, "docker_root_dir", r.DockerRootDir,
			"verified_roots", roots, "containerized", r.Containerized, "self_container_id", r.SelfContainerID)
	}
	for _, d := range r.Diagnostics {
		msg := "storage layout check failed"
		if !r.StacksOK() {
			msg = "stack operations disabled: storage layout check failed"
		}
		a.log.Error(msg, "code", d.Code, "path", d.Path, "detail", d.Message)
	}
}

// checkEngine pings a connected Engine and refreshes its identity after an
// outage (the Engine may have been upgraded).
func (a *Agent) checkEngine(ctx context.Context) {
	eng := a.Engine()
	if eng == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, enginePingTimeout)
	err := eng.Ping(pctx)
	cancel()
	a.mu.Lock()
	wasHealthy := a.healthy
	a.healthy = err == nil
	if err != nil {
		a.engErr = &EngineError{Code: engine.CodeOf(err), Message: err.Error()}
	}
	a.mu.Unlock()
	switch {
	case err != nil && wasHealthy:
		a.log.Error("lost the Docker Engine", "code", engine.CodeOf(err), "error", err)
		a.notify()
	case err == nil && !wasHealthy:
		id, rerr := eng.Refresh(ctx)
		a.mu.Lock()
		if rerr != nil {
			a.healthy = false
			a.engErr = &EngineError{Code: engine.CodeOf(rerr), Message: rerr.Error()}
		} else {
			a.engErr = nil
		}
		a.mu.Unlock()
		if rerr == nil {
			a.log.Info("Docker Engine is back", "engine_version", id.Version, "negotiated_api_version", id.NegotiatedAPIVersion)
			a.verifyStorage(ctx, eng)
		}
		a.notify()
	}
}

func (a *Agent) notify() {
	if a.opts.OnCapabilities != nil {
		a.opts.OnCapabilities(a.Capabilities())
	}
	if a.client != nil {
		a.client.CapabilitiesChanged()
	}
}

// setStatus records the connection state and refreshes the health file
// when it changed.
func (a *Agent) setStatus(st string) {
	a.mu.Lock()
	changed := a.status != st
	a.status = st
	a.mu.Unlock()
	if changed && a.store != nil {
		if err := a.writeHealthFile(a.opts.Clock.Now()); err != nil {
			a.log.Error("could not update health file", "error", err)
		}
	}
}

// Status returns the connection state (health file value).
func (a *Agent) Status() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *Agent) setIdentity(c *state.Credential) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c == nil {
		a.identity = nil
		return
	}
	id := *c
	id.Credential = "" // never kept outside the state store
	a.identity = &id
}

func (a *Agent) closeEngine() {
	a.mu.Lock()
	eng, comp := a.eng, a.compose
	a.eng, a.compose = nil, nil
	a.mu.Unlock()
	if comp != nil {
		_ = comp.Close()
	}
	if eng != nil {
		_ = eng.Close()
	}
}

func (a *Agent) engineStatus() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch {
	case a.engErr != nil:
		return string(a.engErr.Code)
	case a.eng != nil:
		return "connected"
	}
	return "connecting"
}

func (a *Agent) storageStatus() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch {
	case a.storage == nil:
		return "pending"
	case len(a.storage.Diagnostics) > 0:
		return a.storage.Diagnostics[0].Code
	}
	return "verified"
}

func (a *Agent) writeHealth(now time.Time) error {
	if err := a.writeHealthFile(now); err != nil {
		return err
	}
	if a.opts.afterHealthWrite != nil {
		a.opts.afterHealthWrite(now)
	}
	return nil
}

func (a *Agent) writeHealthFile(now time.Time) error {
	st := HealthState{Status: a.Status(), UpdatedAt: now.UTC(), Version: buildinfo.Get().Version, PID: os.Getpid(),
		Engine: a.engineStatus(), Storage: a.storageStatus()}
	a.mu.RLock()
	if a.identity != nil {
		st.AgentID, st.EnvironmentID = a.identity.AgentID, a.identity.EnvironmentID
	}
	if a.transport != nil {
		st.ManagerURL, st.ManagerURLSource = a.tinfo.ManagerURL, managerURLSource(a.transport)
	}
	a.mu.RUnlock()
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	return writeHealth(a.opts.Config.StateDir, st)
}

// writeHealth atomically replaces the health file.
func writeHealth(stateDir string, st HealthState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".health-*.tmp")
	if err != nil {
		return fmt.Errorf("write health file: %w", err)
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write health file: %w", err)
	}
	// A concurrent reader (`docker-agent enroll`, the healthcheck) can make
	// the rename fail on some platforms for a moment: retry briefly.
	var rerr error
	for range 5 {
		if rerr = os.Rename(tmp.Name(), filepath.Join(stateDir, HealthFileName)); rerr == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = os.Remove(tmp.Name())
	return fmt.Errorf("write health file: %w", rerr)
}

// CheckHealth returns nil when the health file exists and was updated
// within maxAge of now.
func CheckHealth(stateDir string, now time.Time, maxAge time.Duration) error {
	b, err := os.ReadFile(filepath.Join(stateDir, HealthFileName)) //nolint:gosec // operator-configured path
	if err != nil {
		return fmt.Errorf("read health file: %w", err)
	}
	var st HealthState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("parse health file: %w", err)
	}
	if age := now.Sub(st.UpdatedAt); age > maxAge {
		return fmt.Errorf("agent health file is stale (last update %s ago)", age.Round(time.Second))
	}
	return nil
}
