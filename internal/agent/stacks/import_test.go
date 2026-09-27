package stacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/protect"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/storage"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobexec"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
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

// TestImportKeepsBuiltImages: a build-only service's running image (named
// by another tool) is tagged with Compose's name before the recreate;
// services with an image of their own are left alone.
func TestImportKeepsBuiltImages(t *testing.T) {
	ctx := testutil.Context(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"compose.yaml": "services:\n  ui:\n    build: .\n  web:\n    image: nginx:1.27\n",
		"Dockerfile": "FROM scratch\n"})
	p, err := compose.LoadProject(ctx, compose.ProjectSpec{Name: "garage", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	eng := enginefake.New("engine-1")
	built := eng.AddImage("arcane.local/garage-ce2b0484/ui:latest")
	eng.AddImage("nginx:1.27")
	for svc, img := range map[string]string{"ui": "arcane.local/garage-ce2b0484/ui:latest", "web": "nginx:1.27"} {
		eng.AddContainer(engine.ContainerSpec{Name: "garage-" + svc, Image: img,
			Labels: map[string]string{lifecycle.ComposeProjectLabel: "garage", lifecycle.ComposeServiceLabel: svc}}, false)
	}
	if err := keepBuiltImages(ctx, eng, p, "garage", []string{"ui", "web"}); err != nil {
		t.Fatal(err)
	}
	if img, err := eng.InspectImage(ctx, "garage-ui"); err != nil || img.ID != built {
		t.Errorf("garage-ui = %+v %v, want the running image %s", img, err, built)
	}
	// Idempotent: a retried recreate finds the tag in place.
	if err := keepBuiltImages(ctx, eng, p, "garage", []string{"ui"}); err != nil {
		t.Fatal(err)
	}
}

// TestImportOwnProjectWhileItRuns (#32): Docker Manager's own project is
// copied while it runs; nothing is stopped, recreated or started.
func TestImportOwnProjectWhileItRuns(t *testing.T) {
	const hostProjects = "/projects"
	stacks, imports := t.TempDir(), t.TempDir()
	writeTree(t, filepath.Join(imports, "docker-manager"), map[string]string{
		"compose.yaml": "services:\n  agent:\n    image: docker-agent:edge\n", ".env": ""})
	res := &storage.Result{Containerized: true, StacksDir: filepath.ToSlash(stacks),
		Roots:   []storage.Root{{Kind: storage.KindStacks, Path: filepath.ToSlash(stacks), OK: true}},
		Imports: []storage.ImportMount{{HostPath: hostProjects, Path: filepath.ToSlash(imports)}}}
	eng := enginefake.New("engine-1")
	eng.AddImage("docker-agent:edge")
	self := eng.AddContainer(engine.ContainerSpec{Name: "docker-agent", Image: "docker-agent:edge",
		Labels: map[string]string{lifecycle.ComposeProjectLabel: "docker-manager", lifecycle.ComposeServiceLabel: "agent",
			labelWorkingDir: hostProjects + "/docker-manager"}}, true)
	c := &fakeComposer{}
	svc := New(Options{Deps: fakeDeps{c: c, eng: eng, st: res}, Clock: testutil.FakeClock(), Logger: testutil.Logger(t),
		Guard: protect.New(protect.Options{SelfContainerID: self})})

	res2, out := run(t, svc, jobspec.StackImport, protocol.StackJobInput{Stack: ref("docker-manager"),
		Import: &protocol.StackImportSource{WorkingDir: hostProjects + "/docker-manager"}})
	if res2.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("import: %+v", res2)
	}
	if out.Import == nil || !out.Import.Live || !out.Import.Switched || strings.Join(out.Import.WasRunning, ",") != "agent" {
		t.Errorf("report %+v", out.Import)
	}
	if len(c.calls) != 0 {
		t.Errorf("Compose recreated Docker Manager: %v", c.calls)
	}
	if d, err := eng.InspectContainer(testutil.Context(t), self); err != nil || !d.State.Running {
		t.Errorf("Docker Manager's agent was stopped: %+v %v", d.State, err)
	}
	if _, err := os.Stat(filepath.Join(stacks, "docker-manager", "compose.yaml")); err != nil {
		t.Errorf("no copy: %v", err)
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
