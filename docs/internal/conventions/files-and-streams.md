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
- **Files:** the operations live in `internal/fsroot` (shared by agent and
  manager, no agent or manager imports): every filesystem access goes
  through an `os.Root` on the scope root, recursive walks never follow
  symlinks, content of multiply-linked files is refused, errors are
  `*protocol.Error` naming root-relative paths. A caller supplies the
  `fsroot.Resolver` that decides which directory a scope maps to (and
  refuses the rest) and the job `Kinds` its executors serve. Agent
  `internal/agent/files` resolves stack and volume scopes (verified stack
  roots, local volumes, never Docker Manager's own volumes) and wires the
  session requests, streams and `files.*` executors; manager
  `internal/manager/files` (`app.Manager.Files()`; #7 installs its stack
  root resolver and Compose-source observer with `SetStacks`); API
  `internal/manager/api/files.go` (checks `<root>.files.*` and
  `stack.definition.*`); shared types `internal/protocol/files.go`; contract
  `docs/internal/api/files.md`. Never log file contents or put them in audit details.
- **Limits:** the file manager's size and entry limits are the manager's
  `DOCKER_MANAGER_FILES_*` configuration (`config.Config.Files`,
  `domain.FileLimits`). `files.Service.Limits(root)` is the limit in
  effect for a root (configured, an older agent's defaults, a template's);
  enforce edit and upload limits in the API from it, send agent-side
  limits as `protocol.FileLimits` only to agents with
  `protocol.FeatureFileLimits`, and let `fsroot` apply them per operation
  (`limitsFor`, capped by `protocol.MaxFileLimit*`). The listing reports
  them (`FileListing.limits`); the web client never hard-codes a limit.
  Agents' `files.read`/`files.write` stay at `protocol.MaxInlineContent`
  (one frame): the manager reads and saves larger editable files through
  the streams. Contract: `docs/internal/api/files.md#limits`.
- **Template drafts** are a third root served by the manager itself: the
  template service's `fsroot` instance (`files.Service.SetTemplates`),
  `template.files.*` manager jobs, `files.invalidated` events with
  `scopeKind=template`. See [templates.md](templates.md).
