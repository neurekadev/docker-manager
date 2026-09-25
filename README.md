# DockYard

DockYard is a self-hosted Docker management platform: one **manager** with a
web UI and API controls Docker Engines on several machines through
outbound-only **agents**. Each agent plus the Engine it controls is an
**Environment**. Compose stacks, containers, images, volumes, networks, logs,
terminals, files, scheduled updates, pruning and restic backups are managed
from one place.

## Status

v1 is in release acceptance (#12; the roadmap in #1 tracks what is still
open). There are no versioned releases yet: `main` publishes the rolling
`edge` images
`code.neureka.dev/dockyard/dockyard-manager:edge` and
`code.neureka.dev/dockyard/dockyard-agent:edge` (linux/amd64 only; arm64
images follow once a native arm64 build runner exists).

The code lives at <https://code.neureka.dev/dockyard/dockyard> (Forgejo,
private). The GitHub issues of `neurekadev/dockyard` (`#N` in these docs)
remain the written record of the roadmap and decisions.

## Quick start

On a Linux amd64 host with Docker Engine 25.0+ and the Compose plugin, with
a DNS name pointing at it. The registry is private: log in with your
Forgejo username and a Forgejo access token with the `read:package` scope.

```bash
echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin
git clone https://code.neureka.dev/dockyard/dockyard.git && cd dockyard/deploy/caddy
cp .env.example .env        # set DOCKYARD_HOST=docker.example.com (and DOCKYARD_TLS)
docker compose up -d        # manager + co-located agent + Caddy, from the :edge images
```

Then open `https://docker.example.com`, create the owner account, and enroll
the co-located agent from **Environments → Add environment**:

```bash
printf '%s\n' '<token>' | docker compose exec -T dockyard-agent dockyard-agent enroll
```

Traefik and nginx variants live next to it (`deploy/traefik`,
`deploy/nginx`), agents for other hosts in `deploy/remote-agent`. Step by
step: the [user and administrator guide](docs/guide/README.md).

## Documentation

- Guide: [deployment](docs/guide/deployment.md) · [first run](docs/guide/first-run.md) ·
  [multi-host](docs/guide/multi-host.md) · [PWA](docs/guide/pwa.md) ·
  [upgrades](docs/guide/upgrades.md) · [backup and restore](docs/guide/backup-restore.md) ·
  [policy safety](docs/guide/policy-safety.md) · [troubleshooting](docs/guide/troubleshooting.md)
- [Support matrix](docs/support-matrix.md) (hosts, Engine versions, Compose features, browsers, versions)
- [Deploying with Docker Compose](deploy/README.md) · [Deployment topology](docs/deployment.md) ·
  [Configuration reference](docs/configuration.md)
- [Security review v1](docs/security/review-v1.md) · [Verification status](docs/support-matrix.md#verification-status)
- [Architecture overview](docs/architecture/overview.md) · [Engine integration](docs/architecture/engine-integration.md)
- API: [conventions](docs/api/conventions.md) · [streams](docs/api/streams.md) ·
  OpenAPI [`api/openapi.json`](api/openapi.json) · agent protocol [`agent-v1.md`](docs/protocol/agent-v1.md)
- [Development guide](docs/development.md) · [Code conventions](CLAUDE.md)
- ADRs: [0001 foundation](docs/adr/0001-foundation.md) · [0002 frontend libraries](docs/adr/0002-frontend-libraries.md) ·
  [0003 auth libraries](docs/adr/0003-auth-libraries.md)
- Roadmap: issue neurekadev/dockyard#1 · Decision register: issue neurekadev/dockyard#25 · Project board: DockYard v1 Roadmap

## Repository layout

```
cmd/dockyard-manager   manager entry point (serve, healthcheck, enrollment, owner-recovery, snapshots, openapi, version)
cmd/dockyard-agent     agent entry point (run, enroll, healthcheck, version)
internal/manager/...   manager: app, api, auth, authz, jobs, stacks, backups, ...
internal/agent/...     agent: engine and compose adapters, files, backups, runtime
internal/protocol      manager<->agent frames (dockyard.agent/v1)
internal/domain        shared domain types
internal/db/migrations versioned Bun migrations
web/                   SvelteKit PWA (embedded into the manager)
deploy/                Dockerfiles and Compose examples
docs/                  guide, architecture, API, operations, security
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
[development guide](docs/development.md#local-gate-and-ci).
