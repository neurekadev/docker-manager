# Alerts (#159)

Problems Docker Manager raises by itself, keeps until they are resolved,
shows in the app (the bell, the Alerts page, the dashboard's "Needs
attention", the environment's notice) and sends through the notification
channels subscribed to their kind ([notifications.md](notifications.md)).
Binding rules: [alerts-and-notifications.md](../conventions/alerts-and-notifications.md).

| package | role |
| --- | --- |
| `internal/domain/alert.go` | `Alert`, severities, states, resolutions, fingerprints (`Fingerprint`, `NewTokens`), `AlertFilter`, `AlertDelivery` |
| `internal/manager/store/alerts.go` | rows of `alerts` and `alert_deliveries` |
| `internal/manager/alerts` | evaluators (`health.go`, `offline.go`, `jobs.go`), raise/resolve and the outbox (`raise.go`), the dispatcher (`dispatch.go`), messages (`message.go`), dismissals (`dismiss.go`), the loops (`service.go`) |
| `internal/manager/authz/alerts.go` | who sees an alert (`AlertVisible`) and may dismiss it (`AlertDismissible`) |
| `internal/manager/api/alerts.go` | `/api/v1/alerts…` routes |
| `web/src/lib/features/alerts` | the Alerts page's model, filters and actions; the bell (`$lib/shell/notices.svelte.ts`) |

## Model

Migration `20260930120000_create_alerts`:

- `alerts`: one row per problem. `dedupe_key` identifies it and is unique
  among firing rows (partial index `alerts_firing_key`); `kind` (the
  notification event kinds), `severity` (`info`, `warning`, `critical`),
  `state` (`firing`, `resolved`), `environment_id`, `resource_type` and
  `resource_id` (what it is about), `job_kind` and `targets` (JSON: who
  may see a job or update alert), `title`, `facts` (JSON object of small,
  non-secret values), `fingerprint` (sorted problem tokens), `escalation`
  (how often it got worse; the API returns it and a browser keys its local
  dismissal by it), `started_at`,
  `updated_at`, `last_seen_at`, `resolved_at` and `resolution`
  (`resolved`, `removed`, `expired`, `archived`), `dismissed_at`,
  `dismissed_by`, `dismissed_by_name`, `revision`. Resolved rows are
  purged after 90 days.
- `alert_deliveries` (the outbox): one message of an alert to one channel
  (`event` `firing`, `worse` or `resolved`) with a **snapshot** of what it
  says, taken when it is written (`kind`, `environment_id`, `severity`,
  `title`, `body` = `Detail`, `link`): a delayed or retried message
  describes the alert as it was then, never a later state or another job.
  `state` `pending`, `sent`, `failed` (given up) or `dropped`, `attempts`,
  `next_attempt_at`, `last_error` (a send error class). Finished rows are
  purged after 7 days. Invariant: a channel's pending rows are never due
  before its oldest one (a new row is due no earlier than the channel's
  latest pending row, a failed send defers them all), so the partial index
  `alert_deliveries_due` finds the due channels. A new alert reads the
  latest due time of all its channels in one query
  (`alert_deliveries_channel_due`).

Dedupe keys: `disk_health/<env>/<path>/<smartctl type>`,
`raid/<env>/md|zfs/<name>`, `environment_offline/<env>`,
`job_failed/policy/<policy>/<kind>/<first target>` (without a policy
`job_failed/job/<kind>/<env>/<first target>`), `updates_available/<policy>`.

## Raise, update, resolve

Every evaluator describes the problem it sees as an `Observation` (key,
kind, severity, source, title, facts, fingerprint) and calls `raise` in a
transaction:

- no firing alert of the key: a new alert fires and a `firing` message is
  written to the outbox for every channel that `Wants(kind, environment)`;
- a firing one got **worse**: a higher severity, or a fingerprint token it
  did not have (a new failing attribute, a new failed md member, one more
  missing disk, a new image digest). It is updated, its `escalation` counts
  up, a dismissal is cleared (it opens again for everyone) and a `worse`
  message is written. Tokens only ever mark problems that are added
  (`missing_at_least_<k>` for every k up to the count, one token per
  failing attribute), never states that replace each other: an improvement
  (a disk back, an attribute failing only in the past, a pool less broken,
  a partly failed job after a failed one) must not add a token;
- anything else users see changed (title, facts such as rebuild progress
  or more sectors, a new failed job of the same key): updated quietly;
- nothing changed: only `last_seen_at` is stamped (no revision).

`resolve` ends an alert with a resolution. Only `resolved` (the problem
is gone) writes `resolved` messages, for channels with `sendResolved`;
`removed`, `expired` and `archived` end silently. Messages of the alert
that were not sent yet are dropped instead (and that channel gets no
resolution): a problem that went away within the delivery delay is never
reported. Every visible change bumps the revision and is published as
`alert.updated` after the commit.

## Evaluators

| kind | source | severity | resolves |
| --- | --- | --- | --- |
| `disk_health` | the environment's `host.health` report (`observe.Service.HostHealth`), on `inventory.updated` with `health=true` and every reconcile | `failing` critical; `warning` warning; `error` warning ("can't be read"; keeps a stronger alert it has); `sleeping` never (keeps its alert) | the disk is `ok` again; not reported for 24 h: `removed` |
| `raid` | the same report's md arrays and ZFS pools | md `failed`/`inactive` critical, `degraded`/`rebuilding` warning; ZFS `DEGRADED` warning, `FAULTED`/`UNAVAIL`/`SUSPENDED`/`REMOVED` critical | `healthy` (or `checking`) / `ONLINE`; not reported for 24 h: `removed` |
| `environment_offline` | the stored connection state, every reconcile and on connection events | critical | back online; archived (every alert of the environment) `archived`; detached `removed` |
| `job_failed` | `jobs.Engine.OnFinish` of every kind (`jobspec.Kinds()`), in the job's finishing transaction | `failed` critical, `partial`/`interrupted` warning | the key's next succeeded job (any origin); no new run for 7 days: `expired` |
| `updates_available` | `OnFinish` of `update.check` (succeeded or partial), and every reconcile for firing ones | info | no candidate `update_available` left; the policy deleted or inactive: `removed` |

- **Disks** are identified by path and smartctl type (disks behind one
  controller share a path). Tokens: `self_assessment_failed`,
  `critical_warning`, `attribute_<id>`, `reallocated`,
  `pending`, `uncorrectable`, `media_errors`, `grown_defects`, `worn`,
  `spare_low`, `unreadable`: counts live in the facts, so more of the same
  is not sent again. Serial numbers never reach an alert. While SMART is
  not readable (turned off, no access) nothing is resolved as fixed; the
  24-hour removal applies.
- **RAID** tokens are the failed members and `missing_at_least_<k>` for
  every k up to the number of missing disks (fewer missing disks never add
  one); a ZFS pool's token is constant (the severity says whether it got
  worse); the rebuild's action and progress are facts (the title follows:
  "is rebuilding"), so a rebuild never sends anything.
