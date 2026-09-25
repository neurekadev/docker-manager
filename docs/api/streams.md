# Streams (`/api/v1`)

OpenAPI documents how each stream is **opened** (path, parameters, auth,
the HTTP errors returned before the stream starts). This document specifies
what happens on the wire afterwards (#4, #23, #27). Every streaming route of
the [route inventory](../../api/route-inventory.yaml) is listed here
(`TestStreamRoutesDocumented`).

| route | operation | transport | owner |
| --- | --- | --- | --- |
| `GET /live/stream` | `stream-live-events` | SSE | #23 |
| `GET /jobs/{jobId}/events/stream` | `stream-job-events` | SSE | #26 (implemented) |
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

Paths are relative to `/api/v1`.

## Rules for every stream

- **Authentication** is the same as for JSON routes: the session cookie
  (browsers; `EventSource` and browser WebSockets send it automatically on the
  same origin) or `Authorization: Bearer <API token>` (other clients). Tokens
  never go into URLs.
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
  WebSockets are pinged every 15 s (`DOCKYARD_STREAM_HEARTBEAT`, one
  implementation each: `internal/manager/server/sse`, `internal/manager/server/ws`).
  Both are shorter than common proxy idle timeouts (#27,
  [deployment.md](../deployment.md#timeouts-and-heartbeats)).
- **Max age:** the server ends SSE streams after 1 h (`event: close`,
  reason `max_age`) so long-lived connections re-authenticate; clients
  reconnect immediately with `Last-Event-ID`.
- **Concurrency limits** (per principal): 8 live streams, 32 SSE streams in
  total, 4 exec sessions (8 per container). Excess opens get `429
  rate_limited`. Browsers should use one live stream per tab and HTTP/2 via
  the proxy (#27).
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

`GET /api/v1/live/stream` is the one multiplexed, permission-filtered,
versioned stream each open UI tab keeps. It carries **invalidations**, never
resource bodies or secrets: the client refetches affected queries with the
generated API client.

Query parameters (all optional): `topics` (comma-separated, default all
topics the caller may see), `environmentId`, `stackId`, `volume` (narrow file
and resource topics to open views). Unknown topics are `422`; topics the
caller may not see simply produce nothing.

Topics: `environments`, `agents`, `containers`, `images`, `volumes`,
`networks`, `stacks`, `jobs`, `files`, `policies`, `backups`, `registries`,
`settings`, `permissions`.

### Events

| event | id | data | client action |
| --- | --- | --- | --- |
| `hello` | — | `{version: "dockyard.live/v1", cursor, heartbeatMs, topics}` | (Re)fetch every open view (the snapshot), then apply events after `cursor` |
| `invalidate` | cursor | `{topic, kind, resourceId, environmentId?, revision?, action: created\|updated\|deleted, at}` | Invalidate queries for that resource and its lists; ignore when `revision` ≤ the cached revision |
| `job` | cursor | `{jobId, state, progressPercent?, at}` | Update job badges; open `/jobs/{jobId}/events/stream` for detail |
| `agent` | cursor | `{environmentId, status: online\|offline\|outdated, at}` | Show connection state; data of an offline environment is stale |
| `files.changed` | cursor | see [file changes](#file-changes) | Invalidate listings; editor conflict handling |
| `permissions.changed` | cursor | `{at}` | Drop **all** cached data, refetch `/me/permissions`, then reconnect; the server closes the stream right after |
| `reset` | — | `{reason: cursor_expired\|gap\|overflow\|server_restart, cursor}` | Discard caches of subscribed topics, refetch (new snapshot), continue from the new `cursor` |
| `close` | — | `{reason: permissions_changed\|session_expired\|max_age\|shutdown}` | Stream ends; reconnect (after re-authentication for `session_expired`) |

- **Snapshot + cursor:** `hello` is sent first and fixes the cursor before
  the client fetches; anything that changes after it arrives as an event, so
  no change falls between snapshot and stream.
- **Replay and gaps:** the manager keeps a bounded replay log (the newest
  10 000 events or 15 minutes). Reconnecting with a `Last-Event-ID` inside
  the log replays the missed events; outside it, or when the manager itself
  lost events (agent reconnect, restart), the stream starts with `reset`.
- **Deduplication:** cursors increase strictly; clients ignore an event whose
  `id` is not above the last one applied. The manager coalesces repeated
  invalidations of one resource within 250 ms and drops duplicate agent
  events (by agent `seq`).
- **Agents offline/reconnecting:** an environment is reported `online` only
  after the manager reconciled it (#3, #26); a `reset` for its topics follows
  so views refetch.
- **Fallback polling:** if the stream cannot be kept (three failed
  connections within 60 s, or a proxy that buffers SSE), clients poll open
  views — at most every 10 s for an open detail view and every 30 s for lists
  and metrics — and stop polling once the stream reconnects. Clients never
  queue mutations while offline (#25 decision 4).

### File changes

External create/modify/rename/delete events in stack and volume roots
(reported by the agent watcher, [agent-v1.md](../protocol/agent-v1.md#fs_invalidation-and-rescan-15-23))
arrive as:

```json
{"scope":{"kind":"stack","id":"0190…"},"paths":["compose.yaml"],"overflow":false,"at":"…"}
```

- Sent only to subscribers with the scope's files-read capability
  (`stack.files.read` / `volume.files.read`); others get nothing — not even
  the path names. Contents are never sent.
- `overflow: true` (or a `reset`) means "refetch the whole listing".
- An open editor whose file is in `paths` keeps the buffer, shows the
  external change, and saves only with `If-Match` of the version it loaded;
  a stale save gets `412` and the UI offers compare / reload / save-as /
  overwrite (#15). Unsaved text is never replaced silently.

## Job events (`stream-job-events`)

Implemented (#26). First `event: job` with the full `Job` (no id), then
every retained event after `Last-Event-ID`: `id: <seq>`, `event: state |
progress | item | log | warning`, `data: JobEvent`. The stream closes after
the job's terminal events, or when the job is deleted by retention or the
caller loses `job.read`. The per-job event log is bounded (newest 500), so
replay covers only retained events; `Last-Event-ID` must be a non-negative
integer (`422` otherwise).

## Environment and stack events

`stream-environment-events` and `stream-stack-events` relay Docker Engine
events (container/image/volume/network lifecycle, health) for one
environment or one stack, filtered per resource capability. Framing as
above; `event: engine`, `data: {type, action, resourceId, attributes, at}`,
`id` is a per-environment cursor with a bounded in-memory replay (1 000
events); outside it the stream starts with `reset`. Attributes are an
allowlist (no environment variables or secret labels).

## Container logs (`stream-container-logs`)

`GET …/logs/stream?tail=200&since=<RFC 3339>&stdout=true&stderr=true&timestamps=true`

- `event: log`, `id: <RFC 3339 nano timestamp>`, `data: {at, stream:
  stdout|stderr, line, partial?}`. Lines longer than 16 KiB are split
  (`partial: true` on all but the last piece). Reconnect with
  `Last-Event-ID` resumes after that timestamp (Docker `since`), which may
  repeat lines with identical timestamps; clients deduplicate by `(id, line)`.
- `event: dropped {count}` when the client could not keep up.
- `event: end {reason: container_removed | permissions_changed | agent_offline}`
  ends the stream. A stopped container keeps the stream open (it may restart).
- Log lines can contain secrets; they are never persisted by the manager or
  cached by the browser. `GET …/logs` (JSON) returns a bounded tail (default
  500, max 5 000 lines).

## Container exec (`stream-container-exec-session`)

1. `POST …/containers/{containerId}/exec-sessions` with `{command: [argv…],
   tty: true, cols, rows, workingDir?, user?}` → `201 {id, streamUrl,
   expiresAt}`. The session must be attached within 60 s.
2. `GET …/exec-sessions/{sessionId}/stream` upgrades to a WebSocket with
   subprotocol `dockyard.exec.v1`. Cookie-authenticated upgrades must carry
   an `Origin` equal to the manager's public origin (`403` otherwise).
   Exactly one attachment per session (`4409` for a second).

Messages:

| direction | message | meaning |
| --- | --- | --- |
| client → server | binary, first byte `0` | stdin bytes |
| server → client | binary, first byte `1` / `2` | stdout / stderr bytes (with a TTY everything is `1`) |
| client → server | text `{"type":"resize","cols":120,"rows":40}` | terminal size |
| server → client | text `{"type":"exit","code":0}` | the process exited; a normal close follows |
| server → client | text `{"type":"error","code":"…","message":"…"}` | failure; a close follows |

- Binary messages are at most 64 KiB (`1009` otherwise). The server buffers
  at most 1 MiB per direction and otherwise slows the agent stream.
- Idle timeout 30 min without input or output (`4408`); sessions end when the
  user signs out or loses `container.exec`.
- `DELETE …/exec-sessions/{sessionId}` detaches and closes stdin. Processes
  that ignore end-of-input keep running inside the container until they exit
  (Engine exec has no kill).
- Terminal input and output are never logged or stored.

Close codes:

| code | meaning | client |
| --- | --- | --- |
| 1000 | process exited (after `exit`) | show exit code |
| 1001 | manager shutting down | offer reconnect (new session) |
| 1008 | policy violation (bad Origin or subprotocol) | — |
| 1009 | message too big | — |
| 1011 | internal error | offer retry |
| 4401 | session expired / unauthenticated | sign in |
| 4403 | `container.exec` revoked | — |
| 4404 | exec session unknown, expired or ended | create a new session |
| 4408 | idle timeout | create a new session |
| 4409 | already attached | — |
| 4429 | too many sessions | close another |
| 4503 | environment offline (agent disconnected) | wait for the agent |

## File downloads and uploads (#15)

Path encoding for every scoped file route: `path` query parameters are
root-relative, slash-separated, percent-encoded UTF-8; no leading `/`, no
`.`/`..` segments, no NUL, at most 4 096 bytes. The manager cleans and checks
them, the agent re-checks containment beneath the root without following
symlinks out of it; violations are `422` with a `query.path` detail. Raw
host paths never appear in responses.

**Download** `GET …/files/downloads?path=a/b.txt[&path=…][&format=zip|tar.gz]`

- One regular file: raw bytes, `Content-Type: application/octet-stream`,
  `Content-Disposition: attachment; filename*=UTF-8''…`, `Content-Length`,
  `ETag`; supports single `Range` requests (`206`) for resuming.
- Several paths or a directory: a streamed archive (`zip` by default), no
  `Content-Length`, no `Range`. Symlinks are stored only when they resolve
  inside the root; others are skipped and listed in a final
  `DOCKYARD-SKIPPED.txt` entry.
- Limits: total bytes `DOCKYARD_FILES_MAX_DOWNLOAD` (default 10 GiB), 100 000
  entries. A failure after the first byte aborts the connection (the client
  sees a truncated download and must not treat it as complete; archives then
  lack their end record).

**Upload** `POST …/files/uploads?path=dir&name=file.txt`

- Body: raw bytes, `Content-Type: application/octet-stream`,
  `Content-Length` required (`411`), at most `DOCKYARD_FILES_MAX_UPLOAD`
  (default 2 GiB, `413`; the proxy body limit must allow it, #27).
- Preconditions: `If-None-Match: *` creates only (`412` if it exists);
  `If-Match: <ETag>` replaces exactly that revision; neither → `428`.
- Optional `X-DockYard-Content-SHA256` is verified before the file is
  committed (`422` on mismatch). The agent writes to a temporary file in the
  target directory and renames atomically, so readers never see a partial
  file. Response `201` with the entry metadata and its `ETag`.
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
