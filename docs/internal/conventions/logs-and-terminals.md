# Container logs and terminals (#8)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

- Agent `internal/agent/containerio` (`container.logs` request/stream,
  `container.exec.*` requests, `container.exec` stream; scripted Engine
  for tests: `containerio/ciotest`); manager `internal/manager/containerio`
  (log feeds with bounded queues, exec sessions, one-use attach tickets,
  WebSocket relay, limits); API `internal/manager/api/container_io.go`;
  contract `docs/internal/api/streams.md`.
- Terminals are authorized only with `api.AuthorizeExec` (tokens need
  `container.exec` in their own grants); logs need `container.logs.read`.
  Never widen either to metrics/restart/details holders.
- Terminal bytes and log lines are never logged, audited or persisted;
  audit carries session IDs, reasons, close codes and exit codes only
  (`container.exec`, `container.exec.end`).
- Stack service logs are the service containers' logs: clients list them
  with `GET /stacks/{stackId}/services` and follow each container's
  route (the #4 catalog has no stack-level logs route). A future
  aggregate must reuse `ContainerIOService.FollowLogs` per container and
  check `container.logs.read` per container.
