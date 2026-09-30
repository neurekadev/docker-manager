# Alerts and notifications (#142, #159)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guides: `docs/internal/architecture/notifications.md` (channels) and
`docs/internal/architecture/alerts.md` (alerts); library decision:
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
- **Owner only.** Every route is `capability: owner` (never with an API
  token); handlers check the owner-only catalog key
  `notification_channel.manage` before any lookup. Creating a channel,
  changing its address and revealing it need a recent step-up
  (`RequireOwner(ctx, true)`); renaming, the subscription, enabling, tests
  and deletion need the owner. The reveal is `Audit: api.AuditAlways`.
- **Subscriptions** are stored per channel: `event_kinds` (the
  `domain.NotificationEventKinds`; never empty; a new channel gets all),
  `send_resolved`, and the environment choice: `all_environments` (every
  environment, future ones included) **or** the rows of
  `notification_channel_environments`. It is always explicit: an empty list
  is refused unless `allEnvironments` is set, and a restricted channel whose
  environments are all gone sends none (never widens to all). Environments
  archived since stay in a filter; only active ones are added (validated in
  one query, `store.GetEnvironmentsByID`). Alerts pick channels with
  `domain.NotificationChannel.Wants(kind, environmentID)` and send with
  `notify.Service.Send`. Adding an event kind extends the domain list, the
  API enum, the web's `EVENT_KINDS` and the user docs.
- **Tests** of a send use a generic webhook
  (`generic+http://127.0.0.1:<port>/…`) on an `httptest` server; test
  messages are rate limited in memory (one per channel every
  `notify.TestInterval`, 429 `notification_test_rate_limited`), driven by
  the fake clock.
- **Web:** the dialog builds the URL from friendly fields
  (`services.ts`: `buildUrl`/`parseUrl`, round trip exact, unknown shapes
  edited as the raw URL under "Other"); extra query options of a stored URL
  are kept. Secrets are `PasswordField`s; the stored address stays masked
  until "Show address" (`withStepUp`). Channel changes arrive on the live
  topic `settings` (`notificationKeys`).

## Alerts (#159)

Package `internal/manager/alerts` (`app.Manager.Alerts()`), store
`internal/manager/store/alerts.go`, domain `internal/domain/alert.go`,
visibility `internal/manager/authz/alerts.go`, API
`internal/manager/api/alerts.go`, web `web/src/lib/features/alerts` and the
bell (`$lib/shell/notices.svelte.ts`).

- **One alert per problem.** Evaluators describe what they see as an
  `alerts.Observation` and call `raise`/`resolve`; never write the
  `alerts` table another way. The dedupe key is unique while firing
  (`alerts_firing_key`). A message is written only on fire, a higher
  severity or a new fingerprint token (`domain.NewTokens`), and on
  resolution `resolved` for channels with `sendResolved`; everything else
  (progress, counters, a new job of the same key) updates quietly. Put
  what gets worse in the fingerprint (tokens), what only describes it in
  the facts.
- **The outbox is the only way out.** Raise/resolve and their
  `alert_deliveries` rows share one transaction; only the dispatcher
  calls `notify.Service.Send`, in order per channel, with backoff (30 s
  to 1 h, given up after 24 h), coalescing bursts into one digest.
  Delivery is at least once. Channels are chosen with
  `NotificationChannel.Wants` when the message is written and checked
  again before sending (deleted, disabled or unsubscribed: dropped).
- **Nothing secret, nothing raw.** Titles, facts, `Detail` and messages
  never hold serial numbers, job error messages or recovery texts,
  addresses or other secrets; job alerts keep the error class only. Tests
  seed canaries (a disk serial, a job error) and scan messages, alerts
  and logs.
- **Hooks never fail the job.** Job finish hooks (`job_failed` on every
  kind, `updates_available` on `update.check`) write in a savepoint of
  the job's transaction, log their own failures and publish only after
  the commit (`OnChange`).
- **Startup and moves.** Offline grace runs from
  `max(connection_changed_at, service start)` (no alert storm after a
  restart); the reconcile and dispatch loops do nothing while the move
  lock is read-only or stronger.
- **Reading and dismissing.** No read key: `authz.AlertVisible` (the
  source's permission) filters lists, gets and the `alert.updated` event;
  `alert.dismiss` is checked with `authz.AlertDismissible` (scoped like
  the source). A dismissal is instance-wide, recorded on the alert and
  audited; getting worse clears it. A new kind needs its rule in
  `authz/alerts.go`, an evaluator with a dedupe key, `Link` and `Detail`
  in `alerts/message.go`, the event kind (above) and the user docs.
- **Retention:** resolved alerts 90 days, finished deliveries 7 days
  (`Service.Purge`, every reconcile).
- **Configuration:** `DOCKER_MANAGER_ALERT_OFFLINE_GRACE` (5m, 1m–24h).
