package storage

import (
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
)

func TestImportMountsAndSources(t *testing.T) {
	mounts := []engine.Mount{
		{Source: "/var/lib/docker/volumes", Destination: "/var/lib/docker/volumes"},
		{Source: "/opt/stacks", Destination: "/import"},
		{Source: "/opt/stacks/big", Destination: "/import/big"},
		{Source: "/srv", Destination: "/importer"}, // not below /import
	}
	r := Result{Containerized: true, Imports: importMounts(mounts)}
	if len(r.Imports) != 2 || r.Imports[0].HostPath != "/opt/stacks/big" {
		t.Fatalf("imports %+v", r.Imports)
	}
	for _, c := range []struct {
		host, want string
		ok         bool
	}{
		{"/opt/stacks/web", "/import/web", true},
		{"/opt/stacks", "/import", true},
		{"/opt/stacks/big/db", "/import/big/db", true}, // the most specific mount
		{"/opt/stacksx/web", "", false},
		{"/srv/app", "", false},
		{"relative/dir", "", false},
		{"/", "", false},
	} {
		got, ok := r.ImportSource(c.host)
		if ok != c.ok || got != c.want {
			t.Errorf("ImportSource(%q) = %q, %v; want %q, %v", c.host, got, ok, c.want, c.ok)
		}
	}
}

func TestImportSourceOnTheHost(t *testing.T) {
	r := Result{StacksDir: "/var/lib/docker/volumes/s/_data",
		Roots: []Root{{Kind: KindStacks, Path: "/var/lib/docker/volumes/s/_data", OK: true}}}
	if got, ok := r.ImportSource("/opt/stacks/web"); !ok || got != "/opt/stacks/web" {
		t.Errorf("host agent: %q, %v", got, ok)
	}
	r.Roots[0].OK = false
	if _, ok := r.ImportSource("/opt/stacks/web"); ok {
		t.Error("an unverified host agent must not read import sources")
	}
}
