# Digest-driven updates (#20, #240)

One instance-wide updates setup covers every Docker Manager-managed stack
and standalone container of every environment, minus what it leaves out.
Covered targets follow the digest
behind its **existing explicit tag**. The tag text never changes, and no
Compose, override or env file is ever written: checks and updates are
runtime operations on the applied definition. There is no automatic
rollback in v1.

| Package | Role |
| --- | --- |
| `internal/manager/updates` | The setup (`setup.go`: validation, target records, setup-wide checks, previews and runs), the digest model (candidates), the `update.check` manager executor, previews, runs, quarantine and history (finish hooks), the scheduler sources, the record Locator. |
| `internal/manager/updates/eligible` | Eligibility rules shared with the stack image status (#7); corpus `testdata/corpus.yaml`. |
| `internal/manager/store` (`updates.go`, `update_settings.go`) | The setup (`update_settings`, one row), target records, candidates, quarantine and history. |
| `internal/manager/api` (`update_settings.go`, `updates.go`) | `/api/v1/update-settings...`; target records are read-only through `/api/v1/update-policies...`. Image status is shown for containers and stack services. |
| `internal/agent/stacks` (`update.go`) | The `update.run` executor (stacks and standalone containers). |
| `internal/agent/lifecycle` (`update.go`) | `Update` (stop / recreate / start preserving the prior running state) and `Confirm` (health confirmation). |
| `internal/agent/compose` | `Adapter.Create`: the Compose SDK's convergence without starting (recreates what diverged, e.g. a changed image ID). |
| `internal/protocol` (`updates.go`) | `UpdateRunInput`/`UpdateRunOutput`, stages, outcomes and error classes. |

## The setup

There is exactly one setup (`update_settings`, `singleton = 1`, created by
migration `20261002180000_update_settings`, which turned the earlier
environment policies into it). Its `id` is the policy ID of its schedules
and setup-wide jobs and the `parent_id` of every target record. It covers
every active environment (offline ones too) except `excludeEnvironments`,
also environments added later; it leaves out the stacks in
`excludeStacks` and the standalone containers in `excludeContainers`
(`environmentID/containerName`). A container with
`docker-manager.update.exclude=true` is also omitted. IDs of environments
that no longer exist are dropped when the setup is saved. The target
records are created and refreshed as stacks and containers are
discovered; users configure the setup, not those records.

The migration kept an all-environments policy as it was; otherwise the
only policy of one environment, with every other current environment left
out and its container exclusions prefixed with its environment; otherwise
(none, or several) a setup with both schedules off. It re-parented every
target record to the setup (their candidates, quarantine and history
stay) and deleted the `update_policy.manage` rules below the instance.

**Target records** (`update_policies` rows with a `parent_id`) are
reconciled (`Service.reconcile`) whenever the setup's targets are listed,
checked, previewed or run (manual or scheduled):

- A record is **named after its target** in plain words: "Automatic updates
  for zerobyte" (the stack's display name, else its Compose project name,
  or the container's name). The name follows renames at the next
  reconciliation. Names are unique per environment: a second target with
  the same name gets "... (stack)" / "... (container)", then
  "... (stack 2)". Names never contain IDs; records named
  "Automatic update <id>" by earlier versions are renamed by the same
  reconciliation (no migration: only it knows the targets' current names
  and keeps them in sync afterwards). A deleted stack's record keeps its
  last name ("Automatic updates for a removed stack" when it only had an
  ID-based one).
- A target the setup no longer covers keeps its record, **inactive**, for
  its history. `GET /update-settings/targets` says why in
  `inactiveReason`: `excluded` (the setup leaves it or its environment
  out, or the container's label) or `missing` (the stack or container no
  longer exists, or no longer qualifies: no saved specification, Docker
  Manager's own). `GET /update-policies` (and so update badges, counts and
  notices) lists only covered records; inactive ones stay readable by ID.
  Its `targetName` is the target's name as users know it, its `parentId`
  the setup. A record has no page of its own: alerts and notifications
  link the Updates page.

Covered targets:

- **stack**: every service of every covered managed stack. Docker Manager's own
  Compose project is refused (#32).
- **container**: a Docker Manager-managed standalone container with a saved
  recreate specification (#6 `ManagedSpec`). Unmanaged containers, stack
  members and Docker Manager's own containers are refused
  (`409 update_target_ineligible`): they are never recreated automatically.

The setup has a **check schedule** (Check Automatically, `update_check`,
default `0 3 * * *`) and a **run schedule** (Update Automatically,
`update_run`, default `0 4 * * *`), each with its own expression, IANA
zone and enabled flag. **Both start disabled**: nothing is checked or updated automatically
before the user enables them. An optional **update window** (days of week,
`HH:MM`-`HH:MM` in the run schedule's zone, may span midnight) restricts
scheduled runs; the run source refuses runs outside it when due and again
at dispatch (`outside_update_window`). Manual checks and runs are explicit
user actions and ignore schedules and the window.

**Migrated stacks (#35).** A moved stack stays covered in its destination
environment (unless the setup leaves that environment out); its record
moves with it. A fresh check establishes the destination's image baseline
before any update can run.

**Setup-wide actions.** `POST /update-settings/checks` enqueues one
`update.check` per covered target, `POST /update-settings/previews`
returns every target's plan with one fingerprint, and
`POST /update-settings/runs {fingerprint}` enqueues an `update.run` per
target with something to apply (409 `update_preview_stale` when the plan
changed since the preview). Every request is built before the first is
enqueued; when an enqueue fails, the jobs already queued are cancelled.

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
| `not_deployed` / `no_applied_digest` | Docker Manager never deployed the service, or its image has no registry digest (built or loaded locally) |
| `protected` / `stack_managed` / `no_recreate_spec` | container targets (see above) |

Explicit tags are eligible **including `latest` and branch tags** (#25);
`nonVersionTag` flags tags that do not read like a version so the UI warns
that they can change meaning. Variable-interpolated references use the
reference resolved at deployment (the stack's applied images); a changed
Compose/env source is a separate stack revision whose deploy refreshes the
baseline (the deploy's finish hook marks the candidates `unchecked`).
After every succeeded (or partial) `update.check` the alerts service's
finish hook raises, updates or resolves the policy's `updates` alert
from these candidates (sent again only for a new digest; resolved when
none is left), and every finished `update.run` records an `updates`
notification with what it updated ([alerts](alerts.md)); both name and
link the Updates page and name the setup ("Automatic Updates"). A failed scheduled or API
token `update.check` is sent under the `updates` kind.

## Digest model (`update_candidates`)

Per service (or the container): the resolved reference and its
registry/repository/tag, the host platform checked (from the applied image,
`os/arch[/variant]`), the registry connection used, the **current** digest
(applied on the host, the #7 baseline or the container image's repository
digest), the **previous** digest (before the last update), the
**candidate** platform manifest digest and the tag's index digest, the
candidate image's creation time (`publishedAt`, display only), status,
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

A new candidate digest gets its image's creation time
(`registries.Service.Created`: the image config's `created`, one manifest
GET by digest and one blob GET, cached per digest) as
`candidate_published_at`; it is kept while the candidate digest stays the
same, so each new digest is read once. It is shown next to the new version
and never decides anything (the comparison stays on digests). An absent
time (no `created`, the Unix epoch of reproducible builds, a registry
error) leaves it empty and never fails the check.

A registry failure (`unauthorized`, `forbidden`, `rate_limited`,
`registry_unavailable`, `not_found`, `platform_not_found`,
`ambiguous_registry_connection`, `registry_connection_revoked`) is stored
on the candidate (`check_failed`) with its message and `retryAfterSeconds`;
nothing is pulled. A check never pulls. For stacks the definition is read
(`compose.read`) before and after; a change in between fails the check
with `source_changed` (Docker Manager never writes it).

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
Docker Manager's own containers (`protected`) and containers replaced since the
plan (`container_recreated`).

**No automatic rollback.** When a failure happens after containers were
touched, the output sets `quarantine`: the manager's finish hook records
the candidate digest in `update_quarantine` (audited as
`update.quarantine`), marks the candidate `quarantined` and the job's
recovery text explains manual recovery: pin the previous digest
(`image: repo@sha256:<previous>`) in the user's own definition and deploy
it. The previous image stays on the host until maintenance
removes it (#14). A quarantined digest is never applied automatically; a
newer digest of the tag is a new candidate.

On success the finish hook records history (`update_history`: from/to
digests and image IDs, registry connection, source hashes, job), moves the
current digest to previous, and updates the stack's applied images
(`stacks.Service.RecordUpdatedImages`, the new baseline; the applied
revision is unchanged).

## Scheduling (#13)

`Service.Register(sched)` installs `CheckSource` (`update_check`) and
`RunSource` (`update_run`); each lists the setup's schedule ("Automatic
Updates", no environment). Validate refuses a disabled schedule and (runs)
the window; `Jobs` reconciles the targets and plans from the latest check
(nothing to do skips the run, drift refuses it). One run may enqueue up to
`scheduler.MaxJobsPerRun` (256) jobs. Scheduled jobs run as the manager
service identity; manual ones carry the setup's ID for overlap
prevention. A record created by hand (no setup; tests) has schedules of
its own.

## Authorization (#17)

| Capability | Opens |
| --- | --- |
| `update_policy.read` | the setup (on all environments); a target record in full, its candidates |
| `update_policy.manage` | changes of the setup (instance-only) |
| `update.check` | checks and previews |
| `update.run` | runs |
| `stack.update` | `POST /stacks/{id}/pulls`: a `stack.pull` job pulls the stack's images on demand without recreating anything (agents announcing `stack.pull`; 501 `agent_unsupported` otherwise). Its finish hook (`stacks.Service`) marks each applied image whose reference now names another image ID (`StackImage.PulledImageID`, shown by `image-status` as `pulledImageId`) until the next deploy; nothing else about the stack changes. |

The setup's routes need their capability on all environments (an
instance grant), and its checks, previews and runs are refused (403,
naming the environment) while a rule denies the capability in one of the
covered environments. Target records accept grants on an environment, a
stack or a container. Target jobs carry the setup's ID and are authorized
again by the job engine against their stack or container target.

## Tests

Docker-free: `internal/manager/updates/eligible` (corpus),
`internal/manager/updates` (in-process manager with the fake OCI registry
and the real agent executor over real files: digest follows the tag,
unchanged digest is a no-op, index-vs-platform, quarantine and no retry
loop, 401/429 never pull, refused pulls need a new check, source drift,
nothing before enabling, windows, standalone containers, ineligible
reasons, candidate publish times, target record names and inactive
reasons), `internal/agent/lifecycle` (`TestUpdate*`, `TestConfirm`:
dependency order, health, completion, optional dependencies, restart
propagation, stopped services), `internal/agent/stacks` (`TestUpdate*`:
source bytes and mtimes identical through successful and failed updates,
tag moved again, index digest, credentials, standalone recreate),
`internal/manager/api` (`TestUpdatePolicy*`, image status).

Updates against a real registry and real Engines are **not verified by
automated tests**: the former integration tests (a digest update from a
private registry on one Engine, a scheduled check and run on two Engines
with a manager-owned connection, #19) were removed on 2026-09-25.
