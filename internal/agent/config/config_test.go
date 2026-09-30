package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/envconfig"
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
	_, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "http://docker-manager:8080"}, nil))
	if err == nil || !strings.Contains(err.Error(), "refusing plain-HTTP") || !strings.Contains(err.Error(), EnvManagerAllowHTTP) {
		t.Fatalf("err = %v", err)
	}
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "http://docker-manager:8080", EnvManagerAllowHTTP: "true"}, nil))
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

func TestHostProc(t *testing.T) {
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com"}, nil))
	if err != nil || cfg.HostProc != DefaultHostProc {
		t.Fatalf("default %q %v", cfg.HostProc, err)
	}
	cfg, err = Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com", EnvHostProc: "/host/proc/"}, nil))
	if err != nil || cfg.HostProc != "/host/proc" {
		t.Fatalf("custom %q %v", cfg.HostProc, err)
	}
	if _, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com", EnvHostProc: "host/proc"}, nil)); err == nil ||
		!strings.Contains(err.Error(), EnvHostProc) {
		t.Fatalf("relative path accepted: %v", err)
	}
}

func TestHostSys(t *testing.T) {
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com"}, nil))
	if err != nil || cfg.HostSys != DefaultHostSys {
		t.Fatalf("default %q %v", cfg.HostSys, err)
	}
	cfg, err = Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com", EnvHostSys: "/host/sys/"}, nil))
	if err != nil || cfg.HostSys != "/host/sys" {
		t.Fatalf("custom %q %v", cfg.HostSys, err)
	}
	if _, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://d.example.com", EnvHostSys: "host/sys"}, nil)); err == nil ||
		!strings.Contains(err.Error(), EnvHostSys) {
		t.Fatalf("relative path accepted: %v", err)
	}
}

// TestParseRedirectURL (#35, manager moves): the address of a
// manager.redirect is an http or https origin (plain http needs no
// opt-in); anything else is refused without echoing credentials.
func TestParseRedirectURL(t *testing.T) {
	for raw, want := range map[string]string{
		"http://192.0.2.10:8080":     "http://192.0.2.10:8080",
		"http://Docker-Manager:80/":  "http://docker-manager:80",
		"https://docker.example.com": "https://docker.example.com",
		"https://[2001:db8::1]:8443": "https://[2001:db8::1]:8443",
	} {
		u, err := ParseRedirectURL(raw)
		if err != nil || u.String() != want {
			t.Errorf("ParseRedirectURL(%q) = %v, %v; want %s", raw, u, err, want)
		}
	}
	for _, bad := range []string{"", "docker.example.com", "ftp://x", "ws://x", "http://", "http://:8080", "https://u:hunter2@x",
		"https://x/path", "https://x?q=1", "https://x?", "https://x#f", "http:opaque", "http://x:port",
		"https://" + strings.Repeat("a", MaxRedirectURLLen)} {
		_, err := ParseRedirectURL(bad)
		if err == nil {
			t.Errorf("ParseRedirectURL(%q) accepted", bad)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("ParseRedirectURL(%q) echoes the password: %v", bad, err)
		}
	}
}

// TestSMARTSettings (#143): SMART is on by default, read every 30 minutes
// (5 minutes to 24 hours) with the image's smartctl.
func TestSMARTSettings(t *testing.T) {
	base := map[string]string{EnvManagerURL: "https://docker.example.com"}
	cfg, err := Load(envconfig.Map(base, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SMARTEnabled || cfg.SMARTInterval != DefaultSMARTInterval || cfg.SmartctlBinary != DefaultSmartctlBinary {
		t.Fatalf("defaults: %v %v %q", cfg.SMARTEnabled, cfg.SMARTInterval, cfg.SmartctlBinary)
	}
	with := func(kv ...string) map[string]string {
		m := map[string]string{EnvManagerURL: "https://docker.example.com"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	cfg, err = Load(envconfig.Map(with(EnvSMARTEnabled, "false", EnvSMARTInterval, "2h", EnvSmartctl, "/opt/smartctl"), nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMARTEnabled || cfg.SMARTInterval != 2*time.Hour || cfg.SmartctlBinary != "/opt/smartctl" {
		t.Fatalf("set: %v %v %q", cfg.SMARTEnabled, cfg.SMARTInterval, cfg.SmartctlBinary)
	}
	for _, bad := range []map[string]string{
		with(EnvSMARTEnabled, "sometimes"),
		with(EnvSMARTInterval, "1m"),
		with(EnvSMARTInterval, "25h"),
		with(EnvSMARTInterval, "often"),
		with(EnvSmartctl, "smartctl"),
	} {
		if _, err := Load(envconfig.Map(bad, nil)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
