// Package config loads and validates the manager's environment configuration.
// Every variable is documented in docs/configuration.md; keep both in sync.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/server/sse"
)

// Environment variable names.
const (
	EnvPublicURL       = "DOCKYARD_PUBLIC_URL"
	EnvListenAddr      = "DOCKYARD_LISTEN_ADDR"
	EnvDataDir         = "DOCKYARD_DATA_DIR"
	EnvSecretKeyFile   = "DOCKYARD_SECRET_KEY_FILE"
	EnvLogLevel        = "DOCKYARD_LOG_LEVEL"
	EnvLogFormat       = "DOCKYARD_LOG_FORMAT"
	EnvTrustedProxies  = "DOCKYARD_TRUSTED_PROXIES"
	EnvStreamHeartbeat = "DOCKYARD_STREAM_HEARTBEAT"

	EnvSessionIdleTimeout = "DOCKYARD_SESSION_IDLE_TIMEOUT"
	EnvSessionLifetime    = "DOCKYARD_SESSION_LIFETIME"

	EnvJobHistoryRetention   = "DOCKYARD_JOB_HISTORY_RETENTION"
	EnvJobHistoryMax         = "DOCKYARD_JOB_HISTORY_MAX"
	EnvJobEventsMax          = "DOCKYARD_JOB_EVENTS_MAX"
	EnvJobMaxConcurrentPulls = "DOCKYARD_JOB_MAX_CONCURRENT_PULLS"
	EnvJobMaxConcurrentBuild = "DOCKYARD_JOB_MAX_CONCURRENT_BUILDS"
)

// Defaults.
const (
	DefaultListenAddr = ":8080"
	DefaultDataDir    = "/var/lib/dockyard"
	SecretKeyFileName = "secret.key"
	DatabaseFileName  = "dockyard.db"
	SnapshotDirName   = "snapshots"

	DefaultJobHistoryRetention   = 30 * 24 * time.Hour
	DefaultJobHistoryMax         = 10000
	DefaultJobEventsMax          = 500
	DefaultJobMaxConcurrentPulls = 2
	DefaultJobMaxConcurrentBuild = 1

	// Browser session limits (#16): NIST SP 800-63B AAL2 reauthentication.
	DefaultSessionIdleTimeout = time.Hour
	DefaultSessionLifetime    = 24 * time.Hour
)

// SessionsConfig bounds browser sessions (#16).
type SessionsConfig struct {
	// IdleTimeout ends a session after this much inactivity.
	IdleTimeout time.Duration
	// Lifetime ends a session this long after sign-in, whatever the activity.
	Lifetime time.Duration
}

// JobsConfig bounds the job engine (#26). Job history retention is
// separate from audit retention (#30).
type JobsConfig struct {
	// HistoryRetention deletes finished jobs (and their events) older than this.
	HistoryRetention time.Duration
	// HistoryMax keeps at most this many finished jobs.
	HistoryMax int
	// EventsMax bounds each job's progress/event log.
	EventsMax int
	// MaxConcurrentPulls and MaxConcurrentBuilds cap pull/build jobs per environment.
	MaxConcurrentPulls  int
	MaxConcurrentBuilds int
}

// Config is the validated manager configuration.
type Config struct {
	// PublicURL is the single public origin (scheme://host[:port]) without a
	// trailing slash. It is https except for local development hosts.
	PublicURL *url.URL
	// LocalDevelopment is true when PublicURL is plain http on a loopback host.
	LocalDevelopment bool
	ListenAddr       string
	DataDir          string
	SecretKeyFile    string
	LogLevel         slog.Level
	LogFormat        string
	// TrustedProxies are the peers whose X-Forwarded-* headers are honored (#27).
	TrustedProxies []netip.Prefix
	// StreamHeartbeat is the SSE heartbeat and WebSocket ping interval; it
	// must stay below the reverse proxy's idle/read timeout (#27).
	StreamHeartbeat time.Duration
	Jobs            JobsConfig
	Sessions        SessionsConfig
}

// DatabasePath is the SQLite database file inside the data directory.
func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, DatabaseFileName) }

// SnapshotDir holds pre-migration database snapshots.
func (c Config) SnapshotDir() string { return filepath.Join(c.DataDir, SnapshotDirName) }

