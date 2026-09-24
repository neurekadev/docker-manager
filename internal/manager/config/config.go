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

	"github.com/neurekadev/dockyard/internal/envconfig"
	"github.com/neurekadev/dockyard/internal/logging"
)

// Environment variable names.
const (
	EnvPublicURL      = "DOCKYARD_PUBLIC_URL"
	EnvListenAddr     = "DOCKYARD_LISTEN_ADDR"
	EnvDataDir        = "DOCKYARD_DATA_DIR"
	EnvSecretKeyFile  = "DOCKYARD_SECRET_KEY_FILE"
	EnvLogLevel       = "DOCKYARD_LOG_LEVEL"
	EnvLogFormat      = "DOCKYARD_LOG_FORMAT"
	EnvTrustedProxies = "DOCKYARD_TRUSTED_PROXIES"
)

// Defaults.
const (
	DefaultListenAddr = ":8080"
	DefaultDataDir    = "/var/lib/dockyard"
	SecretKeyFileName = "secret.key"
	DatabaseFileName  = "dockyard.db"
	SnapshotDirName   = "snapshots"
)

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

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
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
