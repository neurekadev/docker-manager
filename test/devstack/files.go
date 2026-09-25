package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	agentfiles "github.com/neurekadev/dockyard/internal/agent/files"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/agent/storage"
	"github.com/neurekadev/dockyard/internal/agent/watch"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/protocol"
)

// Scoped files (#15) and the file watcher (#23) on the devstack: each host
// gets a real "Docker volume directory" below the data directory
// (<data>/hosts/<host>/volumes) holding its volumes' _data directories and
// the stacks volume (dockyard_stacks/_data) with the stacks' project
// directories. The agent serves them with the production file service and
// watcher, so the web file manager, editor conflicts and external edits
// can be tried without Docker: edit a file below the printed directory and
// an open listing refreshes.

// hostDirs returns a host's volume directory and stacks volume directory
// (absolute, slash-separated: the form agents report host paths in).
func hostDirs(dataDir, host string) (volumes, stacks string) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	volumes = filepath.ToSlash(filepath.Join(abs, "hosts", host, "volumes"))
	return volumes, volumes + "/dockyard_stacks/_data"
}

// definitionNames are the Compose sources compose.read reports.
var definitionNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
	"compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml", ".env"}

// readProject reads a project directory's Compose sources from disk (the
// simulated compose.read), so saves through the file manager and external
// edits become stack revisions like on a real agent.
func readProject(dir string) (protocol.SourceSnapshot, bool) {
	var files []protocol.SourceFile
	for _, name := range definitionNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		files = append(files, protocol.SourceFile{Path: name, Content: b, SHA256: protocol.FileHash(b), Size: int64(len(b))})
	}
	if len(files) == 0 {
		return protocol.SourceSnapshot{Files: []protocol.SourceFile{}}, false
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return protocol.SourceSnapshot{Hash: protocol.SourceHash(files), Files: files}, true
}

