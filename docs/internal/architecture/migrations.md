# Environment migration (#35)

A user moves a managed Compose stack (its project directory and named
volumes), copies a standalone named volume, or moves every stack of an
environment to another environment. The
migration is previewed before anything stops, runs as a job, keeps the
source intact until the user confirms its removal, and never deletes
anything automatically.

| Package | Role |
| --- | --- |
| `internal/manager/migrations` | Preview (`Evaluate`, a pure function over facts from both agents), the `stack.migrate` / `volume.migrate` manager executors, the relay (`Relay`), access-change previews, the `migrations` records and their finish hooks (audit `migration.finished`), source removals. |
| `internal/agent/migration` | The agent side: `migration.preview/stop/start/commit/cleanup` requests, `migration.send/receive` streams, the tar writer/extractor (`WriteTree`, `ExtractTree`) over a contained filesystem (`FS`, `os.Root`), the `stack.remove_source` executor. `migrationtest`: an in-memory host filesystem and simulated environments for tests. |
| `internal/transfer` | Chunk framing with per-chunk and whole-payload SHA-256 (`Writer`, `Reader`, `Verifier`), the bandwidth limiter, `ParseRate`. |
| `internal/protocol` (`migration.go`) | Request, stream and job payloads. |
| `internal/manager/api` (`migrations.go`, `environment_migrations.go`) | The stack and volume routes; the environment routes (preview, start, list, get). |
| `internal/manager/store` (`migrations.go`, `environment_migrations.go`) | `migrations` (migration `20260925235142_create_migrations`), `environment_migrations` (`20260929140000_create_environment_migrations`). |

## Transfer

