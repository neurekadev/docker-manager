// Package config loads and validates the manager's environment configuration.
// Every variable is documented in docs/internal/configuration.md; keep both in sync.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/envconfig"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/server/sse"
	"code.neureka.dev/docker-manager/docker-manager/internal/transfer"
)

// Environment variable names.
const (
	EnvPublicURL       = "DOCKER_MANAGER_PUBLIC_URL"
	EnvListenAddr      = "DOCKER_MANAGER_LISTEN_ADDR"
	EnvDataDir         = "DOCKER_MANAGER_DATA_DIR"
	EnvSecretKeyFile   = "DOCKER_MANAGER_SECRET_KEY_FILE" //nolint:gosec // G101: a variable name, not a credential
	EnvLogLevel        = "DOCKER_MANAGER_LOG_LEVEL"
	EnvLogFormat       = "DOCKER_MANAGER_LOG_FORMAT"
	EnvTrustedProxies  = "DOCKER_MANAGER_TRUSTED_PROXIES"
	EnvStreamHeartbeat = "DOCKER_MANAGER_STREAM_HEARTBEAT"

	EnvSessionIdleTimeout = "DOCKER_MANAGER_SESSION_IDLE_TIMEOUT"
	EnvSessionLifetime    = "DOCKER_MANAGER_SESSION_LIFETIME"

	EnvJobHistoryRetention   = "DOCKER_MANAGER_JOB_HISTORY_RETENTION"
	EnvJobHistoryMax         = "DOCKER_MANAGER_JOB_HISTORY_MAX"
	EnvJobEventsMax          = "DOCKER_MANAGER_JOB_EVENTS_MAX"
	EnvJobMaxConcurrentPulls = "DOCKER_MANAGER_JOB_MAX_CONCURRENT_PULLS"
	EnvJobMaxConcurrentBuild = "DOCKER_MANAGER_JOB_MAX_CONCURRENT_BUILDS"

	EnvAuditRetentionDays = "DOCKER_MANAGER_AUDIT_RETENTION_DAYS"
	EnvAuditMaxSizeMB     = "DOCKER_MANAGER_AUDIT_MAX_SIZE_MB"
	EnvAuditLogMirror     = "DOCKER_MANAGER_AUDIT_LOG_MIRROR"

	EnvMetricsRetentionRaw     = "DOCKER_MANAGER_METRICS_RETENTION_RAW"
	EnvMetricsRetentionMinute  = "DOCKER_MANAGER_METRICS_RETENTION_1M"
	EnvMetricsRetentionQuarter = "DOCKER_MANAGER_METRICS_RETENTION_15M"
	EnvMetricsMaxSizeMB        = "DOCKER_MANAGER_METRICS_MAX_SIZE_MB"
	EnvMetricsMaxSeries        = "DOCKER_MANAGER_METRICS_MAX_SERIES"

	EnvFilesMaxUploadMB = "DOCKER_MANAGER_FILES_MAX_UPLOAD_MB"

	EnvMigrationBandwidthLimit = "DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT"
	// Backups (#10).
	EnvBackupLocalRoots = "DOCKER_MANAGER_BACKUP_LOCAL_ROOTS"
	EnvResticBinary     = "DOCKER_MANAGER_RESTIC_BINARY"
	// Diagnostics (#34): the Prometheus endpoint of Docker Manager's own
	// internals (not the host metrics of #5, which are always collected).
	EnvMetricsEnabled = "DOCKER_MANAGER_METRICS_ENABLED"
)

// Defaults.
const (
	DefaultListenAddr = ":8080"
	DefaultDataDir    = "/var/lib/docker-manager"
	SecretKeyFileName = "secret.key"
	DatabaseFileName  = "docker-manager.db"
	SnapshotDirName   = "snapshots"

	DefaultJobHistoryRetention   = 30 * 24 * time.Hour
	DefaultJobHistoryMax         = 10000
	DefaultJobEventsMax          = 500
	DefaultJobMaxConcurrentPulls = 2
	DefaultJobMaxConcurrentBuild = 1

	// Browser session limits (#16): NIST SP 800-63B AAL2 reauthentication.
	DefaultSessionIdleTimeout = time.Hour
	DefaultSessionLifetime    = 24 * time.Hour

	DefaultAuditRetentionDays = 365
	DefaultAuditMaxSizeMB     = 1024

	// Metrics (#5): a separate database file, excluded from manager-state
	// backups by default (#10).
	MetricsFileName                = "metrics.db"
	DefaultMetricsRetentionRaw     = 24 * time.Hour
	DefaultMetricsRetentionMinute  = 7 * 24 * time.Hour
	DefaultMetricsRetentionQuarter = 90 * 24 * time.Hour
	DefaultMetricsMaxSizeMB        = 2048
	DefaultMetricsMaxSeries        = 5000

	// DefaultFilesMaxUploadMB is also the agents' own upper bound (#15).
	DefaultFilesMaxUploadMB = 2048
)

