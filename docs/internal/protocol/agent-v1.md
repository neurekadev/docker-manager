# Agent protocol `docker-manager.agent/v1`

The private protocol between the Docker Manager and its agents (#3, #4).
It is versioned independently of the public `/api/v1`. This document is
normative; `internal/protocol` implements the envelope, payload types,
allowed names, limits, close codes and the version window, and its tests
(`TestProtocolDocListsNames`, `TestAgentRoutesDocumented`) fail when this
document and the code disagree.

## Principles

- **Agents dial out.** An agent opens one outbound HTTPS WebSocket to the
  manager's single public origin (or an explicitly opted-in internal URL on
  the manager's Docker network, `DOCKER_AGENT_MANAGER_ALLOW_HTTP=true` for plain
  HTTP, #27; after a manager move, the address the manager sent in
  `manager.redirect`). Agents never listen on a socket.
- **Named operations only.** The manager sends job commands, requests and
  stream openings whose names are on the allowlists below. There is no
  Docker Engine API passthrough and no host shell passthrough. The agent
  rejects anything else (`unsupported_request`, `unsupported_stream`,
  `unsupported_kind`).
- **The manager authorizes, the agent confines.** Users and API tokens never
  talk to an agent. The manager checks the caller's capability (#17) before
  it sends anything; the agent additionally enforces what it can check
  locally: allowlists, file-root containment, size and time bounds and its
  own capability set.
