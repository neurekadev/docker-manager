# Alerts and notifications (#159)

**Alerts** are problems Docker Manager raises by itself, keeps until they
are resolved, shows in the app (the bell, the Notifications page's Alerts
tab, the dashboard's "Needs Attention", the environment's notice) and
sends through the notification channels subscribed to their kind and
outcome ([notifications.md](notifications.md)). **Notifications** are
runs that finished (a backup, a restore, a prune, an update run): recorded
once with how they went, listed on the Notifications tab and sent the same
way. Binding rules:
[alerts-and-notifications.md](../conventions/alerts-and-notifications.md).

| package | role |
| --- | --- |
| `internal/domain/alert.go` | `Alert`, severities, states, resolutions, fingerprints (`Fingerprint`, `NewTokens`), `Outcome`, `SentAs`, `AlertFilter`, `AlertDelivery` (and its `Tone`) |
| `internal/domain/notification_record.go` | `Notification` (a finished run), `NotificationKindOfJob`, `JobEventKind`, `NotificationFilter` |
| `internal/domain/alert_thresholds.go` | `AlertThresholds`, `AlertThresholdOverride`, `AlertSettings`, the defaults |
| `internal/manager/store/alerts.go`, `notifications.go`, `alert_settings.go` | rows of `alerts`, `alert_deliveries`, `notifications`, `alert_settings`, `alert_threshold_overrides` |
| `internal/manager/alerts` | evaluators (`health.go`, `thresholds.go`, `offline.go`, `jobs.go`), notifications (`notifications.go`), raise/resolve and the outbox (`raise.go`), the dispatcher (`dispatch.go`), messages (`message.go`), error classes in words (`errors.go`), dismissals (`dismiss.go`), thresholds and lists for the API (`settings.go`), the loops (`service.go`) |
| `internal/manager/authz/alerts.go` | who sees an alert (`AlertVisible`) or a notification (`NotificationVisible`) and may dismiss an alert (`AlertDismissible`) |
| `internal/manager/api/alerts.go`, `notification_events.go` | `/api/v1/alerts…`, `/api/v1/notifications`, `/api/v1/alert-settings` |
| `web/src/lib/features/alerts` | the Notifications page (tabs Notifications and Alerts): models, filters and actions; the bell (`$lib/shell/notices.svelte.ts`) |

## Model

Migrations `20260930120000_create_alerts`,
`20261001090000_notification_events` and
`20261002120000_restore_notifications` (kind `restore`; recorded
restores and their messages moved to it; `notifications` and
`alert_deliveries` rebuilt together, as deliveries reference
notifications):

- `alerts`: one row per problem. `dedupe_key` identifies it and is unique
  among firing rows (partial index `alerts_firing_key`); `kind`
  (`disk_health`, `raid`, `temperature`, `disk_space`, `memory`,
  `environment_offline`, `updates`, `job_failed`, `backup`), `severity` (`info`,
  `warning`, `critical`), `state` (`firing`, `resolved`), `environment_id`,
  `resource_type` and `resource_id` (what it is about), `job_kind` and
  `targets` (JSON: who may see a job or update alert), `title`, `facts`
  (JSON object of small, non-secret values), `fingerprint` (sorted problem
  tokens), `escalation` (how often it got worse; the API returns it and a
  browser keys its local dismissal by it), `started_at`, `updated_at`,
  `last_seen_at`, `resolved_at` and `resolution` (`resolved`, `removed`,
  `expired`, `archived`), `dismissed_at`, `dismissed_by`,
  `dismissed_by_name`, `revision`. Resolved rows are purged after 90 days.
- `notifications`: one row per finished run: `kind` (`backup`,
  `restore`, `prune`, `updates`), `outcome` (`success`, `warning`, `failure`),
  `environment_id`, `job_id`, `job_kind`, `targets` and `origin` (who may
  see it, who started it), `title`, `facts`, `created_at`. Purged after
  90 days (their deliveries cascade).
