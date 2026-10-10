// Package config loads and validates the manager's environment configuration.
// Every variable is documented in docs/internal/configuration.md and in the
// user docs (docs/public/content/docs/configuration.mdx); keep all three in sync.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/envconfig"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/server/sse"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/transfer"
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

	EnvSessionIdleTimeout     = "DOCKER_MANAGER_SESSION_IDLE_TIMEOUT"
	EnvSessionLifetime        = "DOCKER_MANAGER_SESSION_LIFETIME"
	EnvSessionStayIdleTimeout = "DOCKER_MANAGER_SESSION_STAY_IDLE_TIMEOUT"
	EnvSessionStayLifetime    = "DOCKER_MANAGER_SESSION_STAY_LIFETIME"

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

	// File manager limits (#15): the manager enforces them and sends them
	// to agents that announce protocol.FeatureFileLimits.
	EnvFilesMaxEditKB         = "DOCKER_MANAGER_FILES_MAX_EDIT_KB"
	EnvFilesMaxUploadMB       = "DOCKER_MANAGER_FILES_MAX_UPLOAD_MB"
	EnvFilesMaxDownloadMB     = "DOCKER_MANAGER_FILES_MAX_DOWNLOAD_MB"
	EnvFilesMaxExtractMB      = "DOCKER_MANAGER_FILES_MAX_EXTRACT_MB"
	EnvFilesMaxExtractRatio   = "DOCKER_MANAGER_FILES_MAX_EXTRACT_RATIO"
	EnvFilesMaxArchiveEntries = "DOCKER_MANAGER_FILES_MAX_ARCHIVE_ENTRIES"
	// EnvTemplateMaxSizeMB bounds a stack template's files (template
	// registry).
	EnvTemplateMaxSizeMB = "DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB"
	// EnvTemplateRegistryEnabled serves this instance's public template
	// registry (template registry).
	EnvTemplateRegistryEnabled = "DOCKER_MANAGER_TEMPLATE_REGISTRY_ENABLED"
	// EnvTemplateRegistrySyncInterval is how often added template
	// registries are read again (0 turns it off).
	EnvTemplateRegistrySyncInterval = "DOCKER_MANAGER_TEMPLATE_REGISTRY_SYNC_INTERVAL"

	EnvMigrationBandwidthLimit = "DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT"
	// EnvStackArchiveMaxMB bounds a stack archive: an export's data and an
	// uploaded archive (#313).
	EnvStackArchiveMaxMB = "DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB"
	// Backups (#10).
	EnvResticBinary = "DOCKER_MANAGER_RESTIC_BINARY"
	// Diagnostics (#34): the Prometheus endpoint of Docker Manager's own
	// internals (not the host metrics of #5, which are always collected).
	EnvMetricsEnabled = "DOCKER_MANAGER_METRICS_ENABLED"
	// Moving Docker Manager to a new server: a new, empty manager with
	// both set waits for the old manager's handoff
	// (docs/internal/architecture/manager-move.md).
	EnvMoveFrom = "DOCKER_MANAGER_MOVE_FROM"
	EnvMoveCode = "DOCKER_MANAGER_MOVE_CODE" //nolint:gosec // G101: a variable name, not a credential
	// EnvAlertOfflineGrace is how long an active environment may be
	// offline before it raises an alert (#159).
	EnvAlertOfflineGrace = "DOCKER_MANAGER_ALERT_OFFLINE_GRACE"
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

	// Browser session limits (#16): a working day of inactivity and a day
	// at most; "Stay signed in" keeps a device signed in for 30 days of
	// inactivity and a year at most.
	DefaultSessionIdleTimeout     = 8 * time.Hour
	DefaultSessionLifetime        = 24 * time.Hour
	DefaultSessionStayIdleTimeout = 30 * 24 * time.Hour
	DefaultSessionStayLifetime    = 365 * 24 * time.Hour

	DefaultAuditRetentionDays = 365
	DefaultAuditMaxSizeMB     = 1024

	// DefaultAlertOfflineGrace: an environment offline this long raises
	// an alert (#159).
	DefaultAlertOfflineGrace = 5 * time.Minute

	// Metrics (#5): a separate database file, excluded from manager-state
	// backups by default (#10).
	MetricsFileName                = "metrics.db"
	DefaultMetricsRetentionRaw     = 24 * time.Hour
	DefaultMetricsRetentionMinute  = 7 * 24 * time.Hour
	DefaultMetricsRetentionQuarter = 90 * 24 * time.Hour
	DefaultMetricsMaxSizeMB        = 2048
	DefaultMetricsMaxSeries        = 5000

	// File manager limits (#15). The defaults are the agents' built-in
	// ones (internal/fsroot); older agents keep those whatever is set.
	DefaultFilesMaxEditKB         = 512
	DefaultFilesMaxUploadMB       = 2048
	DefaultFilesMaxDownloadMB     = 10240
	DefaultFilesMaxExtractMB      = 10240
	DefaultFilesMaxExtractRatio   = 100
	DefaultFilesMaxArchiveEntries = 100_000
	// Bounds of the file manager limits: the edit limit keeps the editor
	// and the request bodies of saves reasonable, the others are the caps
	// agents apply (protocol.MaxFileLimit*).
	MinFilesMaxEditKB         = 64
	MaxFilesMaxEditKB         = 16 << 10
	MaxFilesMaxSizeMB         = protocol.MaxFileLimitBytes >> 20
	MinFilesMaxExtractRatio   = 10
	MinFilesMaxArchiveEntries = 100
	// DefaultTemplateMaxSizeMB bounds a template's draft and versions.
	DefaultTemplateMaxSizeMB = 32
	// DefaultStackArchiveMaxMB bounds a stack archive (10 GiB); at most
	// MaxStackArchiveMaxMB (1 TiB).
	DefaultStackArchiveMaxMB = 10 << 10
	MaxStackArchiveMaxMB     = 1 << 20
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
	// StayIdleTimeout and StayLifetime replace both for sessions signed in
	// with "Stay signed in".
	StayIdleTimeout time.Duration
	StayLifetime    time.Duration
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
	// Files are the file manager's limits (#15). The reverse proxy's body
	// limit must allow Files.Upload (#27).
	Files domain.FileLimits
	// TemplateMaxSize bounds a stack template's files in bytes (draft and
	// each published version).
	TemplateMaxSize int64
	// TemplateRegistryEnabled serves the public template registry (public
	// templates only; default true).
	TemplateRegistryEnabled bool
	// TemplateRegistrySync is how often added registries are synced
	// (negative: never).
	TemplateRegistrySync time.Duration
	// MigrationBandwidthLimit caps the data environment migrations relay
	// through the manager, in bytes per second (#35; 0: unlimited).
	MigrationBandwidthLimit int64
	// StackArchiveMax bounds a stack archive in bytes (#313): the data an
	// export writes and the size of an uploaded archive. The reverse
	// proxy's body limit must allow it for uploads.
	StackArchiveMax int64
	// ResticBinary is the pinned restic (#10).
	ResticBinary string
	// MetricsEnabled serves GET /api/v1/system/metrics (#34; default off).
	MetricsEnabled bool
	// Move are DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE.
	Move MoveConfig
	// AlertOfflineGrace is how long an active environment may be offline
	// before it raises an alert (#159; 1m to 24h, default 5m).
	AlertOfflineGrace time.Duration
}

