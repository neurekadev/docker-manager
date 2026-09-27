package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/compose"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine/enginefake"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/lifecycle"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func TestDriftComparesContainersWithTheFiles(t *testing.T) {
	ctx := testutil.Context(t)
	src := t.TempDir()
	const host = "/opt/stacks/app"
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", `services:
  web:
    image: nginx:1.27
    environment:
      TOKEN: ${TOKEN}
      MODE: prod
    labels:
      traefik.http.routers.web.rule: 'Host(`+"`${DOMAIN}`"+`)'
    ports:
      - "8080:80"
    volumes:
      - ./data:/data
      - cache:/cache
volumes:
  cache:
`)
	write(".env", "TOKEN=abc\nDOMAIN=example.com\n")
	p, err := compose.LoadProject(ctx, compose.ProjectSpec{Name: "app", Dir: src, Profiles: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	eng := enginefake.New("engine-1")
	spec := func(name string, env []string, rule string) engine.ContainerSpec {
		return engine.ContainerSpec{Name: name, Image: "nginx:1.27", Env: env,
			Labels: map[string]string{lifecycle.ComposeServiceLabel: "web", "traefik.http.routers.web.rule": rule},
			Mounts: []engine.MountSpec{{Type: "bind", Source: host + "/data", Target: "/data"}, {Type: "volume", Source: "app_cache", Target: "/cache"}},
			Ports:  []engine.PortBinding{{ContainerPort: 80, Protocol: "tcp", HostPort: 8080}}}
	}
	ctr := func(id string) []engine.Container {
		return []engine.Container{{ID: id, Labels: map[string]string{lifecycle.ComposeServiceLabel: "web"}}}
	}

	same := eng.AddContainer(spec("app-web-1", []string{"TOKEN=abc", "MODE=prod"}, "Host(`example.com`)"), true)
	diffs, _, err := driftOf(ctx, eng, p, src, host, ctr(same))
	if err != nil || len(diffs) != 0 {
		t.Fatalf("a container created from the files differs: %v %v", diffs, err)
	}

	// A tool supplied TOKEN and EXTRA from its own database, and another rule.
	other := eng.AddContainer(spec("app-web-2", []string{"TOKEN=from-db", "MODE=prod", "EXTRA=1"}, "Host(`other.org`)"), true)
	diffs, _, err = driftOf(ctx, eng, p, src, host, ctr(other))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(diffs, "\n")
	for _, want := range []string{"web: environment variable TOKEN", "web: environment variable EXTRA (not in the files)",
		"web: label traefik.http.routers.web.rule"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %v", want, diffs)
		}
	}
	for _, secret := range []string{"from-db", "abc", "other.org"} {
		if strings.Contains(got, secret) {
			t.Errorf("a value leaked into the differences: %v", diffs)
		}
	}
}

// TestDriftKeepsBuiltImagesAndNamesMounts: a build-only service may run
// an image another tool named (Arcane), which is no difference; a mount
// the files add or change names both sides.
func TestDriftKeepsBuiltImagesAndNamesMounts(t *testing.T) {
	ctx := testutil.Context(t)
	src := t.TempDir()
	const host = "/opt/stacks/app"
	if err := os.WriteFile(filepath.Join(src, "compose.yaml"), []byte(`services:
  ui:
    build: .
  web:
    image: nginx:1.27
    volumes:
      - /srv/conf:/etc/conf:ro
      - /srv/data:/data
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := compose.LoadProject(ctx, compose.ProjectSpec{Name: "app", Dir: src, Profiles: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	eng := enginefake.New("engine-1")
	eng.AddImage("arcane.local/app-ce2b0484/ui:latest")
	eng.AddImage("nginx:1.27")
	ui := eng.AddContainer(engine.ContainerSpec{Name: "ui", Image: "arcane.local/app-ce2b0484/ui:latest",
		Labels: map[string]string{lifecycle.ComposeServiceLabel: "ui"}}, true)
	web := eng.AddContainer(engine.ContainerSpec{Name: "web", Image: "nginx:1.27",
		Labels: map[string]string{lifecycle.ComposeServiceLabel: "web"},
		Mounts: []engine.MountSpec{{Type: "bind", Source: "/srv/other", Target: "/data"}}}, true)
	list := []engine.Container{
		{ID: ui, Labels: map[string]string{lifecycle.ComposeServiceLabel: "ui"}},
		{ID: web, Labels: map[string]string{lifecycle.ComposeServiceLabel: "web"}},
	}
	diffs, created, err := driftOf(ctx, eng, p, src, host, list)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"web: mount at /data (the files bind /srv/data, the container has bind /srv/other)",
		"web: mount at /etc/conf (in the files, not in the container)",
	}
	if strings.Join(diffs, "\n") != strings.Join(want, "\n") {
		t.Errorf("diffs = %q", diffs)
	}
	if created.IsZero() {
		t.Error("the creation time of the differing container is missing")
	}
}

// TestEditedAfterTheDeploy: a definition file newer than the containers
// is named with both times; older files are no hint.
func TestEditedAfterTheDeploy(t *testing.T) {
	dir := t.TempDir()
	file, env := filepath.Join(dir, "compose.yaml"), filepath.Join(dir, ".env")
	for _, f := range []string{file, env} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2026, 8, 27, 21, 31, 0, 0, time.UTC)
	touch := func(f string, at time.Time) {
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	touch(file, created.Add(-time.Hour))
	touch(env, created.Add(-2*time.Hour))
	if got := editedAfter([]string{file, env}, dir, created); got != "" {
		t.Errorf("files older than the containers: %q", got)
	}
	touch(file, time.Date(2026, 9, 5, 19, 54, 0, 0, time.UTC))
	got := editedAfter([]string{file, env}, dir, created)
	if got != "compose.yaml was changed on 2026-09-05 19:54 UTC, after the containers were created on 2026-08-27 21:31 UTC" {
		t.Errorf("hint = %q", got)
	}
	var se *stepError
	if err := driftRefusal([]string{"web: environment variable A (not in the files)"}, got); !errors.As(err, &se) ||
		se.recovery != recoveryImportEdited || !strings.Contains(se.Error(), "compose.yaml was changed") {
		t.Errorf("refusal = %v", err)
	}
	if err := driftRefusal([]string{"web: image"}, ""); !errors.As(err, &se) || se.recovery != recoveryImportDrift {
		t.Errorf("refusal without a hint = %v", err)
	}
}

func TestSameImage(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"nginx:1.27", "docker.io/library/nginx:1.27", true},
		{"nginx", "nginx:latest", true},
		{"ghcr.io/o/app:1", "ghcr.io/o/app:1", true},
		{"nginx:1.27", "nginx:1.28", false},
		{"registry:5000/app", "registry:5000/app:latest", true},
	} {
		if sameImage(c.a, c.b) != c.same {
			t.Errorf("sameImage(%q, %q) != %v", c.a, c.b, c.same)
		}
	}
}
