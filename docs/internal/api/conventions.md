# Public API conventions (`/api/v1`)

How every operation behaves, for client authors and for the workstreams that
add operations. Overview and authentication: [README.md](README.md).
Errors: [errors.md](errors.md). Streams: [streams.md](streams.md).
Compatibility: [versioning.md](versioning.md).

The contract is code-first: Huma operations in `internal/manager/api`
generate OpenAPI 3.1, served at `/api/v1/openapi.json` and
`/api/v1/openapi.yaml` and committed as `api/openapi.json`. The web client
types are generated from that file. Every route of the #4 catalog is listed
in `api/route-inventory.yaml` with its operation ID, capability, scope,
owning issue and status; every v1 route is served (`TestV1CatalogIsServed`).

## Paths and operation metadata

- Every route is under `/api/v1`. Environment-scoped Docker resources nest
  under `/api/v1/environments/{environmentId}/…`; stacks, jobs, policies,
  backups and other manager resources are top level. Path segments are
  lower-kebab plural nouns; parameters are `{camelCase}` IDs.
- Resources are addressed by stable Docker Manager IDs (UUIDv7 strings; treat them
  as opaque). Docker objects keep their Engine IDs/names below their
  environment. Responses never expose raw host filesystem paths or
  credential values.
- Actions that are not CRUD are modelled as creating a sub-resource
  (`POST /jobs/{jobId}/cancellations`, `POST /stacks/{stackId}/deployments`);
  container verbs keep the Docker names (`POST …/containers/{id}/restart`).