// MoveConfig starts a new, empty manager in waiting mode: it asks the old
// manager at From for the handoff of the move whose code is Code. Both or
// neither are set.
type MoveConfig struct {
	// From is the old manager's origin (http or https; plain http is
	// allowed: the handoff is encrypted and authenticated by the code).
	From *url.URL
	// Code is the move code (dmm_...); never logged or listed.
	Code logging.Secret
}

// Set reports whether both move variables are set.
func (m MoveConfig) Set() bool { return m.From != nil && string(m.Code) != "" }

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
	moveFrom, moveCode := "", ""
	if c.Move.From != nil {
		moveFrom = c.Move.From.String()
	}
	if string(c.Move.Code) != "" {
		moveCode = "(set)"
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
		{EnvSessionStayIdleTimeout, d(c.Sessions.StayIdleTimeout)},
		{EnvSessionStayLifetime, d(c.Sessions.StayLifetime)},
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
		{EnvFilesMaxEditKB, strconv.FormatInt(c.Files.Edit>>10, 10)},
		{EnvFilesMaxUploadMB, mb(c.Files.Upload)},
		{EnvFilesMaxDownloadMB, mb(c.Files.Download)},
		{EnvFilesMaxExtractMB, mb(c.Files.ExtractBytes)},
		{EnvFilesMaxExtractRatio, strconv.FormatInt(c.Files.ExtractRatio, 10)},
		{EnvFilesMaxArchiveEntries, i(c.Files.ArchiveEntries)},
		{EnvTemplateMaxSizeMB, mb(c.TemplateMaxSize)},
		{EnvTemplateRegistryEnabled, strconv.FormatBool(c.TemplateRegistryEnabled)},
		{EnvTemplateRegistrySyncInterval, c.TemplateRegistrySync.String()},
		{EnvMigrationBandwidthLimit, strconv.FormatInt(c.MigrationBandwidthLimit, 10) + " B/s (0: unlimited)"},
		{EnvStackArchiveMaxMB, mb(c.StackArchiveMax)},
		{EnvResticBinary, c.ResticBinary},
		{EnvMetricsEnabled, strconv.FormatBool(c.MetricsEnabled)},
		{EnvMoveFrom, moveFrom},
		{EnvMoveCode, moveCode},
		{EnvAlertOfflineGrace, d(c.AlertOfflineGrace)},
		{"local_development", strconv.FormatBool(c.LocalDevelopment)},
	}
}