// writeHostFiles creates the host's directories and seeds the stacks'
// project directories and the volumes with sample content.
func writeHostFiles(h *homelabHost) error {
	vols, err := h.engine.ListVolumes(context.Background())
	if err != nil {
		return err
	}
	for _, v := range vols {
		if err := os.MkdirAll(filepath.FromSlash(v.Mountpoint), 0o750); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.FromSlash(h.stacksDir), 0o750); err != nil {
		return err
	}
	for name, p := range h.projects {
		dir := filepath.Join(filepath.FromSlash(h.stacksDir), name)
		for rel, content := range p.files {
			if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), content); err != nil {
				return err
			}
		}
	}
	for vol, files := range volumeSeeds {
		for _, v := range vols {
			if v.Name != vol {
				continue
			}
			for rel, content := range files {
				if err := writeFile(filepath.Join(filepath.FromSlash(v.Mountpoint), filepath.FromSlash(rel)), content); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// siloExtras is the rest of the mockup's Silo project tree (config, data,
// logs, nginx.conf, appsettings.json, README.md) plus a large directory
// for the file list's windowed rendering.
func siloExtras() map[string]string {
	files := map[string]string{
		"nginx.conf":              siloNginx,
		"appsettings.json":        siloAppSettings,
		"README.md":               siloReadme,
		"config/silo.yaml":        "storage:\n  root: /data\n  thumbnails: true\nlog_level: info\n",
		"config/users.json":       "[\n  { \"name\": \"admin\", \"role\": \"owner\" }\n]\n",
		"logs/api.log":            "2026-09-24T10:14:23Z info Starting API on :8080\n2026-09-24T10:15:02Z info GET /health 200 2ms\n",
		"logs/worker.log":         "2026-09-24T10:14:25Z Worker started, pid 1\n",
		"data/uploads/.gitkeep":   "",
		"data/archive/notes.txt":  "Backups of the old photo library.\n",
		".dockerignore":           "node_modules\n.git\n",
		"scripts/healthcheck.sh":  "#!/bin/sh\nwget -qO- http://localhost:8080/health || exit 1\n",
		"scripts/rotate-logs.sh":  "#!/bin/sh\nfind /data/logs -name '*.log' -mtime +7 -delete\n",
		"data/uploads/avatar.svg": "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"16\" height=\"16\"/>\n",
	}
	for i := range 1200 {
		files[fmt.Sprintf("data/thumbnails/img-%04d.jpg.meta", i)] = fmt.Sprintf("{\"id\":%d,\"w\":320,\"h\":240}\n", i)
	}
	return files
}

// volumeSeeds is sample content of the seeded volumes.
var volumeSeeds = map[string]map[string]string{
	"silo_db-data": {
		"PG_VERSION":       "16\n",
		"postgresql.conf":  "listen_addresses = '*'\nmax_connections = 100\nshared_buffers = 128MB\n",
		"pg_hba.conf":      "host all all all scram-sha-256\n",
		"base/1/.keep":     "",
		"pg_wal/.keep":     "",
		"postmaster.opts":  "/usr/lib/postgresql/16/bin/postgres\n",
		"log/postgres.log": "2026-09-24 10:14:24 UTC LOG:  database system is ready to accept connections\n",
	},
	"silo_redis-data": {"dump.rdb.txt": "REDIS0011 (sample)\n", "appendonlydir/appendonly.aof.1.base.aof": "*2\r\n$6\r\nSELECT\r\n$1\r\n0\r\n"},
	"homeassistant_config": {
		"configuration.yaml": "homeassistant:\n  name: Home\n  unit_system: metric\n  time_zone: Europe/Berlin\n\nautomation: !include automations.yaml\n",
		"automations.yaml":   "- alias: Lights off at midnight\n  trigger:\n    - platform: time\n      at: \"00:00:00\"\n  action:\n    - service: light.turn_off\n      target:\n        entity_id: all\n",
		"secrets.yaml":       "mqtt_password: devstack-not-a-secret\n",
		"blueprints/.keep":   "",
	},
	"media_config": {"system.xml": "<ServerConfiguration>\n  <ServerName>homelab</ServerName>\n</ServerConfiguration>\n"},
}

const siloNginx = `server {
    listen 80;
    server_name _;

    location / {
        root /usr/share/nginx/html;
        try_files $uri /index.html;
    }

    location /api/ {
        proxy_pass http://silo-api:8080/;
    }
}
`

const siloAppSettings = `{
  "Logging": { "LogLevel": { "Default": "Information" } },
  "Storage": { "Root": "/data", "MaxUploadMb": 512 },
  "Features": { "Thumbnails": true, "Sharing": false }
}
`

const siloReadme = "# Silo\n\nPersonal cloud and media platform.\n\n## Services\n\n- **silo-web**: the web frontend on port 8080\n- **silo-api**: the backend API\n- **silo-db**: PostgreSQL 16 (volume `db-data`)\n- **silo-redis**: cache\n- **silo-worker**: thumbnails and transcoding\n\n## Maintenance\n\n1. Deploy after editing `compose.yaml` or `.env`.\n2. Logs rotate weekly (`scripts/rotate-logs.sh`).\n\nSee [the Compose docs](https://docs.docker.com/compose/) for the file format.\n"

// fileServing wires the production file service and watcher into a
// devstack agent.
type fileServing struct {
	svc     *agentfiles.Service
	watcher *watch.Watcher
	roots   []protocol.Root
}

// newFileServing builds the agent's file service over the host's
// directories. client is set once the session exists (invalidations before
// that are dropped).
func newFileServing(h *homelabHost, clk clock.Clock, log *slog.Logger, client *atomic.Pointer[session.Client]) *fileServing {
	st := &storage.Result{DockerRootDir: strings.TrimSuffix(h.volumesDir, "/volumes"), StacksDir: h.stacksDir,
		VolumesDir: h.volumesDir, Roots: []storage.Root{
			{Kind: storage.KindStacks, Path: h.stacksDir, OK: true},
			{Kind: storage.KindVolumes, Path: h.volumesDir, OK: true}}}
	invalidate := func(p protocol.FSInvalidationPayload) {
		if c := client.Load(); c != nil {
			c.FileInvalidations().Publish(p)
		}
	}
	fe := h.engine
	svc := agentfiles.New(agentfiles.Options{
		Engine:  func() agentfiles.Engine { return fe },
		Storage: func() *storage.Result { return st },
		Clock:   clk, Logger: log, Invalidate: invalidate,
	})
	// Kernel notifications where the platform has them (inotify, or
	// ReadDirectoryChangesW through fsnotify on Windows), a short poll
	// otherwise.
	w := watch.New(watch.Options{Resolve: svc.ScopeDir, Clock: clk, Logger: log, Invalidate: invalidate,
		PollInterval: 2 * time.Second})
	mode := protocol.WatchInotify
	if !w.Notifying() {
		mode = protocol.WatchPoll
	}
	return &fileServing{svc: svc, watcher: w, roots: []protocol.Root{
		{Kind: protocol.RootStacks, Path: h.stacksDir, Watch: mode},
		{Kind: storage.KindVolumes, Path: h.volumesDir, Watch: mode}}}
}

// requests are the files.* requests plus files.watch.
func (f *fileServing) requests() map[string]session.RequestHandler {
	reqs := f.svc.Requests()
	reqs[protocol.ReqFilesWatch] = func(ctx context.Context, input json.RawMessage) (any, error) {
		var in protocol.FilesWatchInput
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: "malformed files.watch input"}
		}
		if err := in.Validate(); err != nil {
			return nil, &session.HandlerError{Code: protocol.CodeInvalidFrame, Message: err.Error()}
		}
		return f.watcher.SetScopes(ctx, in), nil
	}
	return reqs
}

func (f *fileServing) streams() map[string]session.StreamHandler { return f.svc.Streams() }

func (f *fileServing) executors() []jobexec.Executor { return f.svc.Executors() }
