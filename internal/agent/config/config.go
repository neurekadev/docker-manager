// Package config loads and validates the agent's environment configuration.
// Every variable is documented in docs/internal/configuration.md and in the
// user docs (docs/public/content/docs/configuration.mdx); keep all three in sync.
package config

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/neurekadev/docker-manager/internal/envconfig"
	"github.com/neurekadev/docker-manager/internal/logging"
)

// Environment variable names.
const (
	EnvManagerURL       = "DOCKER_AGENT_MANAGER_URL"
	EnvManagerAllowHTTP = "DOCKER_AGENT_MANAGER_ALLOW_HTTP"
	EnvManagerCAFile    = "DOCKER_AGENT_MANAGER_CA_FILE"
	EnvEnrollmentToken  = "DOCKER_AGENT_ENROLLMENT_TOKEN" //nolint:gosec // variable name, not a credential; also _FILE
	EnvStateDir         = "DOCKER_AGENT_STATE_DIR"
	EnvDockerHost       = "DOCKER_HOST"
	EnvEnvironmentName  = "DOCKER_AGENT_ENVIRONMENT_NAME"
	EnvLogLevel         = "DOCKER_AGENT_LOG_LEVEL"
	EnvLogFormat        = "DOCKER_AGENT_LOG_FORMAT"
	EnvStacksVolume     = "DOCKER_AGENT_STACKS_VOLUME"
	EnvStackRoots       = "DOCKER_AGENT_STACK_ROOTS"
	EnvHostProc         = "DOCKER_AGENT_HOST_PROC"
	EnvHostSys          = "DOCKER_AGENT_HOST_SYS"
	// Backups (#10).
	EnvBackupLocalRoots        = "DOCKER_AGENT_BACKUP_LOCAL_ROOTS"
	EnvBackupExternalAllowlist = "DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST"
	EnvResticBinary            = "DOCKER_AGENT_RESTIC_BINARY"
	// EnvWatchMax is the file watcher's kernel watch budget (#23).
	EnvWatchMax = "DOCKER_AGENT_WATCH_MAX"
	// Disk health (#143).
	EnvSMARTEnabled  = "DOCKER_AGENT_SMART_ENABLED"
	EnvSMARTInterval = "DOCKER_AGENT_SMART_INTERVAL"
	EnvSmartctl      = "DOCKER_AGENT_SMARTCTL_BINARY"
)

// Defaults.
const (
	DefaultStateDir   = "/var/lib/docker-agent"
	DefaultDockerHost = "unix:///var/run/docker.sock"
	// MaxEnvironmentNameLen bounds DOCKER_AGENT_ENVIRONMENT_NAME.
	MaxEnvironmentNameLen = 63
	// DefaultHostProc is the procfs read for host telemetry (#5).
	DefaultHostProc = "/proc"
	// DefaultHostSys is the sysfs the temperature sensors are read from
	// (#146).
	DefaultHostSys = "/sys"
	// DefaultStacksVolume is the named volume holding stack projects (#28).
	DefaultStacksVolume = "docker-manager_stacks"
	// MaxStackRoots bounds DOCKER_AGENT_STACK_ROOTS.
	MaxStackRoots = 16
	// DefaultResticBinary is where the image installs restic (#10).
	DefaultResticBinary = "/usr/local/bin/restic"
	// MaxPathList bounds the backup path lists.
	MaxPathList = 32
	// DefaultSmartctlBinary is where the image installs smartctl (#143).
	DefaultSmartctlBinary = "/usr/local/bin/smartctl"
	// DefaultSMARTInterval is how often every disk's SMART data is read;
	// MinSMARTInterval and MaxSMARTInterval bound it.
	DefaultSMARTInterval = 30 * time.Minute
	MinSMARTInterval     = 5 * time.Minute
	MaxSMARTInterval     = 24 * time.Hour
)

