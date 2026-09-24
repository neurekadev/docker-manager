// Package config loads and validates the agent's environment configuration.
// Every variable is documented in docs/configuration.md; keep both in sync.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
)

// Environment variable names.
const (
	EnvManagerURL       = "DOCKYARD_MANAGER_URL"
	EnvManagerAllowHTTP = "DOCKYARD_MANAGER_ALLOW_HTTP"
	EnvEnrollmentToken  = "DOCKYARD_ENROLLMENT_TOKEN" //nolint:gosec // variable name, not a credential; also _FILE
	EnvStateDir         = "DOCKYARD_AGENT_STATE_DIR"
	EnvDockerHost       = "DOCKER_HOST"
	EnvEnvironmentName  = "DOCKYARD_ENVIRONMENT_NAME"
	EnvLogLevel         = "DOCKYARD_LOG_LEVEL"
	EnvLogFormat        = "DOCKYARD_LOG_FORMAT"
)

// Defaults.
const (
	DefaultStateDir   = "/var/lib/dockyard-agent"
	DefaultDockerHost = "unix:///var/run/docker.sock"
	// MaxEnvironmentNameLen bounds DOCKYARD_ENVIRONMENT_NAME.
	MaxEnvironmentNameLen = 63
)

// Config is the validated agent configuration.
type Config struct {
	// ManagerURL is the manager origin the agent dials (no path).
	ManagerURL *url.URL
	// PlainHTTP is true when ManagerURL is http:// (explicitly allowed).
	PlainHTTP bool
	// EnrollmentToken is the one-use enrollment secret; never log it.
	EnrollmentToken logging.Secret
	StateDir        string
	DockerHost      string
	// EnvironmentName is the optional initial display name for this Environment.
	EnvironmentName string
	LogLevel        slog.Level
	LogFormat       string
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

// ParseManagerURL validates DOCKYARD_MANAGER_URL. https is required unless
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
