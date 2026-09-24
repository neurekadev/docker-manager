package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/envconfig"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://Docker.Example.com"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagerURL.String() != "https://docker.example.com" || cfg.PlainHTTP {
		t.Errorf("manager url %v plain=%v", cfg.ManagerURL, cfg.PlainHTTP)
	}
	wantState, _ := filepath.Abs(DefaultStateDir)
	if cfg.StateDir != wantState || cfg.DockerHost != DefaultDockerHost || cfg.EnrollmentToken != "" {
		t.Errorf("unexpected defaults %+v", cfg)
	}
}

func TestRefusesPlainHTTPWithoutOptIn(t *testing.T) {
	_, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "http://dockyard-manager:8080"}, nil))
	if err == nil || !strings.Contains(err.Error(), "refusing plain-HTTP") || !strings.Contains(err.Error(), EnvManagerAllowHTTP) {
		t.Fatalf("err = %v", err)
	}
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "http://dockyard-manager:8080", EnvManagerAllowHTTP: "true"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PlainHTTP {
		t.Fatal("PlainHTTP not set")
	}
}

func TestManagerURLValidation(t *testing.T) {
	for _, bad := range []string{"", "docker.example.com", "ftp://x", "https://", "https://u:p@x", "https://x/path", "https://x/?q=1", "ws://x"} {
		if _, _, err := ParseManagerURL(bad, true); err == nil {
			t.Errorf("ParseManagerURL(%q) accepted", bad)
		}
	}
}

func TestEnrollmentTokenFileAndRedaction(t *testing.T) {
	src := envconfig.Map(map[string]string{
		EnvManagerURL:                "https://docker.example.com",
		EnvEnrollmentToken + "_FILE": "/run/secrets/token",
	}, map[string]string{"/run/secrets/token": "dy-enroll-SECRET\n"})
	cfg, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.EnrollmentToken) != "dy-enroll-SECRET" {
		t.Fatalf("token = %q", string(cfg.EnrollmentToken))
	}
	if s := fmt.Sprintf("%+v", cfg); strings.Contains(s, "SECRET") {
		t.Fatalf("token leaks through %%+v: %s", s)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(envconfig.Map(map[string]string{
		EnvManagerAllowHTTP: "maybe",
		EnvDockerHost:       "ssh://host",
		EnvEnvironmentName:  strings.Repeat("x", 64),
		EnvLogLevel:         "loud",
	}, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{EnvManagerAllowHTTP, EnvManagerURL, EnvDockerHost, EnvEnvironmentName, EnvLogLevel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %s: %v", want, err)
		}
	}
}