// Load reads and validates the configuration. All problems are reported
// together so operators can fix them in one pass.
func Load(src envconfig.Source) (Config, error) {
	var errs []error
	cfg := Config{
		ListenAddr: src.String(EnvListenAddr, DefaultListenAddr),
	}

	pub, dev, err := ParsePublicURL(src.String(EnvPublicURL, ""))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.PublicURL, cfg.LocalDevelopment = pub, dev

	if _, _, err := net.SplitHostPort(cfg.ListenAddr); err != nil {
		errs = append(errs, fmt.Errorf("%s: invalid listen address %q (want host:port, e.g. :8080)", EnvListenAddr, cfg.ListenAddr))
	}

	dataDir := src.String(EnvDataDir, DefaultDataDir)
	if abs, err := filepath.Abs(dataDir); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvDataDir, err))
	} else {
		cfg.DataDir = abs
	}
	if strings.ContainsAny(cfg.DataDir, "?#") {
		errs = append(errs, fmt.Errorf("%s: path must not contain '?' or '#'", EnvDataDir))
	}

	cfg.SecretKeyFile = src.String(EnvSecretKeyFile, filepath.Join(cfg.DataDir, SecretKeyFileName))
	if abs, err := filepath.Abs(cfg.SecretKeyFile); err == nil {
		cfg.SecretKeyFile = abs
	}

	if cfg.LogLevel, err = logging.ParseLevel(src.String(EnvLogLevel, "info")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogLevel, err))
	}
	if cfg.LogFormat, err = logging.ParseFormat(src.String(EnvLogFormat, logging.FormatJSON)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogFormat, err))
	}

	if cfg.TrustedProxies, err = ParseTrustedProxies(src.String(EnvTrustedProxies, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvTrustedProxies, err))
	}

	if cfg.StreamHeartbeat, err = src.Duration(EnvStreamHeartbeat, sse.DefaultHeartbeat, sse.MinHeartbeat, sse.MaxHeartbeat); err != nil {
		errs = append(errs, err)
	}

	cfg.Jobs, err = loadJobs(src)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.Sessions, err = loadSessions(src)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func loadJobs(src envconfig.Source) (JobsConfig, error) {
	var c JobsConfig
	var errs [5]error
	c.HistoryRetention, errs[0] = src.Duration(EnvJobHistoryRetention, DefaultJobHistoryRetention, time.Hour, 10*365*24*time.Hour)
	c.HistoryMax, errs[1] = src.Int(EnvJobHistoryMax, DefaultJobHistoryMax, 100, 10_000_000)
	c.EventsMax, errs[2] = src.Int(EnvJobEventsMax, DefaultJobEventsMax, 10, 100_000)
	c.MaxConcurrentPulls, errs[3] = src.Int(EnvJobMaxConcurrentPulls, DefaultJobMaxConcurrentPulls, 1, 64)
	c.MaxConcurrentBuilds, errs[4] = src.Int(EnvJobMaxConcurrentBuild, DefaultJobMaxConcurrentBuild, 1, 64)
	return c, errors.Join(errs[:]...)
}

func loadSessions(src envconfig.Source) (SessionsConfig, error) {
	var c SessionsConfig
	var errs [2]error
	c.IdleTimeout, errs[0] = src.Duration(EnvSessionIdleTimeout, DefaultSessionIdleTimeout, 5*time.Minute, 7*24*time.Hour)
	c.Lifetime, errs[1] = src.Duration(EnvSessionLifetime, DefaultSessionLifetime, 15*time.Minute, 30*24*time.Hour)
	if err := errors.Join(errs[:]...); err != nil {
		return c, err
	}
	if c.IdleTimeout > c.Lifetime {
		return c, fmt.Errorf("%s (%s) must not exceed %s (%s)", EnvSessionIdleTimeout, c.IdleTimeout, EnvSessionLifetime, c.Lifetime)
	}
	return c, nil
}

// ParsePublicURL validates DOCKYARD_PUBLIC_URL. It must be an absolute origin
// (no path, query, fragment or credentials). Plain http is only accepted for
// localhost/loopback development, reported by the second return value.
func ParsePublicURL(raw string) (*url.URL, bool, error) {
	if raw == "" {
		return nil, false, fmt.Errorf("%s is required (the public HTTPS origin, e.g. https://docker.example.com)", EnvPublicURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: invalid URL: %w", EnvPublicURL, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, false, fmt.Errorf("%s: scheme must be https (got %q)", EnvPublicURL, u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, false, fmt.Errorf("%s: missing host", EnvPublicURL)
	}
	if u.User != nil {
		return nil, false, fmt.Errorf("%s: must not contain credentials", EnvPublicURL)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, false, fmt.Errorf("%s: must not contain a query or fragment", EnvPublicURL)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, false, fmt.Errorf("%s: must be an origin without a path; serving DockYard under a sub-path is not supported", EnvPublicURL)
	}
	dev := false
	if u.Scheme == "http" {
		if !isLocalHost(u.Hostname()) {
			return nil, false, fmt.Errorf("%s: must use https; plain http is only allowed for localhost development (got host %q)", EnvPublicURL, u.Hostname())
		}
		dev = true
	}
	out := &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}
	return out, dev, nil
}

func isLocalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// ParseTrustedProxies parses a comma- or space-separated list of CIDRs or
// single IP addresses.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	out := make([]netip.Prefix, 0, len(fields))
	for _, f := range fields {
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", f, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q: %w", f, err)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}
