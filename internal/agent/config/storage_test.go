package config

import (
	"strings"
	"testing"

	"github.com/neurekadev/docker-manager/internal/envconfig"
)

func TestStorageDefaults(t *testing.T) {
	cfg, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://docker.example.com"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StacksVolume != DefaultStacksVolume || len(cfg.StackRoots) != 0 {
		t.Errorf("storage defaults: volume %q roots %v", cfg.StacksVolume, cfg.StackRoots)
	}
	cfg, err = Load(envconfig.Map(map[string]string{
		EnvManagerURL: "https://docker.example.com", EnvStacksVolume: "my_stacks",
		EnvStackRoots: " /opt/stacks, /srv/compose/ ,",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StacksVolume != "my_stacks" || strings.Join(cfg.StackRoots, ",") != "/opt/stacks,/srv/compose" {
		t.Errorf("volume %q roots %v", cfg.StacksVolume, cfg.StackRoots)
	}
}

func TestParseStackRootsRejects(t *testing.T) {
	for _, bad := range []string{"relative/dir", "/", "/opt/../etc", "/opt/stacks,/opt/stacks", "/opt,/opt/stacks", `C:\stacks`} {
		if _, err := ParseStackRoots(bad); err == nil {
			t.Errorf("ParseStackRoots(%q) accepted", bad)
		}
	}
	_, err := Load(envconfig.Map(map[string]string{EnvManagerURL: "https://docker.example.com", EnvStacksVolume: "bad name!"}, nil))
	if err == nil || !strings.Contains(err.Error(), EnvStacksVolume) {
		t.Errorf("invalid volume name: %v", err)
	}
}