- **Offline:** the grace (`DOCKER_MANAGER_ALERT_OFFLINE_GRACE`, 5m,
  1m–24h) runs from `max(connection_changed_at, service start)`: at start
  every environment is marked offline before the subscribers exist
  (`agents.ResetOnline`), so a restart raises nothing by itself and the
  loop's timer wakes exactly when the next grace ends.
- **Failed jobs:** only jobs with origin `scheduled` or `api_token` raise
  (manual jobs are the starting user's browser notices). The alert names
  the job kind, its first target (a stack's or repository's name, a
  Docker object's name) and the environment, never the job's error message
  or recovery text; facts carry the job ID, kind, state, origin, error
  class and policy. The hook writes in a savepoint of the job's
  transaction: a failure rolls back the alert only (logged), never the
  job's outcome. Alerts changed there are held by job and announced only
  as the database has them: after `OnChange` reports the job's committed
  change (a separate announcer goroutine reads them back; the engine's
  goroutine never touches the database), or after a reconcile round for
  ones never reported. An alert missing or at an older revision (its
  transaction rolled back) is never announced; one that could not be
  read (a busy database) stays ready and is tried again.
- **Updates:** the fingerprint is `service@digest` of every candidate with
  an update available (the UI's `summary.available`), so the alert is sent
  again only when a new digest appears.

## Loops

`Service.Run` (started with the other background loops) runs three
goroutines (the third announces alerts of committed job changes):

- **Reconcile:** subscribes to connection changes, archives and health
  reports (a health report evaluates only its environment), ticks every
  minute (`ReconcileInterval`) and wakes at the next offline due time.
  Each round evaluates offline and archived environments, every active
  environment's host health, every firing update alert, expires old job
  alerts and purges history. A dropped bus event is repaired by the next
  tick.
- **Dispatch:** sends the outbox through `notify.Service.Send`. A new
  message waits `DeliveryDelay` (10 s) so bursts coalesce. It reads only
  the due channels (by due time, indexed) and at most `DispatchBatch`
  (100) of each channel's oldest messages that are due (later ones wait
  for their own delay or backoff), which go out as one message
  (one alert) or one digest ("[Name] 3 alerts, 1 resolved", at most 20
  lines, linking to the Alerts page) built from their snapshots; what is
  left is sent right after. Channels are sent to in parallel (4 at once),
  each in order. A failure defers every pending message of the channel
  by 30 s, doubling up to 1 h, and gives a message up 24 h after it was
  written (`failed`). Messages of a deleted or disabled
  channel, or of a kind or environment the channel no longer wants, are
  `dropped`. Delivery is **at least once**: a send that succeeded but
  could not be recorded is sent again.

Both loops (and every write they make) pause while the manager moves
(`movelock.Lock.ReadOnly`): offline alerts would otherwise fire for every
refused agent. Pending messages wait and go out from the manager that runs
afterwards. Job finish hooks keep writing inside the job's own
transaction.

## Messages

Title `[<instance name>] <alert title>`, resolutions `[<instance name>]
Resolved: <title>`; the body is `Detail(alert)` (one or two sentences
built from the facts only: counters, levels, progress, the offline time,
"A scheduled job did not finish successfully"); the link is the public URL
plus the page the alert is about (the environment's System tab for disks
and RAID, the environment, the job, the update policy; digests link to
`/alerts`). Never serial numbers, job error texts or secrets.

## Visibility, dismissal

No read capability of its own: an alert is shown to whoever may see its
source (`authz.AlertVisible`): `environment.system.read` on the
environment for disks and RAID, the environment visible at all for
offline, `job.read` on the job (its targets, or the kind's capabilities
on every target) for failed jobs, `update_policy.read` on the policy for
updates. The same rule filters the `alert.updated` event.

Dismissing (`alert.dismiss`, normal risk, scoped like the source: the
environment, every target of the failed job, or the update policy) is
instance-wide and recorded on the alert (who, when); the API call is
audited (`alert.dismiss`, the kind and severity as details). A dismissed
alert leaves the bell, "Needs attention" and the environment's notice for
everyone, stays in the Alerts list (Dismissed) and opens again when it
gets worse.

## API

| operation | route | notes |
| --- | --- | --- |
| `list-alerts` | `GET /alerts` | `state` `active`/`dismissed`/`firing`/`resolved`, `kind`, `environmentId`; newest first; filtered per alert |
| `get-alert` | `GET /alerts/{alertId}` | 404 unless the source is visible |
| `create-alert-dismissal` | `POST /alerts/{alertId}/dismissals` | `alert.dismiss`; 409 `alert_not_firing` |
| `create-alert-dismissals` | `POST /alerts/dismissals` | "Dismiss all": the listed (or every active) alerts the caller may dismiss, at most 500 |

The DTO carries `detail` (the message body), `facts`, `link`,
`escalation` (changes exactly when a dismissed alert would open again; the
bell keys a browser-local dismissal by it, never by the fingerprint) and
`actions` (`alert.dismiss` while it fires and the caller may dismiss it).
Changes reach the live stream as `invalidate` topic `alerts`, kind `alert`.

## Tests

| what | where |
| --- | --- |
| disk and RAID severity, fingerprints, progress, sleeping, removal | `internal/manager/alerts/health_test.go` |
| offline grace, startup, move lock, archive, reconcile loop | `internal/manager/alerts/offline_test.go` |
| failed jobs (origin, keys, resolution, expiry), updates (new digests) | `internal/manager/alerts/jobs_test.go` |
| channel filters, backoff, give-up, order, move lock, dropped, digests, canaries | `internal/manager/alerts/dispatch_test.go` |
| snapshots, bounded batches, improvements not worse, rolled back hooks never announced, escalation | `internal/manager/alerts/review_test.go` |
| dismissal, re-open on worse | `internal/manager/alerts/dismiss_test.go` |
| unique firing key, filters, retention | `internal/manager/store/alerts_test.go` |
| visibility per kind, the event rule | `internal/manager/authz/alerts_test.go` |
| routes: shaping, dismissal authorization, audit | `internal/manager/api/alerts_test.go` |
