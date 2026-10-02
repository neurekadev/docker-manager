# Logging and security defaults

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

## Logging

- `log/slog` only (JSON by default); request-scoped logger via
  `logging.FromContext(ctx)` (carries `request_id`). No `fmt.Print*` outside
  `cmd/`.
- Never log secrets, tokens, passwords, credentials, keys, file contents,
  Compose/`.env` values, request bodies or query strings. Wrap anything
  possibly sensitive in `logging.Secret`. Config structs hold secrets as
  `logging.Secret`.

## Security defaults

- Containers run as UID 0 (decided, #25/#28); do not add non-root users.
- Every response carries the baseline headers of `server.securityHeaders`
  (CSP, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`, COOP) and,
  on HTTPS requests when `DOCKER_MANAGER_PUBLIC_URL` is https,
  `Strict-Transport-Security: max-age=31536000` (#180).
- Credential checks (passwords, TOTP, recovery, invitation and reset
  codes, passkey assertions) are throttled through the auth service's
  attempt: `begin` takes the tokens before anything is verified, `fail`
  keeps them spent, the deferred `release` refunds them. Never check a
  limiter and count the failure afterwards: concurrent requests would all
  pass the check (#180). Limiter tables (`auth/throttle`) never refuse new
  clients when full: they forget a refilled bucket first and keep drained
  ones up to four times the bound before the least recently used goes.
- All `/api/v1` and `/agent/v1` responses are `no-store` (the one exception:
  template icons requested with their current `?v=<sha256>`, which are
  immutable); never cache API data in the service worker.
- Secrets at rest: `secrets.Keyring.Seal(value, "<table>/<id>/<field>")`.
  Template versions are sealed; template drafts are plain files in the
  data directory (mode 0700), like stack directories on hosts.
- Notification channel addresses (Shoutrrr URLs, #142) are secrets: sealed
  (`notification_channels/<id>/url`), never logged, audited, returned by
  list/get or kept in an error or `last_result`; send failures are error
  classes. Unlike registry and Git credentials they are **revealable**:
  the owner reads one again through the audited, step-up guarded
  `GET /notification-channels/{channelId}/address`
  ([alerts-and-notifications.md](alerts-and-notifications.md)).
- Client IP / scheme / host: `requestinfo.From(ctx)` (trusted proxies are
  resolved once; never read `X-Forwarded-*` or `RemoteAddr`). SSE goes
  through `api.StartSSE` (Huma) or `server/sse` (plain handlers; the one
  implementation), WebSockets through `server/ws`; `/agent/v1` handlers go in
  `server.Options.Agent` and reject with `server.AgentFailure`
  (`docs/internal/deployment.md`, "For contributors").
- Identity (#16, ADR 0003): `internal/manager/auth` authenticates every
  `/api/v1` request (SCS session, CSRF, principal via `authz.WithPrincipal`
  for full sessions only). Handlers read `authz.PrincipalFrom(ctx)`; the
  permission service (`internal/manager/permissions`, #17) decides.
  Identity events enrich the #30 audit record of the request (`auth.TrailAuditor`).
  Never add auth routes outside Huma, never log passwords, codes, seeds or
  tokens (canary tests in `internal/manager/app/identity*_test.go`).
  Every browser session is a signed-in device (`user_sessions`: public ID,
  IP, User-Agent, times; never the token), shown to its user and the owner.
  Session changes go through the identity service: establish/rotate record
  and keep the device, and ending a session deletes its device row (the
  next request of that session is anonymous).
- New dependencies: review them with `scripts/license-check.sh` and
  govulncheck by hand (neither runs in CI); pin exact versions. Pin workflow
  actions by commit SHA with a `# vX.Y.Z` comment.
