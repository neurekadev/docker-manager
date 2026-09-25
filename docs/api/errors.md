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
  `Allow` on `405`; a bearer token that does not authenticate gets
  `WWW-Authenticate: Bearer error="invalid_token"` with its `401` (#31).
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
| `invalid_code` | 400 | no | The one-time code (invitation, password reset or owner recovery) is unknown, expired, revoked or already used. The response never says which. | #16 |
| `unauthenticated` | 401 | no | No valid session cookie or API token; sign in again or send a valid bearer token. | #2 |
| `invalid_credentials` | 401 | no | Sign-in, second factor or step-up failed: unknown account, wrong password or code, disabled account and bad passkey assertions all look alike. | #16 |
| `forbidden` | 403 | no | Authenticated, but the named capability is not granted for this resource. Returned only when the caller may know the resource exists; otherwise `not_found`. | #2 |
| `insecure_origin` | 403 | no | The request did not reach DockYard over HTTPS on DOCKYARD_PUBLIC_URL (first-run setup); the message explains how to fix the proxy or URL. | #16 |
| `cross_origin_request` | 403 | no | A browser sent an unsafe request from another origin (cross-site request forgery protection). | #16 |
| `step_up_required` | 403 | no | The change needs recent authentication; re-authenticate with POST /api/v1/auth/step-ups and retry. | #16 |
| `enrollment_required` | 403 | no | The session may only enroll the sign-in factors the instance policy requires; finish enrollment first. | #16 |
| `enrollment_expired` | 403 | no | The grace period to enroll required sign-in factors has passed; ask the instance owner for a factor or password reset. | #16 |
| `api_token_not_allowed` | 403 | no | API tokens cannot call this operation: owner administration, sign-in and factor flows, token management and Recovery Key administration need a signed-in browser session. | #31 |
| `api_tokens_disabled` | 403 | no | The instance owner disabled API tokens (security settings); no token can be created or used. | #31 |
| `sign_in_method_not_allowed` | 403 | no | The instance sign-in policy does not accept this sign-in method (for example a passkey when password and TOTP are required). | #16 |
| `not_found` | 404 | no | The resource or route does not exist, or the caller may not know that it exists. | #2 |
| `method_not_allowed` | 405 | no | The route exists but not with this method; see the `Allow` header. | #2 |
| `not_acceptable` | 406 | no | The `Accept` header excludes every media type the route can produce. | #2 |
| `conflict` | 409 | no | Generic conflict with the current state. Routes prefer a specific 409 code. | #2 |
| `job_finished` | 409 | no | The job already reached a terminal state (for example a cancellation of a finished job). | #26 |
| `idempotency_key_reused` | 409 | no | The `Idempotency-Key` was already used by this caller for a different request (different route, parameters or body). | #26 |
| `idempotency_key_in_flight` | 409 | yes | A request with the same `Idempotency-Key` is still being processed; retry after the `Retry-After` delay. | #4 |
| `setup_complete` | 409 | no | First-run setup already created the instance owner; sign in instead. | #16 |
| `username_taken` | 409 | no | Another account already uses this username. | #16 |
| `owner_protected` | 409 | no | The instance owner cannot be disabled, deleted or reset through this route (use owner recovery). | #16 |
| `factor_required` | 409 | no | Removing this factor would leave the account unable to satisfy the instance sign-in policy. | #16 |
| `totp_already_enabled` | 409 | no | TOTP is already enabled; remove it before enrolling a new secret. | #16 |
| `invitation_redeemed` | 409 | no | The invitation was already redeemed and can no longer be revoked. | #16 |
| `no_pending_flow` | 409 | no | No sign-in, TOTP enrollment or passkey ceremony is in progress in this session (or it expired); start again. | #16 |
| `engine_already_enrolled` | 409 | no | Agent enrollment: the Docker Engine already has an active agent (one agent per Engine); enroll with intent replace:<agentId> to move to the new agent. | #3 |
| `engine_identity_conflict` | 409 | no | Agent enrollment: the Engine ID is already enrolled from another host (a cloned machine?); replace the agent, regenerate the clone's Engine ID or allow the duplicate Engine ID. | #3 |
| `environment_archived` | 409 | no | The environment is archived: it cannot be edited, and enrolling its Engine needs intent reattach:<environmentId>. | #3 |
| `environment_detached` | 409 | no | Agent enrollment: the Engine belongs to an environment whose agent was removed; enroll with intent reattach:<environmentId>. | #3 |
| `engine_mismatch` | 409 | no | Agent enrollment: a replace or reattach enrollment was used for a different Docker Engine than its target's. | #3 |
| `enrollment_target_unavailable` | 409 | no | Agent enrollment: the agent to replace or the environment to re-attach is no longer in a state that allows it; create a new enrollment. | #3 |
| `agent_revoked` | 409 | no | The agent was removed or replaced; its credential cannot be rotated. | #3 |
| `group_name_taken` | 409 | no | Another permission group already uses this name. | #17 |
| `default_group_protected` | 409 | no | The default group cannot be deleted; make another group the default first. | #17 |
| `group_not_empty` | 409 | no | The group still has members; move them to another group first (users are never moved implicitly). | #17 |
| `registry_connection_name_taken` | 409 | no | Another registry connection already uses this name. | #19 |
| `ambiguous_registry_connection` | 409 | no | Several registry connections match the image equally well (same host, repository matcher specificity, binding and priority); name one explicitly (`registryId`). | #19 |
| `git_credential_name_taken` | 409 | no | Another Git credential already uses this name. | #33 |
| `ambiguous_git_credential` | 409 | no | Several Git credentials match the repository equally well (same host and path prefix length); name one explicitly (`gitCredentialId`). | #33 |
| `git_credential_revoked` | 409 | no | The Git credential selected for the repository is revoked; DockYard never falls back to anonymous access. Set a new token or select another credential. | #33 |
| `build_definition_name_taken` | 409 | no | Another build definition in this environment already uses this name. | #33 |
| `registry_connection_revoked` | 409 | no | The registry connection selected for the image is revoked; DockYard never falls back to anonymous access. Rotate a new credential into it or select another connection. | #19 |
| `stack_managed` | 409 | no | The container, volume or network belongs to a DockYard-managed stack: change the stack's Compose definition (or use the stack's operations) instead of editing or removing it directly. | #6 |
| `container_running` | 409 | no | The container is running; stop it first or remove it with `force=true`. | #6 |
| `image_in_use` | 409 | no | Containers (running or not) use the image; remove them first. | #6 |
| `volume_in_use` | 409 | no | Containers (running or not) mount the volume; remove them first. | #6 |
| `network_in_use` | 409 | no | Containers are attached to the network; disconnect or remove them first. | #6 |
| `network_builtin` | 409 | no | Predefined networks (`bridge`, `host`, `none`) cannot be removed. | #6 |
| `resource_name_taken` | 409 | no | Another container, volume or network of the environment already uses this name. | #6 |
| `unsupported_api_version` | 409 | no | The environment's Docker Engine API version is too old for the operation; upgrade Docker Engine (25.0 or newer, see the support matrix). | #6 |
| `file_exists` | 409 | no | File manager: the name already exists (choose overwrite, skip or keep both, or another name). | #15 |
| `file_conflict` | 409 | no | File manager: the entry changed during the operation or the operation would put a directory into itself. | #15 |
| `file_type_mismatch` | 409 | no | File manager: the path is a directory where a file is needed, or a path component is not a directory. | #15 |
| `file_unsupported` | 409 | no | File manager: the entry's content is not served (a symlink, device, FIFO or socket, or a file with several hard links whose other names may lie outside the root). | #15 |
| `volume_files_unsupported` | 409 | no | File manager: this volume cannot be browsed (non-local driver or remote-backed local volume, DockYard's own volumes, the stacks volume, or the agent's storage layout is not verified); the message says why. | #15 |
| `gone` | 410 | no | The resource existed but was removed permanently (for example an expired invitation). | #2 |
| `length_required` | 411 | no | Uploads need a `Content-Length` header. | #15 |
| `precondition_failed` | 412 | no | `If-Match` does not name the current revision. The response carries the current `ETag`; refetch, merge and retry. | #4 |
| `payload_too_large` | 413 | no | The request body exceeds the route's documented limit. | #2 |
| `unsupported_media_type` | 415 | no | The `Content-Type` is not accepted by the route. | #2 |
| `range_not_satisfiable` | 416 | no | The `Range` of a single-file download lies outside the file; `Content-Range` carries its size. | #15 |
| `validation_failed` | 422 | no | One or more inputs are invalid; `details` lists each field. | #2 |
| `recreate_required` | 422 | no | The requested container settings cannot change in place; create a new container (or use a Compose stack). `details` lists the fields. | #6 |
| `content_digest_mismatch` | 422 | no | The uploaded bytes do not match `X-DockYard-Content-SHA256`; nothing was written. | #15 |
| `version_unsupported` | 426 | no | Agent routes: the agent's protocol or version is outside the manager's window (same or previous minor release, never newer than the manager); upgrade as the message says. | #3 |
| `precondition_required` | 428 | no | The edit requires an `If-Match` header with the resource's current `ETag`. | #4 |
| `rate_limited` | 429 | yes | Too many requests; retry after the `Retry-After` delay. | #2 |
| `internal` | 500 | no | Unexpected server error. The cause is logged under the request ID and never returned. | #2 |
| `engine_error` | 502 | yes | The environment's Docker Engine failed the operation; the message carries its explanation. Retrying helps only when the cause was transient. | #6 |
| `not_implemented` | 501 | no | The route is declared but this manager build does not implement it yet. | #2 |
| `job_kind_unavailable` | 501 | no | The operation would start a job kind whose executor this manager or agent does not provide yet. | #26 |
| `agent_unsupported` | 501 | no | The environment's agent does not support this operation (it is older than the manager); upgrade the agent. | #6 |
| `unavailable` | 503 | yes | A dependency (database, job engine, agent) is temporarily unavailable. | #2 |
| `not_ready` | 503 | yes | Readiness check failed; `details` lists each failing check. | #2 |
| `environment_offline` | 503 | yes | The environment's agent is not connected; retry when the environment is online again. | #6 |
| `engine_unavailable` | 503 | yes | The environment's agent is connected but cannot reach its Docker Engine. | #6 |
| `timeout` | 504 | yes | The operation did not finish within its deadline (also `408` for slow request bodies). | #2 |

`502 Bad Gateway` maps to `unavailable` and `408 Request Timeout` to `timeout`
(`CodeForStatus`); both are retryable.

### Job failures are not HTTP errors

A request that *starts* a job succeeds with `202` even if the job later fails.
The job's outcome is in the `Job` resource: `state` plus `error {class,
message, recovery}` with stable classes `agent_offline`,
`authorization_revoked`, `step_failed`, `unknown_outcome`, `journal_lost`,
`resume_limit`, `rejected`, `compensation_failed`, `executor_restarted`,
`credential_unavailable`,
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