- `alert_deliveries` (the outbox): one message of an alert or of a
  notification (exactly one of `alert_id`, `notification_id`) to one
  channel (`event` `firing`, `worse`, `resolved` or `notification`) with
  a **snapshot** of what it says, taken when it is written (`kind`,
  `environment_id`, `severity`, `outcome`, `title`, `body` = `Detail` or
  `NotificationDetail`, `fields` (JSON array of name, value, inline),
  `link`): a delayed or retried message describes the alert as it was
  then, never a later state or another job. `state` `pending`, `sent`,
  `failed` (given up) or `dropped`, `attempts`, `next_attempt_at`,
  `last_error` (a send error class). Finished rows are purged after 7 days,
  except those of firing alerts (they record which channels were told, who
  get the resolution). Invariant: a channel's pending rows are never due
  before its oldest one (a new row is due no earlier than the channel's
  latest pending row, a failed send defers them all), so the partial index
  `alert_deliveries_due` finds the due channels. A new message reads the
  latest due time of all its channels in one query
  (`alert_deliveries_channel_due`).
- `alert_settings` (one row): warning and critical levels of temperature
  (°C, 0–150), disk space and memory (percent used, 0–100); 0 is off.
  Defaults 80/90, 85/95, 90/95. `alert_threshold_overrides`: per
  environment (cascades with it), each level `NULL` (the default) or a
  value.

