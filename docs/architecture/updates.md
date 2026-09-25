# Digest-driven updates (#20)

An opted-in DockYard stack (all of its services, or a list, minus
exclusions) or DockYard-managed standalone container follows the digest
behind its **existing explicit tag**. The tag text never changes, and no
Compose, override or env file is ever written: checks and updates are
runtime operations on the applied definition. There is no automatic
rollback in v1.

| Package | Role |
| --- | --- |
| `internal/manager/updates` | Policies (CRUD, validation, target checks), the digest model (candidates), the `update.check` manager executor, previews, runs, quarantine and history (finish hooks), the scheduler sources, the policy Locator. |
| `internal/manager/updates/eligible` | Eligibility rules shared with the stack image status (#7); corpus `testdata/corpus.yaml`. |
| `internal/manager/store` (`updates.go`) | `update_policies`, `update_candidates`, `update_quarantine`, `update_history` (migration `20260926001853_create_update_policies`). |
| `internal/manager/api` (`updates.go`) | `/api/v1/update-policies...` and `GET /environments/{id}/containers/{id}/image-status`; `GET /stacks/{id}/image-status` shows the policy's state per service. |
| `internal/agent/stacks` (`update.go`) | The `update.run` executor (stacks and standalone containers). |
| `internal/agent/lifecycle` (`update.go`) | `Update` (stop / recreate / start preserving the prior running state) and `Confirm` (health confirmation). |
| `internal/agent/compose` | `Adapter.Create`: the Compose SDK's convergence without starting (recreates what diverged, e.g. a changed image ID). |
| `internal/protocol` (`updates.go`) | `UpdateRunInput`/`UpdateRunOutput`, stages, outcomes and error classes. |

## Policies

One policy per target (`409 update_policy_target_used`):

- **stack**: every service, or `services` (opt-in list), minus
  `excludeServices`. DockYard's own Compose project is refused (#32).
- **container**: a DockYard-managed standalone container with a saved
  recreate specification (#6 `ManagedSpec`). Unmanaged containers, stack
  members and DockYard's own containers are refused
  (`409 update_target_ineligible`): they are never recreated automatically.

Each policy has a **check schedule** (`update_check`, default `0 3 * * *`)
and a **run schedule** (`update_run`, default `0 4 * * *`) from the #13
instance defaults, each with its own expression, IANA zone and enabled
flag. **Both start disabled**: nothing is checked or updated automatically
before the user enables them. An optional **update window** (days of week,
`HH:MM`-`HH:MM` in the run schedule's zone, may span midnight) restricts
scheduled runs; the run source refuses runs outside it when due and again
at dispatch (`outside_update_window`). Manual checks and runs are explicit
user actions and ignore schedules and the window.

## Eligibility (`eligible.Check`)

Never inferred from tag text or image creation time:

| Reason | When |
| --- | --- |
| `build_only` | the service has a build section (#33) |
| `digest_pinned` | the reference names `@sha256:` (immutable; the user changes their own definition to follow a tag) |
| `untagged` | no explicit tag (`nginx` implies `latest`) |
| `pull_policy_conflict` | Compose `pull_policy` other than `missing`/`if_not_present`: `never`/`build` keep the image local; `always` and periodic policies pull on their own at every up and would bypass the checked candidate and its quarantine |
| `invalid_reference` | unparsable reference |
| `excluded` | not opted in by the policy |
| `not_deployed` / `no_applied_digest` | DockYard never deployed the service, or its image has no registry digest (built or loaded locally) |
| `protected` / `stack_managed` / `no_recreate_spec` | container targets (see above) |

Explicit tags are eligible **including `latest` and branch tags** (#25);
`nonVersionTag` flags tags that do not read like a version so the UI warns
that they can change meaning. Variable-interpolated references use the
reference resolved at deployment (the stack's applied images); a changed
Compose/env source is a separate stack revision whose deploy refreshes the
baseline (the deploy's finish hook marks the candidates `unchecked`).

## Digest model (`update_candidates`)

Per service (or the container): the resolved reference and its
registry/repository/tag, the host platform checked (from the applied image,
`os/arch[/variant]`), the registry connection used, the **current** digest
(applied on the host, the #7 baseline or the container image's repository
digest), the **previous** digest (before the last update), the
**candidate** platform manifest digest and the tag's index digest, status,
check time and job, errors with retry guidance, and the stack definition's
hash read before and after the check.

Statuses: `ineligible`, `unchecked`, `up_to_date`, `update_available`,
`quarantined`, `check_failed`, `run_failed`.

## Check (`update.check`, manager executor)

`registries.Service.Check` (#19: connection matching, credentials in
memory, cached one minute, request sharing, 401/403/429 classification,
Retry-After cooldown, no anonymous fallback) resolves the tag's
host-platform manifest digest. The comparison is on the **host-platform
manifest**:

- the platform digest (or the index digest, which the Engine may have
  recorded) equals an applied digest → `up_to_date`;
- otherwise, when the applied digest is an older index whose platform
  manifest (resolved `repo@<applied>`) equals the new platform digest →
  `up_to_date`: **an index change never triggers an update**;
- otherwise `update_available` (or `quarantined` when that digest failed
  before).

A registry failure (`unauthorized`, `forbidden`, `rate_limited`,
`registry_unavailable`, `not_found`, `platform_not_found`,
`ambiguous_registry_connection`, `registry_connection_revoked`) is stored
on the candidate (`check_failed`) with its message and `retryAfterSeconds`;
nothing is pulled. A check never pulls. For stacks the definition is read
(`compose.read`) before and after; a change in between fails the check
with `source_changed` (DockYard never writes it).

## Preview and run

`POST .../previews` (nothing changes) lists the services/containers the run
would recreate (current and candidate digests, running state, expected
downtime), dependents restarted with them (`depends_on` `restart: true`),
the stack's dependency graph, **other consumers of the same tags on the
environment** (other stacks' services, excluded services, standalone
containers: the pull moves the tag for them too; they keep their container
until their next recreate — accepted v1 behaviour), the applied source hash
and **source drift** (undeployed changes), whether now is inside the
window, and a fingerprint. `POST .../runs` takes an `Idempotency-Key`,
optional `candidates` (IDs or service names) and the preview's
`previewFingerprint` (`409 update_preview_stale` when anything changed
since). Runs are refused with `409 update_source_drift` while the
definition on disk differs from the applied revision (an update never
deploys an edit) and `409 no_update_candidates` when nothing is
`update_available`.

Only candidates whose **last check succeeded** are applied: after a failed
check or a failed pull (`run_failed`) a new successful check is needed, so
401/403/429 never lead to repeated pulls.

`update.run` (agent executor; locks: host shared, the stack or container
exclusive; pull concurrency class) steps:

1. `pull_images`: for stacks, the definition's hash must equal the applied
   revision's (`source_changed` otherwise, before anything is pulled); the
   services' state and image IDs are journaled; each **unchanged tagged
   reference** is pulled with the connection's credential
   (`regauth.ForReference`, required when the input names the connection:
   never anonymous); the pulled image's repository digests must contain the
   candidate (platform or index digest) — a tag that moved again fails with
   `candidate_changed` without recreating anything; the hash is read again.
2. `recreate`: the project is loaded **from exactly the applied bytes**
   (snapshot, load, re-read); services whose pulled image ID equals the
   running one are `unchanged` (no recreate). `lifecycle.Update` then:
   keeps changed services that were stopped **stopped** (`kept_stopped`,
   they pick up the image at their next deploy); refuses before touching
   anything when a required dependency is stopped and its condition does
   not already hold (`dependency_conflict`); stops the changed running
   services and their running `restart: true` dependents (dependents
   first); recreates the changed services with the Compose SDK's create
   (`Adapter.Create`: diverged containers get new ones, anonymous volumes
   inherited, dependencies never recreated); starts the set dependencies
   first, waiting for `service_started`/`service_healthy`/
   `service_completed_successfully` (optional dependencies only warn),
   bounded by the policy's wait timeout.
3. `wait_healthy`: every recreated or restarted service must run and be
   healthy when it has a health check (`unhealthy`, `service_exited`,
   `timeout`); the definition's hash is read a third time.

Standalone containers: the old container is stopped and renamed aside, the
new one created from the **saved specification with the unchanged
reference** (anonymous volumes carried over, extra networks connected) and
started when the old one ran; the old one is removed once the replacement
exists. A failed create puts the old container back. The agent refuses
DockYard's own containers (`protected`) and containers replaced since the
plan (`container_recreated`).

**No automatic rollback.** When a failure happens after containers were
touched, the output sets `quarantine`: the manager's finish hook records
the candidate digest in `update_quarantine` (audited as
`update.quarantine`), marks the candidate `quarantined` and the job's
recovery text explains manual recovery: pin the previous digest
(`image: repo@sha256:<previous>`) in the user's own definition and deploy
it. The previous image stays on the host until an enabled prune policy
removes it (#14). A quarantined digest is never applied automatically; a
newer digest of the tag is a new candidate.

On success the finish hook records history (`update_history`: from/to
digests and image IDs, registry connection, source hashes, job), moves the
current digest to previous, and updates the stack's applied images
(`stacks.Service.RecordUpdatedImages`, the new baseline; the applied
revision is unchanged).

## Scheduling (#13)

`Service.Register(sched)` installs `CheckSource` (`update_check`) and
`RunSource` (`update_run`). Validate refuses deleted policies, disabled
schedules, vanished stacks and (runs) the window; `Jobs` plans from the
latest check (nothing to do skips the run, drift refuses it). Scheduled
jobs run as the manager service identity; manual ones carry the policy ID
for overlap prevention.

## Authorization (#17)

| Capability | Opens |
| --- | --- |
| `update_policy.read` | the policy in full, its candidates |
| `update_policy.manage` | create (in the environment), edit, delete |
| `update.check` | checks and previews |
| `update.run` | runs |

Policies are located in their environment below their stack (or
container). `update.check` and `update.run` are granted instance-wide, per
environment, stack or container — the jobs' targets, on which the job
engine checks them again — and a stack or container grant also covers its
policy (shown minimally). Creating a
policy also requires seeing its target. Other capabilities show id, name,
environment and target.

## Tests

Docker-free: `internal/manager/updates/eligible` (corpus),
`internal/manager/updates` (in-process manager with the fake OCI registry
and the real agent executor over real files: digest follows the tag,
unchanged digest is a no-op, index-vs-platform, quarantine and no retry
loop, 401/429 never pull, refused pulls need a new check, source drift,
nothing before enabling, windows, standalone containers, ineligible
reasons), `internal/agent/lifecycle` (`TestUpdate*`, `TestConfirm`:
dependency order, health, completion, optional dependencies, restart
propagation, stopped services), `internal/agent/stacks` (`TestUpdate*`:
source bytes and mtimes identical through successful and failed updates,
tag moved again, index digest, credentials, standalone recreate),
`internal/manager/api` (`TestUpdatePolicy*`, image status).

Integration (`-tags integration`, compose-fixtures job):
`TestComposeDigestUpdateFromPrivateRegistry` (registry fixture, one DinD
Engine) and `TestRegistryAutomaticUpdateOnTwoAgents` (scheduled check and
run on two Engines with a manager-owned connection, #19).
