package regauth

import (
	"errors"
	"fmt"
	"testing"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/protocol"
)

func TestForReference(t *testing.T) {
	s := &protocol.CommandSecrets{Registries: []protocol.RegistryCredential{
		{ConnectionID: "hub", Host: "docker.io", ServerAddress: "https://index.docker.io/v1/", Username: "acme", Secret: "hub-secret-value"},
		{ConnectionID: "lan", Host: "registry.lan:5000", Username: "bot", Secret: "lan-secret-value"},
	}}
	for ref, want := range map[string]string{
		"acme/app:1":                     "https://index.docker.io/v1/",
		"index.docker.io/library/nginx":  "https://index.docker.io/v1/",
		"registry-1.docker.io/acme/app":  "https://index.docker.io/v1/",
		"registry.lan:5000/team/app:2":   "registry.lan:5000",
		"registry.lan:5001/team/app:2":   "",
		"ghcr.io/org/app":                "",
		"registry.lan/team/app:2":        "",
		"localhost:5000/something:else1": "",
	} {
		a, err := ForReference(s, ref, false)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		got := ""
		if a != nil {
			got = a.ServerAddress
		}
		if got != want {
			t.Errorf("%s: server %q, want %q", ref, got, want)
		}
	}
	if _, err := ForReference(s, "ghcr.io/org/app", true); !errors.Is(err, ErrMissing) {
		t.Fatalf("required: %v", err)
	}
	if _, err := ForReference(nil, "ghcr.io/org/app", true); !errors.Is(err, ErrMissing) {
		t.Fatalf("nil secrets: %v", err)
	}
	a, _ := ForReference(s, "registry.lan:5000/x", false)
	if a.Username != "bot" || string(a.Password) != "lan-secret-value" {
		t.Fatalf("%+v", a)
	}
	// The password redacts when formatted or logged.
	if s := fmt.Sprintf("%v", a.Password); s == "lan-secret-value" {
		t.Fatal("password is not redacted")
	}
	if all := All(s); len(all) != 2 || all[1].ServerAddress != "registry.lan:5000" || engine.RegistryHost(all[0].ServerAddress) != "registry-1.docker.io" {
		t.Fatalf("%+v", all)
	}
}