- **One agent per Engine**, one active session per agent (#3, #25).
- **Bounded everything:** frame size, stream windows, chunk size, paths per
  invalidation, request deadlines, pre-auth time.

## Routes

| route | purpose | auth |
| --- | --- | --- |
| `POST /agent/v1/enroll` | exchange a one-use enrollment token for an agent credential | enrollment token |
| `GET /agent/v1/session` | WebSocket upgrade to the session | agent credential |

Both are publicly reachable on the shared origin, so they are rate limited
per client IP (via `DOCKER_MANAGER_TRUSTED_PROXIES`, #27: 1 request/s, burst 30)
and per token/credential (enrollment 6/min, burst 5 per enrollment ID;
session upgrades 10/min, burst 10 per credential ID), answer failures
generically, and bound request sizes (64 KiB) and pre-auth time (10 s).
Responses carry `Cache-Control: no-store`. Browser cookies never
authenticate `/agent/v1`, agent credentials never authenticate `/api/v1`,
and requests carrying an `Origin` header (every browser request does) are
rejected with `403 forbidden`, which also prevents cross-site WebSocket
hijacking. HTTP errors on these routes use the public error shape
([errors.md](../api/errors.md)).

## Enrollment

The owner (or a user with `agent.enroll`) creates an enrollment with `POST
/api/v1/agent-enrollments` — or, before the UI and accounts exist (#16),
inside the manager container with `docker-manager enrollment create
[-name N] [-intent …] [-ttl 1h] [-json]`. The manager returns, **once**, a
token and install commands containing the manager URL
(`DOCKER_MANAGER_PUBLIC_URL`) and the token — on stdin, in an environment variable
or a `.env` file, never in a URL:

| variant | how the token reaches the agent |
| --- | --- |
| `colocated` | `printf '%s\n' "$TOKEN" \| docker compose exec -T docker-agent docker-agent enroll` next to the manager's compose file (the agent already runs on the internal URL) |
| `remote` | `docker run -d … docker-agent` with the public origin, then the same `docker-agent enroll` on stdin |
| `remote_compose` | `DOCKER_AGENT_ENROLLMENT_TOKEN=` in the `.env` next to the agent's `compose.yaml` (user documentation, "Add more servers"), then `docker compose up -d` |

The enrollment records its intent:

| intent | meaning |
| --- | --- |
| `new` (default) | create a new environment; optional preset name (`DOCKER_AGENT_ENVIRONMENT_NAME`) |
| `replace:<agentId>` | the new agent replaces the given agent for the same Engine; the old credential is revoked when enrollment succeeds |
| `reattach:<environmentId>` | re-attach an archived environment (#34) — the owner's confirmation is given when creating the token |

Enrollment tokens: format `dye_<enrollmentId>_<secret>` (secret: 32 random
bytes, base64url; the `dye_` prefix lets the manager refuse them on
`/api/v1`, #27), single use, expire after a lifetime chosen at creation
(default 1 h, 1 min to 24 h), stored only as SHA-256 of the secret (compared
in constant time), revocable (`DELETE /api/v1/agent-enrollments/{id}`),
consumed atomically by the first successful enrollment, and never logged.
Expired enrollments are deleted a week after they expired.

### `POST /agent/v1/enroll`

```http
POST /agent/v1/enroll HTTP/1.1
Authorization: Bearer dye_0190a6e0-..._q2V1c...
Content-Type: application/json
User-Agent: docker-agent/1.4.0

{
  "protocol": "docker-manager.agent/v1",
  "agentVersion": "1.4.0",
  "installId": "0190a6e0-1111-7000-8000-000000000001",
  "engine": { "id": "4VQD:...:ZK2M", "version": "28.5.2", "apiVersion": "1.51", "os": "linux", "arch": "amd64" },
  "hostname": "nas-01",
  "environmentName": "NAS"
}
```

- `installId` is generated by the agent on first start and persisted in its
  state volume. The pair (Engine ID, install ID) identifies the installation;
  Engine IDs alone can collide on cloned VMs (#3).
- Body limit 64 KiB; unknown fields are rejected.

Success, `201 Created`:

```json
{
  "agentId": "0190a6e0-2222-7000-8000-000000000002",
  "environmentId": "0190a6e0-3333-7000-8000-000000000003",
  "environmentName": "NAS",
  "credential": "dya_0190a6e0-4444-7000-8000-000000000004_Zm9vYmFy...",
  "sessionPath": "/agent/v1/session",
  "reattached": false
}
```

The agent writes the credential atomically (temporary file, fsync, rename,
mode `0600`) to its persistent secret mount **before** it opens a session.
If it crashes in between, the credential is lost and the owner creates a new
enrollment (the token was consumed).

Failures (bodies use the standard error shape):

| status | code | meaning |
| --- | --- | --- |
| 401 | `unauthenticated` | token unknown, wrong secret, expired, revoked or already used — deliberately indistinguishable |
| 403 | `forbidden` | the request carries an `Origin` header (browsers never enroll) |
| 409 | `engine_already_enrolled` | an active agent already controls this Engine (same install ID, or the same host name with a new install ID); create an enrollment with intent `replace:<agentId>` (the message names it) |
| 409 | `engine_identity_conflict` | the Engine ID is enrolled from another host with another install ID (cloned VM): replace the agent if it is the same Engine, regenerate the clone's Engine ID (`/var/lib/docker/engine-id`), or create an enrollment with `allowDuplicateEngineId` |
| 409 | `environment_archived` | this Engine belongs to an archived environment; create an enrollment with intent `reattach:<environmentId>` |
| 409 | `environment_detached` | this Engine belongs to an environment whose agent was removed; create an enrollment with intent `reattach:<environmentId>` |
| 409 | `engine_mismatch` | a `replace` or `reattach` enrollment was used for another Engine than its target's |
| 409 | `enrollment_target_unavailable` | the agent to replace is no longer active, or the environment to re-attach no longer archived/detached |
| 413 | `payload_too_large` | body over 64 KiB |
| 422 | `validation_failed` | malformed body |
| 426 | `version_unsupported` | protocol or agent version outside the window (see below); message says what to upgrade. Checked before the token, so an outdated agent learns it even with a valid token |
| 429 | `rate_limited` | honour `Retry-After` |
| 503 | `unavailable` | the manager moved to a new server and refuses agents (checked before the token, `Retry-After: 60`); the token is not consumed |

A 409 does **not** consume the token; the manager records the refused
attempt on the enrollment (`lastRejection`: code, Engine ID, install ID,
host name and the conflicting agent/environment) so the owner can resolve
it. One active agent per Engine is enforced in the enrollment transaction
(and by a unique index of one active agent per environment).

### Agent side

The agent enrolls once it knows its Engine identity (the Engine is
reachable). Token sources, in order: a token handed over by `docker-agent
enroll` (read from stdin or `-token-file`, written 0600 to
`<state dir>/enrollment-token`, picked up within 2 s, deleted once used; also
while enrolled — the new token wins, e.g. to replace or re-attach), then
`DOCKER_AGENT_ENROLLMENT_TOKEN(_FILE)` while not enrolled. `docker-agent
enroll` waits (`-wait 90s`) for the outcome
(`<state dir>/enrollment-status.json`) and for the session to come online,
and exits 0 (online), 1 (refused, with the manager's code), 2 (usage) or 3
(timeout). Tokens that were used or refused are remembered as SHA-256
(`enrollment-used.json`) and never sent again; a 426 is not remembered (an
upgraded agent may retry). Network failures, 429 and 5xx are retried with
the session backoff. The state directory also holds `install-id` (generated
once), `credential.json` (0600, atomic write) and `manager.json` (the highest
manager generation seen and the address a moving manager sent in
`manager.redirect`, 0600, atomic write; see "Manager generation" under
[welcome](#welcome)). Enrollment goes to the address the agent dials now
(after a move the redirected one), which `credential.json` records as
`managerUrl`; enrolling again keeps the redirect. When the manager refuses
the credential (401 at the upgrade, close 4401/4403) the agent deletes it,
reports `unauthorized` in `health.json` and waits for a new token.

### Credential

- Format `dya_<credentialId>_<secret>`; the manager looks the credential
  up by ID and compares SHA-256(secret) in constant time. Only the hash is
  stored. It is a bearer secret presented on the session upgrade, not mTLS,
  because TLS terminates at the proxy (#27).
- Never logged, never in URLs, never sent to browsers, never stored outside
  the agent's secret mount.
- **Rotation** (`POST /api/v1/agents/{agentId}/credential-rotations`): the
  manager creates a new credential (kept sealed with the secret-protection
  key while pending) and sends the request `agent.credential.rotate {credential}`
  on the live session. The agent persists it atomically and answers
  `response {persisted: true}`; only then does the manager make it the only
  valid credential and revoke the old one — the session keeps running
  (`state: completed`). If the agent is offline or does not confirm within
  30 s the rotation is `pending`: the old credential stays valid and the new
  one is delivered when the agent's next session comes online. An agent that
  persisted a pending credential whose confirmation was lost presents it on
  its next upgrade, which completes the rotation the same way.
- **Revocation** (agent removed, `DELETE /api/v1/agents/{agentId}`, or
  replaced): the credential stops working immediately and a live session is
  closed with `4403`.

## Session

### Upgrade

```http
GET /agent/v1/session HTTP/1.1
Upgrade: websocket
Authorization: Bearer dya_0190a6e0-4444-..._Zm9vYmFy...
Sec-WebSocket-Protocol: docker-manager.agent/v1
User-Agent: docker-agent/1.4.0
```

Before the upgrade the manager answers with an HTTP error (standard shape):
`401 unauthenticated` (unknown, revoked or rotated-out credential), `403
forbidden` (an `Origin` header is present), `426 version_unsupported` (the
`docker-manager.agent/v1` subprotocol was not offered), `429 rate_limited`, and `503
unavailable` with `Retry-After: 60` once the manager handed its state to a
new server (checked before the credential; the agent keeps its credential
and retries with backoff). The negotiated subprotocol is
`docker-manager.agent/v1`.

### Handshake

```
agent                                   manager
  ── upgrade (Bearer credential) ─────────▶  authenticate, rate limit
  ── hello {HelloPayload} ───────────────▶  within 10 s, else close 4400
                                            check agentId matches the credential,
                                            CheckAgentVersion (N-1 window)
  ◀─ welcome {WelcomePayload} ────────────  (or error version_unsupported + close 4426)
  check the manager generation              (lower than recorded: close 4421)
  ── capabilities {CapabilitiesPayload} ─▶
  ── job_report {JobReportPayload} ──────▶  reconcile jobs (#26)
  ◀─ ack {forget} ────────────────────────
                                            environment is online; full resync (#23)
```

- The agent checks the welcome's `generation` before anything else (see
  "Manager generation" below); a refused manager gets no capabilities or
  job report and none of its frames is handled: the agent closes with
  `4421` and reconnects with backoff, keeping its credential.
- The hello must match the credential: another agent or install ID closes
  with `4401`; another Engine ID than the enrolled one closes with `4403`
  (enroll the agent again). `capabilities` must follow `welcome`, then
  exactly one `job_report`; anything else before it closes with `4400`.
- The manager closes any older session of the same agent with `4409`, and a
  session of a replaced agent for the same environment with `4403`.
- The environment is reported **online only after** `job_report` was
  reconciled and every registered reconciler (inventory owners re-read what
  changed while the agent was away, `agents.Hub.AddReconciler`) returned, so
  live views never claim stale state is current (#23). The online/offline
  transition is persisted on the environment (`online`,
  `connectionChangedAt`) and published on the manager's event bus
  (`environment.online` / `environment.offline`), followed by
  `environment.resync` (reason `reconnect`). A manager restart marks every
  environment offline until its agent is back.
- Reconnect: exponential backoff with full jitter from 1 s to 60 s, reset
  after 60 s of healthy session; never reconnect after `4401`, `4403`, `4409`
  or `4426` (`protocol.ReconnectAllowed`).

### Liveness

Each side sends `heartbeat` (optional `{seq, sentAt}`) at least every 15 s
while idle (`HeartbeatInterval`); any received frame counts as liveness. A
side that heard nothing for 45 s (`HeartbeatTimeout`) closes with `4408`.
Manager-side heartbeats are also shorter than common proxy idle timeouts.
The manager records the agent's (and its environment's) last-seen time on
connect and disconnect and, while the session lives, refreshes it from the
received frames at most every 60 s (not per heartbeat).

## Envelope

Every WebSocket message is one **text** message containing one JSON frame
(binary messages are a protocol error). Unknown fields and trailing data are
rejected on both the envelope and the payload.

```json
{
  "type": "command",
  "id": "cmd.0190a6e0-...-0001.1",
  "correlationId": "…",
  "jobId": "0190a6e0-...-0001",
  "attempt": 1,
  "fencingToken": 42,
  "deadline": "2026-09-24T12:30:00Z",
  "payload": { "kind": "stack.deploy", "input": { } }
}
```

| field | rule |
| --- | --- |
| `type` | one of the frame types below |
| `id` | unique per sender and session, `^[A-Za-z0-9._:-]{1,128}$`; command IDs are `cmd.<jobId>.<attempt>` |
| `correlationId` | the frame this one answers or belongs to (required where the table says so) |
| `jobId`, `attempt`, `fencingToken` | job reference (#26); required on `command`, forbidden on `request` |
| `deadline` | RFC 3339; required on `command` and `request`; after it the receiver must not start and must abort |
| `requestId` | optional, manager → agent on `command`, `request` and `stream_open` only, `^[A-Za-z0-9._:-]{1,128}$`: the public API request (`X-Request-ID`) that caused the work (#34). The agent logs it as `request_id` with everything it does for that frame, so one ID correlates the proxy, manager and agent logs. Scheduled work carries none |
| `payload` | a JSON object whose schema depends on `type` |

Limits: a frame is at most 1 MiB encoded (`MaxFrameSize`; larger closes the
socket with `1009`). Receivers drop a repeated frame `id` within a session
(the last 1024 IDs are remembered) — retransmission never duplicates work.

## Frame types

| type | direction | correlationId | payload | purpose |
| --- | --- | --- | --- | --- |
| `hello` | agent → manager | — | required | first frame: identity and versions |
| `welcome` | manager → agent | hello | required | session established: IDs, timing, limits |
| `capabilities` | agent → manager | — | required | what this agent and its Engine can do; resent when it changes |
| `heartbeat` | both | — | optional | liveness |
| `command` | manager → agent | — | job command | run a durable job attempt (#26) |
| `ack` | both | command, result or job_report | ack | agent: accepted/rejected a command; manager: forget journal entries |
| `progress` | agent → manager | command | progress | job progress, items |
| `result` | agent → manager | command | result | job attempt outcome |
| `job_report` | agent → manager | — | job report | after every (re)connect: fencing high-water mark and journaled jobs |
| `cancel` | manager → agent | command or request | optional | cancel a job attempt at its next safe point, or abort a request |
| `request` | manager → agent | — | required | named bounded non-job operation |
| `response` | agent → manager | request or rescan | optional | successful request output |
| `event` | agent → manager | — | required | Docker Engine or agent event |
| `fs_invalidation` | agent → manager | — | required | changed paths under a watched file scope |
| `rescan` | manager → agent | — | required | bounded reconciliation of a file scope; answered by `response` |
| `stream_open` | either | — | required | open a byte stream; the frame ID is the stream ID |
| `stream_data` | stream sender | stream_open | required | one chunk |
| `stream_credit` | stream receiver | stream_open | required | grant the sender more bytes |
| `stream_close` | either | stream_open | optional | end a stream |
| `error` | either | failed frame (or none) | required | a frame failed, or a session-level problem before close |

`ValidatePayload` strictly decodes and checks every payload listed here.

## Payloads

### hello

```json
{ "protocol": "docker-manager.agent/v1", "agentId": "…", "agentVersion": "1.4.0",
  "installId": "…", "engineId": "…", "previousSessionId": "…" }
```

### welcome

```json
{ "sessionId": "…", "managerVersion": "1.4.2", "environmentId": "…",
  "agentStatus": "current", "heartbeatIntervalMs": 15000, "heartbeatTimeoutMs": 45000,
  "limits": { "maxFrameBytes": 1048576, "maxStreams": 32, "streamWindowBytes": 1048576,
              "maxChunkBytes": 262144, "maxPaths": 256 }, "generation": 2 }
```

`agentStatus` is `current` or `outdated` (previous minor release, still
supported; the UI shows an upgrade notice). `generation` is the manager
instance's generation (below).

#### Manager generation (#35)

Every move of the manager to another server raises its instance's
generation (0 or absent: a manager that predates moves, counted as 1). The
agent keeps the highest generation it has seen in `<state dir>/manager.json`
(missing or corrupt: 0, with a warning) so that an old manager (resurrected,
or restored from a backup) never commands an agent that met the new one.
It checks twice, against the same record:

1. **welcome** (`session.Options.AcceptWelcome` →
   `protect.Guard.AcceptGeneration`), before the read loop starts. Lower:
   the agent logs a warning naming both generations, sends nothing more
   (no capabilities, no job report), handles no frame of that session and
   closes it with `4421`; `Client.Run` reconnects with its normal backoff
   and the credential stays. Higher: written to `manager.json` before the
   session continues.
2. **`manager.identity`** (second line, e.g. a manager that fills only the
   request): lower answers `error conflict` (message names both
   generations), then closes with `4421` once the answer is written
   (`session.EndSessionError`), keeping the previous identity; higher is
   written before the answer.

Equal generations pass. A failed write of a higher generation is logged
and the manager is still accepted (the next check writes it again).
Generation writes never lower the record (a concurrent `manager.redirect`
may have raised it) and keep the redirect.

#### Manager redirect (#35)

When Docker Manager moves to a new server, the old manager sends
`manager.redirect {url, generation}` to the agents it can place, just
before it refuses agents ([manager-move.md](../architecture/manager-move.md),
"Agents follow"). The agent (`internal/agent/runtime`, `redirect.go`):

1. **Validates**: `url` is an http or https origin (scheme and host with
   an optional port; no credentials, path other than `/`, query or
   fragment; at most 2048 characters; `config.ParseRedirectURL`);
   `generation` is at least 1 and higher than the one the agent follows,
   or equal to it for an `https` origin or the agent's
   `DOCKER_AGENT_MANAGER_URL` origin (the manager it follows gives it
   another address of its own: after a move, the new manager's public
   HTTPS URL instead of the plain-HTTP address the move gave; the
   configured origin forgets the stored redirect). Agents that accept
   this announce the feature `manager.redirect.secure`; the manager sends
   such a redirect only to them. Malformed JSON: `invalid_frame`; a bad
   `url` or `generation` < 1: `invalid_argument`; a lower generation, or
   an equal one with another plain-`http` address: `conflict` (the same
   `url` and `generation` again, e.g. a lost answer, is answered like the
   first time). A refused redirect changes nothing and the session stays.
2. **Persists** the address (as an origin, lower-case host), the
   `DOCKER_AGENT_MANAGER_URL` origin it replaces and the new generation in
   `manager.json` in one atomic write (0600) **before** answering. A
   failed write answers `internal` (retryable) and changes nothing.
3. **Answers** `{}` and then closes the session with `1001` (the handler
   returns `session.EndSessionError` with `Output`). `Client.Run`
   reconnects with its normal backoff, asking the runtime's current
   transport before every dial (`session.Options.Target`), so the next
   session dials the new address with the same credential. The old
   manager (lower generation) is refused from then on.

From then on the redirected address replaces `DOCKER_AGENT_MANAGER_URL`,
also after restarts and for enrollment. Plain `http` is allowed for it
without `DOCKER_AGENT_MANAGER_ALLOW_HTTP`, because the authenticated
manager of the current session sent it (`transport.NewRedirected`, the only
exception to that rule); it is still reported as `transport.plainHttp`
and flagged. `https` keeps the TLS trust of `DOCKER_AGENT_MANAGER_CA_FILE`.
The capabilities' `transport.managerUrl`, the startup log
(`manager_url_source: move`) and `health.json` (`managerUrl`,
`managerUrlSource`) show the address dialed. The redirect is forgotten
(the generation stays) when the agent starts with a
`DOCKER_AGENT_MANAGER_URL` origin other than the one the redirect
replaced: the operator's new value wins.

### capabilities

```json
{
  "agentVersion": "1.4.0",
  "protocols": ["docker-manager.agent/v1"],
  "os": "linux", "arch": "amd64",
  "engine": { "id": "…", "version": "28.5.2", "apiVersion": "1.51", "minApiVersion": "1.24",
              "os": "linux", "arch": "amd64", "rootless": false },
  "commands": ["stack.deploy", "container.restart"],
  "requests": ["engine.info", "container.list"],
  "streams": ["container.logs", "container.exec"],
  "features": ["fs.inotify"],
  "roots": [ { "kind": "stacks", "path": "/var/lib/docker/volumes/docker-manager_stacks/_data", "watch": "inotify" },
             { "kind": "volumes", "path": "/var/lib/docker/volumes", "watch": "inotify" } ],
  "transport": { "managerUrl": "https://docker.example.com", "plainHttp": false, "customCa": false },
  "diagnostics": [ { "area": "storage", "code": "storage_root_mismatch", "path": "/opt/stacks",
                     "message": "stack root /opt/stacks is mounted from /srv/stacks; mount it at its identical path" } ]
}
```

- `diagnostics` (optional) explain what the agent cannot do and why, with a
  stable `code` per `area` (`engine`: the #21 Engine codes such as
  `unsupported_api_version` or `engine_unavailable`; `storage`: the #28
  layout checks such as `storage_path_mismatch`, `storage_mount_missing`,
  `storage_rootless_engine`). Only verified directories appear in `roots`;
  stack operations are offered only when the `stacks` feature is present
  (the stacks volume passed the identical-path check). The host page shows
  the diagnostics.

- `transport` (required, #27) is how the agent reaches the manager:
  `managerUrl` is the origin the agent dials (after a move the redirected
  one); `plainHttp` is true exactly for an `http://` manager URL
  (`DOCKER_AGENT_MANAGER_ALLOW_HTTP=true`, co-located agents only, or a
  plain-HTTP `manager.redirect` address) and the host page flags such
  environments; `customCa` reports a
  `DOCKER_AGENT_MANAGER_CA_FILE` bundle. Built by `internal/agent/transport`.

- `engine.apiVersion` is the version the Moby client negotiated (#21). The
  manager maps Engine and API versions to supported features; a job kind or
  request the environment cannot serve is rejected **before** dispatch
  (`501 job_kind_unavailable` / `501 not_implemented` on the public API).
  The supported Engine minimum is decision Q2 in #25.
- `commands`, `requests` and `streams` must be subsets of the allowlists
  below; `roots` are absolute paths the agent serves and watches (#28).

### Version window (#34)

The manager supports agents of its own minor release and of the previous
one (N-1). `protocol.CheckAgentVersion(manager, agent)`:

| agent vs manager | result |
| --- | --- |
| same `major.minor` (any patch), or identical version strings (edge builds) | `current` |
| previous minor, same major | `outdated` — works, UI flags it |
| older than N-1, different major, newer than the manager, or unparsable | refused: `error {code: version_unsupported}` then close `4426`; enrollment answers `426` |

Upgrade order is manager first, then agents. The protocol identifier itself
(`docker-manager.agent/v1`) changes only for incompatible protocol changes; within
v1, fields and frame types are only added, and receivers reject unknown
fields, so an addition is used only after both sides announce it (`features`).
Example: agents announce `frame.request_id` (`protocol.FeatureRequestID`);
the manager sets the envelope's `requestId` only on sessions whose agent
announced it (#34). Likewise `backup.activity`
(`protocol.FeatureBackupActivity`, #10): only those agents get
`backup.run` inputs with `activity: true`, and only then send progress
frames with `activity`. And `backup.compression`
(`protocol.FeatureBackupCompression`, #10): only those agents get
`backup.run` and `backup.retention` inputs whose
`repository.destination` has `compression` (`max` or `off`; auto is
never sent), added by the manager at dispatch; other agents back up and
prune with restic's default. And `backup.expire`
(`protocol.FeatureBackupExpire`): only those agents get `backup.retention`
inputs with `expire` (items whose snapshots all go: deleted stacks and
volumes past the setup's expiry); other agents apply the rules alone. And
`backup.retention_any_policy` (`protocol.FeatureBackupAnyPolicy`, #246):
only those agents get `backup.retention` inputs with `anyPolicy: true`
(judge every snapshot carrying a policy tag, so earlier backup policies'
backups expire too); other agents judge the snapshots of `policyId` only.
And `stack.remove_volumes`
(`protocol.FeatureStackRemoveVolumes`): only those agents get
`stack.remove` inputs with `removeVolumes` (and `keepVolumes`); the manager
refuses the option for other agents (they would keep the volumes). And
`stack.import_copy` (`protocol.FeatureStackImportCopy`, #7): only those
agents execute `stack.import` (a `stack.*` input with `import`, the
project's current directory) and report `copyable` in `compose.discover`;
the manager refuses an import by copy for other agents. Newer agents also
report `protected` (Docker Manager's own project): their `stack.import`
copies it while it runs, neither stopping nor recreating it (result output
`import.live`); older agents refuse it with `protected`. And
`stack.import_containerless` (`protocol.FeatureStackImportContainerless`):
only those agents get `stack.import` inputs whose `import` has
`containerless: true` (a project `compose.discover` reported
`containerless`: copied and switched, nothing stopped, recreated or
started, refused once the project has containers); the manager refuses such
an import for other agents. `compose.discover`'s `containerless` projects
(found through their Compose files in stack roots and import mounts) and
every project's `volumes` are output fields newer agents add (no feature).
And `stack.rename`
(`protocol.FeatureStackRename`, #7): only those agents serve
`compose.rename_preview` and execute `stack.rename` (a `stack.*` input with
`rename`: the new project name and directory; result output `rename`, with
`switched` once the stack lives under the new name); the manager refuses a
rename for other agents. And `stack.pull` (`protocol.FeatureStackPull`):
only those agents execute `stack.pull` (pull a stack's images, change no
container; result output `pulled`: the services whose tag now names
another image); the manager refuses a pull-only request for other agents.
And `container.recreate` (`protocol.FeatureContainerRecreate`, #273): only
those agents execute `container.recreate` (the `container.*` action input:
`name`, `id`, optional `timeoutSeconds`; replaces a standalone container
with a clone of its configuration on the image its reference names now;
result output `wasRunning` and `containerId`, the new container); the
manager refuses a recreate for other agents (501 `agent_unsupported`).
And
`exec.shell` (`protocol.FeatureExecShell`, #8): only those agents get
`container.exec.create` inputs with `shell`; for other agents the manager
sends the shell's most common path as `cmd` (`protocol.LegacyShellCommand`:
`/bin/bash`, `/bin/zsh`, else `/bin/sh`). And `labels.docker_manager`
(`protocol.FeatureLabels`): those agents write Docker Manager's labels
under the `docker-manager.` prefix, read them under it and under the
legacy `dev.neureka.docker-manager.` prefix, and accept the `ownership`
labels of `container.create` and `update.run` under either key (writing
them under the current one); the manager sends ownership under the legacy
keys (`protocol.LegacyLabels`) to other agents, which accept only those.
And `files.limits` (`protocol.FeatureFileLimits`, #15): only those agents
get `limits` (`FileLimits`: the manager's `DOCKER_MANAGER_FILES_*`
upload, download, extraction and entry limits) in `files.upload` and
`files.download` inputs, extract previews and `files.archive` /
`files.extract` job inputs, and apply them instead of their built-in
defaults; other agents keep the defaults. And `files.stack_no_follow`
(`protocol.FeatureStackFilesNoFollow`, #15): those agents follow no
symlink in `stack` scopes (see [Scoped files](#scoped-files-15)), so a
path names exactly one file; for other agents' stacks the manager
requires `stack.definition.read` for every content read and
`stack.definition.write` for every change, as any path may reach a
definition file through an in-root symlink. Nothing on the wire changes.
And `manager.redirect.secure`
(`protocol.FeatureManagerRedirectSecure`): only those agents get a
`manager.redirect` at the generation they follow (an https origin or
their configured origin, see "Manager redirect"); other agents keep the
plain-HTTP address a manager move gave them.
Optional fields an agent adds to
its own request outputs need no feature: agents are not newer than the
manager, the manager decodes a response's `output` without refusing
unknown fields, and it treats a missing field as not reported. Examples:
`container.inspect`'s `restartMaxRetries` and the addresses of
`network.inspect`'s attached containers (`ipAddress`, `ipv6Address`); an
N-1 agent omits them. Upgrade procedure:
`docs/internal/operations/upgrades.md`.

### command, ack, progress, result, job_report, cancel (jobs, #26)

Defined in `internal/protocol/jobs.go` and
[job-engine.md](../architecture/job-engine.md#dispatch-fencing-and-agent-recovery):

- `command {kind, input, completedSteps, output, secrets}` with `jobId`,
  `attempt`, `fencingToken` (per-environment, persisted, strictly
  increasing) and `deadline` (latest start). A resumed attempt carries the
  steps earlier attempts completed and the `output` they reported: it
  skips those steps and continues from that output (later steps read what
  the completed ones recorded, e.g. a prune's collected candidates).
- `secrets {registries: [{connectionId, host, serverAddress, username,
  secret}], git: [{credentialId, host, username, secret, plainHttp}]}` (optional) are
  the credentials of this attempt only (#19, #33). The manager resolves them
  at every dispatch from the connection IDs named in the job input (never
  stored with the job); the agent keeps them in memory for the attempt and
  never journals, logs or writes them to a Docker config. A resumed attempt
  receives them again (after a rotation: the new credential).
- The agent journals (fsync) before it acks: `ack {accepted, code, message,
  highWater}`; rejection codes `duplicate`, `stale_fencing_token`,
  `unsupported_kind`, `invalid_command`, `attempt_in_progress`,
  `deadline_exceeded`, `journal_failed`.
- `progress {step, percent (-1 unknown), message, item, activity}`;
  `activity {item, itemIndex, itemCount, percent, filesDone, filesTotal,
  bytesDone, bytesTotal, secondsRemaining, currentFile}` is a running
  backup's live state (#10, `protocol.ActivityPayload`): a frame with only
  `activity` is kept in the manager's memory, never in the job, its events
  or the audit trail, and `currentFile` (relative to the item, at most
  4096 bytes) reaches only holders of the scope's files-read capability.
  `result {outcome,
  errorClass, message, recovery, items, completedSteps, interruptedStep,
  resumable, compensations, output}`; outcomes `succeeded`, `failed`, `partial`,
  `cancelled`, `interrupted`. `output` is the kind's result data (a JSON
  object of at most 128 KiB, journaled with the attempt and sent with every
  outcome), e.g. the sources, images and pre-operation state of a
  `stack.deploy` (`protocol.StackJobOutput`). A `job_report` carries at most
  512 KiB of outputs; beyond that they are dropped and the manager treats
  them as unknown.
- The manager acks a result with `forget: [jobId]`; after every reconnect the
  agent sends `job_report {highWater, jobs}` and the manager reconciles
  instead of re-running. A job the report lists as `running` sends its
  `result` after the report, never before it.
- `cancel` carries the job reference; it is honoured at the kind's next
  cancellation safe point (or mid-step by steps that stop safely, such as
  an image build or a backup snapshot) and compensations always run.

### request / response

```json
{ "type": "request", "id": "q-7", "deadline": "2026-09-24T12:00:10Z",
  "payload": { "name": "files.list", "input": { "scope": { "kind": "volume", "id": "data" }, "path": "config" } } }
{ "type": "response", "id": "r-7", "correlationId": "q-7", "payload": { "output": { "entries": [] } } }
```

The `compose.*` payloads are defined in `internal/protocol/compose.go`
(#7): projects are addressed by a `ProjectRef` (root `stacks` or a registered
`bind` root, a clean relative project directory and the project name); the
agent resolves it against its verified roots (#28) and refuses anything
outside them (`forbidden_path`). Definition files (compose files, override
files, `.env` and service `env_file`s inside the project directory) travel
with SHA-256 hashes; `SourceHash` identifies a definition identically on
both sides. `compose.write` creates a new project directory (never over an
existing one: `conflict`) or replaces definition files when the current
definition still has the expected hash (`conflict` otherwise).

Failures answer with `error` (correlationId = request). Requests the agent
does not serve fail with `unsupported_request`. The agent serves at most
`protocol.MaxConcurrentRequests` (16) request and rescan frames at once per
session and refuses more with a retryable `busy` before running them; the
manager keeps at most that many in flight (a request waits for a free slot
within its deadline) and sends a frame refused that way again with a new
frame ID after a short backoff (250 ms, doubling, at most 4 times, within
the deadline). The Docker resource
requests and job inputs of #6 are defined in `internal/protocol/docker.go`
(strict decoding: unknown input fields are refused); their semantics are in
[docker-resources.md](../architecture/docker-resources.md). The request's `input` and
`output` schemas are owned by the feature issue named in the table and
documented next to its code; this document fixes the names, capabilities and
bounds.

### Sequence numbers and gaps

`event` and `fs_invalidation` frames carry `seq`: two independent counters
per session, starting at 1 and increasing by one per frame. The agent relays
them from bounded queues; when a queue is congested it drops the frame **but
still consumes its number** (it never blocks the session, and it never
renumbers). The manager tracks each counter (`protocol.SeqTracker`): a
number at or below the last one is a duplicate and dropped; a jump is a gap.
After a gap, and after every (re)connect, the manager stops trusting the
history: for events it publishes `environment.resync` (reason `event_gap`
or `reconnect`) so consumers re-read the environment's inventory; for file
invalidations it publishes a whole-environment `files.invalidated` with
overflow (reason `sequence_gap`), so open file views rescan (#23). An
invalidation with more than 256 paths is sent as `overflow` of its scope
without paths.

### event

```json
{ "source": "engine", "type": "container", "action": "die", "resourceId": "…",
  "attributes": { "exitCode": "137", "name": "web-1" }, "at": "2026-09-24T12:00:00Z", "seq": 812 }
```

`seq` increases per session. Attributes are an allowlist (names, image,
exit code, health status); never environment variables, labels that may
hold secrets, or file contents. After a reconnect or a `seq` gap the manager
re-reads inventory instead of trusting the event history.

### fs_invalidation and rescan (#15, #23)

```json
{ "scope": { "kind": "stack", "id": "0190…" }, "paths": ["compose.yaml", "config/app.env"],
  "overflow": false, "at": "2026-09-24T12:00:00Z", "seq": 44 }
```

- **Watch set.** The manager declares the complete set of scopes an
  agent watches with the `files.watch` request (`FilesWatchInput {scopes:
  [FileScope]}`, at most 4 096; `internal/protocol/watch.go`): every
  stack of the environment (so external edits of `compose.yaml`,
  overrides and `.env` become revisions, #25 Q1) plus the volumes with an
  open file view (live stream `volume` filters and recent `files.*`
  requests, leased for 5 minutes). It re-sends the set after every
  (re)connect and whenever it changes; scopes not listed stop being
  watched. Each scope is resolved exactly like a file operation (a stack
  directory inside a verified stack root, a supported local volume,
  symlink-free) and the answer `FilesWatchOutput {scopes: [{scope, mode,
  watches, entries, reason}], watchLimit, watchesUsed}` reports how it is
  watched: `inotify`, `poll` (`watch_limit`, `remote_filesystem`,
  `notify_unavailable`) or `unavailable` (`not_found`,
  `unsupported_volume`, `forbidden_path`; retried every 30 s).
- The agent watches only these roots (the stacks volume project
  directories and named-volume roots at their identical host paths, #28),
  recursively with inotify where available: one kernel watch per
  directory, added before the directory is read, new directories added as
  they appear, renamed or removed ones released; symlinks are never
  followed and a directory swapped for a symlink is not watched. Changes
  are debounced 200 ms and coalesced per path. Every scope's watches
  count against one budget (`DOCKER_AGENT_WATCH_MAX`, default half of
  `fs.inotify.max_user_watches`); a scope that does not fit, a remote
  filesystem (NFS, SMB/CIFS, FUSE, Ceph, …) or an agent without kernel
  notifications is polled: bounded reconciliation scans every 30 s
  (at least every 60 s, #25 decision 5) compare a per-directory hash of
  names, sizes, modification times and modes and report the directories
  that changed. inotify scopes are reconciled too, every 10 minutes and at
  once after a kernel queue overflow. Budgets: docs/internal/support-matrix.md
  ("File watching").
- `paths` are root-relative, cleaned, slash-separated and never escape the
  root (`ValidRelativePath`); at most 256 per frame, otherwise `overflow:
  true`. File contents are never sent. Symlinks are not followed out of a root.
- The manager turns invalidations into permission-filtered live events
  ([streams.md](../api/streams.md#file-changes)); names reach only users with
  the matching files-read capability.
- `rescan {scope, path, maxEntries, reason}` (the frame carries a
  `deadline` like a request; the manager sends it after an fs `seq` gap for
  every watched stack scope) makes the agent walk at most `maxEntries`
  entries (≤ 200 000) of the subtree without following symlinks and answer
  `response {output: RescanResult {scope, path, entries, truncated,
  changed}}`: `changed` are the directories whose listing differs from the
  agent's last scan (its baseline is updated); `truncated` (or no baseline
  yet) makes the manager invalidate the whole scope. A scope the agent does
  not watch answers `error not_found`; an agent without the watcher
  `unsupported_request`.
- Paths in `fs_invalidation` name changed entries (inotify) or directories
  whose listing changed (reconciliation): consumers refresh the listing of
  a path's directory and of the path itself.

### Streams

Byte streams carry logs, exec sessions, file transfers and migration data.

```
opener                                  sender/receiver
  ── stream_open {kind, direction, jobId?, input, windowBytes, maxBytes} ─▶
  ◀─ stream_data {seq: 1, data, channel?}   (≤ credit, ≤ 256 KiB each)
  ── stream_credit {bytes} ──────────────────▶  (as the receiver consumes)
  ◀─ stream_close {reason: eof, bytes, sha256}
```

- The `stream_open` frame ID is the stream ID; `stream_data`,
  `stream_credit` and `stream_close` correlate with it.
- **Flow control:** the data sender may have at most the granted credit in
  flight (initial `windowBytes`, default 1 MiB); the receiver grants more with
  `stream_credit` after it has forwarded or written the bytes. There is no
  unbounded buffering anywhere; a slow browser slows the agent's reads.
- `seq` starts at 1 and increases by one; a gap is a stream error. `data` is
  base64 in JSON; at most 256 KiB decoded per frame.
- At most 32 open streams per session; one more is refused with
  `stream_close {reason: limit, code: stream_limit}`.
- `stream_close` reasons: `eof`, `cancelled`, `error` (with a code), `timeout`,
  `limit`; `bytes` and `sha256` let the receiver verify a transfer;
  `exitCode` ends an exec stream.
- Directions are fixed per kind (`StreamDirection`). In v1 only the manager
  opens streams (an agent-opened stream is refused with
  `unsupported_stream`).
- **Credit:** `windowBytes` in `stream_open` is the credit the opener grants
  the agent for data sent to the manager. When the manager sends data
  (`manager_to_agent`, `both`) it starts without credit: the agent grants
  its window with a `stream_credit` as soon as it accepted the stream.
  Receivers grant more credit after the application consumed at least a
  quarter of the window. Data beyond the granted credit, beyond `maxBytes`
  or with a `seq` gap aborts that stream (`error`, `invalid_frame` /
  `too_large`), never the session.
- **Half close:** `stream_close {reason: eof, bytes, sha256}` means "I sent
  everything"; it travels on the same FIFO queue as the sender's
  `stream_data`, so it never overtakes them. The stream ends when both
  sides sent an eof close. Any other reason aborts the stream for both
  sides at once (it overtakes queued data, which the receiver discards).
  A receiver of a one-way stream may close early (e.g. an upload it
  skipped); the sender stops writing.
- **Result:** the side that commits a transfer may attach a kind-specific
  `result` (JSON, at most 16 KiB) to its final eof close
  (`files.upload`: the written entry).
- Stream data frames use their own bounded queue on both session ends
  (they wait for space; credit bounds what one stream can queue); control
  frames (jobs, requests, credits, aborts) are written first. The shared
  implementation is `internal/streammux`.

**Migration transfer relay (#35):** for a stack or volume migration job the
manager opens `migration.receive` on the destination agent and
`migration.send` on the source agent, both with the job ID, once per part
(`project`, each `volume`, `image`; inputs `MigrationReceiveInput` /
`MigrationSendInput` in `internal/protocol/migration.go`). It relays
`stream_data` between them and reads from the source only after the
destination accepted the previous bytes (end-to-end backpressure: at most
the source stream's window plus a 64 KiB copy buffer per part in manager
memory, nothing on disk), optionally rate-limited
(`DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT`).

- **Format** (`internal/transfer`): the magic `DYXFER01`, then chunks
  `uint32 BE length (1..196608) | payload | SHA-256(payload)`, then
  `uint32 0 | uint64 BE total | SHA-256(whole payload) | uint64 BE chunks`.
  The payload of `project` and `volume` parts is a PAX tar archive of the
  tree: the root directory first (`./`), then every entry depth-first in
  sorted order with numeric UID/GID (no names), permission and
  setuid/setgid/sticky bits, nanosecond modification and access times;
  symlinks as links with their target text (never followed), files with
  several hard links once and then as hard links to the first name, FIFOs;
  sockets and device nodes are skipped and listed in the result. `image`
  parts carry the Engine's image save archive.
- **Verification:** the destination's reader returns a chunk only after its
  SHA-256 matched and ends only after the trailer matched; the manager
  verifies the same framing incrementally while relaying; `stream_close`
  adds the stream's own `bytes`/`sha256`. Both agents close with a
  `MigrationPartResult {bytes, sha256, chunks, entries, skipped}` and the
  manager requires source, manager and destination to agree. A mismatch
  (or a lost session) fails the part, which the job retries from its start.
- **Containment:** the source reads only the stack's project directory
  inside a verified stack root and supported local volumes below the
  verified volume directory (never Docker Manager's own volumes, #32). The
  destination writes only into `<stacks>/.docker-manager-migrations/<id>/project`
  (moved to the new project directory by `migration.commit`, which never
  replaces an existing directory) and into volumes it creates itself with
  the label `docker-manager.migration=<id>` (a volume carrying the legacy
  `dev.neureka.docker-manager.migration=<id>` counts too); extraction refuses
  escaping names, members below symlinks or files, hard links to anything
  but earlier regular files and device nodes.
- `migration.cleanup` removes the staging directory and, unless
  `finished`, the committed directory, the project's containers whose
  working directory is that directory, and the volumes labeled with the
  migration's ID.

### Scoped files (#15)

The `files.*` requests, the `files.download` / `files.upload` streams and
the `files.*` job kinds share the types in `internal/protocol/files.go`;
the agent side is `internal/agent/files` (scope checks and wiring over the
shared `internal/fsroot` operations), the manager side
`internal/manager/files` (public API: [files.md](../api/files.md)).

- **Scope:** every input carries `scope {kind, id, dir?}`. `stack` scopes
  name the project directory (`dir`, absolute, identical host path, #28):
  the agent refuses it unless it and its resolved (symlink-free) path lie in
  a verified stack root. `volume` scopes name a Docker volume: the agent
  inspects it and serves only local-driver volumes under the verified volume
  directory (`storage.Result.AccessFor`), never the stacks volume and never
  volumes mounted by Docker Manager's own containers (label
  `docker-manager.role`, or its legacy key `dev.neureka.docker-manager.role`).
  Before the storage check ran nothing is
  served (`unsupported_volume`).
- **Paths** are root-relative, slash-separated, without a leading `/`,
  `.`/`..` segments, backslashes or control characters
  (`protocol.CleanRelativePath`); every access goes through an `os.Root`
  opened on the scope directory, so symlinks are followed only while they
  stay inside and absolute or escaping targets fail with `forbidden_path`.
  In `stack` scopes no symlink is followed at all (agents announcing
  `files.stack_no_follow`): a path through a symlinked directory fails with
  `forbidden_path`, a final symlink is never opened for content (reads,
  raw downloads and writes onto it are refused), and the link itself is
  still listed, stat'ed, renamed, copied as a link and deleted.
- **Content access** (read, download, archive, copy, chmod/chown) is refused
  for regular files with more than one hard link, devices, FIFOs and
  sockets (`unsupported_file`). Recursive operations never follow symlinks.
- **Requests:** `files.list` (`FilesListInput` → `FilesListOutput`: sorted,
  filtered, paged after a cursor; at most 500 entries per answer and
  100 000 scanned), `files.stat` (`FilesStatInput` → `FileEntry`, with the
  content `etag` on request), `files.read` (`FilesReadInput` →
  `FilesReadOutput`: at most 512 KiB, base64 in the frame, binary
  detection), `files.write` (`FilesWriteInput` → `FileEntry`: at most
  512 KiB, exactly one of `ifMatch`, `createOnly`, `overwrite`; written to a
  temporary file and renamed after re-checking the target), `files.mkdir`
  (`FilesMkdirInput`: an empty directory or a new file), and
  `files.conflict_preview` (`FilesPreviewInput` → `FilesPreviewOutput`:
  existing destinations, at most 1000, and the recursive impact, at most
  100 000 entries).
- **ETag** of a regular file: `f1-` + hex(SHA-256 over the content's
  SHA-256, size and modification time), for files up to 256 MiB.
- **`files.download`** (`FilesDownloadInput`): `raw` streams one regular
  file (optional `offset`/`length`); `zip` / `tar.gz` stream an archive of
  the paths (escaping symlinks, hard-linked and special files are listed in
  a final `DOCKER-MANAGER-SKIPPED.txt` member).
- **Limits:** without `limits` the agent applies its built-in ones (upload
  2 GiB, download or created archive 10 GiB, extraction 10 GiB and 100x
  the archive with at least 1 MiB, 100 000 archive entries). With
  `limits` (`FileLimits {maxUpload, maxDownload, maxExtractBytes,
  maxExtractRatio, maxArchiveEntries}`, only from managers that saw
  `files.limits`) each set field replaces its default, capped at 1 TiB
  (`protocol.MaxFileLimitBytes`), a ratio of 10 000 and 1 000 000 entries.
  Larger editable files (a raised manager edit limit) are read with a raw
  `files.download` after `files.read`, and saved with `files.upload` and
  the same precondition; `files.read` and `files.write` stay at 512 KiB.
- **`files.upload`** (`FilesUploadInput`, exactly `size` bytes, optional
  `sha256`, one of `ifMatch` / `createOnly` / `conflict`): the agent checks
  the precondition before storing anything (an early `stream_close` with
  `result.skipped` for `conflict=skip`), writes a temporary file, verifies
  size and digest, re-checks and commits (rename, or a hard link for
  no-clobber names), then closes with `result` = `FilesUploadResult`.
- **Jobs** `files.archive`, `files.extract`, `files.copy`, `files.move`,
  `files.delete`, `files.metadata` take `FilesJobInput`; items report
  per-path outcomes (at most 200, then a summary). Extraction validates
  every entry (no `../`, absolute or drive names, symlinks only when they
  resolve inside the root from where they land, hard links only to
  earlier members, no devices, no setuid bits, nothing below a refused
  link, in `stack` scopes nothing below any link) and limits entries, bytes
  actually written and the expansion ratio (see **Limits**).
- Docker Manager's own changes are published as `fs_invalidation` of the
  changed paths (the watcher of #23 reports external ones).

### Maintenance: previews and prune runs (#14)

Types in `internal/protocol/maintenance.go`; semantics in
[maintenance.md](../architecture/maintenance.md). Implemented by
`internal/agent/prune`.

- `maintenance.preview` input and `prune.run` job input: `PruneInput
  {policyId, rules: [{category, minAgeSeconds, includeLabels, excludeLabels,
  exclude, containerStates, buildCacheAll, keepStorageBytes}], protect:
  {projects, images, volumes, networks: [{ref, reason}]}}` — `policyId` is
  the maintenance setup's ID (or `manual-<uuid>` for a one-off prune); only
  the enabled rules (categories `stopped_containers`,
  `dangling_images`, `unused_images`, `unused_networks`,
  `anonymous_volumes`, `named_volumes`, `build_cache`; at most one rule
  each) and what the manager protects (Docker Manager stacks' Compose projects
  and images, saved container specifications, backup destinations). Strict
  decoding; invalid inputs answer `invalid_argument` before the Engine is
  read.
- `maintenance.preview` output: `PrunePreviewOutput {at, categories:
  [{category, remove, protected, excluded, retained, bytes, unknownSizes,
  items: [{category, id, name, decision (remove|protected|excluded|retained),
  reason, bytes, since}], truncated}]}` (at most 200 items per category).
- `prune.run` steps `collect_candidates` (journals the candidates, at most
  300, as the output) and `delete_candidates` (revalidates each against a
  fresh Engine read, removes it with a targeted call, journals the output
  after every item, honors cancellation between items). Output
  `PruneRunOutput {items: [{category, id, name, status
  (pending|removed|skipped|failed), reason, bytes}], protected, excluded,
  retained, deferred, removed, skipped, failed, bytesReclaimed}`. The agent
  never calls a broad Engine prune endpoint; build cache records are pruned
  one record ID at a time.

### Container logs and exec (#8)

Agent side `internal/agent/containerio`, manager side
`internal/manager/containerio`; wire types `internal/protocol/containerio.go`.

- `container.logs` (request, `ContainerLogsInput` → `ContainerLogsOutput`):
  a bounded tail (default 500, at most 5 000 lines and 600 KiB; `truncated`
  when older lines were left out), `since`/`until` filtered to the
  sub-second by the agent (the Engine filters whole seconds). Lines are
  split at newlines and at 16 KiB (`partial` on all but the last piece).
- `container.logs` (stream, `agent_to_manager`, input
  `ContainerLogsInput`): newline-delimited JSON `LogLine`s (tail or `since`,
  then follow). A stopped container keeps the stream open: the agent polls
  its state (every 2 s) and resumes after the last delivered timestamp,
  skipping lines it already sent at that timestamp. The stream ends with
  `stream_close {reason: error, code: not_found}` when the container is
  removed. The manager decouples the browser with a bounded queue (1 024
  lines per subscriber) and counts dropped lines instead of withholding
  credit from the agent.
- `container.exec.create` (request, `ExecCreateInput` → `ExecCreateOutput`):
  creates an Engine exec instance on a running, unpaused container
  (`conflict` otherwise) with stdin attached. The input names either
  `cmd` (an argv) or `shell` (`auto`, `bash`, `sh`, `zsh`; agents
  announcing `exec.shell` only; both → `invalid_frame`, an unknown shell →
  `invalid_argument`). For a shell the agent runs the first of its paths
  that exists in the container (`protocol.ShellCandidates`, checked with
  the Engine's archive stat: bash `/bin/bash`, `/usr/bin/bash`,
  `/usr/local/bin/bash`; zsh `/bin/zsh`, `/usr/bin/zsh`,
  `/usr/local/bin/zsh`; sh `/bin/sh`, `/usr/bin/sh`, `/busybox/sh`; auto
  the bash paths, then the sh paths) and fails with `command_not_found`
  when none exists. The output's `cmd` is the argv actually run. At most
  256 argv entries / 64 KiB and 256 instances per agent (`busy`). An
  instance that is not attached within 2 minutes is forgotten.
- `container.exec.resize`, `container.exec.delete` (requests): resize the
  TTY; delete closes the instance's stdin (Engine exec has no kill).
- `container.exec` (stream, `both`, input `ExecStreamInput{execId}`): one
  attachment per instance (`conflict` for a second). Manager →
  agent `stream_data` is stdin; an eof close from the manager closes stdin.
  Agent → manager `stream_data` is output with `channel` `stdout` or
  `stderr` (with a TTY everything is `stdout`). When the process exits the
  agent sends its eof close with `exitCode`. A command missing from the
  image (Engine exit 126/127 with a "not found" message in the first
  output) ends with `stream_close {reason: error, code: not_found}` instead.
  Terminal bytes are never logged on either side.

### error

```json
{ "type": "error", "id": "e-1", "correlationId": "q-7",
  "payload": { "code": "forbidden_path", "message": "path escapes the volume root", "retryable": false } }
```

Without `correlationId` it reports a session-level problem and is followed by
a close.

## Allowed job commands

The agent executes only these job kinds (agent-executed kinds of the #26
catalog, `internal/jobspec`; `TestProtocolDocListsCommands` keeps the table
complete). Capability is what the manager checks on every target before
enqueueing and again at dispatch for queued manual jobs.

| kind | frame | capability |
| --- | --- | --- |
| `backup.retention` | command | `backup.retention` |
| `backup.run` | command | `backup.run` |
| `backup.verify` | command | `backup.verify` |
| `container.create` | command | `container.create` |
| `container.pause` | command | `container.pause` |
| `container.recreate` | command | `container.recreate` |
| `container.remove` | command | `container.remove` |
| `container.restart` | command | `container.restart` |
| `container.start` | command | `container.start` |
| `container.stop` | command | `container.stop` |
| `container.unpause` | command | `container.unpause` |
| `container.update` | command | `container.update` |
| `files.archive` | command | `files.archive` |
| `files.copy` | command | `files.copy` |
| `files.delete` | command | `files.delete` |
| `files.extract` | command | `files.extract` |
| `files.metadata` | command | `files.metadata` |
| `files.move` | command | `files.move` |
| `image.build` | command | `image.build` |
| `image.pull` | command | `image.pull` |
| `image.remove` | command | `image.remove` |
| `network.create` | command | `network.create` |
| `network.remove` | command | `network.remove` |
| `prune.run` | command | `maintenance.run` |
| `restore.run` | command | `backup.restore` |
| `stack.build` | command | `stack.build` |
| `stack.deploy` | command | `stack.deploy` |
| `stack.down` | command | `stack.down` |
| `stack.import` | command | `stack.import` |
| `stack.remove` | command | `stack.remove` |
| `stack.pull` | command | `stack.update` |
| `stack.remove_source` | command | `stack.migrate` |
| `stack.rename` | command | `stack.rename` |
| `stack.restart` | command | `stack.restart` |
| `stack.start` | command | `stack.start` |
| `stack.stop` | command | `stack.stop` |
| `stack.update` | command | `stack.update` |
| `update.run` | command | `update.run` |
| `volume.create` | command | `volume.create` |
| `volume.remove` | command | `volume.remove` |

Backup and restore are explicit, capability-gated commands: the agent runs
restic itself against the repository named in the command input, with
bounded paths from the policy; there are no pre/post hooks (#25). The
command input names the destination and the environment scope
(`protocol.BackupRepositoryRef`; the destination's optional `compression`
is restic's `--compression` for backup and prune, see `backup.compression`
above); the repository credentials (the Recovery
Key, during a key rotation also the previous key, and the S3 key pair)
travel only in the command's `secrets.repositories` and are never
journaled. The `backup.snapshots`, `backup.contents` requests and the
`backup.file` stream carry the same credential in their input's
`credential` field for that call only. Destinations are S3 only
(`kind: s3`, #244). External bind paths need both the policy's
opt-in and `DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST`. A `backup.run` member
whose volume the Engine no longer knows (Not Found), or whose stack
project directory was deleted, when its turn comes has `state: skipped`
and `errorClass: item_gone` in the output and the host manifest (job item
status `skipped`); it is not a failure, and a run whose items were all
skipped succeeds. Older agents fail such members (`volume_unavailable`).
Anonymous volumes only temporary containers mount
(`protocol.IsHelperContainer`) are not discovered as a stack's volumes.
Registry credentials needed by a pull, build, deploy or update travel only
inside that command's input for that operation and are never persisted on
the agent (#19).

## Allowed requests

Bounded, non-durable operations. Mutating requests are marked; they are
never re-sent automatically after a disconnect (the caller gets `503
unavailable` and decides). Read-only requests may be retried by the manager
on a new session with a new frame ID. Any request, mutating ones included,
is sent again after a retryable `busy` (the agent refused it before running
it; see "request / response").

| name | frame | capability checked by the manager | mutating | owner |
| --- | --- | --- | --- | --- |
| `engine.info` | request | `environment.system.read` | no | #5 |
| `engine.disk_usage` | request | `environment.metrics.read` | no | #5 |
| `engine.compatibility` | request | manager service (session setup) | no | #21 |
| `host.metrics` | request | `environment.metrics.read` | no | #5 |
| `metrics.live` | request | manager service: about once a second while a browser live stream is open, only to agents that advertise it; the answers are served with `environment.metrics.read` / `container.metrics.read` | no | #5 |
| `host.health` | request | manager service: about once a minute, only to agents that advertise it; checks (`refresh`) with `environment.system.read`; the answers are served with `environment.system.read` | no | #143 |
| `container.list` | request | any container capability (fields shaped per #17; entries carry their network addresses and, while running, `startedAt`) | no | #6 |
| `container.inspect` | request | `container.details.read` (the on-failure restart policy carries `restartMaxRetries` when limited) | no | #6 |
| `container.stats` | request | `container.metrics.read` | no | #5 |
| `container.logs` | request | `container.logs.read` (bounded tail) | no | #8 |
| `container.exec.create` | request | `container.exec` | yes | #8 |
| `container.exec.resize` | request | `container.exec` | yes | #8 |
| `container.exec.delete` | request | `container.exec` | yes | #8 |
| `image.list` | request | `image.read` | no | #6 |
| `image.inspect` | request | `image.read` | no | #6 |
| `image.tag` | request | `image.tag` | yes | #6 |
| `image.local_digests` | request | `update.check` / manager service | no | #20 |
| `volume.list` | request | `volume.read` (a volume's `composeLabels`: Docker Manager labels its stack's Compose file declared at the last deploy that the volume lacks) | no | #6 |
| `volume.inspect` | request | `volume.read` (with `composeLabels`, as `volume.list`) | no | #6 |
| `volume.usage` | request | `volume.read` (sizes of the volumes the caller sees; the manager caches the answer for 60 s per environment and sends it only to agents that advertise it) | no | #6 |
| `network.list` | request | `network.read` | no | #6 |
| `network.inspect` | request | `network.read` (attached `containers` carry their `ipAddress`/`ipv6Address` on the network) | no | #6 |
| `compose.discover` | request | `stack.import` | no | #7 |
| `compose.validate` | request | `stack.create` / `stack.manage` | no | #7 |
| `compose.read` | request | `stack.definition.read`, or the manager service (revision recording, #7) | no | #7 |
| `compose.write` | request | `stack.create` / `stack.import` / `stack.definition.write` (create a project directory or restore a revision; expected hash) | yes | #7 |
| `compose.services` | request | `stack.read` (each container with its state, ports, `networks` and `volumes`: volume mounts only, `anonymous` marked) | no | #7 |
| `compose.rename_preview` | request | `stack.rename` (plans a `stack.rename`: what moves, outside containers, blockers; changes nothing) | no | #7 |
| `files.list` | request | `stack.files.read` / `volume.files.read` | no | #15 |
| `files.stat` | request | `stack.files.read` / `volume.files.read` | no | #15 |
| `files.read` | request | `stack.files.read` / `volume.files.read` (≤ 512 KiB; larger via `files.download`) | no | #15 |
| `files.write` | request | `stack.files.write` / `volume.files.write` (≤ 512 KiB, expected revision) | yes | #15 |
| `files.mkdir` | request | `stack.files.write` / `volume.files.write` | yes | #15 |
| `files.conflict_preview` | request | `stack.files.read` / `volume.files.read` | no | #15 |
| `files.watch` | request | manager service: the watch set (every stack of the environment, volumes with open file views) | no | #23 |
| `backup.snapshots` | request | `backup.read` | no | #10 |
| `backup.contents` | request | `backup.contents.read` | no | #10 |
| `backup.scope_preview` | request | `backup_policy.read` | no | #10 |
| `restore.preview` | request | `backup.restore` | no | #10 |
| `maintenance.preview` | request | `maintenance.preview` | no | #14 |
| `migration.preview` | request | `stack.migrate` / `volume.migrate` | no | #35 |
| `migration.stop` | request | job-linked (`stack.migrate`): stop the source project in reverse dependency order | yes | #35 |
| `migration.start` | request | job-linked (`stack.migrate`): start the services that ran before (rollback) | yes | #35 |
| `migration.commit` | request | job-linked (`stack.migrate`): move the staged project directory into place | yes | #35 |
| `migration.cleanup` | request | job-linked (`stack.migrate` / `volume.migrate`): remove what a migration created on the destination | yes | #35 |
| `agent.credential.rotate` | request | `agent.manage` | yes | #3 |
| `agent.diagnostics` | request | owner (support bundle, redacted) | no | #34 |
| `manager.identity` | request | manager service (after every reconnect, when advertised) | yes | #32, #35 |
| `manager.redirect` | request | manager service (a manager move, just before the old manager refuses agents) | yes | #35 |

`manager.identity {instanceId, containerId?, generation?}` →
`{colocated}`; a `generation` lower than the agent's record is refused
(`error conflict`, then close `4421`), see "Manager generation" under
[welcome](#welcome).

`manager.redirect {url, generation}` → `{}`, then the agent closes the
session with `1001` and reconnects to `url`; see "Manager redirect" under
[welcome](#welcome).

### Observation requests (#5)

Implemented by `internal/agent/observe` (agent) and `internal/manager/observe`
(manager); Go types in `internal/protocol/observe.go`; units and buffering in
[metrics.md](../architecture/metrics.md).

- `engine.info` → `EngineInventory {engineId, hostname, version, apiVersion,
  minApiVersion, negotiatedApiVersion, os, arch, operatingSystem,
  kernelVersion, storageDriver, cgroupVersion, cpus, memoryBytes, rootless,
  dockerDesktop, containers, containersRunning, containersPaused,
  containersStopped, images, volumes, networks, collectedAt}` (counts the
  agent could not read are `-1`; never host paths). `containersRunning`
  counts only the `running` state; every state but `running` and `paused`
  (`exited`, `created`, `restarting`, `dead`, `removing`) counts as
  `containersStopped`. The manager asks after
  every reconnect (a reconciler, before the environment is online), 1 s
  after Docker events or a capabilities change, and every 5 minutes.
- `host.metrics {epoch?, afterSeq?, maxBatches?}` → `HostMetricsOutput {epoch,
  now, intervalSeconds, oldestSeq, lastSeq, more, batches}`. The agent samples
  every 10 s into a ring of 180 batches (30 min); each batch is `{seq, at,
  flags, host, disks, containers, temperatures}` with `seq` increasing per sampler epoch
  (one per agent process). The manager passes its cursor (`epoch`,
  `afterSeq`); another epoch returns the whole ring. An answer holds at most
  60 batches or 768 KiB (`more: true` asks for the next page). `now` is the
  agent clock for skew estimation. Absent values are unknown (gaps).
  `temperatures` (#146, optional: agents before it omit it, and as an
  output field it needs no feature) lists at most 32 host sensors
  `{sensor, celsius}`: `sensor` is the hwmon chip name and label
  (`coretemp: Package id 0`, `nvme: Composite`, `acpitz`; 1–64 bytes of
  UTF-8 without control characters, unique in the batch, never a host
  path), `celsius` −100 to 250; a sensor without a reading is absent.
  The host part's `memoryUsedBytes` is total − available without the ZFS
  ARC; `memoryCacheBytes` (buffers and page cache without shared memory),
  `memoryZfsArcBytes` (absent without ZFS), `swapUsedBytes`,
  `swapTotalBytes` (0 without swap) and `diskReadBytesPerSecond` /
  `diskWriteBytesPerSecond` (the host's whole disks, at most 1 TB/s) are
  optional: older agents omit them and as output fields they need no
  feature. The manager fetches 2 s after each 10 s slot.
- `metrics.live {}` → `LiveMetricsOutput {at, flags, host {cpuPercent,
  cpus, memoryUsedBytes, memoryTotalBytes}, containers [{name, id,
  cpuPercent, memoryBytes, memoryLimitBytes}]}` (`memoryUsedBytes` as in
  `host.metrics`, without the ZFS ARC): the current CPU and memory
  of the host (procfs) and of every running container (one-shot stats),
  read when asked and never buffered or stored (added after the 10 s
  sampler: the manager sends it only to agents whose capabilities list
  it). CPU is the change since the previous `metrics.live` read; a previous
  read older than 5 s (`protocol.LiveMetricsBaselineAge`) is dropped, so
  the first answer after a pause has no CPU. The manager asks every
  second while a browser live stream is open, one request per
  environment at a time (2 s timeout), and simply stops asking when none
  is: there is no lease to release. Containers are read within 800 ms
  (`protocol.LiveMetricsBudget`), at most 1 000; those not reached are
  left out with `flags` `BatchContainersTruncated` (1) and read first by
  the next request; without an Engine `flags` is
  `BatchEngineUnavailable` (2) and only host values are present. Absent
  values are unknown.
- `host.health {refresh?}` → `HostHealthOutput {sampledAt, smart {status,
  message?, checking?, scannedAt?, checkedAt?, intervalSeconds?, devices
  [{name, type, protocol?, model?, serial?, firmware?, capacityBytes?,
  rotationRpm?, smartSupported, passed?, temperatureC?, powerOnHours?,
  temperatureLimitC?, temperatureCriticalC?, overTemperatureMinutes?,
  criticalTemperatureMinutes?,
  reallocatedSectors?, endToEndErrors?, reportedUncorrectable?, pendingSectors?,
  offlineUncorrectable?, failingAttributes? [{id, name, whenFailed}],
  criticalWarning?, availableSpare?, availableSpareThreshold?,
  mediaErrors?, percentageUsed?, grownDefects?, uncorrectedErrors?,
  attributes? [{id, name, value?, worst?, threshold?, raw?, rawText?,
  prefailure?, whenFailed?}], values? [{key, value}], state, errorCode?,
  readAt?}]}, raid {readAt, message?, md [{name, level?, state, readOnly?,
  devices?, active?, sizeBytes?, members [{name, slot, state,
  writeMostly?}], metadata?, chunkBytes?, layout?, bitmap?,
  bitmapChunkBytes?, action?, pending?, progress?, finishSeconds?,
  speedBytesPerSecond?}], zfs [{name, health, state}]}}` (#143, added
  after `metrics.live`: the manager sends it only to agents whose
  capabilities list it; Go types in `internal/protocol/health.go`). The
  disk health of the host: SMART data read with the agent image's
  smartctl (cached, refreshed every `DOCKER_AGENT_SMART_INTERVAL`; a disk
  in standby is not woken and keeps its previous values (not its
  temperature, #212) with state
  `sleeping`, or `failing` / `warning` when the last read found that,
  until it went unread for `DOCKER_AGENT_SMART_WAKE_AFTER`; a disk a later
  scan no longer finds stays listed as `error` `missing` until the agent
  restarts) and the md arrays and ZFS pools read from procfs on every
  request. `intervalSeconds` (added later; older agents omit it) is the
  agent's read interval. `attributes` (the ATA attribute table as read),
  `values` (the other numeric health values by smartctl's JSON key, nested
  keys joined by dots: the NVMe health log, the SCSI error counters and
  start-stop counter, the power cycle count) and the md `metadata`,
  `chunkBytes`, `layout`, `bitmap` and `bitmapChunkBytes` were added later
  (#206; older agents omit them, the details dialogs show what is there).
  `temperatureLimitC`, `temperatureCriticalC` (the drive's own limits,
  -273..1000), `overTemperatureMinutes` and `criticalTemperatureMinutes`
  (lifetime minutes above them) were added with #212; older agents omit
  them.
  `refresh` is empty, `smart` (a fresh scan and read of every
  disk, never a self-test: the agent waits up to 3 s, then answers with
  `smart.checking` and the result comes with a later request) or `raid`
  (never a scrub); anything else is `invalid_argument`. `smart.status` is
  `ok`, `disabled`, `no_access`, `not_installed` or `error`; a device's
  `state` is `ok`, `warning`, `failing`, `sleeping` or `error` (with
  `errorCode` `permission_denied`, `open_failed`, `unsupported`, or, from
  newer agents (the manager is upgraded first), `timeout`, `missing`,
  `smart_disabled` or `no_data`); array
  and pool states are `healthy`, `degraded`, `rebuilding`, `checking`,
  `failed` or `inactive`; `whenFailed` is `now` or `past`. At most 256
  devices, 64 md arrays, 64 pools, 128 members per array, 32 failing
  attributes, 64 attribute rows and 64 values per device (keys and raw
  texts at most 64 bytes; beyond 512 KiB of attribute rows and values in
  one answer the agent leaves them out, last devices first, so the answer
  stays within a frame); the manager validates every bound
  (`HostHealthOutput.Validate`). A device is identified by `name` and
  `type` together (disks behind one RAID controller share its path). A
  read that failed, or read nothing about the disk's health
  (`permission_denied`, `open_failed`, `timeout`, `smart_disabled`,
  `no_data`), reports `state` `error` with the last measurements and
  their `readAt` kept; `readAt` is absent when nothing was ever read. Serial numbers are data, never logged.
  Rules and derivation: [metrics.md](../architecture/metrics.md#host-health).

## Allowed streams

| kind | frame | direction | capability | owner |
| --- | --- | --- | --- | --- |
| `container.logs` | stream | agent_to_manager | `container.logs.read` | #8 |
| `container.stats` | stream | agent_to_manager | `container.metrics.read` | #5 |
| `container.exec` | stream | both | `container.exec` | #8 |
| `files.download` | stream | agent_to_manager | `stack.files.download` / `volume.files.download` | #15 |
| `files.upload` | stream | manager_to_agent | `stack.files.write` / `volume.files.write` | #15 |
| `backup.file` | stream | agent_to_manager | `backup.contents.download` | #10 |
| `migration.send` | stream | agent_to_manager | job-linked (`stack.migrate` / `volume.migrate`) | #35 |
| `migration.receive` | stream | manager_to_agent | job-linked (`stack.migrate` / `volume.migrate`); also a stack's creation from a template (the manager writes the version's tar itself, then `migration.commit` / `migration.cleanup`) | #35 |

`container.exec` runs a process **inside a container** through the Engine
exec API with the argv the user supplied (or the container's own shell the
agent found); it is not a host shell.

## Error codes

Codes of `error` frames and of `stream_close {reason: error}`:

| code | meaning |
| --- | --- |
| `invalid_frame` | envelope or payload failed validation |
| `unsupported_request` | request name not served by this agent |
| `unsupported_stream` | stream kind not served by this agent |
| `version_unsupported` | agent outside the version window (before close 4426) |
| `unauthorized` | credential no longer valid |
| `forbidden_path` | a path escapes its scope root or is not allowed |
| `not_found` | the Engine object or file does not exist |
| `conflict` | the object changed (for example an expected file revision); `manager.identity` from a manager with a lower generation (before close 4421); `manager.redirect` whose generation is lower than the agent's, or equal with another plain-`http` address |
| `deadline_exceeded` | the deadline passed before or during the work |
| `busy` | the agent is at a limit (retryable: its request limit, re-sent by the manager) or a conflicting operation is running |
| `stream_limit` | too many open streams |
| `too_large` | an input or output exceeds its bound |
| `engine_unavailable` | the Docker Engine is unreachable |
| `engine_error` | the Engine rejected the operation (message is sanitized) |
| `invalid_argument` | the Engine rejected the input (for example an invalid tag) |
| `unsupported_api_version` | the Engine's API version is too old for the operation |
| `cancelled` | cancelled by the manager |
| `internal` | unexpected agent failure (details only in the agent log) |
| `already_exists` | the file name exists (create-only, no-clobber) |
| `not_directory` | a path component or the target is not a directory |
| `is_directory` | the target is a directory where a file is needed |
| `unsupported_file` | the entry's content is not served: symlink, device, FIFO, socket, or a regular file with several hard links |
| `unsupported_volume` | the volume cannot be served (non-local driver, remote-backed, Docker Manager's own or the stacks volume, storage not verified) |
| `digest_mismatch` | an upload's bytes do not match its SHA-256 |
| `repository_not_found` | no restic repository exists at the backup location (#10) |
| `recovery_key_rejected` | the Recovery Key does not open the backup repository |
| `repository_locked` | another restic process holds the repository lock |
| `repository_damaged` | the repository check found damaged or missing data |
| `storage_access_denied` | the storage refused the S3 credentials |
| `storage_unreachable` | the storage could not be reached |
| `snapshot_not_found` | the snapshot or the path in it does not exist |
| `restic_unavailable` | the agent image has no restic executable |
| `restic_failed` | restic failed for another reason |
| `snapshot_path_unknown` | a restore names a path the backup does not hold (#10) |
| `path_not_restorable` | a restore names a path outside the stack's project directory and its volumes, or one that cannot be replaced in place |
| `target_missing` | a restore of a volume's file names a volume missing on the host |
| `command_not_found` | none of a terminal shell's paths exists in the container (#8) |

The manager maps them to public errors: `not_found` → 404,
`conflict` → 409/412, `deadline_exceeded` → 504 `timeout`,
`engine_unavailable` and a missing session → 503 `unavailable`,
`forbidden_path` → 422 on the path field, `already_exists` → 409
`file_exists` (412 for create-only preconditions), `not_directory` /
`is_directory` → 409 `file_type_mismatch`, `unsupported_file` → 409
`file_unsupported`, `unsupported_volume` → 409 `volume_files_unsupported`,
`too_large` → 413, `digest_mismatch` → 422 `content_digest_mismatch`,
`command_not_found` → 422 `command_not_found`,
`unsupported_request`/`unsupported_stream` → 501, the rest → 500
`internal` or 502.

## Close codes

| code | name | agent reconnects? | meaning |
| --- | --- | --- | --- |
| 1000 | normal | yes (if still running) | orderly close |
| 1001 | going away | yes | manager shutting down; sent by the agent after answering `manager.redirect` (it reconnects to the new address) |
| 1009 | too large | yes | a frame exceeded 1 MiB |
| 1011 | internal | yes | unexpected failure |
| 1012 | service restart | yes (backoff, credential kept) | sent by the manager: it moved to a new server and refuses agents from now on (the agent must reach the new one at the public URL) |
| 4400 | protocol error | yes | invalid frame, hello missing or late, unexpected frame for the session state |
| 4401 | unauthorized | no | credential stopped being valid (e.g. rotation completed elsewhere) |
| 4403 | revoked | no | agent removed or replaced; re-enroll |
| 4408 | heartbeat timeout | yes | nothing received for 45 s |
| 4409 | replaced | no | a newer session with the same credential took over |
| 4421 | manager superseded | yes (backoff, credential kept) | sent by the agent: the manager's generation (welcome or `manager.identity`) is lower than the highest the agent has seen (an old manager after a move) |
| 4426 | version unsupported | no | outside the N-1 window or protocol not negotiated; upgrade |

## Deadlines, idempotency and replay

- Every `command` and `request` has a `deadline`. The agent never starts
  work after it (`ack deadline_exceeded` / `error deadline_exceeded`) and
  aborts requests that run past it.
- Commands are exactly-once by construction: fencing tokens reject replays
  and superseded dispatches, the journal survives agent restarts, and the
  manager reconciles from `job_report` instead of re-sending (#26).
- Frame IDs are unique per sender and session; duplicates are dropped.
  Events and invalidations carry `seq` for gap detection and deduplication.
- Mutating requests are not retried automatically; read-only requests are.
- Public API idempotency keys never reach the agent: the manager resolves
  them before anything is sent ([conventions.md](../api/conventions.md#retries-and-idempotency-keys)).

## Security checklist for implementations

- Reject unknown frame types, fields, request names and stream kinds.
- Resolve every file path beneath its scope root without following symlinks
  out of it (openat2 `RESOLVE_BENEATH` semantics or an equivalent walk);
  re-check at use time (TOCTOU corpora in `internal/testutil/fscorpus`, #29).
- Enforce size limits while reading, not after.
- Engine access only through the Moby adapter (#21); never the docker CLI.
- Never log credentials, tokens, file contents, Compose/`.env` values, exec
  input or output.

## Implementation status

| part | where | status |
| --- | --- | --- |
| envelope, strict decode, size limit, frame types, payload types and validation, allowlists, limits, close codes, version window | `internal/protocol` | implemented (#4, #26) with unit tests |
| job command semantics, fencing, journal, reconciliation | `internal/protocol/jobs.go`, `internal/manager/jobs`, `internal/agent/jobs` | implemented (#26) |
| `/agent/v1/enroll`, `/agent/v1/session`, handshake, heartbeats, close codes, requests, rotation, event/invalidation relay with sequence numbers | `internal/manager/agents` (manager), `internal/agent/{enroll,session,state,runtime}` (agent) | implemented (#3) |
| job dispatch over the session (`jobs.AgentDispatcher`) and job frame routing, reconcile-before-online | `internal/manager/agents` (`Hub`), `internal/agent/session` + `internal/agent/jobs` | implemented (#3) |
| byte streams: open/accept, credit flow control, half close, results, aborts, limits | `internal/streammux` (both ends), `agents.Session.OpenStream` / `Hub.OpenStream`, `session.Options.Streams` | implemented (#15) |
| scoped files: `files.*` requests, `files.download` / `files.upload` streams, `files.*` job executors | `internal/agent/files`, `internal/manager/files` | implemented (#15) |
| `engine.info`, `host.metrics`, `metrics.live`, Docker event relay (coalescing, rate bound) | `internal/agent/observe`, `internal/manager/observe` | implemented (#5) |
| `host.health` (SMART through smartctl, md and ZFS state) | `internal/agent/health`, `internal/agent/smartctl`, `internal/manager/observe` | implemented (#143) |
| Docker resource requests (`container.list/inspect`, `image.list/inspect/tag`, `volume.list/inspect/usage`, `network.list/inspect`) and executors (`container.*`, `image.pull/remove`, `volume.*`, `network.*`) | `internal/protocol/docker.go` (inputs/outputs), `internal/agent/resources` | implemented (#6) |
| `files.watch` watch set, scoped filesystem watcher (inotify, debounce, rename handling, watch-limit accounting, bounded reconciliation), `rescan` | `internal/agent/watch`, `internal/manager/files` (`Watcher`), `agents.Session.Rescan` | implemented (#23) |
| agent-opened streams (manager answers `stream_close` `unsupported_stream`) | stub | not needed in v1 |
| `compose.discover/validate/read/write/services` requests, `stack.deploy/start/stop/restart/down/remove` executors, result `output` | `internal/agent/stacks`, `internal/jobexec`, `internal/manager/stacks` | implemented (#7) |
| `stack.build` executor (input `noCache`, `pullBase`, `buildTimeoutSeconds`; output `built`; a `stack.deploy` with `pull: always` and `build: true` also carries `pullBase`) | `internal/agent/stacks`, `internal/agent/buildrun` | implemented (#33) |
| container logs (`container.logs` request and stream) and exec (`container.exec.create/resize/delete`, `container.exec` stream) | `internal/agent/containerio`, `internal/manager/containerio` | implemented (#8) |
| `maintenance.preview` request and `prune.run` executor | `internal/protocol/maintenance.go`, `internal/agent/prune` | implemented (#14) |
| backups and restores (`backup.snapshots/contents/scope_preview`, `restore.preview` requests, `backup.file` stream, backup/restore/verification executors) | `internal/protocol/backup.go`, `internal/agent/backups`, `internal/restic` | implemented (#10, #24, #28) |
| environment migration (`migration.preview/stop/start/commit/cleanup` requests, `migration.send`/`migration.receive` streams relayed by the manager) | `internal/protocol/migration.go`, `internal/agent/migration`, `internal/manager/migrations` | implemented (#35) |
| digest-driven updates (`update.run` executor) | `internal/protocol/updates.go`, `internal/agent/stacks/update.go` | implemented (#20) |
| self-protection (`manager.identity` request), manager generation check (welcome and `manager.identity`) | `internal/agent/protect`, `internal/agent/session` (`AcceptWelcome`), `internal/agent/state` (`manager.json`) | implemented (#32, #35) |
| `manager.redirect` (agent side: validate, persist, switch the transport, reconnect) | `internal/agent/runtime` (`redirect.go`), `internal/agent/transport` (`NewRedirected`), `internal/agent/config` (`ParseRedirectURL`), `internal/agent/state` (`manager.json`), `internal/agent/session` (`Target`, `EndSessionError.Output`) | implemented (#35) |
| Engine access for all of the above | `internal/agent/engine` (Moby adapter), `internal/agent/compose` | implemented (#21) |