Agents only dial out (#27): data flows **source agent → manager →
destination agent** over the existing sessions. Per part (the project
directory, each volume, the locally built images) the manager opens
`migration.receive` on the destination and `migration.send` on the source
and copies between them (protocol and format:
[agent-v1.md](../protocol/agent-v1.md#streams), "Migration transfer
relay"):

- **Backpressure and memory:** the manager reads from the source only after
  the destination accepted the previous bytes (stream credit end to end), so
  a relayed part holds at most one stream window plus a 64 KiB buffer in
  manager memory; nothing is written to the manager's disk
  (`TestRelayBackpressureBoundsMemory`).
- **Bandwidth cap:** `DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT` (bytes per second,
  shared by all running migrations; `TestRelayBandwidthCap`).
- **Checksums:** chunks carry their SHA-256 and the stream ends with the
  payload's total length and SHA-256; the destination extracts a chunk only
  after it matched, the manager verifies the framing while relaying, and the
  source's, manager's and destination's results must agree. A mismatch or a
  lost session retries the part from its start (at most 3 attempts; a lost
  agent is awaited for up to 2 minutes, then the job is interrupted).
- **Format:** PAX tar with the root directory first, numeric UID/GID,
  permission and setuid/setgid/sticky bits, nanosecond modification and
  access times, symlinks as links (never followed), hard links, FIFOs.
  Sockets and device nodes are skipped and listed. A symlink's own
  timestamps are not kept (`os.Root` has no lutimes).
- **Containment:** the source reads only the project directory inside a
  verified stack root and supported local volumes below the verified volume
  directory; Docker Manager's own volumes, images and project are refused (#32).
  The destination writes only into
  `<stacks>/.docker-manager-migrations/<migrationId>/project` (moved into place
  by `migration.commit`, which never replaces a directory) and into volumes
  it creates itself labeled `docker-manager.migration=<migrationId>`
  (with the source volume's Compose labels, so Compose adopts them, and its
  user-set exclusion labels; never Docker Manager's own labels, under
  either prefix; volumes of earlier versions carry the legacy
  `dev.neureka.docker-manager.migration`, read the same way). The
  label stays after a successful migration; backups (#10) select
  such a standalone volume only when its migration succeeded
  (`backups.Service.migrationSucceeded`), so a failed migration's partial
  copy is never backed up.
  Extraction refuses escaping names, members below symlinks or files, hard
  links to anything but earlier files, device nodes, and data beyond the
  destination's free space.

## Stack migration

`stack.migrate` (manager executor; locks: host shared on both
environments, the stack, the source volumes and the destination's new
volumes exclusive; only the stack is authorized with `stack.migrate`,
`jobspec.Spec.LockOnly`):

1. **prepare** — re-checks the destination capabilities of the initiator
   (`stack.create` in the destination, `stack.deploy` on the stack there),
   removes the destination's leftovers of earlier unsuccessful migrations of
   the stack (`migration.cleanup`) and re-runs the preview: blockers fail the
   job before anything stops.
2. **stop_source** — records the services that run, registers the
   `start_source` compensation, then `migration.stop` stops the project in
   reverse dependency order (`internal/agent/lifecycle`).
3. **transfer** — project directory (with its relative bind directories),
   the selected named volumes, locally built images (image save/load);
   `migration.commit` moves the project into `<stacks>/<name>`.
4. **deploy_destination** — cut-over: the stack record moves to the
   destination (same ID, revisions, display metadata; `Root` stacks,
   `Dir` = the project name), then a `stack.deploy` job (the initiator's,
   idempotency key `migration-<id>`) deploys it there in dependency order,
   waiting for health and dependency conditions (#7); the migration waits
   for it.
5. **finalize** — records the migration completed (and runs the
   `OnStackMoved` hooks in the same transaction: the stack's update
   record moves to the destination with its candidates reset to
   unchecked, #20; the backup setup covers environments, so the stack is
   backed up where it is now, #10 — see updates.md and backups.md),
   releases the compensation and removes the staging directory.

The **source stays stopped and untouched**. `POST
/stacks/{id}/migrations/{migrationId}/source-removals` (after completion,
once; refused while a Docker Manager stack manages the source project again)
starts `stack.remove_source` on the source: containers and networks of the
project, the migrated volumes, then the project directory; exact
permission rules naming them are deleted.

Until then the stopped source is **held** (`migrations.Service.RetainedSources`:
stack migrations of the environment that run or completed): prune runs
(#14) keep its Compose project (containers, networks, volumes carrying the
label) and the migration's source volumes, and the Docker resource routes
(#6) refuse to remove its containers, volumes and networks with
`stack_managed` (`resources.Service.SetRetainedProjects`). Nothing but
`stack.remove_source` deletes the project directory. The hold ends when
the removal succeeds (`source_removed`); importing the project as a stack
again protects it as that stack.

**Jobs queued before the cut-over** — a job queued against the source
while the migration held the stack lock (a scheduled update run, a
backup, a manual stop) is refused at dispatch with `target_moved` instead
of acting on the stopped source; the policies that moved with the stack
enqueue their next scheduled run against the destination.

**Stopping before completion** — a failure, cancellation or crash runs
`start_source`: the stack record is put back on the source (when it had
moved; a running destination deploy is cancelled) and the services that
ran before are started again (`lifecycle.Resume`). When the source's agent
is unreachable (e.g. right after a manager restart) the job ends
interrupted with that guidance and the stack is started once the agent is
back. The destination keeps what the migration wrote (`targetPartial`);
the next migration of the stack to it removes it first. Backups of the
source stay in their repository and restorable there (#10).

Anonymous volumes are skipped unless selected (then copied under their
name; the service still gets a new anonymous volume). Volumes with driver
options and non-local drivers keep their definition only (data not
migrated in v1). External volumes and networks must exist on the
destination.

## Environment migration

`environment.migrate` (manager executor, `environment.go`; locks: host
shared on the source only, because each stack moves as its own
`stack.migrate` job holding its stack's lock; the engine authorizes
`stack.migrate` on every stack target) moves the chosen stacks of an
environment. There is no limit on the number of stacks: the kind sets
`jobspec.Spec.UnboundedTargets`, the confirmed stacks are the job's
targets (not its input), and the groups, networks and stopped services
live in the record, not in the job's output or journal:

- **Order** (`order.go`, `orderStacks`, pure): a stack's links are the
  networks and named volumes its project creates (Docker names) and the
  ones it joins as `external`. Stacks linked this way (and stacks that
  name the same network or volume) form a group; inside a group a stack
  follows the stacks it joins (topological, then name order; a circle is
  reported as `dependency_cycle` and moves in name order). Groups follow
  the name of their first stack. No setting marks a stack: the order
  comes from the definitions only.
- **Preview** (`planEnvironment`, pure over each stack's own preview):
  every stack of the source is previewed (`previewStack`, four at a
  time, so a large environment is checked in reasonable time); stacks the
  caller may not migrate to the destination (`stack.migrate` on the stack,
  `stack.create` and `stack.deploy` there) and Docker Manager's own
  project are left out (`skipped`: `not_permitted`, `docker_manager`,
  `not_selected`). Walking the order, an `external_network_missing` or
  `external_volume_missing` of a stack is dropped when an earlier stack
  creates it. An external network no moving stack creates that the source
  has as a hand-made `bridge` network is created on the destination first
  (`networks`, warning `network_created`: default addressing, fixed
  subnets are not copied); other drivers stay blockers
  (`network_not_creatable`), as does a caller without `network.create`
  there (`network_create_denied`). The data of all stacks together is
  checked against the destination's free space (`insufficient_space`);
  the downtime is the longest group's (the sum of its stacks').
- **Run**: `prepare` re-runs the preview (the stack set must equal the
  confirmed one; blockers fail the job before anything changes) and
  writes the `environment_migrations` record; `create_networks` starts a
  `network.create` job per missing network on the destination (the
  initiator's); `migrate` handles group by group: every stack of the group
  still on the source stops first, in reverse order (`migration.stop`
  with this job's ID), each after registering `start_group` with the
  services that ran; then each stack moves as a `stack.migrate` job (the
  initiator's, idempotency key `environment-<id>-<stackId>`) and the job
  waits for it (a cancellation cancels it). Before each further stack of
  the group moves, the group's stacks still waiting are stopped again
  (this job holds no stack lock, so another job may have started one; a
  shared volume must not be written while it is copied). When the group
  moved, its `start_group` compensations are released.
- **Failure**: when a stack's migration does not complete (or is blocked
  when it starts), the job fails with `stack_not_moved`. That stack's own
  compensation put it back on the source (it restarts nothing: this job
  stopped it); `start_group` then starts the services this job stopped of
  every stack of the group still on the source (it first waits for a
  running `stack.migrate` of the stack, so the stack is back). Stacks
  moved before stay on the destination. A new environment migration moves
  what is left (the preview lists only stacks still on the source).
- **Record**: `environment_migrations` keeps the groups, each stack's
  state (`pending`, `moving`, `moved`, `failed`) with its stack migration
  and the services this job stopped of it, and the networks to create
  with their settings; the finish hook sets the state from the job.
  The moved stacks' sources are held and removed exactly as after a
  stack migration (per stack, `source-removals`).

## Volume migration

`volume.migrate` copies a standalone volume to another environment,
optionally under a new name, into a new volume. Containers using it on the
source block the migration unless a crash-consistent copy is acknowledged
(`acknowledgeCrashConsistency`). The source volume is kept.

## Preview

`Evaluate` turns the source's facts (`migration.preview` role source:
services, images with platform and repository digests, volumes with their
sizes, networks, binds, ports, devices) and the destination's
(`migration.preview` role destination: conflicts, free space, platform,
leftovers) into blockers and warnings (`preflight.go` lists the stable
codes; corpus: `TestPreflightCorpus`):

- **Images:** pull on the destination through its registry connection
  (#19, checked with `Registries().Check` for the destination's platform),
  rebuild from `build:` (#33), copy locally built images through the relay
  (blocked on another architecture), or already present.
- **Conflicts** (blockers): Docker Manager stack, Compose project, container,
  volume and network names, the project directory, published host ports of
  running containers; missing external networks and volumes.
- **Warnings:** bind paths outside the project directory (not migrated),
  device mappings, definition-only volumes, skipped anonymous volumes,
  plain-HTTP transport of either agent, host ports held outside Docker.
- **Size and downtime:** data size against the destination's free space
  (blocker), expected downtime (stop + copy at the cap or 50 MB/s + start).
- **Access** (#17): every stack and container capability (and volume
  capabilities of copied volumes) evaluated before and after the move per
  user (`permissions.Service.MoveImpact`): stack- and service-scoped rules
  follow the stack, environment rules and exact per-environment rules do
  not. The instance owner sees every affected user, others their own change
  and a count.

## Authorization

`stack.migrate` on the stack (engine check at request and dispatch) plus
`stack.create` in the destination and `stack.deploy` on the stack there
(API check at preview and request, executor re-check in `prepare`, engine
check of the destination deploy job). Volumes: `volume.migrate` on the
volume plus `volume.create` in the destination. Environments: a visible
source and `stack.create` in the destination at the API; each stack needs
what its own migration needs (stacks without it are left out, not
refused), `network.create` in the destination when networks are created;
the engine authorizes `stack.migrate` on every stack target and each
child job checks its own again. Its records (list, get) are readable with
`stack.migrate` on a stack of the source or on one of the record's stacks
where it is now (after every stack moved away the source has none left,
while their old copies still wait there for their removal); each lists
only the stacks the caller can see.

## Audit

Requests are audited by `api.Register` (`stack.migrate`,
`stack.migrate.preview`, `stack.migrate.remove_source`, `volume.migrate`,
`volume.migrate.preview`, `environment.migrate`,
`environment.migrate.preview`) with source and destination; the job lifecycle
by the engine; the outcome as `migration.finished` with source,
destination, bytes, every part's size and checksum and the resulting state.
