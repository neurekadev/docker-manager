# Audit trail (#30)

One append-only trail records who did what to which resource, from where,
and with what result, for every security-relevant and mutating action.

| Package | Role |
| --- | --- |
| `internal/manager/audit` | Recording (`Log.Record`, `Log.RecordTx`, `audit.Record`), request enrichment (`Draft`, `AddTarget`, `SetDiff`, ...), redaction, hash chain and `VerifyChain`, retention purge, capability metadata, action keys and categories. |
| `internal/manager/store` (`audit.go`) | Rows of `audit_events` and the `audit_chain` state row; migration `20260925100000_create_audit`. |
| `internal/manager/api` (`audit_http.go`) | Audit by construction: `api.Register` records every audited operation. |
| `internal/manager/api` (`audit.go`) | `GET /api/v1/audit`, `GET /api/v1/audit/exports`. |
| `internal/manager/jobs` (`audit.go`) | Job lifecycle records inside the engine's transactions. |

## Record

| Field | Content |
| --- | --- |
| `seq`, `id` | Chain position (1, 2, 3, ...) and UUIDv7. |
| `at` | UTC timestamp (microseconds). |
| `category` | `identity`, `authorization`, `credentials`, `operations`, `system` (`audit.CategoryFor`). |
| `action` | Dotted action key (below). |
| `operationId` | The API operation, for records of requests. |
| `actor` | `user` (+ user ID), `api_token` (+ token ID and owning user ID), `service` (the manager's own scheduled/maintenance work), `agent` (+ agent ID) or `anonymous`. |
| `clientIp`, `userAgent` | Resolved through the trusted proxies (`requestinfo`, #27); the user agent is bounded and scrubbed. |
| `environmentId`, `targets` | The environment ("host") and the resources touched (`{type, id, environmentId}`). |
| `outcome`, `errorClass` | `success`, `partial`, `failure`, `denied`, `error`, and the stable API error code or job error class. **Messages are never recorded.** |
| `jobId`, `requestId` | Links to the job (#26) and the request/log lines (#34). |
| `details` | Small redacted JSON object: rule diffs (`diff.before/after`), counts, item lists, filters. |
| `prevHash`, `hash` | The hash chain. |

### Action keys

- Operations authorized by a capability record the **#17 capability key**
  (`stack.deploy`, `container.restart`, `job.cancel`, `audit.export`). A
  selector operation (`stack.{action}`) records the concrete key its handler
  authorized (`audit.SetAction(ctx, "stack.stop")`).
- Operations guarded by a pseudo-capability (`public`, `authenticated`,
  `owner`) record a key derived from the operation ID
  (`create-invitation` → `invitation.create`, `delete-my-passkey` →
  `my_passkey.delete`) unless they declare `Operation.AuditAction`.
- Lifecycle keys that are not requests: `job.queued`, `job.started`,
  `job.cancel_requested`, `job.finished`, `audit.purge`. Features add their
  own non-request keys (for example `auth.sign_in_failed`, `agent.enroll`,
  `registry.credential_used`) with `audit.Record`.

## Recording

### HTTP: audit by construction

`api.Register` audits **every non-GET operation** (no opt-out) and every GET
operation that declares `Audit: api.AuditAlways` (downloads, exports). The
operation's action is published as `x-dockyard-audit` in the OpenAPI
document. After the operation answered (including validation failures,
Idempotency-Key replays and panics), one record is appended with the action,
operation ID, actor, client IP, user agent, request ID, path-parameter
targets (`/stacks/{stackId}` → `stack`, `{environmentId}` → the record's
environment), outcome from the final status (2xx/3xx success, 401/403
denied, other 4xx failure, 5xx error), error class, the job of a 202
response and `details.status`.

Request bodies, query strings, headers other than the user agent and
response bodies are never recorded. Handlers add what accountability needs:

```go
audit.AddTarget(ctx, domain.AuditTarget{Type: "stack", ID: created.ID}) // e.g. the created resource
audit.SetDiff(ctx, oldRules, newRules)                                   // permission changes (#17)
audit.SetDetail(ctx, "userName", in.Body.User)                           // never a secret or content
audit.SetAction(ctx, "stack.stop")                                       // selector operations
audit.SetPrincipal(ctx, principal)                                       // sign-in: the principal just established
```

The record is written after the handler's own transactions committed; if
it cannot be written (database failure) the error is logged with the
operation and action — the response has already been produced.

Unauthenticated requests to non-public operations answered 401 are **not**
recorded: authentication refused them before any effect, and recording them
would let anyone flood (and, through the size cap, flush) the trail; they
stay in the access log. Public operations (sign-in, setup, invitation and
recovery-code redemption) are always recorded, with an anonymous actor until
the handler names one.

`TestEveryCatalogedMutatingRouteIsAudited` registers every non-GET route of
`api/route-inventory.yaml` (planned or implemented) through `api.Register`
and requires one record with the right action, actor, targets and outcome;
`TestEveryServedMutatingOperationIsAudited` calls every served operation
with its real handler; `TestOpenAPICompleteness` fails on a mutating
operation without `x-dockyard-audit`; and
`TestOperationsRegisteredOnlyThroughRegister` fails when code registers an
operation with Huma directly.

### Jobs

The job engine records, in the same transaction as the state change, for
every kind: `job.queued` (actor = initiator: user, API token + owner, or the
service identity for scheduled jobs), `job.started` (each attempt),
`job.cancel_requested` (actor = the caller) and `job.finished` (outcome from
the terminal state, error class, `details.state`, and the item list — for
example what a prune deleted — as names and statuses). Details carry the
kind, capability, origin, executor, attempt and policy; never the job input,
error messages or item messages. `TestEveryJobKindEmitsLifecycleAuditRecords`
runs every kind of the catalog.

### Everything else

Events without a request or job call `audit.Record(ctx, domain.AuditEvent{...})`
(the recorder is in every request context) or `(*audit.Log).Record` (from the
manager, e.g. `app.Manager.Audit()`):

- #16: sign-in failures outside the session route, lockouts, owner-recovery
  CLI (actor `service` or the recovered user), session revocations by the
  system.
- #3: agent enrollment, credential rotation and revocation on `/agent/v1`
  (`audit.AgentActor(agentID)`; set `ClientIP`/`UserAgent`/`RequestID` from the
  request, the context has them when called from a server handler).
- #19/#33: each use of a registry or Git credential (`registry.credential_used`
  with the credential ID as target — never the value).
- #13 and other scheduled work: `Actor: audit.ServiceActor()`.

Inside a database transaction (single-connection SQLite), use
`RecordTx(ctx, tx, ev)`; calling `Record` while holding a transaction
deadlocks.

## Redaction

Every event passes `internal/manager/audit/redact.go` before it is hashed:

- values under sensitive keys (password, secret, token, credential, key,
  env/environment, content, body, data, value, input, output, logs, ...) become
  `[REDACTED]`, unless the key names metadata (`…Id`, `…Name`, `…Count`,
  `…Type`, `…At`, `…Scope`);
- byte slices, `logging.Secret`, errors and functions are always redacted;
  structs are reduced to their JSON form (unexported and `json:"-"` fields
  never appear);
- strings shaped like credentials anywhere (bearer/basic credentials,
  DockYard `dy_`/`dya_`/`dye_` tokens, private keys, JWTs, cloud and forge
  tokens, URL passwords, `password=…` pairs) are redacted whatever their key;
  strings over 1 KiB are omitted (file contents); depth, item counts and the
  details size (16 KiB) are bounded;
- IDs, targets and the user agent are cleaned (control characters, length)
  and scrubbed the same way; error classes must be snake_case codes.

The secret-canary tests (#29) seed every canary kind through request bodies,
headers, query strings, careless handler enrichment, job inputs and agent job
output, and assert that no canary reaches the stored rows, the list API, the
NDJSON/CSV exports or the mirrored log
(`TestSecretCanariesNeverReachTheAuditTrail`,
`TestSecretCanariesNeverStored`).

## Hash chain and verification

`hash = hex(SHA-256("dockyard-audit-v1\n" + prevHash + "\n" + canonical))`,
where `canonical` is the compact JSON object
`{"v":1,"seq":…,"id":…,"at":"YYYY-MM-DDTHH:MM:SS.ffffffZ","category":…,"action":…,"operationId":…,"actorKind":…,"actorUserId":…,"actorTokenId":…,"actorAgentId":…,"clientIp":…,"userAgent":…,"environmentId":…,"targets":"<stored targets JSON>","outcome":…,"errorClass":…,"jobId":…,"requestId":…,"details":"<stored details JSON>"}`
(members in this order, JSON strings escaped as Go's `encoding/json` does).
`prevHash` is the preceding record's hash, or the purge anchor's hash for the
oldest retained record (`""` before the first purge).

The `audit_chain` row holds the anchor (last purged seq and hash), the head
(newest seq and hash), the record count and total size. Database triggers
reject every `UPDATE` of `audit_events` and every `DELETE` outside the
retention purge. There is no update or delete API.

`audit.VerifyChain` / `(*Log).Verify` (for #34's support bundle and
diagnostics) recomputes every hash and link from the anchor to the head and
reports `hash` (content changed), `link` (record removed, inserted or
reordered), `sequence`, `head` (newest records removed) and `count`
problems (`TestVerifyDetectsTampering`).

## Retention

- `DOCKYARD_AUDIT_RETENTION_DAYS` (default 365): older records are deleted.
- `DOCKYARD_AUDIT_MAX_SIZE_MB` (default 1024): when the retained records'
  canonical size exceeds it, the oldest are deleted down to 90% of the cap.
- The purge runs hourly as manager service work (`Log.Run`), deletes only a
  prefix of the chain in batches, moves the anchor to the last deleted record
  and appends an `audit.purge` record (actor `service`, category `system`,
  with the reason, counts and new anchor) in the same transaction — the purge
  is audited and the chain stays verifiable from the new oldest record
  (`TestRetentionPurgeKeepsChainVerifiable`, `TestSizeCapPurge`).
- The trail lives in the manager database, so manager-state backups (#10,
  #24) include it.

## Access and export

- `GET /api/v1/audit` (`list-audit-events`, capability `audit.read`): newest
  first, cursor pagination, filters `actorKind`, `actorId`, `action`,
  `category`, `outcome`, `environmentId`, `resource=type:id`, `jobId`, `since`,
  `until` (repeat a parameter to OR its values).
- `GET /api/v1/audit/exports` (`export-audit-events`, capability
  `audit.export`): the same filters, `format=ndjson|csv`, streamed oldest
  first up to the newest record at the start. NDJSON lines carry `details`
  exactly as hashed; CSV cells starting with `=`, `+`, `-`, `@`, tab or CR are
  prefixed with `'` (formula injection). The export is itself audited (with
  its format, filters and record count).
- Both capabilities are instance-scoped, owner-only by default and
  all-or-nothing (`audit.Capabilities()` carries the high-risk flag for the
  #17 catalog): records reveal activity on resources the reader cannot
  otherwise see, so they are never filtered per item. Until #17 wires its
  evaluator the routes are denied to everyone (`authz.DenyAll`).
- `DOCKYARD_AUDIT_LOG_MIRROR=true` also writes every stored (redacted) record
  as a structured log line (`msg="audit"`, `component=audit_mirror`, level
  info, so `DOCKYARD_LOG_LEVEL` must be `info` or `debug`) for external
  collection. Off by default. A record written in a transaction that
  later rolls back can appear in the mirror only.

The viewer UI (filters, rule-diff display) is built with the #22 screens.
