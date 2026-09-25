# Compose stacks (#7)

A **stack** is a Compose project DockYard manages on one environment: a
project directory in the environment's stacks volume (or a registered stack
root, #28) with its `compose.yaml`, optional override files and `.env`.

| Package | Role |
| --- | --- |
| `internal/manager/stacks` | Stack records, immutable revisions, create/validate/import, deploy and operations (jobs), revision restore, discovery, live services and drift, finish hooks of the `stack.*` jobs, the stack Locator, the reconnect reconciler, hooks for #15/#23. |
| `internal/manager/store` (`stacks.go`) | `stacks` and `stack_revisions` (migration `20260925191336_create_stacks`). |
| `internal/manager/api` (`stacks.go`) | The `/api/v1/stacks` and `/environments/{id}/stacks/...` routes. |
| `internal/agent/stacks` | The agent's `compose.*` request handlers and `stack.*` job executors. |
| `internal/agent/lifecycle` | The shared dependency-aware lifecycle (exported for #9, #10, #20, #35). |
| `internal/agent/compose` | Compose loading (disk or in memory), the SDK operations (#21). |
| `internal/protocol` (`compose.go`) | `ProjectRef` (where a project lives), source snapshots and hashes, request/job payloads. |

## Source of truth (#25 Q1)

The definition files on disk are authoritative. The manager keeps:

- **Revisions** (`stack_revisions`): immutable (an `UPDATE` trigger refuses
  changes) and numbered per stack, each a snapshot of the definition files
  (path, SHA-256, size and the contents, **sealed** with the
  secret-protection key because `.env` holds secrets, #25: no secret store)
  plus its hash (`protocol.SourceHash`: SHA-256 over `"<sha256>  <path>\n"`
  lines sorted by path; the agent computes the same). Sources:
  `deploy` (every deploy, from the bytes the agent deployed), `editor`
  (created or imported with an explicit source), `file_manager` (a save
  through #15), `external` (an edit observed on disk: #23's watcher,
  reconciliation after an agent reconnect, adoption in place), `restore`.
  The author (user, API token) is audit metadata. Observations are
  deduplicated by hash; every deploy records a revision.
- **Deployment intent** on the stack: the **applied revision** (last
  successful deploy), applied images (reference, image ID, repository
  digest, platform: #20's baseline), the services, binds and dependency
  graph of that deploy, the **failed revision** and the Engine state before
  a failed deploy (recovery), and the deployment status (`undeployed`,
  `deployed`, `stopped`, `down`, `failed`: what DockYard last did).
- **Observed state**: the newest revision seen on disk (**source
  revision**) and the Engine state last observed (`running`, `partial`,
  `stopped`, `missing`, per-service container counts).

`undeployedChanges` = the source revision differs from the applied one (or
DockYard never deployed it). The three states — applied revision, source
revision, live Engine state — are separate fields of the API's `Stack`, and
`GET /stacks/{id}/services` computes **drift** (a missing or stopped
service of a deployed stack, a running service of a stopped stack, an
unexpected service, a container running another image than the applied
one).

### Deploy

`POST /stacks/{id}/deployments` enqueues `stack.deploy` (202 + job). The
agent's steps:

1. `resolve_sources`: resolve the project directory (verified root, no
   symlink escape), load and validate the project (unsupported features are
   refused), record the Engine state **before** anything changes in the
   job's journaled output (`jobexec.StepContext.SetOutput`, fsync'd).
2. `pull_images`: only with `pull: always` (the default `missing` never
   moves tags implicitly, #20). Every pull, build and `up` uses the
   registry credentials of the command (#19, below), in memory only.
3. `build_images`: builds the images of build sections that are missing
   on the host (every build section with `build: true`) through the
   Engine's BuildKit, with streamed progress and cancellation, exactly
   like `stack.build` (builds.md, #33).
4. `apply`: snapshot the definition files, **load the project from exactly
   those bytes**, re-read the files and retry when they changed meanwhile,
   then Compose `up` (dependency order and `depends_on` conditions by the
   SDK). A container recreated because its definition or image changed
   inherits its predecessor's anonymous volumes, like `docker compose up`
   (the adapter sets the SDK's `Inherit`, whose zero value would start it
   with empty ones; `compose.UpOptions.RenewAnonymousVolumes` is the
   explicit `--renew-anon-volumes`, not exposed by the API). The result
   output (`protocol.StackJobOutput`) carries the
   sources (contents inline up to 96 KiB, hashes only above), the applied
   images, binds, warnings and the state before and after — also when `up`
   fails.

The manager's finish hook (`jobs.Engine.OnFinish`, in the job's finishing
transaction) records a `deploy` revision from the reported sources and, on
success, makes it the applied revision. **The agent never writes
definition files during a deploy**; only `compose.write` (stack creation
and explicit restores) writes them.

A deploy that fails **while applying** sets status `failed`, keeps the last
applied revision, records the failed revision and the pre-deploy state
(its image IDs are still on the host) and shows recovery guidance: fix and
deploy again, or restore the last applied revision and deploy it. Nothing
is rolled back automatically (#25). A deploy that fails before applying
(invalid project) changes nothing.

Deploys (and all `stack.*` jobs) of one stack serialize through the job
engine's exclusive `stack` lock; deploys of different stacks on the same
environment run in parallel (shared `host` lock).

### Restore

`POST /stacks/{id}/revision-restores {revisionId}` needs
`stack.definition.write` and an online environment. It reads the current
definition (recording it first if it was new, so no edit is lost), writes
the revision's files with `compose.write` in `replace` mode —
compare-and-set on the current hash (`409 stack_definition_changed`
otherwise) and removing definition files the revision does not have — and
records a `restore` revision. It **never deploys**: `deployOffered` tells
the client to offer one.

### Offline environments

Requests to the agent fail with `503 environment_offline`: creation,
validation, import, discovery and restores are refused and the last known
revisions and Engine state are served read-only (`readOnly: true`,
`services.live: false`). Deploy and operation jobs queue in the job engine
and fail with `agent_offline` after their offline deadline (#26). After
every reconnect the reconciler re-reads every stack of the environment in
the background: edits made while it was offline become `external`
revisions and the Engine state is refreshed.

### Location in the UI (#22)

The stack header shows the logical location "environment · stack". `GET
/stacks/{stackId}` adds the project directory's host path
(`location.hostPath`, resolved from the agent's reported stacks root) for
callers with `stack.definition.read`, the same rule as bind sources; lists
never carry it.

## Discovery and import

`GET /environments/{id}/stacks/discovered` (`stack.import`) lists the
Engine's Compose projects from container labels (`compose.discover`):
services with container counts, the project directory and Compose files
from the labels, whether the project is **adoptable in place** (its
directory is below the stacks volume or a verified registered root and its
Compose/env files are inside it) and the DockYard stack already managing
it. Labels never reconstruct a source.

`POST /environments/{id}/stacks/imports`:

- without `source`: adopt in place; the real files become the first
  revision (`external`); the stack starts `deployed` with no applied
  revision (DockYard has not deployed it yet, so it shows undeployed
  changes);
- with `source` (projects outside the roots): the given definition is
  written into a **new** directory `<projectName>` of the stacks volume.

Nothing is overwritten silently: a project DockYard already manages →
`409 stack_name_taken` (also enforced by unique indexes on
`(environment, name)` and `(environment, root, root_path, dir)`); an
existing directory → `409 stack_directory_exists`. `POST /stacks` refuses a
name the Engine already runs as a Compose project (`409
compose_project_exists`: import it instead).

## Authorization (#17)

| Capability | Opens |
| --- | --- |
| `stack.read` | the stack in full: status, revisions refs, images, Engine state, services (containers minimal unless `container.details.read`), image status, events |
| `stack.definition.read` | revisions with contents, bind sources (they come from the definition) |
| `stack.definition.write` | revision restores |
| `stack.create`, `stack.import` | creation/validation, discovery/import in an environment |
| `stack.manage` | display metadata (never written to Compose files) |
| `stack.deploy`, `stack.start/stop/restart/down`, `stack.remove` | the jobs |
| `stack.build` | `POST /stacks/{id}/builds` (rebuild the build sections without deploying, #33) |

Any other capability on a stack shows it minimally (id, name, environment,
status, actions). Stacks are located by the stack Locator; stack-scoped
rules name the stack ID and follow it across environments (#35). Deleting a
stack removes the rules naming it and its services.

## The lifecycle helper (`internal/agent/lifecycle`)

`NewGraph` validates services and `depends_on` (conditions, cycles).
`Start` starts dependencies first and waits, per dependency, for
`service_started` (running or exited), `service_healthy` (healthy; fails
immediately when unhealthy or without a health check) or
`service_completed_successfully` (exited 0; fails on another exit code),
each bounded by `Options.WaitTimeout`. Optional (`required: false`)
dependencies only add warnings. `Stop` stops dependents first. `Restart`
restarts the set plus every `restart: true` dependent, transitively.
`Resume` starts exactly the services that were running before (backup
shutdown and restore, #10), dependencies first, and refuses with
`dependency_conflict` before starting anything when a service would need a
dependency that was stopped (a completed one-shot is fine). Errors carry
stable codes (`dependency_failed`, `dependency_missing`, `timeout`,
`dependency_conflict`, `no_containers`).

`EngineRuntime` drives a Compose project's service containers through the
Engine adapter (inspect-based state, so the pre-26 container-list lag does
not matter). `GraphFromContainers` builds the **deployed** graph from the
containers' labels: DockYard's `dev.neureka.dockyard.depends_on`
(`service:condition:restart:required`, set by the Compose adapter on every
service) or Compose's own label (no `required`: treated as required).
Stack start/stop/restart jobs use it, so a stack whose files were edited
but not deployed is operated as deployed.

## For other workstreams

- **#15 file manager** (wired in `app`: `Files().SetStacks(stacks,
  stacks)`): `StackFileRoot` resolves a stack scope to its absolute project
  directory (the stacks volume path the agent reported in its capabilities,
  or the registered root); `StackSourcesChanged` records a `file_manager`
  revision in the background after a save of a definition file (never
  deploys). `Root`, `IsDefinitionFile` and `RecordFileSave` remain for
  callers that need the definition file list. `app.Manager.Stacks()`
  exposes the service.
- **#23 watcher**: `RecordObserved(ctx, stackID, domain.RevisionExternal,
  authz.Service())` after an external change to a definition file.
- **#20 updates** (implemented, [updates.md](updates.md)): the applied
  images (`Stack.Images`, `GET /stacks/{id}/image-status`) are the digest
  baseline; `update.run` (`internal/agent/stacks/update.go`) reuses the
  deploy executor's source snapshot, `Adapter.Create` and
  `lifecycle.Update`/`Confirm`, never writes definition files, and
  `RecordUpdatedImages` stores the new baseline after a successful run.
  The stack image status shows each service's eligibility
  (`updates/eligible`, including `pull_policy_conflict`; services carry
  their Compose `pull_policy`) and its policy's update state.
- **#10 backups**: `Stack.Binds` lists resolved bind sources (`relPath`
  inside the project directory, `external` otherwise); use
  `lifecycle.Stop`/`Resume` with the pre-backup running set.
- **#35 migrations** ([migrations.md](migrations.md)): stack rules follow
  the stack ID; `Place` moves the record to the destination at cut-over (and
  back on rollback) and the destination deploy is a normal `stack.deploy`
  job; the source stops and restarts through the lifecycle helper.
- **#19 registry credentials**: `Deploy` selects the registry connection
  of every non-build image (`Registries().Select`, stack and environment
  bindings apply; ambiguous or revoked selections refuse the request with
  `409`) and puts the IDs into the input (`registryConnections`); the engine
  resolves them to command secrets at every dispatch, and the executor
  passes `regauth.All(sc.Secrets)` to the SDK's pulls, builds and `up`
  (missing secrets fail the job instead of pulling anonymously). Base images
  of build sections get whatever credentials the selected connections
  carry.
- **#5 metrics**: `GET /stacks/{id}/services` has no per-service metrics
  yet.
- **#6 Docker resources**: `stacks.Service.StackIDs` is the resources
  service's `StackResolver` (wired in `app`): containers, volumes and
  networks of a managed stack's project are placed in their stack and
  refused for direct edits (`stack_managed`).
- **Job error classes**: stack job failures are classified
  (`jobexec.ClassedError`): Engine/Compose codes (`dependency_failed`,
  `invalid_project`, `unsupported_compose_feature`, `unauthorized`,
  `rate_limited`, ...), lifecycle codes (`dependency_missing`,
  `no_containers`, `dependency_conflict`), `storage_*` (#28),
  `credential_unavailable` (#19) and `nothing_to_build` (a stack build
  without a build section, #33), each with recovery guidance.
