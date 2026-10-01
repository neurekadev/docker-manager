# Notification channels (#142)

Owner-administered outgoing destinations for Docker Manager's messages:
Discord, Slack, Microsoft Teams, Telegram, email (SMTP), ntfy, Gotify,
Pushover, Matrix, a generic webhook, or any other Shoutrrr service
([ADR 0004](../adr/0004-notification-library.md)). This feature stores and
tests channels and their subscriptions and renders each message for its
service; alerts and notifications ([alerts.md](alerts.md)) send through
them with `notify.Service.Send`. Binding rules:
[alerts-and-notifications.md](../conventions/alerts-and-notifications.md).

| package | role |
| --- | --- |
| `internal/domain/notification.go` | `NotificationChannel`, input and patch, event kinds and their outcomes, `NotificationSubscriptions`, error classes, `Wants`, `NotificationMessage` (status line, tone, fields with links and lists, footer, time) |
| `internal/manager/store/notification_channels.go` | rows of `notification_channels` and `notification_channel_environments`; the sealed address only through `NotificationChannelWithSecret` (the channel and its address from one row read) |
| `internal/manager/notify` | service (`List`, `Get`, `Create`, `Update`, `Delete`, `Reveal`, `Test`, `Send`), the Shoutrrr adapter (`shoutrrr.go`), rich rendering per service (`render.go`), error classes (`classify.go`), non-secret targets (`target.go`) |
| `internal/manager/api/notifications.go` | `/api/v1/notification-channels...` routes |
| `web/src/lib/features/notifications` | the Settings → Notifications page's model, the channel dialog, per-service URL building (`services.ts`) |

## Model

Migrations `20260930090000_create_notification_channels` and
`20261001090000_notification_events` (subscriptions):

- `notification_channels`: display name (unique, case-insensitive
  `name_key`), `service` (the URL's Shoutrrr service: `discord`, `smtp`,
  `generic`, …), `target` (the host of a mail, push or chat server or of a
  generic webhook, else empty: never a token), `enabled`, `subscriptions`
  (JSON object: event kind to outcomes, at least one), `all_environments`,
  the sealed address
  (`secret_sealed`, context `notification_channels/<id>/url`), its keyed
  fingerprint, version and change time, `last_result` (`''`, `ok` or an
  error class), `last_attempt_at`, `last_success_at`, `revision`, times.
- `notification_channel_environments(channel_id, environment_id)`: the
  environments of a channel without `all_environments`. Both foreign keys
  cascade; a restricted channel that loses all its rows sends no
  environment's events (it never widens to every environment).

Event kinds (`domain.NotificationEventKinds`) and their outcomes
(`Outcomes()`):

| kind | outcomes |
| --- | --- |
| `disk_health`, `raid`, `temperature`, `disk_space`, `memory` | `warning`, `critical`, `resolved` |
| `environment_offline` | `critical`, `resolved` |
| `backup` (backups and restores) | `failure`, `warning`, `success` |
| `prune` | `failure`, `success` |
| `updates` | `available` (an update check found newer images), `failure`, `success` (an update run) |
| `job_failed` (other failed jobs) | `failure`, `warning`, `resolved` |

A new channel subscribes to every outcome of every kind. The migration
turned an old channel's kinds into all their outcomes (without `resolved`
when it did not send resolved problems); a channel that had every old
kind also got the new ones.

## Flows

- **Create** (owner, recent step-up): name, address, subscription. The
  address is validated without sending (parse, known scheme, Shoutrrr's
  `Initialize` with a client and dialer that refuse every connection),
  sealed, fingerprinted; `service` and `target` are derived from it.
- **Update** (owner, If-Match): name, enabled, the subscriptions (replaced
  as a whole; outcomes in their kind's order, empty kinds dropped, at
  least one outcome), environments (`allEnvironments` true clears the list; a
  non-empty `environmentIds` restricts the channel; an emptied list is
  refused; environments archived since may stay, new ones must be active,
  checked in one query). A new address additionally needs a recent
  step-up, is validated and re-sealed, bumps the address version and
  resets `last_result`/`last_attempt_at`/`last_success_at`.
- **Reveal** (owner, recent step-up): opens the sealed address and returns
  it; the API audits every call (`notification_channel.reveal`, category
  credentials, the service as detail).
