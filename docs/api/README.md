# DockYard API

Everything the DockYard web UI does, it does through this API; there are no
UI-only privileged endpoints and no Docker Engine API proxy (#4). An
independent client (script, CLI, integration) uses exactly the same routes
with an API token.

| document | contents |
| --- | --- |
| [conventions.md](conventions.md) | paths, JSON rules, pagination/filter/sort/total, ETag/If-Match, idempotency keys, 202 + job, caching |
| [errors.md](errors.md) | the error shape and the catalog of stable error codes |
| [streams.md](streams.md) | SSE, WebSocket and binary stream contracts: live invalidation, jobs, logs, exec, files |
| [versioning.md](versioning.md) | compatibility rules and the breaking-change check |
| [`api/openapi.json`](../../api/openapi.json) | the OpenAPI 3.1 contract (generated; also served at `/api/v1/openapi.json` and `.yaml`) |
| [`api/route-inventory.yaml`](../../api/route-inventory.yaml) | every route of the v1 catalog with capability, scope, owning issue and status |
| [agent-v1.md](../protocol/agent-v1.md) | the private manager ↔ agent protocol (not for clients) |

## Base URL

One public origin behind the operator's TLS reverse proxy (#27), for example
`https://docker.example.com`. The API lives under `/api/v1`; the PWA under
`/`; agents use `/agent/v1`. Browsers must use HTTPS (Secure cookies,
passkeys, the service worker).

## Authentication

The OpenAPI document declares two security schemes; every non-public
operation accepts either (`security: [{cookieSession: []}, {bearerToken: []}]`),
public operations have none.

### `cookieSession` — browsers (#16)

- `POST /api/v1/auth/session` signs in (password, then TOTP / passkey /
  recovery code as required) and sets the cookie `__Host-dockyard_session`:
  `HttpOnly; Secure; SameSite=Strict; Path=/`, no `Domain` (pinned to the one
  origin). `DELETE /api/v1/auth/session` signs out. Passkeys sign in
  without a username (`/auth/passkeys/authentication-options` →
  `/authentication-verifications`). The session state is
  `second_factor_required` (send one of `factors`), `enrollment_required`
  (a limited session that may only use `/me`, `/me/…` and `/auth/…` to
  enroll the factors the instance policy requires; everything else answers
  `403 enrollment_required`) or `authenticated`. The token changes at every
  privilege change; sessions end after 1 h idle / 24 h.
- **Cross-site protection:** requests with unsafe methods authenticated by
  the cookie must come from the manager's own origin — the manager checks
  `Origin` (and `Sec-Fetch-Site`) against `DOCKYARD_PUBLIC_URL` and rejects
  others with `403 cross_origin_request` (Go's `CrossOriginProtection`). WebSocket upgrades are checked the same way.
- **Step-up:** sensitive changes (security settings, permissions, passwords,
  factor removal, Recovery Key flows) additionally require a recent
  re-authentication (`POST /api/v1/auth/step-ups`, valid 10 minutes; a
  fresh sign-in counts); the route answers `403 step_up_required`
  otherwise (#16).
- Session and permission changes invalidate open streams (#23).

### `bearerToken` — scripts and integrations (#31)

- `Authorization: Bearer <token>`. Tokens are created in the UI or with
  `POST /api/v1/me/api-tokens`; the value is shown once. Tokens expire, can
  be revoked, and carry a chosen subset of the owner's capabilities: each
  request is authorized as *token scope ∩ the owner's current effective
  permissions*.
- Bearer requests are not subject to cookie CSRF checks; never send a token
  from a browser page.
- Cookie-only routes (`/auth/session`, `/auth/step-ups`, passkey and TOTP
  enrollment, owner security settings) reject bearer tokens.

Sessions are implemented (#16); API tokens arrive with #31 (until then a
bearer request has no principal and non-public routes answer `401`).
Authorization (#17, [authorization](../architecture/authorization.md)):
the instance owner may use every route; everyone else gets exactly what
their group rules and user overrides grant (`403`/`404`, lists filtered),
and a new account in the initial **Restricted** group sees nothing.

### Public routes

Liveness/readiness (`/health`, `/health/ready`), `/capabilities`,
`/setup/status`, sign-in and redemption flows, and the protected first-run
setup routes (which additionally require the setup session and HTTPS and are
refused once an owner exists).

## Authorization metadata in OpenAPI

Every operation carries DockYard extensions (checked by
`TestOpenAPICompleteness` and reconciled by `TestRouteInventory`):

| extension | values | meaning |
| --- | --- | --- |
| `x-dockyard-capability` | `public`, `authenticated`, `owner`, a key such as `container.restart`, or a selector such as `stack.{action}` | what the caller needs. `owner` surfaces (users, groups, invitations, security policy, credential administration) are never delegable (#16, #17). |
| `x-dockyard-capability-values` | list of keys | for a selector: the concrete capabilities; the request body picks one (e.g. `action: restart` needs `stack.restart`) |
| `x-dockyard-scope` | `none`, `instance`, `environment`, `resource` | where the capability is evaluated: nowhere (public/authenticated), manager-wide, on the environment in the path or body, or on the individual resource (and, for lists, per item) |
| `x-dockyard-idempotency` | `stored`, `job` | the operation honours `Idempotency-Key` ([conventions](conventions.md#retries-and-idempotency-keys)) |
| `x-dockyard-audit` | an action key such as `stack.deploy` or `invitation.create` | every call is recorded in the audit trail under this action (every non-GET operation, and GET operations such as downloads and exports; #30, [audit](../architecture/audit.md)) |

Capability keys come from the permission catalog (`GET
/api/v1/permission-catalog`, #17). A client can show or hide actions from
`GET /api/v1/me/permissions`, but the server decides on every request.

## Building a client

- **TypeScript:** the web UI generates types with `openapi-typescript`
  (`bash scripts/generate.sh` → `web/src/lib/api/schema.d.ts`) and calls the
  API through `openapi-fetch`. Any OpenAPI 3.1 generator works for other
  languages.
- Send `Accept: application/json`; parse errors as
  [`Error`](errors.md) (`application/problem+json`) and switch on `code`.
- Send `X-Request-ID` if you have a correlation ID; it is echoed and logged.
- Retry only idempotent requests or requests with an `Idempotency-Key`, with
  backoff and `Retry-After`.
- Treat IDs and cursors as opaque strings; tolerate new fields and enum
  values ([versioning.md](versioning.md)).
- Use `If-Match` for every edit; handle `412` by refetching.
- For long operations expect `202` + `Location` and follow the job.
- For live UIs keep one `/api/v1/live/stream` per tab and refetch what it
  invalidates ([streams.md](streams.md)).

## Discovering the manager

`GET /api/v1/capabilities` (public) returns the manager version, the API
version (`v1`), the agent protocol version and stable feature flags. Clients
use feature flags, not version comparisons, to enable optional behaviour.
