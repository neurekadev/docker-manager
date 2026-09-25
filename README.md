# DockYard

DockYard is a self-hosted Docker management platform: one **manager** with a
web UI and API controls Docker Engines on several machines through
outbound-only **agents**. Each agent plus the Engine it controls is an
**Environment**. Compose stacks, containers, images, volumes, networks, logs,
terminals, files, scheduled updates, pruning and restic backups are managed
from one place.

## Status

v1 is in release acceptance (neurekadev/dockyard#12; the roadmap in
neurekadev/dockyard#1 tracks what is still open). The code lives at
<https://code.neureka.dev/dockyard/dockyard>. There are no versioned releases yet: `main` publishes the rolling
`edge` images
`code.neureka.dev/dockyard/dockyard-manager:edge` and
`code.neureka.dev/dockyard/dockyard-agent:edge` (linux/amd64 and linux/arm64).

## Quick start

On a Linux host with Docker Engine 25.0+ and the Compose plugin, with a DNS
name pointing at it:

```bash
echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin   # packages are private
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
- [Security review v1](docs/security/review-v1.md) · [Verification matrix](docs/testing/verification-matrix.md)
- [Architecture overview](docs/architecture/overview.md) · [Engine integration](docs/architecture/engine-integration.md)
- API: [conventions](docs/api/conventions.md) · [streams](docs/api/streams.md) ·
  OpenAPI [`api/openapi.json`](api/openapi.json) · agent protocol [`agent-v1.md`](docs/protocol/agent-v1.md)
- [Development guide](docs/development.md) · [Code conventions](CLAUDE.md) · [Test harness](docs/testing/harness.md)
- ADRs: [0001 foundation](docs/adr/0001-foundation.md) · [0002 frontend libraries](docs/adr/0002-frontend-libraries.md) ·
  [0003 auth libraries](docs/adr/0003-auth-libraries.md)
- Roadmap: issue #1 · Decision register: issue #25 · Project board: DockYard v1 Roadmap

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
e2e/                   Playwright specs (TLS proxies, devstack)
deploy/                Dockerfiles and Compose examples
docs/                  guide, architecture, API, operations, testing, security
scripts/               local and CI gates (bash), deploy smoke test
test/                  deployment checks, devstack, fixtures, verification map checks
```

Local gate: `bash scripts/check.sh`.
