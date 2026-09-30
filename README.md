# Docker Manager

Docker Manager is a self-hosted Docker management platform: one **manager** with a
web UI and API controls Docker Engines on several machines through
outbound-only **agents**. Each agent plus the Engine it controls is an
**Environment**. Compose stacks, containers, images, volumes, networks, logs,
terminals, files, scheduled updates, pruning and restic backups are managed
from one place.

## Status

v1 is in release acceptance (#12; the roadmap in #1 tracks what is still
open). There are no versioned releases yet: `main` publishes the rolling
`edge` images
`ghcr.io/neurekadev/docker-manager:edge` and
`ghcr.io/neurekadev/docker-agent:edge` (linux/amd64 and linux/arm64).

The code lives at <https://github.com/neurekadev/docker-manager>. Its GitHub
issues #1–#35 (`#N` in these docs) remain the written record of the roadmap
and decisions.

## Quick start

On a Linux amd64 or arm64 host with Docker Engine 25.0+ and the Compose plugin,
behind your own HTTPS reverse proxy. The
[Quickstart](docs/public/content/docs/quickstart.mdx) has the
`compose.yaml` and `.env` to copy, and
its "Add more servers" section the agent for
other hosts. The images are public; no registry login is needed.

## Documentation

- **User documentation** ([`docs/public`](docs/public/content/docs/overview.mdx), published as the
  `ghcr.io/neurekadev/docker-manager-docs:edge` site image): installation, every
  feature, configuration, upgrades and troubleshooting.

For operators and contributors (`docs/internal`):

- Operations: [upgrades and rollback](docs/internal/operations/upgrades.md) ·
  [removing hosts](docs/internal/operations/removing-hosts.md) · [diagnostics](docs/internal/operations/diagnostics.md)
- [Support matrix](docs/internal/support-matrix.md) (hosts, Engine versions, Compose features, browsers, versions)
- [Deployment topology](docs/internal/deployment.md) ·
  [Configuration reference](docs/internal/configuration.md)
- [Security review v1](docs/internal/security/review-v1.md) · [Verification status](docs/internal/support-matrix.md#verification-status)
- [Architecture overview](docs/internal/architecture/overview.md) · [Engine integration](docs/internal/architecture/engine-integration.md)
- API: [conventions](docs/internal/api/conventions.md) · [streams](docs/internal/api/streams.md) ·
  OpenAPI [`api/openapi.json`](api/openapi.json) · agent protocol [`agent-v1.md`](docs/internal/protocol/agent-v1.md)
- [Development guide](docs/internal/development.md) · [Code conventions](docs/internal/conventions/README.md)
- ADRs: [0001 foundation](docs/internal/adr/0001-foundation.md) · [0002 frontend libraries](docs/internal/adr/0002-frontend-libraries.md) ·
  [0003 auth libraries](docs/internal/adr/0003-auth-libraries.md)
- Roadmap: issue neurekadev/dockyard#1 · Decision register: issue neurekadev/dockyard#25 · Project board: Docker Manager v1 Roadmap

## Repository layout

```
cmd/docker-manager   manager entry point (serve, healthcheck, enrollment, owner-recovery, snapshots, openapi, version)
cmd/docker-agent     agent entry point (run, enroll, healthcheck, version)
internal/manager/...   manager: app, api, auth, authz, jobs, stacks, backups, ...
internal/agent/...     agent: engine and compose adapters, files, backups, runtime
internal/protocol      manager<->agent frames (docker-manager.agent/v1)
internal/domain        shared domain types
internal/db/migrations versioned Bun migrations
web/                   SvelteKit PWA (embedded into the manager)
deploy/docker          Dockerfiles of the manager and agent images
docs/public            user documentation site (Fumadocs, served by nginx)
docs/internal          conventions, architecture, API, protocol, operations, security
scripts/               local gate, code generation, static builds, policy and license checks (bash)
```

## Checks

The local gate `bash scripts/check.sh` mirrors CI (`.github/workflows/CI.yaml`,
GitHub Actions, on pushes to `main`): `lint` (gofmt, Prettier,
golangci-lint, repository policy, ESLint), `unit-tests` (`go test ./...`,
Vitest) and `build` (web build, `go build`, static linux/amd64 and
linux/arm64 binaries). The tests are isolated unit tests with in-memory
fakes; nothing starts Docker, a browser or a real registry. Details:
[development guide](docs/internal/development.md#local-gate-and-ci).
