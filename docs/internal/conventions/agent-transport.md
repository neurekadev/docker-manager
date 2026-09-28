# Agent transport (#3)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Manager side: `internal/manager/agents` (`Service`: enrollment, agents,
environments; `Hub`: live sessions). Agent side: `internal/agent/session`
(outbound session client), `internal/agent/state` (install ID, credential,
handed-over tokens), `internal/agent/runtime` (control loop). Protocol:
`docs/internal/protocol/agent-v1.md`.

- **Call an agent** (named, bounded, non-job operation):
  `out, err := hub.RequestEnvironment(ctx, envID, protocol.ReqContainerList, input, 0)`.
  Errors: `jobs.ErrAgentOffline` (map to 503 `unavailable`),
  `*agents.RequestError{Code}` (agent `error` frame; map per the protocol
  doc), `agents.ErrRequestTimeout` (504 `timeout`). Never retry mutating
  requests automatically. The request name must be in
  `protocol.RequestNames()` and advertised in the agent's capabilities.
- **Serve a request on the agent:** add a `session.RequestHandler` to
  `runtime.Options.Requests` (keyed by request name); return output (JSON
  encoded) or `&session.HandlerError{Code: protocol.CodeNotFound, ...}` (an alias of
  `protocol.Error`, which shared packages such as `internal/fsroot` return).
  The handler's ctx ends at the request deadline; at most 16 run at once per
  session. The session advertises every registered name in the
  capabilities' `requests`.
- **Jobs:** do not talk to agents for job work; enqueue in the job engine.
  `Hub` is the engine's `jobs.AgentDispatcher`; agent executors go in
  `runtime.Options.Executors`.
- **Reconcile after reconnect:** inventory owners register
  `hub.AddReconciler(func(ctx, s *agents.Session) error)` (re-read what
  changed while the agent was away). The environment is reported online only
  after every reconciler returned.
- **Events and file invalidations:** the agent publishes with
  `client.Events().Publish(protocol.EventPayload{...})` /
  `client.FileInvalidations().Publish(...)` (per-session `seq`, drops count
  as gaps); the watcher (`internal/agent/watch`) publishes external file
  changes and answers `rescan` (`hub.RescanEnvironment`). The manager republishes them on the in-process bus
  (`internal/manager/events`, `Bus.Subscribe`) as `docker.event` /
  `files.invalidated`, plus `environment.resync` after reconnects and gaps;
  environment/agent/enrollment changes are published there too (#23
  consumes the bus). File-scope paths on the bus are internal: filter by the
  reader's file permissions before anything leaves the manager.
- **Last seen:** an agent's and its environment's `lastSeenAt` are written
  on connect, capabilities and disconnect, and refreshed while the session
  lives: inbound frames (at least one heartbeat per 15 s) are persisted at
  most once per `agents.LastSeenRefresh` (60 s) per session, off the read
  loop (the watchdog starts the write on its own goroutine with a 5 s
  deadline; `store.TouchLastSeen` only moves the time forward, only for the
  agent's current session, and leaves revision and `updated_at` alone). No
  bus event is published for a refresh; a failed refresh warns once per
  series of failures. Do not add per-heartbeat database writes.
- **Identity:** agent ID ≠ Engine ID; `(engineId, installId)` identifies an
  installation; one active agent per Engine and per environment. Agent
  secrets are `dye_…` (enrollment) / `dya_…` (credential), minted with
  `authsep.Mint*`, stored as verifiers only, never logged.
