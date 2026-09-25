package main

import (
	"context"
	"sort"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginefake"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// The seeded homelab: "homelab" (the mockup's stack "silo" plus a media
// stack and standalone containers), "nas" (file services and stopped
// leftovers for the prune demo) and "edge" (a Raspberry Pi whose agent
// disconnects after seeding, so the UI has an offline environment).

// usage is a container's simulated load: CPU in % of the host's cores and
// resident memory.
type usage struct {
	cpu    float64
	memory int64
}

const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

type homelabHost struct {
	name, hostname, os, kernel string
	serviceAddress             string
	cpus                       int
	memory, baseMemory         int64
	baseCPU                    float64
	diskUsed, diskTotal        int64
	uptime                     time.Duration
	offline                    bool
	engine                     *devEngine
	projects                   map[string]*project
	stacks                     []stackSeed
	usage                      map[string]usage
	logLines                   map[string][]string
	// volumesDir is the host's "Docker volume directory" and stacksDir its
	// stacks volume inside it: real directories below the data directory
	// (files.go), so the file manager (#15) and watcher (#23) work.
	volumesDir, stacksDir string
}

// project is a Compose project's files as the stacks volume holds them
// (written to the host's stacks directory at start, files.go).
type project struct {
	files map[string]string
}

// stackSeed is a DockYard stack with display metadata (#22: descriptions
// and icons are DockYard metadata, never written to Compose files).
type stackSeed struct {
	name, displayName, description, icon string
	services                             []serviceSeed
	deployedAgo                          time.Duration
}

type serviceSeed struct {
	name, image, description, icon string
	ports                          []engine.PortBinding
	health                         string
	running                        bool
	volume                         string
}

func (h *homelabHost) running() []enginefake.Container {
	cs, _ := h.engine.ListContainers(context.Background(), engine.ContainerFilter{})
	out := make([]enginefake.Container, 0, len(cs))
	for _, c := range cs {
		if d, ok := h.engine.Container(c.ID); ok {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Details.Name < out[j].Details.Name })
	return out
}

func (h *homelabHost) profile(name string) usage {
	if u, ok := h.usage[name]; ok {
		return u
	}
	return usage{cpu: 0.4, memory: 64 * mib}
}

const siloCompose = `services:
  silo-web:
    image: ghcr.io/silo/web:latest
    container_name: silo-web
    restart: unless-stopped
    ports:
      - "8080:80"
    environment:
      - API_URL=http://silo-api:8080
      - TZ=UTC
    depends_on:
      - silo-api
  silo-api:
    image: ghcr.io/silo/api:latest
    restart: unless-stopped
    ports:
      - "8081:8080"
    env_file: .env
    depends_on:
      silo-db:
        condition: service_healthy
      silo-redis:
        condition: service_started
  silo-db:
    image: postgres:16
    restart: unless-stopped
    ports:
      - "5432:5432"
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "silo"]
  silo-redis:
    image: redis:7-alpine
    restart: unless-stopped
    ports:
      - "6379:6379"
    volumes:
      - redis-data:/data
  silo-worker:
    image: ghcr.io/silo/worker:latest
    restart: unless-stopped
    env_file: .env
    depends_on:
      - silo-api

volumes:
  db-data:
  redis-data:
`

const siloEnv = `POSTGRES_USER=silo
POSTGRES_DB=silo
POSTGRES_PASSWORD=devstack-not-a-secret
REDIS_URL=redis://silo-redis:6379/0
`

const mediaCompose = `services:
  jellyfin:
    image: jellyfin/jellyfin:10.10
    restart: unless-stopped
    ports:
      - "8096:8096"
    volumes:
      - config:/config
      - /srv/media:/media:ro

volumes:
  config:
`

func newHomelab(dataDir string) []*homelabHost {
	homelab := &homelabHost{name: "homelab", hostname: "homelab", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-25-amd64",
		serviceAddress: "192.168.1.10", cpus: 4, memory: 8 * gib, baseMemory: 520 * mib, baseCPU: 3,
		diskUsed: 212 * gib, diskTotal: 480 * gib, uptime: 14 * 24 * time.Hour,
		projects: map[string]*project{
			"silo":  {files: map[string]string{"compose.yaml": siloCompose, ".env": siloEnv}},
			"media": {files: map[string]string{"compose.yaml": mediaCompose}},
		},
		stacks: []stackSeed{
			{name: "silo", displayName: "Silo", description: "Personal cloud and media platform", icon: "layers", deployedAgo: 49 * time.Hour,
				services: []serviceSeed{
					{name: "silo-web", image: "ghcr.io/silo/web:latest", description: "Web frontend", icon: "globe", running: true,
						ports: []engine.PortBinding{{ContainerPort: 80, HostPort: 8080}}},
					{name: "silo-api", image: "ghcr.io/silo/api:latest", description: "Backend API", icon: "box", running: true,
						ports: []engine.PortBinding{{ContainerPort: 8080, HostPort: 8081}}},
					{name: "silo-db", image: "postgres:16", description: "PostgreSQL", icon: "database", running: true, health: "healthy",
						ports: []engine.PortBinding{{ContainerPort: 5432, HostPort: 5432}}, volume: "silo_db-data:/var/lib/postgresql/data"},
					{name: "silo-redis", image: "redis:7-alpine", description: "Redis cache", icon: "layers", running: true,
						ports: []engine.PortBinding{{ContainerPort: 6379, HostPort: 6379}}, volume: "silo_redis-data:/data"},
					{name: "silo-worker", image: "ghcr.io/silo/worker:latest", description: "Background worker", icon: "cog", running: true},
				}},
			{name: "media", displayName: "Media", description: "Jellyfin media server", icon: "film", deployedAgo: 9 * 24 * time.Hour,
				services: []serviceSeed{
					{name: "jellyfin", image: "jellyfin/jellyfin:10.10", description: "Media server", icon: "clapperboard", running: true,
						ports: []engine.PortBinding{{ContainerPort: 8096, HostPort: 8096}}, volume: "media_config:/config"},
				}},
		},
		usage: map[string]usage{
			"silo-silo-web-1": {2.4, 312 * mib}, "silo-silo-api-1": {3.1, 420 * mib}, "silo-silo-db-1": {1.2, 512 * mib},
			"silo-silo-redis-1": {0.7, 128 * mib}, "silo-silo-worker-1": {2.1, 256 * mib}, "media-jellyfin-1": {4.5, 780 * mib},
			"homeassistant": {1.8, 350 * mib}, "pihole": {0.3, 90 * mib},
		},
		logLines: map[string][]string{
			"silo-silo-web-1":    {"Starting nginx 1.27.3", "/docker-entrypoint.sh: configuration complete", `192.168.1.23 - - "GET / HTTP/1.1" 200 1532`, `192.168.1.23 - - "GET /api/health HTTP/1.1" 200 17`},
			"silo-silo-api-1":    {"[info] Starting API on :8080", "[info] connected to postgres at silo-db:5432", "[info] GET /health 200 2ms", "[info] POST /api/uploads 201 48ms"},
			"silo-silo-db-1":     {"database system is ready to accept connections", "checkpoint starting: time", "checkpoint complete: wrote 42 buffers (0.3%)"},
			"silo-silo-redis-1":  {"Ready to accept connections tcp", "1 changes in 3600 seconds. Saving...", "Background saving terminated with success"},
			"silo-silo-worker-1": {"Worker started, pid 1", "Processing queue item 1824 (thumbnail)", "Processing queue item 1825 (transcode)", "Queue empty, sleeping 5s"},
		},
	}
	nas := &homelabHost{name: "nas", hostname: "nas", os: "Ubuntu 24.04.1 LTS", kernel: "6.8.0-45-generic", cpus: 4, memory: 16 * gib,
		baseMemory: 900 * mib, baseCPU: 2, diskUsed: 5 * 1024 * gib, diskTotal: 8 * 1024 * gib, uptime: 41 * 24 * time.Hour,
		projects: map[string]*project{},
		usage:    map[string]usage{"syncthing": {1.1, 180 * mib}, "samba": {0.2, 40 * mib}},
		logLines: map[string][]string{"syncthing": {"INF Ready to synchronize \"photos\" (sendreceive)", "INF Completed initial scan of \"photos\""}},
	}
	edge := &homelabHost{name: "edge", hostname: "edge-pi", os: "Raspberry Pi OS (bookworm)", kernel: "6.6.51+rpt-rpi-v8", cpus: 4,
		memory: 4 * gib, baseMemory: 300 * mib, baseCPU: 1, diskUsed: 21 * gib, diskTotal: 58 * gib, uptime: 3 * 24 * time.Hour,
		offline: true, projects: map[string]*project{}, usage: map[string]usage{"mqtt-broker": {0.4, 20 * mib}}}

	for i, h := range []*homelabHost{homelab, nas, edge} {
		fe := enginefake.New([]string{"4f6c:7a1e:homelab", "9b2d:0c3f:nas", "1e8a:55d0:edge"}[i])
		h.engine = &devEngine{Engine: fe, logs: func(name string) []string { return h.logLines[name] }}
		h.volumesDir, h.stacksDir = hostDirs(dataDir, h.name)
		fe.SetVolumeRoot(h.volumesDir)
	}
	for rel, content := range siloExtras() {
		homelab.projects["silo"].files[rel] = content
	}
	homelab.engine.SetPlatform("linux", "amd64")
	nas.engine.SetPlatform("linux", "amd64")
	edge.engine.SetPlatform("linux", "arm64")

	// homelab: the stacks' containers, standalone containers, leftovers.
	for _, st := range homelab.stacks {
		addStack(homelab.engine, homelab.stacksDir, st)
	}
	fe := homelab.engine
	fe.AddContainer(engine.ContainerSpec{Name: "homeassistant", Image: "ghcr.io/home-assistant/home-assistant:2026.9", RestartPolicy: "unless-stopped",
		NetworkMode: "host", Mounts: []engine.MountSpec{{Type: "volume", Source: "homeassistant_config", Target: "/config"}}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "pihole", Image: "pihole/pihole:2026.08", RestartPolicy: "unless-stopped",
		Ports: []engine.PortBinding{{ContainerPort: 53, HostPort: 53, Protocol: "udp"}, {ContainerPort: 80, HostPort: 8053}}}, true)
	exited(fe, engine.ContainerSpec{Name: "backup-runner", Image: "alpine:3.20"})
	fe.AddImage("alpine:3.20")
	fe.AddImage("postgres:15")

	// nas: file services and stopped leftovers (the prune run's candidates).
	fe = nas.engine
	fe.AddContainer(engine.ContainerSpec{Name: "syncthing", Image: "syncthing/syncthing:1.29", RestartPolicy: "unless-stopped",
		Ports: []engine.PortBinding{{ContainerPort: 8384, HostPort: 8384}}, Mounts: []engine.MountSpec{{Type: "volume", Source: "syncthing_data", Target: "/var/syncthing"}}}, true)
	fe.AddContainer(engine.ContainerSpec{Name: "samba", Image: "dperson/samba:latest", RestartPolicy: "always",
		Ports: []engine.PortBinding{{ContainerPort: 445, HostPort: 445}}}, true)
	exited(fe, engine.ContainerSpec{Name: "old-nextcloud", Image: "nextcloud:28"})
	exited(fe, engine.ContainerSpec{Name: "tmp-builder", Image: "golang:1.22"})
	exited(fe, engine.ContainerSpec{Name: "speedtest", Image: "ghcr.io/librespeed/speedtest:5.4"})
	fe.AddVolume("nextcloud_data", nil)

	// edge: one broker, disconnected after seeding.
	edge.engine.AddContainer(engine.ContainerSpec{Name: "mqtt-broker", Image: "emqx/emqx:5.8", RestartPolicy: "unless-stopped",
		Ports: []engine.PortBinding{{ContainerPort: 1883, HostPort: 1883}}}, true)
	return []*homelabHost{homelab, nas, edge}
}

// addStack creates a stack's network, volumes and containers with the
// Compose labels the agent classifies them by.
func addStack(fe *devEngine, stacksDir string, st stackSeed) {
	network := st.name + "_default"
	fe.AddNetwork(network, map[string]string{protocol.ComposeProjectLabel: st.name, "com.docker.compose.network": "default"})
	for _, svc := range st.services {
		labels := map[string]string{protocol.ComposeProjectLabel: st.name, protocol.ComposeServiceLabel: svc.name,
			protocol.ComposeWorkingDirLabel: stacksDir + "/" + st.name, "com.docker.compose.container-number": "1",
			"com.docker.compose.project.config_files": stacksDir + "/" + st.name + "/compose.yaml"}
		spec := engine.ContainerSpec{Name: st.name + "-" + svc.name + "-1", Image: svc.image, NetworkMode: network, Labels: labels,
			RestartPolicy: "unless-stopped", Ports: svc.ports, NetworkAliases: []string{svc.name}}
		if svc.volume != "" {
			name, target, _ := cut(svc.volume)
			fe.AddVolume(name, map[string]string{protocol.ComposeProjectLabel: st.name, "com.docker.compose.volume": name})
			spec.Mounts = []engine.MountSpec{{Type: "volume", Source: name, Target: target}}
		}
		if svc.health != "" {
			spec.Healthcheck = &engine.HealthcheckSpec{Test: []string{"CMD", "true"}}
			fe.AddImage(svc.image)
			fe.SetStartHealth(svc.image, svc.health)
		}
		id := fe.AddContainer(spec, false)
		if svc.running {
			_ = fe.StartContainer(context.Background(), id)
		}
	}
}

func exited(fe *devEngine, spec engine.ContainerSpec) {
	id := fe.AddContainer(spec, false)
	_ = fe.StartContainer(context.Background(), id)
	_ = fe.StopContainer(context.Background(), id, nil)
}

func cut(s string) (before, after string, ok bool) {
	for i := range len(s) {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