// MetricsConfig bounds the metrics database (#5).
type MetricsConfig struct {
	// RetentionRaw, RetentionMinute and RetentionQuarter keep 10 s samples,
	// 1 min and 15 min rollups this long.
	RetentionRaw     time.Duration
	RetentionMinute  time.Duration
	RetentionQuarter time.Duration
	// MaxBytes caps the database; retention shortens above it.
	MaxBytes int64
	// MaxSeries caps the stored series (hosts, filesystems, containers).
	MaxSeries int
}

// SessionsConfig bounds browser sessions (#16).
type SessionsConfig struct {
	// IdleTimeout ends a session after this much inactivity.
	IdleTimeout time.Duration
	// Lifetime ends a session this long after sign-in, whatever the activity.
	Lifetime time.Duration
}

// AuditConfig bounds the audit trail (#30).
type AuditConfig struct {
	// RetentionDays deletes audit records older than this many days.
	RetentionDays int
	// MaxBytes caps the retained records' size; the oldest are purged first.
	MaxBytes int64
	// LogMirror also writes every audit record to the structured log.
	LogMirror bool
}

// Retention is RetentionDays as a duration.
func (a AuditConfig) Retention() time.Duration {
	return time.Duration(a.RetentionDays) * 24 * time.Hour
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
	Audit           AuditConfig
	Metrics         MetricsConfig
	// FilesMaxUpload bounds one file-manager upload in bytes (#15); the
	// reverse proxy's body limit must allow it (#27).
	FilesMaxUpload int64
	// MigrationBandwidthLimit caps the data environment migrations relay
	// through the manager, in bytes per second (#35; 0: unlimited).
	MigrationBandwidthLimit int64
	// BackupLocalRoots are the directories local backup repositories on
	// the manager may live in (#10); ResticBinary the pinned restic.
	BackupLocalRoots []string
	ResticBinary     string
	// MetricsEnabled serves GET /api/v1/system/metrics (#34; default off).
	MetricsEnabled bool
}

// Setting is one effective configuration value for diagnostics (#34).
type Setting struct {
	Name  string
	Value string
}

// Settings lists the effective configuration by environment variable for
// the support bundle (#34). Config holds no secret values (the secret key
// is a file: only its path is listed).
func (c Config) Settings() []Setting {
	public := ""
	if c.PublicURL != nil {
		public = c.PublicURL.String()
	}
	proxies := make([]string, 0, len(c.TrustedProxies))
	for _, p := range c.TrustedProxies {
		proxies = append(proxies, p.String())
	}
	d := func(v time.Duration) string { return v.String() }
	i := func(v int) string { return strconv.Itoa(v) }
	mb := func(v int64) string { return strconv.FormatInt(v>>20, 10) }
	return []Setting{
		{EnvPublicURL, public},
		{EnvListenAddr, c.ListenAddr},
		{EnvDataDir, c.DataDir},
		{EnvSecretKeyFile, c.SecretKeyFile + " (path only)"},
		{EnvLogLevel, c.LogLevel.String()},
		{EnvLogFormat, c.LogFormat},
		{EnvTrustedProxies, strings.Join(proxies, ",")},
		{EnvStreamHeartbeat, d(c.StreamHeartbeat)},
		{EnvSessionIdleTimeout, d(c.Sessions.IdleTimeout)},
		{EnvSessionLifetime, d(c.Sessions.Lifetime)},
		{EnvJobHistoryRetention, d(c.Jobs.HistoryRetention)},
		{EnvJobHistoryMax, i(c.Jobs.HistoryMax)},
		{EnvJobEventsMax, i(c.Jobs.EventsMax)},
		{EnvJobMaxConcurrentPulls, i(c.Jobs.MaxConcurrentPulls)},
		{EnvJobMaxConcurrentBuild, i(c.Jobs.MaxConcurrentBuilds)},
		{EnvAuditRetentionDays, i(c.Audit.RetentionDays)},
		{EnvAuditMaxSizeMB, mb(c.Audit.MaxBytes)},
		{EnvAuditLogMirror, strconv.FormatBool(c.Audit.LogMirror)},
		{EnvMetricsRetentionRaw, d(c.Metrics.RetentionRaw)},
		{EnvMetricsRetentionMinute, d(c.Metrics.RetentionMinute)},
		{EnvMetricsRetentionQuarter, d(c.Metrics.RetentionQuarter)},
		{EnvMetricsMaxSizeMB, mb(c.Metrics.MaxBytes)},
		{EnvMetricsMaxSeries, i(c.Metrics.MaxSeries)},
		{EnvFilesMaxUploadMB, mb(c.FilesMaxUpload)},
		{EnvMigrationBandwidthLimit, strconv.FormatInt(c.MigrationBandwidthLimit, 10) + " B/s (0: unlimited)"},
		{EnvBackupLocalRoots, strings.Join(c.BackupLocalRoots, ",")},
		{EnvResticBinary, c.ResticBinary},
		{EnvMetricsEnabled, strconv.FormatBool(c.MetricsEnabled)},
		{"local_development", strconv.FormatBool(c.LocalDevelopment)},
	}
}

