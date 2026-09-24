# Errors (`/api/v1`)

Every error response of the public API — handler errors, request
validation, unknown routes (404/405), content negotiation, panics — has the
same body, media type `application/problem+json`:

```json
{
  "code": "precondition_failed",
  "message": "the resource was changed since you loaded it",
  "details": [
    {
      "field": "header.If-Match",
      "message": "stale revision; reload the resource, reapply your change and retry with its current ETag"
    }
  ],
  "requestId": "4f1c2e7a9b0d4c3e8f6a1b2c3d4e5f60",
  "retryable": false
}
```

| member | type | meaning |
| --- | --- | --- |
| `code` | string | Stable snake_case code from the catalog below. **Switch on this.** |
| `message` | string | Human-readable summary. Not stable; do not parse or match it. |
| `details` | array | Always present (possibly empty). Each `{field, message}` names an input location: `body.<json path>` (e.g. `body.services[0].image`), `query.<name>`, `path.<name>`, `header.<Name>`, or `check.<name>` for readiness checks. |
| `requestId` | string | Same value as the `X-Request-ID` response header and the manager's `request_id` log field. Quote it in bug reports. |
| `retryable` | boolean | `true` when repeating the *identical* request later may succeed. Honour `Retry-After` when present. |

The body is a valid RFC 9457 problem document that uses only extension
members (`type` is implicitly `about:blank`), so generic problem-details
tooling accepts it. No other members are ever added; new information goes into
new codes or `details`.

Rules clients can rely on:

- **5xx never contain internal text.** The cause is logged under the request
  ID; the body says `internal server error`.
- **Existence does not leak.** A resource the caller may not see is `404
  not_found`, the same as a missing one. `403 forbidden` is used only when the
  caller may already know the resource exists (for example it can read a job
  but not cancel it). List routes filter instead of failing.
- **Headers carry machine-readable extras:** `ETag` on `412` (the current
  revision), `Retry-After` whenever the server knows a delay (always on
  `409 idempotency_key_in_flight`; on `429` and `503` when rate limits and
  maintenance windows set one),
  `Allow` on `405`; bearer-token requests will also get `WWW-Authenticate:
  Bearer` on `401` once tokens exist (#31).
- **Validation is exhaustive:** a `422` lists every invalid field it found, not
  only the first.
- Streams report errors in-band once established; see [streams.md](streams.md).
  Agent protocol errors are separate: [agent-v1.md](../protocol/agent-v1.md).

## Code catalog

Source of truth: `ErrorCodes()` in `internal/manager/api/errorcodes.go`.
`TestErrorCodesCatalogued` fails when code in the API package returns a code
missing from the catalog, and `TestErrorCatalogDocumented` fails when this
table and the catalog disagree. Codes are never renamed or reused; feature
workstreams add their specific codes (e.g. `stack_name_taken`) to both in the
same change.

| code | status | retryable | meaning | added by |
| --- | --- | --- | --- | --- |
| `bad_request` | 400 | no | The request is malformed (unparsable JSON, wrong content encoding). | #2 |
| `unauthenticated` | 401 | no | No valid session cookie or API token; sign in again or send a valid bearer token. | #2 |
| `forbidden` | 403 | no | Authenticated, but the named capability is not granted for this resource. Returned only when the caller may know the resource exists; otherwise `not_found`. | #2 |
| `not_found` | 404 | no | The resource or route does not exist, or the caller may not know that it exists. | #2 |
| `method_not_allowed` | 405 | no | The route exists but not with this method; see the `Allow` header. | #2 |
| `not_acceptable` | 406 | no | The `Accept` header excludes every media type the route can produce. | #2 |
| `conflict` | 409 | no | Generic conflict with the current state. Routes prefer a specific 409 code. | #2 |
| `job_finished` | 409 | no | The job already reached a terminal state (for example a cancellation of a finished job). | #26 |
| `idempotency_key_reused` | 409 | no | The `Idempotency-Key` was already used by this caller for a different request (different route, parameters or body). | #26 |
| `idempotency_key_in_flight` | 409 | yes | A request with the same `Idempotency-Key` is still being processed; retry after the `Retry-After` delay. | #4 |
| `gone` | 410 | no | The resource existed but was removed permanently (for example an expired invitation). | #2 |
| `precondition_failed` | 412 | no | `If-Match` does not name the current revision. The response carries the current `ETag`; refetch, merge and retry. | #4 |
| `payload_too_large` | 413 | no | The request body exceeds the route's documented limit. | #2 |
| `unsupported_media_type` | 415 | no | The `Content-Type` is not accepted by the route. | #2 |
| `validation_failed` | 422 | no | One or more inputs are invalid; `details` lists each field. | #2 |
| `precondition_required` | 428 | no | The edit requires an `If-Match` header with the resource's current `ETag`. | #4 |
| `rate_limited` | 429 | yes | Too many requests; retry after the `Retry-After` delay. | #2 |
| `internal` | 500 | no | Unexpected server error. The cause is logged under the request ID and never returned. | #2 |
| `not_implemented` | 501 | no | The route is declared but this manager build does not implement it yet. | #2 |
| `job_kind_unavailable` | 501 | no | The operation would start a job kind whose executor this manager or agent does not provide yet. | #26 |
| `unavailable` | 503 | yes | A dependency (database, job engine, agent) is temporarily unavailable. | #2 |
| `not_ready` | 503 | yes | Readiness check failed; `details` lists each failing check. | #2 |
| `timeout` | 504 | yes | The operation did not finish within its deadline (also `408` for slow request bodies). | #2 |

`502 Bad Gateway` maps to `unavailable` and `408 Request Timeout` to `timeout`
(`CodeForStatus`); both are retryable.

### Job failures are not HTTP errors

A request that *starts* a job succeeds with `202` even if the job later fails.
The job's outcome is in the `Job` resource: `state` plus `error {class,
message, recovery}` with stable classes `agent_offline`,
`authorization_revoked`, `step_failed`, `unknown_outcome`, `journal_lost`,
`resume_limit`, `rejected`, `compensation_failed`, `executor_restarted`,
`cancelled`, `internal` (see [job-engine.md](../architecture/job-engine.md)).

## Client handling guide

| situation | do |
| --- | --- |
| `401 unauthenticated` | Browser: send the user to sign-in and drop cached data. Token client: the token is invalid, expired or revoked. |
| `403 forbidden` | Hide/disable the action; permissions changed (the live stream sends `permissions.changed`, #23). |
| `404 not_found` after a permission change | Treat as gone; remove it from caches. |
| `409 idempotency_key_in_flight` | Wait `Retry-After` seconds and resend the identical request with the same key. |
| `409 idempotency_key_reused` | A bug in the client: a new logical request needs a new key. |
| `412 precondition_failed` | Fetch the resource (or use the returned `ETag`), show the user the difference, retry with the new `If-Match`. Never overwrite silently. |
| `428 precondition_required` | Send `If-Match` with the `ETag` from the last GET. |
| `422 validation_failed` | Map `details[].field` to form fields. |
| `retryable: true` | Retry with exponential backoff (start 1 s, cap 30 s, jitter), honouring `Retry-After`; only for idempotent requests or requests with an `Idempotency-Key`. |
