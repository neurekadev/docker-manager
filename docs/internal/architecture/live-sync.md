# Live synchronization (#23)

Every open view converges on the manager's and agents' current state
without a manual reload, including stack and volume file browsers when a
file changes outside Docker Manager. Wire contracts:
[streams.md](../api/streams.md#live-invalidation-stream-23) (browser) and
[agent-v1.md](../protocol/agent-v1.md#fs_invalidation-and-rescan-15-23)
(agent). Decisions: #25 Q1 (on-disk source of truth), Q4 (no offline
data), Q5 (2 s p95 local, 60 s remote).

```
agent                                   manager                                        browser tab
─────                                   ───────                                        ───────────
Docker events ──event (seq)──────────► agents.Session ─┐
watch.Watcher ──fs_invalidation (seq)► (dedupe, gaps)  ├─► events.Bus ─► live.Hub ─► GET /live/stream ─► $lib/live
  ▲  files.watch / rescan                               │      ▲  ▲       (coalesce,   (filter per user,    (invalidate
  └──────────────────────────────── files.Watcher ◄─────┤      │  │        replay,      shape, resume,       Svelte Query
                                    (watch set,         │      │  │        resets)      heartbeat, close)    keys, polling)
                                     external edits ──► stacks.ExternalChange ─► revision
                                                        │      │  └─ jobs.Engine.OnChange ─► live.JobSource
                                                        │      └──── API mutations (resource.changed)
```

## Sources on the bus (`internal/manager/events`)

| source | bus events |
| --- | --- |
| agent sessions (#3, #5) | `docker.event`, `environment.*` (online after reconciliation, `resync` after reconnects and event gaps), `agent.*`, `enrollment.*`, `files.invalidated` (a whole-environment overflow after an fs `seq` gap) |
| observation (#5) | `metrics.sampled` (stored samples, every 10 s), `metrics.live` (live CPU and memory in memory, about every second per environment while at least one live stream is open: the hub's subscriber count is the manager's demand signal, [metrics.md](metrics.md#live-metrics)), `inventory.updated` (also when a disk health report changes, attribute `health`, [metrics.md](metrics.md#host-health)) |
| stacks (#7) | `stack.created/updated/removed/revision_recorded` |
| jobs (#26) | `job.updated` from `live.JobSource` (engine change listener, batched per 250 ms, one database read per job) |
| API mutations (#30 audit path) | `resource.changed` for every target of a successful non-GET operation (policies, schedules, backups, registries, Git credentials, build definitions, settings, groups, users, invitations, API tokens); file operations and job targets are left to their precise sources |
| manager move ([manager-move.md](manager-move.md), "Live updates") | `manager_move.updated` (the move's ID: every change of the move here, the new server's agent enrolling or going on- or offline and Move Everything's job heard on the bus, a check-in that starts or stops counting, the new manager's confirmation attempts) and `manager_move.lock_changed` (no ID: the session's move lock changed) |
| alerts ([alerts.md](alerts.md)) | `alert.updated` (the alert's ID; raised, changed, dismissed or resolved, published after the commit), `notification.created` (a finished run's notification, after its job's commit) |

Every event type has a visibility rule in `internal/manager/authz/events.go`
(`TestEveryEventTypeHasAVisibilityRule`); `job.updated` carries the job for
`job.read` on its targets, `resource.changed` uses the catalog type's view
(owner-only for users, groups, tokens and other non-catalog types),
`manager_move.updated` needs `manager.move` (the owner) and
`manager_move.lock_changed` reaches every signed-in stream (it names no
move, like the lock in `GET /auth/session`), `alert.updated` carries
the alert for `authz.AlertVisible` (whoever sees its source) and
`notification.created` the notification for `authz.NotificationVisible`
(`job.read` on its job).

## The hub (`internal/manager/live`)

- One bus subscription (8 192 buffered); a loss becomes a `reset gap`
  record for every stream, an environment resync a `reset gap` scoped to
  that environment.
- Coalescing per resource (type, resource, environment): first event of a
  250 ms window immediately, the rest merged (paths united, members united)
  and emitted when the window ends; stored and live metrics use 1 s per
  environment.
- Records get a strictly increasing sequence; the replay log keeps the
  newest 10 000 or 15 minutes. Cursors are `<epoch>.<seq>`; the epoch
  changes with every manager process. `metrics.live` records are fanned
  out but not kept for replay (one per environment and second would push
  everything else out); a cursor is expired only when a retained record
  after it was dropped.
- Subscribers have 512-record queues; overflow drains the queue and the
  stream sends `reset overflow` with a fresh cursor. At most 32 streams per
  principal (one per open tab); the web client names the limit in its
  banner when a connection is refused with 429.

The stream handler (`internal/manager/api/live.go`) builds one checker per
stream, filters every record with `authz.EventVisible`, shapes it to
identity and action (no attributes), applies topic, environment and file
scope filters, and ends with `permissions.changed` + `close` when the
identity layer cancels the request for a permission change
(`auth.Service.AccessChanged`). Volume filters hold the volume in the
agent's watch set while the stream is open.

## File watching

Manager (`internal/manager/files`, `Watcher`): the watch set of an
environment is every stack (`stacks.Service.WatchScopes`) plus held or
leased volumes; it is pushed with `files.watch` when it changes and after
every reconnect. Stack-scope invalidations touching a Compose, override or
env file (or a directory holding one, or the whole scope) are settled for
1 s and passed to `stacks.Service.ExternalChange`, which reads the
definition from disk and records a revision (source `external`) only when
the bytes differ — the file manager's own saves record theirs first
(`RecordFileSave`), so the settle mostly finds nothing new. After an fs
`seq` gap every stack scope is rescanned (bounded) and definition changes
are recorded the same way.

Agent (`internal/agent/watch`): scopes are resolved with the file service's
checks (`files.Service.ScopeDir`: verified stack roots, supported local
volumes, symlink-free). inotify mode registers one watch per directory
while walking the tree (the watch before the directory is read), adds new
directories, drops renamed and removed ones, never follows symlinks and
re-checks that a directory is still real right before and after adding its
watch. Every watch counts against one budget; scopes that do not fit, remote
filesystems and agents without kernel notifications are polled with
bounded reconciliation scans (per-directory hashes, 30 s). inotify scopes
are reconciled every 10 minutes and after a kernel queue overflow. Budgets
and measurements: [support-matrix.md](../support-matrix.md#file-watching-23).

## Browser (`web/src/lib/live`)

One `EventSource` per tab; see [web.md](../web.md#live-data-23) for the
query-key conventions, `liveStatus`, the critical-work registry the PWA
update prompt consults, and the polling fallback.

## Tests

| what | tests |
| --- | --- |
| snapshot, cursor resume, expired/foreign cursors, replay, coalescing, overflow resets, bus-loss and environment resets, stream limit | `internal/manager/live` (`TestSnapshotCursorAndReplay`, `TestCoalescingAndDedupe`, `TestLiveMetricsAreNotReplayed`, `TestSlowSubscriberOverflowReset`, `TestLossesBecomeResets`, `TestStreamsPerPrincipal`), `internal/manager/api` (`TestLiveStreamSnapshotResumeAndReset`) |
| permission filtering and shaping (metrics-only, files, jobs, policies) | `TestLiveEventFiltering`, `TestEventVisibility`, `TestLiveStreamThroughTheRealManager` |
| revocation closes the stream | `TestLiveStreamRevocation`, `TestLiveStreamThroughTheRealManager` (real `AccessChanged`) |
| two sessions converge, high volume | `TestTwoSessionsConverge`, `TestLiveHighEventVolume`, `TestLiveSustainedVolumeIsLossless` |
| agent watcher: create/edit/rename/delete, debounce, overflow, missed events (notifications disabled), watch limit, symlink escape, rescan | `internal/agent/watch` |
| real-filesystem latency (p95) | `TestRealFilesystemLatency` |
| external compose.yaml edit → revision (real fsnotify through the manager) | `TestExternalComposeEditRecordsRevision`, `TestExternalChangeAndWatchScopes`, `TestExternalChangesSettleAndGapsRescan`, `TestWatchSetFollowsStacksAndOpenVolumes` |
| unsaved editor buffer: stale save refused with the current ETag | #15: `TestVolumeFilesThroughTheAPI` (412 with the current ETag), `TestReadWriteETagAndConflicts`; the editor's side in `ConflictDialog.test.ts` / `EditorPane.test.ts` (jsdom). Two real browser sessions are not verified by automated tests (the Playwright spec was removed on 2026-09-25) |
| browser client | `web/src/lib/live/*.spec.ts`, `web/src/lib/pwa/register.spec.ts` |
