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
	diffs, err := driftOf(ctx, eng, p, src, host, ctr(same))
	if err != nil || len(diffs) != 0 {
		t.Fatalf("a container created from the files differs: %v %v", diffs, err)
	}

	// A tool supplied TOKEN and EXTRA from its own database, and another rule.
	other := eng.AddContainer(spec("app-web-2", []string{"TOKEN=from-db", "MODE=prod", "EXTRA=1"}, "Host(`other.org`)"), true)
	diffs, err = driftOf(ctx, eng, p, src, host, ctr(other))
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
