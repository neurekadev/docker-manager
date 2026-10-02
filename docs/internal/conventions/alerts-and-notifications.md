# Alerts and notifications (#142, #159)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guides: `docs/internal/architecture/notifications.md` (channels) and
`docs/internal/architecture/alerts.md` (alerts, notifications,
thresholds); library decision:
[ADR 0004](../adr/0004-notification-library.md). Package
`internal/manager/notify` (`app.Manager.Notifications()`), store
`internal/manager/store/notification_channels.go`, domain
`internal/domain/notification.go`, API `internal/manager/api/notifications.go`,
web `web/src/lib/features/notifications`.

- **Shoutrrr only through `notify`.** No other package imports
  `github.com/nicholas-fedor/shoutrrr` (it is manager-only:
  `scripts/build-static.sh` fails if the agent links it). Every send goes
  through the adapter's HTTP client and dialer (`shoutrrr.go`: 15 s timeout,
  no cross-scheme redirects, no destination restrictions by owner decision);
  never call `shoutrrr.Send` or the router, never set Shoutrrr's logger.
- **Rich messages are rendered in `notify/render.go` only.** A
  `domain.NotificationMessage` carries a status line (`Label`), a title,
  a plain body, labeled `Fields` (a field may link to a page, `Link`, or
  be a list, `Items`, each entry linked, with an optional `From → To`
  change shown as code), a `Tone` (critical, warning, success, info), a
  footer, a time and a link; `render` turns it into the richest form of
  the address's service (Discord an embed sent in its JSON mode: the
  status line as author, the tone's color, linked values and bulleted
  lists, "Open in Docker Manager" as the last field, its room reserved
  first, the logo `notify.LogoURL` beside the footer; Slack colored
  attachments, Teams an accented card, email one branded dark layout
  (`emailHTML`: the web's design tokens copied as values and kept equal
  by `TestEmailColorsAreTheAppsTokens`, a dark `color-scheme`, the logo
  lockup with `notify.EmailLogoURL`, the status line as the app's badge,
  tables and inline styles only) with the plain part kept, the subject prefixed with the message's `Tag` in
  brackets (its environment, `Test` for a test message) and the sender named `EmailFromName` ("Docker Manager", the
  web's `EMAIL_FROM_NAME`) unless the address names one, Telegram HTML, ntfy and Gotify Markdown with priority and
  click link, Pushover a priority, a generic webhook `tone` and `url`
  keys, anything else plain text). Tone colors are the app's `--danger`,
  `--warn`, `--ok` and `--accent` tokens. Only parameters the service
  knows are set, and an option the owner put in the address (`color`,
  `priority`, `parsemode`, `usehtml`, a Discord `username` or `avatar`,
  …) wins over ours; Discord's name and avatar are never set otherwise
  (the webhook's own stay). In chat formats the title is never a link:
  "Open in Docker Manager" is the message's last line, after every field
  (email keeps its button). The logo is the documentation site's:
  Discord fetches it itself, and the manager's own address may be
  private. Callers never format for a service.
- **One message convention** (`alerts/message.go`): the status line is
  the kind and outcome as **What to send** names them (`alerts.Label`,
  "Disk health · Critical", "Backups · Success"; a resolution "…
  · Resolved", `deliveryLabel`); the title puts the subject first, then
  what happened ("Disk /dev/sda is failing", "Update of Paperless
  succeeded"; a backup policy's run is its policy, "Daily Backups
  succeeded"), names the environment only when it is the subject
  ("homelab is offline") and never the instance (the footer does);
  resolutions are "Resolved: <title>"; digest entries put a problem's
  severity first and a finished run's title alone. Fields: the
  environment, the target (a stack by its display name), the policy and
  the repository link to their pages (an update policy: the environment
  policy above the target's record, `putUpdatePolicy`, never the record,
  which has no page); short (inline) fields come before lists
  (`fieldList.ordered`); services are a list, each linked to its logs (a
  standalone container: its page), with its image digests. Snapshots keep
  paths; `buildMessage` makes them URLs with the public URL (none
  without one). A new kind or field follows the same convention.
- **The address is a secret.** A channel's Shoutrrr URL is sealed
  (`notification_channels/<id>/url`) and read only with
  `store.NotificationChannelWithSecret` inside `notify` (a send, or
  `Reveal`): one row read returns the channel and its address together, so
  a send, its recorded result and its audit details always refer to the
  same address version, service and name.
  Never put it in a domain value, log, audit detail, job input, error,
  `last_result` or response other than the reveal operation; normal reads
  exclude `secret_sealed`. Tests seed `canary.NotificationURL` into the
  URL and scan logs, results, errors, responses, audit records and the
  database file.
- **Send results are classes** (`domain.NotifyErr*`, `notify.Message` in
  words, mirrored in the web's `ERROR_TEXT`): never return, log or store a
  Shoutrrr or `url.Error` text. A failed delivery is a `notify.Result` with
  `OK` false, not an error; it is recorded as the channel's `last_result`
  (no revision change) only while the address version it was sent with is
  still the current one (`RecordNotificationResult` matches
  `secret_version`): a replaced address is never marked Working or Failing
  by its predecessor's send. Log failures with the channel ID, service and
  class only.
- **Owner only.** Every channel route and the alert thresholds are
  `capability: owner` (never with an API token); handlers check the
  owner-only catalog key `notification_channel.manage` before any lookup.
  Creating a channel, changing its address and revealing it need a recent
  step-up (`RequireOwner(ctx, true)`); renaming, the subscription,
  enabling, tests and deletion need the owner. The reveal is
  `Audit: api.AuditAlways`.
- **Subscriptions are outcomes per kind** (`subscriptions` JSON object:
  event kind to outcomes, `domain.NotificationSubscriptions`, never
  empty; a new channel gets every outcome of every kind): each kind lists
  its outcomes in `NotificationEventKind.Outcomes()` (problems warning,
  critical, resolved; offline critical, resolved; backups failure,
  warning, success; restores and prune failure, success; updates
  available, failure, success; other jobs failure, warning, resolved).
  A failed scheduled or API token job is sent as its area's kind
  (`domain.JobEventKind` through `Alert.SentAs`: backup retention,
  verification and imports as backups, update checks as updates; partly
  failed or interrupted is a warning only where the area has one; a
  resolution goes with its failure's outcome, to the told channels that
  still send it, green and labeled "Resolved", never as the area's
  success); `job_failed` keeps the others, and the alert itself stays
  `job_failed`. Moving events to another kind carries channels'
  subscriptions over in its migration, so no channel silently loses
  what it was sent (`splitSubscriptions`). The environment
  choice is `all_environments` (every environment, future ones included)
  **or** the rows of `notification_channel_environments`. It is always
  explicit: an empty list is refused unless `allEnvironments` is set, and
  a restricted channel whose environments are all gone sends none (never
  widens to all). Environments archived since stay in a filter; only
  active ones are added (validated in one query,
  `store.GetEnvironmentsByID`). Messages pick channels with
  `domain.NotificationChannel.Wants(kind, outcome, environmentID)` and
  send with `notify.Service.Send`. Adding an event kind or outcome extends
  the domain lists, the API enums, the web's `EVENT_KINDS` and the user
  docs.
- **Tests** of a send use a generic webhook
  (`generic+http://127.0.0.1:<port>/…`) on an `httptest` server; test
  messages are rate limited in memory (one per channel every
  `notify.TestInterval`, 429 `notification_test_rate_limited`), driven by
  the fake clock. Rendering is tested per service in `render_test.go`
  (services located offline, like address validation); email is also
  sent through Shoutrrr to a loopback SMTP server, because only a real
  send shows which template Shoutrrr writes into the HTML part.
- **Web:** the dialog builds the URL from friendly fields
  (`services.ts`: `buildUrl`/`parseUrl`, round trip exact, unknown shapes
  edited as the raw URL under "Other"); extra query options of a stored URL
  are kept. Email's **From name** is written as `fromname` only when it
  is not the default. Secrets are `PasswordField`s; the stored address stays masked
  until "Show address" (`withStepUp`). "What to send" is one row per kind
  (a master checkbox, the kind's outcomes beside it; a kind whose label
  does not say all it covers explains it as the row's tooltip,
  `EventKindInfo.hint`: Backups, Image updates, Other jobs). Channel changes
  arrive on the live topic `settings` (`notificationKeys`).

## Alerts (#159) and notifications

Package `internal/manager/alerts` (`app.Manager.Alerts()`), store
`internal/manager/store/alerts.go`, `notifications.go` and
`alert_settings.go`, domain `internal/domain/alert.go`,
`notification_record.go` and `alert_thresholds.go`, visibility
`internal/manager/authz/alerts.go`, API `internal/manager/api/alerts.go`
and `notification_events.go`, web `web/src/lib/features/alerts` (the
Notifications page: tabs Notifications and Alerts) and the bell
(`$lib/shell/notices.svelte.ts`).

- **One alert per problem.** Evaluators describe what they see as an
  `alerts.Observation` and call `raise`/`resolve`; never write the
  `alerts` table another way. The dedupe key is unique while firing
  (`alerts_firing_key`). A message is written only on fire, a higher
  severity or a new fingerprint token (`domain.NewTokens`), and on
  resolution `resolved` only to the channels that were sent a message of
  the alert and want resolutions; everything else (progress, counters, a
  peak, a new job of the same key) updates quietly. Put what gets worse in
  the fingerprint (tokens), what only describes it in the facts. Tokens
  mark problems that are only ever added (per member, per attribute,
  `missing_at_least_<k>` ladders), never states that replace each other:
  an improvement must never add a token. Getting worse counts up the
  alert's `escalation`.
- **A notification is a finished run.** Every finished backup (kind
  `backup`), restore (`restore`), prune and update run (any origin;
  cancelled ones are not) records one `notifications` row (kind,
  outcome, facts, job) and its
  messages in a savepoint of the job's finishing transaction
  (`onRunFinished`), announced as `notification.created` once committed.
  Outcomes: failure (failed, partly failed, interrupted), warning (a
  backup that saved everything but had unreadable files or skipped
  items), success. A failed backup, restore, prune or update run of a
  schedule or API token still raises its `job_failed` alert (the Alerts tab), but
  that alert writes no messages: the notification is the message
  (`enqueueTo`, `domain.NotificationKindOfJob`).
- **Host usage thresholds** (`thresholds.go`): temperature, disk space
  per filesystem and memory against `alert_settings` (defaults 80/90 °C,
  85/95 %, 90/95 %) and an environment's override (`NULL` keeps the
  default, 0 is off). A level counts after `ThresholdSustain` (5 min) at
  or above it, resolves below the warning level by `ThresholdHysteresis`
  (3 °C or points), and samples older than `ThresholdStale` (2 min)
  change nothing. The facts keep the peak, so the alert changes only when
  it gets higher. Evaluated on `metrics.sampled` (host) and every
  reconcile.
- **The outbox is the only way out.** Raise/resolve, notifications and
  their `alert_deliveries` rows share one transaction; each row keeps a
  snapshot of what its message says (`snapshot`: title, body, fields,
  outcome, link), and messages are built only from snapshots, never from
  the alert's current row; only the dispatcher calls
  `notify.Service.Send`, in order per channel, with backoff (30 s to 1 h,
  given up after 24 h), coalescing bursts into one digest. Delivery is
  at least once. Channels are chosen with `NotificationChannel.Wants`
  when the message is written and checked again before sending (deleted,
  disabled or unsubscribed: dropped).
- **Nothing secret, nothing raw.** Titles, facts, `Detail`, fields and
  messages never hold serial numbers, job error messages or recovery
  texts, input secrets (an update's container specification, a
  repository's location), host paths, addresses or other secrets; a
  failure is explained from its error class only (`errors.go`: what went
  wrong and what to do, per class; an unknown class is named as it is).
  Tests seed canaries (a disk serial, a job error, a recovery text, a
  destination, an environment value) and scan messages, alerts,
  notifications and logs.
- **Hooks never fail the job.** Job finish hooks (`job_failed` on every
  kind, notifications on backups, restores, prunes and update runs,
  `updates` on `update.check`) write in a savepoint of the job's
  transaction, log their own failures and are announced only as the
  database has them once committed (`OnChange`, read back by the
  announcer; never a rolled back alert or notification).
- **Startup and moves.** Offline grace runs from
  `max(connection_changed_at, service start)` (no alert storm after a
  restart); the reconcile and dispatch loops do nothing while the move
  lock is read-only or stronger.
- **Reading and dismissing.** No read key: `authz.AlertVisible` (the
  source's permission) filters lists, gets and the `alert.updated` event;
  `authz.NotificationVisible` (`job.read` on the job) filters the
  notifications and `notification.created`. `alert.dismiss` is checked
  with `authz.AlertDismissible` (scoped like the source); notifications
  are not dismissed. A dismissal is instance-wide, recorded on the alert
  and audited; getting worse clears it. A new kind needs its rule in
  `authz/alerts.go`, an evaluator with a dedupe key, `Link`, `Detail` and
  fields in `alerts/message.go`, its outcomes and the event kind (above)
  and the user docs.
- **Retention:** resolved alerts and notifications 90 days, finished
  deliveries 7 days except those of firing alerts (they say which
  channels were told) (`Service.Purge`, every reconcile).
- **Configuration:** `DOCKER_MANAGER_ALERT_OFFLINE_GRACE` (5m, 1m–24h);
  thresholds are settings (Settings → Notifications), not variables.
