# ADR 0004: Outgoing notifications through Shoutrrr

- Status: accepted
- Date: 2026-09-30
- Issues: #142 (notification channels); supersedes #25 decision 6 ("email
  and webhooks are post-v1") for outbound notifications; builds on ADR 0001

## Context

The owner wants Docker Manager to tell people about problems (disk health,
RAID, offline environments, failed jobs, available updates) where they
already are: Discord, Slack, Microsoft Teams, Telegram, email, ntfy,
Gotify, Pushover, Matrix, a webhook of their own. #25 decision 6 had put
email and webhooks after v1; #142 brings outbound notifications (never
inbound mail or account email) into scope. Each service speaks its own API
with its own credentials; writing and maintaining a dozen clients is not
Docker Manager's business, and the address of each destination has to be
stored safely and shown to the owner again.

## Decisions

### Library

All versions are pinned exactly in `go.mod`. `scripts/build-static.sh`
fails unless the manager binary links exactly this version and the agent
links none of it (the manager-only module list it shares with ADR 0003's
libraries). `scripts/license-check.sh` and govulncheck are manual reviews
before a bump (neither runs in CI).

| Module | Version | License | Owns |
| --- | --- | --- | --- |
| `github.com/nicholas-fedor/shoutrrr` | v0.21.1 (pre-v1: bump deliberately, rerun the `internal/manager/notify` tests) | MIT | one URL per destination (`discord://…`, `smtp://…`, `ntfy://…`, `generic+https://…`, ~35 services) and the per-service send clients |
| `github.com/eclipse/paho.golang` | v0.23.0 (Shoutrrr's MQTT service) | EPL-2.0 or EDL-1.0, used under **EDL-1.0** (BSD-3-Clause; a reviewed entry in `license-check.sh`) | MQTT client |
| `mellium.im/xmpp`, `sasl`, `xmlstream`, `reader` | v0.23.0, v0.3.2, v0.15.4, v0.1.0 (Shoutrrr's XMPP service) | BSD-2-Clause | XMPP client |
| `github.com/gorilla/websocket` | v1.5.4-0.20250319132907-e064f32e3674 (through Paho) | BSD-2-Clause | WebSocket transport of MQTT |

Shoutrrr is the maintained fork used by Watchtower and Beszel. Its router
imports every service, so "Other (Shoutrrr URL)" accepts any scheme it
supports; the web dialog offers friendly fields for ten of them.
`golang.org/x/net` moved from v0.58.0 to v0.59.0 with it.

### How Docker Manager uses it

`internal/manager/notify` is the only importer. It does not use Shoutrrr's
router to send:

- **Locate by hand**: the service is created from the URL's scheme, the
  adapter's HTTP client and TCP dialer are injected *before and after*
  `Initialize` (Matrix signs in while initializing), then `Send` or
  `SendContext` runs under the send's context.
- **One HTTP client per send**: 15 s timeout (connect, TLS, answer), no
  keep-alive, proxy from the environment as usual, at most 3 same-scheme
  redirects and **no cross-scheme redirect** (refused, class `redirect`).
  SMTP uses the same dialer.
- **No destination restrictions** (owner decision): LAN, loopback,
  internet and SMTP servers are all allowed; there is no IP blocking.
- **Errors are classes**: Shoutrrr's error texts and log lines can contain
  the URL (tokens, passwords). Its logger discards everything; a send is
  reduced to `dns`, `connect`, `tls`, `timeout`, `auth`, `http_4xx`,
  `http_5xx`, `redirect`, `rejected` or `invalid_url` from what the adapter
  saw on the wire (DNS, dial and TLS errors, refused redirects, the last
  HTTP status) and, for SMTP, the kind of error. Only the class and a
  sentence in words leave the package. Panics inside a service are
  recovered (`rejected`).
- **Validation without sending**: creating or changing an address parses
  it, checks the scheme and initializes the service with a client and
  dialer that refuse every connection (a service that tries to connect
  while initializing counts as valid).

### The address is a secret, revealable to the owner

The address carries the credentials. It is sealed with the
secret-protection key (`notification_channels/<id>/url`, keyed
fingerprint, version bumped on every change), never logged, audited, put
in a job input, returned by list or get, repeated in an error or stored in
the last result. The owner can read it again (`GET
…/notification-channels/{id}/address`, recent step-up, every reveal
audited without the value) so the dialog can show and edit it. The audit
redaction layer also recognizes Shoutrrr URLs, Discord and Slack webhook
URLs, Telegram bot tokens and signed webhook URLs.

## Consequences

- The manager binary grows by the Shoutrrr services (MQTT, XMPP and ~30
  HTTP clients); the agent is unchanged.
- A later alerts feature sends through `notify.Service.Send` and the stored
  subscription (event kinds, environments, resolved messages) without a
  migration.
- A Shoutrrr bump can change URL formats: the web's `services.ts`
  build/parse spec and the notify tests must pass before it ships.
