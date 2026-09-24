// Package runtime is the agent's main loop.
//
// The agent is an outbound-only connector: it dials the manager and never
// opens a listening socket (enforced by nolisten_test.go). It must run as
// root (UID 0, #28). At startup it connects to the local Docker Engine
// through the Moby adapter (internal/agent/engine, #21), negotiates the API
// version and logs the Engine identity; Capabilities() exposes that
// identity for the hello/capabilities frame of the session protocol (#3).
// Until enrollment and the session protocol land (#3) the loop also reports
// liveness through a health file in the state directory, which
// `dockyard-agent healthcheck` checks for freshness.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/compose"
	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/transport"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
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

// Agent status values written to the health file.
const (
	StatusNotEnrolled = "not_enrolled"
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
	// connected; default compose.New on Config.DockerHost.
	ConnectCompose func(ctx context.Context, eng engine.Engine) (*compose.Adapter, error)
	// OnCapabilities is called whenever Capabilities change (Engine
	// connected, lost or recovered); the session transport (#3) uses it to
	// send an updated capabilities frame. It must not block.
	OnCapabilities func(Capabilities)

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
}

// Capabilities is what the agent reports to the manager in its
// hello/capabilities frame (#3). Engine is nil until the Engine was
// reached; EngineError then explains why.
type Capabilities struct {
	AgentVersion string
	AgentCommit  string
	Engine       *engine.Identity
	EngineError  *EngineError
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
	transport protocol.TransportInfo
	eng       engine.Engine
	compose   *compose.Adapter
	engErr    *EngineError
	healthy   bool // the Engine answered the last ping
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
		opts.ConnectCompose = func(ctx context.Context, eng engine.Engine) (*compose.Adapter, error) {
			return compose.New(ctx, compose.Options{Host: host, Engine: eng, Logger: logger})
		}
	}
	return &Agent{opts: opts, log: opts.Logger}, nil
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
	return c
}

// CapabilitiesPayload maps Capabilities to the protocol's capabilities
// frame (#3). ok is false while the Engine is not usable: the frame needs
// the Engine ID and API version. Commands, requests, streams and roots are
// filled in by the workstreams that implement them.
func (a *Agent) CapabilitiesPayload() (protocol.CapabilitiesPayload, bool) {
	c := a.Capabilities()
	a.mu.RLock()
	ti := a.transport
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
	if c.Engine == nil || c.EngineError != nil {
		return p, false
	}
	id := c.Engine
	p.Engine = protocol.EngineInfo{
		ID: id.EngineID, Version: id.Version, APIVersion: id.APIVersion, MinAPIVersion: id.MinAPIVersion,
		OS: id.OS, Arch: id.Arch, Rootless: id.Rootless,
	}
	for _, capa := range id.Capabilities {
		if capa.Supported {
			p.Features = append(p.Features, "engine."+capa.Name)
		}
	}
	return p, true
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
	a.transport = ti
	a.mu.Unlock()

	info := buildinfo.Get()
	log.Info("starting dockyard-agent",
		"version", info.Version, "commit", info.Commit,
		"manager_url", cfg.ManagerURL.String(), "docker_host", cfg.DockerHost,
		"environment_name", cfg.EnvironmentName, "state_dir", cfg.StateDir,
		"manager_plain_http", ti.PlainHTTP, "manager_custom_ca", ti.CustomCA)
	if ti.Flagged() {
		// Reported to the manager in the capabilities (protocol.TransportInfo,
		// #3) so the host page flags this environment.
		log.Warn("manager URL uses plain HTTP (DOCKYARD_MANAGER_ALLOW_HTTP=true); only acceptable on the manager's internal Docker network")
	}
	// TODO(#3): enroll with the one-use token (if not yet enrolled), persist
	// the agent credential in the state dir, dial /agent/v1/session, send
	// Capabilities() in the hello frame and run the protocol.
	// TODO(#28): verify the identical-path volume mount.
	if cfg.EnrollmentToken != "" {
		log.Warn("agent is not enrolled: an enrollment token is configured, but enrollment arrives with #3")
	} else {
		log.Warn("agent is not enrolled: no enrollment token configured; enrollment arrives with #3")
	}
	defer a.closeEngine()

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
		if comp, err = a.opts.ConnectCompose(cctx, eng); err != nil {
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
	a.log.Info("connected to Docker Engine",
		"engine_id", id.EngineID, "engine_name", id.Name, "engine_version", id.Version,
		"api_version", id.APIVersion, "negotiated_api_version", id.NegotiatedAPIVersion,
		"os", id.OS, "arch", id.Arch, "operating_system", id.OperatingSystem,
		"docker_root_dir", id.DockerRootDir, "storage_driver", id.StorageDriver,
		"rootless", id.Rootless, "docker_desktop", id.DockerDesktop)
	a.notify()
	return true
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
		}
		a.notify()
	}
}

func (a *Agent) notify() {
	if a.opts.OnCapabilities != nil {
		a.opts.OnCapabilities(a.Capabilities())
	}
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

func (a *Agent) writeHealth(now time.Time) error {
	if err := writeHealth(a.opts.Config.StateDir, HealthState{
		Status: StatusNotEnrolled, UpdatedAt: now.UTC(), Version: buildinfo.Get().Version, PID: os.Getpid(), Engine: a.engineStatus(),
	}); err != nil {
		return err
	}
	if a.opts.afterHealthWrite != nil {
		a.opts.afterHealthWrite(now)
	}
	return nil
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
	if err := os.Rename(tmp.Name(), filepath.Join(stateDir, HealthFileName)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write health file: %w", err)
	}
	return nil
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
