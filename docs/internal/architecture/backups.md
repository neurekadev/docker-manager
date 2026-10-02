# Backups (#10, #24)

Docker Manager backs up the manager's state and the stacks and volumes of every
environment with **restic**: a pinned, checksum-verified restic 0.19.1 in
both images (`deploy/docker/*.Dockerfile`), run by `internal/restic` — one
of the two production process executions in Docker Manager (the other is
the agent's smartctl runner for disk health, see
[engine-integration.md](engine-integration.md#process-execution)).

| Package | Role |
| --- | --- |
| `internal/restic` | The runner (`Runner`, `Repo`): arguments, child environment, secret delivery, JSON parsing, exit-code classes, cancellation. `restictest` is an in-memory restic for executor tests. |
| `internal/backup` | Shared by manager and agents: destinations and scopes, snapshot tags, the portable manifest, the retention algorithm, `OpenLocation` (key rotation per location), `ApplyRetention`, `Prune`, `Verify`. |
| `internal/protocol` (`backup.go`) | Job inputs/outputs, the `backup.scope_preview`, `backup.snapshots`, `backup.contents` requests and the `backup.file` stream; `CommandSecrets.Repositories`. |
| `internal/agent/backups` | Scope planning, the `backup.run`, `backup.retention`, `backup.verify` executors, requests and stream. |
| `internal/manager/backups` | Repositories, the Recovery Key, policies, runs (backup sets), the snapshot index, the manager-state snapshot and set manifest, the `manager.backup`, `manager.retention`, `manager.verify` executors, finish hooks, scheduler sources, command secrets. `s3probe` tests S3 capabilities over a client that refuses loopback and link-local addresses. |
| `internal/manager/api` (`backup_repositories.go`, `backup_policies.go`, `backups.go`) | The public routes. |

## Model

- A **backup repository** is a destination: a local directory on one
  executor (the manager, or one environment's agent) or an S3
  bucket/prefix. It belongs to the instance, never to a user.
- Below it every **scope** has its own restic repository (a
  **location**): `docker-manager` for the manager state,
  `docker-manager-env-<environmentId>` for an environment's data. A local
  repository serves only its own executor's scope; an S3 repository serves
  every scope. Ownership, locking and retention stay per location.
- Every repository (local or S3) has a **compression** mode: `auto`
  (default, restic's), `max` or `off` (`domain.BackupCompression*`,
  column `backup_repositories.compression`). It is editable at any time
  and applies to data written afterwards: new backups, and the data a
  prune repacks; what is stored keeps its compression.
  `backup.Destination.Compression` carries it (`""` for auto, never
  written out, so manifests and older agents see the destination they
  know); `Destination.Location` sets `restic.Location.Compression`.
- Removing a repository (refused while a policy uses it) also removes
  its snapshots from the index and the sets held only by it: they cannot
  be browsed or restored without it. Finish hooks do not index snapshots
  of a removed repository; the restic data at the destination is kept.
- A **policy** covers one environment or all environments. Overlap is
  rejected (`backup_scope_overlap`). Every managed stack and standalone
  volume in its scope is selected at preview and run time unless its stack
  ID or volume name is excluded. Volume exclusions also apply to the
  selected stacks' volumes (named and anonymous). All-environments volume
  exclusions use `environmentID/volumeName`. Anonymous volumes (Engine
  label `com.docker.volume.anonymous`, and a stack container's unnamed
  mounts) are left out unless the policy's `anonymousVolumes` switch is on
  (default off), and so are buildx builder volumes
  (`buildx_buildkit_<builder>_state`, `protocol.IsBuildxVolume`: rebuildable
  build cache) unless `buildxVolumes` is on (default off). The user-set
  label `docker-manager.backup.exclude=true` (`protocol.LabelBackupExclude`,
  one of the `protocol.UserLabels` users may set under the reserved
  prefix, like the update label) leaves a volume out:
  on the volume itself, or on a container that mounts it. The manager
  applies it to standalone volumes (`standaloneVolumes`), the agent to a
  stack's named and anonymous volumes (`planStackVolumes`, source reason
  names the label); a stack's project directory is always backed up. A
  stack volume created before the label was added to its Compose file
  never carries it (Docker keeps a volume's labels): the deploy records it
  as a Compose label (`internal/agent/volumelabels`, see
  [docker-resources](docker-resources.md)) and `includeVolume` honors it
  (reason "the stack's Compose file gives the volume the label …"; a
  declared `"false"` wins over the volume's `true`). The policy wizard
  (`VolumeCoverage`) lists label-excluded volumes unchecked and locked with
  an (i) naming where the label is (`labeledBy`: volume, compose,
  container).
  Docker Manager's temporary objects never get into a backup:
  **temporary containers** (`protocol.IsHelperContainer`: a container set
  aside during a standalone image update or a stack rename,
  `<name>-docker-manager-update-<12 hex>` / `<name>-docker-manager-rename-<12 hex>`
  (`protocol.UpdateAsideInfix`, `RenameAsideInfix`), normally removed within
  seconds and left behind only when removing it failed; Compose's
  temporary replacement during a recreate, `<12 hex>_<name>` with
  `com.docker.compose.replace` (Compose keeps the label after renaming the
  replacement, so the name decides); the agent's self-update helper,
  `docker-manager.role=self-update`, or its legacy key) never count as users of a
  volume: `standaloneVolumes` ignores them for the managed-stack and label
  checks and leaves out a volume only they use, `planStackVolumes`
  discovers anonymous volumes only on the other containers (one only a
  temporary container mounts is listed as excluded, "only a temporary
  container of Docker Manager or Compose uses it"). And a standalone volume
  an **environment migration** created (`docker-manager.migration=<migration ID>`
  or its legacy key,
  #35) is selected only when that migration succeeded (`completed` or
  `source_removed` in the manager's migration record); a failed, cancelled,
  interrupted, still running or unknown one left a partial copy the next
  migration of the stack removes. Docker maintenance keeps protecting these
  volumes as before (`VolumeReferences` uses the selection without these
  two rules). The UI's `coveredVolumes` repeats these rules, except the
  migration one (it cannot see how a migration ended: such a volume is
  listed and left out at run time). The policy also configures manager state
  (owner only), container shutdown (off by default), a schedule (#13,
  starts disabled), and retention. A migrated stack is covered by the
  destination environment's policy. Existing snapshots retain their source
  location. Docker maintenance (#14) protects covered standalone volumes
  (`Maintenance().SetBackupReferences`); stack volumes are protected as part
  of Docker Manager stacks, and a local repository mounted into the Docker Agent
  is Docker Manager's own (#32).
- A **run** of a policy is one **backup set**: a `backup.run` job per
  environment and, with the manager state, a `manager.backup` job queued
  last. Each member (stack, volume, manager state) is its own snapshot with
  its own time; multi-host sets are not atomic. A set with failed or
  missing members is `partial`, never `complete`; `retrySetId` re-runs only
  the members that did not complete. A member whose item no longer exists
  when its turn comes (a standalone volume the Engine answers Not Found
  for, e.g. a CI job's temporary volume removed after the run was planned;
  a stack whose project directory was deleted while its parent directory is
  there) is **skipped** (`backup.StateSkipped`, error class
  `item_gone`): no snapshot, the job item says so ("skipped: volume x was
  removed before its turn") and it is recorded in the host manifest and
  the set. Any other error (an unreadable or unmounted directory, an
  inspection failure) still fails the member. `backup.Completeness` and
  `settle` leave skipped members out: a set whose other members completed
  is `complete`; a set whose members were **all** skipped is `skipped`
  (nothing was backed up and nothing failed: neither `complete`, which
  would claim a backup, nor `failed`); it gets no retention follow-up, and
  a retry never re-runs skipped members (`nothing_to_retry` when only they
  remain). A `backup.run` whose items were all skipped succeeds; one with
  failed and skipped items only still fails (`empty_scope`). An older agent
  keeps failing such members (`volume_unavailable`), as before. One run per policy at a time: a
  scheduled run is skipped while a `backup.run` or `manager.backup` job of
  the policy is not finished (scheduler overlap), and a new manual run is
  refused with 409 `backup_run_active` (a retry and an idempotent replay
  are not). The UI spins the policy's **Back up now** button while
  `/backup-activity` lists a job of the policy.
- **Cancelling a run** (`POST /jobs/{jobId}/cancellations` on its
  `backup.run` or `manager.backup` job) does not wait for the snapshot
  step to end: the step runs restic under `StepContext.WatchCancel`, which
  ends restic's context within a second of the request (SIGINT, restic
  writes no snapshot for the item it was reading), and the step returns
  `jobexec.ErrStepCancelled`, so the job ends `cancelled`. Items backed up
  before keep their snapshots (the output carries them; the finish hook
  indexes them), the item being read and the ones after it stay pending and
  are recorded as failed with class `cancelled`, so the set is `partial`
  (or `failed`) and a retry re-runs them. No host manifest is written; the
  `start_containers` compensation restarts what the run stopped. The
  `manager.backup` finish hook removes the job's staging copy of the
  database whatever the outcome (also when a cancel lands between restic's
  end and `write_manifest`, or after a failure or restart). Between items
  the snapshot step honors a request at once (`sc.CancelRequested`).
- **Retention jobs** (`backup.retention`, `manager.retention`) report
  their stages as job progress (opening, removing the backups the rules no
  longer keep, "removed N backups, kept M", freeing space): restic's prune
  has no live progress, only a closing summary. Cancelling stops a running
  prune the same way (`WatchCancel`, then `ErrStepCancelled`; restic keeps
  a repository usable wherever a prune stops and the next prune finishes
  it); the output keeps what forget removed (the index follows it) with
  `pruneError: cancelled`. Every prune marks its location as pending
  first (`backup.MarkPrunePending`, files under `prune-pending/` in the
  agent's state directory or the manager's data directory) and clears the
  mark only after it succeeded, so the next retention of a location whose
  prune was cancelled, failed or died prunes even when it forgets nothing. Forget itself is short and never interrupted
  (its output must match what restic removed).
- The **snapshot index** (`backup_snapshots`, the API's "backups") is
  filled by the jobs' finish hooks and caught up by verification jobs,
  which list what a location holds; a restored manager also reconciles it
  with every manifest the import read (snapshots written after its own
  state snapshot).

## The Recovery Key (#25 Q7)

**Decision: one instance-wide Recovery Key**, the restic password of every
repository Docker Manager creates (manager and every environment, local and S3).
The owner stores one key; any single repository opens with it; the UI's
statement "this key opens all Docker Manager backups" is true. A per-repository
key bundle was rejected: more keys to lose, and a fresh import from one
host repository would need to know which key belongs to which location.

- Generated by the first repository created (owner only), returned once
  with its fingerprint (`rk_` + 16 hex digits of a domain-separated
  SHA-256), stored sealed with the secret-protection key
  (`backup_key_state`), never returned by read routes, never logged,
  audited (fingerprints only), put in URLs, arguments or job inputs.
- Format `DYRK-` + 13 groups of four base32 characters: 240 random bits and
  a checksum, so a typo is `recovery_key_malformed`, not a wrong key.
- **Confirmation** (`POST .../recovery-confirmations`, owner, session only):
  the owner re-enters the key and states it is saved (`backedUp: true`).
  Every repository needs it before policies can use or enable it; nothing
  is initialized before. The API and docs say plainly that confirming does
  not prove the key is stored safely.
- **Rotation** (`POST .../key-rotations`, owner, session only, step-up):
  a new key is returned once; confirming it makes it current and keeps the
  previous key until every known location moved. A job opening a location
  (`backup.OpenLocation`) adds the current key where only the previous one
  works and removes the previous key; verification jobs for every location
  are queued at once. While locations remain on the previous key the
  rotation is **partial**: `keyState.pendingLocations` lists them,
  connection tests report `previousKey`, a fresh import of those needs the
  previous key too (`backup_import_key_rotated` / the import's
  per-location `key: previous`), and a new rotation is refused
  (`key_rotation_in_progress`).
- Agents receive the key (and, during a rotation, the previous key) only in
  a command's `secrets.repositories` at dispatch, or in the `credential`
  of a `backup.snapshots` / `backup.contents` request or `backup.file`
  stream; never journaled.

## Secrets and restic

`internal/restic` builds the child environment from scratch
(`RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, cache, temp; S3 credentials as
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` of that child only). The
password file is an inherited pipe (`/proc/self/fd/N`) on Linux, so the key
never touches a disk; elsewhere a 0600 file in a private directory removed
after the call. Error text from stderr is bounded and scrubbed of every
secret of the call. Exit codes map to stable classes (`repository_not_found`,
`recovery_key_rejected`, `repository_locked`, `storage_access_denied`,
`storage_unreachable`, `repository_damaged`, `snapshot_not_found`,
`restic_unavailable`, `restic_failed`), which are job error classes.
Cancellation sends SIGINT (restic releases its locks), then kills.
Every call waits `--retry-lock` (2 min) for a lock. restic never removes a
lock itself, so a lock left by a killed process (a manager or agent
restart during a run) would block every later run: when a call still finds
the repository locked, the runner runs `restic unlock` (it removes only
stale locks: not refreshed for 30 minutes, or whose process is gone on the
same host) and repeats the call once. Calls reading stdin are not
repeated. A `repository_locked` failure therefore means a live run holds
the lock.
The compression mode is `--compression max|off`, passed only to the calls
that write pack files (`backup`, `prune`); auto passes nothing, and no
other call (`cat config`, `check`, `snapshots`, ...) gets the flag.
restic compresses only repositories of format version 2 (restic 0.14 or
newer; every repository Docker Manager initializes): `backup.OpenLocation`
reads the version with `cat config` and, for a version 1 repository (an
old one imported from another restic), reopens the location without the
mode (`Opened.CompressionIgnored`, logged by the agent) instead of failing
the backup, so it keeps writing uncompressed as before. Upgrading such a
repository (`restic migrate upgrade_repo_v2`) is left to the operator.
The manager's own restic (manager-state backups and their prune) uses the
same `Destination`, so it honors the mode as well.

Agents get the mode only when they announce `backup.compression`
(`protocol.FeatureBackupCompression`). Repository references
(`repositoryRef`) never carry it, so no request (scope preview, snapshot
listing, contents, restore preview) and no stored job input does; at
every dispatch, `backups.Service.CommandInput` (`jobs.Options.CommandInput`)
sets the repository's current mode in `repository.destination.compression`
of `backup.run` and `backup.retention` commands for agents announcing the
feature (other kinds, such as `backup.verify` and `restore.run`, do not
write data). An older agent gets the input as stored and backs up and
prunes with restic's default (auto) instead of failing; it works again
with the chosen mode once upgraded.
restic retries every backend error its S3 backend does not deem permanent
for 15 minutes, with no option to shorten that (a wrong secret key,
`SignatureDoesNotMatch`, is retried); the runner reads restic's retry
notices and stops the run at the first one that no retry can fix (access
denied, unknown key ID, clock skew, missing bucket) with its class.

The manager's own S3 requests (`s3probe`: connection tests and the
import's scope listing) go through `s3probe.NewClient`: a direct
connection (no proxy from the environment), no redirects followed, and a
dialer `Control` that refuses loopback, link-local (`169.254.0.0/16`,
`fe80::/10`, so `169.254.169.254`), the IPv6 metadata address
`fd00:ec2::254`, unspecified, `0.0.0.0/8` and multicast addresses. It
checks the address being connected to, after DNS resolution, so a name
that resolves or rebinds there is refused too. Private ranges stay
allowed (MinIO on the LAN or a Docker network). A refusal is the class
`address_not_allowed` ("this address is not allowed"), never a status or
reachability verdict: without the check the connection tests, which a
fresh manager answers to anyone during setup, would probe internal
services. A connection test (repository or import) runs no restic after
a refused address. restic is an external program with its own HTTP
client; its runs (backups, retention, the import's preview) are not
covered by the check.

## Scope (agent)

A stack member backs up its **project directory** (Compose files, `.env`,
workspace, every relative bind source inside it) and its **named volumes**
(per-volume include/exclude). **Anonymous volumes** only with the policy's
toggle (default off). **Bind sources outside the project directory**
(`../data`, `/srv/x`) are shown in the preview as `requires_opt_in`; they are
included only when the stack's selection lists them in `externalPaths` **and** they lie
below the agent's `DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST`. A policy's `externalBinds`
switch (default off; UI: **Back up allowed folders outside stacks**) fills
each selected stack's `externalPaths` with its recorded outside bind sources
(`StackBind.External`, `externalBindSources`: clean absolute paths, at most
64) in `scopeSelections`; system paths and
Docker's data root never. Path excludes are relative to the project
directory (or the volume root). Sources resolve through symlinks and must
stay in their root (a symlinked bind leading out is `blocked`); restic
stores symlinks inside the tree as links. Docker Manager's own volumes are never
selected (#32). A local repository inside (or containing) a source is
refused (`repository_inside_source`), as is a local location outside the
agent's `DOCKER_AGENT_BACKUP_LOCAL_ROOTS`.

The **scope preview** (`POST /backup-policies/{id}/scope-previews`, or
`POST /backup-policy-scope-previews` with the create body for a policy the
wizard has not saved yet: the UI saves a policy only at its last step, and
the draft preview refuses a scope another policy covers) asks each
agent for the effective sources with states and reasons, excludes, the
estimated size (bounded walk), and with shutdown on the containers that stop
in their stop order, the downtime warning and the conflicts a shutdown
cannot cover (a volume also used by another project's container, which is
never stopped). The manager part shows the database size and that
`metrics.db` is excluded by default.

## Backup-time shutdown

With the toggle on, `backup.run` records each affected service's state,
registers the `start_containers` compensation (journaled on the agent
before anything stops: a manager disconnect or agent restart cannot strand
containers), stops the running services of each stack in reverse
dependency order (`internal/agent/lifecycle`), snapshots, then starts
exactly the services that were running, dependencies first
(`lifecycle.Resume`, which refuses rather than start a service that was
stopped). The compensation runs after a failure, a cancellation or a crash
(`jobexec.Recover`). A failed stop aborts the job before any snapshot.
Docker Manager's own project is backed up live (#32): the preview gives none of
its containers a stop order and says so, the run never stops it and its
snapshot stays `live` even when other stacks of the same run are stopped.
Snapshots taken with the stack stopped are marked `consistency: shutdown`,
others `live` (crash-consistent). Resuming honors dependency conditions
like a deploy: a dependency that comes back unhealthy keeps its dependents
stopped and fails the job with `restart_failed` (the backup itself is
complete); a completed one-shot that was not running satisfies
`service_completed_successfully` without running again.

## Manager state

`manager.backup` takes a consistent SQLite snapshot (`VACUUM INTO`) into
`<data>/backup-staging/<job>/docker-manager-state/`, adds `secret-key.bundle`
(the secret-protection key sealed with XChaCha20-Poly1305 under
HKDF-SHA256(Recovery Key)) and `state.json` (app version, applied
migrations, instance, secret key ID, `templatesIncluded`) and
`templates.tar.gz` (every template draft, `templates.WriteDrafts`;
published versions are in the database), backs the directory up
(`docker-manager-state` tag), then writes the **set manifest**. The
metrics database is included only when the policy asks for it. The staging
directory is removed after the run and at every start.

## Portable manifest (#24)

One small JSON document per set and location, stored **inside the
repositories** as its own snapshot (`docker-manager-manifest` tag, file
`docker-manager-manifest.json`), so restic encrypts it with the Recovery Key and a
fresh manager finds it without the old database:

- the **set manifest** in the manager location: repositories (destination
  without credentials, key fingerprint and generation), locations
  (restic repository IDs, environment IDs, names and Engine IDs), every
  planned member with its snapshot, time, paths, consistency, state and the
  capabilities a restore needs, the manager's schema (applied
  migrations), the app version and the set's completeness;
- a **host manifest** in each environment location for the members that
  environment wrote, so one host repository alone is enough to find its
  snapshots.

Encoding: a header `DOCKER-MANAGER-MANIFEST v1 length=<n> sha256=<hex>` plus the
JSON; decoding detects truncation (`ErrManifestTruncated`), corruption
(`ErrManifestCorrupt`) and newer versions (`ErrManifestUnsupported`).
`backup.Merge` overlays host manifests on a set manifest.

## Retention and verification

Retention rules (last, hourly, daily, weekly, monthly, yearly, within days)
follow restic's keep policies but are **computed by Docker Manager**
(`backup.Plan`) so the preview and the execution are the same decision: the
executor forgets exactly the IDs the plan removes (`restic forget <ids>`,
one call per location), then prunes, **only when it forgot something** (a
prune downloads and rewrites pack data; a retention that removed nothing
skips it). Like restic's, the rules judge each stack/volume by its own
snapshots, so an item's newest snapshot always stays and failing backups
never shrink it; `last` keeps an item's newest N whatever the other rules
say. (The former minimum recovery floor did exactly that; it was removed:
migration `backup_retention_floor_into_last` raised `last` to it for
policies with rules, and the API still accepts `minKeep` as a deprecated
member folded into `last`, never returned.) Only the policy's snapshots of that location are considered; manifests of
sets without remaining data there go too.

Because the rules keep a **deleted** item's last snapshots forever, a policy
may expire them: with `retention.expireDeletedDays` > 0 (default 0, off), a
stack Docker Manager no longer has or a standalone volume its environment
no longer has loses every snapshot once its newest one is older than that
(`backups.Service.expiredItems`, then `RetentionPlan.Expire`, reason
`deleted`; the preview shows the same). Nothing counts as deleted while the
environment is archived or its volumes cannot be listed (offline agent),
and manager state never does. The items travel in
`BackupRetentionInput.Expire`, sent only to agents announcing
`backup.expire`; an older agent applies the rules alone.

Retention runs manually (`retention-runs`, `confirm: true`) or once after
every finished set when `afterBackup` is set: once per set and location
(repository and scope), never per stack or volume. A set stays `pending`
until every member has reported, even when one already failed
(`Service.settle`), so the follow-up (`flagRetention`, `RunFollowUps`) sees
every location the run wrote to. It takes the repository lock
exclusively (#26), serialized with backups and restores, and reports
reclaimed space and failures (Object Lock refusing deletions).

Verification (`backup.verify`, `manager.verify`) runs `restic check`, with
`readDataSubset` also reading pack data; damage fails the job with
`repository_damaged`. Each repository has an editable verification
schedule (#13 kind `backup_verification`, disabled until enabled).

## Live activity and storage statistics

- **Activity.** restic reports a status line every second while a backup
  runs (`RESTIC_PROGRESS_FPS=1` when the caller wants progress), including
  `current_files`. The persisted job progress (at most every 5 s) carries
  only counts ("backing up volume/media (12 of 30 files)"), because
  `job.read` holders see it. The file itself travels as
  `ProgressPayload.Activity` (`protocol.ActivityPayload`, relative to the
  item: `<volume>/<path>`, a project-relative path, else the base name):
  the engine hands activity-only frames to `Engine.OnActivity` without a
  transaction or job event, and `backups.Service` keeps the latest report
  per job in memory (dropped when the job finishes, stale after 30 s).
  Agents announce `backup.activity`; only then does `backup.run` get
  `activity: true`. `GET /backup-activity` lists unfinished backup and
  retention jobs the caller may read (`job.read`; a `backup.run` with its
  `stacks` and `volumes` counts) and returns `currentFile` only with
  `stack.files.read` / `volume.files.read` on the item (the manager state:
  the owner), and `cancellable` when the caller holds `job.cancel` on the
  job and no cancellation was requested yet. The overview and the policy
  page poll it while something runs and show it in a "Running now" card
  (`RunningBackups`): one fixed-height line per job (policy, where, stacks
  and volumes, the item with its file and byte counts, a bar, the time
  left, **Cancel** with a confirmation) and one line with the file restic
  reads; never generic job cards, which appear, list every item and move
  the page. A job leaving the list reports its outcome in a toast
  (`onJobsFinished` reads the ended job once).
- **Previews in the UI.** The scope preview (`ScopePreviewView`) lists
  each item once per environment with its stack name, estimated size and
  counts per source state; its sources (listed once: the agent repeats a
  bind two services mount and volumes without a path) open by themselves
  when something needs the user. Keyed `each` blocks never key by source
  text (a repeated key throws and nothing renders). An agent that does
  not answer the 2-minute request reads as `timeout`. "Apply retention
  now" shows `RetentionPreviewPanel` by repository and environment name,
  one row per stack or volume (removed/kept counts, the backups with the
  rules keeping them), and confirms only once the preview is loaded and
  removes something ("Remove N backups").
- **Storage.** After every backup (agent `record`, manager `write_manifest`)
  and every prune, the executor runs `restic stats --mode raw-data`
  (`backup.MeasureStats`: index and directory metadata only, never file
  contents; a failure never fails the job) and returns it as `stats`. The
  finish hooks store it on the location (`backup_locations`: size,
  uncompressed size, ratio, compression progress, restic snapshot count,
  `stats_at`). Repositories expose the sum as `storage` (with every measured
  location, so views can filter by environment).
- **Storage history.** Every stats update (`store.UpsertBackupLocation`
  with `Stats`) also appends a sample to `backup_storage_samples`
  (repository, scope, time, stored and uncompressed bytes), at most one
  per location and UTC hour (a later measurement in the hour replaces
  it). It lives in the manager database, not `metrics.db`: it is backup
  history the owner expects back after a manager-state restore (sets and
  the index are there too), it is tiny (one row per measurement) and the
  aggregation needs the repositories for authorization; `metrics.db` is
  expendable, excluded from manager-state backups and written only
  through `Store.Ingest` in 10 s slots. Samples older than two years are
  pruned on every append, except each location's newest older one (its
  value carries into the kept range); a zero sample without anything
  newer goes too. Removing a repository appends a zero sample to each of
  its locations (`store.EndBackupStorage`, in the removal's transaction;
  no foreign key), so it counts until its removal and not afterwards.
  The migration seeds every measured location's current size as its
  first sample. `GET /backup-storage/history?from=&to=&environmentId=`
  (`backup_repository.read`, filtered per repository like the storage
  figures; default the last 30 days, at most 731) returns a point at
  `from`, at every whole UTC hour (ranges up to 8 days) or day in between
  and at `to` (clamped to now); each point is the sum over the readable
  locations of their latest sample at or before it (carried forward;
  `null` before the first). The overview's **Storage over time** card
  draws stored (solid) and before compression (dashed; never below
  stored), the last 30 days by default (7 days, 90 days, a year), with a
  sentence on the change and the figures as a table.

## Restores (host data)

`POST /backups/{id}/restore-previews` and `.../restores` (`confirm: true`)
restore from a stack or volume snapshot on the agent of the environment the
data lives in now (a stack migrated since, #35, is restored where it is when
the repository is S3):

| Scope | Restores | Never |
| --- | --- | --- |
| `stack` | the project directory: Compose files, `.env`, workspace and relative bind data | volumes; redeploying (the output suggests a deploy; the definition is recorded as an observed revision) |
| `volume` | named volumes (a volume snapshot, or some or all volumes of a stack snapshot); a missing volume is created (with Compose's labels for a stack volume) | the stack definition |
| `file` | one file in place (below the project directory or a volume) | anything outside the stack and its volumes (download it instead) |
| `full` | everything the backup holds: a stack backup's project directory **and** every volume in it (one job); a volume backup's volume. With `redeploy` (stack backups, needs `stack.deploy`) the stack is deployed from the restored definition afterwards with the services that were running before | — |
| `paths` | up to 1000 selected files and directories in place, each below the project directory or one of the backup's volumes. A selected directory is made **identical** to the backup (entries it did not hold are removed); nothing outside the selection changes; a missing path is created | paths outside the stack and its volumes, paths the backup does not hold (409 `restore_refused`) |

The snapshot records where the project directory and each volume were
(`projectPath`, `volumePaths`), and `restore.run` maps them to their current
places. A stack renamed since the snapshot (#7) has its project volumes
under the new project's names: the manager restores `<old>_<key>` into
`<current>_<key>` (`restore.run` input `volumes[].name`, with the snapshot's
`source`), also for single files and paths. The preview reports targets, files and bytes, how many files are
overwritten, removed and added, the owners the files carry, free space, the
containers that stop, and what blocks the restore (running containers
without shutdown, Docker Manager's own containers, insufficient space, paths that
cannot be restored). The job stops every container using the data (Compose
projects in reverse dependency order, standalone containers directly) after
journaling the restart compensation, restores into a staging directory next
to each target (same filesystem), then swaps: the target's entries move to
a rollback directory and the staged entries into place; any failure moves
the original entries back. Only the previously running containers start
again, dependencies first. `full` and `paths` reach only agents announcing
`restore.selection` (`protocol.FeatureRestoreSelection`; otherwise 501
`agent_unsupported`).

While a restore has not ended (queued included), every job kind that
starts containers (`jobspec.Spec.StartsContainers`: stack start, restart,
deploy and update, container start, restart and unpause, update runs) is
**refused** on its data with 409 `restore_in_progress` instead of waiting
behind it: the restore starts exactly the previously running containers
itself. The restore locks its stacks, volumes and (lock-only targets) the
containers outside the restored stack that mount a restored volume. A
full restore's redeploy is a `stack.deploy` job the finish hook queues for
the restore's initiator (idempotency key `restore-redeploy:<jobId>`);
nothing is deployed when no service of the stack was running. A crash mid-swap leaves
`.docker-manager-rollback-<job>` next to the target (the job's recovery guidance
says so). Authorization: `backup.restore` on the backup **and** on every
target (stack, volumes, repository). Manager-state snapshots answer
`manager_restore_required`: the manager state is restored by importing it
into a fresh manager (below), never over a running one.

**UI.** The Backups section has three tabs: **Overview** (setup steps,
KPIs, running backups, the policies as its main table, recent runs,
storage and storage over time), **Backups** (every backup grouped by run, with Restore per run)
and **Repositories**. `/backups/policies` redirects to the overview;
restic's raw snapshots (`/backups/snapshots?repository=`) open from a
repository page. The header's primary action is "Create backup policy"
on every tab. Policy pages follow the shared policy layout (status
sentence, Back up now / Edit / overflow, KPIs, what it covers, schedule
with next runs, recent runs); creating a policy is a wizard, editing one
screen. The policy list returns every policy shown in full with the
detail's `recentSets` (the newest 5) and `schedule.nextRun`, looked up in
one batch per page (`addPolicyRuns`: one query for the sets of all its
policies, a `ROW_NUMBER()` window per policy, one for their members'
backups and one for the next runs), so the UI reads only the list
(`backupPoliciesQuery`). Set members carry `backupId`, the backup
(`GET /backups/{id}`) they took, when it exists, is not forgotten and the
caller can see it; `SetMembers` links to it.
Stacks and volumes have a **Backups** tab (volumes: right before
Migrate; `$lib/features/backups/BackupsTab.svelte`, listed with
`GET /backups?stackId=` or `?environmentId=&volume=`, which also returns
the stack backups holding the volume); it names the policies that cover
the stack or volume (`policyCovers`) with their next run and, without
backups yet, those policies' recent runs that included it
(`memberRuns`). *Restore all* restores the whole
backup (a volume's page: only that volume); *Choose files* opens a lazily
listed file tree (`FilePickerDialog`, one directory per request, at most
500 entries each, tri-state ticks; `selection.ts` keeps no path inside a
ticked one and splits a ticked folder when something inside is unticked).
Every restore is previewed and confirmed with a danger button that says
what is replaced (a full restore also needs the name typed); the stack
header hides Deploy and the lifecycle button's Start and Restart while a
restore of the stack has not ended.

## Fresh-manager import (#24)

The system restore is an **isolated recovery exercise**: a clean manager
(new data volume, no owner) is pointed at the backups. It needs only the
destination, its S3 key pair (newly issued keys are fine) and the saved
Recovery Key; not the old volume, database or a running old manager.

1. **Connection test** (`POST /setup/backup-imports/connection-tests`):
   S3 read/write/delete and Object Lock, whether the key opens the manager
   repository (`docker-manager`), and every host repository the set
   manifests name or the destination holds (S3 listing or the local
   directory), each `found`, opened with the `current` or `previous` key,
   or why not (`note`: local to a host, another repository).
2. **Preview** (`POST .../previews`): the newest 20 sets from the manifests
   (never from a database), merged with the host manifests: completeness,
   each member's snapshot `located` as `found`, `missing` (its repository
   was read and the snapshot is gone), `unverified` (not reachable from the
   manager yet: a host-local repository) or `not_backed_up`; the version
   that wrote it and whether this build knows every migration of its
   schema. With `setId`, the set's secret-key bundle is opened too.
3. **Import** (`POST .../restores`, `confirm: true`, 202 + `backup.import`
   job; progress in `GET /setup/status` → `backupImport`): `scan` dumps
   `state.json`, the bundle, the database and (when `templatesIncluded`)
   `templates.tar.gz` from the manager-state snapshot into
   `<data>/backup-import/<job>/`, opens the bundle with the
   Recovery Key (the current, else the previous one), runs
   `PRAGMA quick_check`, checks the instance and the schema, and opens the
   restored Recovery Key record with the recovered secret key (proof that
   the encrypted settings decrypt); `import_index` reads every manifest it
   can reach and renames the directory to `<data>/restore-pending/` with a
   marker (credentials and a newer key sealed with the recovered secret
   key). The job's success requests a **controlled restart** (`app.Run`
   starts the manager again in process).
4. **Apply at startup** (`backups.ApplyPendingRestore`, before the
   database opens; every step repeatable after a crash): the current
   database (with `-wal`/`-shm`) and key file move to
   `<data>/pre-restore-<time>/`, the restored database and the recovered
   secret key (`DOCKER_MANAGER_SECRET_KEY_FILE`, which must be writable) take
   their places, migrations run as usual. A staged `templates.tar.gz`
   replaces `<data>/templates` (`templates.RestoreDrafts`: extracted into
   `templates.restoring` with checked names, the current directory kept as
   `pre-restore-<time>/templates`); a snapshot without drafts leaves the
   directory alone and the templates service's startup sweep removes the
   drafts of unknown templates and creates empty ones for the rest.
5. **Complete** (before anything is served; the marker is removed only
   when every step succeeded): every stored session is deleted and every
   user's session epoch bumped (no session of the snapshot is revived);
   `auth.Service.RevokeAllAPITokens(ctx, domain.RevokedRestore)`; every
   agent is revoked and detached (`agent.restore_revoke`): its environment
   keeps its ID, stacks and backups and waits for an enrollment with intent
   `reattach:<environmentId>` (#34), so a restored credential is never
   trusted silently; the imported repository points at the destination and
   S3 key pair entered for the import (a local repository may now live at a
   new path); a newer entered Recovery Key becomes current with the
   restored one as previous (locations move as jobs use them); the snapshot
   index is reconciled with the manifests (members the snapshot did not
   know yet, and sets written after it); one `system.restore` audit record
   carries the counts.

What survives: users, groups, permissions, TOTP and passkeys, registry and
Git credentials, backup repositories, policies and the index, stacks and
their revisions, environments: all decrypt with the recovered secret key,
so nothing must be re-entered except the S3 key pair of the import (which
replaces the stored one). What does not: sessions, API tokens (revoked,
reason `restore`), agent credentials (revoked; re-attach each host), jobs
that were running (recovered as interrupted), metrics (never backed up by
default).

The same staging and startup path applies the copy a moving manager hands
over ([manager-move.md](manager-move.md)): the new manager's waiting mode
stages it with `backups.StageRestore` as a marker of kind `move` (the
move, the old manager's address, the move code sealed with the moved
secret key), and
`ApplyPendingRestore` puts it in place like an import.
`app.(*Manager).finishRestore` leaves such a marker to `finishMove`, which
revokes nothing: the moved manager is the same instance.

Errors, each with recovery guidance in the message:

| Code | When | What to do |
| --- | --- | --- |
| `backup_import_key_rejected` | the key opens neither the manager repository nor a host one | check it; after a rotation enter the previous key too; a **lost key** cannot be recovered by anyone (restic encryption): set up a new instance |
| `backup_import_not_found` | no repository at the destination, or no such set | check endpoint/bucket/prefix or the mounted path (below `DOCKER_MANAGER_BACKUP_LOCAL_ROOTS`) |
| `backup_import_manifest_corrupt` | the set's manifest is truncated or fails its checksum | choose another set; `restic check` the repository |
| `backup_import_schema_incompatible` | a newer Docker Manager wrote the set | install at least that version |
| `backup_import_key_rotated` | the set's secret key is sealed under another key (rotated after the set: **partially rotated keys**) | enter the newest key and the previous one |
| `backup_import_state_missing` | no readable manager state (host-only set, damaged bundle or database) | choose another set, or host-only recovery |
| `backup_import_unreachable` | storage refused access, unreachable, locked, damaged | the message names the class |
| `backup_import_in_progress` | another import runs | wait |
| `backup_import_secrets_lost` (job) | the manager restarted during the import (the key is kept in memory only) | start the import again |

The connection test and preview report partially rotated keys per location
(`key: previous`) and missing repositories as problems without failing.

**Host-local repositories** are never reachable by the new manager: they
show as `unverified` and become usable once their host re-attaches with
the repository directory mounted at the same path (the path is part of the
restored repository; mount it there, or recreate the agent's
`DOCKER_AGENT_BACKUP_LOCAL_ROOTS` accordingly).

**Host-only recovery** (the manager repository is lost): the preview lists
sets from host manifests (`hostOnly`), which cannot be imported. Set up a
new instance, then restore a host's data with restic directly: the
repository is a plain restic repository whose password is the Recovery
Key; its host manifests (`restic snapshots --tag docker-manager-manifest`, then
`restic dump <id> /docker-manager-manifest.json`) list every snapshot with its
paths, stack and volume.

The setup routes are public but refused once an owner exists
(`setup_complete`), outside a secure origin (`insecure_origin`) and beyond
the setup rate limit; the Recovery Key and credentials are write-only.

## Jobs and locks

| Kind | Executor | Locks | Steps |
| --- | --- | --- | --- |
| `backup.run` | agent | host S, stack X, volume S, repository S | prepare, stop_containers, snapshot, start_containers, record |
| `backup.retention` | agent | host S, repository X | forget, prune_repository |
| `backup.verify` | agent | host S, repository S | check |
| `restore.run` | agent | host S, stack X, volume X, repository S | prepare, stop_containers, restore_data, start_containers |
| `manager.backup` | manager | repository X | snapshot_database, backup, write_manifest |
| `manager.retention` | manager | repository X | forget, prune_repository |
| `manager.verify` | manager | repository S | check |
| `backup.import` | manager | repository S | scan, import_index (then a controlled restart) |

Repository locks name the Docker Manager repository (destination): the manager
backup waits for the environment backups of its set to the same
repository, so its manifest usually carries their results.

## Authorization (#17)

- Repositories: `backup_repository.read` / `.manage`; Recovery Key
  confirmation and rotation additionally need the **instance owner** and a
  browser session (API tokens get `api_token_not_allowed`); rotation needs a
  step-up.
- Policies: `backup_policy.read` / `.manage`; including the manager state
  is owner-only (`manager.backup`).
- Runs: `backup.run` on the policy, its repositories, stacks and volumes
  (the job engine checks every job again at dispatch; scheduled runs use
  the service identity and survive the removal of whoever configured
  them).
- Backups: `backup.read`; contents and downloads need
  `backup.contents.read` / `.download` **and** the capability that reads the
  same data live: `stack.definition.read` for stack snapshots (they hold
  `compose.yaml` and `.env`), `volume.files.read` for volume snapshots;
  manager-state snapshots are owner-only. Downloads are audited.

## Tests

- `internal/restic`: runner tests against the test binary acting as restic
  (secrets never in arguments, environment or logs, JSON parsing, exit
  classes, cancellation).
- `internal/backup`: manifest round trip, corruption/truncation,
  completeness (skipped members) and merge, retention rules, deleted-item
  expiry and time zones.
- `internal/agent/backups`: the scope corpus (relative binds, opt-ins,
  allowlist, anonymous volumes, exclusions, symlink escapes, nested
  repositories, Docker Manager's volumes, temporary containers), items
  removed before their turn (skipped, Not Found only), shutdown order,
  restart after failure,
  cancellation and agent crash, key rotation per location, damage
  detection, retention scope.
- `internal/manager/app/backups_test.go`: the API end to end with a real
  agent session (Recovery Key once, confirmation challenge, rotation across
  locations, runs, contents, downloads, manifests, authorization, API token
  refusal, a scheduled run after its creator was deleted, canaries in
  responses, logs, audit and the database); `backup_import_test.go`: the
  recovery proof (a clean manager imports a set across the manager and two
  host repositories with a new S3 secret, every import error, partially
  rotated keys, revocations, re-attach and a restore; a local repository at
  a new mount path; the key is never stored and a restart loses it).
- `internal/manager/backups/restoreapply_test.go`: applying a staged
  restore is repeatable after a crash and keeps the replaced files.
- All of these use the in-memory restic (`restic/restictest`) or the fake
  restic binary. The former tests against the real restic with local and
  MinIO repositories were removed on 2026-09-25: restic against real local,
  MinIO or S3 repositories is **not verified by automated tests**.
