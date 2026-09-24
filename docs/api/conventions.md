# Public API conventions (`/api/v1`)

The contract is code-first: Huma operations in `internal/manager/api` generate
OpenAPI 3.1, served at `/api/v1/openapi.json` and `/api/v1/openapi.yaml` and
committed as `api/openapi.json`. The web client types are generated from that
file. The endpoint catalog lives in issue #4.

## Paths and operation metadata

- Every route is under `/api/v1`. Environment-scoped Docker resources nest
  under `/api/v1/environments/{environmentId}/…`.
- Operations are registered with `api.Register` only. Each declares:
  - `OperationID` — kebab-case, unique, stable (`get-stack`, `list-stacks`,
    `create-stack-deployment`). Renaming one is a breaking change.
  - `Capability` — `public`, `authenticated`, or a dotted capability key from
    the #17 catalog (`container.restart`). Emitted as `x-dockyard-capability`.
  - `Scope` — `none` (only with public/authenticated), `instance`,
    `environment` or `resource`. Emitted as `x-dockyard-scope`.
  - `Summary` (and ideally `Description` and `Tags`).
- `TestOpenAPICompleteness` fails the build if any operation lacks these or
  duplicates an operation ID.

## Errors

Every error — Huma validation, handler errors, unknown routes (404/405), panics
and the `/agent/v1` placeholder — uses one shape:

```json
{
  "code": "validation_failed",
  "message": "validation failed",
  "details": [{ "field": "body.name", "message": "expected length >= 1" }],
  "requestId": "4f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60",
  "retryable": false
}
```

- `code` is stable snake_case; clients switch on it. `message` is for humans.
- `details` is always an array (possibly empty). `field` is the input
  location: `body.<json path>`, `query.<name>`, `path.<name>`, `header.<Name>`,
  or `check.<name>` for readiness checks.
- `requestId` equals the `X-Request-ID` response header and the `request_id`
  in manager logs.
- `retryable` is true for 429, 503, 502, 504 and 408 by default.
- 5xx responses never include internal error text; causes are logged.

**Content type:** `application/problem+json`. The body is a valid RFC 9457
problem document that uses only extension members (`type` is implicitly
`about:blank`), so generic problem-details tooling accepts it while DockYard
clients parse the five members above. We did not adopt RFC 9457's
`title/status/detail/errors` members to keep one small, stable shape.

Default codes by status: 400 `bad_request`, 401 `unauthenticated`,
403 `forbidden`, 404 `not_found`, 405 `method_not_allowed`, 409 `conflict`
(prefer a specific code such as `stack_name_taken`), 412
`precondition_failed`, 413 `payload_too_large`, 422 `validation_failed`,
428 `precondition_required`, 429 `rate_limited`, 500 `internal`,
503 `unavailable` / `not_ready`. Helpers: `api.NotFound`, `api.Invalid` +
`api.Field`, `api.Conflict`, `api.PreconditionFailed`, `api.RateLimited`,
`api.Unavailable`, `api.Internal`, `api.NewError`.

## Lists and pagination

List responses are `api.Page[T]`: `{"items": [...], "nextCursor": "…", "total": 12}`.
Inputs embed `api.PageParams` (`?cursor=&limit=`, default 50, max 200).
Cursors are opaque (`api.EncodeCursor`/`api.DecodeCursor`), encode the sort
key of the last item (never an offset) and are absent on the last page.
`total` is optional and counts only items the caller may see. Filter/sort
conventions: TODO(#4).

## Edits, retries and long operations (to be completed by #4)

- Revisioned resources return an `ETag`; edits embed `api.IfMatchParam` and
  get 412 `precondition_failed` on mismatch.
- Dangerous retries embed `api.IdempotencyKeyParam` (`Idempotency-Key`).
- Long operations return `202 Accepted` with a job ID and URL (#26).

## Caching and headers

All `/api/v1` and `/agent/v1` responses carry `Cache-Control: no-store`.
Every response carries `X-Request-ID`, `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY` and a CSP with
`frame-ancestors 'none'`.

## System endpoints

| Route | Operation | Purpose |
| --- | --- | --- |
| `GET /api/v1/health` | `get-health` | Liveness: `status`, `version`, `commit`. |
| `GET /api/v1/health/ready` | `get-health-ready` | Readiness: DB reachable and migrations applied; 503 `not_ready` otherwise. |
| `GET /api/v1/capabilities` | `get-capabilities` | Manager version, API version, agent protocol version, feature flags. |