- Operations are registered with `api.Register` only. Each declares:
  - `OperationID` — kebab-case, unique, stable (`get-stack`, `list-stacks`,
    `create-stack-deployment`). Renaming one is a breaking change.
  - `Capability` — `public`, `authenticated`, `owner`, a dotted capability
    key from the #17 catalog (`container.restart`), or a selector like
    `stack.{action}` with `CapabilityValues` when the body picks the
    capability. Emitted as `x-docker-manager-capability` (and
    `x-docker-manager-capability-values`).
  - `Scope` — `none` (public/authenticated), `instance` (owner and
    manager-wide), `environment` or `resource`. Emitted as `x-docker-manager-scope`.
  - `Idempotency` — `stored` or `job` when the input takes an
    `Idempotency-Key` (below). Emitted as `x-docker-manager-idempotency`.
  - `Audit` / `AuditAction` — every non-GET operation is recorded in the
    audit trail (#30) by `Register`, with no opt-out; GET operations opt in
    with `Audit: AuditAlways` (downloads, exports). The action is the
    capability key, or for pseudo-capabilities a key derived from the
    operation ID (`create-invitation` → `invitation.create`) unless
    `AuditAction` overrides it. Emitted as `x-docker-manager-audit`. See
    [audit](../architecture/audit.md).
  - `Summary` (and ideally `Description`, `Tags`, `Errors`).
- `Register` adds the security requirements (cookie or bearer) and a `401`
  to every non-public operation, and panics at startup on missing or
  inconsistent metadata. `TestOpenAPICompleteness` checks the generated spec;
  `TestRouteInventory` reconciles it with the inventory;
  `TestEveryCatalogedMutatingRouteIsAudited` proves every mutating catalog
  route records an audit event.

## Examples in the OpenAPI document

- Every JSON request body and every `2xx` JSON response has a complete
  media-type `example`. It is assembled from the field examples the DTOs
  declare (`example:"…"` struct tags), then `const`/`default`/`enum`
  values, then neutral values that satisfy the field's constraints. Request
  examples contain the required members plus the optional ones that declare
  an example.
- Every documented error response (`4xx`/`5xx`, `application/problem+json`)
  references `#/components/examples/Error<status>` (for example `Error412`),
  an [`Error`](errors.md) with the generic code of that status.
- `TestEveryBodyHasAnExample` validates each example against its schema and
  fails when a body declares no field example of its own: a new DTO needs
  at least one `example` tag.

## JSON

- `application/json`, UTF-8, camelCase member names. Unknown request members
  are rejected (`422`).
- Optional members that do not apply are **omitted**, not `null`. Lists are
  always present (`[]`, never `null`).
- Timestamps are RFC 3339 in UTC (`2026-09-24T12:00:00Z`); durations are
  integers with a unit suffix in the name (`timeoutSeconds`) unless
  documented as Go duration strings; sizes are bytes (`int64`).
- Enumerations are lowercase snake_case strings. Clients must tolerate new
  enum values in responses (show them as "unknown").

## Lists and pagination

List responses are `api.Page[T]`:

```json
{ "items": [ … ], "nextCursor": "eyJxIjoi…", "total": 12 }
```

- **Cursor pagination:** `?cursor=&limit=` (`api.PageParams`; default 50,
  max 200). `nextCursor` is absent on the last page; follow it until then.
  Cursors are opaque, encode the sort key of the last item (never an
  offset), stay valid while items are inserted or deleted, and are bound to
  the filters and sort they were issued for — changing filters with an old
  cursor is `422` on `query.cursor` (`api.CursorFor`/`api.DecodeCursorFor`).
- **Pages may be short:** items are filtered per item by the caller's
  permissions (#17), so a page can hold fewer than `limit` items (even zero)
  while `nextCursor` is present. The server bounds the work per request
  (`api.ScanPage`).
- **Filters** are query parameters named after the item field
  (`?state=running&state=failed&environmentId=…`). Repeating a parameter ORs
  its values; different parameters AND. Free-text search is `?q=`. Each route
  documents its filters in OpenAPI; unknown values are `422`.
- **Sort:** `?sort=-createdAt,name` (`api.SortParam`, `api.ParseSort`); a
  leading `-` sorts descending. Each route documents its sortable fields and
  default order, and breaks ties by ID so the order is total.
- **`total`** is present only on routes that document it. It counts the
  items matching the filters **that the caller may see** — never a raw count,
  so aggregates do not leak (#17). Routes whose permission filtering makes
  counting expensive omit it (audit) or send it only when it is exact
  (jobs: always for the instance owner, for other callers when at most
  1000 jobs match the filters; absent otherwise).

## Edits and revisions (ETag / If-Match)

Revisioned resources (stacks, policies, settings, files, groups, …) carry a
`revision` in the body and a strong `ETag` header (`api.ETagHeader`,
`api.RevisionETag`).

- Edits (`PATCH`, `PUT`, `DELETE` of revisioned resources) require
  `If-Match: <ETag>` (`api.IfMatchParam`, `in.CheckIfMatch(current)`):
  - missing → `428 precondition_required`;
  - stale → `412 precondition_failed`, with the **current `ETag`** in the
    response header; the client refetches, merges and retries;
  - `*` matches any existing version; weak tags never match.
- A successful edit returns the new representation and its new `ETag`.
- Handlers compare and write in one transaction (or compare-and-swap on the
  revision) so concurrent edits cannot both pass.
- File saves use the file's content ETag the same way; an external change
  made outside Docker Manager also changes it (#15, #23).

## Retries and idempotency keys

Safe methods (`GET`) and `PUT`/`DELETE` are idempotent by definition. Other
dangerous operations accept `Idempotency-Key: <1–128 chars of [A-Za-z0-9._:-]>`
(`api.IdempotencyKeyParam`); clients generate a fresh random key (a UUID)
per logical request and reuse it for every retry of that request. Keys are
scoped to the caller (user or API token) and the operation, and remembered
for 24 hours.

- **Operations that start jobs** (`x-docker-manager-idempotency: job`): the job
  engine stores the key with the job (#26). A retry with the same key and
  the same request returns the **existing job** (`202`, same job ID), also
  after it finished. The same key with a different request is
  **`409 idempotency_key_reused`**.
- **Other dangerous operations** (`x-docker-manager-idempotency: stored`, for
  example creating an enrollment token, invitation or API token, or
  rotating a credential): the manager reserves the key before running the
  request, stores the first `2xx` response (status, `Content-Type`,
  `Location`, `ETag`, body — sealed at rest because it may contain a one-time
  secret) and **replays** it for retries with the header
  `Idempotent-Replayed: true`. While the first request is still running a
  retry gets `409 idempotency_key_in_flight` (retryable, `Retry-After`).
  A non-`2xx` answer is not stored, so a retry runs again. A different
  request under the same key is `409 idempotency_key_reused`. Stored
  responses of a principal are dropped when its sessions, token or
  permissions change.
- The request fingerprint is method, path, sorted query, `Content-Type` and
  the exact body bytes: send byte-identical retries.

## Long operations: 202 + job

Operations that start work in the job engine (#26) answer **`202
Accepted`** with the `Job` as body and its URL in `Location`
(`/api/v1/jobs/{jobId}`); `api.Register` enforces that `202` is used exactly
by operations returning `api.JobAccepted` (`api.Accepted(job)`).

- Follow progress with `GET /api/v1/jobs/{jobId}` or the SSE stream
  `GET /api/v1/jobs/{jobId}/events/stream`; the live stream (#23) announces
  job state changes.
- A job exposes `state`, `progress`, `items`, `error {class, message,
  recovery}`, `blockedBy`, `locks`, `attempt`, `origin`, timestamps and
  `cancellable`. Cancel with `POST /api/v1/jobs/{jobId}/cancellations`
  (`202`; honoured at the kind's next safe point; compensations always run).
- A conflicting job is queued as `blocked` (with `blockedBy`), not rejected.
- Job-starting operations accept `Idempotency-Key` (see above).

## Authorization shaping

- Every operation, list item, aggregate, stream event and job has a named
  capability (#17). Lists and counts contain only what the caller may see;
  single resources the caller may not know about are `404`.
- A grant for one action on a resource exposes only the minimal identity and
  status fields needed to find it and run that action (for example
  restart-only shows name/state and the restart action, not config or logs).
  Resource objects carry `view` (`minimal` or `full`) and `actions` (the
  granted capability keys); fields outside the minimal view are absent, and
  the revision/ETag is present only in the full view or with an edit action
  ([authorization](../architecture/authorization.md#response-shaping-minimal-discovery)).
- Scheduled jobs run as the manager service identity; the initiating user of
  a manual job is audit metadata, never an access-control owner.

## Caching and headers

- All `/api/v1` and `/agent/v1` responses carry `Cache-Control: no-store`
  (JSON, errors, SSE, downloads). The PWA service worker caches only
  versioned static assets and the offline shell (#23).
- Every response carries `X-Request-ID` (an inbound well-formed one is
  echoed, #27), `X-Content-Type-Options: nosniff`, `Referrer-Policy:
  no-referrer`, `X-Frame-Options: DENY` and a CSP with
  `frame-ancestors 'none'`.
- Rate-limited answers are `429 rate_limited` with `Retry-After`.

## System endpoints

| Route | Operation | Purpose |
| --- | --- | --- |
| `GET /api/v1/health` | `get-health` | Liveness: `status`, `version`, `commit`. |
| `GET /api/v1/health/ready` | `get-health-ready` | Readiness: DB reachable and migrations applied; 503 `not_ready` otherwise. |
| `GET /api/v1/capabilities` | `get-capabilities` | Manager version, API version, agent protocol version, feature flags (none in v1). |
| `GET`/`PATCH /api/v1/settings` | `get-settings`, `update-settings` | Instance settings (`settings.read` / `settings.manage`): the editable display name, plus the read-only deployment configuration (public URL, trusted proxy count, stream heartbeat, upload limit, metrics endpoint). The sign-in policy (`/settings/security`, owner), schedule defaults (`/schedule-defaults`) and maintenance defaults (`/maintenance-defaults`) are separate revisioned resources. |