// Config is the validated agent configuration.
type Config struct {
	// ManagerURL is the manager origin the agent dials (no path).
	ManagerURL *url.URL
	// PlainHTTP is true when ManagerURL is http:// (explicitly allowed).
	PlainHTTP bool
	// ManagerCAFile is an optional PEM bundle of extra CA certificates
	// trusted for the manager's HTTPS origin (private PKI), in addition to
	// the system roots. ManagerCAPEM holds its validated content.
	ManagerCAFile string
	ManagerCAPEM  []byte
	// EnrollmentToken is the one-use enrollment secret; never log it.
	EnrollmentToken logging.Secret
	StateDir        string
	DockerHost      string
	// EnvironmentName is the optional initial display name for this Environment.
	EnvironmentName string
	LogLevel        slog.Level
	LogFormat       string
	// StacksVolume is the named volume holding one directory per stack (#28).
	StacksVolume string
	// StackRoots are extra host directories holding stacks, bind-mounted
	// into the agent at their identical paths (absolute, cleaned, unique).
	StackRoots []string
	// HostProc is the procfs mount host telemetry is read from (#5).
	HostProc string
	// HostSys is the sysfs mount the hwmon temperature sensors are read
	// from (#146).
	HostSys string
	// BackupLocalRoots are the directories local backup repositories on
	// this agent may live in; BackupExternalAllowlist the host paths
	// outside stack project directories that policies may opt into (#10).
	BackupLocalRoots        []string
	BackupExternalAllowlist []string
	// ResticBinary is the pinned restic executable.
	ResticBinary string
	// WatchMax is the file watcher's kernel watch budget (0: half the
	// kernel's fs.inotify.max_user_watches, #23).
	WatchMax int
	// SMARTEnabled reads the disks' SMART data (#143); SMARTInterval is
	// how often; SmartctlBinary the pinned smartctl.
	SMARTEnabled   bool
	SMARTInterval  time.Duration
	SmartctlBinary string
}

// Load reads and validates the configuration, reporting all problems at once.
func Load(src envconfig.Source) (Config, error) {
	var errs []error
	var cfg Config

	allowHTTP, err := src.Bool(EnvManagerAllowHTTP, false)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.ManagerURL, cfg.PlainHTTP, err = ParseManagerURL(src.String(EnvManagerURL, ""), allowHTTP)
	if err != nil {
		errs = append(errs, err)
	}

	if cfg.ManagerCAFile = src.String(EnvManagerCAFile, ""); cfg.ManagerCAFile != "" {
		if cfg.ManagerCAPEM, err = LoadCABundle(cfg.ManagerCAFile); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvManagerCAFile, err))
		}
	}

	token, err := src.Secret(EnvEnrollmentToken)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.EnrollmentToken = logging.Secret(strings.TrimSpace(token))

	if cfg.StateDir, err = filepath.Abs(src.String(EnvStateDir, DefaultStateDir)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvStateDir, err))
	}

	cfg.DockerHost = src.String(EnvDockerHost, DefaultDockerHost)
	if !strings.HasPrefix(cfg.DockerHost, "unix://") && !strings.HasPrefix(cfg.DockerHost, "tcp://") {
		errs = append(errs, fmt.Errorf("%s: unsupported value %q (want unix:///path or tcp://host:port)", EnvDockerHost, cfg.DockerHost))
	}

	cfg.EnvironmentName = src.String(EnvEnvironmentName, "")
	if err := validateName(cfg.EnvironmentName); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvEnvironmentName, err))
	}

	cfg.StacksVolume = src.String(EnvStacksVolume, DefaultStacksVolume)
	if !volumeNameRE.MatchString(cfg.StacksVolume) {
		errs = append(errs, fmt.Errorf("%s: %q is not a valid volume name", EnvStacksVolume, cfg.StacksVolume))
	}
	if cfg.StackRoots, err = ParseStackRoots(src.String(EnvStackRoots, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvStackRoots, err))
	}

	cfg.HostProc = path.Clean(src.String(EnvHostProc, DefaultHostProc))
	if !path.IsAbs(cfg.HostProc) {
		errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", EnvHostProc, cfg.HostProc))
	}
	cfg.HostSys = path.Clean(src.String(EnvHostSys, DefaultHostSys))
	if !path.IsAbs(cfg.HostSys) {
		errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", EnvHostSys, cfg.HostSys))
	}

	if cfg.BackupLocalRoots, err = ParsePathList(src.String(EnvBackupLocalRoots, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvBackupLocalRoots, err))
	}
	if cfg.BackupExternalAllowlist, err = ParsePathList(src.String(EnvBackupExternalAllowlist, "")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvBackupExternalAllowlist, err))
	}
	cfg.ResticBinary = src.String(EnvResticBinary, DefaultResticBinary)
	if !path.IsAbs(cfg.ResticBinary) {
		errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", EnvResticBinary, cfg.ResticBinary))
	}

	if cfg.WatchMax, err = src.Int(EnvWatchMax, 0, 0, 4_194_304); err != nil {
		errs = append(errs, err)
	}

	if cfg.SMARTEnabled, err = src.Bool(EnvSMARTEnabled, true); err != nil {
		errs = append(errs, err)
	}
	if cfg.SMARTInterval, err = src.Duration(EnvSMARTInterval, DefaultSMARTInterval, MinSMARTInterval, MaxSMARTInterval); err != nil {
		errs = append(errs, err)
	}
	cfg.SmartctlBinary = src.String(EnvSmartctl, DefaultSmartctlBinary)
	if !path.IsAbs(cfg.SmartctlBinary) {
		errs = append(errs, fmt.Errorf("%s: %q must be an absolute path", EnvSmartctl, cfg.SmartctlBinary))
	}

	if cfg.LogLevel, err = logging.ParseLevel(src.String(EnvLogLevel, "info")); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogLevel, err))
	}
	if cfg.LogFormat, err = logging.ParseFormat(src.String(EnvLogFormat, logging.FormatJSON)); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", EnvLogFormat, err))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// ParseManagerURL validates DOCKER_AGENT_MANAGER_URL. https is required unless