Dedupe keys: `disk_health/<env>/<path>/<smartctl type>` (a disk alert
moves to its disk's new path, below), `disk_health/<env>/monitoring`,
`raid/<env>/md|zfs/<name>`, `raid/<env>/monitoring`, `temperature/<env>`,
`disk_space/<env>/<mount>`, `memory/<env>`, `environment_offline/<env>`,
`job_failed/policy/<policy>/<kind>/<first target>` (without a policy
`job_failed/job/<kind>/<env>/<first target>`), `updates_available/<policy>`
(the key kept its old name; the kind is `updates`), `backup/no_primary`.

## Raise, update, resolve

Every evaluator describes the problem it sees as an `Observation` (key,
kind, severity, source, title, facts, fingerprint) and calls `raise` in a
transaction:

- no firing alert of the key: a new alert fires and a `firing` message is
  written to the outbox for every channel that
  `Wants(kind, outcome, environment)`;
- a firing one got **worse**: a higher severity, or a fingerprint token it
  did not have (a new failing attribute, a new failed md member, one more
  missing disk, a new image digest). It is updated, its `escalation` counts
  up, a dismissal is cleared (it opens again for everyone) and a `worse`
  message is written. Tokens only ever mark problems that are added
  (`missing_at_least_<k>` for every k up to the count, one token per
  failing attribute), never states that replace each other: an improvement
  (a disk back, an attribute failing only in the past, a pool less broken,
  a partly failed job after a failed one) must not add a token;
- anything else users see changed (title, facts such as rebuild progress,
  a new peak or more sectors, a new failed job of the same key): updated
  quietly;
- nothing changed: only `last_seen_at` is stamped (no revision).

A message's **kind and outcome** are what channels subscribe to
(`Alert.SentAs`): the alert's kind and the severity in the words of its
kind (`Alert.Outcome`: `warning`, `critical`; a failed job's critical is
`failure`, available updates `available`) or `resolved`. A failed job's
alert is sent as the kind of its job's area instead (below, "Failed jobs
by area").

`resolve` ends an alert with a resolution. Only `resolved` (the problem
is gone) writes `resolved` messages, to the channels that were **sent** a
message of the alert and want `resolved`; `removed`, `expired` and
`archived` end silently. Messages of the alert that were not sent yet are
dropped instead (that channel was never told: it gets no resolution): a
problem that went away within the delivery delay is never reported. Every
visible change bumps the revision and is published as `alert.updated`
after the commit.

## Evaluators

| kind | source | severity | resolves |
| --- | --- | --- | --- |
| `disk_health` | the environment's `host.health` report (`observe.Service.HostHealth`), on `inventory.updated` with `health=true` and every reconcile; every listed device whatever the SMART status | `failing` critical; `warning` warning; `error` warning ("can't be read", `missing`: "is missing"; keeps a stronger alert it has); `unsupported` never (an alert it had ends `removed`); `permission_denied` under status `no_access` never (the monitoring alert says it); `sleeping` never (keeps its alert). Monitoring (`disk_health/<env>/monitoring`, warning): status `error`, `no_access` or `not_installed`, `checkedAt` older than twice `intervalSeconds` plus 30 min, or an environment online for 15 min whose report is 15 min old | the disk is `ok` again; not reported for 24 h while its monitoring works: `removed`; monitoring: it works again |
| `raid` | the same report's md arrays and ZFS pools | md `failed`/`inactive` critical, `degraded`/`rebuilding` warning; ZFS `DEGRADED` warning, `FAULTED`/`UNAVAIL`/`SUSPENDED`/`REMOVED` critical. Monitoring (`raid/<env>/monitoring`, warning): `raid.message` set, or the report 15 min old as above | `healthy` (or `checking`) / `ONLINE`; not reported for 24 h while its monitoring works: `removed`; monitoring: it works again |
| `temperature` | the hottest sensor of the latest readings (`observe.Service.LatestTemperatures`: sensors that reported in the last 5 minutes, one query), on `metrics.sampled` with `host=true` and every reconcile | at or above the warning / critical level for 5 minutes | below the warning level by 3 °C; thresholds off or no sensor reported: `removed` |
| `disk_space` | each filesystem of the latest host sample (`observe.Service.Latest`: Docker data, stacks, bind mounts) | the same, in percent used | below the warning level by 3 points; off or no longer reported: `removed` |
| `memory` | the latest host sample's used memory (page cache not counted) | the same, in percent used | the same |
| `environment_offline` | the stored connection state, every reconcile and on connection events | critical | back online; archived (every alert of the environment) `archived`; detached `removed` |
| `job_failed` | `jobs.Engine.OnFinish` of every kind (`jobspec.Kinds()`), in the job's finishing transaction | `failed` critical, `partial`/`interrupted` warning | the key's next succeeded job (any origin); no new run for 7 days: `expired` |
| `updates` | `OnFinish` of `update.check` (succeeded or partial), and every reconcile for firing ones | info | no candidate `update_available` left; the policy deleted or inactive: `removed` |
| `backup` (backups paused, #246) | the backup service's `OnPrimaryMissing` (`Service.BackupsPaused`): at start and after every change of the backup setup or of a repository with a role, when backups are on without a Primary repository | critical; sent as a backup failure, its resolution to the told channels | a Primary is set or backups are turned off |

- **Disks** are keyed by path and smartctl type (disks behind one
  controller share a path) and follow their disk: the fact `diskId` is a
  hash of the environment and the serial number (never the number). When
  the disk is reported under another path (sdX names move after a reboot
  or a hot-swap), its alert is re-keyed there (`store.RekeyAlert`, two
  steps so paths may swap), keeping its ID, start, escalation and
  dismissal; nothing is sent for the move. When another disk holds its
  path and its own disk is not reported (replaced), it ends `removed`,
  never as fixed. Tokens: `self_assessment_failed`, `critical_warning`,
  `over_temperature`, `ran_hot`, `ran_critically_hot`, `attribute_<id>`,
  `reallocated`, `pending`, `uncorrectable`, `end_to_end`,
  `media_errors`, `grown_defects`, `worn`, `spare_low`, `unreadable`,
  `missing`: counts live in the facts, so more of the same is not sent
  again. `over_temperature` (the NVMe temperature warning, or a
  temperature at or above the disk's own limit, #212) has the host
  temperature alert's hysteresis: once raised it holds, with the reading
  that raised it, until the disk is `ThresholdHysteresis` (3 °C) below
  its limit, so a disk hovering around its limit sends nothing again;
  `ran_hot` / `ran_critically_hot` (lifetime minutes above the limits)
  stay, so a new overheat after cooling down is still news. Serial numbers never reach an alert.
- **What can't be seen is not gone.** Without a report, while the SMART
  status is anything but `ok` or `disabled`, while the RAID state can't be
  read or while the report is stale, alerts of disks and arrays the report
  does not list are kept past the 24-hour removal, and the monitoring
  alert (facts `monitoring` `smart` or `raid`, `reason` `scan_failed`,
  `no_access`, `not_installed`, `stale`, `no_report` or `raid_read`;
  resource the environment; a constant token, the reasons replace each
  other) says why. SMART turned off on purpose (`disabled`) raises
  nothing; the 24-hour removal applies.
- **RAID** tokens are the failed members and `missing_at_least_<k>` for
  every k up to the number of missing disks (fewer missing disks never add
  one); a ZFS pool's token is constant (the severity says whether it got
  worse); the rebuild's action and progress are facts (the title follows:
  "is rebuilding"), so a rebuild never sends anything.
- **Host usage** (`thresholds.go`): the thresholds are the environment's
  (`AlertSettings.For`: the defaults with its override). Since when a
  value is at or above each level is kept in memory per key
  (`Service.over`): a level counts once it held for `ThresholdSustain`
  (5 min), so a short spike raises nothing and a restart starts counting
  again; critical counts on its own. Between the warning level and 3
  below it a firing alert stays as it is. The facts keep the peak
  (`celsius` or `usedPercent`, with the sensor, the free and total bytes
  at the peak and the levels): a lower value changes nothing. Samples
  older than `ThresholdStale` (2 min) are not evaluated (an offline
  environment keeps its alerts). Changing the thresholds wakes the
  reconcile loop.
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
  class and policy. A failed or partly failed update check also names the
  services it could not check (`failedItems`) and the first one's error
  class (`itemErrorClass`, from its candidate; never the registry's
  message), which explains it when the job's own class is only
  `step_failed`. A failed backup, restore, prune or update run raises
  its alert like any job, but the alert writes **no messages**: the run's
  notification is its message. Every alert of a failed job keeps the
  kind `job_failed` (the Alerts tab's "Failed Job", its dedupe key and
  expiry). The hook writes in a savepoint of the
  job's transaction: a failure rolls back the alert only (logged), never
  the job's outcome. Alerts changed there are held by job and announced
  only as the database has them: after `OnChange` reports the job's
  committed change (a separate announcer goroutine reads them back; the
  engine's goroutine never touches the database), or after a reconcile
  round for ones never reported. An alert missing or at an older revision
  (its transaction rolled back) is never announced; one that could not be
  read (a busy database) stays ready and is tried again.
- **Failed jobs by area:** the messages of a failed job's alert go out
  as the kind of its job's area (`domain.JobEventKind`, used by
  `alertSnapshot` through `Alert.SentAs`), so channels choose them with
  that area's row of **What to Send**: `backup.retention`,
  `backup.verify`, `backup.import`, `manager.retention` and
  `manager.verify` as `backup`, `update.check` as `updates` (the runs
  with notifications as theirs: `backup.run` and `manager.backup`
  `backup`, `restore.run` `restore`, `prune.run` `prune`, `update.run`
  `updates`, which write no alert messages); every other kind stays
  `job_failed` (in practice jobs API tokens start, such as deploys).
  Firing: failed is `failure`; partly failed or interrupted is `warning`
  where the area has warnings (`backup`), else `failure`. The area has
  no `resolved`: the resolution goes to each told channel (`resolve`,
  raise.go) with the outcome the alert has when it resolves, or, when
  the channel does not send that one, with an outcome it was told before
  (a failure lowered to a warning by a later partly failed run still
  resolves to a channel that sends failures only; `write`), checked again
  by the dispatcher (`Wants`); never the area's `success`,
  which would need run successes switched on. Its status line says
  "Resolved" (`deliveryLabel`) and its tone is green (`Tone` of a
  `resolved` event). `job_failed` keeps its own outcomes. The migration
  carried every channel's `job_failed` failure and warning over to
  `backup` (failure, warning) and `updates` (failure), so no channel
  loses the failed jobs it was sent before.
- **Updates:** the fingerprint is `service@digest` of every candidate with
  an update available (the UI's `summary.available`), so the alert is sent
  again only when a new digest appears. Its facts are the target (a stack
  by its display name), the count, the first 10 services in words
  (`services`) and with their digests (`changes`: what runs → the newer
  image, `encodeChanges`), and the updates setup that manages the target
  (`policy` "Automatic Updates", `policyId`: the record's parent,
  `putUpdatePolicy`; the record itself has no page). Its link and Policy
  field open the Updates page; a record created by hand names itself.

## Notifications

`onRunFinished` is the finish hook of `backup.run`, `manager.backup`
(kind `backup`), `restore.run` (`restore`), `prune.run` (`prune`) and
`update.run` (`updates`), whoever started the job; a cancelled one records nothing. In
a savepoint of the job's transaction it records the notification and its
messages (to every channel that `Wants(kind, outcome, environment)`), and
the announcer publishes `notification.created` once the job's change is
committed (read back like alerts). Facts come from the job's state, error
class, timing and result output (`ResultOutput`; nil when too large: a
notification without numbers), never from its error message, recovery,
warnings or input secrets:

| run | facts | outcome |
| --- | --- | --- |
| backup | policy, repository (names and IDs, for their pages), items backed up / failed / skipped, the names that failed, were skipped or had unreadable files, bytes read, repository size and snapshots | failure: failed, partly failed or interrupted; warning: succeeded with unreadable files or skipped items; success |
| restore | the stack (by its display name) or volumes, the target (`targetType`, `targetId`), the repository (name and ID), whether to deploy the restored Compose file | failure or success |
| prune | policy ("Maintenance" and the setup's ID for maintenance's runs; none for one-off prunes), reclaimed bytes, removed / skipped / failed / deferred, and per kind of object (containers, images, volumes, networks, build cache: removed items and their bytes) | failure or success |
| update | the target (a stack by its display name; `targetType`, `targetId`), the updates setup that manages it ("Automatic Updates" and its ID; the input's `policyId` is the target's record, which has no page; a record created by hand: its own name), linked to Updates, per group (updated, unchanged, kept stopped, failed) its count (`<group>Count`) and up to 10 services with their digests (`<group>Changes`: "service TAB from TAB to", 12 digits); the unchanged, kept stopped and failed names in words for the body | failure (failed, partly failed, interrupted) or success |

Every notification also has the job, its kind, state, origin, who started
it (a manual job's user) and its duration.

## Loops

`Service.Run` (started with the other background loops) runs three
goroutines (the third announces alerts and notifications of committed job
changes):

- **Reconcile:** subscribes to connection changes, archives, health
  reports (a health report evaluates only its environment's disks) and
  host samples (`metrics.sampled` with `host=true`: that environment's
  usage), ticks every minute (`ReconcileInterval`) and wakes at the next
  offline due time. Each round evaluates offline and archived
  environments, every active environment's host health and usage, every
  firing update alert, expires old job alerts and purges history. A
  dropped bus event is repaired by the next tick.
- **Dispatch:** sends the outbox through `notify.Service.Send`. A new
  message waits `DeliveryDelay` (10 s) so bursts coalesce. It reads only
  the due channels (by due time, indexed) and at most `DispatchBatch`
  (100) of each channel's oldest messages that are due (later ones wait
  for their own delay or backoff), which go out as one message
  (one alert or notification) or one digest (status line "Summary",
  title "3 alerts, 1 resolved, 2 notifications", a "What Happened" list
  of at most 20 entries: a problem's severity before its title
  ("Critical: Disk /dev/sda is failing"), "Resolved: <title>", a finished
  run's or available updates' title alone (it says what happened), each
  linked to its page and naming its
  environment unless the title starts with it ("(homelab)"), linking to the
  Notifications page, its Alerts tab when it holds alerts only, in the
  tone of its worst entry) built from their snapshots; what is left is
  sent right after. Channels are sent to in parallel (4 at once), each in
  order. A failure defers every pending message of the channel by 30 s,
  doubling up to 1 h, and gives a message up 24 h after it was written
  (`failed`). Messages of a deleted or disabled channel, or of a kind,
  outcome or environment the channel no longer wants, are `dropped`.
  Delivery is **at least once**: a send that succeeded but could not be
  recorded is sent again.

Both loops (and every write they make) pause while the manager moves
(`movelock.Lock.ReadOnly`): offline alerts would otherwise fire for every
refused agent. Pending messages wait and go out from the manager that runs
afterwards. Job finish hooks keep writing inside the job's own
transaction.

## Messages

Every message follows one convention (#174). The **status line**
(`Label`) is the kind and outcome as **What to Send** names them, in Title
Case ("Disk Health · Critical", "Environment Offline · Back Online", "Image
Updates · Applied", "Backups · Success", "Restores · Failure"; a resolution says
"Resolved", also a failed job's sent with its failure's outcome:
"Backups · Resolved"). The **title** is the alert's or notification's: the subject
first, then what happened ("Disk /dev/sda is failing", "RAID md0 is
degraded", "Docker data disk is almost full", "homelab is offline",
"Paperless has 2 updates available", "Deploy of Paperless failed",
"Backups succeeded" (a run of the backup setup, #246; "Backup succeeded"
without one, "Docker Manager backup succeeded" for the manager's own), "Restore of Paperless succeeded", "Prune reclaimed 3
GiB", "Update of Paperless succeeded"; a stack by its display name). It names the environment only when the environment is the
subject (it has a field of its own) and never the instance's name (the
footer does); resolutions are `Resolved: <title>`. The body is
`Detail(alert)` (resolutions:
`resolvedDetail`, "The disk is healthy again.") or
`NotificationDetail(notification)`: one or two sentences built from the
facts only (counters, levels, progress, the offline time, the peak and
thresholds, what a run did) that say what to do where there is something
to do. A failure is explained from its error class (`errors.go`,
`describeError`: about 90 classes of the job engine, restic, backups,
restores, updates, update checks and prune, each "what went wrong" and
"what to do"; an unknown class is named as it is). The **fields**
(`alertFields`, `NotificationFields`) label the numbers in Title Case, the
short (inline) ones before the lists: the environment, what it is about (the
target, policy and repository), sizes and counts per kind (a prune's
Containers / Images / Volumes / Networks / Build Cache), duration, who
started it ("Started By"), "What Went Wrong" and "What to Do". A failed
job's Job field is `kindLabel` (the title's `kindNoun` in Title Case:
"Backup Retention"); a filesystem's is `MountLabel` ("Docker Data", "Bind
Mount 2"), while sentences use `mountWords` ("Docker data"). The environment, target,
policy and repository link to their pages; services are lists, each
entry linked to the stack's logs of that service (a standalone
container: its page) with its image digests (`from → to`; an update
alert: what runs and the newer image). The severity is the status
line's, not a field. Snapshots keep the links as paths; `buildMessage`
prefixes the public URL (no links without one). The **tone** follows the
outcome: critical and failure red, warning amber, every resolution and success
green, available blue. The footer is the instance's name ("Docker
Manager" without one), the time the message's. `Tag` is the
name of the one environment the message is about (the delivery's
Environment field; a digest's only when every entry names the same one):
emails prefix their subject with it. The link is the public
URL plus the page it is about (the
environment's System tab for disks and RAID, the environment for offline
and host usage, the job for failed jobs and notifications, `/updates`
for updates, `/backups` for paused backups; digests link to
`/notifications`). Never serial numbers, job
error texts or secrets. `notify` renders the message for each service
([notifications.md](notifications.md#delivery)).

## Visibility, dismissal

No read capability of its own: an alert is shown to whoever may see its
source (`authz.AlertVisible`): `environment.system.read` on the
environment for disks and RAID, `environment.metrics.read` for
temperature, disk space and memory, the environment visible at all for
offline, `job.read` on the job (its targets, or the kind's capabilities
on every target) for failed jobs, `update_policy.read` on the policy for
updates, `backup_policy.read` on the instance for paused backups. The same rule filters the `alert.updated` event. A notification
is shown with `job.read` on its job (`authz.NotificationVisible`, also for
`notification.created`).

Dismissing (`alert.dismiss`, normal risk, scoped like the source: the
environment, every target of the failed job, or the update policy) is
instance-wide and recorded on the alert (who, when); the API call is
audited (`alert.dismiss`, the kind and severity as details). A dismissed
alert leaves the bell, "Needs Attention" and the environment's notice for
everyone, stays in the Alerts list (Dismissed) and opens again when it
gets worse. Notifications are a history: not dismissed.

## API

| operation | route | notes |
| --- | --- | --- |
| `list-alerts` | `GET /alerts` | `state` `active`/`dismissed`/`firing`/`resolved`, `kind`, `environmentId`; newest first; filtered per alert |
| `get-alert` | `GET /alerts/{alertId}` | 404 unless the source is visible |
| `create-alert-dismissal` | `POST /alerts/{alertId}/dismissals` | `alert.dismiss`; 409 `alert_not_firing` |
| `create-alert-dismissals` | `POST /alerts/dismissals` | "Dismiss All": the listed (or every active) alerts the caller may dismiss, at most 500 |
| `list-notifications` | `GET /notifications` | `kind`, `outcome`, `environmentId`; newest first; filtered per notification |
| `get-alert-settings` | `GET /alert-settings` | owner; ETag |
| `update-alert-settings` | `PUT /alert-settings` | owner; If-Match; replaces the thresholds and every override; audited (the values) |

The alert DTO carries `detail` (the message body), `facts`, `fields`,
`link`, `escalation` (changes exactly when a dismissed alert would open
again; the bell keys a browser-local dismissal by it, never by the
fingerprint) and `actions` (`alert.dismiss` while it fires and the caller
may dismiss it); a notification `detail`, `fields`, `facts` and `link`.
Changes reach the live stream as `invalidate` topic `alerts`, kind
`alert` or `notification`.

## Tests

| what | where |
| --- | --- |
| disk and RAID severity, fingerprints, progress, sleeping, removal | `internal/manager/alerts/health_test.go` |
| host usage: sustain, margin, peak, overrides, stale samples, validation | `internal/manager/alerts/thresholds_test.go` |
| offline grace, startup, move lock, archive, reconcile loop | `internal/manager/alerts/offline_test.go` |
| failed jobs (origin, keys, resolution, expiry, sent by area), updates (new digests, the link to Updates) | `internal/manager/alerts/jobs_test.go` |
| backups paused (one alert, sent as a failure, resolution to the told channel) | `internal/manager/alerts/backups_test.go` |
| notifications (prune breakdown, backup failure words and warnings, restores, updates, canaries), outcome filters, resolutions only to told channels | `internal/manager/alerts/notifications_test.go` |
| channel filters, backoff, give-up, order, move lock, dropped, digests, canaries | `internal/manager/alerts/dispatch_test.go` |
| details, fields, error classes in words, tones, digests | `internal/manager/alerts/message_test.go` |
| snapshots, bounded batches, improvements not worse, rolled back hooks never announced, escalation | `internal/manager/alerts/review_test.go` |
| dismissal, re-open on worse | `internal/manager/alerts/dismiss_test.go` |
| unique firing key, filters, retention, notifications, delivery fields, thresholds and overrides | `internal/manager/store/alerts_test.go`, `notifications_test.go` |
| old subscriptions converted to outcomes; restores split from backups (rows, deliveries, subscriptions, down) | `internal/db/migrations/notification_events_test.go`, `restore_notifications_test.go` |
| visibility per kind, the event rules | `internal/manager/authz/alerts_test.go` |
| routes: shaping, dismissal authorization, audit, notifications per job, thresholds owner-only | `internal/manager/api/alerts_test.go`, `notification_events_test.go` |