// DefaultFiles are the file manager limits without configuration.
func DefaultFiles() domain.FileLimits {
	return domain.FileLimits{Edit: DefaultFilesMaxEditKB << 10, Upload: DefaultFilesMaxUploadMB << 20,
		Download: DefaultFilesMaxDownloadMB << 20, ExtractBytes: DefaultFilesMaxExtractMB << 20,
		ExtractRatio: DefaultFilesMaxExtractRatio, ArchiveEntries: DefaultFilesMaxArchiveEntries}
}

// loadFiles reads the file manager limits (#15).
func loadFiles(src envconfig.Source) (domain.FileLimits, error) {
	var errs []error
	get := func(name string, def, lo, hi int) int {
		v, err := src.Int(name, def, lo, hi)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}
	f := domain.FileLimits{
		Edit:           int64(get(EnvFilesMaxEditKB, DefaultFilesMaxEditKB, MinFilesMaxEditKB, MaxFilesMaxEditKB)) << 10,
		Upload:         int64(get(EnvFilesMaxUploadMB, DefaultFilesMaxUploadMB, 1, MaxFilesMaxSizeMB)) << 20,
		Download:       int64(get(EnvFilesMaxDownloadMB, DefaultFilesMaxDownloadMB, 1, MaxFilesMaxSizeMB)) << 20,
		ExtractBytes:   int64(get(EnvFilesMaxExtractMB, DefaultFilesMaxExtractMB, 1, MaxFilesMaxSizeMB)) << 20,
		ExtractRatio:   int64(get(EnvFilesMaxExtractRatio, DefaultFilesMaxExtractRatio, MinFilesMaxExtractRatio, protocol.MaxFileLimitRatio)),
		ArchiveEntries: get(EnvFilesMaxArchiveEntries, DefaultFilesMaxArchiveEntries, MinFilesMaxArchiveEntries, protocol.MaxFileLimitEntries),
	}
	return f, errors.Join(errs...)
}

// DefaultResticBinary is where the image installs restic (#10).
const DefaultResticBinary = "/usr/local/bin/restic"

// ResticCacheDir is restic's cache inside the data directory.
func (c Config) ResticCacheDir() string { return filepath.Join(c.DataDir, "restic-cache") }

// StackArchiveDir holds stack archives: exports and uploads (#313).
func (c Config) StackArchiveDir() string { return filepath.Join(c.DataDir, "stack-archives") }

