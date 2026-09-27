package stacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
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

func TestProjectDirTranslatesManagerPaths(t *testing.T) {
	const projects = "/docker/engine/volumes/arcane_data/_data/projects"
	tmp := t.TempDir()
	for _, d := range []string{"beszel", "forgejo"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	res := &storage.Result{Containerized: true, Imports: []storage.ImportMount{{HostPath: projects, Path: filepath.ToSlash(tmp)}}}
	// Arcane runs Compose in its own container, which mounts its data
	// volume at /app/data: labels carry /app/data/projects/<name>.
	arcane := engine.Container{Mounts: []engine.Mount{
		{Type: "volume", Source: "/docker/engine/volumes/arcane_data/_data", Destination: "/app/data"},
		{Type: "bind", Source: "/etc/localtime", Destination: "/etc/localtime"},
	}}
	all := []engine.Container{arcane}

	host, local, err := ProjectDir(res, []string{"/app/data/projects/beszel"}, all)
	if err != nil || host != projects+"/beszel" || local != filepath.Join(tmp, "beszel") {
		t.Errorf("manager path: %q %q %v", host, local, err)
	}
	// Containers created from the host path and from the manager's path
	// lead to the same directory.
	host, _, err = ProjectDir(res, []string{projects + "/forgejo", "/app/data/projects/forgejo"}, all)
	if err != nil || host != projects+"/forgejo" {
		t.Errorf("mixed labels: %q %v", host, err)
	}
	// A directory no import mount exposes.
	if _, _, err := ProjectDir(res, []string{"/docker/projects/arcane"}, all); err == nil || !strings.Contains(err.Error(), "/import") {
		t.Errorf("unmounted directory: %v", err)
	}
	// Without the manager's container the path cannot be translated.
	if _, _, err := ProjectDir(res, []string{"/app/data/projects/beszel"}, nil); err == nil {
		t.Error("an untranslatable label was resolved")
	}
}
