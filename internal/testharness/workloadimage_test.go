package testharness

import (
	"encoding/json"
	"testing"
)

func TestNewWorkloadImageRevisionsDiffer(t *testing.T) {
	bin := []byte("#!workload")
	a, err := NewWorkloadImage("amd64", bin, "1")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := NewWorkloadImage("amd64", bin, "1")
	b, _ := NewWorkloadImage("amd64", bin, "2")
	if a.Digest != again.Digest || a.Digest == b.Digest || a.LayerDigest != b.LayerDigest {
		t.Fatalf("digests %s %s %s (layers %s %s)", a.Digest, again.Digest, b.Digest, a.LayerDigest, b.LayerDigest)
	}
	var cfg struct {
		Config struct {
			Entrypoint []string
			Labels     map[string]string
		} `json:"config"`
	}
	if err := json.Unmarshal(b.Config, &cfg); err != nil || len(cfg.Config.Entrypoint) != 1 || cfg.Config.Entrypoint[0] != WorkloadBinary ||
		cfg.Config.Labels["dev.neureka.dockyard.test.revision"] != "2" {
		t.Fatalf("config %s %v", b.Config, err)
	}
}
