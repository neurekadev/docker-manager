# Notification channels (#142)

Owner-administered outgoing destinations for Docker Manager's messages:
Discord, Slack, Microsoft Teams, Telegram, email (SMTP), ntfy, Gotify,
Pushover, Matrix, a generic webhook, or any other Shoutrrr service
([ADR 0004](../adr/0004-notification-library.md)). This feature stores and
tests channels and their subscriptions; alerts ([alerts.md](alerts.md))
send through them with `notify.Service.Send`. Binding rules:
[alerts-and-notifications.md](../conventions/alerts-and-notifications.md).

| package | role |
| --- | --- |
| `internal/domain/notification.go` | `NotificationChannel`, input and patch, event kinds, error classes, `Wants` |
| `internal/manager/store/notification_channels.go` | rows of `notification_channels` and `notification_channel_environments`; the sealed address only through `NotificationChannelWithSecret` (the channel and its address from one row read) |
| `internal/manager/notify` | service (`List`, `Get`, `Create`, `Update`, `Delete`, `Reveal`, `Test`, `Send`), the Shoutrrr adapter (`shoutrrr.go`), error classes (`classify.go`), non-secret targets (`target.go`) |
| `internal/manager/api/notifications.go` | `/api/v1/notification-channels...` routes |
| `web/src/lib/features/notifications` | the Settings → Notifications page's model, the channel dialog, per-service URL building (`services.ts`) |

## Model

Migration `20260930090000_create_notification_channels`:

- `notification_channels`: display name (unique, case-insensitive
  `name_key`), `service` (the URL's Shoutrrr service: `discord`, `smtp`,
  `generic`, …), `target` (the host of a mail, push or chat server or of a
  generic webhook, else empty: never a token), `enabled`, `event_kinds`
  (JSON array, at least one), `send_resolved`, `all_environments`, the
  sealed address
  (`secret_sealed`, context `notification_channels/<id>/url`), its keyed
  fingerprint, version and change time, `last_result` (`''`, `ok` or an
  error class), `last_attempt_at`, `last_success_at`, `revision`, times.
- `notification_channel_environments(channel_id, environment_id)`: the
  environments of a channel without `all_environments`. Both foreign keys
  cascade; a restricted channel that loses all its rows sends no
  environment's events (it never widens to every environment).

Event kinds (`domain.NotificationEventKinds`): `disk_health`, `raid`,
`environment_offline`, `job_failed`, `updates_available`. A new channel
subscribes to all of them and to resolved messages.

## Flows

- **Create** (owner, recent step-up): name, address, subscription. The
  address is validated without sending (parse, known scheme, Shoutrrr's
  `Initialize` with a client and dialer that refuse every connection),
  sealed, fingerprinted; `service` and `target` are derived from it.
- **Update** (owner, If-Match): name, enabled, event kinds, resolved
  messages, environments (`allEnvironments` true clears the list; a
  non-empty `environmentIds` restricts the channel; an emptied list is
  refused; environments archived since may stay, new ones must be active,
  checked in one query). A new address additionally needs a recent
  step-up, is validated and re-sealed, bumps the address version and
  resets `last_result`/`last_attempt_at`/`last_success_at`.
- **Reveal** (owner, recent step-up): opens the sealed address and returns
  it; the API audits every call (`notification_channel.reveal`, category
  credentials, the service as detail).
- **Test** (owner): reads the channel and its address in one row read,
  sends "Docker Manager test message" (with the public URL) through it,
  enabled or not, at most once per channel every 5 s (in memory, fake
  clock in tests; 429 `notification_test_rate_limited`), records the
  result for that address version only (a result of an address replaced
  meanwhile is dropped) and answers
  `{ok, errorClass?, message?, sentAt}`. A failed delivery is `200` with
  `ok: false`.
- **Send** (alerts): the same delivery without the owner check or the rate
  limit, whether the channel is enabled or not; the alerts dispatcher
  chooses channels with `Wants(kind, environmentID)` when it writes a
  message and checks again before it sends (a deleted, disabled or
  unsubscribed channel's messages are dropped).
- **Delete** (owner, If-Match): removes the channel, its filter rows and
  its address.

## Delivery

`deliver` locates the Shoutrrr service itself (instead of the router) so
the adapter's HTTP client and dialer are set before and after
`Initialize`; one client per send: 15 s timeout, no keep-alive, the
environment's proxy settings, at most 3 same-scheme redirects, none across
schemes. Services with `SendContext` (SMTP, XMPP) get the send's context;
the others stop at the client's timeout. A panic inside a service is
recovered. `classify` turns the outcome into a class, preferring what the
recording transport and dialer saw (DNS, TLS, dial errors, redirects, the
last HTTP status: 401/403 `auth`, other 4xx `http_4xx`, 5xx `http_5xx`)
over the error's words (SMTP); the error text never leaves the function.

## API

| operation | route | notes |
| --- | --- | --- |
| `list-notification-channels` | `GET /notification-channels` | never the address |
| `create-notification-channel` | `POST /notification-channels` | step-up; 409 `notification_channel_name_taken`; 422 `body.address` |
| `get-notification-channel` | `GET /notification-channels/{channelId}` | ETag |
| `update-notification-channel` | `PATCH /notification-channels/{channelId}` | If-Match; step-up for `address` |
| `delete-notification-channel` | `DELETE /notification-channels/{channelId}` | If-Match |
| `get-notification-channel-address` | `GET /notification-channels/{channelId}/address` | step-up; audited |
| `create-notification-channel-test` | `POST /notification-channels/{channelId}/tests` | 429 `notification_test_rate_limited` |

All are `capability: owner` (the handler checks the owner-only catalog key
`notification_channel.manage`), cookie sessions only. Changes are
published as `resource.changed` (`notification_channel`, topic
`settings`, visible to the owner).

## Web

**Settings → Notifications** (owner only): a table (name with the channel
tile and "service, target", status Working / Failing with the reason as
tooltip / Not tested / Off, what it sends, last sent) with the row menu
Send test, Edit, Delete. One dialog adds and edits: the service picker
(icons from `serviceIcons.ts`), the service's friendly fields (secrets as
`PasswordField`), the stored address masked until **Show address**, "What
to send" (event kinds, environments when there is a choice: "All
environments" is an explicit choice, the ticks list the active
environments plus any archived or removed one the filter names, an emptied
selection blocks Save; resolved messages) and **Enabled**. `services.ts` builds the Shoutrrr URL from the
fields and parses it back (exact round trip, unknown query options kept,
unreadable shapes edited as the raw URL).