// ResticTempDir holds restic's temporary files inside the data directory.
func (c Config) ResticTempDir() string { return filepath.Join(c.DataDir, "tmp") }

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

	cfg.Files, err = loadFiles(src)
	if err != nil {
		errs = append(errs, err)
	}

	templateMB, err := src.Int(EnvTemplateMaxSizeMB, DefaultTemplateMaxSizeMB, 1, 1024)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.TemplateMaxSize = int64(templateMB) << 20
	archiveMB, err := src.Int(EnvStackArchiveMaxMB, DefaultStackArchiveMaxMB, 1, MaxStackArchiveMaxMB)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.StackArchiveMax = int64(archiveMB) << 20
	if cfg.TemplateRegistryEnabled, err = src.Bool(EnvTemplateRegistryEnabled, true); err != nil {
		errs = append(errs, err)
	}
	if cfg.TemplateRegistrySync, err = time.ParseDuration(src.String(EnvTemplateRegistrySyncInterval, "30m")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvTemplateRegistrySyncInterval, err))
	} else if cfg.TemplateRegistrySync == 0 {
		cfg.TemplateRegistrySync = -1
	} else if cfg.TemplateRegistrySync < 5*time.Minute {
		errs = append(errs, fmt.Errorf("%s: at least 5m (or 0 to turn syncing off)", EnvTemplateRegistrySyncInterval))
	}

	if cfg.MigrationBandwidthLimit, err = transfer.ParseRate(src.String(EnvMigrationBandwidthLimit, "0")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvMigrationBandwidthLimit, err))
	}

	if cfg.MetricsEnabled, err = src.Bool(EnvMetricsEnabled, false); err != nil {
		errs = append(errs, err)
	}
	if cfg.Move, err = loadMove(src); err != nil {
		errs = append(errs, err)
	}
	if cfg.AlertOfflineGrace, err = src.Duration(EnvAlertOfflineGrace, DefaultAlertOfflineGrace, time.Minute, 24*time.Hour); err != nil {
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

// loadMove reads DOCKER_MANAGER_MOVE_FROM and DOCKER_MANAGER_MOVE_CODE:
// both or neither; the address an http or https origin (no credentials,
// path, query or fragment), the code a move code's shape.
func loadMove(src envconfig.Source) (MoveConfig, error) {
	from := strings.TrimSpace(src.String(EnvMoveFrom, ""))
	code := strings.TrimSpace(src.String(EnvMoveCode, ""))
	switch {
	case from == "" && code == "":
		return MoveConfig{}, nil
	case from == "":
		return MoveConfig{}, fmt.Errorf("%s is set but %s is not: set both (the old Docker Manager's address and the move code) or neither",
			EnvMoveCode, EnvMoveFrom)
	case code == "":
		return MoveConfig{}, fmt.Errorf("%s is set but %s is not: set both (the old Docker Manager's address and the move code) or neither",
			EnvMoveFrom, EnvMoveCode)
	}
	u, err := ParseMoveFrom(from)
	if err != nil {
		return MoveConfig{}, fmt.Errorf("%s: %w", EnvMoveFrom, err)
	}
	if !strings.HasPrefix(code, "dmm_") || len(code) > 256 || strings.ContainsAny(code, " \t") {
		return MoveConfig{}, fmt.Errorf("%s: not a move code (it starts with dmm_); copy it from the .env shown on the old Docker Manager", EnvMoveCode)
	}
	return MoveConfig{From: u, Code: logging.Secret(code)}, nil
}

// ParseMoveFrom checks the old manager's address of a move: an http or
// https origin without credentials, path, query or fragment.
func ParseMoveFrom(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("enter the old Docker Manager's address, like http://192.168.1.10:8080")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("enter only the address (scheme, host and port), like http://192.168.1.10:8080")
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
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
	var errs [4]error
	c.IdleTimeout, errs[0] = src.Duration(EnvSessionIdleTimeout, DefaultSessionIdleTimeout, 5*time.Minute, 7*24*time.Hour)
	c.Lifetime, errs[1] = src.Duration(EnvSessionLifetime, DefaultSessionLifetime, 15*time.Minute, 30*24*time.Hour)
	c.StayIdleTimeout, errs[2] = src.Duration(EnvSessionStayIdleTimeout, DefaultSessionStayIdleTimeout, time.Hour, 365*24*time.Hour)
	c.StayLifetime, errs[3] = src.Duration(EnvSessionStayLifetime, DefaultSessionStayLifetime, 24*time.Hour, 2*365*24*time.Hour)
	if err := errors.Join(errs[:]...); err != nil {
		return c, err
	}
	for _, p := range []struct {
		shorter, longer         time.Duration
		shorterName, longerName string
	}{
		{c.IdleTimeout, c.Lifetime, EnvSessionIdleTimeout, EnvSessionLifetime},
		{c.StayIdleTimeout, c.StayLifetime, EnvSessionStayIdleTimeout, EnvSessionStayLifetime},
		{c.IdleTimeout, c.StayIdleTimeout, EnvSessionIdleTimeout, EnvSessionStayIdleTimeout},
		{c.Lifetime, c.StayLifetime, EnvSessionLifetime, EnvSessionStayLifetime},
	} {
		if p.shorter > p.longer {
			return c, fmt.Errorf("%s (%s) must not exceed %s (%s)", p.shorterName, p.shorter, p.longerName, p.longer)
		}
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
