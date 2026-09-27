# Docker maintenance: prune policies (#14)

Users define prune policies for one environment or all environments, with one rule per resource
category, preview exactly what a run would remove, and run them manually
or on their own cron schedule (#13). Docker Manager never calls the Engine's
broad prune endpoints: the agent lists the objects, filters them with the
rules and every protection, and removes each candidate with a targeted
call after revalidating it.

| Package | Role |
| --- | --- |
| `internal/domain` (`maintenance.go`) | Policies, rules, categories, the shipped suggestions (`SuggestedMaintenanceRules`), defaults, run summaries. |
| `internal/manager/maintenance` | CRUD, validation (volume opt-in), the instance's default rules, previews (agent request), manual runs, the scheduler's `PolicySource`, the `prune.run` finish hook, protections the manager knows. |
| `internal/manager/store` (`maintenance.go`) | `maintenance_policies`, `maintenance_defaults` (migration `20260925233517_create_maintenance_policies`). |
| `internal/manager/api` (`maintenance.go`) | `/api/v1/maintenance-policies` (CRUD, `/previews`, `/runs`), `/api/v1/maintenance-defaults` and the one-off `/api/v1/environments/{id}/prune-previews` and `/prunes`. |
| `internal/protocol` (`maintenance.go`) | `PruneInput` (the `maintenance.preview` request and the `prune.run` job input), preview and run output. |
| `internal/agent/prune` | Planning (candidates and decisions), the `maintenance.preview` handler and the `prune.run` executor (revalidation, targeted removals, per-item results, cancellation between items). |
| `internal/agent/engine` (`prune.go`) | Adapter additions: `ListBuildCache`, `RemoveBuildCache` (one record ID, never the whole cache), `VolumeUsage`; `ContainerFilter.Size`, `Container.SizeRw` and `Container.Networks`. |

## Policies and rules

A policy covers one environment or all environments. Overlapping scopes
are rejected (`maintenance_scope_overlap`). An all-environments preview and
run resolves every current active environment and reports a result per
environment. Each policy has a name, its own schedule
(`cron`, `timeZone`, `enabled`, prefilled from the prune default of the
schedule defaults, `0 3 * * 0`) and exactly one rule per category. The
enabled rules together are the policy's "system cleanup": an explicit
combination, never a Docker system prune.

| Category | Candidates | Age measured from | Filters |
| --- | --- | --- | --- |
| `stopped_containers` | containers in the selected states (`exited`, `dead` by default; `created` on request); never running, paused, restarting | when it stopped (creation if it never ran) | labels, IDs/names |
| `dangling_images` | untagged images no container uses | image creation (Engine) | labels, IDs |
| `unused_images` | every image no container uses (dangling ones the dangling rule already covers are not listed twice) | image creation | labels, IDs/references |
| `unused_networks` | custom networks no container (running or stopped) is configured for and without endpoints | creation | labels, IDs/names |
| `anonymous_volumes` | volumes with `com.docker.volume.anonymous` no container mounts | creation | labels, names |
| `named_volumes` | other volumes no container mounts | creation | labels, names |
| `build_cache` | unused BuildKit records of the Engine's builder: dangling (not shared, not internal/frontend) or all; keep-storage cap | last use (creation if never used) | record IDs only |

Every rule has `enabled`, `minAgeHours` (required in the API: omitting it
never means "any age"), `includeLabels` (all must match), `excludeLabels`
(any excludes), `exclude` (IDs — full or ≥ 12 characters — and names), plus
`containerStates`, `buildCacheAll` and `keepStorageBytes` where they apply.
Configurations the Engine cannot honor exactly are rejected: build cache
records have no labels (422), and options of other categories are refused.

**Safe defaults.** The shipped suggestions (`GET /maintenance-defaults`)
are: every rule disabled, 30 days, stopped means exited or dead, dangling
build cache only; every schedule disabled. Nothing is pruned on first
install: no policy exists, a new policy has no enabled rule (a run is
refused with `maintenance_policy_empty`, a scheduled run is rejected
`no_rules_enabled`), and its schedule is off. The owner (with
`settings.manage`) can change the suggestions; existing policies keep their
rules.

**Volume opt-in.** Enabling `anonymous_volumes` or `named_volumes` needs
`volumeOptIn: true` on that rule (policies and defaults; 422 otherwise).
Each volume rule needs its own; previews evaluate volume rules without it.

## Protection

A candidate is never removed when it is:

- one of Docker Manager's own objects (#32): the agent's `protect.Guard`
  identifies them on every plan and again right before every removal, and
  `protection.Check` refuses them in the executor;
- part of a Docker Manager stack: its Compose project is sent by the manager
  (every stack of the environment, also when it is down) and recognized by
  the agent (working directory in a verified stack root) — containers,
  networks and volumes carrying the project label;
- an image referenced by a stack definition (service image, applied image
  and image ID) or by a saved container specification (#6), or a volume or
  network a saved specification uses; Docker Manager-created standalone containers
  with a saved specification are protected too;
- a backup destination or other object backups rely on: #10 installs
  `maintenance.Service.SetBackupReferences` (volumes/networks by name;
  local repository volumes mounted into Docker Manager containers are also
  Docker Manager's own);
- part of the stopped source of a migrated stack (#35) until the user
  confirms its removal: after the cut-over the source project is no
  longer a Docker Manager stack, so `app` installs
  `maintenance.Service.AddReferences` with
  `migrations.Service.RetainedSources` (the Compose project and the
  migration's source volumes, while the migration runs or completed);
- a predefined (`bridge`, `host`, `none`) or swarm-scoped network.

Previews show these as `protected` with the reason; objects the rule
excludes are `excluded`, recent ones or those within the keep-storage cap
are `retained`. Objects in use are not listed at all (they are not
unused).

## Previews

`POST /maintenance-policies/{id}/previews` sends the policy's enabled rules
(or unsaved `rules`, or all rules with `includeDisabled`) and the
protections to the agent (`maintenance.preview`, 3-minute timeout: volume
sizes are computed by the Engine). The answer lists per category the
candidates in removal order, then protected, excluded and retained objects
(at most 200 items per category), counts and approximate bytes (image sizes
count shared layers; build cache shared with images frees less; `-1` is
unknown). The plan simulates the run: an image, network or volume used only
by a container the same run removes is a candidate. Nothing is stored or
removed. An offline environment answers `503 environment_offline`.

## Runs

`POST /maintenance-policies/{id}/runs {confirm: true, background}` with an
optional `Idempotency-Key` enqueues a `prune.run` job (202 + job):

- **Confirmation**: without `confirm: true` → `409
  prune_confirmation_required`.
- **Foreground/background** is a presentation preference only: both are
  the same durable manager-owned job, independent of the requesting session;
  leaving the UI never cancels it. A repeated key returns the same job
  whatever the preference (`TestMaintenancePolicyLifecycle`).
- **Overlap**: a second manual run while one of the policy is queued or
  running → `409 maintenance_run_active` (with the job ID); scheduled runs
  are skipped by the scheduler for the same reason.
- **Input**: the enabled rules and the protections at enqueue time. A
  queued run that has not started is cancelled when the policy's rules
  change or the policy is deleted (it carries the old rules).
- **Locks** (#26): shared `*` locks on stacks, containers, images,
  networks and volumes of the environment: prunes run together, but a prune
  waits for (and holds back) deploys, builds, updates, pulls, migrations,
  backup shutdowns and restores — a freshly pulled image has no container
  yet, so revalidation alone could not protect it.

The agent executor:

1. `collect_candidates` plans exactly like a preview and journals the
   candidates (at most 300 per run; the rest is `deferred` to the next run)
   as the job output.
2. `delete_candidates` processes them in category order (containers first,
   build cache last; build cache children before parents). For each item it
   reads the Engine again — the container list, Docker Manager's protected set,
   the object itself — and re-evaluates rule, protection and usage; an item
   that changed is `skipped` with the reason (`now running`, `now used by
   container x`, `protected: …`, `already removed`). The removal is a
   targeted call without force, so the Engine itself refuses anything that
   became used in the last instant (a conflict is a skip). Tagged images are
   untagged down to one reference and removed by ID. Build cache records are
   removed with a builder prune restricted to exactly that record ID.
3. After every item the output (status, reason, bytes) is journaled and a
   job item is reported (`removed`, skipped reasons, errors); failed items
   make the job `partial`. Cancellation (`POST /jobs/{id}/cancellations`)
   is honored between items; completed deletions are final.

An Engine that becomes unreachable fails the step with the items done so
far in the output; a resumed attempt (agent restart, #26 reconciliation)
continues with the pending items only. An offline agent leaves the job
`blocked` (`agent_offline`) until the kind's offline deadline (1 h), then
it fails without having touched anything.

The job's audit record (`job.finished`, #30) carries the item list. The
finish hook stores the latest run's summary on the policy (`lastRun`:
state, origin, removed, skipped, failed, deferred, bytes reclaimed).

## One-off prunes

The Containers, Images, Volumes, Networks and Builds pages have a
"Prune" button (`web/src/lib/features/maintenance/PruneButton.svelte`)
for a single prune of one environment without a policy:

- `POST /environments/{id}/prune-previews {rules}` and
  `POST /environments/{id}/prunes {rules, confirm: true}` (202 + job,
  `Idempotency-Key`) take the rules of this prune only; categories not
  given are not pruned, at least one rule must be enabled, and enabling a
  volume rule needs its `volumeOptIn` (previews evaluate volume rules
  without it). Nothing is saved.
- Authorization: `maintenance.preview` / `maintenance.run` on the
  environment (instance or environment rules; a grant on a policy is not
  enough). The job is a `prune.run` without policy and without targets, so
  the job engine authorizes it on the environment, too.
- The agent input carries the same protections as a policy run and a
  synthetic policy ID (`manual-<uuid>`, `maintenance.ManualPolicyPrefix`):
  agents require one, and N-1 agents must keep accepting the input. The
  finish hook ignores jobs without a policy.
- A repeated `Idempotency-Key` returns the prune it started (resolved by
  the service: the input is rebuilt on every call).
- The UI starts from the page's usual prune (`manualPruneRules`), like the
  Docker CLI's prunes with `--all`: exited or dead containers, every
  unused image (dangling ones included), unused networks, anonymous
  volumes (named volumes off; volume rules need their opt-in) and all
  unused build cache, of any age. Every rule's options can be changed for
  this prune. A preview is required before the prune button appears.

## Scheduling (#13)

`maintenance.Service.PolicySource()` is registered for the `prune` kind:
`Schedules` lists every policy's saved schedule; `Validate` (when due and at
dispatch) rejects deleted (`policy_not_found`), disabled
(`policy_disabled`), emptied (`no_rules_enabled`) policies and missing or
archived environments; `Jobs` builds the same request as a manual run.
Scheduled runs run as the manager's service identity (origin `scheduled`),
always in the background. Missed runs (manager down) are recorded and
skipped (catch-up policy `skip`): a prune never starts at an unexpected
time. Overlap: a due run while the previous one is active is `skipped`.

## Limitations

- Image age is the image's creation time as the Engine reports it (like
  `docker image prune --filter until`), not when it was pulled.
- Anonymous volumes created before Docker 23 carry no anonymous label and
  count as named volumes.
- Build cache: v1 uses only the Engine's built-in builder, so there is no
  builder selection; records have no labels; a parent record BuildKit keeps
  while a child exists goes in a later run.
- Container sizes need the Engine's size computation (`size=1`), which can
  be slow on large hosts; they are informational.
