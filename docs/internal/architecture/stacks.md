# Compose stacks (#7)

A **stack** is a Compose project Docker Manager manages on one environment: a
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
  `deploy` (a deploy of bytes no revision holds yet), `editor`
  (created or imported with an explicit source), `file_manager` (a save
  through #15), `external` (an edit observed on disk: #23's watcher,
  reconciliation after an agent reconnect, adoption in place), `restore`.
  The author (user, API token) is audit metadata. A revision is a version
  of the files, not a deploy: observations are deduplicated by hash, and a
  deploy (or an import or rename) whose bytes the newest observed revision
  or else the applied one holds reuses that revision
  (`stacks.Service.deployedRevision`), so redeploying unchanged files (to
  run a pulled image) or a digest update (#20) never numbers a new one.
  Deploys themselves are in the job history and the audit log.
- **Deployment intent** on the stack: the **applied revision** (last
  successful deploy), applied images (reference, image ID, repository
  digest, platform: #20's baseline), the services, binds and dependency
  graph of that deploy, the **failed revision** and the Engine state before
  a failed deploy (recovery), and the deployment status (`undeployed`,
  `deployed`, `stopped`, `down`, `failed`: what Docker Manager last did).
- **Observed state**: the newest revision seen on disk (**source
  revision**) and the Engine state last observed (`running`, `partial`,
  `stopped`, `missing`, per-service container counts).

`undeployedChanges` = the source revision differs from the applied one (or
Docker Manager never deployed it). The three states — applied revision, source
revision, live Engine state — are separate fields of the API's `Stack`, and
`GET /stacks/{id}/services` computes **drift** (a missing or stopped
service of a deployed stack, a running service of a stopped stack, an
unexpected service, a container running another image than the applied
one). An unexpected service is usually an **orphan**: a service removed
from the Compose file whose container is still on the host. A plain deploy
keeps it; the UI's "Cleanup Orphans & Deploy" (the deploy
body's `removeOrphans`, off by default) removes it. Its containers carry
their image ID, networks and volume mounts (`volumes`: name, destination,
read-only, anonymous; bind mounts are the stack's `binds`) in the full
view (`container.details.read` on the container); the services table
links each image and volume to its page from them.

### Validation

Both validations send `compose.validate` to the agent and answer the same
`StackValidation` (valid, project name, errors, warnings, services, binds;
findings are the answer, never an error; no file contents or `.env`
values). Neither writes anything, records a revision or runs a job.

- `POST /stacks/validations` (`stack.create` in the environment): a
  submitted definition, in memory, as if it were the new project `<name>`
  in the stacks volume (service `env_file`s are not read).
- `POST /stacks/{id}/validations` (`stack.definition.write` on the stack,
  the capability that edits its Compose files): the stack's current files
  on disk in its own project directory (`stacks.Service.ValidateStack`,
  `compose.validate` without files), loaded exactly as a deploy loads them:
  explicit Compose files, override, `.env`, service `env_file`s, relative
  paths. The file editor calls it after saving a definition file. It does
  not repeat the deploy's `stack_project_renamed` check (a top-level
  `name:` other than the stack's). Audited as `stack.validate`.

A file-manager save of a definition file is validated before it is written
(`ValidateSourceSave`, 422 `invalid_definition`).

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
transaction) resolves the reported sources to their revision (the
existing one with the same hash, otherwise a new `deploy` revision) and, on
success, makes it the applied revision. The agent compares every container's
start time before and after `up`: a deploy that started no container (none
created, recreated or restarted) reports `unchanged`, and the manager then
keeps the stack's last deploy time (the applied revision is still set).
**The agent never writes
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

### Pull

`POST /stacks/{id}/pulls` (202 + job) enqueues `stack.pull` (capability `stack.update`) only downloads the stack's images
(Compose pull with the registry credentials of the command, #19); it never
creates, recreates, stops or starts a container. Its output lists the
services whose tag now names another image (`pulled`); the next deploy runs
them. `stack.update` (#20) is the pull that also recreates changed services.
A pull does not hide an update from an update policy: `update.check`
compares the registry with the **applied** digest (what runs), not with the
tag on the host, and `update.run` recreates a service whose pulled image
differs from the one its container runs.

### Restore

`POST /stacks/{id}/revision-restores {revisionId}` needs
`stack.definition.write` and an online environment. It reads the current
definition (recording it first if it was new, so no edit is lost), writes
the revision's files with `compose.write` in `replace` mode —
compare-and-set on the current hash (`409 stack_definition_changed`
otherwise) and removing definition files the revision does not have — and
records a `restore` revision. It **never deploys**: `deployOffered` tells
the client to offer one.

### Removal

`stack.remove` takes the stack down (compose down: containers and networks)
and forgets it when that succeeds; the project directory stays. Volumes stay
too unless the request sets `removeVolumes` (the delete dialog's checkbox,
off by default). Then the agent, inside the `down` step, records before
anything is stopped which volumes the stack owns: the top-level volumes its
definition declares that are not external, and the anonymous volumes of its
containers (the definition must load, else nothing changes,
`project_unreadable`). After compose down it re-checks each one and removes
it without force only if it still carries this project's Compose labels
(`com.docker.compose.project` and `com.docker.compose.volume` = the key;
anonymous: `com.docker.volume.anonymous`), no container mounts it, it is not
Docker Manager's own (#32) and the manager does not hold it (a migrated
stack's retained source of the same project, #35). Kept volumes are skipped
job items with the reason; the removal still succeeds. Never the Compose
SDK's `down --volumes`. Agents announce `stack.remove_volumes`; the manager
refuses the option (501 `agent_unsupported`) for other or offline agents.

### Rename (`stack.rename`)

`POST /stacks/{id}/rename-previews {name}` returns the agent's plan (outside
containers the caller cannot see are counted, not named);
`POST /stacks/{id}/renames {name}` (If-Match, stored idempotency) re-runs it
and enqueues `stack.rename`: 409 `stack_rename_blocked` with the blockers,
`stack_name_taken`, 501 `agent_unsupported` for agents without
`stack.rename`. `stack.rename` alone never stops or replaces a container
outside the stack: each one the rename would recreate needs the caller's
`container.stop` and `container.remove` on it (`container_not_permitted`
blocker, also in the preview).

Renaming a stack changes its Compose project name (and its directory when
that is named after the project: always in the stacks volume, in place in a
registered root). Compose names volumes, networks and containers after the
project, so the agent carries the data over (`internal/agent/stacks/rename.go`):

1. `prepare`: `planRename` (also served as `compose.rename_preview`) decides
   what moves and refuses with blockers, changing nothing: a top-level
   `name:` in the Compose files pins the project name (equal to the current
   name: refused, change `name:` instead; different: the only target is that
   name), Docker Manager's own project (#32), an existing target directory,
   volume or project, a volume another Compose project's container mounts
   (its files name it), a protected or held volume, a volume whose data
   cannot move (another driver, not in Docker's volume directory), an
   outside container attached to one of the project's networks.
2. `stop_containers`: the services that run stop (lifecycle), and the
   containers outside any Compose project that mount a volume that moves.
3. `move`: every named volume whose name follows the project gets its new
   name: a plain local volume by renaming its `_data` directory into a new
   volume (same disk, no copy), a volume with driver options by a new
   volume with the same options. The new volume is created exactly as
   Compose would (`compose.Project.VolumeSpec`: labels and config hash), so
   Compose adopts it unchanged. Then the directory is renamed. Until the
   switch, `undo_rename` moves everything back and `start_containers` starts
   what ran under the old name.
4. `recreate`: the switch (`rename.switched`, journaled): built images are
   tagged with the new project's names, outside containers are recreated on
   the new volume names with their complete configuration
   (`engine.Cloner`), the old project is taken down, the project is
   created (not started) from the current files under the new name, the
   anonymous volumes' data moves into the new containers' anonymous volumes
   (same service, replica and target) and the old volumes are removed.
5. `start_containers`: what ran starts again under the new name.

After the switch the stack lives under the new name even when the job
fails; the manager's finish hook records the new name and directory (a new
revision, so the ETag changes) and, like an import, the definition the
rename created as its revision (reused when the bytes are unchanged);
`stacks.Service.OnRenamed` hooks run in the same transaction (the
resources service rewrites the moved volume names in saved specs of
Docker Manager–managed standalone containers).
Rules that follow the stack (stack and service scopes) keep applying;
rules on individual containers name containers, whose names follow the
project, so they must be granted again for the new names. Restores of
snapshots taken before the rename write into the stack's current volumes
(backups.md, "Restores").
A deploy whose files set a top-level `name:` other than the stack's project
name fails before changing anything (`stack_project_renamed`): it would start
a second project with empty volumes next to the running one. Agents
announce `stack.rename`.

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

### Details and links

Display name, description, per-service descriptions and **links** are
Docker Manager metadata, never written to Compose files. A service's
description is filled once from its `docker-manager.description` label
(or the legacy `dev.neureka.docker-manager.description`, which Compose
files written before 2026-09-28 carry) when it has none (`importLabelMeta`).

Stacks and services have **no icon** of their own: the web shows the stack
tile (or the image of the template the stack was created from) and one
service tile for every service. The former `icon` members of `POST
/stacks`, `PATCH /stacks/{stackId}` (the stack's and each service's) and
the two imports are deprecated and ignored; the `icon` members of the
stack and service responses are deprecated and never returned. Migration
`20260928192810_clear_stack_icons` cleared the stored icons (the
`stacks.icon` column stays, unused) and the former
`dev.neureka.docker-manager.icon` label is no longer read
(`protocol.ComposeService.Icon` stays, deprecated, so results of agents of
the previous version still decode).

Links
(`domain.Link`: optional label, URL; `stacks.links`, a JSON list in the
user's order, migration `20260928180542_stack_template_links`) point to
the stack's documentation, website or repository. They come with a
creation (`links` of `POST /stacks`), are copied from the template by
`CreateFromTemplate`, and are replaced by `PATCH /stacks/{stackId}`
(`stack.manage`, If-Match, a new metadata revision and a `stack.updated`
event like any details edit). `domain.NormalizeLinks` checks them (at most
10, absolute `http`/`https` URLs of at most 2048 characters without user
information, each URL once, labels of at most 60 characters) and names the
field of the first problem (`422` on `body.links[1].url`). Only the full
view carries them. URLs may carry query strings: they are never logged or
audited (the audit diff has the number of links). The web shows them under
the header's meta row (`LinkList`) and edits them in "Edit details"
(`LinksEditor`, the same rules inline).

## Discovery and import

`GET /environments/{id}/stacks/discovered` (`stack.import`) lists the
Engine's Compose projects from container labels (`compose.discover`):
services with container counts, the project directory and Compose files
from the labels, whether the project is **adoptable in place** (its
directory is below the stacks volume or a verified registered root and its
Compose/env files are inside it), whether it is **copyable** (not
adoptable, but the agent reads its directory through an import mount, below)
and the Docker Manager stack already managing it. Labels never reconstruct a
source.

It also lists **containerless** projects (`containerless`: never started,
or after `docker compose down`), found through their Compose files
(`internal/agent/stacks/discover.go`): every direct, non-hidden subfolder
of a verified stack root holding one of Compose's default files (adoptable
in place, `location` set), and every import mount plus up to two folder
levels below it (never descending into a project, hidden folders and
symlinks skipped; importable by copy with the same checks as a project with
containers, `sourceDir` the folder's host path). The walk is bounded (2000
folders, 200 projects; the agent logs a warning when it stops). The project
name is the one Compose resolves (top-level `name:`, else the host folder's
name normalized, `compose.DeclaredName`/`NormalizeProjectName`), the
services come from the file with every profile (no containers). A folder
whose files do not load is listed with the reason and cannot be imported;
two folders resolving to the same name are listed once and neither is
importable; a folder whose name or directory a project with containers has
is skipped (that project has everything). Every project also reports its
existing named **volumes** (`volumes`, sorted, at most 100): those labeled
`com.docker.compose.project=<name>`, those its containers mount and, for a
containerless project, those its Compose file resolves to (named, `name:`
and external); anonymous volumes and Docker Manager's own are left out. The
manager marks a containerless folder that is already a stack's project
directory (same root and dir, another project name) as managed by that
stack, and `POST /stacks` and template creations ignore containerless
projects of the name (the directory check refuses an existing folder).

`POST /environments/{id}/stacks/imports`:

- without `source`: adopt in place; the real files become the first
  revision (`external`); the stack starts `deployed` with no applied
  revision (Docker Manager has not deployed it yet, so it shows undeployed
  changes); a containerless project starts `undeployed` with Engine state
  `missing` instead and nothing is started (its first deploy reuses the
  project's volumes: the stack keeps the project name);
- with `source` (projects outside the roots): the given definition is
  written into a **new** directory `<projectName>` of the stacks volume.

### Import by copy (`stack.import`)

Projects that live elsewhere on the host (for example `/opt/stacks/<name>`
from another tool) are imported by **moving** them into the stacks volume.
The agent needs to read them: mount the host directory, or one above it,
into the agent **at or below `/import`** (`storage.ImportDir`; read-only is
enough, e.g. `/opt/stacks:/import/stacks:ro`, or several mounts such as
`/import/opt` and `/import/srv`). The mount is optional: without it
everything else works and such projects are simply not copyable. The agent
finds its import mounts in its own container's mounts at startup
(`storage.Result.Imports`) and maps a project's directory on the host to
where it reads it (`ImportSource`); an agent running directly on the host
reads the path itself. The host directory comes from the working directory
labels of the project's containers (`stacks.ProjectDir`): a label is a host
path when Compose ran on the host, while a manager that runs Compose inside
its own container (Arcane, Dockge, Portainer, ...) records its internal path
(e.g. Arcane's `/app/data/projects/<name>`), which is translated through the
mount of the container covering it (Arcane's data volume at `/app/data`).
All of a project's containers must lead to the same directory (Arcane
projects often mix both forms); discovery reports it as `sourceDir`. The
import refuses a container that bind-mounts a path below such an internal
directory (a manager that did not translate relative binds: that data is
elsewhere on the host and would not be copied).

`POST /environments/{id}/stacks/import-copies {projectName}` (`stack.import`,
202 + job, stored Idempotency-Key) creates the stack at once (the job's
target: origin `imported`, **the same project name** and a directory of
that name in the stacks volume, so the project's named volumes, networks and
containers keep their Compose names) and enqueues `stack.import`. The
manager refuses projects that are adoptable in place or not copyable (`409
stack_not_copyable` with the agent's reason) and agents without
`stack.import_copy` (`501 agent_unsupported`). The agent's steps
(`internal/agent/stacks/import.go`):

1. `prepare`: loads the project where it is (every profile), refuses
   definition files outside its directory and bind mounts that reach next
   to it through a relative path (`import_not_relocatable`: after the move
   they would point into the stacks volume), refuses when an image of a
   service is not on the host (`import_image_missing`: nothing is pulled),
   when a container runs from another directory (`import_source_changed`),
   when the stacks volume has a directory of that name
   (`stack_directory_exists`) or not enough free space
   (`insufficient_space`), and records the services that run. Absolute
   binds into the original directory are warned about: they keep using it.
   Then it compares every container with what Compose would create from the
   files (`driftOf`, `compose.Project.Expected`, `engine.ConfigInspector`):
   image, environment (values compared in memory, only names reported;
   variables inherited unchanged from the image are fine), the labels the
   files set, command, entrypoint, user, working directory, published ports
   and bind/volume mounts (bind sources translated to the host directory;
   a difference names both sources). Any difference refuses the import
   (`import_config_drift`): the tool that deployed the project supplied
   settings outside its files (Portainer, Komodo or Coolify variables, a
   pass-through variable from its process environment) or the files changed
   after its last deploy, and recreating would silently change the service.
   When a definition file is newer than the oldest differing container
   (`editedAfter`), the refusal names the file and both times and says to
   redeploy from that tool first. Otherwise: put the values into the files
   (usually `.env`) or redeploy from that tool, then import again.
   The image of a build-only service (a `build` section and no `image`) is
   not compared: the tool that built it named it (Arcane:
   `arcane.local/<project>-<id>/<service>:latest`), and the import keeps the
   image it runs (`recreate` tags it with Compose's name,
   `<project>-<service>`, so nothing is rebuilt).
2. `stop_containers`: journals the `start_containers` compensation, stops
   the running services in dependency order and refuses to continue while
   any container of the project still runs (`shutdown_failed`): the copy is
   taken at rest, so databases next to the Compose file are consistent.
3. `copy_files`: copies the whole directory into
   `.docker-manager-import-<jobId>` in the stacks volume with the migration
   archive format (`migration.CopyTree`: numeric owners, permission and
   setuid/setgid/sticky bits, modification and access times, symlinks as
   links, hard links, FIFOs, empty directories; sockets and device nodes are
   skipped and listed), compares both trees entry by entry
   (`migration.VerifyTree`: type, size, mode, owner, mtime, link targets),
   copies extended attributes (`migration.CopyXattrs`: POSIX ACLs, file
   capabilities, user/trusted attributes; not SELinux labels), flushes the
   copy to disk (`migration.SyncTree`: fsync of its files and directories
   only, cancellable; never a host-wide `sync(2)`, which waits for every
   filesystem of the host and cannot be cancelled), renames the staging
   directory to `<name>` and flushes the stacks volume's directory
   (`migration.SyncDir`). The
   `remove_import_copy` compensation (journaled after the staging directory
   exists, with its device and inode) removes the copy on any failure until
   the switch, never a directory it did not create.
4. `recreate`: loads the copy, checks every bind source moved with the
   directory or stayed the same absolute path, then **switches** (journaled
   `import.switched`, copy compensation released), tags the images of
   build-only services (`keepBuiltImages`) and recreates, with Compose
   `create --force-recreate`, every service that had containers (anonymous
   volumes inherited, nothing started, nothing pulled or built).
5. `start_containers`: starts exactly the services that ran before,
   dependencies first (`lifecycle.Resume`); a service whose containers still
   use the original directory is never started (kept stopped, warned).

Docker Manager's own project (#32; discovery reports it `protected`) is
imported **while it runs** (`import.live`): `prepare` refuses it when a
service binds a writable path inside the project directory (that data
cannot be copied consistently while it runs), `stop_containers` stops
nothing, `recreate` switches without recreating, and `start_containers`
reports the services as kept running (warning `kept_running`). Its
containers keep running from the original directory until the stack's next
deploy recreates them from the copy (the agent's own service through the
self-update helper, `internal/agent/selfupdate`). The stop step refuses
Docker Manager's own project on its own whenever the import did not prepare
it as live.

The original directory is only ever read. A failure **before the switch**
changes nothing: the copy is removed, the services that ran start again
from the original directory, and the finish hook forgets the stack. After
the switch the project lives in the stacks volume: a failure leaves the
stack `failed` on its copy and a deploy finishes it (no automatic rollback,
#25). A successful import records the copy's definition as its revision
(a new `deploy` one unless a revision already holds those bytes) and makes
it the applied revision (Docker Manager created the
containers from exactly those bytes), with the applied images. Remove the
original directory by hand once the stack runs from its copy.

A **containerless** project is imported by copy with
`import.containerless` in the input, sent only to agents announcing
`stack.import_containerless` (`protocol.FeatureStackImportContainerless`;
other agents: `501 agent_unsupported`). The stack is created `undeployed`.
`prepare` refuses it once the project has containers
(`import_source_changed`: "import it again from the list") or its folder
resolves to another project name now, and skips the drift, directory and
image checks (there is nothing to compare); `stop_containers`,
`recreate` (before the switch) refuse it when containers appeared;
nothing is stopped, recreated or started, and `recreate` only switches.
The finish hook records the copy's files as the observed revision
(`external`, not applied), reports no images and leaves the stack
`undeployed` for its first deploy, which reuses the project's volumes.

The web UI's stack list has one **Import project** dialog
(`ImportStackDialog`, `routes.importStack()`): the discovered projects of an
environment, each with one Import button that adopts in place or imports by
copy (with the job's progress in the row) and otherwise explains how to add
an import mount. A containerless project shows **No containers** instead of
its running count and imports without a stop confirmation; every row lists
the project's volumes (`volumesLine`: the first three, "+N more"). A **Hide managed stacks** switch (on by default,
`importCandidates`) leaves out projects Docker Manager already manages,
except those imported from the open dialog. Imports with an explicit source
remain an API feature.

Nothing is overwritten silently: a project Docker Manager already manages →
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
| `stack.definition.write` | revision restores, validation of the definition on disk (`POST /stacks/{id}/validations`) |
| `stack.create`, `stack.import` | creation/validation, discovery/import in an environment |
| `stack.manage` | display metadata (never written to Compose files) |
| `stack.deploy`, `stack.start/stop/restart/down`, `stack.remove` | the jobs |
| `stack.build` | `POST /stacks/{id}/builds` (rebuild the build sections without deploying, #33) |
| `stack.update` | `POST /stacks/{id}/pulls` (pull the images without deploying) and image updates (#20) |
| `stack.rename` | rename previews and renames (outside containers also need `container.stop` and `container.remove`) |

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
containers' labels: Docker Manager's `docker-manager.depends_on`
(`service:condition:restart:required`, set by the Compose adapter on every
service; containers deployed before 2026-09-28 carry the legacy
`dev.neureka.docker-manager.depends_on`, read the same way) or Compose's
own label (no `required`: treated as required).
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
