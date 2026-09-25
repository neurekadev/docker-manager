# Upgrading DockYard (#34)

DockYard has no in-app self-update in v1 (#25): an agent recreating its own
container through its own Docker socket is fragile. You upgrade the images
where they run. The API and UI show which agents are outdated and how to
upgrade them.

## Rules

- **Manager first, then agents.** A manager serves agents of its own minor
  release and of the previous one (N-1). An agent newer than its manager,
  older than N-1, of another major version or with an unparsable version is
  refused: its session closes with `4426` and enrollment answers `426
  version_unsupported`, with a message saying what to upgrade. The agent
  does not retry; its health file shows `version_unsupported` and its log
  says "the manager refuses this agent version; upgrade it".
- **Downgrades are unsupported.** A manager refuses a database migrated by a
  newer build (unknown migrations). The rollback of an upgrade is restoring
  its pre-migration snapshot and starting the previous image (below).
- Rolling `edge` builds all report version `0.0.0-edge`; two edge builds
  count as the same version, so upgrade manager and agents together (the
  window applies to versioned releases).

## Before you upgrade

1. Note the image digests you run, so you can go back:
   ```bash
   docker compose images --format json | jq -r '.[] | "\(.Repository):\(.Tag) \(.ID)"'
   docker image inspect --format '{{index .RepoDigests 0}}' code.neureka.dev/dockyard/dockyard-manager:edge
   ```
2. Optional but recommended: run a manager-state backup (#10) or copy the
   `dockyard_data` volume. The upgrade takes its own pre-migration snapshot
   anyway (next section).

## Upgrade the manager

Compose deployments from `deploy/caddy`, `deploy/traefik` or `deploy/nginx`
(manager, co-located agent and proxy on host A):

```bash
cd deploy/caddy                       # your deploy directory
docker compose pull dockyard-manager
docker compose up -d dockyard-manager
docker compose ps                     # dockyard-manager healthy
```

On start the manager:

1. writes a consistent SQLite snapshot (`VACUUM INTO`) of the database to
   `<data dir>/snapshots/dockyard-<UTC time>-<seq>-pre-<first pending migration>.db`
   when migrations are pending and the database holds data; the newest 3
   snapshots are kept;
2. applies the pending migrations, each in its own transaction;
3. only then listens (`/api/v1/health/ready` answers 200 once migrations are
   done; the image `HEALTHCHECK` probes `/api/v1/health`).

A migration failure aborts startup: the container exits non-zero (Compose
restarts it and it fails again) and logs `migration failed (pre-migration
snapshot: "…")`. Roll back as described below.

After the manager upgrade, every environment reconnects. `GET
/api/v1/environments` (and `…/agents`, `…/environments/{id}/system`) report
per environment `agentVersion` and `compatibility`: `current`, `outdated`
(previous minor: works, upgrade it) or `unsupported` (refused until
upgraded), with `upgradeInstructions`.

## Upgrade the agents

Every host running an agent (host A's co-located agent, and each remote
host from `deploy/remote-agent`):

```bash
docker compose pull dockyard-agent
docker compose up -d dockyard-agent
```

The agent keeps its identity and credential in its state volume
(`dockyard_agent_state`); it reconnects, reports its new version and its
environment's `compatibility` becomes `current`. Jobs the agent was running
are reconciled by its job journal (#26).

Plain `docker run` agents: `docker pull` the image, then remove and
recreate the container with the same volumes, mounts and environment.

## Roll back a failed upgrade

The documented rollback is: restore the pre-migration snapshot, start the
previous image. Snapshots are only useful with the image version that wrote
them; the newer image would migrate again.

```bash
docker compose stop dockyard-manager
# List the snapshots in the data volume (the manager must be stopped):
docker compose run --rm --no-deps dockyard-manager snapshots list
# Restore the one written by the failed upgrade ("pre-<migration>"):
docker compose run --rm --no-deps dockyard-manager snapshots restore dockyard-20261001T020000Z-00-pre-20261001000000_example.db
```

`snapshots restore` verifies the snapshot (SQLite integrity check, readable
migration table) on a copy, moves the current `dockyard.db` (and its
`-wal`/`-shm`) to `<data dir>/replaced-<UTC time>/` and puts the snapshot in
place. Then pin the manager image to the digest you noted and start it:

```yaml
# compose.yaml
  dockyard-manager:
    image: code.neureka.dev/dockyard/dockyard-manager@sha256:<previous digest>
```

```bash
docker compose up -d dockyard-manager
```

Everything done after the snapshot (the few seconds of the failed start)
is lost; nothing else is. If agents were already upgraded past the window
of the old manager, pin them to their previous digest too. Keep the
`replaced-…` directory until you are sure; delete it afterwards.

A successful upgrade can be rolled back the same way, but you lose every
change made since the upgrade (the snapshot is the state before it), and
agents keep running whatever they were doing.

## Verifying the version window (manual procedure)

DockYard publishes only rolling `edge` images, so CI cannot pull a real
previous release. The window is covered by unit and transport tests
(`internal/protocol` `TestCheckAgentVersion`, `TestAgentCompatibility`;
`internal/manager/agents` `TestAgentVersionWindow`,
`TestAgentStopsOnNonRetryableRefusal`; `internal/manager/api`
`TestAgentVersionCompatibilityFlags`). Once versioned images exist, check a
real N-1 agent by hand:

1. Build or pull a manager `1.N.x` and an agent `1.(N-1).y` (version stamped
   with `-ldflags -X …/internal/buildinfo.Version=…`, or the images' build
   argument `GIT_TAG`).
2. Start the manager, enroll the agent: it connects, its environment is
   online and `GET /api/v1/environments/{id}` shows `compatibility:
   "outdated"` with upgrade instructions; run a container restart and a
   stack deploy through it.
3. Start an agent `1.(N-2).z` (or `1.(N+1).0`) against the same manager:
   enrollment answers `426 version_unsupported` ("upgrade the agent" /
   "upgrade the manager first"); an already enrolled agent's session closes
   with `4426`, its health file says `version_unsupported` and it does not
   reconnect.
