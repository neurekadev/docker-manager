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
// with a one-use token (DOCKYARD_ENROLLMENT_TOKEN(_FILE) or one handed over
// by `dockyard-agent enroll` through the state directory), stores the
// credential (internal/agent/state) and keeps the manager session up
// (internal/agent/session) with the job runner (internal/agent/jobs) wired
// in. The loop reports liveness and the connection state through a health
// file in the state directory, which `dockyard-agent healthcheck` checks
// for freshness.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	agentjobs "github.com/neurekadev/dockyard/internal/agent/jobs"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/state"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/agent/transport"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/protocol"
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
var ErrNotRoot = errors.New("dockyard-agent must run as root (UID 0): it needs to read and write container-owned files " +
	"in Docker volumes for browsing, backup and restore. Running DockYard containers as a non-root user is not supported " +
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
	// (docs/protocol/agent-v1.md, "Allowed requests").
	Requests map[string]session.RequestHandler
	// TokenPoll is how often a handed-over enrollment token is looked for
	// (default DefaultTokenPoll).
	TokenPoll time.Duration
	// Backoff overrides the session reconnect policy (tests).
	Backoff session.Backoff

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

	healthMu    sync.Mutex
	store       *state.Store
	client      *session.Client
	engineReady chan struct{}
	readyOnce   sync.Once
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
	return &Agent{opts: opts, log: opts.Logger, status: StatusNotEnrolled, engineReady: make(chan struct{})}, nil
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
		Commands:     []string{},
		Requests:     []string{},
		Streams:      []string{},
		Transport:    ti,
	}
	if c.EngineError != nil {
		p.Diagnostics = append(p.Diagnostics, protocol.Diagnostic{
			Area: protocol.DiagnosticEngine, Code: string(c.EngineError.Code), Message: bound(c.EngineError.Message),
		})
	}
	if r := c.Storage; r != nil {
		for _, root := range r.Roots {
			if root.OK {
				p.Roots = append(p.Roots, protocol.Root{Kind: root.Kind, Path: root.Path, Watch: "inotify"})
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
	// engine.apiVersion is the negotiated version (docs/protocol/agent-v1.md).
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
	// DOCKYARD_MANAGER_CA_FILE) before anything is sent to the manager.
	tr, err := transport.New(cfg)
	if err != nil {
		return err
	}
	ti := tr.Info()
	a.mu.Lock()
	a.transport, a.tinfo = tr, ti
	a.mu.Unlock()
	if a.store, err = state.Open(cfg.StateDir); err != nil {
		return err
	}
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
		State: a.store, Clock: clk, Logger: log, URL: tr.WebSocketURL(protocol.SessionPath),
		DialOptions:  func(h http.Header) *websocket.DialOptions { return tr.DialOptions(h, protocol.Version) },
		AgentVersion: info0.Version, UserAgent: userAgent(), Capabilities: a.CapabilitiesPayload,
		Requests: a.opts.Requests, Backoff: a.opts.Backoff, OnStatus: a.onSessionStatus,
	})
	runner, err := agentjobs.New(ctx, agentjobs.Options{StateDir: cfg.StateDir, Clock: clk, Logger: log.With("component", "jobs"),
		Sender: a.client, Executors: a.opts.Executors})
	if err != nil {
		return err
	}
	a.client.SetRunner(runner)

	info := buildinfo.Get()
	log.Info("starting dockyard-agent",
		"version", info.Version, "commit", info.Commit,
		"manager_url", cfg.ManagerURL.String(), "docker_host", cfg.DockerHost,
		"environment_name", cfg.EnvironmentName, "state_dir", cfg.StateDir, "install_id", installID,
		"enrolled", cred != nil, "manager_plain_http", ti.PlainHTTP, "manager_custom_ca", ti.CustomCA)
	if ti.Flagged() {
		// Reported to the manager in the capabilities (protocol.TransportInfo,
		// #3) so the host page flags this environment.
		log.Warn("manager URL uses plain HTTP (DOCKYARD_MANAGER_ALLOW_HTTP=true); only acceptable on the manager's internal Docker network")
	}
	if cred == nil && cfg.EnrollmentToken == "" {
		log.Warn("agent is not enrolled: set DOCKYARD_ENROLLMENT_TOKEN or hand it a token with `dockyard-agent enroll`")
	}
	defer a.closeEngine()
	var control sync.WaitGroup
	control.Add(1)
	go func() { defer control.Done(); a.control(ctx) }()
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

	if err := a.writeHealth(clk.Now()); err != nil {
		return err
	}
	ticker := clk.NewTicker(HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("dockyard-agent stopping")
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
	// A concurrent reader (`dockyard-agent enroll`, the healthcheck) can make
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