// DefaultResticBinary is where the image installs restic (#10).
const DefaultResticBinary = "/usr/local/bin/restic"

// ResticCacheDir is restic's cache inside the data directory.
func (c Config) ResticCacheDir() string { return filepath.Join(c.DataDir, "restic-cache") }

// ResticTempDir holds restic's temporary files inside the data directory.
func (c Config) ResticTempDir() string { return filepath.Join(c.DataDir, "tmp") }

// parseRoots parses a comma-separated list of absolute, distinct
// directories other than "/".
func parseRoots(raw string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.HasPrefix(f, "/") || strings.Contains(f, "\\") || strings.Contains("/"+f+"/", "/../") {
			return nil, fmt.Errorf("%q must be an absolute path without \"..\"", f)
		}
		c := path.Clean(f)
		if c == "/" {
			return nil, fmt.Errorf("%q: the filesystem root is not allowed", f)
		}
		for _, o := range out {
			if o == c {
				return nil, fmt.Errorf("%q is listed twice", c)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// DatabasePath is the SQLite database file inside the data directory.
func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, DatabaseFileName) }

// MetricsPath is the metrics database file inside the data directory (#5).
func (c Config) MetricsPath() string { return filepath.Join(c.DataDir, MetricsFileName) }

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

	cfg.Audit, err = loadAudit(src)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.Metrics, err = loadMetrics(src)
	if err != nil {
		errs = append(errs, err)
	}

	uploadMB, err := src.Int(EnvFilesMaxUploadMB, DefaultFilesMaxUploadMB, 1, DefaultFilesMaxUploadMB)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.FilesMaxUpload = int64(uploadMB) << 20

	if cfg.MigrationBandwidthLimit, err = transfer.ParseRate(src.String(EnvMigrationBandwidthLimit, "0")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvMigrationBandwidthLimit, err))
	}

	if cfg.BackupLocalRoots, err = parseRoots(src.String(EnvBackupLocalRoots, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvBackupLocalRoots, err))
	}
	if cfg.MetricsEnabled, err = src.Bool(EnvMetricsEnabled, false); err != nil {
		errs = append(errs, err)
	}
	cfg.ResticBinary = src.String(EnvResticBinary, DefaultResticBinary)
	if !filepath.IsAbs(cfg.ResticBinary) && !strings.HasPrefix(cfg.ResticBinary, "/") {
		errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", EnvResticBinary, cfg.ResticBinary))
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

func loadAudit(src envconfig.Source) (AuditConfig, error) {
	var c AuditConfig
	var errs [3]error
	c.RetentionDays, errs[0] = src.Int(EnvAuditRetentionDays, DefaultAuditRetentionDays, 1, 36500)
	mb, err := src.Int(EnvAuditMaxSizeMB, DefaultAuditMaxSizeMB, 16, 1<<20)
	c.MaxBytes, errs[1] = int64(mb)<<20, err
	c.LogMirror, errs[2] = src.Bool(EnvAuditLogMirror, false)
	return c, errors.Join(errs[:]...)
}

func loadMetrics(src envconfig.Source) (MetricsConfig, error) {
	var c MetricsConfig
	var errs [5]error
	day := 24 * time.Hour
	c.RetentionRaw, errs[0] = src.Duration(EnvMetricsRetentionRaw, DefaultMetricsRetentionRaw, time.Hour, 7*day)
	c.RetentionMinute, errs[1] = src.Duration(EnvMetricsRetentionMinute, DefaultMetricsRetentionMinute, day, 90*day)
	c.RetentionQuarter, errs[2] = src.Duration(EnvMetricsRetentionQuarter, DefaultMetricsRetentionQuarter, 7*day, 5*365*day)
	mb, err := src.Int(EnvMetricsMaxSizeMB, DefaultMetricsMaxSizeMB, 64, 1<<20)
	c.MaxBytes, errs[3] = int64(mb)<<20, err
	c.MaxSeries, errs[4] = src.Int(EnvMetricsMaxSeries, DefaultMetricsMaxSeries, 100, 1_000_000)
	if err := errors.Join(errs[:]...); err != nil {
		return c, err
	}
	if c.RetentionRaw > c.RetentionMinute || c.RetentionMinute > c.RetentionQuarter {
		return c, fmt.Errorf("%s <= %s <= %s is required (finer levels are rolled up into coarser ones)",
			EnvMetricsRetentionRaw, EnvMetricsRetentionMinute, EnvMetricsRetentionQuarter)
	}
	return c, nil
}

// ParsePublicURL validates DOCKER_MANAGER_PUBLIC_URL. It must be an absolute origin
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
		return nil, false, fmt.Errorf("%s: must be an origin without a path; serving Docker Manager under a sub-path is not supported", EnvPublicURL)
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
