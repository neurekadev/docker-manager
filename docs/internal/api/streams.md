# Streams (`/api/v1`)

OpenAPI documents how each stream is **opened** (path, parameters, auth,
the HTTP errors returned before the stream starts). This document specifies
what happens on the wire afterwards (#4, #23, #27). Every streaming route of
the [route inventory](../../../api/route-inventory.yaml) is listed here
(`TestStreamRoutesDocumented`); all of them are implemented.

| route | operation | transport | owner |
| --- | --- | --- | --- |
| `GET /live/stream` | `stream-live-events` | SSE | #23 |
| `GET /jobs/{jobId}/events/stream` | `stream-job-events` | SSE | #26 |
| `GET /environments/{environmentId}/events/stream` | `stream-environment-events` | SSE | #5 |
| `GET /stacks/{stackId}/events/stream` | `stream-stack-events` | SSE | #7 |
| `GET /environments/{environmentId}/containers/{containerId}/logs/stream` | `stream-container-logs` | SSE | #8 |
| `GET /environments/{environmentId}/containers/{containerId}/exec-sessions/{sessionId}/stream` | `stream-container-exec-session` | WebSocket | #8 |
| `GET /stacks/{stackId}/files/downloads` | `download-stack-files` | binary response | #15 |
| `GET /environments/{environmentId}/volumes/{volumeId}/files/downloads` | `download-volume-files` | binary response | #15 |
| `POST /stacks/{stackId}/files/uploads` | `upload-stack-files` | binary request | #15 |
| `POST /environments/{environmentId}/volumes/{volumeId}/files/uploads` | `upload-volume-files` | binary request | #15 |
| `GET /backups/{backupId}/contents/download` | `download-backup-content` | binary response | #10 |
| `GET /audit/exports` | `export-audit-events` | NDJSON/CSV response | #30 |
| `GET /system/metrics` | `get-system-metrics` | Prometheus text response | #34 |
| `GET /support-bundle` | `get-support-bundle` | zip response | #34 |

Paths are relative to `/api/v1`.

## Rules for every stream

- **Authentication** is the same as for JSON routes: the session cookie
  (browsers; `EventSource` and browser WebSockets send it automatically on the
  same origin) or `Authorization: Bearer <API token>` (other clients). Tokens
  never go into URLs. A revoked, expired or disabled token ends its open
  streams like an ended session (SSE `event: close`, reason
  `session_expired`), within the sweep interval for expiry.
- **Authorization** is checked when the stream opens and again whenever the
  caller's permissions, session or token change. A revoked permission ends
  the stream (SSE: `event: close` with reason `permissions_changed`;
  WebSocket: close `4403`). Events are filtered per subscriber: a user never
  receives an event, count or file name for a resource they may not see.
- **Before the stream starts** failures are ordinary JSON errors
  ([errors.md](errors.md)) with the usual status. After the `200`/`101`
  they are reported in-band (SSE `event: error` / `event: close`, WebSocket
  close codes), never as a second HTTP status.
- **Headers:** `Cache-Control: no-store` on everything, `X-Accel-Buffering:
  no` on SSE (proxies must not buffer), `Content-Type: text/event-stream`.
  The service worker never caches streams (#23).
- **Heartbeats:** SSE sends a `: heartbeat` comment every 15 s; exec
  WebSockets are pinged every 15 s (`DOCKER_MANAGER_STREAM_HEARTBEAT`, one
  implementation each: `internal/manager/server/sse`, `internal/manager/server/ws`).
  Both are shorter than common proxy idle timeouts (#27,
  [deployment.md](../deployment.md#timeouts-and-heartbeats)).
- **Max age:** the server ends SSE streams after 1 h (`event: close`,
  reason `max_age`) so long-lived connections re-authenticate; clients
  reconnect immediately with `Last-Event-ID`.
- **Concurrency limits:** 8 live streams per user or API token and 4 exec
  sessions per user or token (8 per container); excess opens get `429
  rate_limited`. Job, environment, stack and log streams have no count
  limit of their own: each is bounded by its queue, the max age and the
  permission re-checks. Browsers should use one live stream per tab and
  HTTP/2 via the proxy (#27).
- **Backpressure:** every subscriber has a bounded queue. The manager never
  blocks producers (agents, the job engine) on a slow client: SSE
  invalidation streams drop the queue and send `reset` (reason `overflow`);
  log streams drop lines and report `dropped`; byte streams (exec, files)
  slow down end to end through agent stream credit
  ([agent-v1.md](../protocol/agent-v1.md#streams)).

## SSE framing

```
id: 812
event: invalidate
data: {"topic":"containers","kind":"container","resourceId":"…","environmentId":"…","revision":17,"action":"updated","at":"2026-09-24T12:00:00Z"}

: heartbeat

```

- `data` is always one line of JSON; `event` names the payload schema.
- `id`, when present, is the resume cursor. On reconnect the client sends it
  back as `Last-Event-ID` (EventSource does this automatically); non-browser
  clients send the header themselves. Events without `id` (snapshots, hello,
  close) are not resumable positions.
- `retry: <ms>` may be sent to tune EventSource reconnect delay (default
  3000 ms). Clients add jitter and back off exponentially (cap 30 s) after
  repeated failures.

## Live invalidation stream (#23)

Implemented (`internal/manager/live`, handler `internal/manager/api/live.go`,
browser client `web/src/lib/live`). `GET /api/v1/live/stream` is the one
multiplexed, permission-filtered, versioned stream each open UI tab keeps. It
carries **invalidations**, never resource bodies, file contents or secrets:
the client refetches affected queries with the generated API client.

Query parameters (all optional):

| parameter | meaning |
| --- | --- |
| `topics` | comma-separated topics (default: every topic). Unknown topics are `422`; topics the caller may not see simply produce nothing |
| `environmentId` | only events of this environment (instance-wide events such as policies, settings and jobs without an environment still pass) |
| `stackId` | comma-separated stack IDs (at most 16): narrows `files.changed` to these stacks (the tab's open file views) |
| `volume` | comma-separated `<environmentId>/<volume name>` (at most 16): narrows `files.changed` to these volumes **and keeps them watched** by the agent while the stream is open (needs `volume.files.read`; volumes are otherwise watched only for 5 minutes after a listing) |
| `templateId` | comma-separated template IDs (at most 16): narrows `files.changed` to these templates' drafts (served by the manager; `scope.kind` is `template`, no environment) |
| `cursor` | resume position for clients that reconnect with a new `EventSource` (which cannot send `Last-Event-ID`); `Last-Event-ID` wins |

Whole-environment file invalidations reach every file view of the
environment whatever the narrowing. A tab whose open views change
reconnects with its cursor; nothing is lost.

Topics: `environments`, `agents`, `containers`, `images`, `volumes`,
`networks`, `stacks`, `jobs`, `files`, `policies`, `backups`, `registries`,
`settings`, `permissions`, `metrics`, `templates`.

### Events

| event | id | data | client action |
| --- | --- | --- | --- |
| `hello` | — | `{version: "docker-manager.live/v1", cursor, heartbeatMs, topics, resumed}` | `resumed: false`: (re)fetch every open view (the snapshot), then apply events after `cursor`; `resumed: true`: the missed events follow, cached data stays valid |
| `invalidate` | cursor | `{topic, kind, resourceId, environmentId?, revision?, action: created\|updated\|deleted, at}` | Invalidate queries for that resource and its lists; `revision` (when the resource has one) may be compared with the cached one |
| `job` | cursor | `{jobId, kind, state, environmentId?, progressPercent?, revision, at}` | Update job badges and lists; open `/jobs/{jobId}/events/stream` for detail |
| `agent` | cursor | `{environmentId, status: online\|offline, at}` | Show connection state; data of an offline environment is stale |
| `files.changed` | cursor | see [file changes](#file-changes) | Invalidate listings; editor conflict handling |
| `permissions.changed` | — | `{at}` | Drop **all** cached data, refetch `/me/permissions`, then reconnect; `close` follows at once |
| `reset` | — | `{reason: cursor_expired\|gap\|overflow\|server_restart, cursor, environmentId?}` | Discard cached data (only that environment's with `environmentId`), refetch, continue from the new `cursor` |
| `close` | — | `{reason: permissions_changed\|session_expired\|max_age\|shutdown}` | Stream ends; reconnect (after re-authentication for `session_expired`) |

What the sources are: Docker events relayed by agents (#5) → `invalidate`
on `containers`/`images`/`volumes`/`networks`; environment and agent state
and enrollments → `environments`/`agents` (online/offline as `agent`);
Engine inventory refreshes → `invalidate` kind `inventory`; new metric
samples → `invalidate` topic `metrics` (at most every 10 s per
environment); stacks and their revisions (#7, including revisions the
watcher recorded after an external edit) → `stacks`; jobs (#26: created,
state, progress) → `job`; file-scope invalidations (#15/#23) →
`files.changed`; every successful API mutation of other resources
(policies, schedules, backups and repositories, registry and Git
credentials, build definitions, settings, groups, users, invitations, API
tokens) → `invalidate` on `policies`, `backups`, `registries`, `images`,
`settings` or `permissions` with `kind` the resource type.

- **Snapshot + cursor:** `hello` is sent first and fixes the cursor before
  the client fetches; anything that changes after it arrives as an event, so
  no change falls between snapshot and stream.
- **Replay and gaps:** the manager keeps a bounded replay log (the newest
  10 000 records or 15 minutes). Reconnecting with a `Last-Event-ID` (or
  `cursor`) inside the log replays the missed events (`hello.resumed:
  true`); a cursor older than the log gets `reset` `cursor_expired`, one of
  another manager process `reset` `server_restart`. When the manager itself
  lost events (its bus subscription overflowed) every stream gets `reset`
  `gap`; when an agent reconnected or lost events (a sequence gap) the
  streams get `reset` `gap` with that `environmentId`, before the
  environment is reported `online` again (#3, #26).
- **Deduplication and coalescing:** ids increase strictly; clients ignore
  an event whose `id` is not above the last one applied. The manager
  coalesces repeated events of one resource: the first in a 250 ms window
  is sent at once, the rest are merged (file paths united; beyond 256 paths
  an overflow) and sent once when the window ends; metrics per environment
  use a 10 s window. Duplicate agent events are dropped by their agent
  `seq` before they reach the bus.
- **Backpressure:** each stream has a queue of 512 records. A client that
  falls behind loses its queue and gets `reset` `overflow` with a fresh
  cursor; producers (agents, the job engine) are never blocked. At most 8
  live streams per user or API token (`429 rate_limited`).
- **Permissions:** every record is filtered per subscriber with the #17
  event rules (`authz.EventVisible`) and shaped to identity and action
  only: no attributes, no names of resources the caller cannot see, file
  paths only with the scope's files-read capability. Container events of a
  container seen only through `container.metrics.read` are status
  invalidations; such a user receives nothing about files, jobs, policies
  or other containers. A permission change ends the stream with
  `permissions.changed` and `close permissions_changed`; the client drops
  its cache and reconnects under the new rules, so a revoked user keeps
  nothing privileged on screen. Ended sessions and revoked or expired API
  tokens close with `session_expired`.
- **Fallback polling:** if the stream cannot be kept (three failed
  connections within 60 s, or a proxy that buffers SSE), clients poll open
  views — at most every 10 s for an open detail view and every 30 s for lists
  and metrics — and stop polling once the stream reconnects. Clients never
  queue mutations while offline (#25 decision 4).
- **Measured load** (`TestLiveHighEventVolume`,
  `TestLiveSustainedVolumeIsLossless`): 20 sessions × 10 000 events are
  delivered losslessly at about 400 000 deliveries/s when the waves fit the
  queues; an unbounded flood ends in resets (never silent gaps) within
  milliseconds.

### File changes

External create/modify/rename/delete events in stack and volume roots
(reported by the agent watcher, [agent-v1.md](../protocol/agent-v1.md#fs_invalidation-and-rescan-15-23))
and changes made through Docker Manager's file manager arrive as:

```json
{"scope":{"kind":"stack","id":"0190…","environmentId":"0190…"},"paths":["compose.yaml"],"overflow":false,"at":"…"}
```

- Sent only to subscribers with the scope's files-read capability
  (`stack.files.read` / `volume.files.read`); others get nothing — not even
  the path names. Contents are never sent. Scope `kind: environment`
  (`overflow: true`, no paths: the agent lost file notifications) reaches
  everyone who sees the environment and means "refresh every file view of
  it".
- `paths` name changed entries or directories whose listing changed
  (reconciliation scans report directories): refresh the listing of each
  path's directory and of the path itself, and the metadata and content of
  the path. `overflow: true` (or a `reset`) means "refetch the whole scope".
- An open editor whose file is in `paths` keeps the buffer, shows the
  external change, and saves only with `If-Match` of the version it loaded;
  a stale save gets `412` with the current `ETag` and the UI offers compare /
  reload / save-as / overwrite (#15). Unsaved text is never replaced
  silently.
- An external edit of a stack's `compose.yaml`, override or `.env` is also
  recorded as a stack revision (source `external`, "undeployed changes",
  #25 Q1) about a second later and announced on `stacks`.
- Latency: local volumes within 2 s at p95 (measured 200 ms with the 200 ms
  debounce), remote or unsupported mounts within 30 s (#25 Q5; budgets in
  [support-matrix.md](../support-matrix.md#file-watching-23)).

### How the other streams relate

Job, environment, stack, log and terminal streams keep their dedicated
protocols. They share the resource IDs of the live stream (`jobId`,
`environmentId`, stack IDs, container names), the same authentication,
heartbeats, max age and close reasons, and are ended the same way by a
permission change. A client typically keeps the live stream for
invalidation and opens a dedicated stream only for a view that shows a
job's log or a container's output.

## Job events (`stream-job-events`)

Implemented (#26). First `event: job` with the full `Job` (no id), then
every retained event after `Last-Event-ID`: `id: <seq>`, `event: state |
progress | item | log | warning`, `data: JobEvent`. The stream closes after
the job's terminal events, or when the job is deleted by retention. When
the caller's permissions change (#17: rule edit or group move) or the
session ends, the stream ends with `event: close`, `data: {"reason":
"permissions_changed"}` (or `"session_expired"`); reconnect to be filtered
by the new permissions. Visibility is `job.read` on every target of the job,
or holding the job kind's own capability on every target. The per-job event log is bounded (newest 500), so
replay covers only retained events; `Last-Event-ID` must be a non-negative
integer (`422` otherwise).

## Environment and stack events

`stream-environment-events` (#5) relays Docker Engine events
(container/image/volume/network lifecycle, health) for one environment,
filtered per resource capability. Framing as above; `event: engine`,
`data: {type, action, resourceId, attributes, at}`, `id` is a
per-environment cursor with a bounded in-memory replay (1 000 events);
outside it the stream starts with `reset`. Attributes are an allowlist (no
environment variables or secret labels).

`stream-stack-events` (#7, implemented) needs `stack.read`. It starts with
`event: stack` (the current `Stack`, no id), then sends, as they happen:
`stack.updated` (status, job started or finished, Engine state observed,
metadata), `stack.revision_recorded` (a new revision of the definition:
`attributes.source`, `attributes.seq`), `stack.removed` (the stream then
ends) and `engine` events of the stack's containers the caller may see
(when the agent relays the Compose project label). `id` is the manager bus
sequence; there is no replay: after a reconnect the new `stack` snapshot is
the state, so clients refetch the stack (and its services or revisions) on
every event. Attributes never contain file contents or `.env` values.

### `stream-environment-events` (implemented, #5)

Opening needs `environment.events.read` on the environment (`403` when the
environment is visible without it, `404` when it is not visible). Every
event is then filtered per subscriber with the #17 event rules
(`authz.EventVisible`) and shaped per view:

| event | id | data | who receives it |
| --- | --- | --- | --- |
| `hello` | — | `{version: "docker-manager.environment-events/v1", cursor, heartbeatMs}` | everyone, first |
| `reset` | — | `{reason: server_restart\|cursor_expired\|gap\|overflow, cursor}` | after `hello` when `Last-Event-ID` cannot be resumed, or when the manager lost events |
| `engine` | cursor | `{type, action, resourceId, attributes, at}` | holders of any capability on the resource (containers and networks by name). For a container seen only minimally (e.g. metrics-only or restart-only) `attributes` keep only `name`, `exitCode` and `health`; `image` and `signal` need `container.details.read`. |
| `status` | cursor | `{environmentId, status: online\|offline\|resync\|updated\|archived\|reattached, reason?, at}` | everyone who sees the environment; `resync` (reason `reconnect` or `event_gap`) means refetch the environment's inventory |
| `metrics` | cursor | `{environmentId, host, containers, at}` | new samples: `host` with `environment.metrics.read`, `containers` with `container.metrics.read` on at least one sampled container; refetch open charts (at most every 10 s per environment) |
| `inventory` | cursor | `{environmentId, at}` | `environment.system.read` or `environment.metrics.read`: refetch system information and capacity |
| `close` | — | `{reason: max_age\|permissions_changed\|session_expired}` | stream ends |

- **Replay:** the manager journals, per environment, the newest 1 000
  events or the last 15 minutes (`internal/manager/observe`, `Journal`).
  `id` is `<journal epoch>.<sequence>`; a cursor from before a manager
  restart gets `reset` `server_restart`, one older than the retained events
  `reset` `cursor_expired`. Replayed events are filtered again with the
  caller's current permissions.
- **Limits:** each stream has a queue of 256 events; when a slow client lets
  it overflow, the queue is dropped and the stream sends `reset` `overflow`
  with a new cursor. If the journal itself falls behind the bus, every
  environment's streams get `reset` `gap`. The agent coalesces repeated
  Docker events (the same action on the same resource within 1 s), drops
  noise (`exec_*`, `attach`, `top`, volume `mount`, …) and rate-limits
  relayed events (50/s, burst 200); anything dropped for the rate becomes a
  sequence gap, which the manager turns into `status` `resync`.
- **Heartbeat and max age** as for every stream (`: heartbeat`, `close`
  `max_age` after 1 h).

## Container logs (`get-container-logs`, `stream-container-logs`, #8)

Both need `container.logs.read` on the container (users and API tokens
alike; `container.metrics.read`, `container.restart` or
`container.details.read` never open logs). Log lines can contain secrets:
the manager never persists, logs or audits them, and responses are
`no-store`. Stack service logs are the logs of the service's containers:
list them with `GET /stacks/{stackId}/services` and read or follow each
container (a grant on the stack or service covers its containers; there
is no separate stack-level logs route).

`GET …/containers/{containerId}/logs?tail=500&since=&until=&stdout=true&stderr=true`
returns `{lines: [{at, stream, line, partial?}], truncated}`, oldest first:
at most `tail` lines (default 500, max 5 000) and 600 KiB; `truncated`
says older lines were left out. `since`/`until` are RFC 3339 (sub-second
precision is applied by the agent, the Engine only filters whole seconds).

`GET …/logs/stream?tail=200&since=<RFC 3339>&stdout=true&stderr=true`

- `event: log`, `id: <RFC 3339 nano timestamp>`, `data: {at, stream:
  stdout|stderr, line, partial?}`. Lines longer than 16 KiB are split
  (`partial: true` on all but the last piece); invalid UTF-8 becomes U+FFFD.
- Reconnect with `Last-Event-ID` resumes at that timestamp and skips the
  lines already delivered with exactly that timestamp; clients should still
  deduplicate by `(id, line)` after a reconnect.
- `event: dropped {count}` when the client could not keep up (the manager
  keeps at most 1 024 lines per subscriber and drops the rest; the agent
  stream is never blocked by a slow browser).
- `event: end {reason}` ends the stream: `container_removed`,
  `permissions_changed` (re-checked at every heartbeat and on permission
  changes) or `agent_offline`. A stopped container keeps the stream open;
  it continues when the container starts again.
- `event: close {reason: max_age}` after 1 h (reconnect with
  `Last-Event-ID`); `session_expired` when the session or token ends.

## Container exec (`create-container-exec-session`, `stream-container-exec-session`, #8)

Terminals are authorized with `api.AuthorizeExec` only: `container.exec`
on the container, which an API token must hold in its own grants (#31);
no other capability (restart, metrics, logs, details) opens a terminal. The
command runs inside the container through the Engine's exec API; Docker Manager
never offers a shell on the host.

1. `POST …/containers/{containerId}/exec-sessions` with `{shell?: "auto" |
   "bash" | "sh" | "zsh", command?: [argv…], tty?: true, cols?: 80, rows?: 24,
   workingDir?, user?}` → `201 {id, streamUrl, subprotocol: "docker-manager.exec.v1",
   ticket, expiresAt, command}`. `shell` and `command` are mutually exclusive
   (`422 validation_failed` on `body.shell`); without either the shell is
   `auto`. For a shell the agent starts the first of its usual paths that
   exists in the container: bash `/bin/bash`, `/usr/bin/bash`,
   `/usr/local/bin/bash`; zsh `/bin/zsh`, `/usr/bin/zsh`,
   `/usr/local/bin/zsh`; sh `/bin/sh`, `/usr/bin/sh`, `/busybox/sh`; `auto`
   is Bash if present, else sh. None found → `422 command_not_found`
   (choose another shell). Agents older than the shell lookup get the
   shell's most common path (`/bin/bash`, `/bin/zsh`, else `/bin/sh`). The
   response's `command` is the argv actually started. The container must be
   running and not paused (`409` otherwise). Limits: 4 open sessions per principal, 8 per container
   (`429 rate_limited`). The session start is audited (`container.exec` with the session
   ID and whether a TTY was requested; never the command's output).
2. `GET {streamUrl}` upgrades to a WebSocket. The client offers two
   subprotocols: `docker-manager.exec.v1` and `docker-manager.ticket.<ticket>` (browsers
   cannot set headers on WebSockets; the ticket never goes into the URL).
   The ticket is one-use, bound to the session and to the principal that
   created it, and expires with `expiresAt` (60 s after creation; the
   session is discarded if nobody attached). The server selects
   `docker-manager.exec.v1`. Cookie-authenticated upgrades must carry an `Origin`
   equal to the manager's public origin (`403` otherwise). A wrong, reused
   or expired ticket, or a session that is not the caller's, is refused
   with `404` before the upgrade (browsers only see a failed handshake,
   close `1006`); a second attachment while one is active is closed with
   `4409`.

Messages:

| direction | message | meaning |
| --- | --- | --- |
| client → server | binary, first byte `0` | stdin bytes |
| server → client | binary, first byte `1` / `2` | stdout / stderr bytes (with a TTY everything is `1`) |
| client → server | text `{"type":"resize","cols":120,"rows":40}` | terminal size |
| server → client | text `{"type":"exit","code":0}` | the process exited; close `1000` follows |
| server → client | text `{"type":"error","code":"…","message":"…"}` | failure (`command_not_found`, `environment_offline`, `internal`); a close follows |

- Messages from the client are at most 64 KiB (`1009` otherwise); binary
  messages must start with `0`, text messages must be `resize` (`1008`
  otherwise). Output is relayed through the agent stream's credit window,
  so a slow client slows the process's output instead of growing buffers.
- Idle timeout 30 min without input or output and a maximum session length
  of 8 h (both `4408`). `container.exec` is re-checked every 15 s and on
  permission changes (`4403`); signing out or revoking the token closes
  with `4401`.
- `DELETE …/exec-sessions/{sessionId}` (`204`) ends the session: it closes
  stdin and the WebSocket (`1000`). Processes that ignore end-of-input keep
  running inside the container until they exit (Engine exec has no kill).
- When the WebSocket closes for any reason the manager closes the agent
  stream, which closes the process's stdin; the session end is audited
  (`container.exec.end` with session ID, reason, close code, duration and
  exit code).
- An explicit command (or a shell on an older agent) that does not exist in
  the image (for example `/bin/sh` in a distroless image) is reported as
  `error` `command_not_found` and close `4422` ("the image may have no
  shell"); pick another shell or command. With the shell lookup the create
  call already answers `422 command_not_found`.
- Terminal input and output are never logged, audited or stored.

Close codes:

| code | meaning | client |
| --- | --- | --- |
| 1000 | process exited (after `exit`) or session deleted | show exit code |
| 1001 | manager shutting down | offer reconnect (new session) |
| 1008 | policy violation (bad Origin, subprotocol or message) | — |
| 1009 | message too big | — |
| 1011 | internal error | offer retry |
| 4401 | session expired / token revoked | sign in |
| 4403 | `container.exec` revoked | — |
| 4404 | exec session ended or expired while the upgrade was in progress | create a new session |
| 4408 | idle timeout or maximum duration (8 h) reached | create a new session |
| 4409 | already attached | — |
| 4422 | command not found in the container | choose another command |
| 4503 | environment offline (agent disconnected) | wait for the agent |

## File downloads and uploads (#15)

Implemented. Path encoding for every scoped file route: `path` query
parameters are root-relative, slash-separated, percent-encoded UTF-8; no
leading `/`, no `.`/`..` segments, no backslash, NUL or control characters,
at most 4 096 bytes (empty or `.` is the root). The manager checks them, the
agent re-checks containment beneath the root without following symlinks out
of it; violations are `422` with a `query.path` detail. Raw host paths never
appear in responses. Details and the other file routes:
[files.md](files.md).

**Download** `GET …/files/downloads?path=a/b.txt[&path=…][&format=zip|tar.gz]`

- One regular file (without `format`): raw bytes, `Content-Type:
  application/octet-stream`, `Content-Disposition: attachment;
  filename*=UTF-8''…`, `Content-Length`, `Accept-Ranges: bytes`, `ETag` (files
  up to 256 MiB); a single `Range: bytes=a-b` request answers `206` with
  `Content-Range` (`416 range_not_satisfiable` outside the file). The range
  is not tied to the ETag: resuming clients compare the ETag themselves.
- Several paths, a directory, or `format`: a streamed archive (`zip` by
  default, `application/zip` / `application/gzip`), no `Content-Length`, no
  `Range`. Symlinks are stored only when they resolve inside the root;
  escaping symlinks, hard-linked and special files are skipped and listed in
  a final `DOCKER-MANAGER-SKIPPED.txt` entry.
- Limits (agent): 10 GiB per download or archive, 100 000 entries.
- The manager waits for the first bytes before answering, so refusals
  (`404`, `409 file_unsupported`, `413`, …) are ordinary JSON errors. A
  failure after the first byte aborts the connection (the client sees a
  truncated download and must not treat it as complete; archives then lack
  their end record). A client disconnect cancels the agent's stream.
- Backpressure: the agent reads the file only as fast as the manager
  forwards bytes to the client (stream credit, 1 MiB window).

**Upload** `POST …/files/uploads?path=dir&name=file.txt[&conflict=overwrite|skip|keep_both]`

- Body: raw bytes, `Content-Type: application/octet-stream` (`415`
  otherwise), `Content-Length` required (`411 length_required`), at most
  `DOCKER_MANAGER_FILES_MAX_UPLOAD_MB` (default and maximum 2048 MiB, `413`; the
  proxy body limit must allow it, #27).
- Preconditions, exactly one: `If-None-Match: *` creates only (`412` if the
  name exists); `If-Match: <ETag>` replaces exactly that revision (`412` with
  the current `ETag` otherwise); `conflict=overwrite|skip|keep_both` (skip
  answers `201` with `skipped: true` and writes nothing; keep_both picks
  `name (1).ext`); none → `428`.
- Optional `X-Docker-Manager-Content-SHA256` (hex) is verified before the file is
  committed (`422 content_digest_mismatch`). The agent checks the
  precondition before storing anything, writes a temporary file in the
  target directory, verifies size and digest, re-checks the precondition and
  renames atomically, so readers never see a partial file. Response `201`
  with the entry metadata and its `ETag`.
- Backpressure: the manager reads the request body only as fast as the agent
  writes it (stream credit); the body is never buffered whole.
- Many files: one request each, or upload an archive and extract it
  (`POST …/files/extractions`, a job).

**Backup content** `GET /backups/{backupId}/contents/download?path=…` streams
one file from a snapshot like a single-file download (`backup.contents.download`;
snapshot contents can hold secrets).

## Audit export (`export-audit-events`)

`GET /audit/exports?format=ndjson|csv&<audit list filters>` streams
`application/x-ndjson` (one `AuditEvent` per line, `details` exactly as
hashed) or `text/csv` (header row; cells starting with `=`, `+`, `-`, `@`,
tab or CR are prefixed with `'` against formula injection), oldest first, as
an attachment (`Content-Disposition`), covering the records present when the
export started (#30). There is no resume; repeat with a narrower time range.
A failure mid-stream aborts the connection instead of ending the body
cleanly. The export requires `audit.export` and is itself audited.

## Internal metrics (`get-system-metrics`)

`GET /system/metrics` answers `text/plain; version=0.0.4` (the Prometheus
text exposition format) with Docker Manager's own metrics: `docker_manager_build_info`,
`docker_manager_jobs{state}` (unfinished jobs), `docker_manager_job_queue_depth`,
`docker_manager_jobs_unfinished_by_kind{kind}`, `docker_manager_agent_sessions`,
`docker_manager_environments{status,online}`, `docker_manager_agents{compatibility}`,
`docker_manager_sse_streams`, `docker_manager_event_bus_subscribers`,
`docker_manager_database_size_bytes{database}`, `docker_manager_audit_chain_records`,
`docker_manager_audit_chain_head_seq`, `go_goroutines` and
`go_memstats_heap_alloc_bytes` (#34). Labels carry only enumerations, never
names chosen by users. It is off by default: `404 not_found` unless the
manager runs with `DOCKER_MANAGER_METRICS_ENABLED=true`. It needs
`system.metrics.read` (instance scope): create an API token with only that
grant for the scraper (`Authorization: Bearer …`). Host and container
metrics are the JSON routes of #5, not this endpoint.

## Support bundle (`get-support-bundle`)

`GET /support-bundle` streams an `application/zip` attachment
(`docker-manager-support-<UTC time>.zip`) for troubleshooting (#34): `README.txt`,
`versions.json`, `configuration.json` (the effective `DOCKER_MANAGER_*` settings;
secrets are files whose paths only are listed), `support-matrix.json`
(per-environment checks against the supported host boundary),
`agents.json`, `audit-chain.json` (the audit hash chain verification),
`jobs.json` (counts per kind and state, unfinished jobs without inputs),
`database.json` (migrations, pre-migration snapshots, sizes) and
`logs.ndjson` (the manager's recent in-memory log lines, redacted again).
It never contains secret values. Owner only (`system.support_bundle`, never
an API token) and audited. A failure mid-stream aborts the connection.
