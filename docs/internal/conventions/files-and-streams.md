# Byte streams and scoped files (#15)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

- **Streams** (logs, exec, file transfers, migrations): the manager opens
  them with `hub.OpenStream(ctx, envID, protocol.StreamX, input,
  streammux.OpenOptions{MaxBytes: n})` and reads/writes the returned
  `*streammux.Stream` (credit flow control, `CloseWrite` = half close,
  `Result` = the agent's final close result, `Abort` on failure; errors:
  `*streammux.CloseError{Code}` from the agent, `streammux.ErrSessionClosed`
  when the session ends). Agents serve kinds with a `session.StreamHandler`
  in `runtime.Options.Streams`. Implementation `internal/streammux` (shared
  by both ends); in-memory pairs for tests: `streammux/muxtest.New`.
- **Files:** agent `internal/agent/files` — every filesystem access goes
  through an `os.Root` on the scope root, recursive walks never follow
  symlinks, content of multiply-linked files is refused; manager
  `internal/manager/files` (`app.Manager.Files()`; #7 installs its stack
  root resolver and Compose-source observer with `SetStacks`); API
  `internal/manager/api/files.go` (checks `<root>.files.*` and
  `stack.definition.*`); shared types `internal/protocol/files.go`; contract
  `docs/internal/api/files.md`. Never log file contents or put them in audit details.
