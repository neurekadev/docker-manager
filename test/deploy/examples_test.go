// Package deploy checks the deployment examples in deploy/ (#27, #28)
// without Docker: topology, pinning, volumes, proxy settings, and that the
// proxy E2E stack (e2e/compose.yaml) runs exactly these proxy
// configurations. The examples themselves are exercised through each proxy
// by the Playwright suite (e2e/tests/proxy.spec.ts) and validated with
// `docker compose config` in CI.
package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neurekadev/dockyard/internal/manager/server/sse"
)

type service struct {
	Image       string            `yaml:"image"`
	Command     []string          `yaml:"command"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Networks    yaml.Node         `yaml:"networks"`
}

type composeFile struct {
	Services map[string]service `yaml:"services"`
	Volumes  map[string]any     `yaml:"volumes"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func load(t *testing.T, rel string) composeFile {
	t.Helper()
	var c composeFile
	if err := yaml.Unmarshal([]byte(read(t, rel)), &c); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return c
}

// interp resolves ${VAR:-default} with the defaults (what `docker compose`
// uses without a .env file).
var interpRE = regexp.MustCompile(`\$\{([A-Z0-9_]+):-([^}]*)\}`)

func interp(s string) string { return interpRE.ReplaceAllString(s, "$2") }

var pinnedRE = regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)

// proxies maps each example to its proxy service.
var proxies = map[string]string{"caddy": "caddy", "traefik": "traefik", "nginx": "nginx"}

func TestProxyExamplesTopology(t *testing.T) {
	for dir, proxy := range proxies {
		t.Run(dir, func(t *testing.T) {
			c := load(t, "deploy/"+dir+"/compose.yaml")
			m, a, p := c.Services["dockyard-manager"], c.Services["dockyard-agent"], c.Services[proxy]
			if m.Image != "ghcr.io/neurekadev/dockyard-manager:edge" || a.Image != "ghcr.io/neurekadev/dockyard-agent:edge" {
				t.Errorf("DockYard images %q %q", m.Image, a.Image)
			}
			if !pinnedRE.MatchString(p.Image) {
				t.Errorf("proxy image %q is not pinned by digest", p.Image)
			}
			// One public HTTPS origin; only the proxy is trusted.
			if interp(m.Environment["DOCKYARD_PUBLIC_URL"]) != "https://localhost" {
				t.Errorf("public URL %q", m.Environment["DOCKYARD_PUBLIC_URL"])
			}
			proxyIP := proxyAddress(t, p)
			if interp(m.Environment["DOCKYARD_TRUSTED_PROXIES"]) != proxyIP || proxyIP == "" {
				t.Errorf("trusted proxies %q, proxy address %q", m.Environment["DOCKYARD_TRUSTED_PROXIES"], proxyIP)
			}
			if len(m.Ports) != 0 || len(a.Ports) != 0 {
				t.Error("manager or agent publishes ports; only the proxy may")
			}
			if !slices.ContainsFunc(p.Ports, func(s string) bool { return strings.HasSuffix(s, ":443") }) {
				t.Errorf("proxy ports %v lack 443", p.Ports)
			}
			// Co-located agent: internal URL with the explicit opt-in.
			if a.Environment["DOCKYARD_MANAGER_URL"] != "http://dockyard-manager:8080" || a.Environment["DOCKYARD_MANAGER_ALLOW_HTTP"] != "true" {
				t.Errorf("co-located agent env %v", a.Environment)
			}
			checkStorage(t, c, m, a)
		})
	}
}

// proxyAddress returns the proxy's fixed ipv4_address (defaults resolved).
func proxyAddress(t *testing.T, p service) string {
	t.Helper()
	var nets map[string]struct {
		IPv4 string `yaml:"ipv4_address"`
	}
	if err := p.Networks.Decode(&nets); err != nil {
		t.Fatalf("proxy networks: %v", err)
	}
	return interp(nets["dockyard"].IPv4)
}

// checkStorage enforces #28: DockYard state only in named volumes, plus the
// Docker socket and the identical-path mount of Docker's volume directory.
func checkStorage(t *testing.T, c composeFile, svcs ...service) {
	t.Helper()
	allowedHost := map[string]bool{
		"/var/run/docker.sock:/var/run/docker.sock":       true,
		"/var/lib/docker/volumes:/var/lib/docker/volumes": true,
	}
	for _, s := range svcs {
		for _, v := range s.Volumes {
			src := strings.SplitN(v, ":", 2)[0]
			if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "$") {
				if !allowedHost[strings.TrimSuffix(v, ":ro")] {
					t.Errorf("%s: bind mount %q; use a named volume", s.Image, v)
				}
				continue
			}
			if _, ok := c.Volumes[src]; !ok {
				t.Errorf("%s: volume %q not declared", s.Image, src)
			}
		}
	}
	if len(svcs) > 1 { // manager + agent
		if !slices.Contains(svcs[1].Volumes, "/var/lib/docker/volumes:/var/lib/docker/volumes") {
			t.Error("agent lacks the identical-path mount of /var/lib/docker/volumes")
		}
	}
}

