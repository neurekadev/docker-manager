// Package runtime is the agent's main loop.
//
// The agent is an outbound-only connector: it dials the manager and never
// opens a listening socket (enforced by nolisten_test.go). It must run as
// root (UID 0, #28). Until enrollment and the session protocol land (#3) the
// loop only reports liveness through a health file in the state directory,
// which `dockyard-agent healthcheck` checks for freshness.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/clock"
)

// Health file settings.
const (
	HealthFileName = "health.json"
	HealthInterval = 15 * time.Second
	// HealthMaxAge is how stale the health file may be before the agent is
	// considered unhealthy (several missed updates).
	HealthMaxAge = 4 * HealthInterval
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

	afterHealthWrite func(time.Time) // test hook
}

// HealthState is the content of the health file.
type HealthState struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
}

// Run runs the agent until ctx is canceled.
func Run(ctx context.Context, opts Options) error {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Geteuid == nil {
		opts.Geteuid = os.Geteuid
	}
	if uid := opts.Geteuid(); uid != 0 {
		return fmt.Errorf("%w (current UID %d)", ErrNotRoot, uid)
	}
	cfg, log := opts.Config, opts.Logger

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	info := buildinfo.Get()
	log.Info("starting dockyard-agent",
		"version", info.Version, "commit", info.Commit,
		"manager_url", cfg.ManagerURL.String(), "docker_host", cfg.DockerHost,
		"environment_name", cfg.EnvironmentName, "state_dir", cfg.StateDir)
	if cfg.PlainHTTP {
		log.Warn("manager URL uses plain HTTP (DOCKYARD_MANAGER_ALLOW_HTTP=true); only acceptable on the manager's internal Docker network")
	}
	// TODO(#3): enroll with the one-use token (if not yet enrolled), persist
	// the agent credential in the state dir, dial /agent/v1/session and run
	// the protocol. TODO(#28): verify the identical-path volume mount.
	if cfg.EnrollmentToken != "" {
		log.Warn("agent is not enrolled: an enrollment token is configured, but enrollment arrives with #3")
	} else {
		log.Warn("agent is not enrolled: no enrollment token configured; enrollment arrives with #3")
	}

	if err := writeHealth(cfg.StateDir, opts.Clock.Now(), info.Version); err != nil {
		return err
	}
	if opts.afterHealthWrite != nil {
		opts.afterHealthWrite(opts.Clock.Now())
	}
	ticker := opts.Clock.NewTicker(HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("dockyard-agent stopping")
			return nil
		case now := <-ticker.C():
			if err := writeHealth(cfg.StateDir, now, info.Version); err != nil {
				log.Error("could not update health file", "error", err)
				continue
			}
			if opts.afterHealthWrite != nil {
				opts.afterHealthWrite(now)
			}
		}
	}
}

// writeHealth atomically replaces the health file.
func writeHealth(stateDir string, now time.Time, version string) error {
	b, err := json.Marshal(HealthState{Status: StatusNotEnrolled, UpdatedAt: now.UTC(), Version: version, PID: os.Getpid()})
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