// allowHTTP is set (for an internal URL on the manager's Docker network, #27).
func ParseManagerURL(raw string, allowHTTP bool) (*url.URL, bool, error) {
	if raw == "" {
		return nil, false, fmt.Errorf("%s is required (e.g. https://docker.example.com)", EnvManagerURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: invalid URL: %w", EnvManagerURL, err)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, false, fmt.Errorf("%s: missing host", EnvManagerURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, false, fmt.Errorf("%s: must be an origin like https://docker.example.com (no credentials, path or query)", EnvManagerURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return nil, false, fmt.Errorf("%s: refusing plain-HTTP manager URL %q; enrollment tokens and agent credentials require HTTPS. "+
				"Set %s=true only for an internal URL on the manager's Docker network", EnvManagerURL, u.Scheme+"://"+u.Host, EnvManagerAllowHTTP)
		}
	default:
		return nil, false, fmt.Errorf("%s: scheme must be https (got %q)", EnvManagerURL, u.Scheme)
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, u.Scheme == "http", nil
}

// MaxRedirectURLLen bounds the manager address of manager.redirect.
const MaxRedirectURLLen = 2048

// ParseRedirectURL validates the manager address received in
// manager.redirect when Docker Manager moves to a new server
// (docs/internal/architecture/manager-move.md): an http or https origin,
// with the same shape rules as DOCKER_AGENT_MANAGER_URL. Plain http is
// allowed without DOCKER_AGENT_MANAGER_ALLOW_HTTP because the manager of
// the current, authenticated session sent it; this is the only exception
// to that rule, and it covers only this address (persisted in the state
// directory, transport.NewRedirected).
func ParseRedirectURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxRedirectURLLen {
		return nil, fmt.Errorf("manager address must be 1 to %d characters", MaxRedirectURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("manager address is not a valid URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("manager address must use http or https")
	}
	if u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("manager address has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("manager address must be an origin like http://192.0.2.10:8080 (no credentials, path or query)")
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
}

// LoadCABundle reads a PEM file and checks that it holds at least one
// parsable certificate and nothing but certificates.
func LoadCABundle(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-configured path
	if err != nil {
		return nil, err
	}
	rest, n := b, 0
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s: unexpected PEM block %q (want CERTIFICATE only)", path, blk.Type)
		}
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			return nil, fmt.Errorf("%s: invalid certificate: %w", path, err)
		}
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("%s: no PEM certificates found", path)
	}
	return b, nil
}

// volumeNameRE is Docker's volume name rule.
var volumeNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{1,254}$`)

// ParseStackRoots parses DOCKER_AGENT_STACK_ROOTS: comma-separated absolute
// Linux paths (not "/", no "..", no duplicates or nested roots).
func ParseStackRoots(raw string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !path.IsAbs(f) || strings.Contains(f, "\\") {
			return nil, fmt.Errorf("%q must be an absolute path", f)
		}
		for _, seg := range strings.Split(f, "/") {
			if seg == ".." {
				return nil, fmt.Errorf("%q must not contain \"..\"", f)
			}
		}
		c := path.Clean(f)
		if c == "/" {
			return nil, fmt.Errorf("%q: the filesystem root cannot be a stack root", f)
		}
		for _, o := range out {
			if o == c || strings.HasPrefix(c, o+"/") || strings.HasPrefix(o, c+"/") {
				return nil, fmt.Errorf("%q overlaps %q", c, o)
			}
		}
		out = append(out, c)
	}
	if len(out) > MaxStackRoots {
		return nil, fmt.Errorf("at most %d stack roots", MaxStackRoots)
	}
	return out, nil
}

// ParsePathList parses a comma-separated list of absolute Linux paths (not
// "/", no "..", no duplicates) for the backup settings.
func ParsePathList(raw string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !path.IsAbs(f) || strings.Contains(f, "\\") {
			return nil, fmt.Errorf("%q must be an absolute path", f)
		}
		for _, seg := range strings.Split(f, "/") {
			if seg == ".." {
				return nil, fmt.Errorf("%q must not contain \"..\"", f)
			}
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
	if len(out) > MaxPathList {
		return nil, fmt.Errorf("at most %d paths", MaxPathList)
	}
	return out, nil
}

func validateName(name string) error {
	if len(name) > MaxEnvironmentNameLen {
		return fmt.Errorf("must be at most %d characters", MaxEnvironmentNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}
