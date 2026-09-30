# Alerts and notifications (#142)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/notifications.md`; library decision:
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
  `store.NotificationChannelSecret` inside `notify` (a send, or `Reveal`).
  Never put it in a domain value, log, audit detail, job input, error,
  `last_result` or response other than the reveal operation; normal reads
  exclude `secret_sealed`. Tests seed `canary.NotificationURL` into the
  URL and scan logs, results, errors, responses, audit records and the
  database file.
- **Send results are classes** (`domain.NotifyErr*`, `notify.Message` in
  words, mirrored in the web's `ERROR_TEXT`): never return, log or store a
  Shoutrrr or `url.Error` text. A failed delivery is a `notify.Result` with
  `OK` false, not an error; it is recorded as the channel's `last_result`
  (no revision change). Log failures with the channel ID, service and
  class only.
- **Owner only.** Every route is `capability: owner` (never with an API
  token); handlers check the owner-only catalog key
  `notification_channel.manage` before any lookup. Creating a channel,
  changing its address and revealing it need a recent step-up
  (`RequireOwner(ctx, true)`); renaming, the subscription, enabling, tests
  and deletion need the owner. The reveal is `Audit: api.AuditAlways`.
- **Subscriptions** are stored per channel: `event_kinds` (the
  `domain.NotificationEventKinds`; never empty; a new channel gets all),
  `send_resolved`, and `notification_channel_environments` (no rows =
  every environment, future ones included). Alerts pick channels with
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
