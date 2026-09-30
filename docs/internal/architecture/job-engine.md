# Job engine (#26)

Every long or mutating operation — pull, build, deploy, stack operations,
update, prune, backup, restore, retention, repository verification, file
archive/extract/recursive metadata changes, migrations — runs as a durable,
manager-owned **job**. The scheduler (#13) only decides *when* to enqueue;
features never run their own serialization or recovery.

| Package | Role |
| --- | --- |
| `internal/jobspec` | Kind catalog: executor, capability, **lock definition**, offline deadline, steps (idempotency, cancellation safe points, recovery guidance), compensations, concurrency class, manager-restart policy. Shared by manager and agent. |
| `internal/manager/jobs` | The engine: enqueue, idempotency, lock acquisition, dispatch, fencing, reconciliation, cancellation, manager-local execution, recovery, retention. |
| `internal/jobexec` | Step runner used by both executors: journal-before-step, safe points, compensations, crash recovery. |
| `internal/agent/jobs` | Agent side: fencing check, fsync'd journal in the agent state dir, execution, reconnect report. |
| `internal/protocol` (`jobs.go`) | Command/ack/progress/result/cancel/`job_report` frames. |
| `internal/manager/authz` | `Authorizer` (the #17 permission service), principals, job targets (`TargetResources`, `JobResource`). |
| `internal/manager/api` (`jobs.go`) | `/api/v1/jobs` routes, `Job` schema, SSE stream, `JobErrorFor`, `Accepted`. |

## Job model

Stored in SQLite (`jobs`, `job_targets`, `job_locks`, `job_events`,
`job_fencing`; migration `20260924120000_create_jobs`; `retry_of` and the
`jobs_policy (policy_id, id)` index from `20260928041737_job_retries_policy_index`):

- `kind`, `executor` (`agent`/`manager`), `origin` (`manual`, `scheduled`,
  `api_token`), initiator user and API-token IDs (**audit metadata only**,
  never an access-control owner), `policy_id` (the policy the job runs
  for: its scheduled runs and manual runs of it), `retry_of` (the job a
  retry re-runs, [below](#retries)), `environment_id`, targets
  (`stack`, `container`, `volume`, `image`, `network`, `repository`, `path`,
  `destination_path`, `template`; a target may name another environment for
  migrations).
- canonical JSON `input` and its `input_hash`, optional idempotency key.
- `attempt`, `state`, progress (percent/step/message), item-level results,
  error class, error message, **recovery guidance**, `blocked_by` and
  `blocked_reason`, planned lock set, `fencing_token`, cancel flag, the
  execution journal of manager-local jobs (current step, completed steps,
  compensations) and timestamps (created, updated, dispatched, started,
  finished).

### State machine

`domain.CanTransition` is the single table of legal moves; the engine's
`transition()` is the only code that changes a job's state (it also stamps
timestamps, writes a state event and releases locks on terminal states).

| From | To |
| --- | --- |
| `queued` | `blocked`, `dispatched`, `cancelled`, `failed` |
| `blocked` | `dispatched`, `cancelled`, `failed` |
| `dispatched` | `running`, `cancelling`, any terminal state |
| `running` | `dispatched` (resume as a new attempt), `cancelling`, any terminal state |
| `cancelling` | any terminal state |

Terminal states: `succeeded`, `failed`, `partial`, `cancelled`,
`interrupted`. Every non-successful terminal state carries an error class
and recovery guidance; no job silently disappears (only retention deletes
old finished jobs).

Stable error classes: `agent_offline`, `authorization_revoked`,
`step_failed`, `unknown_outcome`, `journal_lost`, `resume_limit`,
`rejected`, `compensation_failed`, `executor_restarted`,
`credential_unavailable`, `policy_rejected`, `target_moved`, `cancelled`,
`internal`. A step may fail with its own class instead of `step_failed` by
returning a `jobexec.ClassedError` (class plus recovery guidance), e.g. the
Engine and registry codes and refusals of the Docker resource kinds
(`rate_limited`, `unauthorized`, `stack_managed`, `volume_in_use`, ...;
[docker-resources.md](docker-resources.md#job-error-classes)).

### Idempotency

`Request.IdempotencyKey` is scoped to the initiating principal (user, API
token, or the service identity for scheduled jobs). The same key with the
same input hash (kind, environment, policy, sorted targets, canonical
input) returns the existing job — also after it finished; the same key with a
different input fails with `domain.ErrJobIdempotencyConflict`, which the API
maps to **409 `idempotency_key_reused`**.

## Resource locking

Scopes: `host` (the environment), `stack`, `container`, `volume`, `image`,
`network`, `file_path`, `repository` (instance-wide restic repository —
added so retention/verify/backup serialize correctly) and `template` (a
stack template's draft; instance-wide like `repository`). Modes: `shared` and
`exclusive`. Two locks of different jobs **conflict** when they overlap and
at least one is exclusive. They overlap when scope and environment match
and the names are equal, either name is `*` (all resources of the scope in
the environment, used by prune), or — for `file_path` — one path equals or
is an ancestor of the other (segment-wise prefix: `/a` covers `/a/b`, not
`/ab`).

- Each kind declares its lock rules (below); the lock set is computed from
  the job's targets at enqueue, deduplicated (exclusive wins), **sorted** by
  (scope, environment, name) and shown in the API while queued.
- At dispatch the engine acquires the **full set in one SQLite transaction**
  (all or nothing), persists it in `job_locks` and releases it only in the
  transaction that moves the job to a terminal state.
- Dispatch is FIFO per resource: a job that cannot start reserves its lock
  set for the rest of the pass, so a later job cannot overtake it on the same
  resources. A waiting job shows `blocked_by` (the job holding or queued
  first for the conflicting lock) with reason `lock`, `agent_offline` or
  `concurrency_limit`. Conflicts never fail a job; features that want reject
  semantics use an idempotency key or check before enqueueing.
- Per-environment concurrency caps apply to kinds with a concurrency class
  (`pull`: `DOCKER_MANAGER_JOB_MAX_CONCURRENT_PULLS`, default 2; `build`:
  `DOCKER_MANAGER_JOB_MAX_CONCURRENT_BUILDS`, default 1).
- Prune takes shared `*` locks and must revalidate each candidate right
  before deleting it. Its shared `*` stack lock (#14) serializes it with
  deploys, builds, updates, migrations, backup shutdowns and restores (their
  exclusive stack locks), and it waits for (and
  blocks) exclusive volume/image/network/container work such as a volume
  restore.

### Lock matrix

Generated from `internal/jobspec` by `scripts/generate.sh`
(`TestMatrixDocUpToDate` fails when stale; `TestEveryJobKindHasLockDefinition`
fails when a declared kind has no lock definition). Legend: **X** exclusive,
S shared; steps flagged `i` are idempotent, `c` are cancellation safe points
(cancellation is honored immediately before them).

<!-- BEGIN GENERATED: lock-matrix (scripts/generate.sh; do not edit) -->

| Kind | Executor | Capability | Locks | Steps | Offline deadline | Cap class | Compensations | Manager restart |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `backup.import` | manager | `backup.import` | `repository` S (repository targets) | `scan` (i,c) → `import_index` (i,c) | — | — | — | resume |
| `backup.retention` | agent | `backup.retention` | `host` S (each environment)<br>`repository` **X** (repository targets) | `forget` (i,c) → `prune_repository` (i,c) | 1h | — | — | — |
| `backup.run` | agent | `backup.run` | `host` S (each environment)<br>`stack` **X** (stack targets, optional)<br>`volume` S (volume targets, optional)<br>`file_path` S (path targets, optional)<br>`repository` S (repository targets) | `prepare` (i,c) → `stop_containers` (i,c) → `snapshot` (c) → `start_containers` (i) → `record` (i,c) | 1h | — | `start_containers` | — |
| `backup.verify` | agent | `backup.verify` | `host` S (each environment)<br>`repository` S (repository targets) | `check` (i,c) | 1h | — | — | — |
| `container.create` | agent | `container.create` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `create` (c) → `connect_networks` (i) → `start` (i) | 10m | — | — | — |
| `container.pause` | agent | `container.pause` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `pause` (i,c) | 10m | — | — | — |
| `container.remove` | agent | `container.remove` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `remove` (i,c) | 10m | — | — | — |
| `container.restart` | agent | `container.restart` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `restart` (i,c) | 10m | — | — | — |
| `container.start` | agent | `container.start` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `start` (i,c) | 10m | — | — | — |
| `container.stop` | agent | `container.stop` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `stop` (i,c) | 10m | — | — | — |
| `container.unpause` | agent | `container.unpause` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `unpause` (i,c) | 10m | — | — | — |
| `container.update` | agent | `container.update` | `host` S (each environment)<br>`container` **X** (container targets)<br>`stack` S (stack targets, optional) | `update` (i,c) | 10m | — | — | — |
| `environment.migrate` | manager | `stack.migrate` | `host` S (each environment) | `prepare` (i,c) → `create_networks` (i,c) → `migrate` (i,c) → `finalize` (i) | — | — | `start_group` | interrupt |
| `files.archive` | agent | `{stack,volume}.files.archive` | `host` S (each environment)<br>`file_path` S (path targets)<br>`file_path` **X** (destination_path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `archive` (i,c) | 10m | — | — | — |
| `files.copy` | agent | `{stack,volume}.files.copy` | `host` S (each environment)<br>`file_path` S (path targets)<br>`file_path` **X** (destination_path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `copy` (c) | 10m | — | — | — |
| `files.delete` | agent | `{stack,volume}.files.delete` | `host` S (each environment)<br>`file_path` **X** (path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `delete` (i,c) | 10m | — | — | — |
| `files.extract` | agent | `{stack,volume}.files.extract` | `host` S (each environment)<br>`file_path` S (path targets)<br>`file_path` **X** (destination_path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `extract` (c) | 10m | — | — | — |
| `files.metadata` | agent | `{stack,volume}.files.chmod`, `{stack,volume}.files.chown` | `host` S (each environment)<br>`file_path` **X** (path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `apply` (i,c) | 10m | — | — | — |
| `files.move` | agent | `{stack,volume}.files.move` | `host` S (each environment)<br>`file_path` **X** (path targets)<br>`file_path` **X** (destination_path targets)<br>`volume` S (volume targets, optional)<br>`stack` S (stack targets, optional) | `move` (c) | 10m | — | — | — |
| `image.build` | agent | `image.build` | `host` S (each environment)<br>`image` **X** (image targets) | `fetch_context` (i,c) → `build` (i,c) | 30m | build | — | — |
| `image.pull` | agent | `image.pull` | `host` S (each environment)<br>`image` **X** (image targets) | `pull` (i,c) | 30m | pull | — | — |
| `image.remove` | agent | `image.remove` | `host` S (each environment)<br>`image` **X** (image targets) | `remove` (i,c) | 10m | — | — | — |
| `manager.backup` | manager | `manager.backup` | `repository` **X** (repository targets) | `snapshot_database` (i,c) → `backup` (c) → `write_manifest` (i,c) | — | — | — | interrupt |
| `manager.move` | manager | `manager.move` | `manager` **X** (manager targets) | `migrate` (i,c) → `ready` (i,c) | — | — | — | interrupt |
| `manager.retention` | manager | `backup.retention` | `repository` **X** (repository targets) | `forget` (i,c) → `prune_repository` (i,c) | — | — | — | resume |
| `manager.verify` | manager | `backup.verify` | `repository` S (repository targets) | `check` (i,c) | — | — | — | resume |
| `network.create` | agent | `network.create` | `host` S (each environment)<br>`network` **X** (network targets) | `create` (c) | 10m | — | — | — |
| `network.remove` | agent | `network.remove` | `host` S (each environment)<br>`network` **X** (network targets) | `remove` (i,c) | 10m | — | — | — |
| `prune.run` | agent | `maintenance.run` | `host` S (each environment)<br>`stack` S (all (`*`))<br>`container` S (all (`*`))<br>`image` S (all (`*`))<br>`network` S (all (`*`))<br>`volume` S (all (`*`)) | `collect_candidates` (i,c) → `delete_candidates` (i,c) | 1h | — | — | — |
| `restore.run` | agent | `backup.restore` | `host` S (each environment)<br>`stack` **X** (stack targets, optional)<br>`container` **X** (container targets, optional)<br>`volume` **X** (volume targets, optional)<br>`file_path` **X** (destination_path targets, optional)<br>`repository` S (repository targets) | `prepare` (i,c) → `stop_containers` (i,c) → `restore_data` (c) → `start_containers` (i) | 1h | — | `start_containers` | — |
| `stack.build` | agent | `stack.build` | `host` S (each environment)<br>`stack` **X** (stack targets) | `fetch_sources` (i,c) → `build_images` (i,c) | 30m | build | — | — |
| `stack.deploy` | agent | `stack.deploy` | `host` S (each environment)<br>`stack` **X** (stack targets) | `resolve_sources` (i,c) → `pull_images` (i,c) → `build_images` (i,c) → `apply` (i,c) | 30m | — | — | — |
| `stack.down` | agent | `stack.down` | `host` S (each environment)<br>`stack` **X** (stack targets) | `down` (i,c) | 10m | — | — | — |
| `stack.import` | agent | `stack.import` | `host` S (each environment)<br>`stack` **X** (stack targets) | `prepare` (i,c) → `stop_containers` (i,c) → `copy_files` (i,c) → `recreate` (i,c) → `start_containers` (i) | 10m | — | `start_containers`, `remove_import_copy` | — |
| `stack.migrate` | manager | `stack.migrate` | `host` S (each environment)<br>`stack` **X** (stack targets)<br>`volume` **X** (volume targets, optional) | `prepare` (i,c) → `stop_source` (i,c) → `transfer` (i,c) → `deploy_destination` (i,c) → `finalize` (i) | — | — | `start_source` | interrupt |
| `stack.pull` | agent | `stack.update` | `host` S (each environment)<br>`stack` **X** (stack targets) | `pull_images` (i,c) | 30m | pull | — | — |
| `stack.remove` | agent | `stack.remove` | `host` S (each environment)<br>`stack` **X** (stack targets) | `down` (i,c) | 10m | — | — | — |
| `stack.remove_source` | agent | `stack.migrate` | `host` S (each environment)<br>`stack` **X** (stack targets)<br>`volume` **X** (volume targets, optional) | `down` (i,c) → `remove_volumes` (i,c) → `remove_files` (i) | 30m | — | — | — |
| `stack.rename` | agent | `stack.rename` | `host` S (each environment)<br>`stack` **X** (stack targets) | `prepare` (i,c) → `stop_containers` (i,c) → `move` (i,c) → `recreate` (i) → `start_containers` (i) | 10m | — | `start_containers`, `undo_rename` | — |
| `stack.restart` | agent | `stack.restart` | `host` S (each environment)<br>`stack` **X** (stack targets) | `restart` (i,c) | 10m | — | — | — |
| `stack.start` | agent | `stack.start` | `host` S (each environment)<br>`stack` **X** (stack targets) | `start` (i,c) | 10m | — | — | — |
| `stack.stop` | agent | `stack.stop` | `host` S (each environment)<br>`stack` **X** (stack targets) | `stop` (i,c) | 10m | — | — | — |
| `stack.update` | agent | `stack.update` | `host` S (each environment)<br>`stack` **X** (stack targets) | `pull_images` (i,c) → `apply` (i,c) | 30m | pull | — | — |
| `template.files.archive` | manager | `template.files.archive` | `template` **X** (template targets) | `archive` (i,c) | — | — | — | interrupt |
| `template.files.copy` | manager | `template.files.copy` | `template` **X** (template targets) | `copy` (c) | — | — | — | interrupt |
| `template.files.delete` | manager | `template.files.delete` | `template` **X** (template targets) | `delete` (i,c) | — | — | — | interrupt |
| `template.files.extract` | manager | `template.files.extract` | `template` **X** (template targets) | `extract` (c) | — | — | — | interrupt |
| `template.files.metadata` | manager | `template.files.chmod`, `template.files.chown` | `template` **X** (template targets) | `apply` (i,c) | — | — | — | interrupt |
| `template.files.move` | manager | `template.files.move` | `template` **X** (template targets) | `move` (c) | — | — | — | interrupt |
| `update.check` | manager | `update.check` | `host` S (each environment)<br>`stack` S (stack targets, optional)<br>`container` S (container targets, optional) | `check` (i,c) | — | — | — | resume |
| `update.run` | agent | `update.run` | `host` S (each environment)<br>`stack` **X** (stack targets, optional)<br>`container` **X** (container targets, optional) | `pull_images` (i,c) → `recreate` (i,c) → `wait_healthy` (i) | 1h | pull | — | — |
| `volume.create` | agent | `volume.create` | `host` S (each environment)<br>`volume` **X** (volume targets) | `create` (i,c) | 10m | — | — | — |
| `volume.migrate` | manager | `volume.migrate` | `host` S (each environment)<br>`volume` **X** (volume targets) | `prepare` (i,c) → `transfer` (i,c) → `finalize` (i) | — | — | — | interrupt |
| `volume.remove` | agent | `volume.remove` | `host` S (each environment)<br>`volume` **X** (volume targets) | `remove` (i,c) | 10m | — | — | — |

<!-- END GENERATED: lock-matrix -->

Executor assignments of manager-side kinds (`update.check`,
`backup.retention`, `backup.verify`, `backup.import`, `manager.backup`) are
provisional; the owning feature may move a kind between executors by
changing its spec (and regenerating this table) before it ships.
`stack.migrate` and `volume.migrate` are manager-executed (they relay data
between two agents, [migrations.md](migrations.md)); their volume targets
and the destination's targets only take locks (`Spec.LockOnly`: the engine
authorizes `stack.migrate`/`volume.migrate` on the source only, the
executor checks the destination's capabilities).

## Dispatch, fencing and agent recovery

- Dispatch allocates a token from the environment's persisted counter
  (`job_fencing`) in the acquisition transaction and sends a `command` frame
  with job ID, attempt, fencing token, deadline and `{kind, input,
  completedSteps, output}` through `AgentDispatcher.Send`. Commands of one
  environment leave in token order.
- The agent persists the highest token it accepted (journal high-water mark)
  and rejects any command whose token is not above it (`ack`
  `stale_fencing_token`), except an exact duplicate of a journaled command
  (acknowledged as `duplicate`, never re-run). A command replayed after a
  reconnect or from a superseded dispatch is therefore rejected — also after
  an agent restart. If the agent reports a high-water mark above the
  manager's counter (manager database restored), the counter moves past it.
- The agent journals (fsync, atomic rename) the command **before** acking,
  each step as in-flight **before** running it and as completed after, and
  every compensation before causing the effect it undoes. Results stay in
  the journal until the manager acknowledges them (`ack.forget`).
- **Reconnect** (`job_report` → `HandleAgentFrame`): the manager reconciles
  instead of re-running:

  | Agent report | Manager action |
  | --- | --- |
  | finished `succeeded`/`failed`/`partial`/`cancelled` | apply the outcome, tell the agent to forget it |
  | finished `interrupted`, resumable (the in-flight step, if any, is idempotent and no compensation ran) | new attempt with a new fencing token, skipping completed steps and continuing from the output the interrupted attempt reported (the command's `output`; later steps read what the completed ones recorded), bounded by `MaxResumes` (default 3); cancelled instead if cancellation was requested |
  | finished `interrupted`, not resumable (non-idempotent step with unknown outcome) | `interrupted` with the step's recovery guidance — **never retried automatically** |
  | running | keep running (re-send a pending cancellation); the agent serializes the report with its "attempt finished" transition, so the attempt's `result` is always sent **after** this report (never before it and lost with the old session) |
  | job missing, never acknowledged | the command never arrived: new attempt with a new token (or `cancelled` if cancellation was requested) |
  | job missing, acknowledged | `interrupted` / `journal_lost` with recovery guidance |
  | entry for an unknown, finished or superseded job | forget it (a differing late outcome is recorded as a warning event) |

- **Offline agent:** a queued agent job waits (`blocked`/`agent_offline`) up
  to its kind's offline deadline, then fails with `agent_offline`; nothing
  was changed. A dispatched command never acknowledged before the agent went
  offline also fails after the deadline. Running jobs wait for the agent's
  report (the agent keeps executing and journaling while disconnected).
- **Manager restart:** `Engine.Recover` runs at startup (after migrations,
  before the listener). Agent jobs keep their state and locks and are
  reconciled on reconnect. Manager-local jobs run their compensations and
  then resume (kinds with `resume` policy whose in-flight step is idempotent;
  the output the attempt journaled with the job row carries over)
  or become `interrupted` (`interrupt` policy or non-idempotent step).
- **Cancellation** (`POST /jobs/{id}/cancellations`): waiting jobs are
  cancelled immediately; active jobs move to `cancelling` and stop only at
  the kind's next safe point. Unreleased compensations (e.g. restart the
  containers stopped for a backup) always run when a job does not succeed —
  on failure, cancellation and crash recovery. A job that finishes before
  reaching a safe point keeps its outcome. A step whose work may be
  interrupted safely stops mid-way instead: it runs that work under
  `StepContext.WatchCancel` (a context that ends within
  `jobexec.DefaultCancelPoll`, 1 s, of the request) and returns
  `jobexec.ErrStepCancelled`, which ends the attempt `cancelled` (image
  builds, backup snapshots, retention prunes).

## Authorization

`internal/manager/authz` provides `Authorizer.Can(ctx, principal,
capability, resource)`; the manager's Authorizer is the #17 permission
service (`DenyAll` when none is configured), so every job route fails
closed: 401 without a principal, 404 for jobs the caller may not read
(existence does not leak), 403 when reading is allowed but cancelling is
not. Manual and API-token jobs are authorized at enqueue against the kind's
capabilities (`jobspec.Spec.Capabilities`: the kind's key; per-root file
keys such as `stack.files.copy`; `files.metadata` selects
`<root>.files.chmod` / `.chown` from its input keys) on **every** target
(`authz.TargetResources`: file paths are covered by their stack or volume
root) and **rechecked at dispatch** while still queued (lost grant →
`failed`/`authorization_revoked`). Scheduled jobs run as the manager service
identity (`authz.Service()`, never derivable from a request); at dispatch
the scheduler's `ScheduledCheck` (`Engine.SetScheduledCheck`, #13)
revalidates their policy (disabled/deleted policy or vanished target →
`failed`/`policy_rejected`, nothing sent; see [scheduler](scheduler.md)).
Every job's stack targets must still be in the environment it was queued
against when it is dispatched: a stack that moved meanwhile (#35 migration
cut-over, while the migration held the stack lock) fails the job with
`target_moved`, nothing sent; kinds that act on a stack's former
environment set `jobspec.Spec.FormerStackLocation` (`stack.remove_source`).
Running jobs
finish or recover after the initiator loses access. Job visibility and
cancellation are checked with `job.read` / `job.cancel` against
`authz.JobResource(job)`: allowed when the caller holds the capability — or
all of the kind's own capabilities — on every target, per item in lists;
never by initiator ([authorization](authorization.md)).

## API

- `GET /api/v1/jobs` — cursor pagination (`cursor`, `limit`), filters
  `state` (repeatable or comma-separated), `kind` (one kind or a
  comma-separated list of at most 32), `origin` (repeatable),
  `environmentId`, `target=type:id` with optional `targetEnvironmentId`
  (the target's environment: its own, else the job's; container, volume
  and network names repeat across environments), `policyId` (index
  `jobs_policy`); permission-filtered per item. The web UI restores every
  progress bar from `?state=<active states>&limit=200`
  ([web.md](../web.md#job-progress-after-reload)). `total` counts the
  visible matches and is sent only when exact: one COUNT query
  (`Engine.Count`, the list's filters) for a caller who reads every job
  (the owner's session, detected with an owner-only capability),
  otherwise a `job.read` check of each of at most 1000 matching jobs;
  absent above that.
- `GET /api/v1/jobs/{jobId}`.
- `POST /api/v1/jobs/{jobId}/cancellations` — 202 with the job; 409
  `job_finished`.
- `POST /api/v1/jobs/{jobId}/retries` — 202 with the new job
  ([retries](#retries)); `Idempotency-Key` (job mode); 403 without the
  kind's capabilities, 409 `job_not_retryable`.
- `GET /api/v1/jobs/{jobId}/events/stream` — SSE: `event: job` snapshot,
  then events with `id: <seq>` after `Last-Event-ID`, `: heartbeat` every
  15 s, `X-Accel-Buffering: no`, closes after the terminal events.

The `Job` schema includes `origin`, `attempt`, `blockedBy`, `locks`,
`locksHeld`, `progress`, `items`, `error {class, message, recovery}`,
`retryOf` and `retryable` (the caller may retry it now; false in the 202
answers of job-starting operations).

## Retries

`Engine.Retry(ctx, principal, jobID, key)` runs a finished job that did not
succeed (`failed`, `partial`, `interrupted`, `cancelled`) again as a **new
job** with the same kind, environment, policy, targets and input and
`retry_of` set; the original never changes. It is an ordinary request of
the caller: the kind's capabilities are authorized on every target before
anything else (and again by `Enqueue`), archived environments refuse it
and the engine records `job.queued` (with `retryOfJobId`); the API adds
`job.read` visibility (404) and records the request as `job.retry`
(targets: both jobs). `jobspec.RetryRefusal` refuses (a
`domain.JobNotRetryableError`, API 409 `job_not_retryable`) jobs that are
still active (`active`), succeeded (`succeeded`), whose kind is not
`jobspec.Spec.Retryable` (`kind`) or whose input was not kept (`no_input`).

Only kinds whose stored input is still right to act on later are
retryable — no secret in it (credentials are IDs resolved at dispatch),
no plan or snapshot that may have gone stale, no feature record created
next to the job: `image.pull`, `stack.deploy`, `stack.pull` and
`update.check` (`TestRetryableKinds`). Not retryable: `update.run` (a
plan of digests checked against drift at enqueue), `prune.run` (a copy
of the policy's rules, destructive), `backup.run` and the other backup
kinds (backup sets have their own retry, `RetrySetID`), `image.build`
(an image build record per job; build arguments in the input), and every
other kind. A feature may take part with `Engine.OnRetry(kind,
jobs.Retrier{Input, Queued})`: `Input` rebuilds the input from its
current state (a `domain.RetryRefusedUnavailable` refusal when the
resource is gone), `Queued` runs after the retry was queued. The stacks
service refreshes a retried deploy's or pull's stack reference (directory,
project name, Compose files) and registry connections, keeps its options
and makes the retry the stack's last job.

## Audit

Every kind emits audit records (#30) from the engine itself, in the same
transaction as the state change: `job.queued` (Enqueue), `job.started`
(each attempt), `job.cancel_requested` (Cancel) and `job.finished` (every
terminal state, with the error class and the item list). Executors do not
record lifecycle events. A retry's `job.queued` carries `retryOfJobId`;
the retry request itself is recorded as `job.retry`. See [audit](audit.md).

## Retention

Finished jobs and their events are deleted after
`DOCKER_MANAGER_JOB_HISTORY_RETENTION` (default `720h`) and beyond the newest
`DOCKER_MANAGER_JOB_HISTORY_MAX` (default 10000) finished jobs; each job keeps its
newest `DOCKER_MANAGER_JOB_EVENTS_MAX` (default 500) events. Unfinished jobs are
never deleted. This is independent of audit retention (#30).

## Crash recovery tests

Crash recovery is covered by unit tests of the journal replay and recovery
paths, not by killing processes: `TestManagerRestartRecovery`
(`internal/manager/jobs`), `TestAgentCrashMidStepEndToEnd` and
`TestResumedAttemptLostAgainIsResentNotLost` (reconciliation after an agent
loses an attempt), `TestRestartRecoversInFlightAttempt`
(`internal/agent/jobs`), and per-kind tests such as
`TestBackupRunRecoversAfterAgentCrash` (`internal/agent/backups`). A resumed
attempt gets the output of its completed steps back
(`Job.ResumeOutput`, persisted), so later steps can read it from
`sc.Output()`.

The former fault-injection harness (named fault points in production code,
`-tags faultinject`, and `internal/manager/jobs/faulttest`, which killed the
manager and an agent at every stage of simulated and real `stack.deploy`,
`backup.run` and `prune.run` jobs) was removed on 2026-09-25: recovery
from a process killed mid-job is **not verified by automated tests** any
more.

## For feature workstreams

**Define a kind.** Add a `domain.JobKind` constant and a `Spec` in
`internal/jobspec/catalog.go` (or adjust the provisional one): capability,
executor, lock rules from targets, offline deadline, steps with
`Idempotent`/`SafePoint`/`Recovery`, compensations. Mark it `Retryable`
only when its stored input is safe to run again later ([retries](#retries);
update `TestRetryableKinds`). Run `bash scripts/generate.sh`.

**Enqueue** from an API handler and answer 202:

```go
job, _, err := engine.Enqueue(ctx, jobs.Request{
	Kind:           jobspec.StackDeploy,
	Principal:      principal,          // authz.Service() for scheduled jobs
	EnvironmentID:  envID,
	Targets:        []domain.JobTarget{{Type: domain.TargetStack, ID: stackID}},
	Input:          deployInput,        // JSON object
	IdempotencyKey: in.IdempotencyKey,  // api.IdempotencyKeyParam
})
if err != nil {
	return nil, api.JobErrorFor(err)
}
return api.Accepted(job), nil // register with DefaultStatus: http.StatusAccepted
```

**Implement an agent executor** (`internal/agent/...`, wired into
`agentjobs.Options.Executors`):

```go
jobexec.Executor{
	Kind: jobspec.BackupRun,
	Steps: map[string]jobexec.StepFunc{
		"stop_containers": func(ctx context.Context, sc *jobexec.StepContext) error {
			// register BEFORE causing the effect
			if err := sc.AddCompensation(ctx, jobspec.CompStartContainers, ids); err != nil {
				return err
			}
			sc.Progress(ctx, 20, "stopping containers")
			return engine.Stop(ctx, ids)
		},
		// ... one func per declared step; sc.Item(ctx, name, status, msg) for item results
	},
	Compensations: map[string]jobexec.CompensationFunc{
		jobspec.CompStartContainers: startContainers, // must be idempotent
	},
}
```

**Result output.** A step may record result data with
`sc.SetOutput(ctx, v)` (a JSON object of at most 128 KiB, journaled before
it returns, `sc.Output()` reads it back). It is sent with **every** outcome
in `result.output` (and in `job_report` entries, at most 512 KiB of outputs
per report) — e.g. `stack.deploy` reports the exact definition bytes it
deployed and the Engine state before and after (#7).

**React to outcomes** on the manager with a finish hook, registered before
`Run`:

```go
engine.OnFinish(jobspec.StackDeploy, func(ctx context.Context, db bun.IDB, j domain.Job) error {
	// j.State is terminal; j.ResultOutput is the executor's output (nil
	// when the job ended without one, e.g. cancelled while queued).
	// Write with db: it is the transaction that finishes the job.
})
```

Hooks run inside `transition()` for every terminal state; a hook error
aborts the transaction (the agent's result is delivered again later), so
hooks must tolerate malformed output (log and record what they can).
Alerts (#159, [alerts](alerts.md)) register a hook on **every** kind
(`jobspec.Kinds()`: failed scheduled and API token jobs raise, a
succeeded job resolves its key) and one on `update.check`; they write in
a savepoint of the job's transaction and never fail the job, and announce
their changes, read back from the database, once `OnChange` reports the
commit.

Manager-local kinds register the same structure with
`engine.RegisterManagerExecutor` before `Recover`. Steps must honor `ctx`;
cancellation takes effect only at declared safe points.

**Credentials (#19, #33)**: job inputs name registry connections / Git
credentials by ID (`jobspec.CredentialRefs`); `Options.CommandSecrets`
resolves them into the command's `secrets` at every dispatch (never stored
with the job), and the agent keeps them in `StepContext.Secrets` for the
running attempt only (`jobexec.State.Secrets` is never serialized). An
unavailable credential fails the job with `credential_unavailable` before
anything is sent. See [registries](registries.md).

**Per-agent input (#10)**: `Options.CommandInput` may return the input a
command carries instead of the stored one, at every dispatch, for the
agent receiving it (an optional field only agents announcing its feature
accept, filled with the current setting: a backup repository's
compression mode, [backups](backups.md#secrets-and-restic)). nil sends the
stored input; the stored job never changes.

**Transport (#3)**: `internal/manager/agents.Hub` implements
`jobs.AgentDispatcher` (ordered `Send` through one writer per session,
`Online`), feeds `job_report`/`ack`/`progress`/`result` frames to
`Engine.HandleAgentFrame` and returns its replies on the session, and
reports the environment online only after its `job_report` was reconciled
(and the registered reconcilers ran), then wakes the engine. On the agent,
`internal/agent/session` sends the job report built from
`agentjobs.Runner.Report` on every (re)connect (ordered against result
sends) and passes inbound command/cancel/ack frames to `Runner.HandleFrame`;
the client is the runner's `Sender`.
