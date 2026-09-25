// Package deploy checks the deployment examples in deploy/ (#27, #28) as
// files, without Docker or a network: topology, pinning, volumes, proxy
// settings and the variables each example sets. Nothing here starts the
// examples; running them through each proxy is not automated.
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
			if m.Image != "code.neureka.dev/dockyard/dockyard-manager:edge" || a.Image != "code.neureka.dev/dockyard/dockyard-agent:edge" {
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
			checkStacksVolume(t, c, a)
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
	checkStacksVolume(t, c, a)
}

// checkStacksVolume enforces the #28 stacks volume: a fixed volume name
// (the agent's DOCKYARD_STACKS_VOLUME default) mounted into the agent at its
// own mountpoint, i.e. the identical path under Docker's volume directory.
func checkStacksVolume(t *testing.T, c composeFile, agent service) {
	t.Helper()
	const mnt = "dockyard_stacks:/var/lib/docker/volumes/dockyard_stacks/_data"
	if !slices.Contains(agent.Volumes, mnt) {
		t.Errorf("agent lacks %q", mnt)
	}
	v, ok := c.Volumes["dockyard_stacks"].(map[string]any)
	if !ok || v["name"] != "dockyard_stacks" {
		t.Errorf("volume dockyard_stacks must be declared with name: dockyard_stacks (got %v)", c.Volumes["dockyard_stacks"])
	}
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

var (
	configVarRE  = regexp.MustCompile(`"(DOCKYARD_[A-Z0-9_]+)"`)
	envExampleRE = regexp.MustCompile(`(?m)^#?\s*(DOCKYARD_[A-Z0-9_]+)=`)
	composeRefRE = regexp.MustCompile(`\$\{(DOCKYARD_[A-Z0-9_]+)(:?[-?][^}]*)?\}`)
)

// configVars returns the DOCKYARD_* variables a config package reads.
func configVars(t *testing.T, rel string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range configVarRE.FindAllStringSubmatch(read(t, rel), -1) {
		out[m[1]] = true
	}
	if len(out) < 5 {
		t.Fatalf("%s: found only %d variables", rel, len(out))
	}
	return out
}

// TestExampleVariablesAreKnown (#12, #27): the examples run the published
// edge images and set only variables the manager and the agent read (a
// renamed setting would otherwise be ignored silently); every variable of
// an .env.example is used by its example, and every variable an example
// needs without a default is in its .env.example, so
// `cp .env.example .env && docker compose up -d` works as documented.
func TestExampleVariablesAreKnown(t *testing.T) {
	manager := configVars(t, "internal/manager/config/config.go")
	agent := configVars(t, "internal/agent/config/config.go")
	for _, dir := range []string{"caddy", "traefik", "nginx", "remote-agent"} {
		t.Run(dir, func(t *testing.T) {
			base := "deploy/" + dir
			c := load(t, base+"/compose.yaml")
			for name, known := range map[string]map[string]bool{"dockyard-manager": manager, "dockyard-agent": agent} {
				svc, ok := c.Services[name]
				if !ok {
					if name == "dockyard-agent" || dir != "remote-agent" {
						t.Errorf("no service %s", name)
					}
					continue
				}
				if svc.Image != "code.neureka.dev/dockyard/"+name+":edge" {
					t.Errorf("%s image %q, want the published edge image", name, svc.Image)
				}
				for k := range svc.Environment {
					if strings.HasPrefix(k, "DOCKYARD_") && !known[k] {
						t.Errorf("%s sets %s, which %s does not read", name, k, name)
					}
				}
			}
			// Everything in the example directory except .env.example.
			var used strings.Builder
			err := filepath.WalkDir(filepath.Join(repoRoot(t), filepath.FromSlash(base)), func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || d.Name() == ".env.example" {
					return err
				}
				b, err := os.ReadFile(p)
				used.Write(b)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			listed := map[string]bool{}
			for _, m := range envExampleRE.FindAllStringSubmatch(read(t, base+"/.env.example"), -1) {
				listed[m[1]] = true
				if !strings.Contains(used.String(), m[1]) {
					t.Errorf(".env.example sets %s, which the example never uses", m[1])
				}
			}
			for _, m := range composeRefRE.FindAllStringSubmatch(read(t, base+"/compose.yaml"), -1) {
				if (m[2] == "" || strings.Contains(m[2], "?")) && !listed[m[1]] {
					t.Errorf("compose.yaml needs %s without a default, but .env.example does not list it", m[1])
				}
			}
		})
	}
}
