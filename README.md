# DockYard

DockYard is a self-hosted Docker management platform: one **manager** with a
web UI and API controls Docker Engines on several machines through
outbound-only **agents**. Each agent plus the Engine it controls is an
**Environment**. Compose stacks, containers, images, volumes, networks, logs,
terminals, files, scheduled updates, pruning and restic backups are managed
from one place.

## Status

Early development (v1 in progress). The foundation is in place: the two Go
executables, SQLite persistence with migrations, the `/api/v1` conventions,
the embedded SvelteKit shell, CI and the rolling `edge` images. Features land
per the roadmap. There are no releases yet; `main` publishes
`ghcr.io/neurekadev/dockyard-manager:edge` and
`ghcr.io/neurekadev/dockyard-agent:edge`.

## Quick links

- Roadmap: issue #1 · Decision register: issue #25 · Project board: DockYard v1 Roadmap
- [Deploying with Docker Compose](deploy/README.md)
- [Configuration reference](docs/configuration.md)
- [Architecture overview](docs/architecture/overview.md)
- [API conventions](docs/api/conventions.md) · OpenAPI: [`api/openapi.json`](api/openapi.json)
- [Development guide](docs/development.md) · [Code conventions](CLAUDE.md)
- [ADR 0001: foundation](docs/adr/0001-foundation.md)

## Repository layout

```
cmd/dockyard-manager   manager entry point (serve, healthcheck, openapi, version)
cmd/dockyard-agent     agent entry point (run, healthcheck, version)
internal/manager/...   manager: app, config, server, api, store, secrets
internal/agent/...     agent: config, runtime
internal/protocol      manager<->agent frames (dockyard.agent/v1)
internal/domain        shared domain types
internal/db/migrations versioned Bun migrations
web/                   SvelteKit PWA (embedded into the manager)
deploy/                Dockerfiles and Compose example
docs/                  architecture, API, configuration, ADRs
scripts/               local and CI gates (bash)
```

Local gate: `bash scripts/check.sh`.