func TestRemoteAgentExample(t *testing.T) {
	c := load(t, "deploy/remote-agent/compose.yaml")
	a, ok := c.Services["dockyard-agent"]
	if !ok || len(c.Services) != 1 {
		t.Fatalf("services %v", c.Services)
	}
	if u := interp(a.Environment["DOCKYARD_MANAGER_URL"]); !strings.HasPrefix(u, "https://") {
		t.Errorf("remote agent must use the public https origin, got %q", u)
	}
	if _, ok := a.Environment["DOCKYARD_MANAGER_ALLOW_HTTP"]; ok {
		t.Error("remote agent sets DOCKYARD_MANAGER_ALLOW_HTTP")
	}
	if _, ok := a.Environment["DOCKYARD_MANAGER_CA_FILE"]; !ok {
		t.Error("remote agent does not expose DOCKYARD_MANAGER_CA_FILE")
	}
	if len(a.Ports) != 0 {
		t.Error("agent publishes ports")
	}
	if !slices.Contains(a.Volumes, "/var/lib/docker/volumes:/var/lib/docker/volumes") {
		t.Error("identical-path mount missing")
	}
	checkStorage(t, c, a)
}

// TestProxySettings pins the proxy settings the manager relies on.
func TestProxySettings(t *testing.T) {
	caddy := read(t, "deploy/caddy/Caddyfile")
	for _, want := range []string{"reverse_proxy dockyard-manager:8080", "flush_interval -1", "read_timeout {$DOCKYARD_PROXY_READ_TIMEOUT:60s}", "max_size {$DOCKYARD_MAX_BODY_SIZE:1GB}"} {
		if !strings.Contains(caddy, want) {
			t.Errorf("Caddyfile lacks %q", want)
		}
	}
	nginx := read(t, "deploy/nginx/templates/dockyard.conf.template")
	for _, want := range []string{
		"http2 on;", "proxy_http_version 1.1;", "proxy_set_header Host              $http_host;",
		"X-Forwarded-For   $proxy_add_x_forwarded_for;", "X-Forwarded-Proto $scheme;", "X-Forwarded-Host  $http_host;",
		"proxy_set_header Upgrade    $http_upgrade;", "proxy_set_header Connection $connection_upgrade;",
		"proxy_read_timeout ${DOCKYARD_PROXY_READ_TIMEOUT};", "client_max_body_size ${DOCKYARD_MAX_BODY_SIZE};",
		"resolver 127.0.0.11",
	} {
		if !strings.Contains(nginx, want) {
			t.Errorf("nginx template lacks %q", want)
		}
	}
	if strings.Contains(nginx, "proxy_buffering off") {
		t.Error("nginx must keep response buffering on; the manager disables it per stream (X-Accel-Buffering: no)")
	}
	traefik := load(t, "deploy/traefik/compose.yaml").Services["traefik"]
	if !slices.Contains(traefik.Command, "--entryPoints.websecure.transport.respondingTimeouts.readTimeout=0s") {
		t.Errorf("traefik must disable the 60 s readTimeout that cuts streams: %v", traefik.Command)
	}
	dyn := read(t, "deploy/traefik/dynamic/dockyard.yml")
	for _, want := range []string{"url: http://dockyard-manager:8080", "passHostHeader: true", "certResolver: letsencrypt"} {
		if !strings.Contains(dyn, want) {
			t.Errorf("traefik dynamic config lacks %q", want)
		}
	}
	// Every default proxy idle timeout leaves room for several heartbeats.
	for _, f := range []string{"deploy/caddy/compose.yaml", "deploy/nginx/compose.yaml", "deploy/caddy/.env.example", "deploy/nginx/.env.example"} {
		m := regexp.MustCompile(`DOCKYARD_PROXY_READ_TIMEOUT(?::-|=)(\d+s)`).FindStringSubmatch(read(t, f))
		if m == nil {
			t.Errorf("%s: no DOCKYARD_PROXY_READ_TIMEOUT default", f)
			continue
		}
		d, err := time.ParseDuration(m[1])
		if err != nil || d < 3*sse.DefaultHeartbeat {
			t.Errorf("%s: read timeout %s is not well above the %s heartbeat", f, m[1], sse.DefaultHeartbeat)
		}
	}
}

// TestE2EComposeMatchesDeploy: the proxy E2E stack runs the deploy/ proxy
// images, arguments and configuration files.
func TestE2EComposeMatchesDeploy(t *testing.T) {
	e2e := load(t, "e2e/compose.yaml")
	mounts := map[string]string{
		"caddy":   "../deploy/caddy/Caddyfile:/etc/caddy/Caddyfile:ro",
		"traefik": "../deploy/traefik/dynamic:/etc/traefik/dynamic:ro",
		"nginx":   "../deploy/nginx/templates:/etc/nginx/templates:ro",
	}
	for dir, proxy := range proxies {
		dep := load(t, "deploy/"+dir+"/compose.yaml").Services[proxy]
		got, ok := e2e.Services[proxy]
		if !ok {
			t.Fatalf("e2e lacks %s", proxy)
		}
		if got.Image != dep.Image {
			t.Errorf("%s image %q differs from deploy %q", proxy, got.Image, dep.Image)
		}
		if !slices.Contains(got.Volumes, mounts[proxy]) {
			t.Errorf("%s does not mount the deploy config %q: %v", proxy, mounts[proxy], got.Volumes)
		}
		want := make([]string, 0, len(dep.Command))
		for _, a := range dep.Command {
			want = append(want, interp(a))
		}
		if !slices.Equal(got.Command, want) {
			t.Errorf("%s arguments differ from deploy:\n got  %v\n want %v", proxy, got.Command, want)
		}
	}
}
