# Upgrading Docker Manager (#34)

You upgrade the images where they run, or let Docker Manager do it: once its
own Compose project is imported as a stack (see [deployment.md](../deployment.md),
"Docker Manager's own containers"), **Pull and Deploy** or a digest update
policy (#20) pulls and recreates the manager and the co-located agent. The
agent never recreates its own container in the middle of a job: it hands
that service to a short-lived helper container after the job (#32), which
starts the previous agent again if Compose fails. The rules below (manager
first, then agents; a pre-migration snapshot) apply either way. The API and
UI show which agents are outdated and how to upgrade them.

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
   docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/neurekadev/docker-manager:edge
   ```
2. Optional but recommended: run a manager-state backup (#10) or copy the
   `docker-manager_data` volume. The upgrade takes its own pre-migration snapshot
   anyway (next section).

## Upgrade the manager

The Compose deployment of the user documentation's Quickstart (manager and
co-located agent on host A):

```bash
cd /path/to/docker-manager            # the directory of its compose.yaml
docker compose pull docker-manager
docker compose up -d docker-manager
docker compose ps                     # docker-manager healthy
```

On start the manager:

1. writes a consistent SQLite snapshot (`VACUUM INTO`) of the database to
   `<data dir>/snapshots/docker-manager-<UTC time>-<seq>-pre-<first pending migration>.db`
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
host), in the directory of its `compose.yaml`:

```bash
docker compose pull docker-agent
docker compose up -d docker-agent
```

The agent keeps its identity and credential in its state volume
(`docker-manager_agent`); it reconnects, reports its new version and its
environment's `compatibility` becomes `current`. Jobs the agent was running
are reconciled by its job journal (#26).

Plain `docker run` agents: `docker pull` the image, then remove and
recreate the container with the same volumes, mounts and environment.

**Disk health (2026-09-29, #143):** agents installed before disk health
run unprivileged and report `no_access` on the System tab. Add
`privileged: true` to the agent service (in the imported stack's files,
or `--privileged` for a `docker run` agent) and deploy it again; see
`docs/internal/deployment.md` ("Disk health: the agent runs privileged").

## Label prefix `docker-manager.` (2026-09-28)

Docker Manager's labels moved from the prefix `dev.neureka.docker-manager.`
to `docker-manager.` (owner decision: no `dev.neureka` in labels):

| Before | Now |
| --- | --- |
| `dev.neureka.docker-manager.role` | `docker-manager.role` |
| `dev.neureka.docker-manager.managed`, `.instance`, `.spec` | `docker-manager.managed`, `.instance`, `.spec` |
| `dev.neureka.docker-manager.migration` | `docker-manager.migration` |
| `dev.neureka.docker-manager.depends_on` | `docker-manager.depends_on` |
| `dev.neureka.docker-manager.description` | `docker-manager.description` |
| `docker-manager.update.exclude`, `.backup.exclude`, `.maintenance.exclude` | unchanged |

Nothing is mandatory. Docker cannot relabel a container or volume, so
objects created before keep their labels, and Docker Manager reads both
keys everywhere (the new key wins when an object has both): its own
containers stay protected, standalone containers it created stay managed
(updates, recreate specifications), migration volumes and dependency
labels keep working, and a Compose file's
`dev.neureka.docker-manager.description` still fills in a service's
description. Everything Docker Manager creates or recreates from now on
(containers, a standalone container's update, stack deploys, clones of a
stack rename, migrated volumes, the self-update helper, the install
command) gets the new keys only.

Optionally switch your own files to the new keys: in `compose.yaml` rename
`dev.neureka.docker-manager.role: manager` / `agent` to
`docker-manager.role: manager` / `agent` (then `docker compose up -d`
recreates the containers with the new label), in a `docker run` agent
replace `--label dev.neureka.docker-manager.role=agent` by
`--label docker-manager.role=agent`, and rename a
`dev.neureka.docker-manager.description` label in your stacks' Compose
files. Upgrade the manager and every agent together: an agent of the
previous version reads only the old keys (the manager still sends it the
ownership labels of new containers under the old keys, gated by the agent
feature `labels.docker_manager`), and it would not recognize a container
labeled with the new `docker-manager.role`.

Two consequences of reserving `docker-manager.`: create forms refuse
labels under it (except the three `*.exclude` labels) and under the old
prefix; and a label under `docker-manager.` that you set yourself before
(for example on a container you created) is now read as Docker Manager's.
Recreate specifications saved with such a label drop it when Docker
Manager recreates or clones the container.

## Upgrading from DockYard (renamed 2026-09-26)

The project was renamed from DockYard to Docker Manager (the manager) and
Docker Agent (the agent). The rename changes names an installation relies
on; there is no in-place upgrade from `dockyard-*` images:

| What | Before | Now |
| --- | --- | --- |
| Images | `code.neureka.dev/dockyard/dockyard-{manager,agent}:edge` | `ghcr.io/neurekadev/docker-{manager,agent}:edge` |
| Compose project and volumes | `dockyard`: `dockyard_data`, `dockyard_agent`, `dockyard_stacks`, `dockyard_agent_ca` | `docker-manager`: `docker-manager_data`, `docker-manager_agent`, `docker-manager_stacks`, `docker-manager_agent_ca` |
| Manager database | `/var/lib/dockyard/dockyard.db` | `/var/lib/docker-manager/docker-manager.db` |
| Agent state | `/var/lib/dockyard-agent` | `/var/lib/docker-agent` |
| Environment variables | `DOCKYARD_*` | `DOCKER_MANAGER_*` (manager) and `DOCKER_AGENT_*` (agent; `DOCKYARD_AGENT_STATE_DIR` is `DOCKER_AGENT_STATE_DIR`) |
| Labels | `dev.neureka.dockyard.*`, `dockyard.update.exclude` | `dev.neureka.docker-manager.*` (`docker-manager.*` since 2026-09-28, above), `docker-manager.update.exclude` |
| Backup repositories | `dockyard-manager`, `dockyard-env-<id>` | `docker-manager`, `docker-manager-env-<id>` |

What carries over: API tokens, agent credentials and the Recovery Key
(their formats did not change), the audit trail and every record in the
database. What does not: sessions (everyone signs in again), agents still
running a `dockyard-agent` image (upgrade every agent together with the
manager), backup sets taken before the rename (the new manager neither
finds nor reads them: take a new backup right after upgrading and keep the
old repositories until you no longer need them), and standalone containers
created before the rename (their DockYard labels are no longer recognized,
so Docker Manager no longer treats them as containers it created; recreate
them from the UI to manage them again). Rename `dev.neureka.dockyard.*` labels in your own
Compose files (to `docker-manager.*`) (`description`, `depends_on`; the former `icon` label is
no longer read) and
`dockyard.update.exclude` on containers you keep out of updates.

Per host, with the new example files:

```bash
cd /path/to/docker-manager      # your deploy directory, still with the old files
docker compose down             # removes containers and network, keeps volumes
# Replace compose.yaml (and the proxy files) with the new example and rename
# the variables in .env (table above).
docker compose up --no-start    # creates the new, empty volumes
docker run --rm -v dockyard_data:/from:ro -v docker-manager_data:/to alpine   sh -c 'cp -a /from/. /to/ && for f in /to/dockyard.db*; do mv "$f" "/to/docker-manager.db${f#/to/dockyard.db}"; done'
docker run --rm -v dockyard_agent:/from:ro -v docker-manager_agent:/to alpine cp -a /from/. /to/
docker compose up -d
```

Deployed stacks record their project directory, which lies in the stacks
volume's path. Keep the old stacks volume so they stay managed stacks: in
`compose.yaml` declare it as
`stacks: {name: dockyard_stacks, external: true}`, mount it at
`/var/lib/docker/volumes/dockyard_stacks/_data` and set
`DOCKER_AGENT_STACKS_VOLUME=dockyard_stacks`. On a remote host run the same
steps in the agent's directory (volumes `dockyard_agent` and, with a
private PKI, `dockyard_agent_ca`). Once every environment is back online,
remove the old volumes you no longer use with `docker volume rm`.

## Roll back a failed upgrade

The documented rollback is: restore the pre-migration snapshot, start the
previous image. Snapshots are only useful with the image version that wrote
them; the newer image would migrate again.

```bash
docker compose stop docker-manager
# List the snapshots in the data volume (the manager must be stopped):
docker compose run --rm --no-deps docker-manager snapshots list
# Restore the one written by the failed upgrade ("pre-<migration>"):
docker compose run --rm --no-deps docker-manager snapshots restore docker-manager-20261001T020000Z-00-pre-20261001000000_example.db
```

`snapshots restore` verifies the snapshot (SQLite integrity check, readable
migration table) on a copy, moves the current `docker-manager.db` (and its
`-wal`/`-shm`) to `<data dir>/replaced-<UTC time>/` and puts the snapshot in
place. Then pin the manager image to the digest you noted and start it:

```yaml
# compose.yaml
  docker-manager:
    image: ghcr.io/neurekadev/docker-manager@sha256:<previous digest>
```

```bash
docker compose up -d docker-manager
```

Everything done after the snapshot (the few seconds of the failed start)
is lost; nothing else is. If agents were already upgraded past the window
of the old manager, pin them to their previous digest too. Keep the
`replaced-…` directory until you are sure; delete it afterwards.

A successful upgrade can be rolled back the same way, but you lose every
change made since the upgrade (the snapshot is the state before it), and
agents keep running whatever they were doing.

## Verifying the version window (manual procedure)

Docker Manager publishes only rolling `edge` images, so CI cannot pull a real
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