- **Test** (owner): reads the channel and its address in one row read,
  sends "Docker Manager test message" (status line "Test message", info
  tone, the channel's name and how many kinds it sends as fields, the
  public URL) through it,
  enabled or not, at most once per channel every 5 s (in memory, fake
  clock in tests; 429 `notification_test_rate_limited`), records the
  result for that address version only (a result of an address replaced
  meanwhile is dropped) and answers
  `{ok, errorClass?, message?, sentAt}`. A failed delivery is `200` with
  `ok: false`.
- **Send** (alerts and notifications): the same delivery without the
  owner check or the rate limit, whether the channel is enabled or not;
  the alerts dispatcher chooses channels with
  `Wants(kind, outcome, environmentID)` when it writes a message and
  checks again before it sends (a deleted, disabled or unsubscribed
  channel's messages are dropped).
- **Delete** (owner, If-Match): removes the channel, its filter rows and
  its address.

## Delivery

`deliver` locates the Shoutrrr service itself (instead of the router) so
the adapter's HTTP client and dialer are set before and after
`Initialize`; one client per send: 15 s timeout, no keep-alive, the
environment's proxy settings, at most 3 same-scheme redirects, none across
schemes. `render` (`render.go`) turns the message into the service's
richest form, from the address's service name and query options:

| service | form |
| --- | --- |
| `discord` | one embed in the webhook's JSON mode (the service's `Config.JSON`): the status line as the author, the title linking to the page, the body (Markdown escaped) and "Open in Docker Manager", the fields (inline ones side by side; linked values `[value](url)`; lists as `- ` entries, each linked, with its change as `` `from` → `to` ``, cut after a whole entry with "…and n more"; Discord's limits kept, the 6000 characters of the whole embed too: a field that would pass them is shown plain within the room left, a list cut after a whole entry, and a field without room is skipped while later ones may still fit), the tone's color as the strip, the footer (the instance's name) beside the logo `LogoURL` (the documentation site's, which Discord can always fetch), the time; the username and avatar only when the address sets them (the webhook's own stay otherwise); mentions disabled |
| `slack` | `color` (the tone's hex) and `title`; one line per attachment: the status line in italics, the body, `*Name:* value` fields (linked values `<url\|value>`, a list's `• ` entries on lines of their own, changes as code), `<url\|Open in Docker Manager>` |
| `teams` | `title` and `color` (`attention`, `warning`, `good`, `accent`); Markdown body |
| `smtp` | subject = title; an HTML card (a colored top bar with the status line, or the tone's word without one; title, body, a table of fields with linked values and lists, changes as `<code>`, a button, footer and time; inline styles) as the HTML part (`usehtml`, the service's `html` template), the plain text as the plain part |
| `telegram` | `parsemode=HTML`: the service bolds the title; the status line in italics, fields in bold labels (linked values, `• ` list entries, changes as `<code>`), an HTML link |
| `ntfy` | `title`, `priority` (critical 4, warning and info 3, success 2), `tags` (an emoji per tone), `click` (the link), `markdown=yes` with a Markdown body |
| `gotify` | `title`, `priority` (8, 5, 4, 4), `extras` (Markdown display, click URL) with a Markdown body |
| `pushover` | `title`, priority 1 for critical |
| `generic` | `title`, plus `tone` and `url` keys beside title and message (a JSON template gets them as data) |
| others | `title` and plain text: the status line, the body, a line "Name: value" per field (a list's entries as "- name: from → to" lines below its name), the link |

Only parameters the service has are set, and an option the address
already sets (`color`, `priority`, `parsemode`, `markdown`, `extras`,
`usehtml`, `title`, …; case-insensitive) is left to it: the body stays
plain where the owner chose another format. Tone colors are the app's
`--danger` `#fd6b66`, `--warn` `#f5b544`, `--ok` `#4cf683` and `--accent`
`#2566fd`. Services with `SendContext` (SMTP, XMPP) get the send's context;
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
Send test, Edit, Delete, and the **Alert thresholds** card (the defaults
and per-environment overrides, `PUT /alert-settings`,
[alerts.md](alerts.md#evaluators)). One dialog adds and edits: the
service picker (icons from `serviceIcons.ts`), the service's friendly
fields (secrets as `PasswordField`), the stored address masked until
**Show address**, "What to send" (a row per kind, grouped Hosts and Jobs:
a master checkbox and the kind's outcomes beside it; nothing ticked
blocks Save; environments when there is a choice: "All environments" is
an explicit choice, the ticks list the active environments plus any
archived or removed one the filter names, an emptied selection blocks
Save) and **Enabled**. `services.ts` builds the Shoutrrr URL from the
fields and parses it back (exact round trip, unknown query options kept,
unreadable shapes edited as the raw URL).
