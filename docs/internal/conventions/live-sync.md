# Live synchronization (#23)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/live-sync.md`. Manager `internal/manager/live`
(`app.Manager.Live()`: hub, `JobSource`), stream handler
`internal/manager/api/live.go` (`GET /live/stream`), watch set and external
edits `internal/manager/files` (`Watcher`, `app.Manager.FileWatch()`);
agent watcher `internal/agent/watch`; browser `web/src/lib/live`.

- **Publish changes on the bus**, never to streams directly: anything a
  view shows must reach `events.Bus` (a new event type needs a visibility
  rule in `authz/events.go` and a topic in `live.Classify`). Successful
  audited API mutations are published automatically as
  `events.ResourceChanged` (targets from the path and `audit.AddTarget`);
  jobs through `jobs.Engine.OnChange`; do not add a second path for them.
- Events carry IDs, kinds and revisions only: the stream never sends
  attributes, bodies, file contents or secrets (nor metric values: views
  refetch them); file paths only reach holders of the scope's files-read
  capability. The one exception is the `job` event: for `job.read`
  holders it also carries the job's targets, policy and progress (step,
  message, percent), as `GET /jobs/{jobId}` shows them, never its input
  (`LiveJob`, `newLiveJob` in `api/live.go`). High-rate kinds
  (`metrics.live`, about one per environment and second) coalesce per
  environment and are not kept in the replay log.
- **Stacks:** external edits of definition files become revisions through
  `stacks.Service.ExternalChange` (settled by the watcher); the agent's
  watch set of stacks comes from `WatchScopes`.
- **Web views:** key queries with `liveKeys` (`$lib/live/keys.ts`),
  declare open file views with `liveClient()?.setScopes(...)`, register
  unsaved edits, terminals, restores and uploads with
  `criticalWork.register(...)` (the PWA update prompt will not reload
  while any is open), read connection state from `liveStatus`. Editors keep
  their buffer when a refetched `etag` differs and save with `If-Match`.
  Current CPU and memory come from the `containers-latest`, `capacity` and
  overview queries (refreshed by `live_metrics` about every second), not
  from a chart's last point; queries the stream keeps current poll only
  through `pollWhileDown(ms)`.
- **Notification channels** (#142) are announced as `resource.changed`
  with resource type `notification_channel` on the topic `settings`
  (owner only: `notification_channel.manage`); the web keys them with
  `notificationKeys` (`liveKeys.list('settings', 'notification-channels')`).
  A test's new last result arrives the same way (the test is an audited
  POST).
- **Alerts** (#159) are published as `alert.updated` (carrying the alert:
  `authz.AlertVisible` decides who receives it) after the change is
  committed (job hooks: read back after `OnChange`); topic `alerts`, kind `alert`.
  The audited dismissal's own `resource.changed` (type `alert`) reaches
  nobody. The web keys every alerts query with `liveKeys.alerts(...)`
  (`['alerts', 'list', …]`), so the bell, the Notifications page's Alerts
  tab, the dashboard and the environment page refresh together.
  Notifications (finished runs) are published as `notification.created`
  (carrying the notification: `authz.NotificationVisible`), topic
  `alerts`, kind `notification`, once their job's change is committed;
  the web keys notification lists `['notifications', 'list', …]` and
  refreshes only those on it.
- **Manager move:** the move service publishes `manager_move.updated`
  (owner, the move's ID) on every change and `manager_move.lock_changed`
  (everyone, no ID) when the session's lock changes (`managermove/live.go`,
  `published`); what the move's view shows from elsewhere (the new
  server's agent, Move everything's job) is followed on the bus
  (`followBus`), never polled. Web keys: `liveKeys.managerMove(...)` and
  `liveKeys.session` (topic `manager`).
- **Agents:** `files.watch`, `rescan` and `metrics.live` reach only agents
  that serve them (`Session.Serves` / `Hub.EnvironmentServes`; an N-1 agent
  would close the session on an unknown request name).
