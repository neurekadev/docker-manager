package stacks

import (
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
)

func ctr(svc, dir string) engine.Container {
	return engine.Container{Labels: map[string]string{lifecycle.ComposeServiceLabel: svc, labelWorkingDir: dir}}
}

func TestImportStartSet(t *testing.T) {
	const old, moved = "/opt/stacks/app", "/var/lib/docker/volumes/s/_data/app"
	// Before the switch every service that ran starts again (from the original).
	start, kept := importStartSet([]engine.Container{ctr("web", old), ctr("db", old)}, []string{"db", "web"}, moved)
	if strings.Join(start, ",") != "db,web" || len(kept) != 0 {
		t.Errorf("before the switch: start %v kept %v", start, kept)
	}
	// After it, a service still (partly) on the original is never started.
	list := []engine.Container{ctr("web", moved), ctr("db", old), ctr("worker", moved), ctr("worker", old)}
	start, kept = importStartSet(list, []string{"db", "web", "worker"}, moved)
	if strings.Join(start, ",") != "web" || strings.Join(kept, ",") != "db,worker" {
		t.Errorf("after the switch: start %v kept %v", start, kept)
	}
}

func TestRelocated(t *testing.T) {
	const src, dst = "/import/app", "/stacks/app"
	orig := &compose.Project{Binds: []compose.Bind{
		{Service: "db", Source: src + "/pgdata", Target: "/var/lib/postgresql/data"},
		{Service: "web", Source: "/srv/media", Target: "/media"},
	}}
	moved := &compose.Project{Binds: []compose.Bind{
		{Service: "db", Source: dst + "/pgdata", Target: "/var/lib/postgresql/data"},
		{Service: "web", Source: "/srv/media", Target: "/media"},
	}}
	if err := relocated(orig, moved, src, dst); err != nil {
		t.Fatalf("relocatable project refused: %v", err)
	}
	// ../shared resolves elsewhere once the directory moved.
	orig.Binds = append(orig.Binds, compose.Bind{Service: "web", Source: "/import/shared", Target: "/shared"})
	moved.Binds = append(moved.Binds, compose.Bind{Service: "web", Source: "/stacks/shared", Target: "/shared"})
	if err := relocated(orig, moved, src, dst); err == nil || !strings.Contains(err.Error(), "relative path outside") {
		t.Errorf("relative bind outside the project: %v", err)
	}
}
