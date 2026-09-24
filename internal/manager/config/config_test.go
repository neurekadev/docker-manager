package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/envconfig"
)

func load(t *testing.T, vars map[string]string) (Config, error) {
	t.Helper()
	return Load(envconfig.Map(vars, nil))
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(t, map[string]string{EnvPublicURL: "https://Docker.Example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.PublicURL.String(); got != "https://docker.example.com" {
		t.Errorf("public URL = %q", got)
	}
	if cfg.LocalDevelopment {
		t.Error("https origin flagged as local development")
	}
	if cfg.ListenAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	wantData, _ := filepath.Abs(DefaultDataDir)
	if cfg.DataDir != wantData {
		t.Errorf("data dir = %q, want %q", cfg.DataDir, wantData)
	}
	if cfg.SecretKeyFile != filepath.Join(wantData, "secret.key") {
		t.Errorf("secret key file = %q", cfg.SecretKeyFile)
	}
	if cfg.DatabasePath() != filepath.Join(wantData, "dockyard.db") || cfg.SnapshotDir() != filepath.Join(wantData, "snapshots") {
		t.Errorf("derived paths wrong: %q %q", cfg.DatabasePath(), cfg.SnapshotDir())
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("trusted proxies = %v", cfg.TrustedProxies)
	}
}

func TestPublicURLValidation(t *testing.T) {
	cases := []struct {
		url     string
		wantErr string
		dev     bool
	}{
		{"", "is required", false},
		{"docker.example.com", "scheme must be https", false},
		{"ftp://docker.example.com", "scheme must be https", false},
		{"http://docker.example.com", "must use https", false},
		{"http://10.0.0.5:8080", "must use https", false},
		{"https://", "missing host", false},
		{"https://user:pw@docker.example.com", "credentials", false},
		{"https://docker.example.com/dockyard", "sub-path", false},
		{"https://docker.example.com/?x=1", "query", false},
		{"http://localhost:8080", "", true},
		{"http://127.0.0.1:5173", "", true},
		{"http://[::1]:8080", "", true},
		{"https://docker.example.com:8443", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			cfg, err := load(t, map[string]string{EnvPublicURL: tc.url})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LocalDevelopment != tc.dev {
				t.Fatalf("LocalDevelopment = %v", cfg.LocalDevelopment)
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := load(t, map[string]string{
		EnvListenAddr:     "8080",
		EnvLogLevel:       "loud",
		EnvLogFormat:      "xml",
		EnvTrustedProxies: "10.0.0.0/8, nonsense",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{EnvPublicURL, EnvListenAddr, EnvLogLevel, EnvLogFormat, EnvTrustedProxies} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	cfg, err := load(t, map[string]string{
		EnvPublicURL:      "https://docker.example.com",
		EnvTrustedProxies: "10.1.2.3/8, 192.168.1.10 fd00::/8",
		EnvDataDir:        "relative/data",
		EnvSecretKeyFile:  "/run/secrets/dockyard_secret_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(cfg.TrustedProxies))
	for _, p := range cfg.TrustedProxies {
		got = append(got, p.String())
	}
	if strings.Join(got, ",") != "10.0.0.0/8,192.168.1.10/32,fd00::/8" {
		t.Errorf("proxies = %v", got)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("data dir not absolute: %q", cfg.DataDir)
	}
	if !strings.HasSuffix(filepath.ToSlash(cfg.SecretKeyFile), "/run/secrets/dockyard_secret_key") {
		t.Errorf("secret key file = %q", cfg.SecretKeyFile)
	}
}

func TestJobLimits(t *testing.T) {
	cfg, err := load(t, map[string]string{EnvPublicURL: "https://d.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := JobsConfig{HistoryRetention: DefaultJobHistoryRetention, HistoryMax: DefaultJobHistoryMax, EventsMax: DefaultJobEventsMax,
		MaxConcurrentPulls: DefaultJobMaxConcurrentPulls, MaxConcurrentBuilds: DefaultJobMaxConcurrentBuild}
	if cfg.Jobs != want {
		t.Fatalf("defaults %+v", cfg.Jobs)
	}
	cfg, err = load(t, map[string]string{EnvPublicURL: "https://d.example.com", EnvJobHistoryRetention: "168h",
		EnvJobHistoryMax: "500", EnvJobEventsMax: "50", EnvJobMaxConcurrentPulls: "4", EnvJobMaxConcurrentBuild: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jobs.HistoryRetention.Hours() != 168 || cfg.Jobs.HistoryMax != 500 || cfg.Jobs.EventsMax != 50 ||
		cfg.Jobs.MaxConcurrentPulls != 4 || cfg.Jobs.MaxConcurrentBuilds != 2 {
		t.Fatalf("custom %+v", cfg.Jobs)
	}
	_, err = load(t, map[string]string{EnvPublicURL: "https://d.example.com", EnvJobHistoryRetention: "1m", EnvJobEventsMax: "x"})
	if err == nil || !strings.Contains(err.Error(), EnvJobHistoryRetention) || !strings.Contains(err.Error(), EnvJobEventsMax) {
		t.Fatalf("invalid limits: %v", err)
	}
}

func TestStreamHeartbeat(t *testing.T) {
	cfg, err := load(t, map[string]string{EnvPublicURL: "https://docker.example.com"})
	if err != nil || cfg.StreamHeartbeat != 15*time.Second {
		t.Fatalf("default heartbeat %v %v", cfg.StreamHeartbeat, err)
	}
	cfg, err = load(t, map[string]string{EnvPublicURL: "https://docker.example.com", EnvStreamHeartbeat: "5s"})
	if err != nil || cfg.StreamHeartbeat != 5*time.Second {
		t.Fatalf("5s: %v %v", cfg.StreamHeartbeat, err)
	}
	// Heartbeats at or above the common 60 s proxy timeouts are refused.
	for _, v := range []string{"60s", "500ms", "soon"} {
		if _, err := load(t, map[string]string{EnvPublicURL: "https://docker.example.com", EnvStreamHeartbeat: v}); err == nil || !strings.Contains(err.Error(), EnvStreamHeartbeat) {
			t.Errorf("%s accepted: %v", v, err)
		}
	}
}
