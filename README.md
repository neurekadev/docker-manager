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
`code.neureka.dev/docker-manager/docker-manager:edge` and
`code.neureka.dev/docker-manager/docker-agent:edge` (linux/amd64 only; arm64
images follow once a native arm64 build runner exists).

The code lives at <https://code.neureka.dev/docker-manager/docker-manager> (Forgejo,
private). The GitHub issues of `neurekadev/dockyard` (`#N` in these docs)
remain the written record of the roadmap and decisions.

## Quick start

On a Linux amd64 host with Docker Engine 25.0+ and the Compose plugin, with
a DNS name pointing at it. The registry is private: log in with your
Forgejo username and a Forgejo access token with the `read:package` scope.

```bash
echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin
git clone https://code.neureka.dev/docker-manager/docker-manager.git && cd docker-manager/deploy/caddy
cp .env.example .env        # set DOCKER_MANAGER_HOST=docker.example.com (and DOCKER_MANAGER_TLS)
docker compose up -d        # manager + co-located agent + Caddy, from the :edge images
```

Then open `https://docker.example.com`, create the owner account, and enroll
the co-located agent from **Environments → Add environment**:

```bash
printf '%s\n' '<token>' | docker compose exec -T docker-agent docker-agent enroll
```

Traefik and nginx variants live next to it (`deploy/traefik`,
`deploy/nginx`), agents for other hosts in `deploy/remote-agent`. Step by
step: the [user documentation](docs/public/content/docs/index.mdx).

## Documentation

- **User documentation** ([`docs/public`](docs/public/content/docs/index.mdx), published as the
  `code.neureka.dev/docker-manager/docker-manager-docs:edge` site image): overview, quickstart,
  usage, configuration and troubleshooting.

For operators and contributors (`docs/internal`):

- Operations: [upgrades and rollback](docs/internal/operations/upgrades.md) ·
  [removing hosts](docs/internal/operations/removing-hosts.md) · [diagnostics](docs/internal/operations/diagnostics.md)
- [Support matrix](docs/internal/support-matrix.md) (hosts, Engine versions, Compose features, browsers, versions)
- [Deploying with Docker Compose](deploy/README.md) · [Deployment topology](docs/internal/deployment.md) ·
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
deploy/                Dockerfiles and Compose examples
docs/public            user documentation site (Fumadocs, served by nginx)
docs/internal          conventions, architecture, API, protocol, operations, security
scripts/               local gate, code generation, static builds, policy and license checks (bash)
test/deploy            static checks of the deploy examples
```

## Checks

The local gate `bash scripts/check.sh` mirrors CI (`.github/workflows/CI.yaml`,
Forgejo Actions, on pushes to `main`): `lint` (gofmt, Prettier,
golangci-lint, repository policy, ESLint), `unit-tests` (`go test ./...`,
Vitest) and `build` (web build, `go build`, static linux/amd64 and
linux/arm64 binaries). The tests are isolated unit tests with in-memory
fakes; nothing starts Docker, a browser or a real registry. Details:
[development guide](docs/internal/development.md#local-gate-and-ci).
