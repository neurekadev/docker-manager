# Architecture overview

DockYard is a centralized manager with a web UI that controls Docker Engines
on several machines through enrolled agents. The roadmap and decisions live
in GitHub issues #1 (roadmap) and #25 (decision register).

## Components

```
 browser (PWA) ──HTTPS──┐
 API clients  ──HTTPS──┤   reverse proxy (TLS)   ┌────────────────────────┐
 remote agent ──HTTPS──┴──── one public origin ──▶│ dockyard-manager :8080 │
                                                 │  /        embedded UI  │
 co-located agent ──http (opt-in, internal)─────▶│  /api/v1  public API   │
                                                 │  /agent/v1 agent (#3)  │
                                                 │  SQLite (data volume)  │
                                                 └────────────────────────┘
 dockyard-agent (one per Docker Engine = one Environment)
   dials the manager, never listens; owns Docker socket + volume paths
```

- **dockyard-manager** — users, authorization, configuration, stacks and
  revisions, jobs, audit, metrics, persistence. Serves the embedded SvelteKit
  PWA, the public `/api/v1` API and the private `/agent/v1` endpoint on a
  single plain-HTTP listener behind the operator's TLS proxy (#27). Never
  mounts a Docker socket.
- **dockyard-agent** — outbound-only connector for one Docker Engine: Engine
  API access via the official Moby Go SDK (#21), Compose, file operations and
  restic backups on manager instructions. Runs as root with Docker's volume
  directory mounted at its identical host path (#28).
- **Images** — separate manager and agent images from `deploy/docker/`, each
  with its static binary and a pinned, checksum-verified restic. Published as
  rolling `ghcr.io/neurekadev/dockyard-{manager,agent}:edge` from `main`.

## Package boundaries

| Path | Responsibility | May import |
| --- | --- | --- |
| `cmd/dockyard-manager`, `cmd/dockyard-agent` | Entry points, subcommands, signal handling | anything below |
| `internal/manager/app` | Manager startup order and lifecycle | manager packages |
| `internal/manager/config` | Manager env configuration | `envconfig`, `logging` |
| `internal/manager/server` | HTTP mux, middleware, SPA serving | `api` |
| `internal/manager/api` | Huma operations, transport DTOs, error shape, pagination | `domain`, `protocol`, `authz` |
| `internal/manager/store` | SQLite/Bun: open, migrate, snapshots, DB models | `domain`, `db/migrations` |
| `internal/manager/secrets` | AEAD sealing of settings at rest, key file | — |
| `internal/manager/jobs` | Job engine: queue, lock matrix, dispatch, fencing, reconciliation, recovery (#26) | `store`, `authz`, `jobspec`, `jobexec`, `protocol` |
| `internal/manager/authz` | Authorization contract (#17): principals, `Resource`, `Authorizer`, per-request `Checker`, shaping (`ViewOf`), job targets, event filtering; subpackages `catalog` (capability catalog), `policy` (evaluator, corpus), `authztest` (test kit) | `domain`, `jobspec`, `events` |
| `internal/manager/permissions` | Permission service (#17): rule storage, the manager's Authorizer, Locators, owner-only group/rule management, effective permissions, previews | `store`, `authz`, `audit` |
| `internal/manager/auth` | Identity (#16): sessions middleware, sign-in/factor/invitation/user flows, owner guard and stream invalidation for #17; subpackages wrap the #18 libraries (`sessions`, `password`, `totp`, `passkey`, `csrf`, `throttle`), ADR 0003 | `api`, `store`, `authz`, `secrets`, `requestinfo` |
| `internal/manager/metrics` | Metrics database (#5): idempotent ingest, 1 min/15 min rollups, retention and storage caps, downsampled queries, stored Engine inventories | `store`, `domain`, `db/metricsmigrations` |
| `internal/manager/observe` | Observation (#5): metrics collector over the agent session, Engine inventory cache, per-environment event journal for `stream-environment-events` | `metrics`, `events`, `protocol`, `domain` |
| `internal/agent/observe` | Agent telemetry (#5): procfs host sampler, container stats via the Engine adapter, bounded sample ring, `engine.info`, Docker event relay | `agent/engine`, `agent/session`, `protocol` |
| `internal/jobspec` | Job kind catalog and lock definitions (shared by manager and agent) | `domain` |
| `internal/jobexec` | Journaled step runner (shared by manager and agent) | `jobspec`, `protocol`, `faultinject` |
| `internal/agent/jobs` | Agent job runner: fencing, fsync'd journal, reconnect report | `jobexec`, `protocol` |
| `internal/agent/resources` | Docker resource requests and job executors (#6) over the Engine adapter | `engine`, `session`, `jobexec`, `protocol` |
| `internal/manager/resources` | Docker resources of every environment (#6): agent requests, job requests, recreate specifications, Locators | `store`, `jobs`, `authz`, `permissions`, `protocol` |
| `internal/faultinject` | Named fault points, no-op unless built with `-tags faultinject` | stdlib |
| `internal/db/migrations` | Versioned Bun migrations (one file each) | `bun` |
| `internal/agent/config`, `internal/agent/runtime` | Agent configuration and main loop | `protocol`, shared |
| `internal/protocol` | Manager↔agent frame envelope (`dockyard.agent/v1`) | stdlib, websocket |
| `internal/domain` | Shared domain types, no HTTP/DB/Docker concerns | stdlib |
| `internal/clock`, `internal/logging`, `internal/envconfig`, `internal/ids`, `internal/buildinfo` | Small shared utilities | stdlib |
| `internal/testutil` | Test-only helpers | anything |
| `web/` | SvelteKit app, generated API client, `embed.go` | — |

Rules: the agent never imports `internal/manager/...` (enforced by
`internal/agent/nolisten_test.go`); Docker SDK types never leave the agent's
Engine adapter (#21); transport DTOs (`api`), database models (`store`) and
domain types (`domain`) are separate and converted explicitly.

## Data flows

- **Startup (manager):** config → data dir → open SQLite → snapshot if
  migrations are pending → migrate (abort on failure) → secret key and
  instance record → HTTP handler → listen. See `internal/manager/app`.
- **Browser → manager:** the PWA calls `/api/v1` with the typed client
  generated from `api/openapi.json`. Responses are `no-store`; static assets
  under `/_app/immutable/` are cached for a year; every other path falls back
  to `index.html` for client-side routing.
- **Agent → manager (#3):** the agent enrolls once (`POST /agent/v1/enroll`,
  one-use `dye_` token → `dya_` credential), then dials `/agent/v1/session`
  (WebSocket, subprotocol `dockyard.agent/v1`) and exchanges JSON frames
  (`internal/protocol`): hello, heartbeat, capabilities, command/ack/
  progress/result, events, file invalidations, stream relay, cancel, error.
  Commands carry job ID, attempt, fencing token and deadline (#26). Manager
  side: `internal/manager/agents` (enrollment, environments, session hub =
  the job engine's dispatcher); agent side: `internal/agent/session`.
  Environment and agent changes are published on the in-process event bus
  `internal/manager/events` (consumed by the live stream, #23).
- **Jobs (#26):** every long or mutating operation is a durable job in the
  manager's engine (`internal/manager/jobs`), with one lock matrix, fencing
  tokens and reconnect reconciliation; see
  [job-engine.md](job-engine.md).
- **Secrets at rest:** sensitive settings are sealed with
  `secrets.Keyring.Seal(value, context)` into `dy1.<keyID>.<ciphertext>`
  envelopes bound to their field context.

## Persistence

One SQLite database (`dockyard.db`) in the manager data volume, WAL mode,
single-connection writer. Migrations are Go files in `internal/db/migrations`.
Sampled metrics live in a separate database file (`metrics.db`, own
migrations in `internal/db/metricsmigrations`, #5) so sample writes never
contend with jobs and auth; manager-state backups (#10) leave it out by
default. See [metrics.md](metrics.md).
